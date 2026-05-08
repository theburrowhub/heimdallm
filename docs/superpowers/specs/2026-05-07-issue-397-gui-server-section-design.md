# Issue #397 — GUI Server section (PR 1 of 2)

**Status:** Draft, pending user review.
**Branch:** `feat/gui-server-section-397` (worktree at `.worktrees/gui-server-397`).
**Issue:** https://github.com/theburrowhub/heimdallm/issues/397
**Scope:** Flutter desktop GUI only. The matching TUI Server screen ships in a follow-up PR with its own spec.

## Goal

Consolidate operational controls — daemon start/stop, listen-URL editing, a live SSE event feed, and the existing logs view — into a single `/server` route. Add a small daemon-side change so the new event feed actually sees poll cycles (`polling_started` / `polling_completed`).

## Why this shape

The issue describes a "Server" section spanning four capabilities. Cross-referencing with the codebase, most of the underlying machinery already exists:

| Capability | Existing | New |
|---|---|---|
| Start/Stop | `DaemonLifecycle` + `_confirmShutdown` / `_startDaemon` in `dashboard_screen.dart`; `POST /shutdown` endpoint | Just relocation + reuse |
| Listen URL | `cfg.Server.Port` + `cfg.Server.BindAddr` already in TOML | UI editor + restart-required UX |
| Live Events | `GET /events` SSE with 14 typed events | Compact-row UI; daemon emits `polling_*` |
| Logs | `LogsScreen` (211 lines) at `/logs` | Refactor body into reusable `LogsView` |

So this PR is mostly a navigation/UX consolidation plus two genuinely new pieces (Listen URL editor, Events feed) and a tiny daemon addition (poll events + `/health` enrichment).

## Non-goals

- **TUI Server screen** — separate PR with its own spec.
- **Hot-reload of GUI's API client when the daemon port changes.** `PlatformServicesImpl` is constructed once at `main()` with port 7842 baked in. We warn the user and require a desktop-app restart on port changes.
- **Persisting polling events to the activity log.** They stay transient (SSE-only). The activity log is for completed work; poll cycles fire every 60s per repo and would flood the table.
- **systemd / launchctl integration.** The Restart action uses the existing `Process.start` + `/shutdown` path that Dashboard already uses. Service-manager integration is a separate concern.
- **Event payload schema changes.** We render whatever the daemon already emits; no breaking changes to existing event shapes.

## Routing & navigation

- New route `/server` (default tab `status`); supports `/server?tab=events` and `/server?tab=logs` query params for deep-linking.
- The AppBar's existing **Logs icon** at `flutter_app/lib/features/dashboard/dashboard_screen.dart:58-62` is replaced by a **Server icon** (`Icons.dns_outlined` or similar) that opens `/server`.
- The existing `/logs` route is kept and **redirects to `/server?tab=logs`**, so tray menu / notification deep-links and any stored bookmarks keep working.
- The AppBar **Start/Stop button** at `dashboard_screen.dart:39-57` **stays** — quick-toggle is the most common operational action, and the Server screen offers the richer surface alongside.

## Components

### 1. `flutter_app/lib/features/server/server_screen.dart` (new)

Top-level `ConsumerStatefulWidget` with `Scaffold` + `AppBar` + 3-tab `TabBar`: **Status / Events / Logs**. Reads daemon liveness via the same providers Dashboard uses: `daemonHealthProvider` (AsyncValue<bool>) and `daemonStartingProvider` (StateProvider<bool>).

- If daemon is stopped, Events and Logs tabs render a small placeholder (`Icon(Icons.power_off)` + "Server is stopped — start it to see live data" + Start button) instead of attempting to connect to SSE. This avoids an immediate reconnect storm and gives the user a clear next step.
- The active tab is driven by the `?tab=` query param so deep links work; default `status`.

### 2. `flutter_app/lib/features/server/widgets/status_tab.dart` (new)

A single `Card`-based form view containing:

**State indicator** — Running / Stopped / Starting / Stopping pill, mirroring the current dashboard logic.

**Start/Stop button** — Reuses `_confirmShutdown` / `_startDaemon` from dashboard; those are extracted into shared helpers (see component 7 below).

**Listen URL editor** — Two fields, autosaved with the existing `PATCH /config` + override pattern from `repo_detail_screen.dart`:
- `BindAddr` text field (e.g. `127.0.0.1`, `0.0.0.0`)
- `Port` numeric field (default 7842)

After a successful save where either field changed, a yellow `MaterialBanner` appears inside the Status tab:

