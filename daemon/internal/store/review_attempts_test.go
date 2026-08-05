package store_test

import (
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/store"
)

func attemptStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// seedPR inserts a PR so the per-repo join has something to resolve.
func seedPR(t *testing.T, s *store.Store, githubID int64, repo string) int64 {
	t.Helper()
	id, err := s.UpsertPR(&store.PR{
		GithubID: githubID,
		Repo:     repo,
		Number:   int(githubID),
		Title:    "t",
		Author:   "alice",
		State:    "open",
		URL:      "https://example.test",
	})
	if err != nil {
		t.Fatalf("upsert pr: %v", err)
	}
	return id
}

// TestCircuitBreaker_CountsFailedRunsThatNeverProducedAReview is the regression
// test for theburrowhub/heimdallm#663.
//
// Three runs all die before InsertReview — the shape of the incident, where the
// CLI's output would not parse. Under the old implementation the breaker counted
// rows in `reviews`, so this scenario left the counter at 0 forever and the
// retry of the identical commit was unbounded. The cap must now be reached.
func TestCircuitBreaker_CountsFailedRunsThatNeverProducedAReview(t *testing.T) {
	s := attemptStore(t)
	prID := seedPR(t, s, 12, "org/repo")
	cfg := store.CircuitBreakerLimits{PerPR24h: 3}
	const sha = "31615409a9152613021ddccfec2c6e2001d4575d"

	for i := 1; i <= 3; i++ {
		tripped, _, err := s.CheckCircuitBreaker(prID, "org/repo", sha, cfg)
		if err != nil {
			t.Fatalf("check %d: %v", i, err)
		}
		if tripped {
			t.Fatalf("breaker tripped on run %d of 3; the cap must allow the configured number", i)
		}
		// Record the attempt, then return without ever inserting a review —
		// exactly what a parse failure does.
		if err := s.RecordReviewAttempt(prID, sha); err != nil {
			t.Fatalf("record attempt %d: %v", i, err)
		}
	}

	tripped, reason, err := s.CheckCircuitBreaker(prID, "org/repo", sha, cfg)
	if err != nil {
		t.Fatalf("final check: %v", err)
	}
	if !tripped {
		t.Fatal("breaker did not trip after 3 failed runs — a failing commit can still " +
			"be retried without limit, which is #663")
	}
	if reason == "" {
		t.Error("tripped with an empty reason; operators and the UI need the explanation")
	}
}

// TestCircuitBreaker_DoesNotDoubleCountSuccessfulRuns guards the cap's meaning.
// A successful run writes BOTH a ledger row and a reviews row; if the breaker
// summed the two sources it would halve the operator's configured cap.
func TestCircuitBreaker_DoesNotDoubleCountSuccessfulRuns(t *testing.T) {
	s := attemptStore(t)
	prID := seedPR(t, s, 13, "org/repo")
	cfg := store.CircuitBreakerLimits{PerPR24h: 3}
	const sha = "abc123"

	// Two complete runs: ledger row + review row each.
	for i := 0; i < 2; i++ {
		if err := s.RecordReviewAttempt(prID, sha); err != nil {
			t.Fatalf("record attempt: %v", err)
		}
		if _, err := s.InsertReview(&store.Review{
			PRID: prID, CLIUsed: "claude", Summary: "ok", Issues: "[]",
			Suggestions: "[]", Severity: "low", Event: "COMMENT",
			CreatedAt: time.Now().UTC(), HeadSHA: sha,
		}); err != nil {
			t.Fatalf("insert review: %v", err)
		}
	}

	tripped, reason, err := s.CheckCircuitBreaker(prID, "org/repo", sha, cfg)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if tripped {
		t.Fatalf("breaker tripped after 2 successful runs with a cap of 3 — the two "+
			"sources are being summed instead of maxed (reason: %s)", reason)
	}
}

// TestCircuitBreaker_CountsPreExistingReviewsWithoutLedgerRows covers the
// in-place upgrade. A database that predates the ledger has reviews history and
// no attempts; counting attempts alone would forget that history and hand out a
// fresh full quota on the first tick after deploy.
func TestCircuitBreaker_CountsPreExistingReviewsWithoutLedgerRows(t *testing.T) {
	s := attemptStore(t)
	prID := seedPR(t, s, 14, "org/repo")
	cfg := store.CircuitBreakerLimits{PerPR24h: 3}
	const sha = "legacy1"

	// Three reviews, no ledger rows — the state of an upgraded DB.
	for i := 0; i < 3; i++ {
		if _, err := s.InsertReview(&store.Review{
			PRID: prID, CLIUsed: "claude", Summary: "ok", Issues: "[]",
			Suggestions: "[]", Severity: "low", Event: "COMMENT",
			CreatedAt: time.Now().UTC(), HeadSHA: sha,
		}); err != nil {
			t.Fatalf("insert review: %v", err)
		}
	}

	tripped, _, err := s.CheckCircuitBreaker(prID, "org/repo", sha, cfg)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if !tripped {
		t.Fatal("breaker ignored pre-existing reviews with no ledger rows; an upgrade " +
			"would reset every PR's cap")
	}
}

