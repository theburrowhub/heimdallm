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
