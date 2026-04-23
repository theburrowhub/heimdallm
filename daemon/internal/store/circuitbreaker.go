package store

import (
	"fmt"
	"time"
)

// CountReviewsForPR returns the number of reviews for the given PR whose
// created_at is at or after `since`. Used by the circuit breaker to cap
// runaway re-review loops. Only reviews already persisted to SQLite count —
// an in-flight review that has not called InsertReview yet is gated
// separately via the inflight table (Task 5).
func (s *Store) CountReviewsForPR(prID int64, since time.Time) (int, error) {
	var n int
	err := s.db.QueryRow(
		"SELECT COUNT(*) FROM reviews WHERE pr_id = ? AND created_at >= ?",
		prID, since.UTC().Format(sqliteTimeFormat),
	).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("store: count reviews for pr: %w", err)
	}
	return n, nil
}

// CountReviewsForRepo returns the number of reviews on ANY PR in the given
// repo whose created_at is at or after `since`. Used for the per-repo rate
// limit of the circuit breaker.
func (s *Store) CountReviewsForRepo(repo string, since time.Time) (int, error) {
	var n int
	err := s.db.QueryRow(`
		SELECT COUNT(*) FROM reviews r
		JOIN prs p ON r.pr_id = p.id
		WHERE p.repo = ? AND r.created_at >= ?`,
		repo, since.UTC().Format(sqliteTimeFormat),
	).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("store: count reviews for repo: %w", err)
	}
	return n, nil
}

// CircuitBreakerLimits is the configured set of caps. Enforced by
// CheckCircuitBreaker; zero values mean "unlimited" for that axis.
type CircuitBreakerLimits struct {
	PerPR24h  int // max reviews per PR in any 24h window
	PerRepoHr int // max reviews per repo in any 1h window
}

// CheckCircuitBreaker returns (tripped, reason, err). When tripped is true,
// the caller MUST NOT proceed to spend Claude credits for this PR. reason is
// a human-readable explanation suitable for logs and UI surfaces; it is
// empty when tripped is false.
func (s *Store) CheckCircuitBreaker(prID int64, repo string, cfg CircuitBreakerLimits) (bool, string, error) {
	if cfg.PerPR24h > 0 {
		n, err := s.CountReviewsForPR(prID, time.Now().Add(-24*time.Hour))
		if err != nil {
			return false, "", err
		}
		if n >= cfg.PerPR24h {
			return true, fmt.Sprintf("per-PR cap reached: %d reviews in last 24h (cap %d)", n, cfg.PerPR24h), nil
		}
	}
	if cfg.PerRepoHr > 0 && repo != "" {
		n, err := s.CountReviewsForRepo(repo, time.Now().Add(-1*time.Hour))
		if err != nil {
			return false, "", err
		}
		if n >= cfg.PerRepoHr {
			return true, fmt.Sprintf("per-repo cap reached: %d reviews on %s in last 1h (cap %d)", n, repo, cfg.PerRepoHr), nil
		}
	}
	return false, "", nil
}
