package scheduler

import (
	"context"
	"testing"
	"time"
)

// TestRateLimiter_ObserveHealthyBudgetDoesNotThrottle verifies that when the
// remaining budget is well above all tier thresholds, Acquire returns
// immediately without blocking.
func TestRateLimiter_ObserveHealthyBudgetDoesNotThrottle(t *testing.T) {
	rl := NewRateLimiter(100)
	reset := time.Now().Add(1 * time.Hour)

	// Observe a healthy budget (far above all thresholds).
	rl.Observe("core", 5000, reset)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	start := time.Now()
	if err := rl.Acquire(ctx, TierDiscovery); err != nil {
		t.Fatalf("Acquire failed with healthy budget: %v", err)
	}
	elapsed := time.Since(start)

	// Should return in well under 50ms — no budget wait, no cooldown.
	if elapsed > 100*time.Millisecond {
		t.Errorf("Acquire took %v with healthy budget, expected near-instant", elapsed)
	}
}

// TestRateLimiter_ObserveDepletedBudgetThrottlesLowPriority verifies that when
// remaining drops below the TierDiscovery threshold, Acquire blocks until
// the reset time elapses.
func TestRateLimiter_ObserveDepletedBudgetThrottlesLowPriority(t *testing.T) {
	rl := NewRateLimiter(100)

	// Set reset 80ms in the future.
	resetDelay := 80 * time.Millisecond
	reset := time.Now().Add(resetDelay)

	// Observe remaining=50, which is below TierDiscovery threshold (100)
	// but above TierWatch threshold (25).
	rl.Observe("core", 50, reset)

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	start := time.Now()
	// TierDiscovery has threshold=100, remaining=50 < 100 → must wait for reset.
	if err := rl.Acquire(ctx, TierDiscovery); err != nil {
		t.Fatalf("Acquire returned error: %v", err)
	}
	elapsed := time.Since(start)

	// Must have waited at least resetDelay - small tolerance.
	if elapsed < resetDelay-10*time.Millisecond {
		t.Errorf("Acquire returned too early: elapsed=%v, reset delay=%v", elapsed, resetDelay)
	}
}

// TestRateLimiter_HighPriorityTierNotThrottledWhenMediumWouldBe verifies tier
// priority: TierWatch uses a lower threshold (25) than TierDiscovery (100).
// When remaining=50, Discovery blocks but Watch proceeds immediately.
func TestRateLimiter_HighPriorityTierNotThrottledWhenMediumWouldBe(t *testing.T) {
	rl := NewRateLimiter(100)
	reset := time.Now().Add(2 * time.Second) // long reset, would block

	// remaining=50: above Watch threshold (25) but below Discovery threshold (100)
	rl.Observe("core", 50, reset)

	// TierWatch should NOT block — remaining(50) >= threshold(25).
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := rl.AcquireResource(ctx, TierWatch, "core"); err != nil {
		t.Fatalf("TierWatch Acquire failed: %v", err)
	}
	elapsed := time.Since(start)
	if elapsed > 100*time.Millisecond {
		t.Errorf("TierWatch took %v, expected near-instant with remaining=50 (threshold=25)", elapsed)
	}

	// TierDiscovery with a fresh limiter (same conditions) SHOULD block,
	// but we can't wait 2s in a test. Instead verify that ctx cancellation
	// interrupts the wait, which proves it would have waited.
	rl2 := NewRateLimiter(100)
	rl2.Observe("core", 50, time.Now().Add(2*time.Second))

	ctx2, cancel2 := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel2()
	if err := rl2.AcquireResource(ctx2, TierDiscovery, "core"); err == nil {
		t.Error("TierDiscovery should have been throttled (ctx should expire first), got nil error")
	}
}

