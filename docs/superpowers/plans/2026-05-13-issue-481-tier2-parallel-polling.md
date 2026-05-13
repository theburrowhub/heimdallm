# Plan — Issue #481: Tier 2 parallel polling

Closes `theburrowhub/heimdallm#481`. Three independent fixes inside the
Tier 2 polling loop, grouped under one PR because they share state and
the test fixtures.

## Problem statement (verbatim from the issue)

`runTier2.processTick()` (`main.go:2083-2170`) runs PR polling and
issue polling **sequentially in a single goroutine**:

1. **Cross-tier blocking** — a slow issue cycle on `repoC` delays PR
   detection across every repo because the entire tick must return
   before the next ticker fires.
2. **Per-repo serialisation** — issues are processed repo-by-repo
   inside the tick (`for _, repo := range currentRepos`), so 20
   repos × 1s per repo = 20s of wall time even when the operations
   are trivially parallelisable.
3. **SSE ordering race** — `repo_discovered` and `review_started`
   are published from different goroutines. The UI can render
   "review in progress" for a repo the user has never seen.

## Current state — evidence

| Concern | Location | Snippet |
|---|---|---|
| Sequential PR → issue inside tick | `main.go:2083-2170` | PR work then `for _, repo := range currentRepos { adapter.ProcessRepo(...) }` |
| Repo discovery side-effect | `adapter.FetchPRsToReview` `main.go:2415-2451` | Calls `upsertDiscoveredRepos` + `processDiscoveredRepos` synchronously; publishes `EventRepoDiscovered` to the in-process broker before returning. |
| SSE bridge to NATS | `main.go:175-186` | Broker → NATS subject `heimdallm.events.<type>` via a single goroutine. |
| Per-repo rate limiter | `scheduler.RateLimiter` | Already supports concurrent `Acquire` calls — safe under a worker pool. |

## Design

### A. Run PR and issue tiers on independent tickers

