# PR Publish Worker (NATS Consumer) Design

**Issue:** #298 (epic), #305 (Task 6)  
**Date:** 2026-04-23  
**Scope:** NATS consumer for publishing stored reviews to GitHub, replacing PublishPending() retry loop  

## Overview

The publish worker consumes `PRPublishMsg{ReviewID}` from `heimdallm.pr.publish` and submits the stored review to GitHub. Two sources publish to this subject: (1) the review worker after a successful review (happy path), and (2) a simplified publish-pending scanner that catches up on reviews that failed initial publication. NATS retry semantics (`NakWithDelay`) replace the manual retry loop in `PublishPending()`.

## Architecture

```
Review Worker → PRPublishMsg → NATS (pr.publish) → PublishWorker → GitHub SubmitReview
                                      ↑
PublishPending scanner (periodic) ─────┘  (catch-up for failed publishes)
```

## Changes

### 1. PublishWorker (worker/publish.go)

Same pattern as ReviewWorker:

```go
type PublishWorker struct {
    js      jetstream.JetStream
    handler func(ctx context.Context, msg bus.PRPublishMsg) error
}
```

Key difference from ReviewWorker: the handler **returns an error**. This controls ack/nak behavior:
- `nil` → `msg.Ack()` (success or permanent failure)
- Non-nil error → `msg.NakWithDelay(30s)` (transient failure, retry via NATS)

`MaxDeliver=5` on the `publish-worker` consumer means 5 attempts before the message is dropped.

### 2. Handler closure (main.go)

```go
publishHandler := func(ctx context.Context, msg bus.PRPublishMsg) error {
    rev, err := s.GetReview(msg.ReviewID)
    // not found → return nil (ack, permanent)
    // already published (GitHubReviewID != 0) → return nil (ack, idempotent)

    pr, err := s.GetPR(rev.PRID)
    // not found or empty repo → mark orphaned, return nil

    // Rebuild ReviewResult from stored JSON
    // SubmitReview to GitHub
    // If 5xx/rate-limit → return error (nak, transient)
    // If success → MarkReviewPublished, return nil
}
```

### 3. PRPublishPublisher (bus/publisher.go)

```go
type PRPublishPublisher struct { js jetstream.JetStream }

func (p *PRPublishPublisher) PublishPRPublish(ctx context.Context, reviewID int64) error
```

Publishes to `SubjPRPublish` with `Nats-Msg-Id = fmt.Sprintf("rev:%d", reviewID)` for dedup. The 2-minute dedup window prevents the scanner and review worker from double-publishing the same review.

### 4. Review worker publishes PRPublishMsg

In the review handler in main.go, after `runReview` succeeds:

```go
if rev != nil && rev.GitHubReviewID == 0 {
    publishPub.PublishPRPublish(ctx, rev.ID)
}
```

`rev` is returned by `p.Run()` — it has `rev.ID` set (from `InsertReview`) and `GitHubReviewID == 0` when the initial GitHub submit failed.

Note: if `GitHubReviewID != 0`, the review was already published to GitHub during `p.Run()` and doesn't need the publish worker.

### 5. PublishPending scanner simplified

The existing `PublishPending()` in `pipeline.go` is simplified to just enqueue:

```go
func (p *Pipeline) PublishPending() {
    reviews, _ := p.store.ListUnpublishedReviews()
    for _, rev := range reviews {
        p.publishPub.PublishPRPublish(ctx, rev.ID)
    }
}
```

This still runs at the end of each Tier 2 `processTick`. Dedup by review_id means overlap with the review worker is harmless.

### 6. Store: GetReview method

Need to verify if `store.GetReview(id)` exists. If not, add it — it's a simple `SELECT ... WHERE id = ?`.

### 7. Error handling

| Error | Action | Reason |
|-------|--------|--------|
| Review not found | Ack | Permanent — review was deleted |
| Already published (GitHubReviewID != 0) | Ack | Idempotent — already done |
| PR not found / empty repo | Ack + mark orphaned | Permanent — PR record lost |
| GitHub 5xx / rate limit | NakWithDelay(30s) | Transient — retry |
| GitHub 404 (PR deleted) | Ack | Permanent — PR no longer exists |
| Other GitHub error | NakWithDelay(30s) | Assume transient |

## Files Changed

| Action | File | What |
|--------|------|------|
| Create | `daemon/internal/worker/publish.go` | PublishWorker consumer with ack/nak logic |
| Create | `daemon/internal/worker/publish_test.go` | Tests with mock handler |
| Modify | `daemon/internal/bus/publisher.go` | Add PRPublishPublisher |
| Modify | `daemon/internal/bus/publisher_test.go` | Test for PRPublishPublisher |
| Modify | `daemon/internal/pipeline/pipeline.go` | Simplify PublishPending to enqueue via NATS |
| Modify | `daemon/cmd/heimdallm/main.go` | publishHandler + PublishWorker startup + review handler publishes PRPublishMsg |
| Possibly | `daemon/internal/store/reviews.go` | Add GetReview(id) if not exists |

## Testing

1. **PublishWorker test** — mock handler, verify ack on nil return, verify nak on error return
2. **PRPublishPublisher test** — publish + consume roundtrip with dedup
3. **Smoke test** — review completes → publish message → publish worker logs activity

## Out of Scope

- Removing `PublishPending()` entirely (it stays as catch-up scanner)
- NATS events (Task 10)
- Metrics/alerting on publish failures
