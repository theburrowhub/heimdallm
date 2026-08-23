package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/config"
	gh "github.com/heimdallm/daemon/internal/github"
	"github.com/heimdallm/daemon/internal/sse"
)

// A newly discovered repo must emit repo_discovered and return its PR in the
// same cycle. The former one-tick deferral added five minutes at the default
// interval and did not actually guarantee event delivery.
func TestTier2Adapter_FetchPRsToReviewProcessesNewlyDiscoveredRepoSameCycle(t *testing.T) {
	updatedAt := time.Date(2026, 5, 13, 10, 0, 0, 0, time.UTC)
	const repo = "newco/newrepo"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/user":
			_ = json.NewEncoder(w).Encode(map[string]string{"login": "heimdallm-bot"})
		case r.URL.Path == "/search/issues":
			result := struct {
				Items []gh.PullRequest `json:"items"`
			}{Items: []gh.PullRequest{{
				ID:        9001,
				Number:    11,
				Title:     "fresh PR on a brand-new repo",
				State:     "open",
				User:      gh.User{Login: "alice"},
				Head:      gh.Branch{Repo: gh.Repo{FullName: repo}},
				UpdatedAt: updatedAt,
			}}}
			_ = json.NewEncoder(w).Encode(result)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	s := newMemStore(t)
	broker := sse.NewBroker()
	broker.Start()
	defer broker.Stop()
	sub := broker.Subscribe()
	if sub == nil {
		t.Fatal("broker subscribe returned nil")
	}

	var (
		loginMu sync.Mutex
		login   = "heimdallm-bot"
		cfgMu   sync.Mutex
		cfg     = &config.Config{}
		ordered bool
	)

	a := &tier2Adapter{
		ghClient: gh.NewClient("fake-token", gh.WithBaseURL(srv.URL)),
		store:    s,
		broker:   broker,
		cfgMu:    &cfgMu,
		cfg:      &cfg,
		loginMu:  &loginMu,
		login:    &login,
		publishOrderedEvents: func(events []sse.Event) error {
			if len(events) == 1 && events[0].Type == sse.EventRepoDiscovered {
				ordered = true
			}
			return nil
		},
		lastSkippedUpdatedAt: make(map[int64]time.Time),
	}

	// First cycle: repo is brand new. Discovery and ingestion both happen now.
	out, err := a.FetchPRsToReview()
	if err != nil {
		t.Fatalf("first FetchPRsToReview: %v", err)
	}
	if len(out) != 1 || out[0].Repo != repo {
		t.Fatalf("first cycle expected 1 PR for %q, got %+v", repo, out)
	}
	if !ordered {
		t.Fatal("repo_discovered was not handed off before same-cycle PR return")
	}

	// Drain SSE events; we should see exactly one repo_discovered for `repo`.
	gotRepoDiscovered := false
	deadline := time.After(200 * time.Millisecond)
drain:
	for {
		select {
		case ev := <-sub:
			if ev.Type == sse.EventRepoDiscovered && strings.Contains(ev.Data, repo) {
				gotRepoDiscovered = true
			}
		case <-deadline:
			break drain
		}
	}
	if !gotRepoDiscovered {
		t.Fatalf("expected EventRepoDiscovered for %q on first cycle", repo)
	}
}
