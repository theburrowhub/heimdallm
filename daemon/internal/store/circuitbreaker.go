package store

import (
	"fmt"
	"time"
)

// CountReviewsForPR returns the number of reviews for the given PR whose
// created_at is at or after `since`. Used by the circuit breaker to cap
// runaway re-review loops when the caller cannot scope the check to a HEAD
// SHA. Only reviews already persisted to SQLite count — an in-flight review
// that has not called InsertReview yet is gated separately via the inflight
// table (Task 5).
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

// CountReviewsForPRHeadSHA returns the number of reviews for the given PR and
// HEAD SHA whose created_at is at or after `since`. This is the PR-side
// breaker's primary axis: cap repeated reviews of the same commit while
// allowing a developer's follow-up commit to be reviewed normally.
func (s *Store) CountReviewsForPRHeadSHA(prID int64, headSHA string, since time.Time) (int, error) {
	var n int
	err := s.db.QueryRow(
		"SELECT COUNT(*) FROM reviews WHERE pr_id = ? AND head_sha = ? AND created_at >= ?",
		prID, headSHA, since.UTC().Format(sqliteTimeFormat),
	).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("store: count reviews for pr head sha: %w", err)
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
	PerPR24h  int // max review runs per PR HEAD SHA in any 24h window
	PerRepoHr int // max review runs per repo in any 1h window
}

// chargeableRuns returns how many review runs in the window should count against
// a cap, given the attempt count from the ledger and the row count from
// `reviews`.
//
// The larger of the two, deliberately:
//
//   - The ledger is authoritative going forward — it records every run that
//     reached the point of spending credits, including the ones that failed
//     before InsertReview and were therefore invisible to the old counter.
//   - `reviews` still has to be consulted because a database upgraded in place
//     has review history but no ledger rows for it. Counting attempts alone
//     would treat that history as if it never happened and hand out a fresh
//     full quota on the first tick after deploy.
//   - max rather than sum: a successful run writes one ledger row AND one
//     reviews row, so adding them would double-count every normal review and
//     halve the operator's configured cap.
//
// max is a floor, not an exact count, and it undercounts in one bounded case: a
// commit carrying N legacy reviews rows inside the live window plus M new ledger
// rows is charged max(N, M) rather than N+M, so up to 2*cap-1 runs can be
// chargeable. That is confined to the first window after an upgrade, since from
// then on every run writes a ledger row and the ledger dominates. Accepted in
// exchange for never double-counting during normal operation, which would be
// permanent.
func chargeableRuns(attempts, reviews int) int {
	if attempts > reviews {
		return attempts
	}
	return reviews
}

// CheckCircuitBreaker returns (tripped, reason, err). When tripped is true,
// the caller MUST NOT proceed to spend Claude credits for this PR. reason is
// a human-readable explanation suitable for logs and UI surfaces; it is
// empty when tripped is false.
//
// The caps count review *runs*, not published reviews. Counting only the latter
// was theburrowhub/heimdallm#663: a run whose CLI output failed to parse never
// reached InsertReview, so it advanced no counter, and the immediate retry of
// the same commit was unbounded. Callers must therefore record an attempt via
// RecordReviewAttempt once this check passes and before invoking the CLI.
func (s *Store) CheckCircuitBreaker(prID int64, repo, headSHA string, cfg CircuitBreakerLimits) (bool, string, error) {
	if cfg.PerPR24h > 0 {
		since := time.Now().Add(-24 * time.Hour)
		var (
			attempts, reviews int
			err               error
		)
		if headSHA != "" {
			if attempts, err = s.CountReviewAttemptsForPRHeadSHA(prID, headSHA, since); err != nil {
				return false, "", err
			}
			reviews, err = s.CountReviewsForPRHeadSHA(prID, headSHA, since)
		} else {
			if attempts, err = s.CountReviewAttemptsForPR(prID, since); err != nil {
				return false, "", err
			}
			reviews, err = s.CountReviewsForPR(prID, since)
		}
		if err != nil {
			return false, "", err
		}
		if n := chargeableRuns(attempts, reviews); n >= cfg.PerPR24h {
			if headSHA != "" {
				return true, fmt.Sprintf("per-PR HEAD cap reached: %d review runs on this commit in last 24h (cap %d)", n, cfg.PerPR24h), nil
			}
			return true, fmt.Sprintf("per-PR cap reached: %d review runs in last 24h (cap %d)", n, cfg.PerPR24h), nil
		}
	}
	if cfg.PerRepoHr > 0 && repo != "" {
		since := time.Now().Add(-1 * time.Hour)
		attempts, err := s.CountReviewAttemptsForRepo(repo, since)
		if err != nil {
			return false, "", err
		}
		reviews, err := s.CountReviewsForRepo(repo, since)
		if err != nil {
			return false, "", err
		}
		if n := chargeableRuns(attempts, reviews); n >= cfg.PerRepoHr {
			return true, fmt.Sprintf("per-repo cap reached: %d review runs on %s in last 1h (cap %d)", n, repo, cfg.PerRepoHr), nil
		}
	}
	return false, "", nil
}
