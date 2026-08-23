package store

import (
	"database/sql"
	"fmt"
	"time"
)

const reviewRetryRepoWindow = time.Hour

// ReviewRetryReservation describes the result of reserving one review
// execution against the repository-wide failed-run budget. ID is non-zero only
// when the execution may proceed. FailureCount is populated when the limit
// blocks the reservation.
type ReviewRetryReservation struct {
	ID           int64
	Blocked      bool
	RetryAt      time.Time
	FailureCount int
}

// ReserveReviewRetryAttempt atomically checks the rolling repository-wide
// failed/in-flight execution count and, when allowed, records the execution
// before it starts. A limit <= 0 disables the aggregate check but still records
// the reservation so a later enabled check sees a failed run.
//
// The caller must clear the returned ID only after a review has been persisted.
// Returned errors, parse/storage failures, and daemon death intentionally leave
// the row in place. This ledger is independent from the review circuit breaker:
// it bounds failed execution spend without claiming that a failure was a
// completed review.
func (s *Store) ReserveReviewRetryAttempt(
	prID int64,
	repo, headSHA string,
	startedAt time.Time,
	limit int,
) (reservation ReviewRetryReservation, err error) {
	tx, err := s.db.Begin()
	if err != nil {
		return reservation, fmt.Errorf("store: begin review retry reservation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if limit > 0 && repo != "" {
		var oldest sql.NullString
		err = tx.QueryRow(`
			SELECT COUNT(*), MIN(a.started_at)
			FROM review_retry_attempts a
			JOIN prs p ON a.pr_id = p.id
			WHERE p.repo = ? AND a.started_at >= ?`,
			repo, startedAt.UTC().Add(-reviewRetryRepoWindow).Format(sqliteTimeFormat),
		).Scan(&reservation.FailureCount, &oldest)
		if err != nil {
			return ReviewRetryReservation{}, fmt.Errorf("store: count review retry attempts for repo: %w", err)
		}
		if reservation.FailureCount >= limit {
			if !oldest.Valid {
				return ReviewRetryReservation{}, fmt.Errorf("store: review retry limit reached without an oldest attempt")
			}
			oldestAt, parseErr := time.Parse(sqliteTimeFormat, oldest.String)
			if parseErr != nil {
				return ReviewRetryReservation{}, fmt.Errorf(
					"store: parse oldest review retry attempt %q: %w", oldest.String, parseErr,
				)
			}
			reservation.Blocked = true
			reservation.RetryAt = oldestAt.Add(reviewRetryRepoWindow)
			if err := tx.Commit(); err != nil {
				return ReviewRetryReservation{}, fmt.Errorf("store: commit blocked review retry reservation: %w", err)
			}
			return reservation, nil
		}
	}

	result, err := tx.Exec(`
		INSERT INTO review_retry_attempts (pr_id, head_sha, started_at)
		VALUES (?, ?, ?)`,
		prID, headSHA, startedAt.UTC().Format(sqliteTimeFormat),
	)
	if err != nil {
		return ReviewRetryReservation{}, fmt.Errorf("store: reserve review retry attempt: %w", err)
	}
	reservation.ID, err = result.LastInsertId()
	if err != nil {
		return ReviewRetryReservation{}, fmt.Errorf("store: review retry reservation id: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return ReviewRetryReservation{}, fmt.Errorf("store: commit review retry reservation: %w", err)
	}
	return reservation, nil
}

// ClearReviewRetryAttempt removes one successful execution reservation. Older
// failed rows remain until their rolling window expires, preserving the
// aggregate cost bound even after a later retry succeeds.
func (s *Store) ClearReviewRetryAttempt(id int64) error {
	if id <= 0 {
		return nil
	}
	if _, err := s.db.Exec("DELETE FROM review_retry_attempts WHERE id = ?", id); err != nil {
		return fmt.Errorf("store: clear review retry attempt: %w", err)
	}
	return nil
}

// PruneReviewRetryAttempts removes reservations older than before. Production
// retains them far beyond the one-hour rolling window used by the aggregate
// limit, so pruning cannot release an active limit early.
func (s *Store) PruneReviewRetryAttempts(before time.Time) (int, error) {
	result, err := s.db.Exec(
		"DELETE FROM review_retry_attempts WHERE started_at < ?",
		before.UTC().Format(sqliteTimeFormat),
	)
	if err != nil {
		return 0, fmt.Errorf("store: prune review retry attempts: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("store: prune review retry attempts rowsaffected: %w", err)
	}
	return int(rows), nil
}
