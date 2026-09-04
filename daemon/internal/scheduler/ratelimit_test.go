package scheduler

import (
	"context"
	"testing"
	"time"
)

func TestRateLimiter_AcquireWithinBudget(t *testing.T) {
	rl := NewRateLimiter(100)
	ctx := context.Background()
	for i := 0; i < 50; i++ {
		if err := rl.Acquire(ctx, TierRepo); err != nil {
			t.Fatalf("acquire %d: %v", i, err)
		}
	}
	if rl.Available() != 50 {
		t.Errorf("available = %d, want 50", rl.Available())
	}
}

func TestRateLimiter_PriorityOrdering(t *testing.T) {
	rl := NewRateLimiter(1)
	ctx := context.Background()
	// Drain the single token
	if err := rl.Acquire(ctx, TierWatch); err != nil {
		t.Fatal(err)
	}
	// T1 (low priority) should timeout
	ctx2, cancel2 := context.WithTimeout(context.Background(), 600*time.Millisecond)
	defer cancel2()
	if err := rl.Acquire(ctx2, TierDiscovery); err == nil {
		t.Error("expected timeout for low-priority tier with empty pool")
	}
}

func TestRateLimiter_ContextCancellation(t *testing.T) {
	rl := NewRateLimiter(0)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := rl.Acquire(ctx, TierWatch); err == nil {
		t.Error("expected error on cancelled context")
	}
}

func TestRateLimiter_Refill(t *testing.T) {
	rl := NewRateLimiter(10)
	ctx := context.Background()
	for i := 0; i < 10; i++ {
		rl.Acquire(ctx, TierRepo)
	}
	if rl.Available() != 0 {
		t.Errorf("should be empty, got %d", rl.Available())
	}
	rl.Refill()
	if rl.Available() != 10 {
		t.Errorf("after refill = %d, want 10", rl.Available())
	}
}

// TestSetDiscoverySafetyThreshold verifies the override and its per-tier scaling.
func TestSetDiscoverySafetyThreshold(t *testing.T) {
	t.Run("default thresholds unchanged when override is zero", func(t *testing.T) {
		rl := NewRateLimiter(100)
		if got := rl.effectiveThreshold(TierDiscovery, "core"); got != 100 {
			t.Errorf("TierDiscovery default = %d, want 100", got)
		}
		if got := rl.effectiveThreshold(TierRepo, "core"); got != 75 {
			t.Errorf("TierRepo default = %d, want 75", got)
		}
		if got := rl.effectiveThreshold(TierWatch, "core"); got != 25 {
			t.Errorf("TierWatch default = %d, want 25", got)
		}
	})

	// The shares are proportional (1.0 / 0.75 / 0.25), not absolute offsets.
	// With offsets, base 200 gave 200/175/125 — the gaps stayed 25 and 75 wide
	// no matter how large the base, so raising the threshold barely changed the
	// relative priority between tiers.
	t.Run("override scales all tiers proportionally", func(t *testing.T) {
		rl := NewRateLimiter(100)
		rl.SetDiscoverySafetyThreshold(200)
		if got := rl.effectiveThreshold(TierDiscovery, "core"); got != 200 {
			t.Errorf("TierDiscovery with override 200 = %d, want 200", got)
		}
		if got := rl.effectiveThreshold(TierRepo, "core"); got != 150 {
			t.Errorf("TierRepo with override 200 = %d, want 150", got)
		}
		if got := rl.effectiveThreshold(TierWatch, "core"); got != 50 {
			t.Errorf("TierWatch with override 200 = %d, want 50", got)
		}
	})

	t.Run("zero or negative override reverts to package default", func(t *testing.T) {
		rl := NewRateLimiter(100)
		rl.SetDiscoverySafetyThreshold(200)
		rl.SetDiscoverySafetyThreshold(0) // revert
		if got := rl.effectiveThreshold(TierDiscovery, "core"); got != 100 {
			t.Errorf("TierDiscovery after revert = %d, want 100", got)
		}
	})

	// The regression the proportional shares fix: with absolute offsets a small
	// base collapsed TierRepo and TierWatch onto the same floor of 1, which
	// erased tier prioritisation and left proactive throttling inert until the
	// budget was completely gone.
	t.Run("small base keeps tiers distinct and ordered", func(t *testing.T) {
		rl := NewRateLimiter(100)
		rl.SetDiscoverySafetyThreshold(20)
		disc := rl.effectiveThreshold(TierDiscovery, "core")
		repo := rl.effectiveThreshold(TierRepo, "core")
		watch := rl.effectiveThreshold(TierWatch, "core")
		if disc != 20 {
			t.Errorf("TierDiscovery with base 20 = %d, want 20", disc)
		}
		if !(disc > repo && repo > watch) {
			t.Errorf("tier ordering collapsed: discovery=%d repo=%d watch=%d", disc, repo, watch)
		}
		if watch < 1 {
			t.Errorf("TierWatch = %d, want >= 1", watch)
		}
	})

	// Search is quoted at 30 req/min, so X-RateLimit-Remaining tops out at 30.
	// Reusing the core base of 100 would hold the budget permanently below
	// threshold and stall every search call.
	t.Run("search resource uses its own base, not the core one", func(t *testing.T) {
		rl := NewRateLimiter(100)
		rl.SetDiscoverySafetyThreshold(100)
		for _, tier := range []Tier{TierDiscovery, TierRepo, TierWatch} {
			got := rl.effectiveThreshold(tier, SearchResource)
			if got > 30 {
				t.Errorf("search threshold for tier %v = %d, exceeds the 30/min quota", tier, got)
			}
			if got < 1 {
				t.Errorf("search threshold for tier %v = %d, want >= 1", tier, got)
			}
		}
	})
}

