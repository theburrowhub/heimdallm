package store_test

import (
	"database/sql"
	"path/filepath"
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

func TestCountReviewsForPRHeadSHA_CountsOnlyMatchingHead(t *testing.T) {
	s := newTestStore(t)
	prID, err := s.UpsertPR(&store.PR{GithubID: 1, Repo: "org/r", Number: 1,
		Title: "t", State: "open", UpdatedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}

	recent := time.Now().Add(-2 * time.Hour)
	for _, headSHA := range []string{"abc", "abc", "def"} {
		if _, err := s.InsertReview(&store.Review{
			PRID: prID, CLIUsed: "claude", Issues: "[]", Suggestions: "[]",
			Severity: "low", CreatedAt: recent, HeadSHA: headSHA,
		}); err != nil {
			t.Fatal(err)
		}
	}

	since := time.Now().Add(-24 * time.Hour)
	n, err := s.CountReviewsForPRHeadSHA(prID, "abc", since)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 2 {
		t.Errorf("want 2 reviews for head abc, got %d", n)
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
			HeadSHA: "abc",
		}); err != nil {
			t.Fatal(err)
		}
	}

	cfg := store.CircuitBreakerLimits{
		PerPR24h:  3,
		PerRepoHr: 20,
	}
	tripped, reason, err := s.CheckCircuitBreaker(prID, "org/r", "abc", cfg)
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

func TestCircuitBreaker_AllowsDifferentHeadSHAUnderPerPRCap(t *testing.T) {
	s := newTestStore(t)
	prID, _ := s.UpsertPR(&store.PR{GithubID: 4, Repo: "org/r", Number: 4,
		Title: "t", State: "open", UpdatedAt: time.Now()})
	// Three reviews on the previous commit must not consume the allowance for
	// a developer's follow-up commit.
	for i := 0; i < 3; i++ {
		if _, err := s.InsertReview(&store.Review{
			PRID: prID, CLIUsed: "claude", Issues: "[]", Suggestions: "[]",
			Severity: "low", CreatedAt: time.Now().Add(time.Duration(-i) * time.Minute),
			HeadSHA: "old",
		}); err != nil {
			t.Fatal(err)
		}
	}
	cfg := store.CircuitBreakerLimits{PerPR24h: 3, PerRepoHr: 20}
	tripped, reason, err := s.CheckCircuitBreaker(prID, "org/r", "new", cfg)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if tripped {
		t.Errorf("new head SHA should be allowed despite previous-head cap (reason=%q)", reason)
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
			HeadSHA: "abc",
		}); err != nil {
			t.Fatal(err)
		}
	}
	cfg := store.CircuitBreakerLimits{PerPR24h: 3, PerRepoHr: 20}
	tripped, _, err := s.CheckCircuitBreaker(prID, "org/r", "abc", cfg)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if tripped {
		t.Errorf("expected allowed, got tripped")
	}
}

// TestCircuitBreaker_ZeroCapMeansUnlimited locks in the contract documented
// on CircuitBreakerLimits: a cap of 0 disables that axis entirely. Without
// this test the "0 = unlimited" behaviour could silently regress to "0 means
// trip immediately" via an off-by-one in CheckCircuitBreaker.
func TestCircuitBreaker_ZeroCapMeansUnlimited(t *testing.T) {
	s := newTestStore(t)
	prID, _ := s.UpsertPR(&store.PR{GithubID: 1, Repo: "org/r", Number: 1,
		Title: "t", State: "open", UpdatedAt: time.Now()})
	// Seed 100 reviews; unlimited cap means no trip.
	for i := 0; i < 100; i++ {
		if _, err := s.InsertReview(&store.Review{
			PRID: prID, CLIUsed: "claude", Issues: "[]", Suggestions: "[]",
			Severity: "low", CreatedAt: time.Now().Add(time.Duration(-i) * time.Minute),
			HeadSHA: "abc",
		}); err != nil {
			t.Fatal(err)
		}
	}
	cfg := store.CircuitBreakerLimits{PerPR24h: 0, PerRepoHr: 0}
	tripped, _, err := s.CheckCircuitBreaker(prID, "org/r", "abc", cfg)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if tripped {
		t.Errorf("PerPR24h=0 must be unlimited, got tripped")
	}
}

// TestCircuitBreaker_IgnoresLegacyFailedAttempts covers upgrades from the
// attempt-ledger release. Existing databases can contain rows for terminated
// executions that never produced a review; those rows must stop blocking both
// breaker axes immediately after this version starts.
func TestCircuitBreaker_IgnoresLegacyFailedAttempts(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "legacy-attempts.db")
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	prID, err := s.UpsertPR(&store.PR{
		GithubID: 73, Repo: "org/repo", Number: 73, Title: "t", State: "open",
		UpdatedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("upsert pr: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	legacyDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open legacy db: %v", err)
	}
	if _, err := legacyDB.Exec(`CREATE TABLE review_attempts (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		pr_id INTEGER NOT NULL REFERENCES prs(id),
		head_sha TEXT NOT NULL,
		started_at DATETIME NOT NULL
	)`); err != nil {
		legacyDB.Close()
		t.Fatalf("create legacy attempt table: %v", err)
	}
	for i := 0; i < 5; i++ {
		if _, err := legacyDB.Exec(
			"INSERT INTO review_attempts (pr_id, head_sha, started_at) VALUES (?, ?, ?)",
			prID, "sha", time.Now().UTC().Format(time.RFC3339),
		); err != nil {
			legacyDB.Close()
			t.Fatalf("insert legacy attempt %d: %v", i, err)
		}
	}
	if err := legacyDB.Close(); err != nil {
		t.Fatalf("close legacy db: %v", err)
	}

	s, err = store.Open(dbPath)
	if err != nil {
		t.Fatalf("reopen upgraded store: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	tripped, reason, err := s.CheckCircuitBreaker(prID, "org/repo", "sha",
		store.CircuitBreakerLimits{PerPR24h: 3, PerRepoHr: 3})
	if err != nil {
		t.Fatalf("check breaker: %v", err)
	}
	if tripped {
		t.Fatalf("legacy failed attempts tripped the breaker without reviews: %s", reason)
	}
}
