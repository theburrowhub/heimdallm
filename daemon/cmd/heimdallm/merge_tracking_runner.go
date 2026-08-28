package main

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/heimdallm/daemon/internal/config"
	"github.com/heimdallm/daemon/internal/executor"
	gh "github.com/heimdallm/daemon/internal/github"
	issuepipeline "github.com/heimdallm/daemon/internal/issues"
	"github.com/heimdallm/daemon/internal/mergetrack"
	"github.com/heimdallm/daemon/internal/repoctx"
)

// mergeTrackWorktreeRunner performs the two merge-tracking actions that need a
// local checkout.
//
// It exists in main because worktree acquisition needs repoctx and the
// per-repo AI config, neither of which belongs in the reconciler. Every run
// takes an ephemeral, Heimdallm-managed worktree — never the operator's own
// checkout, which repoctx.ModeWrite already refuses to hand out for
// local_dir_base mounts.
type mergeTrackWorktreeRunner struct {
	repoCtx  *repoctx.Manager
	resolver *mergetrack.ConflictResolver
	git      mergetrack.GitOps
	token    string

	cfg   **config.Config
	cfgMu *sync.Mutex
}

// ResolveConflicts reserves a worktree and runs the agent-driven resolution.
func (m *mergeTrackWorktreeRunner) ResolveConflicts(ctx context.Context, req mergetrack.ConflictRequest) (mergetrack.ConflictResult, error) {
	handle, opts, err := m.prepare(ctx, req, "merge-conflict")
	if err != nil {
		return mergetrack.ConflictResult{}, err
	}
	defer handle.Release()

	req.Token = m.token
	req.ExecOpts = opts.exec
	req.CLIPrimary = opts.primary
	req.CLIFallback = opts.fallback
	return m.resolver.Resolve(ctx, req)
}

// RebaseAndForcePush is the fallback for a branch GitHub refuses to update with
// a merge commit — a base that requires linear history, typically. No agent is
// involved: if the rebase conflicts, this reports it and the conflict-resolution
// path (a separate, separately-enabled toggle) is what handles that.
func (m *mergeTrackWorktreeRunner) RebaseAndForcePush(ctx context.Context, req mergetrack.ConflictRequest) (string, error) {
	handle, _, err := m.prepare(ctx, req, "merge-update")
	if err != nil {
		return "", err
	}
	defer handle.Release()
	dir := handle.Path()

	preSHA, err := m.git.CheckoutRemoteBranch(ctx, dir, req.Repo, req.HeadRef, m.token)
	if err != nil {
		return "", fmt.Errorf("merge tracking: checkout %s: %w", req.HeadRef, err)
	}
	if req.ExpectedRemoteHeadSHA != "" && preSHA != req.ExpectedRemoteHeadSHA {
		return "", fmt.Errorf("merge tracking: %w", mergetrack.ErrBranchMoved)
	}

	baseSHA, err := m.git.FetchRef(ctx, dir, req.Repo, req.BaseRef, m.token)
	if err != nil {
		return "", fmt.Errorf("merge tracking: fetch %s: %w", req.BaseRef, err)
	}

	outcome, err := m.git.RebaseOnto(ctx, dir, baseSHA)
	if err != nil {
		return "", fmt.Errorf("merge tracking: rebase %s onto %s: %w", req.HeadRef, req.BaseRef, err)
	}
	if !outcome.Clean {
		_ = m.git.AbortRebase(ctx, dir)
		return "", fmt.Errorf("merge tracking: rebase of %s onto %s conflicts in %d file(s); enable resolve_conflicts to have the agent handle it",
			req.HeadRef, req.BaseRef, len(outcome.Conflicts))
	}

	newSHA, err := m.git.HeadSHA(ctx, dir)
	if err != nil {
		return "", fmt.Errorf("merge tracking: read rebased head: %w", err)
	}
	// Leased to the SHA we checked out, so a push that landed meanwhile makes
	// git refuse rather than overwrite it.
	if err := m.git.PushForceWithLease(ctx, dir, req.Repo, req.HeadRef, preSHA, m.token); err != nil {
		return "", fmt.Errorf("merge tracking: force-push %s: %w", req.HeadRef, err)
	}
	slog.Info("merge tracking: branch rebased locally",
		"repo", req.Repo, "pr", req.PRNumber, "branch", req.HeadRef,
		"pre_rebase_sha", preSHA, "new_head_sha", newSHA)
	return newSHA, nil
}

