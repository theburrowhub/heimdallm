# PR Poll → NATS Publisher Design

**Issue:** #298 (epic), #303 (Task 4)  
**Date:** 2026-04-23  
**Scope:** Refactor Tier 2 PR fetching to publish to NATS instead of spawning goroutines  

## Overview

Tier 2 currently spawns an unbounded goroutine per eligible PR to run `ProcessPR` + `WatchQueue.Push`. This task replaces the goroutine spawn with a NATS JetStream publish on `heimdallm.pr.review`. The review consumer (Task 5) will handle ProcessPR + WatchQueue.Push. Dedup is achieved via `Nats-Msg-Id = {github_id}:{head_sha}` with the 2-minute dedup window on HEIMDALLM_WORK.

## Changes

### 1. Tier2PRPublisher interface

New interface in `scheduler/tier2.go`:

```go
type Tier2PRPublisher interface {
    PublishPRReview(ctx context.Context, repo string, number int, githubID int64, headSHA string) error
}
```

Added to `Tier2Deps`. The interface uses primitive fields (not `Tier2PR` or `bus.PRReviewMsg`) so that the scheduler package has no dependency on the bus package.

### 2. PRReviewPublisher (NATS implementation)

Added to `daemon/internal/bus/publisher.go`:

```go
type PRReviewPublisher struct {
    js jetstream.JetStream
}

func NewPRReviewPublisher(js jetstream.JetStream) *PRReviewPublisher
func (p *PRReviewPublisher) PublishPRReview(ctx context.Context, repo string, number int, githubID int64, headSHA string) error
```

- Encodes `PRReviewMsg{Repo, Number, GithubID, HeadSHA}`
- Publishes to `SubjPRReview` with `WithMsgID(fmt.Sprintf("%d:%s", githubID, headSHA))`
- The 2-minute dedup window on HEIMDALLM_WORK prevents duplicate processing if two poll ticks detect the same PR+SHA

### 3. Tier 2 processTick changes

**Removed:**
- `go func(p Tier2PR) { ProcessPR + WatchQueue.Push }(pr)` — the unbounded goroutine spawn
- Direct calls to `PRProcessor.ProcessPR` inside the PR loop
- Direct calls to `WatchQueue.Push` inside the PR loop

**Replaced with:**
```go
if err := deps.PRPublisher.PublishPRReview(ctx, pr.Repo, pr.Number, pr.ID, pr.HeadSHA); err != nil {
    slog.Error("tier2: publish PR review", "repo", pr.Repo, "pr", pr.Number, "err", err)
}
```

**Unchanged:**
- `PRProcessor.PublishPending()` at end of processTick — still called directly
- Issue processing, promotion — no changes
- `WatchQueue` in Tier2Deps — still used by Tier 3
- `PRProcessor` in Tier2Deps — still used for `PublishPending()`

### 4. main.go wiring

```go
PRPublisher: bus.NewPRReviewPublisher(eventBus.JetStream()),
```

Added to `PipelineDeps` construction.

## Files Changed

| Action | File | What |
|--------|------|------|
| Modify | `daemon/internal/bus/publisher.go` | Add PRReviewPublisher |
| Modify | `daemon/internal/bus/publisher_test.go` | Add PRReviewPublisher publish + dedup test |
| Modify | `daemon/internal/scheduler/tier2.go` | Add Tier2PRPublisher interface/field, replace goroutine with publish |
| Create | `daemon/internal/scheduler/tier2_test.go` | Tier 2 unit test with mock publisher |
| Modify | `daemon/internal/scheduler/pipeline.go` | Add PRPublisher to PipelineDeps |
| Modify | `daemon/cmd/heimdallm/main.go` | Wire PRPublisher |

## Testing

1. **PRReviewPublisher test** — real NATS, publish via PublishPRReview, consume from review-worker, verify payload and Nats-Msg-Id dedup
2. **Tier2 unit test** — mock PRPublisher, verify PublishPRReview called per eligible PR, verify skipped for non-monitored and already-reviewed PRs

## Out of Scope

- PR review consumer (Task 5)
- WatchQueue.Push migration (Task 5 consumer handles it, Task 9 replaces WatchQueue)
- Removing PRProcessor from Tier2Deps (still needed for PublishPending)
