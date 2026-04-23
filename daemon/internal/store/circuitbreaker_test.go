package store_test

import (
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/store"
)

func TestCountReviewsForPR_CountsWithinWindow(t *testing.T) {
	s := newTestStore(t)
	prID, err := s.UpsertPR(&store.PR{GithubID: 1, Repo: "org/r", Number: 1,
		Title: "t", State: "open", UpdatedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}

	// Insert three reviews, two recent and one outside the 24h window.
	recent := time.Now().Add(-2 * time.Hour)
	old := time.Now().Add(-48 * time.Hour)
	for _, at := range []time.Time{recent, recent.Add(time.Minute), old} {
		if _, err := s.InsertReview(&store.Review{
			PRID: prID, CLIUsed: "claude", Issues: "[]", Suggestions: "[]",
			Severity: "low", CreatedAt: at,
		}); err != nil {
			t.Fatal(err)
		}
	}

	since := time.Now().Add(-24 * time.Hour)
	n, err := s.CountReviewsForPR(prID, since)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 2 {
		t.Errorf("want 2 within 24h, got %d", n)
	}
}

func TestCountReviewsForRepo_CountsDistinctPRs(t *testing.T) {
	s := newTestStore(t)
	for i := int64(1); i <= 3; i++ {
		prID, _ := s.UpsertPR(&store.PR{GithubID: i, Repo: "org/r", Number: int(i),
			Title: "t", State: "open", UpdatedAt: time.Now()})
		if _, err := s.InsertReview(&store.Review{
			PRID: prID, CLIUsed: "claude", Issues: "[]", Suggestions: "[]",
			Severity: "low", CreatedAt: time.Now().Add(-10 * time.Minute),
		}); err != nil {
			t.Fatal(err)
		}
	}
	since := time.Now().Add(-1 * time.Hour)
	n, err := s.CountReviewsForRepo("org/r", since)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 3 {
		t.Errorf("want 3 reviews in last hour, got %d", n)
	}
}

func TestCircuitBreaker_TripsOnPerPRCap(t *testing.T) {
	s := newTestStore(t)
	prID, _ := s.UpsertPR(&store.PR{GithubID: 1, Repo: "org/r", Number: 1,
		Title: "t", State: "open", UpdatedAt: time.Now()})
	// Seed 3 reviews in the last 24h → cap is 3.
	for i := 0; i < 3; i++ {
		if _, err := s.InsertReview(&store.Review{
			PRID: prID, CLIUsed: "claude", Issues: "[]", Suggestions: "[]",
			Severity: "low", CreatedAt: time.Now().Add(time.Duration(-i) * time.Minute),
		}); err != nil {
			t.Fatal(err)
		}
	}

	cfg := store.CircuitBreakerLimits{
		PerPR24h:  3,
		PerRepoHr: 20,
	}
	tripped, reason, err := s.CheckCircuitBreaker(prID, "org/r", cfg)
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

func TestCircuitBreaker_AllowsUnderCap(t *testing.T) {
	s := newTestStore(t)
	prID, _ := s.UpsertPR(&store.PR{GithubID: 2, Repo: "org/r", Number: 2,
		Title: "t", State: "open", UpdatedAt: time.Now()})
	// 2 reviews, cap 3 → must allow.
	for i := 0; i < 2; i++ {
		if _, err := s.InsertReview(&store.Review{
			PRID: prID, CLIUsed: "claude", Issues: "[]", Suggestions: "[]",
			Severity: "low", CreatedAt: time.Now().Add(time.Duration(-i) * time.Minute),
		}); err != nil {
			t.Fatal(err)
		}
	}
	cfg := store.CircuitBreakerLimits{PerPR24h: 3, PerRepoHr: 20}
	tripped, _, err := s.CheckCircuitBreaker(prID, "org/r", cfg)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if tripped {
		t.Errorf("expected allowed, got tripped")
	}
}
