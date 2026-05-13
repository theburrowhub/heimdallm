# Plan — Issue #483: auto_implement no-changes limbo

> **Status: DONE.** Landed in PR #488 (2026-05-13). Kept as historical
> reference alongside other completed plans in this directory; the PR
> description carries the merge-ready summary. See the PR review for
> the follow-up polish (marker-collision fix, Flutter `error` payload
> alignment, `NEEDS ATTENTION` badge, dead-code removal) that landed on
> top of the original three sub-fixes described below.

Closes `theburrowhub/heimdallm#483`. Three small fixes inside the
`auto_implement` no-changes fallback path so the issue reaches a
clean terminal state instead of silently looping in the fetcher.

## Problem statement (verbatim from the issue)

When `auto_implement` runs but the agent produces no changes, the
daemon posts a fallback comment and stores
`ActionTaken: "auto_implement_no_changes"` — but does **not** post
a `MarkerDone` marker. On every subsequent poll, both branches of
the dedup gate in `fetcher.go:300-317` return `skip=true`, so the
issue is silently ignored every poll cycle. The log fills with
"skipping issue" debug entries, the UI shows it stuck in
"develop" with no activity, and the user gets a "death spiral"
perception even though the daemon is healthy.

Two related defects:

1. `autoImplementNoChangesFallback` (`pipeline.go:889-925`) omits
   `MarkerDone` from the comment body. Compare with the success
   path (`pipeline.go:842-844`) which prepends it.
2. The dedup gate (`fetcher.go:300-317`) is functionally a
   no-op: both the cap-exceeded branch and the cap-not-yet-hit
   branch return `skip=true`. The `MaxAutoImplementNoChanges = 1`
   cap therefore has no effect — the issue can never be retried
   automatically without a `MarkerRetry`.

## Current state — evidence

| Concern | Location | Snippet / fact |
|---|---|---|
| Marker scan in fetcher | `fetcher.go:269-276` | `ScanMarkers(comments)` is run BEFORE the dedup gate. A `MarkerDone` short-circuits to `(true, "done marker", nil)` immediately. |
| Success path posts MarkerDone | `pipeline.go:841-844` | `linkBackBody := fmt.Sprintf("%s\n✅ ...", MarkerDone, ...)` |
| No-changes path does NOT | `pipeline.go:890-896` | `body := fmt.Sprintf("## ⚠️ Heimdallm auto-implement skipped\n\n...")` — no MarkerDone |
| Dead dedup gate | `fetcher.go:300-317` | Both `if noChangesCount >= MaxAutoImplementNoChanges` and the fall-through return `skip=true`. The cap-check has no behavioural effect. |
| Cap constant | `fetcher.go:41` | `const MaxAutoImplementNoChanges = 1` |
| Current SSE emit | `pipeline.go:919-922` | `EventIssueReviewCompleted` with `mode: "auto_implement_no_changes"` — UI renders this as a "completed" success state. |
| Available event for terminal-needs-attention | `sse/broker.go:21` | `EventIssueReviewError = "issue_review_error"` already exists and the UI renders it as a red/warn state. |
| Tests touching this surface | `pipeline_test.go:1015-1030`, `fetcher_test.go:500-590` | The pipeline test only asserts `ActionTaken`; the fetcher tests verify the dedup gate as a "skip permanently" path. |

## Design

Three sub-fixes, in TDD order:

### A. Add `MarkerDone` to the fallback comment body

Prepend the marker to the comment body so the fetcher's marker
scan (which runs before the dedup gate) treats the issue as
terminal:

```go
body := fmt.Sprintf(
    "%s\n## ⚠️ Heimdallm auto-implement skipped\n\n"+
        "The agent looked at #%d but left the working tree unchanged — it likely needs a human decision or more context than the issue alone provides.\n\n"+
        "Add a `%s` marker (or remove this marker) and a retry marker (`%s`) to run auto-implementation again, or remove the develop label to stop here.\n\n"+
        "---\n*auto_implement → review_only fallback · Heimdallm*",
    MarkerDone, issue.Number, MarkerDone, MarkerRetry,
)
```

The marker is invisible in the rendered GitHub comment (HTML
comment syntax) but visible in raw markdown. After this change:

- New no-changes runs reach the fetcher's marker scan first
  (`ScanMarkers → MarkerResultDone → skip with "done marker"`)
  before ever touching the dedup gate.
- A user who wants to retry adds `MarkerRetry` to a comment;
  `ScanMarkers` returns `MarkerResultRetry` and the issue is
  reprocessed (existing behaviour, now actually reachable).

### B. Switch the SSE event from `*ReviewCompleted` to `*ReviewError`

`EventIssueReviewCompleted` is interpreted by the UI as a clean
success state. A no-changes run is **not** clean: the user needs
to either supply more context + retry or close the issue. Switch
to `EventIssueReviewError` so the UI renders it as a
needs-attention card:

```go
p.publish(sse.EventIssueReviewError, map[string]any{
    "issue_id": issueID, "number": issue.Number, "repo": issue.Repo,
    "reason":   "auto_implement_no_changes",
    "post_ok":  postErr == nil,
    "message":  "Agent left the working tree unchanged; add MarkerRetry to retry or close the issue.",
})
```

