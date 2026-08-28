package mergetrack

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/heimdallm/daemon/internal/executor"
	"github.com/heimdallm/daemon/internal/issues"
)

// GitOps is the git plumbing the conflict resolver needs, declared here in the
// consumer so the resolver can be tested with a hand-written fake. Backed by
// issues.GitExec in production.
type GitOps interface {
	CheckoutRemoteBranch(ctx context.Context, dir, repo, branch, token string) (string, error)
	FetchRef(ctx context.Context, dir, repo, ref, token string) (string, error)
	RebaseOnto(ctx context.Context, dir, ontoSHA string) (issues.RebaseOutcome, error)
	ConflictedFiles(ctx context.Context, dir string) ([]string, error)
	HasUnmergedPaths(ctx context.Context, dir string) (bool, error)
	WorktreeDigest(ctx context.Context, dir string) (map[string]string, error)
	FilesWithConflictMarkers(ctx context.Context, dir string, paths []string) ([]string, error)
	StageAll(ctx context.Context, dir string) error
	ContinueRebase(ctx context.Context, dir string) error
	AbortRebase(ctx context.Context, dir string) error
	HeadSHA(ctx context.Context, dir string) (string, error)
	PushForceWithLease(ctx context.Context, dir, repo, branch, expectedRemoteSHA, token string) error
}

// CLIExecutor runs the configured AI agent.
type CLIExecutor interface {
	Detect(primary, fallback string) (string, error)
	ExecuteRaw(cli, prompt string, opts executor.ExecOptions) ([]byte, error)
}

// ConflictRequest is the input to one conflict-resolution run. The caller owns
// the worktree lifecycle and must set ExecOpts.WorkDir to a checkout Heimdallm
// is allowed to mutate — never a human's.
type ConflictRequest struct {
	Repo     string
	PRNumber int
	PRTitle  string
	HeadRef  string
	BaseRef  string
	// ExpectedRemoteHeadSHA is the head SHA the decision was made against. If
	// the branch has moved since, the run aborts before touching anything.
	ExpectedRemoteHeadSHA string
	Token                 string
	ExecOpts              executor.ExecOptions
	CLIPrimary            string
	CLIFallback           string
}

// ConflictResult reports what a resolution run did.
type ConflictResult struct {
	Pushed bool
	// PreRebaseSHA is the head the branch was at before the rewrite. Published
	// in the audit comment and persisted, so a human can undo a bad resolution
	// with `git reset --hard <sha>`.
	PreRebaseSHA string
	NewHeadSHA   string
	// Files are the paths that were in conflict.
	Files []string
	// CommentBody is the audit comment to post on the PR. Always populated,
	// including on the give-up paths, so the PR records that Heimdallm looked.
	CommentBody string
}

// ErrConflictUnresolved means the agent did not fully resolve the conflicts.
// The tree is left clean (the rebase is aborted) and nothing is pushed.
var ErrConflictUnresolved = errors.New("mergetrack: conflicts remain after the agent ran")

// ErrOutOfScopeChanges means the agent modified files that were not in
// conflict. Nothing is pushed.
var ErrOutOfScopeChanges = errors.New("mergetrack: agent changed files outside the conflicted set")

// ErrBranchMoved means the head branch advanced between the decision and the
// resolution attempt.
var ErrBranchMoved = errors.New("mergetrack: head branch moved before resolution started")

// ConflictResolver rebases a PR's head branch onto its base, has the configured
// agent resolve whatever conflicts arise, and force-pushes the result.
type ConflictResolver struct {
	git  GitOps
	exec CLIExecutor
}

// NewConflictResolver builds a resolver from its two dependencies.
func NewConflictResolver(git GitOps, exec CLIExecutor) *ConflictResolver {
	return &ConflictResolver{git: git, exec: exec}
}