> **Listen URL changed.** Restart the server for it to take effect. **[Restart server]**

The Restart button calls `POST /shutdown`, waits for the process to exit, and respawns via `DaemonLifecycle.ensureRunning()`. While the restart is in flight the button is disabled and a spinner is shown.

If the **port** changed (not just bind addr) the banner additionally renders a smaller second line:

> Port change also requires restarting the desktop app for the GUI to reconnect.

The banner is computed from a snapshot of the listen-URL fields taken when the screen first loads vs. the current values, so it stays sticky until the user actually clicks Restart (or until the screen is destroyed). This avoids the banner flickering on every keystroke during autosave debounce.

**Uptime + version** — A small read-only line: `Heimdallm v0.6.13 — running for 1h 23m`. Sourced from the enriched `/health` response (see daemon-side change below). If `version` or `started_at` are missing from the response (older daemon), the line silently degrades to whichever fields are present.

### 3. `flutter_app/lib/features/server/widgets/events_tab.dart` (new)

`ConsumerStatefulWidget` consuming `/events` SSE through the existing `SseClient`:

- Bounded ring buffer of the last **500 events**. (LogsScreen uses 2000 lines, but each event is richer and renders to multiple widgets when expanded; 500 keeps memory and rebuild cost in check.)
- Each row renders compactly: `HH:MM:SS  <icon>  <type>  <human-summary>`. Tapping a row toggles inline expansion to a `SelectableText` JSON pretty-print of the raw payload.
- Top-toolbar controls:
  - **Pause/resume auto-scroll** toggle (matches LogsScreen behavior).
  - **Filter chips** — multi-select across logical groups: `pr_*`, `issue_*`, `polling_*`, `state_*`, `circuit_breaker`. Hidden events stay in the buffer (so toggling a chip back on reveals them) but are not rendered.
  - **Search box** — substring match against the rendered summary line. Composable with filter chips.
  - **Clear** button — drops the current buffer (does not affect the daemon).
- A small connection-status pill at the right of the toolbar: green "Connected", yellow "Reconnecting…", red "Disconnected" (when daemon is down).

### 4. `flutter_app/lib/features/server/event_summary.dart` (new)

Pure function:

```dart
String summarize(String type, Map<String, dynamic> payload);
({IconData icon, Color color}) glyphFor(String type);
```

Maps each known event type to a one-line summary and a glyph. Unknown types fall back to `<type> <repo>` (or just `<type>` if no repo). Tested in isolation with table-driven tests.

Mapping (initial):
- `pr_detected` → `◆  pr_detected  <repo>#<number>`
- `review_started` → `▶  review_started  <repo>#<number> (<agent>)`
- `review_completed` → `✓  review_completed  <repo>#<number> in <duration>`
- `review_error` / `review_skipped` → `✕` / `–`
- `issue_detected` / `issue_review_started` / `issue_review_completed` / `issue_refinement_done` / `issue_implemented` / `issue_review_error` / `issue_promoted` — analogous
- `pr_state_changed` / `issue_state_changed` → `↻  <type>  <repo>#<n>  <old> → <new>`
- `circuit_breaker_tripped` → `⚠  circuit_breaker  <reason>`
- `repo_discovered` → `+  repo_discovered  <repo>`
- `polling_started` → `…  polling_started  <kind> (<n> repos)`
- `polling_completed` → `·  polling_completed  <kind> in <duration_ms>ms (<count>)`

### 5. `flutter_app/lib/features/server/server_actions.dart` (new)

Extracted helpers:
- `Future<void> confirmShutdown(BuildContext, WidgetRef)` (was `_confirmShutdown` in dashboard).
- `Future<void> startDaemon(BuildContext, WidgetRef)` (was `_startDaemon`).
- `Future<void> restartDaemon(BuildContext, WidgetRef)` — new: stop + start sequence used by the Restart banner.

Dashboard's local versions become one-line forwarders so its existing call sites are unchanged.

### 6. `flutter_app/lib/features/logs/logs_screen.dart` (refactor)

Extract the existing body into a new `LogsView` widget exported from the same file (no new file needed; keeps the diff focused). `LogsScreen` becomes a thin `Scaffold(appBar: ..., body: LogsView())` so the standalone `/logs` route still works during the deprecation window. Internal state (ring buffer, scroll controller, SSE subscription) lives in `_LogsViewState`. **Public API of `LogsScreen` does not change.**

The Server screen's Logs tab embeds `LogsView` directly.

### 7. `flutter_app/lib/features/dashboard/dashboard_screen.dart` (modify)

