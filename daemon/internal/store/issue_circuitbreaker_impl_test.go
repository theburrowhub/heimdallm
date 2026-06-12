package store_test

import (
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/store"
)

// seedImplementReview inserts an issue row (if needed to satisfy the FK) and
// one issue_reviews row with the given repo, action, and timestamp. The issue
// is identified by number so multiple calls with the same repo+number reuse
// the same issue row via UpsertIssue's ON CONFLICT logic.
func seedImplementReview(t *testing.T, s *store.Store, repo string, issueNumber int, actionTaken string, createdAt time.Time) {
	t.Helper()
	issueID, err := s.UpsertIssue(&store.Issue{
		GithubID:  int64(issueNumber + 100000), // avoid collisions with other tests
		Repo:      repo,
		Number:    issueNumber,
		Title:     "test issue",
		Author:    "tester",
		State:     "open",
		CreatedAt: time.Now(),
		FetchedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("seedImplementReview: upsert issue: %v", err)
	}
	if _, err := s.InsertIssueReview(&store.IssueReview{
		IssueID:     issueID,
		CLIUsed:     "claude",
		Summary:     "s",
		Triage:      "{}",
		Suggestions: "[]",
		ActionTaken: actionTaken,
		CreatedAt:   createdAt,
	}); err != nil {
		t.Fatalf("seedImplementReview: insert issue_review: %v", err)
	}
}

func TestCheckImplementCircuitBreaker_PerRepoHr(t *testing.T) {
	s := newTestStore(t)

	repo := "acme/widget"
	// Seed one row for each of the four literals in the implement IN clause so
	// that a misspelled literal would change the count and fail this test.
	seedImplementReview(t, s, repo, 101, "develop", time.Now().Add(-10*time.Minute))
	seedImplementReview(t, s, repo, 102, "auto_implement_failed", time.Now().Add(-20*time.Minute))
	seedImplementReview(t, s, repo, 103, "auto_implement", time.Now().Add(-30*time.Minute))
	seedImplementReview(t, s, repo, 104, "auto_implement_no_changes", time.Now().Add(-40*time.Minute))
	// A triage row that must NOT be counted by the implement breaker.
	seedImplementReview(t, s, repo, 105, "review_only", time.Now().Add(-5*time.Minute))

	tripped, reason, err := s.CheckImplementCircuitBreaker(repo, 4)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if !tripped {
		t.Fatalf("want tripped at cap 4 with 4 implements (all four literals counted), got not tripped")
	}
	if reason == "" {
		t.Errorf("want non-empty reason when tripped")
	}

	tripped, _, err = s.CheckImplementCircuitBreaker(repo, 5)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if tripped {
		t.Errorf("want not tripped at cap 5 with 4 implements")
	}

	tripped, _, _ = s.CheckImplementCircuitBreaker(repo, 0)
	if tripped {
		t.Errorf("cap 0 must mean unlimited")
	}
}
