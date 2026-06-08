package store_test

import (
	"testing"
	"time"
)

func TestInFlight_ClaimAndRelease(t *testing.T) {
	s := newTestStore(t)
	inFlight, err := s.ReviewInFlight(42, "abc123")
	if err != nil {
		t.Fatalf("initial in-flight check: %v", err)
	}
	if inFlight {
		t.Fatal("review should not be in-flight before claim")
	}
	claimed, err := s.ClaimInFlightReview(42, "abc123")
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if !claimed {
		t.Errorf("first claim should succeed")
	}
	inFlight, err = s.ReviewInFlight(42, "abc123")
	if err != nil {
		t.Fatalf("in-flight check after claim: %v", err)
	}
	if !inFlight {
		t.Fatal("review should be in-flight after claim")
	}
	inFlight, err = s.ReviewInFlight(42, "")
	if err != nil {
		t.Fatalf("empty sha in-flight check: %v", err)
	}
	if inFlight {
		t.Fatal("empty head SHA should not match an in-flight review")
	}
	// Second claim on the same (pr_id, head_sha) must fail.
	claimed, err = s.ClaimInFlightReview(42, "abc123")
	if err != nil {
		t.Fatalf("second claim: %v", err)
	}
	if claimed {
		t.Errorf("second claim on same (pr, sha) must return false")
	}
	// Different SHA on the same PR is allowed (new commit).
	claimed, err = s.ClaimInFlightReview(42, "def456")
	if err != nil {
		t.Fatalf("new sha claim: %v", err)
	}
	if !claimed {
		t.Errorf("claim for new SHA must succeed")
	}
	// Release the first claim; should allow a re-claim.
	if err := s.ReleaseInFlightReview(42, "abc123"); err != nil {
		t.Fatalf("release: %v", err)
	}
	inFlight, err = s.ReviewInFlight(42, "abc123")
	if err != nil {
		t.Fatalf("in-flight check after release: %v", err)
	}
	if inFlight {
		t.Fatal("review should not be in-flight after release")
	}
	claimed, err = s.ClaimInFlightReview(42, "abc123")
	if err != nil {
		t.Fatalf("re-claim: %v", err)
	}
	if !claimed {
		t.Errorf("re-claim after release must succeed")
	}
}

func TestInFlight_StaleEntriesAreCleared(t *testing.T) {
	s := newTestStore(t)
	// Simulate a stale row from a crashed daemon.
	if err := s.InsertStaleInFlight(42, "abc123", time.Now().Add(-1*time.Hour)); err != nil {
		t.Fatal(err)
	}
	n, err := s.ClearStaleInFlight(30 * time.Minute)
	if err != nil {
		t.Fatalf("clear: %v", err)
	}
	if n != 1 {
		t.Errorf("want 1 stale row cleared, got %d", n)
	}
	// The row should now be claimable again.
	claimed, err := s.ClaimInFlightReview(42, "abc123")
	if err != nil {
		t.Fatalf("claim after clear: %v", err)
	}
	if !claimed {
		t.Errorf("claim after stale-clear must succeed")
	}
}
