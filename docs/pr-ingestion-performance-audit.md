# PR and issue ingestion performance audit

Audit date: 2026-08-21
Baseline: `db35fa9` (`origin/main` when the audit started)

## Scope and method

The audit followed the complete path from discovery through Core NATS,
GitHub Search/Pulls, issue promotion and local classification, and finally the
review pipeline. Git history was inspected to distinguish intentional safety
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
| GraphQL budget | GraphQL issue search waited on the REST Search budget | Give GraphQL its independent gate/resource |
| Issue query size | One query concatenated every `repo:` qualifier, recreating a known failure above roughly 40 repositories | Chunk deterministically at 25 repos and 1,024 URL-encoded query bytes; discard/fallback only the failed or truncated chunk |
| Issue promotion | `PromoteReady` listed every repository serially before the aggregate Search fetched the same issues again | Build the raw Search snapshot first and share it with promotion and local classification; absent chunks retain per-repo REST fallback, only blocked candidates get a fresh validation, and a successful label change is reflected locally for same-cycle ingestion |
| Local limiter tokens | A successful issue snapshot still consumed one core token per repository; 75 repos at one-minute polling exhausted the 4,500-token pool in an hour without making those requests | Acquire core permits only for actual per-repo fallback; keep one promotion permit because dependency reads and writes remain remote |
| Issue comments | Marker scanning and manual-stage audit could fetch the same comments twice in one pass | Share one successful fetch per issue/pass; a failed first fetch is not cached, preserving the audit retry |
| Skipped PR diff | The diff was downloaded before HEAD resolution and re-review dedup | Resolve/deduplicate first; fetch the diff only for a PR that will continue |

## Deterministic before/after budget

| Scenario | Before | After |
|---|---:|---:|
| Cold start before a useful tick | fixed 2 s minimum; race could become 1 full interval | no fixed delay; tick follows the first snapshot |
| First PR in a newly discovered repo | 1 full interval (5 min default) | same cycle |
| Adapter path for 20 PR candidates, warm login | 1 Search + 20 serial Pulls GETs | 1 Search + 0 Pulls GETs |
| End-to-end hydration for 20 accepted candidates | 20 adapter GETs + 20 worker GETs | 20 worker GETs, bounded by the existing worker pool |
| Extra core permits for warm PR discovery | 1 per cycle | 0; Search is metered once per page |
| Open-issue enumeration for 47 repos with promotion configured | 47 serial list GETs + 1 oversized Search | 2 bounded Search chunks + 0 list GETs; only blocked candidates get a fresh validation |
| Core permits for 47 locally classified issue repos | 48 per cycle (cycle + repo) | 0 without promotion; 1 with promotion |
| Comments for marker scan + stage audit | 2 per issue/pass | 1 per issue/pass |
| Diff when HEAD resolution/dedup rejects the PR | 1 | 0 |

The worker remains the source of truth for `requested_reviewers` and HEAD SHA,
so the request reduction does not weaken stale-search or in-flight protection.
REST hydration was not parallelised in the adapter; this avoids adding a new
secondary-rate-limit risk. Likewise, issue Search is used only to discover
promotion candidates: each blocked candidate is refreshed before dependency
checks, so eventual index lag cannot apply labels from stale state.

## Verification gate

All daemon checks must run in Docker:

```bash
make test-docker
```

Focused regression coverage includes startup snapshot readiness, all worker
readiness signals, same-cycle discovery ordering, PR request budget, login
cache/retry, PR Search metering, Search-vs-GraphQL gates, 47-repo chunk coverage,
per-chunk fallback, promotion snapshot reuse, local-token decisions, comment
reuse and no diff before a failed HEAD lookup.

## Follow-up plan

These items remain worthwhile but carry broader behavioural or schema risk and
should be delivered separately with their own measurements:

1. Coalesce managed-clone `fetch/reset/clean` preparation per repository and
   give all PR worktrees in a short freshness window the same prepared base.
2. Persist an issue-comment/marker watermark keyed by issue and `updated_at`,
   allowing unchanged issues to avoid comment RTTs across cycles while keeping
   “latest marker wins” and retry semantics.
3. Move the remaining token accounting—including topic discovery and cold
   archive checks—to actual HTTP request/page boundaries, separating
   concurrency control from hourly quota accounting.
4. Make the 500 ms pipeline retry conditional on transient network/5xx errors;
   schedule rate-limited work at `RetryAt` and do not retry permanent 4xx.
5. Add phase metrics for `poll_due → search_done → candidate_published →
   worker_start → repo_ready → AI_start`, plus endpoint/resource counters with
   no repository or PR labels.
6. Evaluate a paginated GraphQL PR snapshot (`headRefOid` + review requests)
   behind a fallback before removing the remaining one Pulls GET per worker.
7. Trigger a debounced targeted tick after relevant config reloads instead of
   waiting a complete interval, while retaining persistent claims and caps.
8. Move the persistent PR/issue in-flight claim to the dequeue boundary so
   duplicate messages cannot both prepare repository context before one wins.
9. Bound or coalesce cold-start archived-repository checks; the six-hour cache
   removes warm-cycle cost, but the first pass still checks repositories
   serially before publishing discovery.
10. Introduce PR Search pagination only as a stream into the candidate queue,
    preserving current time-to-first-candidate while lifting the existing
    first-100-result limit.
11. Batch autonomous issue selection across repositories and dispatch with a
    small cross-repo concurrency cap; today one long `Drive` delays every later
    repository by design.
