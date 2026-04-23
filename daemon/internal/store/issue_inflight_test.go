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
	// Different updated_at on the same issue is allowed (genuinely new activity).
	claimed, err = s.ClaimIssueTriageInFlight(42, "2026-04-23T12:01:00Z")
	if err != nil {
		t.Fatalf("new updated_at claim: %v", err)
	}
	if !claimed {
		t.Errorf("claim for new updated_at must succeed")
	}
	// Release the first claim; should allow a re-claim.
	if err := s.ReleaseIssueTriageInFlight(42, "2026-04-23T12:00:00Z"); err != nil {
		t.Fatalf("release: %v", err)
	}
	claimed, err = s.ClaimIssueTriageInFlight(42, "2026-04-23T12:00:00Z")
	if err != nil {
		t.Fatalf("re-claim: %v", err)
	}
	if !claimed {
		t.Errorf("re-claim after release must succeed")
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
