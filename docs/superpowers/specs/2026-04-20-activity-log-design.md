# Activity Log — Design Spec

**Date**: 2026-04-20
**Issue**: [theburrowhub/heimdallm#113](https://github.com/theburrowhub/heimdallm/issues/113)
**Scope**: Daily activity log (storage + recording + query endpoints + Flutter tab). AI report generation is explicitly **deferred to a follow-up spec**.

---

## Overview

Add a daily activity log that records every significant Heimdallm action (PR reviews, issue triages, auto-implement runs, label promotions, errors) and exposes it through an HTTP endpoint and a new Flutter "Activity" tab. Users can pick a day or date range, filter by org/repo/action, and see a grouped-by-hour timeline of what Heimdallm did.

AI report generation ("Generate Report" button, 4th agent prompt type) is a separable feature and ships in a later spec once the activity data model is proven in production.

---

## 1. Architecture

Four pieces, each with a single clear responsibility:

1. **`activity_log` SQLite table** — append-only event log, indexed by timestamp and `(repo, ts)`. Owned by `daemon/internal/store/activity.go`.
2. **`ActivityRecorder`** — new package `daemon/internal/activity/`. A single goroutine subscribed to the existing SSE broker; translates broker events into rows.
3. **HTTP handler** — new `GET /activity` on the authenticated route set in `daemon/internal/server/handlers.go`.
4. **Flutter `ActivityScreen`** — new top-level tab under `flutter_app/lib/features/activity/` with date picker, filters, and grouped timeline.

### Key design choices

- **Append-only log as own source of truth** (not a derived view over `reviews`/`issues`). One row per event. Redundant with existing tables by design — the redundancy is what makes timeline queries a single indexed scan.
- **Recording via SSE broker subscription**, not by wrapping the store or peppering call sites. The broker already publishes `review_completed`, `review_error`, `issue_review_completed`, `issue_implemented`, `issue_review_error`. One new event (`issue_promoted`) is added in this spec.
- **Recording failures are logged and dropped.** Activity log is observability; a failed write must never block a real action.
- **Enabled by default**, toggleable via `config.toml` or `PUT /config`.

---

## 2. Data model

### Schema

Added to `daemon/internal/store/store.go` alongside the existing `CREATE TABLE IF NOT EXISTS` statements:

```sql
CREATE TABLE IF NOT EXISTS activity_log (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  ts          DATETIME NOT NULL,            -- when the action occurred (RFC3339, daemon local TZ)
  org         TEXT NOT NULL,                -- extracted from repo "org/name"
  repo        TEXT NOT NULL,                -- "org/name" full slug, matches other tables
  item_type   TEXT NOT NULL,                -- 'pr' | 'issue'
  item_number INTEGER NOT NULL,
  item_title  TEXT NOT NULL,
  action      TEXT NOT NULL,                -- 'review' | 'triage' | 'implement' | 'promote' | 'error'
  outcome     TEXT NOT NULL DEFAULT '',     -- short status: severity, label transition, error class
  details     TEXT NOT NULL DEFAULT '{}',   -- JSON payload for action-specific fields
  created_at  DATETIME NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_activity_ts      ON activity_log(ts DESC);
CREATE INDEX IF NOT EXISTS idx_activity_repo_ts ON activity_log(repo, ts DESC);
```

- `ts` stores the event time. `created_at` records when the row was written (usually microseconds later; kept separately for debugging recording lag).
- `outcome` is a top-level short string so the UI does not have to parse JSON for common display. `details` holds anything action-specific.
- `org` is denormalised from `repo` so `WHERE org = ?` is a direct index scan with no `LIKE`.

### Event → row mapping

| Broker event | `action` | `outcome` | `details` JSON keys |
|---|---|---|---|
| `review_completed` | `review` | severity (`critical`/`major`/`minor`/`none`) | `cli_used`, `review_id`, `github_review_state` |
| `review_error` | `error` | error class (`cli_not_found`/`timeout`/`parse_failed`/...) | `item_type: "pr"`, `cli_used`, `error` |
| `issue_review_completed` | `triage` | triage severity or `ignored` | `cli_used`, `category`, `chosen_action` |
| `issue_implemented` | `implement` | `pr_opened` or `pr_failed` | `cli_used`, `pr_number`, `pr_url` |
| `issue_review_error` | `error` | error class | `item_type: "issue"`, `cli_used`, `error` |
| **new** `issue_promoted` | `promote` | `from_label → to_label` | `from_label`, `to_label`, `reason` |

### New broker event: `issue_promoted`

Added to `daemon/internal/sse/broker.go`:

```go
EventIssuePromoted = "issue_promoted"
```

`daemon/internal/issues/promoter.go` gains a `Publisher` dependency (same `sse.Publisher` interface already used by `issues/pipeline.go`) and emits one event per successful label transition. Event payload:

```json
{
  "repo": "org/name",
  "issue_number": 42,
  "issue_title": "...",
  "from_label": "heimdallm:triage",
  "to_label": "heimdallm:develop",
  "reason": "auto-promote after triage: category=develop"
}
```

---

## 3. Recorder

New package `daemon/internal/activity/` with:

```
recorder.go
recorder_test.go
```

### Interface

```go
type Store interface {
    InsertActivity(ts time.Time, org, repo, itemType string,
        itemNumber int, itemTitle, action, outcome string,
        details map[string]any) error
}

type Recorder struct {
    store Store
    sub   chan sse.Event   // from broker.Subscribe()
}

func New(store Store, broker *sse.Broker) *Recorder
func (r *Recorder) Start(ctx context.Context)  // blocks until ctx cancelled
```

### Lifecycle

- Constructed in `main.go` right after the SSE broker, before the scheduler starts.
- `Start` runs the event loop in a goroutine; `main.go` cancels its context on shutdown.
- If `activity_log.enabled = false`, the recorder is not constructed or started.

### Event loop

```go
for {
    select {
    case <-ctx.Done():
        return
    case ev, ok := <-r.sub:
        if !ok { return }
        if err := r.handle(ev); err != nil {
            slog.Warn("activity: record failed", "err", err, "event", ev.Type)
        }
    }
}
```

`handle` unmarshals `ev.Data` (JSON string), maps to the target row shape using the table above, and calls `store.InsertActivity`. Unknown event types are ignored without warning (other broker consumers may publish events the recorder doesn't care about, e.g. `review_started`).

### Failure semantics

- **Store error** (disk full, locked DB): logged at warn, event dropped. Original action unaffected.
- **Unmarshal error** (malformed event payload): logged at warn, event dropped.
- **Broker channel full**: broker drops events itself (existing behavior). The recorder relies on broker back-pressure being acceptable for observability.

---

## 4. HTTP API

### `GET /activity`

Registered on the authenticated route set in `daemon/internal/server/handlers.go`.

**Query parameters** (all optional, combined with AND):

| Param | Format | Meaning |
|---|---|---|
| `date` | `YYYY-MM-DD` | Single-day window, daemon local TZ. Mutually exclusive with `from`/`to`. |
| `from` | `YYYY-MM-DD` | Inclusive start of range. Requires `to`. |
| `to` | `YYYY-MM-DD` | Inclusive end of range. Requires `from`. |
| `org` | string, repeatable | Filter by org. `?org=a&org=b` → `org IN (a, b)`. |
| `repo` | string, repeatable | Filter by full slug `org/name`. |
| `action` | string, repeatable | Filter by action (`review`/`triage`/`implement`/`promote`/`error`). |
| `limit` | int, default `500`, max `5000` | Safety cap. |

If neither `date` nor `from`/`to` is supplied, the handler defaults to **today** (daemon local TZ).

**Response**:

```json
{
  "entries": [
    {
      "id": 123,
      "ts": "2026-04-20T09:34:12+02:00",
      "org": "freepik-company",
      "repo": "freepik-company/ai-bumblebee-proxy",
      "item_type": "pr",
      "item_number": 4321,
      "item_title": "Fix rate limiter race",
      "action": "review",
      "outcome": "major",
      "details": { "cli_used": "claude", "review_id": 789, "github_review_state": "COMMENTED" }
    }
  ],
  "count": 1,
  "truncated": false
}
```

- Entries sorted by `ts DESC`.
- `truncated: true` when `limit` was reached.
- `details` is returned as already-parsed JSON, not a string.

**Errors** (all `400 Bad Request` with `{"error": "..."}` body):
- Invalid date format.
- `from` without `to` or vice versa.
- `date` combined with `from`/`to`.
- `limit` out of range.

**Disabled state**: if `activity_log.enabled = false`, returns `503 Service Unavailable` with `{"error": "activity log disabled"}`.

**Auth**: bearer token, same as all authenticated endpoints.

No write endpoint — the log is populated only by the recorder.

---

## 5. Flutter UI

### Structure

New feature directory `flutter_app/lib/features/activity/`:

```
activity_screen.dart          # main scaffold
activity_providers.dart       # state providers (match the pattern used in dashboard_providers.dart)
activity_models.dart          # ActivityEntry, ActivityQuery models
activity_api.dart             # HTTP client for GET /activity
```

### Navigation

- New top-level tab **"Activity"**, icon `Icons.timeline`.
- Inserted in the main nav between **Issues** and **Stats**:
  Dashboard → Repositories → Issues → **Activity** → Stats → Agents → Logs.
- Route `/activity` added in `router.dart`.

### Layout

Single scrollable page with a sticky top bar under the AppBar.

**Top bar**:
- Date picker: segmented control `[Today] [Yesterday] [Pick day] [Pick range]`.
  - "Today" / "Yesterday" resolve on the client using the device's local TZ. If that diverges from the daemon's TZ, the user may see an unexpected window; acceptable edge case for v1.
- Filter chips row: **Organization** (multi-select popup), **Repository** (multi-select popup), **Action** (multi-select popup). Each chip shows the selected count when active (e.g. "Organization · 2").
- "Clear filters" text button appears when any filter is active.

**Timeline**:
- Grouped by hour: `09:00`, `10:00`, ... section headers (using the daemon-local TZ present in each entry's `ts`).
- Each entry row:
  - Left: action icon — review → `rate_review`, triage → `label`, implement → `build`, promote → `swap_horiz`, error → `error_outline` in red.
  - Middle: `repo · #number · title` (ellipsis on overflow); subtitle shows outcome (`major review by claude`, `promoted: triage → develop`, `cli_not_found`).
  - Right: time `HH:mm:ss` in monospace.
- Tap → navigate to the existing PR or issue detail screen when the item still exists; otherwise show a snackbar "item no longer available".
- Empty state: centered "No activity for this period." with a button to widen to the last 7 days.
- Truncation banner at the top of the list when `truncated: true`: "Showing 500 most recent entries. Narrow filters to see more."

**Refresh**:
- Pull-to-refresh plus a refresh button in the AppBar.
- Live updates via the existing SSE stream: new events prepend to the timeline **only when the selected window includes "now"** (today, or a range ending ≥ current time). Historical day/range views ignore SSE updates.

### Out of scope for this spec

- "Generate Report" button.
- AI prompt category "Report" on agent profiles.
- Report display dialog and copy-to-clipboard.

These belong to the deferred follow-up spec for AI report generation.

---

## 6. Config

New section in `config.toml`:

```toml
[activity_log]
enabled = true          # default true
retention_days = 90     # default 90, range 1–3650
```

Parsed in `daemon/internal/config/config.go` as:

```go
type ActivityLogConfig struct {
    Enabled       bool `toml:"enabled"`
    RetentionDays int  `toml:"retention_days"`
}
```

Writable via the existing `PUT /config` endpoint (add `activity_log_enabled` and `activity_log_retention_days` keys to the allowlist in `handlers.go`). Validation for `retention_days` mirrors the existing `retention_days` field (1–3650).

When `enabled` flips from true → false: the recorder stops. Existing rows remain. When flipped back to true: recorder starts; no backfill.

---

## 7. Retention

- `main.go` calls `store.PurgeOldActivity(cfg.ActivityLog.RetentionDays)` once at startup, right after the existing `PurgeOldReviews` call.
- Scheduler gets a new 24h ticker (`activityPurgeTicker`) that calls the same function while the daemon runs.
- `PurgeOldActivity`: `DELETE FROM activity_log WHERE ts < ?` with the cutoff computed in Go (matches the `PurgeOldReviews` pattern, avoids SQLite `datetime()` comparison pitfalls noted in `store.go`).

`retention_days = 0` is a no-op (matches existing retention semantics).

---

## 8. Observability

- `slog.Debug("activity recorded", "action", ..., "repo", ..., "item", ...)` on every successful write.
- `slog.Warn("activity: record failed", "err", ..., "event", ...)` on store errors.
- `/stats` response gains an `activity_count_24h` field: `COUNT(*) FROM activity_log WHERE ts > now - 24h`. The dashboard can surface it as a "today so far" counter.

---

## 9. Testing

All Go tests run via `make test-docker` per `AGENTS.md`.

- **`daemon/internal/store/activity_test.go`** — insert + query with each filter combination, index usage, `PurgeOldActivity` cutoff behaviour, malformed JSON in `details`.
- **`daemon/internal/activity/recorder_test.go`** — inject a fake broker; publish each event type; assert expected row written (action / outcome / details). Store-failure path: inject a failing store, assert no panic, warning emitted, event dropped.
- **`daemon/internal/issues/promoter_test.go`** — new cases for `EventIssuePromoted`: publisher called once per successful transition, correct payload, no event on no-op.
- **`daemon/internal/server/handlers_test.go`** — `/activity` happy path, every `400` case, `503` when disabled, auth.
- **`flutter_app/test/features/activity/`** — widget tests for date picker resolution, filter chip multi-select, timeline hour grouping, empty state, truncation banner, tap → detail navigation.

---

## 10. Backwards compatibility

- New table, new endpoint, new SSE event type, new config section. Nothing existing changes shape.
- On upgrade: `CREATE TABLE IF NOT EXISTS` runs, recorder starts, log populates with zero historical data. Matches the issue's stated compatibility requirement.
- `issue_promoted` is a new event; existing SSE subscribers (Flutter dashboard) ignore unknown event types.

---

## 11. Rollout

Single release, split into logical PRs for reviewability:

1. `feat(daemon): activity_log table + store operations`
2. `feat(daemon): issue_promoted SSE event from promoter`
3. `feat(daemon): ActivityRecorder + config + retention`
4. `feat(daemon): GET /activity endpoint`
5. `feat(flutter): Activity screen + navigation tab`

Dependencies:

- PR 1 and PR 2 are independent and can land in either order.
- PR 3 depends on PR 1 (store ops). It can land before PR 2 — the recorder ignores unknown event types, so `promote` rows simply start appearing once PR 2 is merged.
- PR 4 depends on PR 1 only. It is functional even without a recorder (returns empty lists).
- PR 5 depends on PR 4.
