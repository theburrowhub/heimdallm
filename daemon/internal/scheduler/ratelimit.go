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

// tierShare is the fraction of the configured base threshold each tier
// reserves, derived from the default table above (100/75/25). The offsets used
// to be absolute (base-25, base-75), which collapsed every tier to the floor of
// 1 once the base dropped below 75 — silently removing the tier prioritisation
// the table exists to express, and disabling proactive throttling entirely
// until the budget was fully exhausted.
var tierShare = map[Tier]float64{
	TierDiscovery: 1.0,
	TierRepo:      0.75,
	TierWatch:     0.25,
}

// searchSafetyThresholdBase is the TierDiscovery threshold for the "search"
// resource. Search is quoted at 30 requests/minute, not 5000/hour, so
// X-RateLimit-Remaining never exceeds 30 there — reusing the core base of 100
// would put the budget permanently "below threshold" and stall every search.
// The per-tier shares above apply to this base the same way.
const searchSafetyThresholdBase = 6

// SearchResource is the GitHub rate-limit resource name for the Search API.
// Its budget is tracked and throttled separately from "core".
const SearchResource = "search"

// GraphQLResource is GitHub's independent GraphQL rate-limit resource.
const GraphQLResource = "graphql"

// resourceBudget holds the live rate-limit state for a single GitHub resource
// category (e.g. "core", "search").
type resourceBudget struct {
	limit      int
	remaining  int
	used       int
	reset      time.Time
	observedAt time.Time
}

// RateSnapshot is a point-in-time read of a resource's live rate-limit state,
// as last observed from GitHub's X-RateLimit-* response headers. Exposed via
// Snapshots() for callers that report the raw budget (e.g. the GET
// /github/rate_limit HTTP handler) rather than just gating on it.
type RateSnapshot struct {
	Limit      int
	Remaining  int
	Used       int
	Reset      time.Time
	ObservedAt time.Time
}

// RateLimiter governs GitHub API usage across all polling tiers. Each GitHub
// resource has its own token pool: core, search and GraphQL are independent
// quotas, so exhausting one must not consume another's static allowance.
// Higher-priority tiers (Watch) get shorter initial wait times, meaning they
// acquire tokens faster when a resource's pool is under pressure.
//
// In addition to the token-pool burst guard, the limiter tracks GitHub's
// live X-RateLimit-Remaining budget per resource category. When remaining
// drops below the tier's safety threshold the limiter blocks until the reset
// time (respecting ctx cancellation). A secondary-limit cooldown (from
// Retry-After on 403/429) overrides everything and makes all tiers wait.
type RateLimiter struct {
	mu                sync.Mutex
	pools             map[string]chan struct{}   // independent static allowance per GitHub resource
	size              int                        // capacity of each resource pool
	budgets           map[string]*resourceBudget // keyed by resource name, e.g. "core", "search"
	cooldown          time.Time                  // secondary-limit cooldown: block until this time
	baseDiscThreshold int                        // override for TierDiscovery threshold (0 = use package default)
}

const coreResource = "core"

// NewRateLimiter creates a rate limiter with the given number of tokens per
// GitHub resource. The core pool is created eagerly to preserve Available's
// historical meaning; other resource pools are allocated on first use.
func NewRateLimiter(tokens int) *RateLimiter {
	return &RateLimiter{
		pools: map[string]chan struct{}{
			coreResource: newTokenPool(tokens),
		},
		size:    tokens,
		budgets: make(map[string]*resourceBudget),
	}
}

func newTokenPool(tokens int) chan struct{} {
	pool := make(chan struct{}, tokens)
	for i := 0; i < tokens; i++ {
		pool <- struct{}{}
	}
	return pool
}

