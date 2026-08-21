package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/config"
	gh "github.com/heimdallm/daemon/internal/github"
	"github.com/heimdallm/daemon/internal/scheduler"
	"github.com/heimdallm/daemon/internal/sse"
)

type requestBudgetPRPublisher struct {
	published chan scheduler.Tier2PR
	err       error
}

func (p *requestBudgetPRPublisher) PublishPRReviewCandidate(_ context.Context, repo string, number int, githubID int64) error {
	p.published <- scheduler.Tier2PR{Repo: repo, Number: number, ID: githubID}
	return p.err
}

func TestTier2AdapterPRIngestionRequestBudget(t *testing.T) {
	const prCount = 20
	var userCalls, searchCalls, pullCalls atomic.Int32

	items := make([]gh.PullRequest, 0, prCount)
	for i := 1; i <= prCount; i++ {
		items = append(items, gh.PullRequest{
			ID: int64(i), Number: i, Title: fmt.Sprintf("PR %d", i),
			State: "open", User: gh.User{Login: "alice"},
			Head:      gh.Branch{Repo: gh.Repo{FullName: "org/repo"}},
			UpdatedAt: time.Unix(int64(i), 0).UTC(),
		})
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/user":
			userCalls.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]string{"login": "heimdallm-bot"})
		case r.URL.Path == "/search/issues":
			searchCalls.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{"total_count": prCount, "items": items})
		case r.URL.Path == "/repos/org/repo/pulls/1":
			pullCalls.Add(1)
			http.Error(w, "adapter must not hydrate", http.StatusInternalServerError)
		default:
			if strings.HasPrefix(r.URL.Path, "/repos/org/repo/pulls/") {
				pullCalls.Add(1)
				http.Error(w, "adapter must not hydrate", http.StatusInternalServerError)
				return
			}
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	client := gh.NewClient("token", gh.WithBaseURL(srv.URL))
	// Production resolves the login at startup on this same client. Warm that
	// immutable cache, then measure one adapter cycle.
	if _, err := client.AuthenticatedUser(); err != nil {
		t.Fatalf("warm authenticated user: %v", err)
	}

	store := newMemStore(t)
	broker := sse.NewBroker()
	broker.Start()
	defer broker.Stop()
	cfg := &config.Config{GitHub: config.GitHubConfig{Repositories: []string{"org/repo"}}}
	cfgRef := cfg
	login := "heimdallm-bot"
	var cfgMu, loginMu sync.Mutex
	adapter := &tier2Adapter{
		ghClient: client, store: store, broker: broker,
		cfg: &cfgRef, cfgMu: &cfgMu, login: &login, loginMu: &loginMu,
		lastSkippedUpdatedAt: make(map[int64]time.Time),
	}

	got, err := adapter.FetchPRsToReview()
	if err != nil {
		t.Fatalf("FetchPRsToReview: %v", err)
	}
	if len(got) != prCount {
		t.Fatalf("candidates = %d, want %d", len(got), prCount)
	}
	if got := userCalls.Load(); got != 1 {
		t.Fatalf("GET /user calls including warmup = %d, want 1", got)
	}
	if got := searchCalls.Load(); got != 1 {
		t.Fatalf("Search calls = %d, want 1", got)
	}
	if got := pullCalls.Load(); got != 0 {
		t.Fatalf("adapter Pulls calls = %d, want 0", got)
	}
	t.Logf("20 PR candidates: adapter requests warm path = %d Search + %d Pulls", searchCalls.Load(), pullCalls.Load())
}

func TestRunTier2PRSearchConsumesOneSearchPermitWithoutExtraCorePermit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/user":
			_ = json.NewEncoder(w).Encode(map[string]string{"login": "heimdallm-bot"})
		case "/search/issues":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"total_count": 1,
				"items": []gh.PullRequest{{
					ID: 1, Number: 7, State: "open", User: gh.User{Login: "alice"},
					Head: gh.Branch{Repo: gh.Repo{FullName: "org/repo"}},
				}},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	// One shared pool token is enough for the one Search page. The former
	// generic core acquire consumed it first and left the real Search gate
	// blocked until the cycle was cancelled.
	ctx, cancel := context.WithCancel(context.Background())
	limiter := scheduler.NewRateLimiter(1)
	client := gh.NewClient("token", gh.WithBaseURL(srv.URL))
	client.SetSearchGate(func() error {
		return limiter.AcquireResource(ctx, scheduler.TierRepo, scheduler.SearchResource)
	})

	store := newMemStore(t)
	broker := sse.NewBroker()
	broker.Start()
	defer broker.Stop()
	cfg := &config.Config{GitHub: config.GitHubConfig{Repositories: []string{"org/repo"}}}
	login := "heimdallm-bot"
	var cfgMu, loginMu sync.Mutex
	adapter := &tier2Adapter{
		ghClient: client, store: store, broker: broker,
		cfg: &cfg, cfgMu: &cfgMu, login: &login, loginMu: &loginMu,
		lastSkippedUpdatedAt: make(map[int64]time.Time),
	}
	publisher := &requestBudgetPRPublisher{
		published: make(chan scheduler.Tier2PR, 1),
		err:       errors.New("simulated enqueue failure"),
	}
	reposCh := make(chan []string, 1)
	reposCh <- []string{"org/repo"}

	done := make(chan struct{})
	go func() {
		defer close(done)
		runTier2(
			ctx, adapter, limiter, publisher, broker,
			func() []string { return []string{"org/repo"} },
			nil, nil, nil, reposCh, time.Hour, true, nil,
		)
	}()
	defer func() {
		cancel()
		<-done
	}()

	select {
	case got := <-publisher.published:
		if got.Repo != "org/repo" || got.Number != 7 {
			t.Fatalf("published candidate = %+v", got)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("PR Search stalled behind an extra core permit")
	}
}
