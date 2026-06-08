package store_test

import (
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/store"
)

// TestStore_PRReviewState_RoundTrip pins that the new
// external_review_state / external_reviewer / external_review_at columns
// added for issue #482 survive a write-and-read cycle. Tier 3 writes
// them; Flutter reads them through GetPRByGithubID + the dashboard
// providers.
func TestStore_PRReviewState_RoundTrip(t *testing.T) {
	s := newTestStore(t)
	now := time.Now().UTC().Truncate(time.Second)
	id, err := s.UpsertPR(&store.PR{
		GithubID: 9001, Repo: "org/r", Number: 9, Title: "PR",
		Author: "heimdallm-bot", URL: "u", State: "open",
		UpdatedAt: now, FetchedAt: now,
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}

	got, err := s.GetPRByGithubID(9001)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.ExternalReviewState != "" {
		t.Errorf("pre-write ExternalReviewState = %q, want empty", got.ExternalReviewState)
	}

	at := now.Add(2 * time.Minute)
	if err := s.UpdatePRReviewState(id, "CHANGES_REQUESTED", "alice", at); err != nil {
		t.Fatalf("update: %v", err)
	}

	got, err = s.GetPRByGithubID(9001)
	if err != nil {
		t.Fatalf("get after update: %v", err)
	}
	if got.ExternalReviewState != "CHANGES_REQUESTED" {
		t.Errorf("ExternalReviewState = %q, want CHANGES_REQUESTED", got.ExternalReviewState)
	}
	if got.ExternalReviewer != "alice" {
		t.Errorf("ExternalReviewer = %q, want alice", got.ExternalReviewer)
	}
	if !got.ExternalReviewAt.Equal(at) {
		t.Errorf("ExternalReviewAt = %v, want %v", got.ExternalReviewAt, at)
	}
}

// TestStore_MarkPRAutoImplementOrigin asserts that the back-link from
// PR to the originating issue is persistent. Tier 3 uses this to decide
// which PRs are eligible for review-state vigilance — a PR with
// AutoImplementIssueID==0 falls through to the standard PR-review path.
func TestStore_MarkPRAutoImplementOrigin(t *testing.T) {
	s := newTestStore(t)
	now := time.Now().UTC().Truncate(time.Second)
	prID, err := s.UpsertPR(&store.PR{
		GithubID: 9002, Repo: "org/r", Number: 10, Title: "PR",
		Author: "heimdallm-bot", URL: "u", State: "open",
		UpdatedAt: now, FetchedAt: now,
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}

	if err := s.MarkPRAutoImplementOrigin(prID, 4242); err != nil {
		t.Fatalf("mark: %v", err)
	}

	got, err := s.GetPRByGithubID(9002)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.AutoImplementIssueID != 4242 {
		t.Errorf("AutoImplementIssueID = %d, want 4242", got.AutoImplementIssueID)
	}
}

// TestStore_IncrementPRReviewResponseCount_ReturnsNewValue pins the
// atomicity contract the Responder relies on for its per-PR-24h cap:
// the increment returns the post-increment value so the caller can
// compare against the cap in a single round-trip.
func TestStore_IncrementPRReviewResponseCount_ReturnsNewValue(t *testing.T) {
	s := newTestStore(t)
	now := time.Now().UTC().Truncate(time.Second)
	prID, err := s.UpsertPR(&store.PR{
		GithubID: 9003, Repo: "org/r", Number: 11, Title: "PR",
		Author: "bot", URL: "u", State: "open",
		UpdatedAt: now, FetchedAt: now,
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}

	for want := 1; want <= 3; want++ {
		got, err := s.IncrementPRReviewResponseCount(prID)
		if err != nil {
			t.Fatalf("increment #%d: %v", want, err)
		}
		if got != want {
			t.Errorf("increment #%d returned %d, want %d", want, got, want)
		}
	}

	// Same shape for the fix counter — phase 3 reuses the pattern.
	for want := 1; want <= 2; want++ {
		got, err := s.IncrementPRReviewFixCount(prID)
		if err != nil {
			t.Fatalf("fix increment #%d: %v", want, err)
		}
		if got != want {
			t.Errorf("fix increment #%d returned %d, want %d", want, got, want)
		}
	}
}
