package store

import (
	"fmt"
	"time"
)

// RecordReviewAttempt appends one row to the review-attempt ledger for
// (prID, headSHA).
//
// Callers must invoke this immediately BEFORE spending CLI credits and must NOT
// remove the row when the run fails. That is the entire point: the circuit
// breaker used to count rows in `reviews`, which only exist for runs that
// succeeded, so a run that died before InsertReview advanced no counter at all
// and the next tick re-ran the same commit at full price with nothing to stop
// it (theburrowhub/heimdallm#663).
//
// An empty headSHA is allowed and recorded as such; the per-PR axis of the
// breaker falls back to counting every attempt on the PR in that case, which
// matches how CountReviewsForPR is used for the same situation.
func (s *Store) RecordReviewAttempt(prID int64, headSHA string) error {
	_, err := s.db.Exec(
		"INSERT INTO review_attempts (pr_id, head_sha, started_at) VALUES (?, ?, ?)",
		prID, headSHA, time.Now().UTC().Format(sqliteTimeFormat),
	)
	if err != nil {
		return fmt.Errorf("store: record review attempt: %w", err)
	}
	return nil
}

// CountReviewAttemptsForPR returns how many attempts were recorded for the PR at
// or after `since`, regardless of HEAD SHA.
func (s *Store) CountReviewAttemptsForPR(prID int64, since time.Time) (int, error) {
	var n int
	err := s.db.QueryRow(
		"SELECT COUNT(*) FROM review_attempts WHERE pr_id = ? AND started_at >= ?",
		prID, since.UTC().Format(sqliteTimeFormat),
	).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("store: count review attempts for pr: %w", err)
	}
	return n, nil
}

// CountReviewAttemptsForPRHeadSHA returns how many attempts were recorded for
// the PR at that exact commit at or after `since`. This is the breaker's primary
// axis: cap repeated work on one commit while leaving a follow-up commit free to
// be reviewed normally.
func (s *Store) CountReviewAttemptsForPRHeadSHA(prID int64, headSHA string, since time.Time) (int, error) {
	var n int
	err := s.db.QueryRow(
		"SELECT COUNT(*) FROM review_attempts WHERE pr_id = ? AND head_sha = ? AND started_at >= ?",
		prID, headSHA, since.UTC().Format(sqliteTimeFormat),
	).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("store: count review attempts for pr head sha: %w", err)
	}
	return n, nil
}

// CountReviewAttemptsForRepo returns how many attempts were recorded across ANY
// PR of the repo at or after `since`. Mirrors CountReviewsForRepo, including the
// join, so both axes of the breaker read the same shape of data.
func (s *Store) CountReviewAttemptsForRepo(repo string, since time.Time) (int, error) {
	var n int
	err := s.db.QueryRow(`
		SELECT COUNT(*) FROM review_attempts a
		JOIN prs p ON a.pr_id = p.id
		WHERE p.repo = ? AND a.started_at >= ?`,
		repo, since.UTC().Format(sqliteTimeFormat),
	).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("store: count review attempts for repo: %w", err)
	}
	return n, nil
}

// PruneReviewAttempts deletes ledger rows older than maxAge and returns how many
// were removed.
//
// The ledger is append-only and only ever read through a 24h (per-PR) or 1h
// (per-repo) window, so anything older is dead weight. maxAge must stay
// comfortably above the widest breaker window, otherwise pruning would silently
// reset a cap that is still meant to be in force.
//
// Note on precision: started_at is stored in sqliteTimeFormat, which is
// second-granular, and the comparison is strictly less-than. A row written in
// the same second as the computed cutoff therefore survives — the same dead zone
// documented on ClearAllInFlight (#544). Harmless at the 48h maxAge the sweep
// uses; it only matters to a caller passing a maxAge near zero, which no
// production path does.
func (s *Store) PruneReviewAttempts(maxAge time.Duration) (int, error) {
	cutoff := time.Now().Add(-maxAge).UTC().Format(sqliteTimeFormat)
	res, err := s.db.Exec("DELETE FROM review_attempts WHERE started_at < ?", cutoff)
	if err != nil {
		return 0, fmt.Errorf("store: prune review attempts: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("store: prune review attempts rowsaffected: %w", err)
	}
	return int(n), nil
}
