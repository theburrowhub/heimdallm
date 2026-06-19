package store_test

import (
	"testing"
	"time"
)

func TestIssueInflight_ClaimAndRelease(t *testing.T) {
	s := newTestStore(t)
	claimed, err := s.ClaimIssueTriageInFlight(42, "2026-04-23T12:00:00Z")
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if !claimed {
		t.Errorf("first claim should succeed")
	}
	// Second claim on the same (issue_id, updated_at) must fail.
	claimed, err = s.ClaimIssueTriageInFlight(42, "2026-04-23T12:00:00Z")
	if err != nil {
		t.Fatalf("second claim: %v", err)
	}
	if claimed {
		t.Errorf("second claim on same (issue, updated_at) must return false")
	}
	// Different updated_at on the SAME issue must ALSO fail — single-flight
	// per issue. The bot posting its own triage comment bumps the issue's
	// updated_at, and the previous (id, updated_at) key let that bump pass
	// the claim and start a duplicate triage (#458).
	claimed, err = s.ClaimIssueTriageInFlight(42, "2026-04-23T12:01:00Z")
	if err != nil {
		t.Fatalf("new updated_at claim: %v", err)
	}
	if claimed {
		t.Errorf("claim for new updated_at on already-in-flight issue must return false")
	}
	// Release the active claim; a new claim (any updated_at) is allowed again.
	if err := s.ReleaseIssueTriageInFlight(42, "2026-04-23T12:00:00Z"); err != nil {
		t.Fatalf("release: %v", err)
	}
	claimed, err = s.ClaimIssueTriageInFlight(42, "2026-04-23T12:01:00Z")
	if err != nil {
		t.Fatalf("re-claim: %v", err)
	}
	if !claimed {
		t.Errorf("re-claim after release must succeed even with a different updated_at")
	}
	// And a different issue is independent — no contention.
	claimed, err = s.ClaimIssueTriageInFlight(43, "2026-04-23T12:00:00Z")
	if err != nil {
		t.Fatalf("different-issue claim: %v", err)
	}
	if !claimed {
		t.Errorf("claim for a different issue must succeed regardless of other in-flight rows")
	}
}

func TestIssueInflight_StaleEntriesAreCleared(t *testing.T) {
	s := newTestStore(t)
	// Simulate a stale row from a crashed daemon.
	if err := s.InsertStaleIssueTriageInFlight(42, "2026-04-23T12:00:00Z", time.Now().Add(-1*time.Hour)); err != nil {
		t.Fatal(err)
	}
	n, err := s.ClearStaleIssueTriageInFlight(30 * time.Minute)
	if err != nil {
		t.Fatalf("clear: %v", err)
	}
	if n != 1 {
		t.Errorf("want 1 stale row cleared, got %d", n)
	}
	// The row should now be claimable again.
	claimed, err := s.ClaimIssueTriageInFlight(42, "2026-04-23T12:00:00Z")
	if err != nil {
		t.Fatalf("claim after clear: %v", err)
	}
	if !claimed {
		t.Errorf("claim after stale-clear must succeed")
	}
}

// TestIssueInflight_ClearAllIssueTriageInFlight — mirror of
// TestInFlight_ClearAllInFlight for issue-triage claims (#544).
func TestIssueInflight_ClearAllIssueTriageInFlight(t *testing.T) {
	s := newTestStore(t)
	if err := s.InsertStaleIssueTriageInFlight(1, "2026-06-19T10:00:00Z", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertStaleIssueTriageInFlight(2, "2026-06-19T10:00:00Z", time.Now().Add(-29*time.Minute)); err != nil {
		t.Fatal(err)
	}
	n, err := s.ClearAllIssueTriageInFlight()
	if err != nil {
		t.Fatalf("clear all: %v", err)
	}
	if n != 2 {
		t.Errorf("ClearAllIssueTriageInFlight want 2 cleared, got %d", n)
	}
}

// TestIssueInflight_PeriodicSweepPreservesYoungClaims — mirror of the
// PR-side invariant for issue-triage. The periodic sweep with maxAge=30m
// must not reap a freshly-claimed triage. (#544)
func TestIssueInflight_PeriodicSweepPreservesYoungClaims(t *testing.T) {
	s := newTestStore(t)
	claimed, err := s.ClaimIssueTriageInFlight(42, "2026-06-19T10:00:00Z")
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if !claimed {
		t.Fatal("initial claim must succeed")
	}
	n, err := s.ClearStaleIssueTriageInFlight(30 * time.Minute)
	if err != nil {
		t.Fatalf("periodic sweep: %v", err)
	}
	if n != 0 {
		t.Errorf("periodic sweep killed a fresh claim: cleared %d, want 0", n)
	}
}