Per the original issue ("tier of PRs needs its own loop/ticker so
that a slow issue cycle can never delay PR detection") the two
tiers now run as independent goroutines, each with its own ticker:

```go
go func() {
    ticker := time.NewTicker(interval); defer ticker.Stop()
    if coldStart { prTick() }
    for { select { case <-ctx.Done(): return; case <-ticker.C: prTick() } }
}()
go func() {
    ticker := time.NewTicker(interval); defer ticker.Stop()
    if coldStart { issueTick() }
    for { select { case <-ctx.Done(): return; case <-ticker.C: issueTick() } }
}()
```

Each tier serialises against itself: `time.Ticker` drops extra ticks
when the previous run is still in flight, so we never spawn two
concurrent issue cycles. The PR ticker fires every `interval`
regardless of how long an issue cycle is taking.

Both tiers emit their own `polling_started`/`polling_completed`
events — operators see two parallel SSE cycles per interval rather
than the previous single combined cycle.

`adapter.PublishPending()` (NATS retry) runs at the tail of each PR
tick; the typical pending publishes come from PR fetch and the
retry is idempotent, so binding it to one tier is fine.

### B. Parallelise per-repo issue processing

Replace the sequential per-repo loop with a worker pool bounded by a
new config knob (default 5, mirrors `MaxConcurrentWorkers`):

```go
type issueResult struct { repo string; n int; err error }
results := make(chan issueResult, len(currentRepos))
sem := make(chan struct{}, cap)
var wg sync.WaitGroup
for _, repo := range currentRepos {
    wg.Add(1)
    sem <- struct{}{}
    go func(repo string) {
        defer wg.Done()
        defer func() { <-sem }()
        n, err := adapter.ProcessRepo(ctx, repo)
        results <- issueResult{repo, n, err}
    }(repo)
}
wg.Wait()
close(results)
for r := range results { ... aggregate ... }
```

The existing `limiter.Acquire(ctx, scheduler.TierRepo)` call stays
inside each worker — the scheduler rate limiter is concurrency-safe
and continues to throttle the GitHub API hits as before. The
worker-pool cap is wall-clock parallelism, not API rate.

Config:
- New field `ai.tier2_repo_concurrency int` (TOML `tier2_repo_concurrency`).
- Default 5 in `applyDefaults`.
- 0 or negative falls back to default.

### C. Defer reviews on newly-discovered repos by one tick

The current sequence is:

```
FetchPRsToReview()
    upsertDiscoveredRepos()                — appends repoX to config
    processDiscoveredRepos()               — publishes EventRepoDiscovered
returns PR list including repoX's PR
processTick → prPublisher.PublishPRReview(repoX, ...) — enqueues NATS
ReviewWorker picks up NATS — publishes EventReviewStarted
```

In theory `EventRepoDiscovered` is published into the broker before
`PublishPRReview` is even called. In practice the SSE bridge
re-publishes through NATS (`broker → bridgeCh → eventBus.Publish`),
and the ReviewWorker's `EventReviewStarted` flows through the same
NATS instance — observed race in the field is real, and the simplest
robust fix is to **defer the review by one tick**: any PR whose repo
was added this tick is skipped this cycle and picked up next time.

Implementation:
1. `FetchPRsToReview` returns `(prs []scheduler.Tier2PR, addedThisTick map[string]bool, err error)`.
2. `processTick` filters out PRs whose repo is in `addedThisTick`.
3. By the next tick (≤ poll interval), the UI has already received
   `EventRepoDiscovered` for those repos.

Trade-off: first review on a never-before-seen repo is delayed by
one poll interval. Acceptable — the alternative (synchronising with
the SSE handler over NATS) is significantly more invasive.

## Tests

| Test | What it pins |
|---|---|
| `TestTier2ProcessTick_RunsPRAndIssueTiersConcurrently` | A spy adapter with a blocked-issue `ProcessRepo` does not delay `FetchPRsToReview`; PR call returns while issue tier is still running. |
| `TestTier2ProcessTick_ParallelPerRepoIssueProcessing` | 10 repos × 50ms `ProcessRepo` completes in << 500ms total; observed concurrency reaches the configured cap. |
| `TestTier2ProcessTick_RespectsRepoConcurrencyCap` | With cap=2 and 6 repos, never more than 2 `ProcessRepo` calls in flight. |
| `TestTier2ProcessTick_NewlyDiscoveredRepoReviewIsDeferred` | First tick after a PR is discovered for repoX: `EventRepoDiscovered` emitted, `PublishPRReview` NOT called for that PR. Second tick: `PublishPRReview` IS called. |
| `TestApplyDefaults_Tier2RepoConcurrency` | Default 5; non-zero value preserved. |

## Implementation order (TDD)

1. **RED+GREEN**: add `tier2_repo_concurrency` to config with default. Update tests in `config_test.go`.
2. **RED+GREEN (B)**: per-repo parallel processing inside `runIssueTier`. Refactor `processTick` to extract `runIssueTier`.
3. **RED+GREEN (A)**: PR and issue tiers in parallel goroutines.
4. **RED+GREEN (C)**: `FetchPRsToReview` returns the `addedThisTick` set; `processTick` defers review for those PRs.
5. Docs: update `configuration-guide.md` with the new knob + a short note on the deferred-review semantics.

## Risks

| Risk | Mitigation |
|---|---|
| Worker-pool data races on aggregated counts | Use channels + final reduce; no shared mutable state inside workers. |
| Concurrent SSE `polling_started`/`completed` events confuse Flutter | The two events are tagged with `kind` (`prs`/`issues`); UI already keys on `kind`. Verified in `event_summary.dart` from PR #475. |
| `FetchPRsToReview` signature change ripples through tests | Update spy/fake at the same time; signature is internal to `scheduler.Tier2PRFetcher`. |

## Out of scope (follow-ups)

- Different cadences per tier (`pr_poll_interval` vs `issue_poll_interval`).
- Parallelising the PR fan-out itself across many repos (today's PR fetch is a single search query, not per-repo).
- Streaming SSE events with delivery acknowledgement instead of the deferred-review workaround.
