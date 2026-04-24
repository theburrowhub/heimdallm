# SSE Bridge over NATS Design

**Issue:** #298 (epic), #309 (Task 10)  
**Date:** 2026-04-24  
**Scope:** Route SSE events through NATS — bridge broker→NATS + SSE handler reads from NATS  

## Overview

Replace the direct broker→SSE handler path with NATS as intermediary. A bridge goroutine subscribes to the existing SSE broker and re-publishes every event to `heimdallm.events.*` subjects in NATS. The HTTP SSE handler (`/events`) subscribes to NATS `heimdallm.events.>` via core NATS (not JetStream) for fan-out. This removes the 10-subscriber limit (NATS handles fan-out natively) and fixes #286 (zombie connections).

## Architecture

```
Workers → broker.Publish(event)
              ↓
SSE Broker (existing, unchanged)
              ↓ bridge goroutine
NATS core (heimdallm.events.{type})
              ↓ subscribe per HTTP client
handleSSE → SSE wire format → HTTP response
```

## Changes

### 1. Bridge: broker → NATS

Goroutine in main.go that subscribes to the broker and re-publishes to NATS:

- Subscribes via `broker.Subscribe()` (uses one of the 10 broker slots)
- For each event, publishes to `heimdallm.events.{event.Type}` via core NATS `conn.Publish`
- The event data is published as-is (already JSON string from `sseData()`)
- Uses core NATS (not JetStream) — events are ephemeral, no persistence needed

### 2. SSE handler reads from NATS

`handleSSE` changes from reading a broker channel to subscribing to NATS:

- Creates a core NATS subscription on `heimdallm.events.>`
- Each incoming NATS message is formatted as SSE and flushed to the HTTP client
- When the HTTP client disconnects (`r.Context().Done()`), the subscription is cleaned up
- No subscriber limit — NATS handles unlimited fan-out

### 3. Server gets NATS connection

The `Server` struct receives a `*nats.Conn` (or nil for backward compat in tests). When conn is set, `handleSSE` uses NATS. When nil, falls back to the broker (existing behavior).

## Files Changed

| Action | File | What |
|--------|------|------|
| Modify | `daemon/internal/server/handlers.go` | Add conn field, handleSSE reads from NATS |
| Modify | `daemon/cmd/heimdallm/main.go` | Bridge goroutine, pass conn to server |

## Testing

1. Server test with mock NATS — publish event, verify SSE output
2. Smoke test — Flutter UI receives events in real-time

## Out of Scope

- Removing the SSE broker entirely (Task 12 — still used by ActivityRecorder)
- Removing broker.Publish calls from workers (Task 12)