type mergeTrackRunOpts struct {
	exec              executor.ExecOptions
	primary, fallback string
}

// prepare reserves a worktree and resolves the per-repo agent configuration.
func (m *mergeTrackWorktreeRunner) prepare(ctx context.Context, req mergetrack.ConflictRequest, tokenPrefix string) (*repoctx.Handle, mergeTrackRunOpts, error) {
	m.cfgMu.Lock()
	c := *m.cfg
	aiCfg := c.AIForRepo(req.Repo)
	localDirBase := append([]string(nil), c.GitHub.LocalDirBase...)
	mt := c.MergeTrackingForRepo(req.Repo)
	globalPrimary, globalFallback := c.AI.Primary, c.AI.Fallback
	m.cfgMu.Unlock()

	primary := aiCfg.Primary
	if primary == "" {
		primary = globalPrimary
	}
	fallback := aiCfg.Fallback
	if fallback == "" {
		fallback = globalFallback
	}

	handle, err := acquireRepoContext(ctx, m.repoCtx, req.Repo, &aiCfg, localDirBase, m.token,
		repoctx.ModeWrite, wtTokenFor(tokenPrefix, req.PRNumber), "", "")
	if err != nil {
		return nil, mergeTrackRunOpts{}, fmt.Errorf("merge tracking: reserve worktree: %w", err)
	}
	// A rebase needs real history; a shallow clone would fail on the first
	// replay of a commit older than the shallow boundary.
	ensureRepoContextFullHistory(ctx, m.repoCtx, handle, m.token, "merge tracking", req.Repo)

	opts := executor.ExecOptions{WorkDir: handle.Path()}
	if d, err := time.ParseDuration(mt.ResolveTimeout); err == nil && d > 0 {
		opts.Timeout = d
	}
	opts.Effort = mt.ResolveEffort

	return handle, mergeTrackRunOpts{exec: opts, primary: primary, fallback: fallback}, nil
}

// Compile-time check.
var _ mergetrack.WorktreeRunner = (*mergeTrackWorktreeRunner)(nil)

// mergeTrackGateway adapts *gh.Client to the reconciler's Gateway.
//
// A thin adapter rather than the client directly, so the reconciler's
// dependency stays a small, fakeable interface instead of a 1700-line client.
type mergeTrackGateway struct{ c *gh.Client }

func (g mergeTrackGateway) FetchMergeTrackingPRs(includeAssigned bool) ([]*gh.TrackedPR, error) {
	return g.c.FetchMergeTrackingPRs(includeAssigned)
}

func (g mergeTrackGateway) GetMergeStatus(repo string, number int) (*gh.MergeStatus, error) {
	return g.c.GetMergeStatus(repo, number)
}

func (g mergeTrackGateway) EnableAutoMerge(nodeID, method, expectedHeadOid string) (*gh.AutoMergeRequest, error) {
	return g.c.EnableAutoMerge(nodeID, method, expectedHeadOid)
}

func (g mergeTrackGateway) DisableAutoMerge(nodeID string) error {
	return g.c.DisableAutoMerge(nodeID)
}

func (g mergeTrackGateway) UpdatePRBranch(repo string, number int, expectedHeadSHA string) (gh.UpdateBranchOutcome, error) {
	return g.c.UpdatePRBranch(repo, number, expectedHeadSHA)
}

func (g mergeTrackGateway) MergePRAtSHA(repo string, number int, method, expectedHeadSHA string) (gh.MergeOutcome, error) {
	return g.c.MergePRAtSHA(repo, number, method, expectedHeadSHA)
}

