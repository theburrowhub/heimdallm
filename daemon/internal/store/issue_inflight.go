package store

import (
	"fmt"
	"time"
)

// ClaimIssueTriageInFlight inserts a row marking (issueID, updatedAt) as
// currently being triaged. Returns (true, nil) on successful claim,
// (false, nil) if another daemon (or this one, pre-restart) already
// claimed the same snapshot. Errors surface real SQLite problems, not
// contention.
//
// updatedAt is part of the composite key so a genuinely new activity on
// the issue (new updated_at) produces a new claim, but two fetcher ticks
// observing the same snapshot collapse. The caller is expected to pass
// issue.UpdatedAt.UTC().Format(time.RFC3339) so the key is stable.
//
// See theburrowhub/heimdallm#292 — this mirrors the PR-side claim
// (#258) for the issue-triage path so concurrent dispatches within a
// single snapshot cannot each spend a Claude run.
func (s *Store) ClaimIssueTriageInFlight(issueID int64, updatedAt string) (bool, error) {
	res, err := s.db.Exec(
		"INSERT OR IGNORE INTO issue_triage_in_flight (issue_id, updated_at, started_at) VALUES (?, ?, ?)",
		issueID, updatedAt, time.Now().UTC().Format(sqliteTimeFormat),
	)
	if err != nil {
		return false, fmt.Errorf("store: claim issue triage inflight: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("store: claim issue triage inflight rowsaffected: %w", err)
	}
	return n == 1, nil
}

// ReleaseIssueTriageInFlight removes the (issueID, updatedAt) row so the
// pair can be re-claimed. Always call in a defer from the caller that
// successfully claimed; no-op if the row doesn't exist.
func (s *Store) ReleaseIssueTriageInFlight(issueID int64, updatedAt string) error {
	_, err := s.db.Exec(
		"DELETE FROM issue_triage_in_flight WHERE issue_id = ? AND updated_at = ?",
		issueID, updatedAt,
	)
	if err != nil {
		return fmt.Errorf("store: release issue triage inflight: %w", err)
	}
	return nil
}

// ClearStaleIssueTriageInFlight removes claims older than `maxAge`.
// Protects against claims leaked by a daemon that crashed between claim
// and release. Safe to call on every startup; returns the number of rows
// cleared.
func (s *Store) ClearStaleIssueTriageInFlight(maxAge time.Duration) (int, error) {
	cutoff := time.Now().Add(-maxAge).UTC().Format(sqliteTimeFormat)
	res, err := s.db.Exec("DELETE FROM issue_triage_in_flight WHERE started_at < ?", cutoff)
	if err != nil {
		return 0, fmt.Errorf("store: clear stale issue triage inflight: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("store: clear stale issue triage inflight rowsaffected: %w", err)
	}
	return int(n), nil
}