// tokenPool returns the independent static pool for resource. GitHub treats a
// missing X-RateLimit-Resource header as core, so an empty resource name does
// the same here rather than creating an accidental fourth quota.
func (r *RateLimiter) tokenPool(resource string) chan struct{} {
	if resource == "" {
		resource = coreResource
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if pool, ok := r.pools[resource]; ok {
		return pool
	}
	pool := newTokenPool(r.size)
	r.pools[resource] = pool
	return pool
}

// Observe records the live rate-limit state for a GitHub resource category.
// Called by the client-side observer after every API response that carries
// X-RateLimit-* headers.
//
// Limit/Used are merged rather than overwritten when the new observation
// doesn't carry them (snapshot.Limit <= 0): some responses (e.g. certain 304s
// behind a proxy) can omit X-RateLimit-Limit, and losing the last-known limit
// would leave observability callers unable to report a denominator even
// though the resource has been observed before.
func (r *RateLimiter) Observe(resource string, snapshot RateSnapshot) {
	if resource == "" {
		return
	}
	r.mu.Lock()
	limit := snapshot.Limit
	used := snapshot.Used
	if limit <= 0 {
		if prev, ok := r.budgets[resource]; ok {
			limit = prev.limit
			used = prev.used
		}
	}
	r.budgets[resource] = &resourceBudget{
		limit:      limit,
		remaining:  snapshot.Remaining,
		used:       used,
		reset:      snapshot.Reset,
		observedAt: snapshot.ObservedAt,
	}
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
	return r.AcquireResource(ctx, tier, coreResource)
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

	// 3. Per-resource token-pool burst guard. The live budget above was already
	// resource-specific; the static allowance must be too, or a burst of core
	// traffic can stall a healthy GraphQL quota (and vice versa).
	pool := r.tokenPool(resource)
	wait := tierWait[tier]
	timer := time.NewTimer(wait)
	defer timer.Stop()

	select {
	case <-pool:
		return nil
	case <-timer.C:
		select {
		case <-pool:
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

// effectiveThreshold returns the safety threshold for a tier on a resource,
// honouring any SetDiscoverySafetyThreshold override.
//
// The base is per-resource because the quotas differ by two orders of
// magnitude (core: 5000/hour, search: 30/minute), and the per-tier shares are
// proportional so prioritisation survives at any base.
func (r *RateLimiter) effectiveThreshold(tier Tier, resource string) int {
	share, ok := tierShare[tier]
	if !ok {
		return tierSafetyThreshold[tier]
	}

	if resource == SearchResource {
		return scaleThreshold(searchSafetyThresholdBase, share)
	}

	r.mu.Lock()
	base := r.baseDiscThreshold
	r.mu.Unlock()
	if base <= 0 {
		return tierSafetyThreshold[tier]
	}
	return scaleThreshold(base, share)
}

// scaleThreshold applies a tier's proportional share to a base, with a floor of
// 1 so a tier never ends up with a zero reserve.
func scaleThreshold(base int, share float64) int {
	v := int(float64(base)*share + 0.5)
	if v < 1 {
		return 1
	}
	return v
}

// waitForBudget blocks until the resource's remaining budget is above the
// tier's safety threshold, or until the reset time passes, or ctx is done.
func (r *RateLimiter) waitForBudget(ctx context.Context, tier Tier, resource string) error {
	threshold := r.effectiveThreshold(tier, resource)

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

// Refill restores every resource token pool to its original capacity. Pools
// created concurrently after the snapshot are already full by construction.
func (r *RateLimiter) Refill() {
	r.mu.Lock()
	pools := make([]chan struct{}, 0, len(r.pools))
	for _, pool := range r.pools {
		pools = append(pools, pool)
	}
	r.mu.Unlock()

	for _, pool := range pools {
		refillTokenPool(pool)
	}
}

func refillTokenPool(pool chan struct{}) {
	for {
		select {
		case pool <- struct{}{}:
		default:
			return
		}
	}
}

// Available returns the number of core tokens currently available, preserving
// the method's behaviour from before resource pools were separated.
func (r *RateLimiter) Available() int {
	return len(r.tokenPool(coreResource))
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

// Snapshots returns a copy of the last-observed rate-limit state for every
// resource that has received at least one Observe() call. Used by the GET
// /github/rate_limit HTTP handler to report real, measured budgets instead of
// GitHub's separate (and, for some tokens, always-pristine) GET /rate_limit
// endpoint.
func (r *RateLimiter) Snapshots() map[string]RateSnapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string]RateSnapshot, len(r.budgets))
	for resource, b := range r.budgets {
		out[resource] = RateSnapshot{
			Limit:      b.limit,
			Remaining:  b.remaining,
			Used:       b.used,
			Reset:      b.reset,
			ObservedAt: b.observedAt,
		}
	}
	return out
}
