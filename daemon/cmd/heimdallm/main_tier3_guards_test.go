package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/config"
	gh "github.com/heimdallm/daemon/internal/github"
	"github.com/heimdallm/daemon/internal/scheduler"
	"github.com/heimdallm/daemon/internal/sse"
)

// TestTier3Adapter_HandleChange_SkipsClosedPR verifies the Tier 3 correctness
// fix: when the fresh snapshot shows a closed/merged PR, HandleChange must NOT
// call runReview and must publish a review_skipped SSE event with reason
// not_open. This is the bug we set out to fix in this plan.
func TestTier3Adapter_HandleChange_SkipsClosedPR(t *testing.T) {
	s := newMemStore(t)

	broker := sse.NewBroker()
	broker.Start()
	defer broker.Stop()

	// Subscribe BEFORE calling HandleChange so we don't miss the event.
	events := broker.Subscribe()
	defer broker.Unsubscribe(events)

	var (
		loginMu sync.Mutex
		login   = "heimdallm-bot"
		cfgMu   sync.Mutex
		cfg     = &config.Config{}
	)

	runReviewCalls := 0
	runReview := func(pr *gh.PullRequest, aiCfg config.RepoAI) {
		runReviewCalls++
	}

	a := &tier2Adapter{
		ghClient:  nil, // HandleChange on skip path does not touch ghClient
		store:     s,
		broker:    broker,
		cfgMu:     &cfgMu,
		cfg:       &cfg,
		loginMu:   &loginMu,
		login:     &login,
		runReview: runReview,
	}

	ctx := context.Background()
	item := &scheduler.WatchItem{
		Type:     "pr",
		Repo:     "org/repo",
		Number:   42,
		GithubID: 42,
		LastSeen: time.Now(),
	}
	snap := &scheduler.ItemSnapshot{
		State:     "closed",
		Draft:     false,
		Author:    "alice",
		UpdatedAt: time.Now().Add(time.Minute),
	}

	if err := a.HandleChange(ctx, item, snap); err != nil {
		t.Fatalf("HandleChange: %v", err)
	}
	if runReviewCalls != 0 {
		t.Errorf("runReview invoked on closed PR: calls=%d", runReviewCalls)
	}

	select {
	case ev := <-events:
		if ev.Type != sse.EventReviewSkipped {
			t.Errorf("event type = %q, want %q", ev.Type, sse.EventReviewSkipped)
		}
		var p struct {
			Reason   string `json:"reason"`
			PRNumber int    `json:"pr_number"`
			Repo     string `json:"repo"`
		}
		if err := json.Unmarshal([]byte(ev.Data), &p); err != nil {
			t.Fatalf("decode event: %v", err)
		}
		if p.Reason != "not_open" {
			t.Errorf("reason = %q, want not_open", p.Reason)
		}
		if p.PRNumber != 42 || p.Repo != "org/repo" {
			t.Errorf("payload = %+v, want pr=42 repo=org/repo", p)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("no SSE event emitted within 1s")
	}
}

// TestTier2Adapter_FetchPRsToReview_DedupsSkipEvents verifies that when a
// draft PR appears in consecutive GitHub search results with the same
// updated_at, FetchPRsToReview emits EventReviewSkipped only ONCE (on the
// first poll cycle), not on every subsequent cycle.
func TestTier2Adapter_FetchPRsToReview_DedupsSkipEvents(t *testing.T) {
	updatedAt := time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC)

	// The mock server returns the same draft PR on every request.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/user":
			json.NewEncoder(w).Encode(map[string]string{"login": "heimdallm-bot"})
		case "/search/issues":
			result := struct {
				Items []gh.PullRequest `json:"items"`
			}{Items: []gh.PullRequest{
				{
					ID:     101,
					Number: 7,
					Title:  "WIP: draft PR",
					Draft:  true,
					State:  "open",
					User:   gh.User{Login: "alice"},
					Head: gh.Branch{
						Repo: gh.Repo{FullName: "org/repo"},
					},
					UpdatedAt: updatedAt,
				},
			}}
			json.NewEncoder(w).Encode(result)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	s := newMemStore(t)
	broker := sse.NewBroker()
	broker.Start()
	defer broker.Stop()

	events := broker.Subscribe()
	defer broker.Unsubscribe(events)

	var (
		loginMu sync.Mutex
		login   = "heimdallm-bot"
		cfgMu   sync.Mutex
		cfg     = &config.Config{}
	)

	ghClient := gh.NewClient("fake-token", gh.WithBaseURL(srv.URL))

	a := &tier2Adapter{
		ghClient:             ghClient,
		store:                s,
		broker:               broker,
		cfgMu:                &cfgMu,
		cfg:                  &cfg,
		loginMu:              &loginMu,
		login:                &login,
		lastSkippedUpdatedAt: make(map[int64]time.Time),
	}

	// First cycle: draft PR must trigger exactly one EventReviewSkipped.
	if _, err := a.FetchPRsToReview(); err != nil {
		t.Fatalf("cycle 1 FetchPRsToReview: %v", err)
	}

	// Second cycle: same PR, same updated_at — no new event should be emitted.
	if _, err := a.FetchPRsToReview(); err != nil {
		t.Fatalf("cycle 2 FetchPRsToReview: %v", err)
	}

	// Drain the channel with a short timeout to count events.
	skipCount := 0
drain:
	for {
		select {
		case ev := <-events:
			if ev.Type == sse.EventReviewSkipped {
				skipCount++
			}
		case <-time.After(100 * time.Millisecond):
			break drain
		}
	}

	if skipCount != 1 {
		t.Errorf("EventReviewSkipped emitted %d time(s); want exactly 1 (dedup across cycles)", skipCount)
	}
}