- Replace the `/logs` AppBar icon (`Icons.article_outlined`) with `Icons.dns_outlined` linking to `/server`.
- Replace `_confirmShutdown` and `_startDaemon` bodies with one-line calls into `server_actions.dart`.
- No other changes; the dashboard's tab structure stays the same.

### 8. `flutter_app/lib/shared/router.dart` (modify)

- Add `GoRoute(path: '/server', ...)` mapping `?tab=` to the Server screen's initial tab.
- Keep `GoRoute(path: '/logs', ...)` as a `redirect: (ctx, state) => '/server?tab=logs'`.

### 9. Daemon-side polling events (modify)

**File: `daemon/internal/sse/broker.go`** — add two constants:

```go
EventPollingStarted   = "polling_started"
EventPollingCompleted = "polling_completed"
```

**Files: `daemon/internal/pipeline/pipeline.go` and `daemon/internal/issues/pipeline.go`** — at the start and end of each poll cycle (the loops that iterate monitored repos and call GitHub list endpoints), publish:

- `polling_started` — payload `{"kind": "prs"|"issues", "repos": ["org/foo", ...]}`. Emitted once per cycle, before any repo work.
- `polling_completed` — payload `{"kind": "prs"|"issues", "count": <items_seen>, "duration_ms": <int>}`. Emitted once per cycle, after all repos in the cycle have been processed.

If a cycle is cancelled via context, no `polling_completed` is emitted (consistent with how other "completed" events behave under cancellation).

**Activity recorder is NOT extended.** `daemon/internal/activity/recorder.go`'s `handle` switch keeps its existing case list. We add a regression test asserting that recorder ignores `polling_*` events (negative assertion).

### 10. Daemon-side `/health` enrichment (modify)

**File: `daemon/internal/server/handlers.go` — `handleHealth`.**

Currently:
```go
writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
```

Becomes:
```go
writeJSON(w, http.StatusOK, map[string]any{
    "status":     "ok",
    "version":    srv.version,    // from ldflags / build constant
    "started_at": srv.startedAt,  // RFC3339
})
```

`Server` struct gains `version string` and `startedAt time.Time` fields, set in `New(...)` / `NewWithOptions(...)`. The version string is sourced from the existing build-flag mechanism (to be confirmed at implementation — likely `daemon/cmd/heimdallm/main.go` already injects via `-ldflags "-X main.version=..."`; if not, a constant is fine for now).

The Flutter side treats both fields as optional — old daemons without them still work, the Status tab just shows less.

## Data flow

```
┌──────────────────────────────────────┐  GET /events SSE   ┌────────────────────┐
│ /server screen                       │ ◀───────────────── │ daemon /events     │
│ ┌──────────────────────────────────┐ │                    │  publishes:        │
│ │ events_tab                       │ │                    │   pr_detected      │
│ │  - 500-event ring buffer         │ │                    │   review_*         │
│ │  - filter chips + search         │ │                    │   issue_*          │
│ │  - compact rows + JSON expand    │ │                    │   polling_*  (new) │
│ └──────────────────────────────────┘ │                    │   ...              │
│ ┌──────────────────────────────────┐ │  PATCH /config     └────────────────────┘
│ │ status_tab                       │ │ ─────────────────▶ writes TOML
│ │  - state indicator               │ │
│ │  - Start/Stop                    │ │  POST /shutdown    triggers daemon stop
│ │  - Listen URL editor (autosave)  │ │ ─────────────────▶ Process.start respawns
│ │  - Restart-required banner       │ │  GET /health       version + uptime
│ │  - Version + uptime              │ │ ─────────────────▶
│ └──────────────────────────────────┘ │
│ ┌──────────────────────────────────┐ │  GET /logs/stream
│ │ logs_view (refactor of existing) │ │ ─────────────────▶ unchanged
│ └──────────────────────────────────┘ │
└──────────────────────────────────────┘
```

## Edge cases & error handling

