# Multi-Tier Polling Pipeline Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the monolithic poll cycle with a 3-tier pipeline: discovery → per-repo → per-item watch, with priority queue and shared rate limiter.

**Architecture:** Three goroutines connected via channels. Tier 1 discovers repos. Tier 2 processes repos (PRs + issues). Tier 3 watches active items with exponential backoff. A shared rate limiter with priority (T3>T2>T1) governs API usage. All in `daemon/internal/scheduler/`.

**Tech Stack:** Go 1.21, channels, sync/atomic, container/heap

---

## File Structure

| File | Responsibility |
|------|---------------|
| `daemon/internal/scheduler/ratelimit.go` | **Create** — Shared token bucket with tier priorities |
| `daemon/internal/scheduler/ratelimit_test.go` | **Create** — Tests |
| `daemon/internal/scheduler/queue.go` | **Create** — Priority queue with exponential backoff |
| `daemon/internal/scheduler/queue_test.go` | **Create** — Tests |
| `daemon/internal/scheduler/pipeline.go` | **Create** — Orchestrator, wires tiers |
| `daemon/internal/scheduler/tier1.go` | **Create** — Discovery tier |
| `daemon/internal/scheduler/tier2.go` | **Create** — Per-repo processing tier |
| `daemon/internal/scheduler/tier3.go` | **Create** — Per-item watch tier |
| `daemon/internal/scheduler/pipeline_test.go` | **Create** — Integration test |
| `daemon/internal/config/config.go` | **Modify** — Add WatchInterval |
| `daemon/cmd/heimdallm/main.go` | **Modify** — Replace makePollFn with Pipeline |

---

### Task 1: Rate Limiter

**Files:**
- Create: `daemon/internal/scheduler/ratelimit.go`
- Create: `daemon/internal/scheduler/ratelimit_test.go`

- [ ] **Step 1: Write tests**

```go
package scheduler

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestRateLimiter_AcquireWithinBudget(t *testing.T) {
	rl := NewRateLimiter(100) // 100 tokens
	ctx := context.Background()
	for i := 0; i < 50; i++ {
		if err := rl.Acquire(ctx, TierRepo); err != nil {
			t.Fatalf("acquire %d: %v", i, err)
		}
	}
}

func TestRateLimiter_PriorityOrdering(t *testing.T) {
	// Pool of 1 token — T3 should get it before T1
	rl := NewRateLimiter(1)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	// Drain the single token
	rl.Acquire(ctx, TierWatch)

	// Now T1 should fail (no tokens, low priority)
	ctx2, cancel2 := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel2()
	if err := rl.Acquire(ctx2, TierDiscovery); err == nil {
		t.Error("expected timeout for low-priority tier with empty pool")
	}
}

func TestRateLimiter_ContextCancellation(t *testing.T) {
	rl := NewRateLimiter(0) // no tokens
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := rl.Acquire(ctx, TierWatch); err == nil {
		t.Error("expected error on cancelled context")
	}
}

func TestRateLimiter_Refill(t *testing.T) {
	rl := NewRateLimiter(10)
	ctx := context.Background()
	// Drain all
	for i := 0; i < 10; i++ {
		rl.Acquire(ctx, TierRepo)
	}
	// Refill
	rl.Refill()
	// Should work again
	if err := rl.Acquire(ctx, TierRepo); err != nil {
		t.Fatalf("acquire after refill: %v", err)
	}
}
```

- [ ] **Step 2: Implement rate limiter**

```go
package scheduler

import (
	"context"
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

// RateLimiter is a shared token pool that governs GitHub API usage across
// all polling tiers. Higher-priority tiers (Watch) get shorter wait times,
// meaning they acquire tokens faster when the pool is under pressure.
type RateLimiter struct {
	pool chan struct{}
	size int
}

// NewRateLimiter creates a rate limiter with the given number of tokens.
// Tokens represent API calls available in the current refill window.
func NewRateLimiter(tokens int) *RateLimiter {
	pool := make(chan struct{}, tokens)
	for i := 0; i < tokens; i++ {
		pool <- struct{}{}
	}
	return &RateLimiter{pool: pool, size: tokens}
}

// Acquire blocks until a token is available or the context is done.
// Lower-priority tiers have longer wait timeouts before they give up,
// effectively yielding to higher-priority tiers under pressure.
func (r *RateLimiter) Acquire(ctx context.Context, tier Tier) error {
	wait := tierWait[tier]
	timer := time.NewTimer(wait)
	defer timer.Stop()

	select {
	case <-r.pool:
		return nil
	case <-timer.C:
		// Tier-specific timeout — try once more with context deadline
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

// Refill restores the token pool to its original capacity.
// Called periodically (e.g. every hour) by the pipeline.
func (r *RateLimiter) Refill() {
	for {
		select {
		case r.pool <- struct{}{}:
		default:
			return // pool full
		}
	}
}

// Available returns the number of tokens currently in the pool.
func (r *RateLimiter) Available() int {
	return len(r.pool)
}
```

