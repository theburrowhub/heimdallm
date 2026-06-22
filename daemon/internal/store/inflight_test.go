package store_test

import (
	"fmt"
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

// TestInFlight_ClearAllInFlight locks in the invariant that the
// single-instance startup sweep (#544) needs: ClearAllInFlight removes every
// row regardless of age, including ones inserted moments ago. Any claim that
// survives a daemon restart is, by definition, orphaned in a single-instance
// deployment, so the previous 30-min cutoff left a "dead zone" where a claim
// younger than the cutoff would survive forever (the daemon's restart-only
// sweep would skip it and there was no periodic sweep to catch it later).
//
// Distinct from ClearStaleInFlight(0): the latter is age-based with second
// precision and would silently skip rows inserted in the same second as the
// call — which is exactly the dead-zone shape #544 reports.
func TestInFlight_ClearAllInFlight(t *testing.T) {
	s := newTestStore(t)
	if err := s.InsertStaleInFlight(1, "sha1", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertStaleInFlight(2, "sha2", time.Now().Add(-1*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertStaleInFlight(3, "sha3", time.Now().Add(-29*time.Minute)); err != nil {
		t.Fatal(err)
	}
	n, err := s.ClearAllInFlight()
	if err != nil {
		t.Fatalf("clear all: %v", err)
	}
	if n != 3 {
		t.Errorf("ClearAllInFlight want 3 cleared, got %d", n)
	}
	// All three claims must be reclaimable.
	for _, pr := range []int64{1, 2, 3} {
		sha := fmt.Sprintf("sha%d", pr)
		claimed, err := s.ClaimInFlightReview(pr, sha)
		if err != nil {
			t.Fatalf("re-claim pr=%d: %v", pr, err)
		}
		if !claimed {
			t.Errorf("re-claim pr=%d sha=%s: want true after ClearAllInFlight", pr, sha)
		}
	}
}

// TestInFlight_PeriodicSweepPreservesYoungClaims is the dual invariant: while
// the daemon is running, a periodic ClearStaleInFlight(maxAge) MUST NOT kill
// a fresh in-flight review. Locks in the safety property of the periodic sweep
// added in #544 — without this, a 5-min ticker calling ClearStaleInFlight(30m)
// could accidentally reap a long-running but still-live review.
func TestInFlight_PeriodicSweepPreservesYoungClaims(t *testing.T) {
	s := newTestStore(t)
	claimed, err := s.ClaimInFlightReview(42, "freshsha")
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if !claimed {
		t.Fatal("initial claim must succeed")
	}
	n, err := s.ClearStaleInFlight(30 * time.Minute)
	if err != nil {
		t.Fatalf("periodic sweep: %v", err)
	}
	if n != 0 {
		t.Errorf("periodic sweep killed a fresh claim: cleared %d, want 0", n)
	}
	inFlight, err := s.ReviewInFlight(42, "freshsha")
	if err != nil {
		t.Fatal(err)
	}
	if !inFlight {
		t.Error("fresh claim disappeared after periodic sweep")
	}
}