`ActionTaken` in the store stays
`ActionAutoImplementNoChanges` for back-compat with the existing
counter logic and Flutter consumers that already read this
column.

### C. Simplify the now-back-compat dedup gate

After (A), every NEW no-changes run posts `MarkerDone`, so the
fetcher's marker scan handles the skip. The dedup gate at
`fetcher.go:300-317` only matters for **historical rows** that
were written before the fix landed (no MarkerDone in the
comment, but `ActionTaken: auto_implement_no_changes` in the
store). Collapse the gate to a single unconditional skip with a
clearer reason — the cap-based logic was never load-bearing:

```go
if latest.ActionTaken == ActionAutoImplementNoChanges && issue.Mode == config.IssueModeDevelop {
    // Back-compat path for rows written before #483's MarkerDone
    // fix. New no-changes runs are terminated by MarkerDone in
    // the comment scan above; this block only triggers on legacy
    // store rows that never got the marker. Add a MarkerRetry
    // comment to reopen.
    return true, "auto_implement produced no changes (historical row, no done marker); add retry marker to reprocess", nil
}
```

`MaxAutoImplementNoChanges` and `CountAutoImplementNoChanges`
become unreferenced. Leave the constant for the activity-log
consumers but stop calling the counter. Open a separate cleanup
issue if we want to delete the column.

## Tests

| Test | What it pins |
|---|---|
| `TestAutoImplementNoChangesFallback_PostsDoneMarker` | The PostComment body contains `MarkerDone` (and `MarkerRetry` as the retry hint). |
| `TestAutoImplementNoChangesFallback_EmitsReviewError` | The SSE event is `EventIssueReviewError` with `reason="auto_implement_no_changes"`, not `EventIssueReviewCompleted`. |
| `TestAlreadyProcessed_DoneMarkerSkipsBeforeDedupGate` | A row with `ActionTaken=auto_implement_no_changes` AND `MarkerDone` in comments returns `(true, "done marker", nil)` — proves the marker-first ordering. |
| `TestAlreadyProcessed_HistoricalNoChangesRow_StillSkipped` | A row with `ActionTaken=auto_implement_no_changes` AND no marker (back-compat) still returns skip with the new historical-row reason. |
| `TestAlreadyProcessed_RetryMarker_ReprocessesNoChanges` | A row with `ActionTaken=auto_implement_no_changes` AND a `MarkerRetry` in comments returns `(false, "", nil)` — retry path actually reachable now. |

## Implementation order (TDD)

1. **RED+GREEN (A)**: failing test asserts `MarkerDone` in fallback comment body; add the marker.
2. **RED+GREEN (B)**: failing test asserts `EventIssueReviewError` emission; switch event type + payload.
3. **RED+GREEN (C)**: failing test asserts retry path becomes reachable for legacy rows once a `MarkerRetry` is posted; collapse the dedup gate.
4. **Docs**: short note in `configuration-guide.md` near the auto_implement section explaining the new terminal-state semantics and the retry escape hatch.

## Re-entry checklist (post-compact)

1. `cd /Users/imunoz/Projects/ai-platform/heimdallm && git checkout main && git pull --ff-only`
2. `git checkout -b fix/issue-483-auto-implement-no-changes-limbo`
3. Read this plan top-to-bottom.
4. Hot-context files to re-read:
   - `daemon/internal/issues/pipeline.go` (lines 810–930 for the success + fallback paths)
   - `daemon/internal/issues/fetcher.go` (lines 240–340 for `alreadyProcessed` + dedup gate)
   - `daemon/internal/issues/markers.go` (whole file — short)
   - `daemon/internal/issues/pipeline_test.go` (existing fallback test around line 1015)
   - `daemon/internal/issues/fetcher_test.go` (existing dedup tests around line 500)
   - `daemon/internal/sse/broker.go` (event constants)
5. Start with TDD step 1 (RED test for MarkerDone in fallback body). Use `make test-docker` between every change.
6. Branch name: `fix/issue-483-auto-implement-no-changes-limbo`. PR opens as **draft**.

## Risks

| Risk | Mitigation |
|---|---|
| Flutter UI doesn't recognise `EventIssueReviewError` with this reason | Event type itself is already handled; the `reason` field is new but Flutter unmarshals open-ended JSON. Worst case: existing red card without specific reason copy. |
| Activity log queries on `ActionTaken="auto_implement_no_changes"` break if we change it | `ActionTaken` value is unchanged in the store; only the SSE event type changes. |
| Historical rows in production stores stay stuck even after the fix | The back-compat path explicitly accepts them; user can manually post `MarkerRetry` to reopen. Migration is not in scope (no DB rewrite). |

## Out of scope (follow-ups)

- Delete the `MaxAutoImplementNoChanges` constant and `CountAutoImplementNoChanges` store method once we are confident no legacy rows remain.
- Flutter-side rendering polish: distinct icon/colour for `reason=auto_implement_no_changes` vs other `*ReviewError` causes.
- A daemon admin endpoint to bulk-mark historical no-changes rows with `MarkerDone` to clean up old fleet state.
