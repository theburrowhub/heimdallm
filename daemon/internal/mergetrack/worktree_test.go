package mergetrack_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/issues"
	"github.com/heimdallm/daemon/internal/mergetrack"
)

type fakeWorktree struct {
	path     string
	released int
}

func (w *fakeWorktree) Path() string { return w.path }
func (w *fakeWorktree) Release()     { w.released++ }

type fakeRepoContexts struct {
	wt        *fakeWorktree
	err       error
	gotRepo   string
	gotToken  string
	callCount int
}

func (r *fakeRepoContexts) AcquireWrite(_ context.Context, repo, worktreeToken string) (mergetrack.Worktree, error) {
	r.callCount++
	r.gotRepo = repo
	r.gotToken = worktreeToken
	if r.err != nil {
		return nil, r.err
	}
	return r.wt, nil
}

func newOps(t *testing.T, git mergetrack.GitOps, exec mergetrack.CLIExecutor) (*mergetrack.WorktreeOps, *fakeRepoContexts, *fakeWorktree) {
	t.Helper()
	wt := &fakeWorktree{path: "/tmp/worktree"}
	repos := &fakeRepoContexts{wt: wt}
	ops := mergetrack.NewWorktreeOps(
		repos, git, mergetrack.NewConflictResolver(git, exec), "tok",
		func(string) mergetrack.AgentSpec {
			return mergetrack.AgentSpec{
				Primary: "claude", Fallback: "codex",
				Timeout: 30 * time.Minute, Effort: "high",
			}
		},
	)
	return ops, repos, wt
}

func TestWorktreeOps_ResolveConflictsPassesTheAgentSpecThrough(t *testing.T) {
	git := &fakeGit{
		checkoutSHA:  headSHA,
		rebase:       issues.RebaseOutcome{Conflicts: []string{"a.go"}},
		changedFiles: []string{"a.go"},
		newHeadSHA:   "newhead",
	}
	exec := &fakeExec{}
	ops, repos, wt := newOps(t, git, exec)

	res, err := ops.ResolveConflicts(context.Background(), mergetrack.ConflictRequest{
		Repo: "acme/widgets", PRNumber: 7, HeadRef: "feature", BaseRef: "main",
		ExpectedRemoteHeadSHA: headSHA,
	})
	if err != nil {
		t.Fatalf("ResolveConflicts: %v", err)
	}
	if !res.Pushed {
		t.Error("expected the resolution to be pushed")
	}
	// A deterministic token means a retry lands on the same path and two
	// concurrent runs on one repo cannot collide.
	if repos.gotToken != "merge-conflict-7" {
		t.Errorf("worktree token = %q, want merge-conflict-7", repos.gotToken)
	}
	if repos.gotRepo != "acme/widgets" {
		t.Errorf("repo = %q", repos.gotRepo)
	}
	if wt.released != 1 {
		t.Errorf("worktree released %d times, want exactly 1", wt.released)
	}
}

func TestWorktreeOps_ReleasesTheWorktreeOnFailure(t *testing.T) {
	git := &fakeGit{checkoutSHA: "moved"}
	ops, _, wt := newOps(t, git, &fakeExec{})

	_, err := ops.ResolveConflicts(context.Background(), mergetrack.ConflictRequest{
		Repo: "acme/widgets", PRNumber: 7, HeadRef: "feature", BaseRef: "main",
		ExpectedRemoteHeadSHA: headSHA,
	})
	if err == nil {
		t.Fatal("expected an error when the branch moved")
	}
	if wt.released != 1 {
		t.Errorf("worktree released %d times, want 1 — a leak here exhausts the per-repo cap", wt.released)
	}
}

func TestWorktreeOps_ReportsWorktreeAcquisitionFailure(t *testing.T) {
	repos := &fakeRepoContexts{err: errors.New("no worktrees left")}
	ops := mergetrack.NewWorktreeOps(repos, &fakeGit{}, mergetrack.NewConflictResolver(&fakeGit{}, &fakeExec{}), "tok", nil)

	if _, err := ops.ResolveConflicts(context.Background(), mergetrack.ConflictRequest{Repo: "r", PRNumber: 1}); err == nil {
		t.Error("a failed reservation must be reported")
	}
	if _, err := ops.RebaseAndForcePush(context.Background(), mergetrack.ConflictRequest{Repo: "r", PRNumber: 1}); err == nil {
		t.Error("a failed reservation must be reported")
	}
}

func TestWorktreeOps_RebaseAndForcePushLeasesOnTheObservedSHA(t *testing.T) {
	git := &fakeGit{
		checkoutSHA: headSHA,
		baseSHA:     "basesha",
		rebase:      issues.RebaseOutcome{Clean: true},
		newHeadSHA:  "newhead",
	}
	ops, repos, wt := newOps(t, git, &fakeExec{})

	newSHA, err := ops.RebaseAndForcePush(context.Background(), mergetrack.ConflictRequest{
		Repo: "acme/widgets", PRNumber: 7, HeadRef: "feature", BaseRef: "main",
		ExpectedRemoteHeadSHA: headSHA,
	})
	if err != nil {
		t.Fatalf("RebaseAndForcePush: %v", err)
	}
	if newSHA != "newhead" {
		t.Errorf("new sha = %q, want newhead", newSHA)
	}
	if git.pushLease != headSHA {
		t.Errorf("push lease = %q, want the observed head %q", git.pushLease, headSHA)
	}
	if repos.gotToken != "merge-update-7" {
		t.Errorf("worktree token = %q, want merge-update-7", repos.gotToken)
	}
	if wt.released != 1 {
		t.Errorf("worktree released %d times, want 1", wt.released)
	}
}