// Resolve runs the full resolution attempt.
//
// The sequence deliberately keeps git under the daemon's control and gives the
// agent exactly one job — edit the conflicted files. Three guards run after the
// agent and any of them abandons the attempt without pushing:
//
//  1. unmerged paths remain — the agent gave up or half-finished;
//  2. conflict markers remain in the file contents — git would happily stage
//     those, and in some languages the result even compiles;
//  3. files outside the conflicted set were touched — out of scope, and the
//     cheapest possible signal that the agent did something unintended.
//
// On every abandon path the rebase is aborted, so the worktree is left clean
// for the next attempt.
func (r *ConflictResolver) Resolve(ctx context.Context, req ConflictRequest) (ConflictResult, error) {
	if r.git == nil || r.exec == nil {
		return ConflictResult{}, fmt.Errorf("mergetrack: conflict resolver not wired")
	}
	if strings.TrimSpace(req.Token) == "" {
		return ConflictResult{}, fmt.Errorf("mergetrack: conflict resolution requires a GitHub token")
	}
	workDir := strings.TrimSpace(req.ExecOpts.WorkDir)
	if workDir == "" {
		return ConflictResult{}, fmt.Errorf("mergetrack: conflict resolution requires a work dir")
	}
	if strings.TrimSpace(req.HeadRef) == "" || strings.TrimSpace(req.BaseRef) == "" {
		return ConflictResult{}, fmt.Errorf("mergetrack: conflict resolution requires head and base refs")
	}

	cli, err := r.exec.Detect(req.CLIPrimary, req.CLIFallback)
	if err != nil {
		return ConflictResult{}, fmt.Errorf("mergetrack: detect CLI: %w", err)
	}

	preSHA, err := r.git.CheckoutRemoteBranch(ctx, workDir, req.Repo, req.HeadRef, req.Token)
	if err != nil {
		return ConflictResult{}, fmt.Errorf("mergetrack: checkout %s: %w", req.HeadRef, err)
	}
	// The lease for the eventual force-push is the SHA we just checked out. If
	// it already differs from what the decision was based on, someone pushed
	// while we were deciding: stop rather than resolve against a stale plan.
	if req.ExpectedRemoteHeadSHA != "" && preSHA != req.ExpectedRemoteHeadSHA {
		return ConflictResult{PreRebaseSHA: preSHA}, fmt.Errorf("%w: expected %s, found %s",
			ErrBranchMoved, shortSHA(req.ExpectedRemoteHeadSHA), shortSHA(preSHA))
	}

	baseSHA, err := r.git.FetchRef(ctx, workDir, req.Repo, req.BaseRef, req.Token)
	if err != nil {
		return ConflictResult{PreRebaseSHA: preSHA}, fmt.Errorf("mergetrack: fetch %s: %w", req.BaseRef, err)
	}

	outcome, err := r.git.RebaseOnto(ctx, workDir, baseSHA)
	if err != nil {
		return ConflictResult{PreRebaseSHA: preSHA}, fmt.Errorf("mergetrack: rebase onto %s: %w", req.BaseRef, err)
	}
	if outcome.Clean {
		// No conflict after all — GitHub's DIRTY was stale, or the base moved
		// again. Push the rebased branch: it is now up to date, which is
		// progress the caller asked for.
		return r.pushResolved(ctx, req, workDir, preSHA, nil)
	}

	conflicts := outcome.Conflicts
	if len(conflicts) == 0 {
		// Defensive: RebaseOnto only returns !Clean with conflicts, but a
		// future change to it must not silently turn into "run the agent on
		// nothing and push whatever it did".
		r.abort(ctx, workDir)
		return ConflictResult{PreRebaseSHA: preSHA},
			fmt.Errorf("mergetrack: rebase reported conflicts but listed no files")
	}

	// Snapshot the tree exactly as the rebase left it, before the agent runs.
	// This is the baseline the scope guard compares against; diffing against
	// the base commit instead would list every file the PR touches, because
	// mid-rebase HEAD already carries the commits replayed so far.
	before, err := r.git.WorktreeDigest(ctx, workDir)
	if err != nil {
		r.abort(ctx, workDir)
		return ConflictResult{PreRebaseSHA: preSHA, Files: conflicts},
			fmt.Errorf("mergetrack: snapshot worktree: %w", err)
	}

	execOpts := executor.OptionsForSelectedCLI(req.CLIPrimary, cli, req.ExecOpts)
	execOpts = issues.EnsureWritePerms(cli, execOpts)

	prompt := buildConflictPrompt(req, conflicts)
	if _, err := r.exec.ExecuteRaw(cli, prompt, execOpts); err != nil {
		r.abort(ctx, workDir)
		return ConflictResult{PreRebaseSHA: preSHA, Files: conflicts},
			fmt.Errorf("mergetrack: execute %s: %w", cli, err)
	}

	// Guard 1: anything still unmerged means the agent did not finish.
	unmerged, err := r.git.HasUnmergedPaths(ctx, workDir)
	if err != nil {
		r.abort(ctx, workDir)
		return ConflictResult{PreRebaseSHA: preSHA, Files: conflicts},
			fmt.Errorf("mergetrack: check unmerged paths: %w", err)
	}
	if unmerged {
		r.abort(ctx, workDir)
		return ConflictResult{
			PreRebaseSHA: preSHA,
			Files:        conflicts,
			CommentBody:  buildConflictGaveUpComment(req, conflicts),
		}, ErrConflictUnresolved
	}

	// Guard 2: markers left in the file contents. git stages those without
	// complaint, and in some languages the result compiles.
	withMarkers, err := r.git.FilesWithConflictMarkers(ctx, workDir, conflicts)
	if err != nil {
		r.abort(ctx, workDir)
		return ConflictResult{PreRebaseSHA: preSHA, Files: conflicts},
			fmt.Errorf("mergetrack: scan for conflict markers: %w", err)
	}
	if len(withMarkers) > 0 {
		r.abort(ctx, workDir)
		return ConflictResult{
			PreRebaseSHA: preSHA,
			Files:        conflicts,
			CommentBody:  buildConflictMarkersComment(req, withMarkers),
		}, fmt.Errorf("%w: markers remain in %s", ErrConflictUnresolved, strings.Join(withMarkers, ", "))
	}

	// Guard 3: scope. Only the conflicted files may differ from the snapshot
	// taken before the agent ran.
	after, err := r.git.WorktreeDigest(ctx, workDir)
	if err != nil {
		r.abort(ctx, workDir)
		return ConflictResult{PreRebaseSHA: preSHA, Files: conflicts},
			fmt.Errorf("mergetrack: snapshot worktree: %w", err)
	}
	if extra := outOfScope(changedBetween(before, after), conflicts); len(extra) > 0 {
		r.abort(ctx, workDir)
		return ConflictResult{
			PreRebaseSHA: preSHA,
			Files:        conflicts,
			CommentBody:  buildConflictOutOfScopeComment(req, extra),
		}, fmt.Errorf("%w: %s", ErrOutOfScopeChanges, strings.Join(extra, ", "))
	}

	if err := r.git.StageAll(ctx, workDir); err != nil {
		r.abort(ctx, workDir)
		return ConflictResult{PreRebaseSHA: preSHA, Files: conflicts},
			fmt.Errorf("mergetrack: stage resolution: %w", err)
	}
	if err := r.git.ContinueRebase(ctx, workDir); err != nil {
		r.abort(ctx, workDir)
		return ConflictResult{PreRebaseSHA: preSHA, Files: conflicts},
			fmt.Errorf("mergetrack: continue rebase: %w", err)
	}

	return r.pushResolved(ctx, req, workDir, preSHA, conflicts)
}

