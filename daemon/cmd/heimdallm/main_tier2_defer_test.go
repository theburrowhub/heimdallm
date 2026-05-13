package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/config"
	gh "github.com/heimdallm/daemon/internal/github"
	"github.com/heimdallm/daemon/internal/sse"
)

// TestTier2Adapter_FetchPRsToReview_DefersNewlyDiscoveredRepo pins
// the fix for the third symptom in #481: on the tick that
// auto-discovers a new repo, FetchPRsToReview must publish
// EventRepoDiscovered *and* withhold the PR for that repo from the
// returned slice. The next tick — once the UI has had time to
// receive repo_discovered — includes the PR normally so the review
// proceeds.
func TestTier2Adapter_FetchPRsToReview_DefersNewlyDiscoveredRepo(t *testing.T) {
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
		case r.URL.Path == "/repos/"+repo+"/pulls/11":
			_ = json.NewEncoder(w).Encode(gh.PullRequest{
				ID: 9001, Number: 11, State: "open",
				Head:               gh.Branch{SHA: "abc"},
				RequestedReviewers: []gh.User{{Login: "heimdallm-bot"}},
			})
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
	)

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

	// First cycle: repo is brand new. Adapter must publish
	// EventRepoDiscovered and withhold the PR.
	out, err := a.FetchPRsToReview()
	if err != nil {
		t.Fatalf("first FetchPRsToReview: %v", err)
	}
	if len(out) != 0 {
		t.Fatalf("first cycle returned %d PR(s), want 0 (newly-discovered repo must defer review one tick)", len(out))
	}

	// Drain SSE events; we should see exactly one repo_discovered for `repo`.
	gotRepoDiscovered := false
	deadline := time.After(200 * time.Millisecond)
drain:
	for {
		select {
		case ev := <-sub:
			if ev.Type == sse.EventRepoDiscovered && testEventDataContains(ev.Data, repo) {
				gotRepoDiscovered = true
			}
		case <-deadline:
			break drain
		}
	}
	if !gotRepoDiscovered {
		t.Fatalf("expected EventRepoDiscovered for %q on first cycle", repo)
	}

	// Second cycle: repo is now known. The PR makes it through.
	out2, err := a.FetchPRsToReview()
	if err != nil {
		t.Fatalf("second FetchPRsToReview: %v", err)
	}
	if len(out2) != 1 || out2[0].Repo != repo {
		t.Fatalf("second cycle expected 1 PR for %q, got %+v", repo, out2)
	}
}

func testEventDataContains(data, needle string) bool {
	return len(data) > 0 && (data == needle || stringContains(data, needle))
}

func stringContains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
