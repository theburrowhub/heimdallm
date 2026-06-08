package store

import (
	"fmt"
	"time"
)

// ClaimInFlightReview inserts a row marking (prID, headSHA) as currently
// being reviewed. Returns (true, nil) on successful claim, (false, nil) if
// another daemon (or this one, pre-restart) already claimed it. Errors
// surface real SQLite problems, not contention.
//
// headSHA is part of the composite key so a new commit on an in-flight PR
// can be reviewed immediately; only the same commit is deduplicated.
//
// See theburrowhub/heimdallm#243 — this replaces the in-memory inFlight
// map whose state was wiped by daemon restarts and config reloads.
func (s *Store) ClaimInFlightReview(prID int64, headSHA string) (bool, error) {
	res, err := s.db.Exec(
		"INSERT OR IGNORE INTO reviews_in_flight (pr_id, head_sha, started_at) VALUES (?, ?, ?)",
		prID, headSHA, time.Now().UTC().Format(sqliteTimeFormat),
	)
	if err != nil {
		return false, fmt.Errorf("store: claim inflight: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("store: claim inflight rowsaffected: %w", err)
	}
	return n == 1, nil
}

// ReviewInFlight reports whether (prID, headSHA) is currently claimed. The PR
// ID must use the same namespace as ClaimInFlightReview; the daemon passes the
// GitHub PR ID for poll-driven reviews.
func (s *Store) ReviewInFlight(prID int64, headSHA string) (bool, error) {
	if headSHA == "" {
		return false, nil
	}
	var n int
	err := s.db.QueryRow(
		"SELECT COUNT(*) FROM reviews_in_flight WHERE pr_id = ? AND head_sha = ?",
		prID, headSHA,
	).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("store: review inflight: %w", err)
	}
	return n > 0, nil
}

// ReleaseInFlightReview removes the (prID, headSHA) row so the pair can be
// re-claimed. Always call in a defer from the caller that successfully
// claimed; no-op if the row doesn't exist.
func (s *Store) ReleaseInFlightReview(prID int64, headSHA string) error {
	_, err := s.db.Exec(
		"DELETE FROM reviews_in_flight WHERE pr_id = ? AND head_sha = ?",
		prID, headSHA,
	)
	if err != nil {
		return fmt.Errorf("store: release inflight: %w", err)
	}
	return nil
}

// ClearStaleInFlight removes claims older than `maxAge`. Protects against
// claims leaked by a daemon that crashed between claim and release. Safe to
// call on every startup; returns the number of rows cleared.
func (s *Store) ClearStaleInFlight(maxAge time.Duration) (int, error) {
	cutoff := time.Now().Add(-maxAge).UTC().Format(sqliteTimeFormat)
	res, err := s.db.Exec("DELETE FROM reviews_in_flight WHERE started_at < ?", cutoff)
	if err != nil {
		return 0, fmt.Errorf("store: clear stale inflight: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("store: clear stale rowsaffected: %w", err)
	}
	return int(n), nil
}
