# PR ingestion performance audit

Audit date: 2026-08-21
Baseline: `db35fa9` (`origin/main` when the audit started)

> The issue triage, refinement, auto-implement and autonomous pipelines were
> later removed; findings that only applied to issue ingestion have been
> dropped from this document. Worker counts reflect the daemon at audit time.

## Scope and method

The audit followed the complete path from discovery through Core NATS,
GitHub Search/Pulls, and finally the review pipeline. Git history was inspected to distinguish intentional safety
delays from regressions. Verification uses deterministic request counts,
readiness handshakes and call-boundary assertions; wall-clock time is used only
as a bounded regression guard, not as the primary proof.

## Findings and actions completed

| Bottleneck | Previous behaviour | Action |
|---|---|---|
| Cold start | Tier 1 could publish before the discovery subscriber existed; Tier 2 then slept 2 seconds and could miss work until the next 5-minute tick | Start and flush the discovery subscription first; cold polling waits for the first real snapshot, including an empty one |
| Worker startup | Pollers could publish Core NATS work before one or more consumers subscribed | Each of the six workers reports readiness after its own `Subscribe` + `Flush`; producers start only after all six succeed |
| New repository | First review was deliberately deferred one complete poll interval | Publish/flush `repo_discovered` synchronously, then enqueue the PR in the same cycle |
| PR hydration | Every candidate paid a serial Pulls GET in the adapter and another fresh GET in the bounded worker | Enqueue unhydrated Search candidates; the worker performs the single authoritative GET and applies reviewer, SHA and dedup guards there |
| Authenticated login | `/user` was fetched on every PR poll | Cache the successful login for the immutable client/token lifetime; failures remain retryable |
| PR Search accounting | Search traffic was charged to the wrong local resource | Acquire the Search resource for the request; retain first-page delivery until pagination can stream without delaying current candidates |
| PR limiter accounting | Every PR poll also consumed a generic core permit even though the warm path now performs only Search requests | Remove the duplicate core acquire; the one-time `/user` lookup remains covered by live GitHub response accounting |
| Skipped PR diff | The diff was downloaded before HEAD resolution and re-review dedup | Resolve/deduplicate first; fetch the diff only for a PR that will continue |

## Deterministic before/after budget

| Scenario | Before | After |
|---|---:|---:|
| Cold start before a useful tick | fixed 2 s minimum; race could become 1 full interval | no fixed delay; tick follows the first snapshot |
| First PR in a newly discovered repo | 1 full interval (5 min default) | same cycle |
| Adapter path for 20 PR candidates, warm login | 1 Search + 20 serial Pulls GETs | 1 Search + 0 Pulls GETs |
| End-to-end hydration for 20 accepted candidates | 20 adapter GETs + 20 worker GETs | 20 worker GETs, bounded by the existing worker pool |
| Extra core permits for warm PR discovery | 1 per cycle | 0; Search is metered once per page |
| Diff when HEAD resolution/dedup rejects the PR | 1 | 0 |

The worker remains the source of truth for `requested_reviewers` and HEAD SHA,
so the request reduction does not weaken stale-search or in-flight protection.
REST hydration was not parallelised in the adapter; this avoids adding a new
secondary-rate-limit risk.

## Verification gate

All daemon checks must run in Docker:

```bash
make test-docker
```

Focused regression coverage includes startup snapshot readiness, all worker
readiness signals, same-cycle discovery ordering, PR request budget, login
cache/retry, PR Search metering and no diff before a failed HEAD lookup.

## Follow-up plan

These items remain worthwhile but carry broader behavioural or schema risk and
should be delivered separately with their own measurements:

1. Coalesce managed-clone `fetch/reset/clean` preparation per repository and
   give all PR worktrees in a short freshness window the same prepared base.
2. Move the remaining token accounting—including topic discovery and cold
   archive checks—to actual HTTP request/page boundaries, separating
   concurrency control from hourly quota accounting.
3. Make the 500 ms pipeline retry conditional on transient network/5xx errors;
   schedule rate-limited work at `RetryAt` and do not retry permanent 4xx.
4. Add phase metrics for `poll_due → search_done → candidate_published →
   worker_start → repo_ready → AI_start`, plus endpoint/resource counters with
   no repository or PR labels.
5. Evaluate a paginated GraphQL PR snapshot (`headRefOid` + review requests)
   behind a fallback before removing the remaining one Pulls GET per worker.
6. Trigger a debounced targeted tick after relevant config reloads instead of
   waiting a complete interval, while retaining persistent claims and caps.
7. Move the persistent PR in-flight claim to the dequeue boundary so
   duplicate messages cannot both prepare repository context before one wins.
8. Bound or coalesce cold-start archived-repository checks; the six-hour cache
   removes warm-cycle cost, but the first pass still checks repositories
   serially before publishing discovery.
9. Introduce PR Search pagination only as a stream into the candidate queue,
   preserving current time-to-first-candidate while lifting the existing
   first-100-result limit.