// update_branch and resolve_conflicts are separate switches. A branch update
// must never quietly rewrite the branch with an agent's guesses.
func TestWorktreeOps_RebaseStopsAtAConflictAndDoesNotPush(t *testing.T) {
	git := &fakeGit{
		checkoutSHA: headSHA,
		rebase:      issues.RebaseOutcome{Conflicts: []string{"a.go"}},
	}
	ops, _, _ := newOps(t, git, &fakeExec{})

	_, err := ops.RebaseAndForcePush(context.Background(), mergetrack.ConflictRequest{
		Repo: "acme/widgets", PRNumber: 7, HeadRef: "feature", BaseRef: "main",
		ExpectedRemoteHeadSHA: headSHA,
	})
	if err == nil {
		t.Fatal("a conflicting rebase must be reported, not resolved here")
	}
	if !strings.Contains(err.Error(), "resolve_conflicts") {
		t.Errorf("err = %v, want it to point at the setting that would handle this", err)
	}
	if git.called("push") {
		t.Fatal("nothing may be pushed when the rebase conflicts")
	}
	if !git.called("abort") {
		t.Error("the rebase must be aborted so the worktree is left clean")
	}
}

func TestWorktreeOps_RebaseAbortsWhenTheBranchMoved(t *testing.T) {
	git := &fakeGit{checkoutSHA: "somethingelse"}
	ops, _, _ := newOps(t, git, &fakeExec{})

	_, err := ops.RebaseAndForcePush(context.Background(), mergetrack.ConflictRequest{
		Repo: "acme/widgets", PRNumber: 7, HeadRef: "feature", BaseRef: "main",
		ExpectedRemoteHeadSHA: headSHA,
	})
	if !errors.Is(err, mergetrack.ErrBranchMoved) {
		t.Fatalf("err = %v, want ErrBranchMoved", err)
	}
	if git.called("rebase") {
		t.Fatal("must not rebase a branch that moved since the decision")
	}
}

func TestWorktreeOps_ReportsGitFailures(t *testing.T) {
	cases := map[string]*fakeGit{
		"checkout": {checkoutErr: errors.New("boom")},
		"fetch":    {checkoutSHA: headSHA, fetchErr: errors.New("boom")},
		"rebase":   {checkoutSHA: headSHA, rebaseErr: errors.New("boom")},
		"head":     {checkoutSHA: headSHA, rebase: issues.RebaseOutcome{Clean: true}, headErr: errors.New("boom")},
		"push":     {checkoutSHA: headSHA, rebase: issues.RebaseOutcome{Clean: true}, pushErr: errors.New("boom")},
	}
	for name, git := range cases {
		t.Run(name, func(t *testing.T) {
			ops, _, _ := newOps(t, git, &fakeExec{})
			_, err := ops.RebaseAndForcePush(context.Background(), mergetrack.ConflictRequest{
				Repo: "acme/widgets", PRNumber: 7, HeadRef: "feature", BaseRef: "main",
				ExpectedRemoteHeadSHA: headSHA,
			})
			if err == nil {
				t.Fatalf("a %s failure must be reported", name)
			}
		})
	}
}

// A nil spec resolver means the executor's own defaults, not a panic.
func TestWorktreeOps_NilAgentSpecIsSafe(t *testing.T) {
	git := &fakeGit{
		checkoutSHA:  headSHA,
		rebase:       issues.RebaseOutcome{Conflicts: []string{"a.go"}},
		changedFiles: []string{"a.go"},
		newHeadSHA:   "newhead",
	}
	exec := &fakeExec{}
	ops := mergetrack.NewWorktreeOps(
		&fakeRepoContexts{wt: &fakeWorktree{path: "/tmp/wt"}},
		git, mergetrack.NewConflictResolver(git, exec), "tok", nil,
	)
	if _, err := ops.ResolveConflicts(context.Background(), mergetrack.ConflictRequest{
		Repo: "acme/widgets", PRNumber: 7, HeadRef: "feature", BaseRef: "main",
		ExpectedRemoteHeadSHA: headSHA,
	}); err != nil {
		t.Fatalf("ResolveConflicts: %v", err)
	}
}

func TestWorktreeOps_UnwiredDependenciesAreReported(t *testing.T) {
	ops := mergetrack.NewWorktreeOps(nil, nil, nil, "tok", nil)
	if _, err := ops.ResolveConflicts(context.Background(), mergetrack.ConflictRequest{}); err == nil {
		t.Error("an unwired resolver must be reported")
	}
	if _, err := ops.RebaseAndForcePush(context.Background(), mergetrack.ConflictRequest{}); err == nil {
		t.Error("an unwired git layer must be reported")
	}
}
