package main

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/bus"
	"github.com/heimdallm/daemon/internal/store"
)

func TestCancelPublishIfUnmonitoredMarksReviewTerminal(t *testing.T) {
	s := newMemStore(t)
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	reviewID := seedPRWithReview(t, s, 901, now.Add(-time.Minute))

	var checkedRepo string
	cancelled, err := cancelPublishIfUnmonitored(
		s,
		reviewID,
		"org/repo",
		func(repo string) bool {
			checkedRepo = repo
			return false
		},
		now,
	)
	if err != nil {
		t.Fatalf("cancel publish: %v", err)
	}
	if !cancelled {
		t.Fatal("cancelled = false, want true for an unmonitored repo")
	}
	if checkedRepo != "org/repo" {
		t.Fatalf("eligibility checked repo = %q, want org/repo", checkedRepo)
	}

	review, err := s.GetReview(reviewID)
	if err != nil {
		t.Fatalf("get review: %v", err)
	}
	if review.GitHubReviewID != -1 {
		t.Fatalf("GitHubReviewID = %d, want -1 terminal sentinel", review.GitHubReviewID)
	}
	if !review.PublishedAt.Equal(now) {
		t.Fatalf("PublishedAt = %v, want %v", review.PublishedAt, now)
	}

	pending, err := s.ListUnpublishedReviews()
	if err != nil {
		t.Fatalf("list unpublished reviews: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("unpublished reviews = %d, want 0 so PublishPending cannot re-enqueue it", len(pending))
	}
}

func TestEnrollOpenItemsSkipsDisabledRowsBeforeBatchLimit(t *testing.T) {
	s := newMemStore(t)
	ws, err := bus.NewWatchStore(s.DB())
	if err != nil {
		t.Fatalf("new watch store: %v", err)
	}
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)

	for i := int64(1); i <= 10; i++ {
		if _, err := s.UpsertPR(&store.PR{
			GithubID:  i,
			Repo:      fmt.Sprintf("org/disabled-%d", i),
			Number:    int(i),
			Title:     "disabled repo PR",
			Author:    "alice",
			URL:       fmt.Sprintf("https://github.test/org/disabled-%d/pull/%d", i, i),
			State:     "open",
			UpdatedAt: now,
			FetchedAt: now,
		}); err != nil {
			t.Fatalf("upsert disabled PR %d: %v", i, err)
		}
	}

	const monitoredID int64 = 101
	if _, err := s.UpsertPR(&store.PR{
		GithubID:  monitoredID,
		Repo:      "org/monitored",
		Number:    101,
		Title:     "monitored repo PR",
		Author:    "bob",
		URL:       "https://github.test/org/monitored/pull/101",
		State:     "open",
		UpdatedAt: now,
		FetchedAt: now,
	}); err != nil {
		t.Fatalf("upsert monitored PR: %v", err)
	}

	enrollOpenItems(context.Background(), s, ws, []string{"org/monitored"})

	entry, err := ws.Get(context.Background(), "pr.101")
	if err != nil {
		t.Fatalf("get monitored watch entry: %v", err)
	}
	if entry.Repo != "org/monitored" {
		t.Fatalf("watch repo = %q, want org/monitored", entry.Repo)
	}
	if _, err := ws.Get(context.Background(), "pr.1"); err == nil {
		t.Fatal("disabled repo PR was enrolled")
	}
}

func TestEnrollOpenItemsChunksLargeMonitoredRepoSets(t *testing.T) {
	s := newMemStore(t)
	ws, err := bus.NewWatchStore(s.DB())
	if err != nil {
		t.Fatalf("new watch store: %v", err)
	}
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	const targetID int64 = 202
	if _, err := s.UpsertPR(&store.PR{
		GithubID: targetID, Repo: "org/target", Number: 202,
		Title: "target", Author: "alice", URL: "https://github.test/org/target/pull/202",
		State: "open", UpdatedAt: now, FetchedAt: now,
	}); err != nil {
		t.Fatalf("upsert target PR: %v", err)
	}

	repos := make([]string, 0, 502)
	for i := range 501 {
		repos = append(repos, fmt.Sprintf("org/empty-%d", i))
	}
	repos = append(repos, "org/target") // forces the second SQL chunk

	enrollOpenItems(context.Background(), s, ws, repos)
	if _, err := ws.Get(context.Background(), "pr.202"); err != nil {
		t.Fatalf("target in later repo chunk was not enrolled: %v", err)
	}
}
