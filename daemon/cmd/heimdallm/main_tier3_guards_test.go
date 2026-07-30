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
	"github.com/heimdallm/daemon/internal/pipeline"
	"github.com/heimdallm/daemon/internal/scheduler"
	"github.com/heimdallm/daemon/internal/sse"
	"github.com/heimdallm/daemon/internal/store"
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
		cfg     = &config.Config{GitHub: config.GitHubConfig{
			Repositories: []string{"org/repo"},
		}}
	)

	runReviewCalls := 0
	runReview := func(pr *gh.PullRequest, aiCfg config.RepoAI) *store.Review {
		runReviewCalls++
		return nil
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

func TestTier3Adapter_HandleChange_SkipsNonMonitoredRepo(t *testing.T) {
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
		cfg     = &config.Config{GitHub: config.GitHubConfig{
			Repositories: []string{"org/repo"},
			NonMonitored: []string{"org/repo"},
		}}
	)
	runReviewCalls := 0
	a := &tier2Adapter{
		store:   s,
		broker:  broker,
		cfgMu:   &cfgMu,
		cfg:     &cfg,
		loginMu: &loginMu,
		login:   &login,
		runReview: func(*gh.PullRequest, config.RepoAI) *store.Review {
			runReviewCalls++
			return nil
		},
	}

	err := a.HandleChange(context.Background(), &scheduler.WatchItem{
		Type: "pr", Repo: "org/repo", Number: 42, GithubID: 42,
	}, &scheduler.ItemSnapshot{
		State: "open", Author: "alice", UpdatedAt: time.Now(), HeadSHA: "abc",
	})
	if err != nil {
		t.Fatalf("HandleChange: %v", err)
	}
	if runReviewCalls != 0 {
		t.Fatalf("runReview called %d time(s), want 0", runReviewCalls)
	}

	select {
	case ev := <-events:
		if ev.Type != sse.EventReviewSkipped {
			t.Fatalf("event type = %q, want %q", ev.Type, sse.EventReviewSkipped)
		}
		var payload struct {
			Reason string `json:"reason"`
		}
		if err := json.Unmarshal([]byte(ev.Data), &payload); err != nil {
			t.Fatalf("decode event: %v", err)
		}
		if payload.Reason != string(pipeline.SkipReasonNotMonitored) {
			t.Fatalf("reason = %q, want %q", payload.Reason, pipeline.SkipReasonNotMonitored)
		}
	case <-time.After(time.Second):
		t.Fatal("no review_skipped event emitted")
	}
}

func TestTier3Adapter_CheckItemDetectsNewHeadWithSameUpdatedAt(t *testing.T) {
	observedAt := time.Date(2026, 7, 30, 10, 0, 0, 0, time.UTC)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/org/repo/pulls/42" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"state":      "open",
			"draft":      false,
			"user":       map[string]string{"login": "alice"},
			"updated_at": observedAt.Format(time.RFC3339),
			"head":       map[string]string{"sha": "head-b"},
		})
	}))
	defer srv.Close()

	a := &tier2Adapter{
		ghClient: gh.NewClient("fake-token", gh.WithBaseURL(srv.URL)),
		store:    newMemStore(t),
	}
	changed, snap, err := a.CheckItem(context.Background(), &scheduler.WatchItem{
		Type:        "pr",
		Repo:        "org/repo",
		Number:      42,
		GithubID:    4242,
		LastSeen:    observedAt,
		LastHeadSHA: "head-a",
	})
	if err != nil {
		t.Fatalf("CheckItem: %v", err)
	}
	if !changed || snap == nil || snap.HeadSHA != "head-b" {
		t.Fatalf("changed=%v snap=%+v, want same-second head-b detected", changed, snap)
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
					ID:            101,
					Number:        7,
					Title:         "WIP: draft PR",
					Draft:         true,
					State:         "open",
					User:          gh.User{Login: "alice"},
					RepositoryURL: "https://api.github.com/repos/org/repo",
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
