package mergetrack

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/heimdallm/daemon/internal/executor"
)

// Worktree is a reserved checkout Heimdallm may mutate. Backed by
// repoctx.Handle in production.
type Worktree interface {
	// Path is the directory to run git and the agent in.
	Path() string
	// Release frees the per-repo reservation. Safe to call more than once.
	Release()
}

// RepoContexts reserves ephemeral, writable worktrees.
//
// Declared here rather than taking *repoctx.Manager so this package stays free
// of repoctx and of the per-repo AI config — and, more to the point, so the
// logic below is reachable from a test. The production implementation lives in
// cmd, where repoctx and the config are wired.
type RepoContexts interface {
	// AcquireWrite reserves a worktree for repo under a deterministic token, so
	// a retry lands on the same path and two concurrent runs never collide.
	// Callers must Release the returned worktree.
	AcquireWrite(ctx context.Context, repo, worktreeToken string) (Worktree, error)
}

// AgentSpec is the per-repo agent configuration for one run.
type AgentSpec struct {
	Primary  string
	Fallback string
	Timeout  time.Duration
	Effort   string
}

// WorktreeOps performs the two merge-tracking actions that need a local
// checkout: resolving conflicts with the agent, and rebasing a branch GitHub
// refused to update.
type WorktreeOps struct {
	repos    RepoContexts
	git      GitOps
	resolver *ConflictResolver
	token    string
	specFor  func(repo string) AgentSpec
}

// NewWorktreeOps builds the runner. specFor may be nil, in which case the agent
// runs with the executor's own defaults.
func NewWorktreeOps(repos RepoContexts, git GitOps, resolver *ConflictResolver, token string, specFor func(repo string) AgentSpec) *WorktreeOps {
	return &WorktreeOps{repos: repos, git: git, resolver: resolver, token: token, specFor: specFor}
}

// ResolveConflicts reserves a worktree and runs the agent-driven resolution.
func (w *WorktreeOps) ResolveConflicts(ctx context.Context, req ConflictRequest) (ConflictResult, error) {
	if w.repos == nil || w.resolver == nil {
		return ConflictResult{}, fmt.Errorf("mergetrack: conflict resolution is not wired")
	}
	wt, err := w.repos.AcquireWrite(ctx, req.Repo, fmt.Sprintf("merge-conflict-%d", req.PRNumber))
	if err != nil {
		return ConflictResult{}, fmt.Errorf("mergetrack: reserve worktree: %w", err)
	}
	defer wt.Release()

	spec := w.spec(req.Repo)
	req.Token = w.token
	req.ExecOpts = executor.ExecOptions{
		WorkDir: wt.Path(),
		Timeout: spec.Timeout,
		Effort:  spec.Effort,
	}
	req.CLIPrimary = spec.Primary
	req.CLIFallback = spec.Fallback
	return w.resolver.Resolve(ctx, req)
}

// RebaseAndForcePush is the fallback for a branch GitHub refuses to update with
// a merge commit — a base that requires linear history, typically.
//
// No agent is involved. If the rebase conflicts this reports it and stops:
// resolving conflicts is a separate, separately-enabled toggle, and doing it
// here would let update_branch alone rewrite a branch with an agent's guesses.
func (w *WorktreeOps) RebaseAndForcePush(ctx context.Context, req ConflictRequest) (string, error) {
	if w.repos == nil || w.git == nil {
		return "", fmt.Errorf("mergetrack: local rebase is not wired")
	}
	wt, err := w.repos.AcquireWrite(ctx, req.Repo, fmt.Sprintf("merge-update-%d", req.PRNumber))
	if err != nil {
		return "", fmt.Errorf("mergetrack: reserve worktree: %w", err)
	}
	defer wt.Release()
	dir := wt.Path()

	preSHA, err := w.git.CheckoutRemoteBranch(ctx, dir, req.Repo, req.HeadRef, w.token)
	if err != nil {
		return "", fmt.Errorf("mergetrack: checkout %s: %w", req.HeadRef, err)
	}
	// The branch moved between the decision and now: the plan was made for a
	// commit that is no longer there.
	if req.ExpectedRemoteHeadSHA != "" && preSHA != req.ExpectedRemoteHeadSHA {
		return "", fmt.Errorf("mergetrack: %w: expected %s, found %s",
			ErrBranchMoved, shortSHA(req.ExpectedRemoteHeadSHA), shortSHA(preSHA))
	}

	baseSHA, err := w.git.FetchRef(ctx, dir, req.Repo, req.BaseRef, w.token)
	if err != nil {
		return "", fmt.Errorf("mergetrack: fetch %s: %w", req.BaseRef, err)
	}

	outcome, err := w.git.RebaseOnto(ctx, dir, baseSHA)
	if err != nil {
		return "", fmt.Errorf("mergetrack: rebase %s onto %s: %w", req.HeadRef, req.BaseRef, err)
	}
	if !outcome.Clean {
		if abortErr := w.git.AbortRebase(ctx, dir); abortErr != nil {
			slog.Warn("mergetrack: rebase abort failed", "repo", req.Repo, "err", abortErr)
		}
		return "", fmt.Errorf("mergetrack: rebasing %s onto %s conflicts in %d file(s); enable resolve_conflicts to have the agent handle it",
			req.HeadRef, req.BaseRef, len(outcome.Conflicts))
	}

	newSHA, err := w.git.HeadSHA(ctx, dir)
	if err != nil {
		return "", fmt.Errorf("mergetrack: read rebased head: %w", err)
	}
	// Leased to the SHA we checked out, so a push that landed meanwhile makes
	// git refuse rather than overwrite it.
	if err := w.git.PushForceWithLease(ctx, dir, req.Repo, req.HeadRef, preSHA, w.token); err != nil {
		return "", fmt.Errorf("mergetrack: force-push %s: %w", req.HeadRef, err)
	}
	slog.Info("mergetrack: branch rebased locally",
		"repo", req.Repo, "pr", req.PRNumber, "branch", req.HeadRef,
		"pre_rebase_sha", preSHA, "new_head_sha", newSHA)
	return newSHA, nil
}

func (w *WorktreeOps) spec(repo string) AgentSpec {
	if w.specFor == nil {
		return AgentSpec{}
	}
	return w.specFor(repo)
}

// Compile-time check.
var _ WorktreeRunner = (*WorktreeOps)(nil)