func (g mergeTrackGateway) PostComment(repo string, number int, body string) (time.Time, error) {
	return g.c.PostComment(repo, number, body)
}

var _ mergetrack.Gateway = mergeTrackGateway{}

// mergeTrackExecutor adapts *executor.Executor to the resolver's CLIExecutor.
type mergeTrackExecutor struct{ e *executor.Executor }

func (m mergeTrackExecutor) Detect(primary, fallback string) (string, error) {
	return m.e.Detect(primary, fallback)
}

func (m mergeTrackExecutor) ExecuteRaw(cli, prompt string, opts executor.ExecOptions) ([]byte, error) {
	return m.e.ExecuteRaw(cli, prompt, opts)
}

var _ mergetrack.CLIExecutor = mergeTrackExecutor{}

// mergeTrackGitAdapter narrows issuepipeline.GitExec to the resolver's GitOps.
// A compile-time assertion rather than an adapter would be nicer, but GitExec
// also carries the issue-pipeline methods, so naming the subset here keeps the
// merge-tracking dependency honest.
type mergeTrackGitAdapter struct{ g *issuepipeline.GitExec }

func (a mergeTrackGitAdapter) CheckoutRemoteBranch(ctx context.Context, dir, repo, branch, token string) (string, error) {
	return a.g.CheckoutRemoteBranch(ctx, dir, repo, branch, token)
}

func (a mergeTrackGitAdapter) FetchRef(ctx context.Context, dir, repo, ref, token string) (string, error) {
	return a.g.FetchRef(ctx, dir, repo, ref, token)
}

func (a mergeTrackGitAdapter) RebaseOnto(ctx context.Context, dir, ontoSHA string) (issuepipeline.RebaseOutcome, error) {
	return a.g.RebaseOnto(ctx, dir, ontoSHA)
}

func (a mergeTrackGitAdapter) ConflictedFiles(ctx context.Context, dir string) ([]string, error) {
	return a.g.ConflictedFiles(ctx, dir)
}

func (a mergeTrackGitAdapter) HasUnmergedPaths(ctx context.Context, dir string) (bool, error) {
	return a.g.HasUnmergedPaths(ctx, dir)
}

func (a mergeTrackGitAdapter) ChangedFiles(ctx context.Context, dir, sinceSHA string) ([]string, error) {
	return a.g.ChangedFiles(ctx, dir, sinceSHA)
}

func (a mergeTrackGitAdapter) FilesWithConflictMarkers(ctx context.Context, dir string, paths []string) ([]string, error) {
	return a.g.FilesWithConflictMarkers(ctx, dir, paths)
}

func (a mergeTrackGitAdapter) StageAll(ctx context.Context, dir string) error {
	return a.g.StageAll(ctx, dir)
}

func (a mergeTrackGitAdapter) ContinueRebase(ctx context.Context, dir string) error {
	return a.g.ContinueRebase(ctx, dir)
}

func (a mergeTrackGitAdapter) AbortRebase(ctx context.Context, dir string) error {
	return a.g.AbortRebase(ctx, dir)
}

func (a mergeTrackGitAdapter) HeadSHA(ctx context.Context, dir string) (string, error) {
	return a.g.HeadSHA(ctx, dir)
}

func (a mergeTrackGitAdapter) PushForceWithLease(ctx context.Context, dir, repo, branch, expectedRemoteSHA, token string) error {
	return a.g.PushForceWithLease(ctx, dir, repo, branch, expectedRemoteSHA, token)
}

var _ mergetrack.GitOps = mergeTrackGitAdapter{}

// mergeTrackInterval resolves the reconciler's cadence, falling back to the
// shared poll interval when [merge_tracking].poll_interval is unset.
func mergeTrackInterval(c *config.Config, fallback time.Duration) time.Duration {
	if raw := c.MergeTracking.PollInterval; raw != "" {
		if d, err := time.ParseDuration(raw); err == nil && d > 0 {
			return d
		}
	}
	return fallback
}