- [ ] **Step 3: Run tests**

Run: `cd daemon && go test ./internal/scheduler/ -run "TestRateLimiter" -v -timeout 30s`

- [ ] **Step 4: Commit**

```bash
git add daemon/internal/scheduler/ratelimit.go daemon/internal/scheduler/ratelimit_test.go
git commit -m "feat(scheduler): add shared rate limiter with tier priorities"
```

---

### Task 2: Priority Queue

**Files:**
- Create: `daemon/internal/scheduler/queue.go`
- Create: `daemon/internal/scheduler/queue_test.go`

- [ ] **Step 1: Write tests**

Tests for: enqueue, dequeue by priority + NextCheck, backoff doubling, cap at 15m, eviction after 1h inactivity, reset backoff on activity.

- [ ] **Step 2: Implement priority queue**

Uses `container/heap`. `WatchItem` struct with Type, Repo, Number, GithubID, Priority, NextCheck, Backoff, LastSeen. Methods: `Push(item)`, `Pop() *WatchItem` (returns item with earliest NextCheck that's past due), `ReEnqueue(item)` (doubles backoff, updates NextCheck), `ResetBackoff(item)`, `Evict()` (removes items inactive >1h), `Len()`.

- [ ] **Step 3: Run tests**

Run: `cd daemon && go test ./internal/scheduler/ -run "TestQueue" -v -timeout 30s`

- [ ] **Step 4: Commit**

```bash
git add daemon/internal/scheduler/queue.go daemon/internal/scheduler/queue_test.go
git commit -m "feat(scheduler): add priority queue with exponential backoff"
```

---

### Task 3: Tier 1 — Discovery

**Files:**
- Create: `daemon/internal/scheduler/tier1.go`

- [ ] **Step 1: Implement Tier 1**

Tier 1 runs on a ticker (discovery_interval). Each tick:
1. Acquires rate limit token (TierDiscovery)
2. Runs topic-based discovery (existing discoverySvc logic)
3. Sends discovered repos list to `reposChan`

Interface-driven: takes `DiscoveryService` interface + `RateLimiter` + `reposChan chan<- []string`. Runs in a goroutine, stops on context cancellation.

```go
type DiscoveryDeps struct {
    Discovery interface {
        Discovered() []string
    }
    Limiter   *RateLimiter
    ReposChan chan<- []string
    ConfigFn  func() (repos []string, nonMonitored []string, topic string, orgs []string)
    Interval  time.Duration
}

func RunTier1(ctx context.Context, deps DiscoveryDeps)
```

- [ ] **Step 2: Commit**

```bash
git add daemon/internal/scheduler/tier1.go
git commit -m "feat(scheduler): add Tier 1 discovery goroutine"
```

---

### Task 4: Tier 2 — Per-Repo Processing

**Files:**
- Create: `daemon/internal/scheduler/tier2.go`

- [ ] **Step 1: Implement Tier 2**

Tier 2 runs on a ticker (poll_interval). Each tick:
1. Merges repos from Tier 1 channel + static config
2. For each repo (rate limited, TierRepo):
   a. Fetch PRs to review → run PR pipeline → enqueue active PRs to Tier 3
   b. Issue promotion pass
   c. Fetch issues → classify → run pipeline → enqueue active issues to Tier 3
3. Retry pending publishes

Takes interfaces for all dependencies. The core logic is extracted from the current `makePollFn` in main.go — same dedup, same grace window, same in-flight guard.

```go
type Tier2Deps struct {
    Limiter       *RateLimiter
    WatchQueue    *WatchQueue
    GHClient      interface{ ... }  // FetchPRsToReview, FetchIssues, etc.
    PRPipeline    interface{ Run(...); PublishPending() }
    IssueFetcher  interface{ ProcessRepo(...) }
    Store         interface{ ... }
    Broker        interface{ Publish(sse.Event) }
    ConfigFn      func() *config.Config
    Interval      time.Duration
}

func RunTier2(ctx context.Context, deps Tier2Deps, reposChan <-chan []string)
```

- [ ] **Step 2: Commit**

```bash
git add daemon/internal/scheduler/tier2.go
git commit -m "feat(scheduler): add Tier 2 per-repo processing goroutine"
```

---

### Task 5: Tier 3 — Per-Item Watch

**Files:**
- Create: `daemon/internal/scheduler/tier3.go`

- [ ] **Step 1: Implement Tier 3**

Tier 3 runs on a tight ticker (watch_interval, default 1m). Each tick:
1. Evict stale items from queue (>1h inactive)
2. Pop all items whose NextCheck is past due
3. For each item (rate limited, TierWatch):
   a. Fetch current state from GitHub (single API call per item)
   b. Compare with stored state (updated_at, labels, state, comment count)
   c. If changed: reset backoff, trigger appropriate action (re-review, re-triage, re-classify)
   d. If unchanged: re-enqueue with doubled backoff
4. Publish SSE events for state changes

```go
type Tier3Deps struct {
    Limiter    *RateLimiter
    Queue      *WatchQueue
    GHClient   interface{ ... }
    Store      interface{ ... }
    Broker     interface{ Publish(sse.Event) }
    ConfigFn   func() *config.Config
    Interval   time.Duration
}

func RunTier3(ctx context.Context, deps Tier3Deps)
```

- [ ] **Step 2: Commit**

```bash
git add daemon/internal/scheduler/tier3.go
git commit -m "feat(scheduler): add Tier 3 per-item watch with backoff"
```

---

### Task 6: Pipeline Orchestrator

**Files:**
- Create: `daemon/internal/scheduler/pipeline.go`
- Create: `daemon/internal/scheduler/pipeline_test.go`

- [ ] **Step 1: Implement Pipeline**

```go
type PipelineConfig struct {
    DiscoveryInterval time.Duration
    PollInterval      time.Duration
    WatchInterval     time.Duration
    RateLimitPerHour  int // default 4500
}

type PipelineDeps struct {
    GHClient      ...
    Store         ...
    PRPipeline    ...
    IssuePipeline ...
    IssueFetcher  ...
    Discovery     ...
    Broker        ...
    ConfigFn      func() *config.Config
}

type Pipeline struct { ... }

func NewPipeline(cfg PipelineConfig, deps PipelineDeps) *Pipeline
func (p *Pipeline) Start(ctx context.Context)
func (p *Pipeline) Stop()
```

Start spawns: Tier 1 goroutine, Tier 2 goroutine, Tier 3 goroutine, rate limiter refill ticker (hourly). Stop cancels the context and waits for all goroutines to finish.

- [ ] **Step 2: Write integration test with fakes**

Test: start pipeline with fake deps, verify Tier 1 sends repos, Tier 2 processes them, items appear in Tier 3 queue.

- [ ] **Step 3: Commit**

```bash
git add daemon/internal/scheduler/pipeline.go daemon/internal/scheduler/pipeline_test.go
git commit -m "feat(scheduler): add Pipeline orchestrator wiring 3 tiers"
```

---

### Task 7: Config — add WatchInterval

**Files:**
- Modify: `daemon/internal/config/config.go`

- [ ] **Step 1: Add WatchInterval field**

Add to `GitHubConfig`:
```go
WatchInterval string `toml:"watch_interval"`
```

Add env var in `applyEnvOverrides`:
```go
if v := os.Getenv("HEIMDALLM_WATCH_INTERVAL"); v != "" {
    c.GitHub.WatchInterval = v
}
```

Add `parseWatchInterval` helper (default 1m).

- [ ] **Step 2: Commit**

```bash
git add daemon/internal/config/config.go
git commit -m "feat(config): add watch_interval for Tier 3 polling"
```

---

### Task 8: main.go — Replace makePollFn with Pipeline

**Files:**
- Modify: `daemon/cmd/heimdallm/main.go`

- [ ] **Step 1: Replace the monolithic poll cycle**

Remove `makePollFn`, `startScheduler`, the manual discovery loop management. Replace with:

```go
pipe := scheduler.NewPipeline(scheduler.PipelineConfig{
    DiscoveryInterval: parseDiscoveryInterval(cfg.GitHub.DiscoveryInterval),
    PollInterval:      parsePollInterval(cfg.GitHub.PollInterval),
    WatchInterval:     parseWatchInterval(cfg.GitHub.WatchInterval),
    RateLimitPerHour:  4500,
}, scheduler.PipelineDeps{
    GHClient:      ghClient,
    Store:         s,
    PRPipeline:    p,
    IssuePipeline: issuePipe,
    IssueFetcher:  issueFetcher,
    Discovery:     discoverySvc,
    Broker:        broker,
    ConfigFn:      func() *config.Config { cfgMu.Lock(); defer cfgMu.Unlock(); return cfg },
})

ctx, cancel := context.WithCancel(context.Background())
pipe.Start(ctx)
defer func() { cancel(); pipe.Stop() }()
```

Update reload callback to stop + restart the pipeline with new config.

- [ ] **Step 2: Verify daemon builds**

Run: `cd daemon && go build -o bin/heimdallm ./cmd/heimdallm`

- [ ] **Step 3: Run full tests**

Run: `cd /path/to/project && make test-docker`

- [ ] **Step 4: Commit**

```bash
git add daemon/cmd/heimdallm/main.go
git commit -m "feat: replace monolithic poll cycle with multi-tier pipeline (#107)"
```

---

### Task 9: Full Verification

- [ ] **Step 1: `make test-docker`** — all packages pass
- [ ] **Step 2: `go build`** — binary builds
- [ ] **Step 3: `flutter analyze`** — no issues (no Flutter changes, but verify)
- [ ] **Step 4: Manual test** — start daemon, verify logs show 3 tiers running