// TestRateLimiter_Snapshots_ReturnsObservedResources verifies that Snapshots()
// reports the last-observed state for every resource that has received at
// least one Observe() call, keyed by resource name.
func TestRateLimiter_Snapshots_ReturnsObservedResources(t *testing.T) {
	rl := NewRateLimiter(100)
	now := time.Now()
	coreReset := now.Add(1 * time.Hour)
	searchReset := now.Add(45 * time.Second)

	rl.Observe("core", RateSnapshot{Limit: 5000, Remaining: 4937, Used: 63, Reset: coreReset, ObservedAt: now})
	rl.Observe("search", RateSnapshot{Limit: 30, Remaining: 28, Used: 2, Reset: searchReset, ObservedAt: now})

	snaps := rl.Snapshots()
	if len(snaps) != 2 {
		t.Fatalf("Snapshots() returned %d entries, want 2", len(snaps))
	}

	core, ok := snaps["core"]
	if !ok {
		t.Fatal("Snapshots() missing \"core\"")
	}
	if core.Limit != 5000 || core.Remaining != 4937 || core.Used != 63 || !core.Reset.Equal(coreReset) {
		t.Errorf("core snapshot = %+v, want Limit=5000 Remaining=4937 Used=63 Reset=%v", core, coreReset)
	}

	search, ok := snaps["search"]
	if !ok {
		t.Fatal("Snapshots() missing \"search\"")
	}
	if search.Limit != 30 || search.Remaining != 28 || search.Used != 2 {
		t.Errorf("search snapshot = %+v, want Limit=30 Remaining=28 Used=2", search)
	}
}

// TestRateLimiter_Snapshots_EmptyWhenNothingObserved verifies the zero-traffic
// case: no Observe() call yet means no snapshot, not a zero-valued one — the
// caller (GET /github/rate_limit) uses this to distinguish "never measured"
// from "measured and empty".
func TestRateLimiter_Snapshots_EmptyWhenNothingObserved(t *testing.T) {
	rl := NewRateLimiter(100)
	snaps := rl.Snapshots()
	if len(snaps) != 0 {
		t.Errorf("Snapshots() on a fresh limiter = %+v, want empty", snaps)
	}
}

// TestRateLimiter_Observe_MergesLimitWhenNewObservationOmitsIt verifies that a
// later Observe() call without Limit/Used (e.g. from a 304 whose proxy
// stripped X-RateLimit-Limit) does not erase the previously observed
// denominator — only Remaining/Reset/ObservedAt are refreshed.
func TestRateLimiter_Observe_MergesLimitWhenNewObservationOmitsIt(t *testing.T) {
	rl := NewRateLimiter(100)
	now := time.Now()

	rl.Observe("core", RateSnapshot{Limit: 5000, Remaining: 4937, Used: 63, Reset: now.Add(time.Hour), ObservedAt: now})

	// Second observation carries no Limit/Used (zero value) but a fresher Remaining/Reset.
	later := now.Add(time.Minute)
	rl.Observe("core", RateSnapshot{Remaining: 4900, Reset: now.Add(time.Hour), ObservedAt: later})

	snaps := rl.Snapshots()
	core, ok := snaps["core"]
	if !ok {
		t.Fatal("Snapshots() missing \"core\"")
	}
	if core.Limit != 5000 {
		t.Errorf("Limit after merge = %d, want 5000 (preserved from first observation)", core.Limit)
	}
	if core.Used != 63 {
		t.Errorf("Used after merge = %d, want 63 (preserved from first observation)", core.Used)
	}
	if core.Remaining != 4900 {
		t.Errorf("Remaining after merge = %d, want 4900 (updated by second observation)", core.Remaining)
	}
	if !core.ObservedAt.Equal(later) {
		t.Errorf("ObservedAt after merge = %v, want %v (updated by second observation)", core.ObservedAt, later)
	}
}

// TestRateLimiter_Observe_LimitAndUsedAreMergedAsACoupledPair documents (and
// pins) the constraint spelled out in Observe's doc comment: Limit <= 0
// discards the incoming Used too, even if the caller set a nonzero Used. This
// is safe today because the only real producer (parsed X-RateLimit-* headers)
// never sends a nonzero Used without a nonzero Limit — but a future caller
// that violated the assumption would silently lose data, so pin the current
// behavior with a test rather than leave it implicit.
func TestRateLimiter_Observe_LimitAndUsedAreMergedAsACoupledPair(t *testing.T) {
	rl := NewRateLimiter(100)
	now := time.Now()

	rl.Observe("core", RateSnapshot{Limit: 5000, Remaining: 4937, Used: 63, Reset: now.Add(time.Hour), ObservedAt: now})

	// Hypothetical caller that violates the coupling: Limit <= 0 but Used set.
	rl.Observe("core", RateSnapshot{Limit: 0, Used: 999, Remaining: 10, Reset: now.Add(time.Hour), ObservedAt: now})

	core, ok := rl.Snapshots()["core"]
	if !ok {
		t.Fatal("Snapshots() missing \"core\"")
	}
	if core.Used != 63 {
		t.Errorf("Used = %d, want 63 (the Limit<=0 observation's Used=999 must be discarded along with its missing Limit)", core.Used)
	}
}