// TestRateLimiter_SecondaryCooldownBlocksAllTiers verifies that ObserveRetryAfter
// makes Acquire block until the cooldown elapses, regardless of tier.
func TestRateLimiter_SecondaryCooldownBlocksAllTiers(t *testing.T) {
	rl := NewRateLimiter(100)
	cooldown := 80 * time.Millisecond
	rl.ObserveRetryAfter(cooldown)

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	start := time.Now()
	// Even TierWatch (highest priority) must wait for the cooldown.
	if err := rl.Acquire(ctx, TierWatch); err != nil {
		t.Fatalf("Acquire returned error during cooldown: %v", err)
	}
	elapsed := time.Since(start)

	if elapsed < cooldown-10*time.Millisecond {
		t.Errorf("Acquire returned before cooldown elapsed: elapsed=%v, cooldown=%v", elapsed, cooldown)
	}
}

// TestRateLimiter_SecondaryCooldownExpiredDoesNotBlock verifies that once the
// Retry-After window has passed, Acquire returns immediately.
func TestRateLimiter_SecondaryCooldownExpiredDoesNotBlock(t *testing.T) {
	rl := NewRateLimiter(100)
	// Set a cooldown that already expired.
	rl.ObserveRetryAfter(-100 * time.Millisecond) // negative → treated as 0 → no-op

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := rl.Acquire(ctx, TierWatch); err != nil {
		t.Fatalf("Acquire returned error with expired cooldown: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Errorf("Acquire too slow with no active cooldown: %v", elapsed)
	}
}

// TestRateLimiter_CtxCancelDuringBudgetWait verifies that context cancellation
// during a budget-wait returns ctx.Err().
func TestRateLimiter_CtxCancelDuringBudgetWait(t *testing.T) {
	rl := NewRateLimiter(100)
	// Budget depleted, reset far in the future.
	rl.Observe("core", 10, time.Now().Add(10*time.Second))

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	err := rl.AcquireResource(ctx, TierDiscovery, "core") // threshold=100 > remaining=10
	if err == nil {
		t.Fatal("expected ctx error, got nil")
	}
	if err != context.DeadlineExceeded {
		t.Errorf("expected DeadlineExceeded, got %v", err)
	}
}

// TestRateLimiter_CtxCancelDuringCooldownWait verifies that context cancellation
// during a Retry-After cooldown returns ctx.Err().
func TestRateLimiter_CtxCancelDuringCooldownWait(t *testing.T) {
	rl := NewRateLimiter(100)
	rl.ObserveRetryAfter(10 * time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	err := rl.Acquire(ctx, TierWatch)
	if err == nil {
		t.Fatal("expected ctx error, got nil")
	}
}

// TestRateLimiter_NoBudgetInfoProceeds verifies that Acquire is unthrottled
// when no Observe() call has been made (i.e. no budget info yet).
func TestRateLimiter_NoBudgetInfoProceeds(t *testing.T) {
	rl := NewRateLimiter(100)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	start := time.Now()
	if err := rl.Acquire(ctx, TierDiscovery); err != nil {
		t.Fatalf("Acquire should succeed with no budget info: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Errorf("Acquire too slow with no budget info: %v", elapsed)
	}
}

// TestRateLimiter_ObserveUpdatesExistingBudget verifies that a second Observe
// call replaces the previous budget.
func TestRateLimiter_ObserveUpdatesExistingBudget(t *testing.T) {
	rl := NewRateLimiter(100)

	// First observation: healthy budget.
	rl.Observe("core", 5000, time.Now().Add(1*time.Hour))
	rem, ok := rl.BudgetRemaining("core")
	if !ok || rem != 5000 {
		t.Errorf("BudgetRemaining after first observe: got (%d,%v), want (5000,true)", rem, ok)
	}

	// Second observation: updated budget.
	rl.Observe("core", 42, time.Now().Add(1*time.Hour))
	rem, ok = rl.BudgetRemaining("core")
	if !ok || rem != 42 {
		t.Errorf("BudgetRemaining after second observe: got (%d,%v), want (42,true)", rem, ok)
	}
}

// TestRateLimiter_SearchResourceIndependentOfCore verifies that "search" and
// "core" budgets are tracked independently. Depleting "search" does not affect
// Acquire(ctx, tier) which checks "core".
func TestRateLimiter_SearchResourceIndependentOfCore(t *testing.T) {
	rl := NewRateLimiter(100)

	// Core is healthy; search is depleted.
	rl.Observe("core", 5000, time.Now().Add(1*time.Hour))
	rl.Observe("search", 0, time.Now().Add(10*time.Second))

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	// Acquire (which checks "core") should be fast.
	start := time.Now()
	if err := rl.Acquire(ctx, TierDiscovery); err != nil {
		t.Fatalf("Acquire failed: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Errorf("Acquire took %v, expected near-instant (core is healthy)", elapsed)
	}

	// AcquireResource on "search" with Discovery should block (remaining=0 < 100).
	rl2 := NewRateLimiter(100)
	rl2.Observe("search", 0, time.Now().Add(10*time.Second))
	ctx2, cancel2 := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel2()
	if err := rl2.AcquireResource(ctx2, TierDiscovery, "search"); err == nil {
		t.Error("expected throttle on depleted search resource, got nil")
	}
}

// TestRateLimiter_ResetAlreadyPassedProceedsImmediately verifies that if the
// reset time is already in the past, Acquire does NOT wait.
func TestRateLimiter_ResetAlreadyPassedProceedsImmediately(t *testing.T) {
	rl := NewRateLimiter(100)
	// Observe a depleted budget with a reset time in the past.
	rl.Observe("core", 0, time.Now().Add(-5*time.Second))

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	start := time.Now()
	// Should proceed immediately since reset already passed.
	if err := rl.Acquire(ctx, TierDiscovery); err != nil {
		t.Fatalf("Acquire returned error: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Errorf("Acquire took %v with past reset, expected near-instant", elapsed)
	}
}

// TestRateLimiter_TierThresholdValues documents and asserts the exact threshold
// values so a future change to defaults is a deliberate, visible diff.
func TestRateLimiter_TierThresholdValues(t *testing.T) {
	cases := []struct {
		tier       Tier
		wantThresh int
	}{
		{TierDiscovery, 100},
		{TierRepo, 75},
		{TierWatch, 25},
	}
	for _, tc := range cases {
		got := tierSafetyThreshold[tc.tier]
		if got != tc.wantThresh {
			t.Errorf("tierSafetyThreshold[%v] = %d, want %d", tc.tier, got, tc.wantThresh)
		}
	}

	// Verify ordering: Discovery > Repo > Watch (backs off earliest to latest).
	if tierSafetyThreshold[TierDiscovery] <= tierSafetyThreshold[TierRepo] {
		t.Error("TierDiscovery threshold should be > TierRepo (back off earlier)")
	}
	if tierSafetyThreshold[TierRepo] <= tierSafetyThreshold[TierWatch] {
		t.Error("TierRepo threshold should be > TierWatch (back off earlier)")
	}
}

// TestRateLimiter_ExistingBehaviorPreserved verifies that the original token-pool
// behavior (Acquire, Refill, Available, context cancellation on empty pool) is
// unchanged by the GitHub-aware extensions.
func TestRateLimiter_ExistingBehaviorPreserved(t *testing.T) {
	rl := NewRateLimiter(5)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		if err := rl.Acquire(ctx, TierRepo); err != nil {
			t.Fatalf("acquire %d: %v", i, err)
		}
	}
	if rl.Available() != 0 {
		t.Errorf("expected 0 tokens, got %d", rl.Available())
	}

	rl.Refill()
	if rl.Available() != 5 {
		t.Errorf("after refill: got %d, want 5", rl.Available())
	}

	// Context cancellation on empty pool.
	for i := 0; i < 5; i++ {
		rl.Acquire(ctx, TierRepo)
	}
	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := rl.Acquire(cancelledCtx, TierWatch); err == nil {
		t.Error("expected error on cancelled context with empty pool")
	}
}
