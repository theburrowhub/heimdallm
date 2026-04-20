# Multi-Tier Polling Pipeline — Design Spec

**Date**: 2026-04-20
**Issue**: [#107](https://github.com/theburrowhub/heimdallm/issues/107)

---

## Overview

Replace the monolithic poll cycle with a 3-tier pipeline architecture. Tier 1 discovers repos, Tier 2 processes per-repo (PRs + issues), Tier 3 watches individual items for changes with exponential backoff. Connected via channels, governed by a shared rate limiter with priority.

## Architecture

```
Tier 1 (discovery_interval, 15m) → repos channel →
Tier 2 (poll_interval, 5m) → items queue →
Tier 3 (priority queue, 1m base with backoff)
```

All tiers run as independent goroutines. Rate limiter: shared token pool (4500 req/h), priority T3 > T2 > T1, min guaranteed per tier.

## Components

All in `daemon/internal/scheduler/`:

| File | Responsibility |
|------|---------------|
| `scheduler.go` | Existing timer — kept for backwards compat |
| `pipeline.go` | Orchestrator: creates tiers, wires channels, manages shutdown |
| `queue.go` | Priority queue with exponential backoff for Tier 3 |
| `ratelimit.go` | Shared token bucket with tier priorities |
| `tier1.go` | Discovery: repos by topic + new repos from PR activity |
| `tier2.go` | Per-repo: fetch PRs, fetch/classify issues, pipeline dispatch, promotion |
| `tier3.go` | Per-item: watch state changes, comments, label changes |

## Tier 1: Global Discovery (slow)

Interval: `discovery_interval` (default 15m). Responsibilities:
- Topic-based repo discovery (existing `discoverySvc.Run` logic, moved here)
- New repo detection from PR activity (repos seen in `review-requested:@me` that aren't in the monitored list)
- Repos pushed to `reposChan chan []string`

## Tier 2: Per-Repo Processing (medium)

Interval: `poll_interval` (default 5m). Consumes repos from Tier 1 channel + its own timer. Per repo:
- Fetch PRs to review → run PR pipeline → push active PRs to Tier 3 queue
- Issue promotion (dependency check)
- Fetch issues → classify → run triage/implement pipeline → push active issues to Tier 3 queue
- Retry pending publishes (`p.PublishPending()`)

## Tier 3: Per-Item Watch (fast, backoff)

Priority queue, base interval 1m. Consumes items from Tier 2. Each item:

```go
type WatchItem struct {
    Type      string        // "pr" | "issue"
    Repo      string
    Number    int
    GithubID  int64
    Priority  int           // 0 = highest
    NextCheck time.Time
    Backoff   time.Duration // 1m → 2m → 4m → 8m → 15m cap
    LastSeen  time.Time
}
```

Watches for:
- Label changes → re-classify, re-run pipeline if mode changed
- New comments / change requests → re-review/re-triage
- State changes (merge/close) → update store, trigger dependency promotion
- Backoff doubles after each check without changes; resets to 1m on activity
- Items without activity for 1h are evicted

## Rate Limiter

```go
type Tier int
const (TierDiscovery Tier = iota; TierRepo; TierWatch)

type RateLimiter struct {
    pool      chan struct{}    // 4500 tokens, refilled hourly
    minBudget map[Tier]int    // T1=200, T2=1500, T3=1000
    spent     map[Tier]*atomic.Int64
}

func (r *RateLimiter) Acquire(ctx context.Context, tier Tier) error
```

Priority: T3 acquires immediately if tokens available. T2 waits up to 100ms. T1 waits up to 500ms. If pool exhausted, lower-priority tiers pause until next refill. Min budget ensures no tier starves completely.

## Configuration

```toml
[github]
discovery_interval = "15m"   # Tier 1
poll_interval = "5m"          # Tier 2
watch_interval = "1m"         # Tier 3 base
```

New: `watch_interval`. Default 1m. Parsed by `parseWatchInterval` in main.go.

## main.go Integration

Replace `makePollFn` + manual scheduler/discovery setup with:

```go
pipe := scheduler.NewPipeline(scheduler.PipelineConfig{...}, scheduler.PipelineDeps{...})
pipe.Start(ctx)
defer pipe.Stop()
```

The `PipelineDeps` struct carries all dependencies (ghClient, store, pipelines, broker, configFn). The pipeline owns the 3 tier goroutines and the rate limiter.

## Testing

- `queue_test.go`: enqueue, dequeue by priority, backoff doubling, eviction after 1h
- `ratelimit_test.go`: token acquisition, priority ordering, min budget enforcement
- `pipeline_test.go`: integration — fake deps, verify tier flow
- `tier2_test.go`: per-repo processing with fake GitHub client
- `tier3_test.go`: watch item state change detection

## Backwards Compatibility

- `poll_interval` and `discovery_interval` keep their meaning
- `watch_interval` is new, defaults to 1m if absent
- Old `scheduler.New()` still works for non-pipeline uses
- Config validation accepts the new field without requiring it
