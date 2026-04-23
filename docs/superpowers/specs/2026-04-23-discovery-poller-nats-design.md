# Discovery Poller → NATS Publisher Design

**Issue:** #298 (epic), #302 (Task 3)  
**Date:** 2026-04-23  
**Scope:** Refactor Tier 1 to publish to NATS instead of Go channel; bridge consumer feeds existing Tier 2  

## Overview

Tier 1 discovery currently sends the merged repo list to Tier 2 via a buffered Go channel (`reposChan`). This task replaces the channel write with a NATS JetStream publish on `heimdallm.discovery.repos`. A bridge consumer in `Pipeline.Start()` subscribes to the NATS subject and feeds the existing `reposChan`, keeping Tier 2 unchanged. Task 4 will remove the bridge and `reposChan` when Tier 2 is migrated.

## Changes

### 1. Tier1Publisher interface

New interface in `scheduler/tier1.go`:

```go
type Tier1Publisher interface {
    PublishRepos(ctx context.Context, repos []string) error
}
```

`Tier1Deps.ReposChan` is replaced by `Tier1Deps.Publisher`. `sendRepos()` calls `Publisher.PublishRepos(ctx, repos)` instead of sending to the channel.

### 2. RepoPublisher (NATS implementation)

New file `daemon/internal/bus/publisher.go`:

```go
type RepoPublisher struct {
    js jetstream.JetStream
}

func NewRepoPublisher(js jetstream.JetStream) *RepoPublisher
func (p *RepoPublisher) PublishRepos(ctx context.Context, repos []string) error
```

Serializes `DiscoveryMsg{Repos: repos}` and publishes to `SubjDiscoveryRepos`. No `WithMsgID` — each discovery cycle may produce a different list, and the 5-minute dedup window on the DISCOVERY stream already protects against accidental duplicate ticks.

### 3. Bridge consumer in Pipeline

New method `bridgeDiscovery(ctx, out chan<- []string)` on `Pipeline`. Launched as a goroutine in `Pipeline.Start()`.

- Gets the `discovery-consumer` durable consumer from JetStream
- Uses `Messages()` for continuous pull
- Deserializes `DiscoveryMsg`, sends `repos` to `reposChan` (non-blocking select, same as current `sendRepos`)
- Acks each message after forwarding

`Pipeline.Start()` changes:
- `reposChan` is still created (Tier 2 reads from it unchanged)
- Tier 1 no longer receives `reposChan` — it gets `Publisher` instead
- New bridge goroutine connects NATS → `reposChan`
- Tier 2 call is unchanged

### 4. PipelineDeps additions

```go
type PipelineDeps struct {
    // existing fields...
    Publisher Tier1Publisher        // new — Tier 1 NATS publisher
    JS        jetstream.JetStream  // new — bridge consumer (interim, Task 4 removes)
}
```

### 5. main.go wiring

```go
Publisher: bus.NewRepoPublisher(eventBus.JetStream()),
JS:        eventBus.JetStream(),
```

Added to the `PipelineDeps` construction in main.go.

## Files Changed

| Action | File | What |
|--------|------|------|
| Create | `daemon/internal/bus/publisher.go` | RepoPublisher struct |
| Create | `daemon/internal/bus/publisher_test.go` | RepoPublisher publish+consume test |
| Modify | `daemon/internal/scheduler/tier1.go` | Replace ReposChan with Publisher interface |
| Modify | `daemon/internal/scheduler/pipeline.go` | Add JS to PipelineDeps, bridgeDiscovery, wire in Start |
| Modify | `daemon/internal/scheduler/tier1_test.go` | Update to use mock Publisher |
| Create | `daemon/internal/scheduler/bridge_test.go` | Bridge consumer test |
| Modify | `daemon/cmd/heimdallm/main.go` | Wire Publisher and JS into PipelineDeps |

## Testing Strategy

1. **Tier1 unit tests** — mock `Tier1Publisher`, verify `PublishRepos` is called with correct merged repo list
2. **RepoPublisher test** — real embedded NATS, publish via `PublishRepos`, consume from stream, verify payload
3. **Bridge test** — publish `DiscoveryMsg` to NATS, verify it arrives on `reposChan`
4. **Existing scheduler tests** — must pass (adapter updated to provide mock Publisher)

## Out of Scope

- Changing Tier 2 to consume from NATS directly (Task 4)
- Removing `reposChan` (Task 4)
- PR poll migration (Task 4)
