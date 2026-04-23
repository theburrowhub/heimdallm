# State Check Poller → NATS Design

**Issue:** #298 (epic), #308 (Task 9)  
**Date:** 2026-04-24  
**Scope:** Replace WatchQueue with NATS KV + state poller + state worker consumer  

## Overview

Replace the in-memory WatchQueue (min-heap) with a NATS JetStream KV bucket for durable watch state, a poller goroutine that publishes `StateCheckMsg` for ready items, and a state worker consumer that checks GitHub state and updates backoff. Watch state now survives daemon restarts.

## Architecture

```
Workers (review/triage/implement) → KV Put (enroll item)
                                        ↓
NATS KV "HEIMDALLM_WATCH" (backoff state, durable)
                                        ↓ scan every 30s
State Poller → publish StateCheckMsg for ready items
                                        ↓
NATS (state.check) → StateWorker → CheckItem (GitHub API)
                                    ↓ changed → HandleChange + KV reset backoff
                                    ↓ unchanged → KV double backoff
                                    ↓ stale (>1h) → KV delete
```

## Changes

### 1. NATS KV Bucket

Bucket name: `HEIMDALLM_WATCH`. Created in `Bus.Start()` alongside streams.

```go
type WatchEntry struct {
    Type      string    `json:"type"`
    Repo      string    `json:"repo"`
    Number    int       `json:"number"`
    GithubID  int64     `json:"github_id"`
    NextCheck time.Time `json:"next_check"`
    BackoffNs int64     `json:"backoff_ns"` // time.Duration as nanoseconds
    LastSeen  time.Time `json:"last_seen"`
}
```

Key format: `pr:{github_id}` or `issue:{github_id}`.

Constants: initialBackoff=1m, maxBackoff=15m, evictAfter=1h (same as current WatchQueue).

### 2. WatchKV wrapper (bus/watch.go)

```go
type WatchKV struct { kv jetstream.KeyValue }

func (w *WatchKV) Enroll(ctx, entry WatchEntry) error     // Put with initial backoff
func (w *WatchKV) Get(ctx, key string) (*WatchEntry, error)
func (w *WatchKV) ResetBackoff(ctx, key string, observedAt time.Time) error
func (w *WatchKV) IncreaseBackoff(ctx, key string) error
func (w *WatchKV) Delete(ctx, key string) error
func (w *WatchKV) ScanReady(ctx) ([]WatchEntry, error)    // all where NextCheck <= now
func (w *WatchKV) EvictStale(ctx) (int, error)            // delete where LastSeen + 1h < now
```

### 3. State Poller (main.go goroutine)

Every 30s:
1. `watchKV.EvictStale()` — clean up
2. `watchKV.ScanReady()` — get items due for check
3. For each, publish `StateCheckMsg` to NATS

### 4. StateWorker (worker/state.go)

Consumes from `state-worker` consumer. Handler:
1. Read WatchEntry from KV
2. Call CheckItem (same logic as current Tier 3 adapter)
3. Changed → HandleChange + `watchKV.ResetBackoff()`
4. Unchanged → `watchKV.IncreaseBackoff()`
5. Always Ack

### 5. StateCheckPublisher (bus/publisher.go)

```go
func PublishStateCheck(ctx, stateCheckMsg) error
```

No dedup — the poller is the single publisher and runs every 30s.

### 6. Watch enrollment in workers

Replace `pipe.Queue().Push(&WatchItem{...})` with `watchKV.Enroll(ctx, WatchEntry{...})` in:
- reviewHandler (main.go)
- ProcessPR (tier2Adapter)
- HandleChange (tier2Adapter)

## Files Changed

| Action | File | What |
|--------|------|------|
| Create | `daemon/internal/bus/watch.go` | WatchKV wrapper + WatchEntry type |
| Create | `daemon/internal/bus/watch_test.go` | KV CRUD + ScanReady + EvictStale tests |
| Modify | `daemon/internal/bus/bus.go` | Create KV bucket in Start(), expose WatchKV() |
| Modify | `daemon/internal/bus/publisher.go` | Add StateCheckPublisher |
| Create | `daemon/internal/worker/state.go` | StateWorker consumer |
| Create | `daemon/internal/worker/state_test.go` | Tests |
| Modify | `daemon/cmd/heimdallm/main.go` | State poller, stateHandler, StateWorker, replace Queue().Push with KV Enroll |

## Testing

1. **WatchKV tests** — Enroll, Get, ResetBackoff, IncreaseBackoff, ScanReady, EvictStale
2. **StateWorker test** — mock handler
3. **Integration** — enroll → poller publishes → worker consumes
4. **Smoke test** — PR enters watch, state poller logs activity

## Out of Scope

- Removing WatchQueue code entirely (Task 12)
- Removing Tier 3 RunTier3 (Task 12)
