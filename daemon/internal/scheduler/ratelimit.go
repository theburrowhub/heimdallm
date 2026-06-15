package scheduler

import (
	"context"
	"sync"
	"time"
)

// Tier identifies which polling tier is requesting API access.
type Tier int

const (
	TierDiscovery Tier = iota // Tier 1: slow, lowest priority
	TierRepo                  // Tier 2: medium
	TierWatch                 // Tier 3: fast, highest priority
)

// tierWait is how long each tier will wait for a token before giving up.
var tierWait = map[Tier]time.Duration{
	TierDiscovery: 500 * time.Millisecond,
	TierRepo:      200 * time.Millisecond,
	TierWatch:     50 * time.Millisecond,
}

// tierSafetyThreshold maps a Tier to the minimum remaining-budget below which
// that tier must wait until the reset window. Higher-priority tiers (Watch)
// use a smaller reserve so they can still make calls when budget is scarce,
// while Discovery/Repo back off earlier to protect critical work.
//
//	TierWatch     →  25  (backs off very late — only when nearly exhausted)
//	TierRepo      →  75  (medium priority)
//	TierDiscovery → 100  (lowest priority, backs off earliest)
var tierSafetyThreshold = map[Tier]int{
	TierDiscovery: 100,
	TierRepo:      75,
	TierWatch:     25,
}

// resourceBudget holds the live rate-limit state for a single GitHub resource
// category (e.g. "core", "search").
type resourceBudget struct {
	remaining int
	reset     time.Time
}

// RateLimiter is a shared token pool that governs GitHub API usage across
// all polling tiers. Higher-priority tiers (Watch) get shorter initial wait
// times, meaning they acquire tokens faster when the pool is under pressure.
//
// In addition to the token-pool burst guard, the limiter tracks GitHub's
// live X-RateLimit-Remaining budget per resource category. When remaining
// drops below the tier's safety threshold the limiter blocks until the reset
// time (respecting ctx cancellation). A secondary-limit cooldown (from
// Retry-After on 403/429) overrides everything and makes all tiers wait.
type RateLimiter struct {
	pool chan struct{}
	size int

	mu                sync.Mutex
	budgets           map[string]*resourceBudget // keyed by resource name, e.g. "core", "search"
	cooldown          time.Time                  // secondary-limit cooldown: block until this time
	baseDiscThreshold int                        // override for TierDiscovery threshold (0 = use package default)
}

// NewRateLimiter creates a rate limiter with the given number of tokens.
func NewRateLimiter(tokens int) *RateLimiter {
	pool := make(chan struct{}, tokens)
	for i := 0; i < tokens; i++ {
		pool <- struct{}{}
	}
	return &RateLimiter{
		pool:    pool,
		size:    tokens,
		budgets: make(map[string]*resourceBudget),
	}
}

// Observe records the live rate-limit state for a GitHub resource category.
// Called by the client-side observer after every API response that carries
// X-RateLimit-* headers (resource, remaining, reset).
func (r *RateLimiter) Observe(resource string, remaining int, reset time.Time) {
	if resource == "" {
		return
	}
	r.mu.Lock()
	r.budgets[resource] = &resourceBudget{remaining: remaining, reset: reset}
	r.mu.Unlock()
}

// ObserveRetryAfter sets a secondary-limit cooldown: all tiers will block
// until now+d (or ctx cancellation). This is triggered by a 403/429
// secondary rate limit from GitHub which includes a Retry-After header.
func (r *RateLimiter) ObserveRetryAfter(d time.Duration) {
	if d <= 0 {
		return
	}
	until := time.Now().Add(d)
	r.mu.Lock()
	if until.After(r.cooldown) {
		r.cooldown = until
	}
	r.mu.Unlock()
}

// Acquire blocks until a token is available or the context is done.
// Before acquiring a token it also enforces:
//  1. Any active secondary-limit cooldown (Retry-After).
//  2. Per-resource budget: if remaining < tier's safety threshold, waits
//     until the resource's reset time.
//
// The resource checked is "core" by default (the most common limit bucket).
// Tier priority determines the safety threshold: Watch backs off latest,
// Discovery earliest, so critical polling can proceed while discovery idles.
func (r *RateLimiter) Acquire(ctx context.Context, tier Tier) error {
	return r.AcquireResource(ctx, tier, "core")
}

