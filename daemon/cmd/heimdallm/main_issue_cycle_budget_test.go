package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/config"
	gh "github.com/heimdallm/daemon/internal/github"
	issuepipeline "github.com/heimdallm/daemon/internal/issues"
	"github.com/heimdallm/daemon/internal/scheduler"
	"github.com/heimdallm/daemon/internal/sse"
)

func TestRunTier2ChargesCoreOnlyForPromotionAndRESTFallback(t *testing.T) {
	var promoted atomic.Int32
	blocked := map[string]any{
		"id": 10, "number": 10, "title": "blocked work", "state": "open",
		"body":      "## Depends on\n- #5\n",
		"assignees": []map[string]string{{"login": "bot"}},
		"labels":    []map[string]string{{"name": "blocked"}},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/user":
			_ = json.NewEncoder(w).Encode(map[string]string{"login": "bot"})
		case r.Method == http.MethodGet && r.URL.Path == "/search/issues":
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []any{}})
		case r.Method == http.MethodGet && r.URL.Path == "/repos/org/repo/issues":
			_ = json.NewEncoder(w).Encode([]any{blocked})
		case r.Method == http.MethodGet && r.URL.Path == "/repos/org/repo/issues/10/sub_issues":
			_ = json.NewEncoder(w).Encode([]any{})
		case r.Method == http.MethodGet && r.URL.Path == "/repos/org/repo/issues/5":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": 5, "number": 5, "title": "dependency", "state": "closed",
			})
		case r.Method == http.MethodDelete && r.URL.Path == "/repos/org/repo/issues/10/labels/blocked":
			_ = json.NewEncoder(w).Encode([]any{})
		case r.Method == http.MethodPost && r.URL.Path == "/repos/org/repo/issues/10/labels":
			promoted.Add(1)
			_ = json.NewEncoder(w).Encode([]map[string]string{{"name": "ready"}})
		case r.Method == http.MethodPost && r.URL.Path == "/repos/org/repo/issues/10/comments":
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"created_at": time.Now().UTC().Format(time.RFC3339),
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	client := gh.NewClient("token", gh.WithBaseURL(srv.URL))
	s := newMemStore(t)
	fetcher := issuepipeline.NewFetcher(client, client, s, nil)
	broker := sse.NewBroker()
	broker.Start()
	t.Cleanup(broker.Stop)
	cfg := &config.Config{}
	cfg.GitHub.Repositories = []string{"org/repo"}
	cfg.GitHub.IssueTracking = config.IssueTrackingConfig{
		Enabled:        true,
		FilterMode:     config.FilterModeExclusive,
		DefaultAction:  string(config.IssueModeIgnore),
		Assignees:      []string{"bot"},
		BlockedLabels:  []string{"blocked"},
		DevelopLabels:  []string{"ready"},
		PromoteToLabel: "ready",
	}
	cfgRef := cfg
	login := "bot"
	var cfgMu, loginMu sync.Mutex
	adapter := &tier2Adapter{
		ghClient:             client,
		fetcher:              fetcher,
		store:                s,
		broker:               broker,
		cfg:                  &cfgRef,
		cfgMu:                &cfgMu,
		login:                &login,
		loginMu:              &loginMu,
		lastSkippedUpdatedAt: make(map[int64]time.Time),
	}

	// Exactly two core operations are charged here: the configured promotion
	// pass and the real per-repo REST fallback. The PR Search request uses its
	// own resource gate and the absent Search prefetch performs no HTTP call.
	limiter := scheduler.NewRateLimiter(3)
	runCycle := func() {
		ctx, cancel := context.WithCancel(context.Background())
		reposCh := make(chan []string, 1)
		reposCh <- []string{"org/repo"}
		completed := make(chan string, 2)
		done := make(chan struct{})
		go func() {
			defer close(done)
			runTier2(
				ctx, adapter, limiter, nil, nil,
				func() []string { return []string{"org/repo"} },
				nil, nil, nil, reposCh, time.Hour, true,
				func(kind string, _ time.Time) { completed <- kind },
			)
		}()
		seen := map[string]bool{}
		for len(seen) < 2 {
			select {
			case kind := <-completed:
				seen[kind] = true
			case <-time.After(2 * time.Second):
				cancel()
				<-done
				t.Fatalf("timed out waiting for PR+issue cycles; completed=%v", seen)
			}
		}
		cancel()
		<-done
	}

	runCycle()
	if got := promoted.Load(); got != 1 {
		t.Fatalf("promotion label writes = %d, want 1", got)
	}
	if got := limiter.Available(); got != 1 {
		t.Fatalf("remaining core tokens = %d, want 1 after promotion + REST fallback", got)
	}

	// A malformed promotion target is non-fatal to the issue cycle: surface the
	// error and continue local/fallback processing. This also pins the wrapper's
	// error branch rather than only the promoter implementation.
	cfgMu.Lock()
	cfg.GitHub.IssueTracking.DevelopLabels = nil
	cfg.GitHub.IssueTracking.PromoteToLabel = ""
	cfgMu.Unlock()
	limiter.Refill()
	runCycle()
}