// TestCircuitBreaker_AttemptsAreScopedToTheCommit keeps the fix from blocking
// legitimate work: a developer's follow-up commit must still be reviewable after
// an earlier commit exhausted its own cap.
func TestCircuitBreaker_AttemptsAreScopedToTheCommit(t *testing.T) {
	s := attemptStore(t)
	prID := seedPR(t, s, 15, "org/repo")
	cfg := store.CircuitBreakerLimits{PerPR24h: 3}

	for i := 0; i < 3; i++ {
		if err := s.RecordReviewAttempt(prID, "old-sha"); err != nil {
			t.Fatalf("record attempt: %v", err)
		}
	}
	if tripped, _, err := s.CheckCircuitBreaker(prID, "org/repo", "old-sha", cfg); err != nil {
		t.Fatalf("check old sha: %v", err)
	} else if !tripped {
		t.Fatal("expected the exhausted commit to be capped")
	}

	tripped, reason, err := s.CheckCircuitBreaker(prID, "org/repo", "new-sha", cfg)
	if err != nil {
		t.Fatalf("check new sha: %v", err)
	}
	if tripped {
		t.Fatalf("a new commit was capped by the previous commit's attempts (%s)", reason)
	}
}

// TestCircuitBreaker_PerRepoAxisCountsAttempts checks the hourly repo cap reads
// the ledger too — otherwise a repo where every run fails could burn credits
// across many PRs with nothing counting them.
func TestCircuitBreaker_PerRepoAxisCountsAttempts(t *testing.T) {
	s := attemptStore(t)
	cfg := store.CircuitBreakerLimits{PerRepoHr: 2}

	first := seedPR(t, s, 21, "org/repo")
	second := seedPR(t, s, 22, "org/repo")
	if err := s.RecordReviewAttempt(first, "sha1"); err != nil {
		t.Fatalf("record: %v", err)
	}
	if err := s.RecordReviewAttempt(second, "sha2"); err != nil {
		t.Fatalf("record: %v", err)
	}

	third := seedPR(t, s, 23, "org/repo")
	tripped, reason, err := s.CheckCircuitBreaker(third, "org/repo", "sha3", cfg)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if !tripped {
		t.Fatal("per-repo cap ignored failed runs on other PRs of the same repo")
	}
	if reason == "" {
		t.Error("expected a non-empty reason for the per-repo trip")
	}

	// A different repo must be unaffected.
	other := seedPR(t, s, 24, "org/other")
	if tripped, _, err := s.CheckCircuitBreaker(other, "org/other", "sha4", cfg); err != nil {
		t.Fatalf("check other repo: %v", err)
	} else if tripped {
		t.Error("one repo's attempts leaked into another repo's cap")
	}
}

// TestCircuitBreaker_AttemptsOutsideWindowDoNotCount confirms the ledger is read
// through a window rather than for all time, so a cap eventually releases.
func TestCircuitBreaker_AttemptsOutsideWindowDoNotCount(t *testing.T) {
	s := attemptStore(t)
	prID := seedPR(t, s, 31, "org/repo")

	if err := s.RecordReviewAttempt(prID, "sha"); err != nil {
		t.Fatalf("record: %v", err)
	}
	// A window in the future relative to the row: nothing should be counted.
	n, err := s.CountReviewAttemptsForPRHeadSHA(prID, "sha", time.Now().Add(1*time.Hour))
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Errorf("attempts inside a future window = %d, want 0", n)
	}

	n, err = s.CountReviewAttemptsForPRHeadSHA(prID, "sha", time.Now().Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Errorf("attempts inside the 24h window = %d, want 1", n)
	}
}

// TestPruneReviewAttempts_KeepsRowsInsideTheWidestWindow pins the retention
// contract: pruning must never remove a row the breaker would still count, or a
// live cap would silently reset.
func TestPruneReviewAttempts_KeepsRowsInsideTheWidestWindow(t *testing.T) {
	s := attemptStore(t)
	prID := seedPR(t, s, 41, "org/repo")

	if err := s.RecordReviewAttempt(prID, "sha"); err != nil {
		t.Fatalf("record: %v", err)
	}

	// The production sweep prunes at 48h, twice the widest breaker window.
	removed, err := s.PruneReviewAttempts(48 * time.Hour)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if removed != 0 {
		t.Errorf("pruned %d fresh rows; a row inside the 24h window must survive", removed)
	}

	// A negative max-age puts the cutoff in the future so the row is
	// unambiguously older. Passing 0 would NOT work: started_at has
	// second granularity and the comparison is strictly less-than, so a row
	// written in the same second as the cutoff survives — the dead zone
	// documented on PruneReviewAttempts and ClearAllInFlight (#544).
	removed, err = s.PruneReviewAttempts(-1 * time.Second)
	if err != nil {
		t.Fatalf("prune all: %v", err)
	}
	if removed != 1 {
		t.Errorf("pruned %d rows, want 1", removed)
	}
	n, err := s.CountReviewAttemptsForPR(prID, time.Now().Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Errorf("ledger still reports %d attempts after a full prune", n)
	}
}

// TestRecordReviewAttempt_AllowsEmptyHeadSHA covers the Search-API path, where
// the SHA is not yet known. The per-PR fallback axis must still see the run.
func TestRecordReviewAttempt_AllowsEmptyHeadSHA(t *testing.T) {
	s := attemptStore(t)
	prID := seedPR(t, s, 51, "org/repo")

	if err := s.RecordReviewAttempt(prID, ""); err != nil {
		t.Fatalf("record: %v", err)
	}
	n, err := s.CountReviewAttemptsForPR(prID, time.Now().Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Errorf("attempts = %d, want 1", n)
	}
}