- **Daemon down on `/server` open** — Status tab shows Stopped + Start button; Events and Logs tabs show a stub placeholder. No SSE connect attempts.
- **SSE drops mid-session** — Existing `SseClient` reconnect logic handles it; Events tab shows a yellow "Reconnecting…" pill until the next message lands.
- **Concurrent listen-URL edits from another client** (e.g. operator hand-edits TOML while the screen is open) — Last-write-wins, same as other autosaved fields. Banner is computed from the screen's local snapshot, not the server response, so the user still sees the restart prompt for their local edits.
- **Restart click while daemon already restarting** — Button disabled while `daemonStarting || daemonStopping` is true.
- **Port conflict on restart** — `Process.start` succeeds but `/health` polling fails. The existing `_daemonStartHealthMaxAttempts` (80×100ms = 8s) timeout in dashboard surfaces an error toast; user sees daemon as stopped and can revert the port via the still-open Status tab.
- **Empty Events feed at first open** — Hint: "Waiting for events. Polling cycle runs every 60s by default."
- **Cancelled poll cycle** — No `polling_completed` event. Events tab shows a hanging `polling_started` row; that's intentional (matches the `review_started` without `review_completed` pattern when an in-flight review is cancelled).

## Testing

**Unit (Dart):**
- `flutter_app/test/features/server/event_summary_test.dart` — table-driven test covering each known event type's summary mapping, including `polling_*` and unknown-type fallback.

**Widget (Dart):**
- `flutter_app/test/features/server/server_screen_test.dart`:
  - Three tabs render.
  - Status tab shows Start when daemon stopped, Stop when running.
  - Editing BindAddr triggers the restart banner; the banner mentions the desktop app only when the port also changed.
  - Events tab renders compact rows for a sequence of injected SSE events.
  - Filter chips hide non-matching rows; toggling them back on restores; search composes.
  - Clear button empties the buffer.
  - Logs tab embeds `LogsView` (presence assertion only — `LogsView` has its own existing tests via `LogsScreen`).

**Daemon (Go):**
- Pipeline tests assert `polling_started` / `polling_completed` are emitted at cycle boundaries with the expected payloads.
- Activity-recorder regression test asserts `polling_*` events do not produce activity rows.
- `/health` test asserts new `version` and `started_at` fields are present and well-formed.

## Verification

- `make verify-linux` (full Docker pipeline: daemon tests + flutter pub get + flutter test + builds).
- `cd flutter_app && flutter test && flutter analyze`.
- `cd daemon && go test ./...`.
- Manual via `make build-web`: open `/server`, exercise all three tabs; toggle daemon via AppBar AND via Status tab; edit BindAddr only and verify single-line banner; edit port and verify two-line banner; observe live events during a real poll cycle; confirm legacy `/logs` URL redirects to `/server?tab=logs`.

## Files touched (summary)

**New (Dart):**
- `flutter_app/lib/features/server/server_screen.dart`
- `flutter_app/lib/features/server/server_actions.dart`
- `flutter_app/lib/features/server/event_summary.dart`
- `flutter_app/lib/features/server/widgets/status_tab.dart`
- `flutter_app/lib/features/server/widgets/events_tab.dart`
- `flutter_app/test/features/server/server_screen_test.dart`
- `flutter_app/test/features/server/event_summary_test.dart`

**Modified (Dart):**
- `flutter_app/lib/features/dashboard/dashboard_screen.dart` — replace `/logs` AppBar icon, forward shutdown/start to shared helpers.
- `flutter_app/lib/features/logs/logs_screen.dart` — extract `LogsView` widget; `LogsScreen` becomes a thin Scaffold wrapper.
- `flutter_app/lib/shared/router.dart` — add `/server` route + redirect from `/logs`.

**Modified (Go):**
- `daemon/internal/sse/broker.go` — add `EventPollingStarted`, `EventPollingCompleted` constants.
- `daemon/internal/pipeline/pipeline.go` — emit polling events at PR-cycle boundaries.
- `daemon/internal/issues/pipeline.go` — emit polling events at issue-cycle boundaries.
- `daemon/internal/server/handlers.go` — enrich `/health` payload with `version` + `started_at`; add fields to `Server` struct and constructor.
- `daemon/cmd/heimdallm/main.go` — wire version + startedAt into `NewWithOptions`.
- `daemon/internal/activity/recorder.go` (no change) + new regression test asserting polling events are ignored.

## Open items to resolve at implementation

These are deliberately small things I'd rather verify with code in hand than guess at:

1. **Daemon version source** — confirm whether `main.go` already injects via `-ldflags` and what the variable name is. If not present, add it.
2. **Exact poll-loop sites** — the polling event emission needs to land at the right cycle boundary; the precise functions in `pipeline.go` / `issues/pipeline.go` will be identified during implementation. The design contract is "once per cycle, before any repo work" / "once per cycle, after all repos done".
3. **Daemon liveness providers** — Server screen reuses `daemonHealthProvider` (the AsyncValue<bool> at `dashboard_screen.dart:31`) and `daemonStartingProvider` (the StateProvider<bool> at `dashboard_screen.dart:32`).
