# Rate Limiter Integration with NATS Design

**Issue:** #298 (epic), #310 (Task 11)  
**Date:** 2026-04-24  
**Scope:** Add rate limiter to NATS workers that make GitHub API calls  

## Overview

Keep the existing token-bucket rate limiter as middleware. The pollers (Tier 1+2) already use it. Add it to the NATS worker handlers that make GitHub API calls: review worker (GetPR), publish worker (SubmitReview), and state worker (CheckItem).

NATS `MaxAckPending` controls concurrency (how many workers run simultaneously). The rate limiter controls API call rate (how many GitHub calls per hour). They complement each other.

## Changes

### 1. Expose rate limiter via Pipeline.Limiter()

Add a `Limiter() *RateLimiter` accessor to `Pipeline` (same pattern as `Queue()`). Workers access the shared limiter via `pipe.Limiter()` under `cfgMu` to handle config reloads.

### 2. Add Acquire to worker handlers

In each worker handler closure in main.go, call `limiter.Acquire()` at the top before any GitHub API call:

- **reviewHandler**: `TierRepo` (200ms) before `ghClient.GetPR()`
- **publishHandler**: `TierRepo` (200ms) before `ghClient.SubmitReview()`
- **stateHandler**: `TierWatch` (50ms) before `adapter.CheckItem()` — matches old Tier 3 priority since state checks are lightweight and high-priority

`Acquire` only returns `ctx.Err()` (shutdown). On cancellation:
- reviewHandler: returns silently (ack — daemon shutting down, PR re-detected next startup)
- publishHandler: returns wrapped error (nak — transient retry)
- stateHandler: returns `(false, error)` (nak — backoff increased)

## Files Changed

| Action | File | What |
|--------|------|------|
| Modify | `daemon/internal/scheduler/pipeline.go` | Add Limiter() accessor |
| Modify | `daemon/cmd/heimdallm/main.go` | Add Acquire calls to 3 worker handlers |

## Out of Scope

- Replacing the token bucket with NATS-native rate limiting
- Per-worker rate limit configuration
