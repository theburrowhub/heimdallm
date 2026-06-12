package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/config"
	gh "github.com/heimdallm/daemon/internal/github"
	"github.com/heimdallm/daemon/internal/sse"
)

// TestProcessRepo_SkipsWhenAutonomousEnabled proves the double-processing fix:
// when autonomous mode is enabled for a repo, the legacy label-driven issue
// pipeline (ProcessRepo) returns early WITHOUT contacting GitHub, so the
// autonomous poller owns the issue lifecycle and no second agent runs in
// parallel. Issue tracking is also enabled, so the early return is caused by
// the autonomous guard — not by `!repoIT.Enabled`.
func TestProcessRepo_SkipsWhenAutonomousEnabled(t *testing.T) {
	const repo = "org/repo"

	var ghHits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&ghHits, 1)
		http.NotFound(w, r)
	}))
	defer srv.Close()

	s := newMemStore(t)
	broker := sse.NewBroker()
	broker.Start()
	defer broker.Stop()

	var (
		loginMu sync.Mutex
		login   = "heimdallm-bot"
		cfgMu   sync.Mutex
		cfg     = &config.Config{}
	)
	// Issue tracking ENABLED (so the early return is not just "tracking off")
	// and autonomous ENABLED (the guard under test).
	cfg.GitHub.IssueTracking.Enabled = true
	cfg.Autonomous.Enabled = true

	a := &tier2Adapter{
		ghClient:             gh.NewClient("fake-token", gh.WithBaseURL(srv.URL)),
		store:                s,
		broker:               broker,
		cfgMu:                &cfgMu,
		cfg:                  &cfg,
		loginMu:              &loginMu,
		login:                &login,
		lastSkippedUpdatedAt: make(map[int64]time.Time),
	}

	n, err := a.ProcessRepo(context.Background(), repo)
	if err != nil {
		t.Fatalf("ProcessRepo: unexpected error: %v", err)
	}
	if n != 0 {
		t.Fatalf("ProcessRepo processed %d issue(s), want 0 (autonomous repo must skip legacy pipeline)", n)
	}
	if got := atomic.LoadInt32(&ghHits); got != 0 {
		t.Fatalf("ProcessRepo made %d GitHub call(s), want 0 (must early-return before fetching issues)", got)
	}
}

// TestProcessRepo_AutonomousGuardBoundary is the control for the skip test
// above: it pins the exact predicate ProcessRepo / PromoteReady use to decide
// whether to cede the issue lifecycle to the autonomous poller. With issue
// tracking ON in both cases, only the autonomous flag flips the decision —
// proving the early return in TestProcessRepo_SkipsWhenAutonomousEnabled is
// caused by the autonomous guard and not by `!repoIT.Enabled`.
func TestProcessRepo_AutonomousGuardBoundary(t *testing.T) {
	const repo = "org/repo"

	newAdapter := func(autonomousEnabled bool) *tier2Adapter {
		var (
			loginMu sync.Mutex
			login   = "heimdallm-bot"
			cfgMu   sync.Mutex
			cfg     = &config.Config{}
		)
		cfg.GitHub.IssueTracking.Enabled = true
		cfg.Autonomous.Enabled = autonomousEnabled
		return &tier2Adapter{
			cfgMu:   &cfgMu,
			cfg:     &cfg,
			loginMu: &loginMu,
			login:   &login,
		}
	}

	if got := newAdapter(true).autonomousEnabledForRepo(repo); !got {
		t.Fatalf("autonomousEnabledForRepo with autonomous ON = false, want true (ProcessRepo/PromoteReady must skip)")
	}
	if got := newAdapter(false).autonomousEnabledForRepo(repo); got {
		t.Fatalf("autonomousEnabledForRepo with autonomous OFF = true, want false (legacy pipeline must run)")
	}
}
