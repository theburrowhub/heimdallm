package main

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/config"
	gh "github.com/heimdallm/daemon/internal/github"
	"github.com/heimdallm/daemon/internal/repoctx"
	"github.com/heimdallm/daemon/internal/scheduler"
	"github.com/heimdallm/daemon/internal/sse"
	"github.com/heimdallm/daemon/internal/store"
)

// newTier3Adapter builds the minimal adapter the HandleChange review path needs,
// with a capture hook standing in for runReview.
func newTier3Adapter(t *testing.T, cfg *config.Config, mgr *repoctx.Manager, got *config.RepoAI, calls *int) *tier2Adapter {
	t.Helper()
	broker := sse.NewBroker()
	broker.Start()
	t.Cleanup(broker.Stop)

	loginMu := &sync.Mutex{}
	login := "heimdallm-bot"
	cfgMu := &sync.Mutex{}
	cfgPtr := &cfg

	return &tier2Adapter{
		store:   newMemStore(t),
		broker:  broker,
		cfgMu:   cfgMu,
		cfg:     cfgPtr,
		loginMu: loginMu,
		login:   &login,
		repoCtx: mgr,
		ghToken: "test-token",
		runReview: func(_ *gh.PullRequest, aiCfg config.RepoAI) *store.Review {
			*calls++
			*got = aiCfg
			return nil
		},
	}
}

func tier3ConfigWithLocalDir(localDir string) *config.Config {
	return &config.Config{
		GitHub: config.GitHubConfig{Repositories: []string{"org/repo"}},
		AI: config.AIConfig{
			Primary: "claude",
			Repos:   map[string]config.RepoAI{"org/repo": {LocalDir: localDir}},
		},
	}
}

func tier3WatchItem() (*scheduler.WatchItem, *scheduler.ItemSnapshot) {
	return &scheduler.WatchItem{Type: "pr", Repo: "org/repo", Number: 42, GithubID: 42},
		&scheduler.ItemSnapshot{State: "open", Author: "alice", UpdatedAt: time.Now(), HeadSHA: "abc123"}
}

// TestTier3Adapter_HandleChange_RespectsWorktreeCap is the success-path
// counterpart that actually pins the acquisition: with the per-repo worktree cap
// already exhausted, a HandleChange that goes through repoctx cannot obtain a
// checkout and must fall back to an empty LocalDir. An implementation that
// forwarded the configured local_dir without asking repoctx — the bug — would
// hand the path over regardless and bypass the concurrency budget entirely.
func TestTier3Adapter_HandleChange_RespectsWorktreeCap(t *testing.T) {
	localDir := t.TempDir()
	mgr := repoctx.NewManagerWithOptions(repoctx.ManagerOptions{MaxWorktreesPerRepo: 1})

	// Hold the repo's only slot for the duration of the call.
	held, err := mgr.Acquire(context.Background(), repoctx.Request{
		Repo:               "org/repo",
		ConfiguredLocalDir: localDir,
		Mode:               repoctx.ModeRead,
		WorktreeToken:      "holder-1",
	})
	if err != nil {
		t.Fatalf("seed acquire: %v", err)
	}
	defer held.Release()

	var (
		gotAI config.RepoAI
		calls int
	)
	a := newTier3Adapter(t, tier3ConfigWithLocalDir(localDir), mgr, &gotAI, &calls)

	// Short deadline: the cap wait must give up rather than hang the test.
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	item, snap := tier3WatchItem()
	if err := a.HandleChange(ctx, item, snap); err != nil {
		t.Fatalf("HandleChange: %v", err)
	}
	if calls != 1 {
		t.Fatalf("runReview called %d time(s), want 1", calls)
	}
	if gotAI.LocalDir != "" {
		t.Errorf("LocalDir = %q, want empty: the cap was exhausted, so no checkout could be reserved", gotAI.LocalDir)
	}
}

