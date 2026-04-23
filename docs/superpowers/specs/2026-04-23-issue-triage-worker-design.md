# Issue Triage Worker (NATS Consumer) Design

**Issue:** #298 (epic), #306 (Task 7)  
**Date:** 2026-04-23  
**Scope:** Refactor Fetcher to publish issues to NATS + create triage worker consumer  

## Overview

Refactors the issue processing pipeline so the Fetcher publishes classified issues to NATS (`heimdallm.issue.triage` or `heimdallm.issue.implement`) instead of calling `pipeline.Run` directly. A new TriageWorker consumes from `heimdallm.issue.triage` and runs the review pipeline in ReviewOnly mode. This task only implements the triage consumer — Task 8 adds the implement consumer.

## Architecture

```
Tier 2 ProcessRepo → Fetcher.ProcessRepo (fetch + classify + dedup)
                        ↓ review_only issues
                     IssuePublisher.PublishIssueTriage → NATS (issue.triage) → TriageWorker → issuePipe.Run(ReviewOnly)
                        ↓ develop issues
                     IssuePublisher.PublishIssueImplement → NATS (issue.implement) → (Task 8 consumer)
```

## Changes

### 1. IssuePublisher interface

New interface for the Fetcher to publish classified issues:

```go
type IssuePublisher interface {
    PublishIssueTriage(ctx context.Context, repo string, number int, githubID int64) error
    PublishIssueImplement(ctx context.Context, repo string, number int, githubID int64) error
}
```

### 2. NATSIssuePublisher (bus/publisher.go)

```go
type NATSIssuePublisher struct { js jetstream.JetStream }

func (p *NATSIssuePublisher) PublishIssueTriage(ctx, repo, number, githubID) error
func (p *NATSIssuePublisher) PublishIssueImplement(ctx, repo, number, githubID) error
```

Both use `Nats-Msg-Id = fmt.Sprintf("issue:%d", githubID)` for dedup.

### 3. Fetcher modification

Add optional `publisher` field to `Fetcher`. When set, the per-issue dispatch in `ProcessRepo` publishes to NATS instead of calling `pipeline.Run`:

- Issue classified as `review_only` → `publisher.PublishIssueTriage()`
- Issue classified as `develop` → `publisher.PublishIssueImplement()`
- Issue classified as `ignore` or `blocked` → skip (same as today)

When publisher is nil (tests, backward compat), falls back to calling `pipeline.Run` directly.

### 4. TriageWorker (worker/triage.go)

Same pattern as ReviewWorker:

```go
type TriageWorker struct {
    js      jetstream.JetStream
    handler func(ctx context.Context, msg bus.IssueMsg)
}
```

Consumes from `triage-worker` durable consumer. Handler in main.go:
1. Fetch issue from GitHub (fresh data)
2. Resolve RunOptions from config
3. Force Mode = ReviewOnly
4. Call `issuePipe.Run(issue, opts)`
5. Always ack (same strategy as ReviewWorker)

### 5. main.go wiring

- Create `NATSIssuePublisher` and pass to Fetcher
- Create `triageHandler` closure
- Start `TriageWorker` with cancellable context

## Files Changed

| Action | File | What |
|--------|------|------|
| Modify | `daemon/internal/bus/publisher.go` | Add NATSIssuePublisher |
| Modify | `daemon/internal/bus/publisher_test.go` | Tests |
| Modify | `daemon/internal/issues/fetcher.go` | Add publisher field, publish when set |
| Modify | `daemon/internal/issues/fetcher_test.go` | Test publisher path |
| Create | `daemon/internal/worker/triage.go` | TriageWorker consumer |
| Create | `daemon/internal/worker/triage_test.go` | Tests |
| Modify | `daemon/cmd/heimdallm/main.go` | Wire publisher, triageHandler, TriageWorker |

## Testing

1. **NATSIssuePublisher test** — publish + consume roundtrip for both triage and implement subjects
2. **TriageWorker test** — embedded NATS, mock handler, verify called with correct data
3. **Fetcher test** — mock publisher, verify PublishIssueTriage called for review_only issues
4. **Smoke test** — issue detected → triage worker logs processing

## Out of Scope

- Implement worker consumer (Task 8)
- Removing the pipeline.Run fallback in Fetcher (stays for backward compat until Task 12)
- NATS events for issue completion (Task 10)
