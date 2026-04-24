package store_test

import (
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/store"
)

func seedIssueReview(t *testing.T, s *store.Store, issueID int64, at time.Time) {
	t.Helper()
	if _, err := s.InsertIssueReview(&store.IssueReview{
		IssueID:     issueID,
		CLIUsed:     "claude",
		Summary:     "s",
		Triage:      "{}",
		Suggestions: "[]",
		ActionTaken: "review_only",
		CreatedAt:   at,
	}); err != nil {
		t.Fatalf("insert review: %v", err)
	}
}

func TestCountIssueReviewsForIssue_CountsWithinWindow(t *testing.T) {
	s := newTestStore(t)
	issueID, err := s.UpsertIssue(&store.Issue{
		GithubID: 1, Repo: "org/r", Number: 1,
		Title: "t", Author: "a", State: "open",
		CreatedAt: time.Now(), FetchedAt: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}

	// Two recent triages and one outside the 24h window.
	recent := time.Now().Add(-2 * time.Hour)
	old := time.Now().Add(-48 * time.Hour)
	for _, at := range []time.Time{recent, recent.Add(time.Minute), old} {
		seedIssueReview(t, s, issueID, at)
	}

	since := time.Now().Add(-24 * time.Hour)
	n, err := s.CountIssueReviewsForIssue(issueID, since)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 2 {
		t.Errorf("want 2 within 24h, got %d", n)
	}
}

func TestCountIssueTriagesForRepo_CountsAcrossIssues(t *testing.T) {
	s := newTestStore(t)
	for i := int64(1); i <= 3; i++ {
		id, err := s.UpsertIssue(&store.Issue{
			GithubID: i, Repo: "org/r", Number: int(i),
			Title: "t", Author: "a", State: "open",
			CreatedAt: time.Now(), FetchedAt: time.Now(),
		})
		if err != nil {
			t.Fatal(err)
		}
		seedIssueReview(t, s, id, time.Now().Add(-10*time.Minute))
	}
	// An issue in a different repo — must NOT count.
	otherID, _ := s.UpsertIssue(&store.Issue{
		GithubID: 99, Repo: "other/r", Number: 99,
		Title: "t", Author: "a", State: "open",
		CreatedAt: time.Now(), FetchedAt: time.Now(),
	})
	seedIssueReview(t, s, otherID, time.Now().Add(-5*time.Minute))

	since := time.Now().Add(-1 * time.Hour)
	n, err := s.CountIssueTriagesForRepo("org/r", since)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 3 {
		t.Errorf("want 3 triages in org/r in last hour, got %d", n)
	}
}

func TestCheckIssueCircuitBreaker_TripsOnPerIssueCap(t *testing.T) {
	s := newTestStore(t)
	issueID, _ := s.UpsertIssue(&store.Issue{
		GithubID: 1, Repo: "org/r", Number: 1,
		Title: "t", Author: "a", State: "open",
		CreatedAt: time.Now(), FetchedAt: time.Now(),
	})
	// Seed 3 triages in the last 24h → cap is 3.
	for i := 0; i < 3; i++ {
		seedIssueReview(t, s, issueID, time.Now().Add(time.Duration(-i)*time.Minute))
	}
	cfg := store.IssueCircuitBreakerLimits{PerIssue24h: 3, PerRepoHr: 10}
	tripped, reason, err := s.CheckIssueCircuitBreaker(issueID, "org/r", cfg)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if !tripped {
		t.Errorf("expected tripped, got false (reason=%q)", reason)
	}
	if reason == "" {
		t.Errorf("tripped must include a human-readable reason")
	}
}

func TestCheckIssueCircuitBreaker_TripsOnPerRepoCap(t *testing.T) {
	s := newTestStore(t)
	// 10 triages spread across multiple issues in the same repo, all in last hour.
	for i := int64(1); i <= 10; i++ {
		id, _ := s.UpsertIssue(&store.Issue{
			GithubID: i, Repo: "org/r", Number: int(i),
			Title: "t", Author: "a", State: "open",
			CreatedAt: time.Now(), FetchedAt: time.Now(),
		})
		seedIssueReview(t, s, id, time.Now().Add(-5*time.Minute))
	}
	// A new issue in the same repo trying to triage → per-repo cap must trip.
	candidate, _ := s.UpsertIssue(&store.Issue{
		GithubID: 99, Repo: "org/r", Number: 99,
		Title: "t", Author: "a", State: "open",
		CreatedAt: time.Now(), FetchedAt: time.Now(),
	})
	cfg := store.IssueCircuitBreakerLimits{PerIssue24h: 3, PerRepoHr: 10}
	tripped, reason, err := s.CheckIssueCircuitBreaker(candidate, "org/r", cfg)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if !tripped {
		t.Errorf("expected tripped on per-repo cap, got false (reason=%q)", reason)
	}
}

func TestCheckIssueCircuitBreaker_AllowsUnderCap(t *testing.T) {
	s := newTestStore(t)
	issueID, _ := s.UpsertIssue(&store.Issue{
		GithubID: 2, Repo: "org/r", Number: 2,
		Title: "t", Author: "a", State: "open",
		CreatedAt: time.Now(), FetchedAt: time.Now(),
	})
	// 2 triages, cap 3 → must allow.
	for i := 0; i < 2; i++ {
		seedIssueReview(t, s, issueID, time.Now().Add(time.Duration(-i)*time.Minute))
	}
	cfg := store.IssueCircuitBreakerLimits{PerIssue24h: 3, PerRepoHr: 10}
	tripped, _, err := s.CheckIssueCircuitBreaker(issueID, "org/r", cfg)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if tripped {
		t.Errorf("expected allowed, got tripped")
	}
}

// Locks in the "0 = unlimited" contract on IssueCircuitBreakerLimits,
// mirroring the PR-side test TestCircuitBreaker_ZeroCapMeansUnlimited.
// Prevents an off-by-one regression that silently interprets zero as
// "trip immediately".
func TestCheckIssueCircuitBreaker_ZeroCapMeansUnlimited(t *testing.T) {
	s := newTestStore(t)
	issueID, _ := s.UpsertIssue(&store.Issue{
		GithubID: 1, Repo: "org/r", Number: 1,
		Title: "t", Author: "a", State: "open",
		CreatedAt: time.Now(), FetchedAt: time.Now(),
	})
	for i := 0; i < 50; i++ {
		seedIssueReview(t, s, issueID, time.Now().Add(time.Duration(-i)*time.Minute))
	}
	cfg := store.IssueCircuitBreakerLimits{PerIssue24h: 0, PerRepoHr: 0}
	tripped, _, err := s.CheckIssueCircuitBreaker(issueID, "org/r", cfg)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if tripped {
		t.Errorf("PerIssue24h=0 must be unlimited, got tripped")
	}
}