// TestTier3Adapter_HandleChange_ReleasesTheReservation covers the risk of adding
// an acquisition to this entry point: a reservation that is never released
// starves every later execution on the repo. With the cap free, runReview must
// receive the reserved checkout, and the slot must be available again afterwards.
func TestTier3Adapter_HandleChange_ReleasesTheReservation(t *testing.T) {
	localDir := t.TempDir()
	mgr := repoctx.NewManagerWithOptions(repoctx.ManagerOptions{MaxWorktreesPerRepo: 1})

	var (
		gotAI config.RepoAI
		calls int
	)
	a := newTier3Adapter(t, tier3ConfigWithLocalDir(localDir), mgr, &gotAI, &calls)

	item, snap := tier3WatchItem()
	if err := a.HandleChange(context.Background(), item, snap); err != nil {
		t.Fatalf("HandleChange: %v", err)
	}
	if calls != 1 {
		t.Fatalf("runReview called %d time(s), want 1", calls)
	}
	if !sameDir(t, gotAI.LocalDir, localDir) {
		t.Errorf("LocalDir = %q, want the reserved checkout %q", gotAI.LocalDir, localDir)
	}

	// The only slot must be free again: if the handle leaked, this blocks until
	// the deadline and fails.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	h, err := mgr.Acquire(ctx, repoctx.Request{
		Repo:               "org/repo",
		ConfiguredLocalDir: localDir,
		Mode:               repoctx.ModeRead,
		WorktreeToken:      "probe-1",
	})
	if err != nil {
		t.Fatalf("reservation leaked: re-acquiring the repo failed: %v", err)
	}
	h.Release()
}

// sameDir compares paths tolerating symlinked temp roots (macOS /var →
// /private/var).
func sameDir(t *testing.T, got, want string) bool {
	t.Helper()
	if got == want {
		return true
	}
	gotResolved, err1 := filepath.EvalSymlinks(got)
	wantResolved, err2 := filepath.EvalSymlinks(want)
	return err1 == nil && err2 == nil && gotResolved == wantResolved
}

// TestTier3Adapter_HandleChange_ResolvesWorkDirThroughRepoContext covers
// theburrowhub/heimdallm#655. HandleChange was the only review entry point that
// skipped acquireRepoContext: it forwarded the raw config local_dir (usually
// empty) straight to runReview, so the agent ran with no WorkDir and inherited
// the daemon's cwd — `/` under launchd — which makes `codex exec` abort with
// "Not inside a trusted directory". The other two entry points (review-worker
// and tier2 ProcessPR) go through repoctx and must not be the exception.
//
// With a nil manager the acquisition necessarily fails, and the contract is the
// same one ProcessPR already honours: log the fallback and hand runReview an
// empty LocalDir rather than an unreserved path. That is exactly what
// distinguishes the fixed code from the old one, which passed the configured
// value through untouched.
func TestTier3Adapter_HandleChange_ResolvesWorkDirThroughRepoContext(t *testing.T) {
	s := newMemStore(t)
	broker := sse.NewBroker()
	broker.Start()
	defer broker.Stop()

	var (
		loginMu sync.Mutex
		login   = "heimdallm-bot"
		cfgMu   sync.Mutex
		cfg     = &config.Config{
			GitHub: config.GitHubConfig{Repositories: []string{"org/repo"}},
			AI: config.AIConfig{
				Primary: "claude",
				Repos: map[string]config.RepoAI{
					// A path the pipeline must never use directly: reviews run in
					// a worktree reserved through repoctx, never in the operator's
					// own checkout.
					"org/repo": {LocalDir: "/tmp/unreserved-checkout"},
				},
			},
		}
	)

	var (
		gotAI  config.RepoAI
		called int
	)
	a := &tier2Adapter{
		store:   s,
		broker:  broker,
		cfgMu:   &cfgMu,
		cfg:     &cfg,
		loginMu: &loginMu,
		login:   &login,
		repoCtx: nil, // acquisition fails — exercises the fallback contract
		runReview: func(_ *gh.PullRequest, aiCfg config.RepoAI) *store.Review {
			called++
			gotAI = aiCfg
			return nil
		},
	}

	err := a.HandleChange(context.Background(), &scheduler.WatchItem{
		Type: "pr", Repo: "org/repo", Number: 42, GithubID: 42,
	}, &scheduler.ItemSnapshot{
		State: "open", Author: "alice", UpdatedAt: time.Now(), HeadSHA: "abc123",
	})
	if err != nil {
		t.Fatalf("HandleChange: %v", err)
	}
	if called != 1 {
		t.Fatalf("runReview called %d time(s), want 1", called)
	}
	if gotAI.LocalDir != "" {
		t.Errorf("LocalDir = %q, want empty: the checkout must come from repoctx, not straight from config", gotAI.LocalDir)
	}
}
