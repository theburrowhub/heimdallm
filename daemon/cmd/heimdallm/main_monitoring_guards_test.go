package main

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/bus"
	gh "github.com/heimdallm/daemon/internal/github"
	"github.com/heimdallm/daemon/internal/pipeline"
	"github.com/heimdallm/daemon/internal/store"
)

func TestDeferPublishIfUnmonitoredLeavesReviewPending(t *testing.T) {
	s := newMemStore(t)
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	reviewID := seedPRWithReview(t, s, 901, now.Add(-time.Minute))

	var checkedRepo string
	deferred := deferPublishIfUnmonitored(
		"org/repo",
		func(repo string) bool {
			checkedRepo = repo
			return false
		},
	)
	if !deferred {
		t.Fatal("deferred = false, want true for an unmonitored repo")
	}
	if checkedRepo != "org/repo" {
		t.Fatalf("eligibility checked repo = %q, want org/repo", checkedRepo)
	}

	review, err := s.GetReview(reviewID)
	if err != nil {
		t.Fatalf("get review: %v", err)
	}
	if review.GitHubReviewID != 0 {
		t.Fatalf("GitHubReviewID = %d, want 0 pending", review.GitHubReviewID)
	}
	if !review.PublishedAt.IsZero() {
		t.Fatalf("PublishedAt = %v, want zero while deferred", review.PublishedAt)
	}

	pending, err := s.ListUnpublishedReviews()
	if err != nil {
		t.Fatalf("list unpublished reviews: %v", err)
	}
	if len(pending) != 1 || pending[0].ID != reviewID {
		t.Fatalf("unpublished reviews = %+v, want deferred review %d", pending, reviewID)
	}

	if deferPublishIfUnmonitored("org/repo", func(string) bool { return true }) {
		t.Fatal("deferred = true after repo re-enable")
	}
}

func TestPendingReviewInvalidReason(t *testing.T) {
	tests := []struct {
		name     string
		review   *store.Review
		snapshot *gh.PRSnapshot
		want     pipeline.SkipReason
	}{
		{
			name:     "same head remains publishable",
			review:   &store.Review{HeadSHA: "abc"},
			snapshot: &gh.PRSnapshot{State: "open", HeadSHA: "abc"},
		},
		{
			name:     "changed head retires stale review",
			review:   &store.Review{HeadSHA: "abc"},
			snapshot: &gh.PRSnapshot{State: "open", HeadSHA: "def"},
			want:     pipeline.SkipReasonHeadChanged,
		},
		{
			name:     "changed base retires stale review",
			review:   &store.Review{HeadSHA: "abc", BaseSHA: "base-a"},
			snapshot: &gh.PRSnapshot{State: "open", HeadSHA: "abc", BaseSHA: "base-b"},
			want:     pipeline.SkipReasonBaseChanged,
		},
		{
			name:     "closed PR wins over head mismatch",
			review:   &store.Review{HeadSHA: "abc"},
			snapshot: &gh.PRSnapshot{State: "closed", HeadSHA: "def"},
			want:     pipeline.SkipReasonNotOpen,
		},
		{
			name:     "legacy empty review head keeps prior behavior",
			review:   &store.Review{},
			snapshot: &gh.PRSnapshot{State: "open", HeadSHA: "def"},
		},
		{
			name:     "empty current head retries instead of retiring",
			review:   &store.Review{HeadSHA: "abc"},
			snapshot: &gh.PRSnapshot{State: "open"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := pendingReviewInvalidReason(tt.review, tt.snapshot); got != tt.want {
				t.Fatalf("pendingReviewInvalidReason = %q, want %q", got, tt.want)
			}
		})
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