// pushResolved force-pushes the rewritten branch, leased to the SHA we started
// from, and builds the audit comment.
func (r *ConflictResolver) pushResolved(ctx context.Context, req ConflictRequest, workDir, preSHA string, conflicts []string) (ConflictResult, error) {
	newSHA, err := r.git.HeadSHA(ctx, workDir)
	if err != nil {
		return ConflictResult{PreRebaseSHA: preSHA, Files: conflicts},
			fmt.Errorf("mergetrack: read new head: %w", err)
	}
	if err := r.git.PushForceWithLease(ctx, workDir, req.Repo, req.HeadRef, preSHA, req.Token); err != nil {
		return ConflictResult{PreRebaseSHA: preSHA, NewHeadSHA: newSHA, Files: conflicts},
			fmt.Errorf("mergetrack: force-push %s: %w", req.HeadRef, err)
	}
	slog.Info("mergetrack: conflict resolution pushed",
		"repo", req.Repo, "pr", req.PRNumber, "branch", req.HeadRef,
		"pre_rebase_sha", preSHA, "new_head_sha", newSHA, "files", len(conflicts))

	res := ConflictResult{
		Pushed:       true,
		PreRebaseSHA: preSHA,
		NewHeadSHA:   newSHA,
		Files:        conflicts,
	}
	res.CommentBody = buildConflictResolvedComment(req, res)
	return res, nil
}

// abort returns the worktree to a clean state. Failures are logged, not
// returned: the caller is already on an error path, and the worktree is
// ephemeral — the next acquisition re-fetches anyway.
func (r *ConflictResolver) abort(ctx context.Context, workDir string) {
	if err := r.git.AbortRebase(ctx, workDir); err != nil {
		slog.Warn("mergetrack: rebase abort failed; worktree left dirty", "err", err)
	}
}

// outOfScope returns the changed paths that were not in conflict.
// changedBetween lists the paths whose content differs between two worktree
// digests, in a stable order. A path that vanished from the second digest
// counts too: the agent reverting a file to its committed state is as much a
// change as editing one.
func changedBetween(before, after map[string]string) []string {
	var out []string
	for path, sum := range after {
		if prev, ok := before[path]; !ok || prev != sum {
			out = append(out, path)
		}
	}
	for path := range before {
		if _, ok := after[path]; !ok {
			out = append(out, path)
		}
	}
	sort.Strings(out)
	return out
}

func outOfScope(changed, conflicts []string) []string {
	allowed := make(map[string]struct{}, len(conflicts))
	for _, c := range conflicts {
		allowed[c] = struct{}{}
	}
	var extra []string
	for _, c := range changed {
		if _, ok := allowed[c]; !ok {
			extra = append(extra, c)
		}
	}
	return extra
}
