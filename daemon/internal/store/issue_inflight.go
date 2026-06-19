package store

import (
	"fmt"
	"time"
)

// ClaimIssueTriageInFlight inserts a row marking the issue as currently
// being triaged. Returns (true, nil) on successful claim, (false, nil)
// when any claim already exists for this issue_id — single-flight per
// issue regardless of updated_at. Errors surface real SQLite problems.
//
// updatedAt is kept as a stored column for diagnostics (which snapshot
// kicked off the run) but is intentionally NOT part of the contention
// key any more. The previous (issue_id, updated_at) composite let the
// bot's own triage comment — which bumps the issue's updated_at — slip
// past the claim and spawn a duplicate triage on the next poller tick
// (#458, re-emergence of the failure mode #362 fixed for a sibling
// path). The atomic INSERT … WHERE NOT EXISTS replaces a two-step
// "check then insert" so concurrent claims still collapse race-free.
//
// See theburrowhub/heimdallm#292 — this mirrors the PR-side claim
// (#258) for the issue-triage path so concurrent dispatches cannot
// each spend a Claude run.
func (s *Store) ClaimIssueTriageInFlight(issueID int64, updatedAt string) (bool, error) {
	res, err := s.db.Exec(
		`INSERT INTO issue_triage_in_flight (issue_id, updated_at, started_at)
		 SELECT ?, ?, ?
		 WHERE NOT EXISTS (SELECT 1 FROM issue_triage_in_flight WHERE issue_id = ?)`,
		issueID, updatedAt, time.Now().UTC().Format(sqliteTimeFormat), issueID,
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

// ReleaseIssueTriageInFlight removes any in-flight row for the issue so a
// future tick can claim again. Aligned with the single-flight-per-issue
// claim semantics (#458): we drop the updated_at predicate so the release
// never strands a row when the caller's view of updated_at diverged from
// what the claim recorded. The updatedAt argument is accepted for API
// symmetry and is logged at call sites but is intentionally not part of
// the WHERE clause. Always call in a defer from the caller that
// successfully claimed; no-op if no row exists.
func (s *Store) ReleaseIssueTriageInFlight(issueID int64, updatedAt string) error {
	_ = updatedAt // kept for API symmetry; release is single-flight-per-issue
	_, err := s.db.Exec(
		"DELETE FROM issue_triage_in_flight WHERE issue_id = ?",
		issueID,
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

// ClearAllIssueTriageInFlight is the issue-triage mirror of
// ClearAllInFlight (#544). Removes every row from issue_triage_in_flight
// unconditionally — used at single-instance startup where any surviving
// claim is, by definition, orphaned.
func (s *Store) ClearAllIssueTriageInFlight() (int, error) {
	res, err := s.db.Exec("DELETE FROM issue_triage_in_flight")
	if err != nil {
		return 0, fmt.Errorf("store: clear all issue triage inflight: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("store: clear all issue triage inflight rowsaffected: %w", err)
	}
	return int(n), nil
}
