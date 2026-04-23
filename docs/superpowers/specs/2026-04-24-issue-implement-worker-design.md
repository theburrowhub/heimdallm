# Issue Implement Worker (NATS Consumer) Design

**Issue:** #298 (epic), #307 (Task 8)  
**Date:** 2026-04-24  
**Scope:** NATS consumer for heimdallm.issue.implement — runs issue pipeline in Develop mode  

## Overview

Mirror of the TriageWorker (Task 7) but for `develop` issues. Consumes from `implement-worker` durable consumer on `HEIMDALLM_WORK`. The Fetcher already publishes to `heimdallm.issue.implement` for issues classified as `develop` (added in Task 7). This task adds the consumer side.

## Changes

### 1. ImplementWorker (worker/implement.go)

Same pattern as TriageWorker:

```go
type ImplementWorker struct {
    js      jetstream.JetStream
    handler func(ctx context.Context, msg bus.IssueMsg)
}
```

Consumes from `implement-worker` consumer. Always acks. Panic recovery via safeHandle.

### 2. Handler closure (main.go)

Same as triageHandler but with `Mode = config.IssueModeDevelop`. The `RunOptions` are identical — the mode determines the pipeline behavior (triage posts a comment, implement creates a branch + PR).

## Files Changed

| Action | File | What |
|--------|------|------|
| Create | `daemon/internal/worker/implement.go` | ImplementWorker consumer |
| Create | `daemon/internal/worker/implement_test.go` | Test |
| Modify | `daemon/cmd/heimdallm/main.go` | implementHandler + ImplementWorker startup |

## Out of Scope

- NATS events for issue completion (Task 10)
