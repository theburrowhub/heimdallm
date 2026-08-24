package store

import (
	"database/sql"
	"fmt"
	"time"
)

const (
	reviewRetryBaseDelay = 5 * time.Minute
	reviewRetryMaxDelay  = 6 * time.Hour
)

// ReviewExecutionStatus is the durable state of the latest review execution
// that is active or failed before producing a review row. It is derived from
// the retry ledger so a newly connected UI sees active work and can cancel it
// without having observed review_started over SSE.
type ReviewExecutionStatus struct {
	HeadSHA  string    `json:"head_sha"`
	Attempts int       `json:"attempts"`
	FailedAt time.Time `json:"failed_at"`
	RetryAt  time.Time `json:"retry_at"`
	Error    string    `json:"error"`
	Active   bool      `json:"active"`
}

// CheckReviewRetryBackoff reports whether an automatic review of this exact PR
// HEAD should be deferred after earlier executions failed to produce a durable
// review. retryAt and attempts are populated whenever state exists, including
// when its delay has already expired.
//
// This is deliberately independent from CheckCircuitBreaker: retry backoff
// limits how often failures may run, while the breaker counts only completed
// reviews.
func (s *Store) CheckReviewRetryBackoff(
	prID int64,
	headSHA string,
	now time.Time,
) (blocked bool, retryAt time.Time, attempts int, err error) {
	if headSHA == "" {
		return false, time.Time{}, 0, nil
	}

	var lastAttempt string
	err = s.db.QueryRow(`
		SELECT consecutive_attempts, last_attempt_at
		FROM review_retry_backoff
		WHERE pr_id = ? AND head_sha = ?`,
		prID, headSHA,
	).Scan(&attempts, &lastAttempt)
	if err != nil {
		if err == sql.ErrNoRows {
			return false, time.Time{}, 0, nil
		}
		return false, time.Time{}, 0, fmt.Errorf("store: check review retry backoff: %w", err)
	}

	lastAttemptAt, parseErr := time.Parse(sqliteTimeFormat, lastAttempt)
	if parseErr != nil {
		return false, time.Time{}, 0,
			fmt.Errorf("store: parse review retry last_attempt_at %q: %w", lastAttempt, parseErr)
	}
	retryAt = lastAttemptAt.Add(reviewRetryDelay(attempts))
	return now.UTC().Before(retryAt), retryAt, attempts, nil
}

// AdvanceReviewRetryBackoff marks the start of a review execution. It is
// written immediately before Execute and cleared only after InsertReview, so a
// returned error, parse/storage failure, or daemon death leaves a persistent
// cooldown marker. Repeated starts without a durable review increase the delay
// exponentially.
func (s *Store) AdvanceReviewRetryBackoff(prID int64, headSHA string, startedAt time.Time) error {
	if headSHA == "" {
		return nil
	}
	_, err := s.db.Exec(`
		INSERT INTO review_retry_backoff (
			pr_id, head_sha, consecutive_attempts, last_attempt_at, active
		) VALUES (?, ?, 1, ?, 1)
		ON CONFLICT(pr_id, head_sha) DO UPDATE SET
			consecutive_attempts = review_retry_backoff.consecutive_attempts + 1,
			last_attempt_at = excluded.last_attempt_at,
			active = 1`,
		prID, headSHA, startedAt.UTC().Format(sqliteTimeFormat),
	)
	if err != nil {
		return fmt.Errorf("store: advance review retry backoff: %w", err)
	}
	return nil
}

// MarkReviewRetryFailure moves the current delay origin to the time the
// failure was observed. AdvanceReviewRetryBackoff writes at execution start so
// a daemon death still leaves protection; a normally returned failure should
// wait for the full delay after the work actually stops.
func (s *Store) MarkReviewRetryFailure(prID int64, headSHA string, failedAt time.Time, failure string) error {
	if headSHA == "" {
		return nil
	}
	_, err := s.db.Exec(`
		UPDATE review_retry_backoff
		SET last_attempt_at = ?, last_error = ?, active = 0
		WHERE pr_id = ? AND head_sha = ?`,
		failedAt.UTC().Format(sqliteTimeFormat), failure, prID, headSHA,
	)
	if err != nil {
		return fmt.Errorf("store: mark review retry failure: %w", err)
	}
	return nil
}

// LatestReviewExecutionStatusForPR returns the most recent active or completed
// failed execution for a PR. Crash-only rows are inactive with no error after
// Store startup and are omitted.
func (s *Store) LatestReviewExecutionStatusForPR(prID int64) (*ReviewExecutionStatus, error) {
	var (
		status        ReviewExecutionStatus
		lastAttemptAt string
	)
	err := s.db.QueryRow(`
		SELECT head_sha, consecutive_attempts, last_attempt_at, last_error, active
		FROM review_retry_backoff
		WHERE pr_id = ? AND (active <> 0 OR last_error <> '')
		ORDER BY last_attempt_at DESC
		LIMIT 1`, prID,
	).Scan(&status.HeadSHA, &status.Attempts, &lastAttemptAt, &status.Error, &status.Active)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("store: latest review execution status: %w", err)
	}
	status.FailedAt, err = time.Parse(sqliteTimeFormat, lastAttemptAt)
	if err != nil {
		return nil, fmt.Errorf("store: parse latest review execution time %q: %w", lastAttemptAt, err)
	}
	status.RetryAt = status.FailedAt.Add(reviewRetryDelay(status.Attempts))
	return &status, nil
}

// ClearReviewRetryBackoff removes the failure state after a review has been
// persisted. The review row is now the authoritative dedup/circuit-breaker
// signal, so retaining retry state would delay a legitimate future re-request.
func (s *Store) ClearReviewRetryBackoff(prID int64, headSHA string) error {
	if headSHA == "" {
		return nil
	}
	if _, err := s.db.Exec(
		"DELETE FROM review_retry_backoff WHERE pr_id = ? AND head_sha = ?",
		prID, headSHA,
	); err != nil {
		return fmt.Errorf("store: clear review retry backoff: %w", err)
	}
	return nil
}

// PruneReviewRetryBackoffs removes retry state older than before. Production
// retains rows well beyond the six-hour maximum delay, so pruning cannot reset
// a cooldown that is still active.
func (s *Store) PruneReviewRetryBackoffs(before time.Time) (int, error) {
	res, err := s.db.Exec(
		"DELETE FROM review_retry_backoff WHERE last_attempt_at < ?",
		before.UTC().Format(sqliteTimeFormat),
	)
	if err != nil {
		return 0, fmt.Errorf("store: prune review retry backoffs: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("store: prune review retry backoffs rowsaffected: %w", err)
	}
	return int(n), nil
}

func reviewRetryDelay(attempts int) time.Duration {
	if attempts <= 0 {
		return 0
	}
	delay := reviewRetryBaseDelay
	for i := 1; i < attempts; i++ {
		if delay >= reviewRetryMaxDelay/2 {
			return reviewRetryMaxDelay
		}
		delay *= 2
	}
	return delay
}