// AcquireResource is the full form of Acquire: it checks budget for the
// named resource (e.g. "core", "search") before acquiring a token.
// Acquire() calls this with "core".
func (r *RateLimiter) AcquireResource(ctx context.Context, tier Tier, resource string) error {
	// 1. Honor secondary-limit cooldown first.
	if err := r.waitForCooldown(ctx); err != nil {
		return err
	}

	// 2. Honor proactive budget throttle for the named resource.
	if err := r.waitForBudget(ctx, tier, resource); err != nil {
		return err
	}

	// 3. Existing token-pool burst guard.
	wait := tierWait[tier]
	timer := time.NewTimer(wait)
	defer timer.Stop()

	select {
	case <-r.pool:
		return nil
	case <-timer.C:
		select {
		case <-r.pool:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	case <-ctx.Done():
		return ctx.Err()
	}
}

// waitForCooldown blocks until any active secondary-limit cooldown elapses or
// ctx is done.
func (r *RateLimiter) waitForCooldown(ctx context.Context) error {
	r.mu.Lock()
	until := r.cooldown
	r.mu.Unlock()

	now := time.Now()
	if !until.After(now) {
		return nil
	}
	delay := until.Sub(now)
	select {
	case <-time.After(delay):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// SetDiscoverySafetyThreshold overrides the base safety threshold for
// TierDiscovery. The per-tier offsets (TierRepo = base-25, TierWatch = base-75)
// are applied relative to this value when it is set.  A value of 0 or below
// falls back to the package-level default (100). Thread-safe.
func (r *RateLimiter) SetDiscoverySafetyThreshold(threshold int) {
	r.mu.Lock()
	if threshold > 0 {
		r.baseDiscThreshold = threshold
	} else {
		r.baseDiscThreshold = 0 // revert to package default
	}
	r.mu.Unlock()
}

// effectiveThreshold returns the safety threshold for a given tier, taking
// into account any SetDiscoverySafetyThreshold override.
func (r *RateLimiter) effectiveThreshold(tier Tier) int {
	r.mu.Lock()
	base := r.baseDiscThreshold
	r.mu.Unlock()
	if base <= 0 {
		return tierSafetyThreshold[tier]
	}
	// Scale the tier offsets relative to the configured base:
	//   TierDiscovery → base
	//   TierRepo      → base - 25 (same delta as default 100→75)
	//   TierWatch     → base - 75 (same delta as default 100→25)
	switch tier {
	case TierDiscovery:
		return base
	case TierRepo:
		v := base - 25
		if v < 1 {
			return 1
		}
		return v
	case TierWatch:
		v := base - 75
		if v < 1 {
			return 1
		}
		return v
	default:
		return tierSafetyThreshold[tier]
	}
}

// waitForBudget blocks until the resource's remaining budget is above the
// tier's safety threshold, or until the reset time passes, or ctx is done.
func (r *RateLimiter) waitForBudget(ctx context.Context, tier Tier, resource string) error {
	threshold := r.effectiveThreshold(tier)

	r.mu.Lock()
	b, ok := r.budgets[resource]
	if !ok {
		r.mu.Unlock()
		return nil // no budget info yet — proceed optimistically
	}
	remaining := b.remaining
	reset := b.reset
	r.mu.Unlock()

	if remaining >= threshold {
		return nil // budget is healthy — proceed immediately
	}

	// Budget is below threshold: wait until the reset time.
	now := time.Now()
	if !reset.After(now) {
		// Reset time already passed (or wasn't set) — proceed.
		return nil
	}
	delay := reset.Sub(now)
	select {
	case <-time.After(delay):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Refill restores the token pool to its original capacity.
func (r *RateLimiter) Refill() {
	for {
		select {
		case r.pool <- struct{}{}:
		default:
			return
		}
	}
}

// Available returns the number of tokens currently in the pool.
func (r *RateLimiter) Available() int {
	return len(r.pool)
}

// BudgetRemaining returns the last-observed remaining count for the given
// resource, and whether any budget info has been recorded. Used for testing
// and observability.
func (r *RateLimiter) BudgetRemaining(resource string) (remaining int, ok bool) {
	r.mu.Lock()
	b, ok := r.budgets[resource]
	if !ok {
		r.mu.Unlock()
		return 0, false
	}
	remaining = b.remaining
	r.mu.Unlock()
	return remaining, true
}
