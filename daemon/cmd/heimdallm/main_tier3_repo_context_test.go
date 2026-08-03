package main

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/config"
	gh "github.com/heimdallm/daemon/internal/github"
	"github.com/heimdallm/daemon/internal/scheduler"
	"github.com/heimdallm/daemon/internal/sse"
	"github.com/heimdallm/daemon/internal/store"
)

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
