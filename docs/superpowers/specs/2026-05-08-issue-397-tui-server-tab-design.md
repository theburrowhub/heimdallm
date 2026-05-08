# Issue #397 — TUI Server tab (PR 2 of 2)

**Status:** Draft, pending user review.
**Branch:** `feat/tui-server-tab-397` (worktree at `.worktrees/tui-server-397`).
**Issue:** https://github.com/theburrowhub/heimdallm/issues/397
**Companion PR:** https://github.com/theburrowhub/heimdallm/pull/406 — GUI Server section (PR 1).

## Goal

Add a 7th **"Server"** tab to the TUI dashboard that consolidates operational status (running indicator, version, uptime, listen URL display) alongside the existing Stop control. Polish the existing live-event surface so the new `polling_started` / `polling_completed` events emitted by the daemon (PR 1) render with meaningful info instead of raw JSON.

## Why this shape

The TUI already covers most of what the GUI's "Server section" provides:

| GUI's Server tab | TUI today |
|---|---|
| **Status** (running, listen URL, version, uptime) | Scattered: status bar shows version + uptime; nothing surfaces the listen URL; no consolidated "operations" pane. |
| **Events** (live SSE feed) | The **Activity** tab is already a live SSE consumer (prepends every event to a 100-line ring buffer at `dashboard.go:283-307`). |
| **Logs** | The **Logs** tab already exists and works the same way as the GUI's. |
| **Listen URL editor** | Config tab is **read-only**. No `bubbles/textinput` or `huh` dependency in `cli/go.mod`. |
| **Restart action** | Possible to call `/shutdown`, but no `Process.start` equivalent — TUI cannot respawn the daemon. |
| **Stop action** | Already wired via `s/S` key with `y/n` confirmation (`dashboard.go:457-460`). |

So the truly missing pieces are: (a) a consolidated **Server** pane that surfaces operational state and the listen URL, and (b) human-readable rendering for `polling_*` events in Activity. Everything else either already exists or is intentionally out of scope.

## Non-goals

- **Listen URL editing in the TUI.** Read-only display only. The pane prints a one-liner: *"edit `~/.config/heimdallm/config.toml` and restart the daemon"*. Adding edit capability would mean introducing `bubbles/textinput` (or `huh`) and building edit-mode focus management — disproportionate scope for an operator tool that's typically used over SSH where editing TOML is a one-line operation.
- **Restart from the TUI.** TUI has no equivalent of the desktop app's `Process.start`; it can only stop. Surfacing a "Restart" hint for an operation the TUI cannot complete is worse UX than not mentioning it. The pane explains: *"Restarting requires running `heimdalld` again from your shell or service manager."*
- **Reorganizing existing tabs.** Activity stays as a top-level live-event surface (it already does that job well). Logs stays at top-level. The Server tab is purely additive.
- **Sub-tabs inside Server.** Single-pane layout. Easier to read, one less navigation concept to learn.
- **Tests for `renderServer` itself.** No precedent in the TUI for widget-level rendering tests; would require terminal-output capture. Manual visual verification is the convention.

## Components

### 1. New tab constant + label

**File:** `cli/internal/tui/dashboard.go` (around lines 18-27).

```go
const (
    tabActivity tab = iota
    tabPRs
    tabIssues
    tabConfig
    tabStats
    tabLogs
    tabServer  // new
)

var tabNames = []string{"Activity", "PRs", "Issues", "Config", "Stats", "Logs", "Server"}
```

### 2. Number-key shortcut

**File:** `cli/internal/tui/dashboard.go` (the existing `case "1"` ... `case "6"` block in `handleKey`, around lines 461-478).

Add at the end of that block:

```go
case "7":
    d.activeTab = tabServer
    d.cursor = 0
```

Number `7` is currently free. The existing `tab` / `shift+tab` / `h` / `l` / `right` / `left` keys cycle through tabs in order, so the new tab is reachable that way too without further changes — `len(tabNames)` already drives the modulo arithmetic at lines 378 and 381.

### 3. `renderServer(height int) string`

**File:** `cli/internal/tui/dashboard.go` (new function, placed alongside the other `render*` functions like `renderStats` at line 914).

Renders the Server pane using only state that already exists on the `Dashboard` struct: `d.config`, `d.startTime`, `d.version`, `d.connected`, `d.shutdownInFlight`, `d.shutdownMessage`. **No new state, no new API calls.**

Layout:

```
  Server
  ────────────────────────────────────────────────────────────────
  Status     ● running
  Version    v0.6.13
  Uptime     3h 22m
  Bind addr  127.0.0.1   (read-only — edit ~/.config/heimdallm/config.toml)
  Port       7842        (read-only)

  [s] Stop daemon   [r] Refresh

  Restarting requires running heimdalld again from your shell
  or service manager (TUI cannot spawn the daemon).
```

When `d.connected == false` or `d.config == nil`:
- Status indicator becomes `● stopped` in the muted color.
- Bind addr / Port show `(unavailable)` in the muted color.
- The `[s] Stop daemon` hint disappears (no daemon to stop).

While `d.shutdownInFlight == true`:
- Status indicator becomes `● stopping...` in the warning color.
- Hints disabled.

The `View()` switch (around dashboard.go:723-740, the `renderContent` function) gets a new `case tabServer: return d.renderServer(height)` arm.

### 4. `serverStatusBadge() string`

Small private helper (also new in `dashboard.go`) returning the colored status pill. Mirrors the existing `daemonStatusLabel` pattern at line 657-672 (the `renderStatus` function the status bar uses). Reuses `colorOk`, `colorWarning`, `colorMuted` from `styles.go`.

```go
func (d *Dashboard) serverStatusBadge() string {
    if d.shutdownInFlight {
        return lipgloss.NewStyle().Foreground(colorWarning).Bold(true).Render("● stopping...")
    }
    if !d.connected {
        return lipgloss.NewStyle().Foreground(colorMuted).Render("● stopped")
    }
    return lipgloss.NewStyle().Foreground(colorOk).Bold(true).Render("● running")
}
```

### 5. `formatSSEData` extensions for `polling_*` events

**File:** `cli/internal/tui/dashboard.go` (the existing function at line 1158-1190).

Today the function inspects the JSON payload for keys it recognizes (`repo`, `pr_number`, `issue_number`, `severity`) and falls through to raw JSON for unknown shapes. With the daemon's PR 1 emitting `polling_started` and `polling_completed`, the payload shape is `{kind, repos:[…]}` and `{kind, count, duration_ms}` respectively — none of the existing keys match, so the events render as raw JSON.

We change `formatSSEData` to accept the event type and add type-specific formatters:

```go
func formatSSEData(eventType, data string) (itemType string, info string) {
    var m map[string]any
    if err := json.Unmarshal([]byte(data), &m); err != nil {
        return "", data
    }

    switch eventType {
    case "polling_started":
        kind, _ := m["kind"].(string)
        repos, _ := m["repos"].([]any)
        return "", fmt.Sprintf("%s (%d repos)", kind, len(repos))
    case "polling_completed":
        kind, _ := m["kind"].(string)
        count := toInt(m["count"])
        ms := toInt(m["duration_ms"])
        return "", fmt.Sprintf("%s %d items in %dms", kind, count, ms)
    }

    // Existing logic — unchanged.
    parts := make([]string, 0)
    if repo, ok := m["repo"]; ok {
        parts = append(parts, fmt.Sprintf("%v", repo))
    }
    if num, ok := m["pr_number"]; ok {
        itemType = "pr"
        n := toInt(num)
        if n != 0 {
            parts = append(parts, fmt.Sprintf("PR #%d", n))
        }
    }
    if num, ok := m["issue_number"]; ok {
        itemType = "issue"
        n := toInt(num)
        if n != 0 {
            parts = append(parts, fmt.Sprintf("Issue #%d", n))
        }
    }
    if sev, ok := m["severity"]; ok {
        parts = append(parts, fmt.Sprintf("[%v]", sev))
    }

    if len(parts) > 0 {
        return itemType, strings.Join(parts, " ")
    }
    return itemType, data
}
```

Two call sites must be updated:
- `cli/internal/tui/dashboard.go:284` — change `formatSSEData(msg.Data)` to `formatSSEData(msg.Type, msg.Data)`.
- `cli/internal/tui/logs.go:150` — change `formatSSEData(evt.Data)` to `formatSSEData(evt.Type, evt.Data)`.

### 6. `format_sse_test.go` (new)

**File:** `cli/internal/tui/format_sse_test.go` (new).

Table-driven test covering each notable case:

```go
package tui

import "testing"

func TestFormatSSEData(t *testing.T) {
    tests := []struct {
        name      string
        eventType string
        data      string
        wantType  string
        wantInfo  string
    }{
        {
            name:      "review_completed with repo + pr_number + severity",
            eventType: "review_completed",
            data:      `{"repo":"acme/foo","pr_number":42,"severity":"high"}`,
            wantType:  "pr",
            wantInfo:  "acme/foo PR #42 [high]",
        },
        {
            name:      "polling_started renders kind + repo count",
            eventType: "polling_started",
            data:      `{"kind":"prs","repos":["acme/foo","acme/bar"]}`,
            wantType:  "",
            wantInfo:  "prs (2 repos)",
        },
        {
            name:      "polling_completed renders kind + count + duration",
            eventType: "polling_completed",
            data:      `{"kind":"issues","count":5,"duration_ms":800}`,
            wantType:  "",
            wantInfo:  "issues 5 items in 800ms",
        },
        {
            name:      "unknown event falls back to raw JSON",
            eventType: "mystery",
            data:      `{"foo":"bar"}`,
            wantType:  "",
            wantInfo:  `{"foo":"bar"}`,
        },
        {
            name:      "malformed JSON returns raw data",
            eventType: "polling_started",
            data:      "not json",
            wantType:  "",
            wantInfo:  "not json",
        },
    }
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            gotType, gotInfo := formatSSEData(tt.eventType, tt.data)
            if gotType != tt.wantType {
                t.Errorf("type: got %q want %q", gotType, tt.wantType)
            }
            if gotInfo != tt.wantInfo {
                t.Errorf("info: got %q want %q", gotInfo, tt.wantInfo)
            }
        })
    }
}
```

## Architecture

Single-file change: existing `cli/internal/tui/dashboard.go` (plus the one-line call-site update in `logs.go`). No new files in `lib/`. One new test file. **No new dependencies.** Roughly 100-150 net lines of Go.

The TUI's existing model already has every piece of state we need:
- `d.config["bind_addr"]` / `d.config["server_port"]` — fetched on tick from `/config`. Cast via existing `toInt` helper at line 1170.
- `d.version` — passed into `NewDashboard`.
- `d.startTime` — captured in `NewDashboard`.
- `d.connected` — already toggled from SSE / fetch failures.
- `d.shutdownInFlight` / `d.shutdownMessage` — already used for the `s/S` flow.

## Data flow

```
                       tickMsg (10s)
                           ↓
   d.fetchData ─────────▶ /config
                           ↓
                    d.config["bind_addr"], d.config["server_port"]
                           ↓
                    renderServer(height)  ◀─── d.startTime, d.version, d.connected
                           ↓
                    View() composes tabs + content

   SSE event ──▶ formatSSEData(type, data) ──▶ activityLine ──▶ Activity tab
   (polling_*)              (extended)
                           ↘
                            sseToLogLine(...) ──▶ Logs tab
```

## Edge cases

- **Daemon stopped, TUI still open.** `d.config` is nil (last fetch failed); `d.connected` is false. Server pane renders `Status: ● stopped`, `Bind addr: (unavailable)`, `Port: (unavailable)`. Stop hint hidden. Help text suggests starting the daemon.
- **Bind addr absent from config.** Older daemons without `bind_addr` set in TOML — `d.config["bind_addr"]` is nil; render `(default: 127.0.0.1)` in the muted color.
- **Polling event with malformed payload.** `formatSSEData` falls through to the existing default branch (raw JSON). Same behavior as today for unknown event types.
- **Tab key collisions.** `7` is currently free. `tab` / `shift+tab` / `h` / `l` already iterate via modulo on `len(tabNames)`, so they pick up the new tab automatically.
- **Config value type for port.** Daemon emits `server_port` as a JSON number → unmarshals to `float64` → cast via `toInt`. Same pattern as existing config rendering.

## Testing

- **Unit:** `cli/internal/tui/format_sse_test.go` (new) — table-driven coverage of `formatSSEData` for `review_completed`, `polling_started`, `polling_completed`, unknown event, and malformed JSON.
- **No widget test for `renderServer`.** No precedent in the TUI; would require terminal-output capture. Verification is manual + analyzer + `go test`.

## Verification

- `make verify-linux` (full Docker pipeline). The CLI is built but not exercised end-to-end inside the verify image; the unit tests cover the changed surface.
- `cd cli && go test ./...` — including the new `format_sse_test.go`.
- `cd cli && go vet ./...` and `gofmt -d cli/` — should be clean.
- **Manual:**
  - Build CLI (`make build` from worktree, then run `./cli/cmd/heimdallm-cli/heimdallm-cli` against a local daemon).
  - Press `7` → confirm Server tab renders with running indicator, version, uptime, bind addr, port.
  - Trigger a poll cycle (60s wait or kick a `/reload` if available); confirm `polling_started` / `polling_completed` appear in Activity tab with formatted info, not raw JSON.
  - Press `s` → confirm `y/n` confirmation; press `y` → confirm Server tab status indicator changes to `stopping...` then `stopped`. Verify Stop hint disappears.
  - Hide a daemon (kill the process) and reopen TUI without it running → confirm Server tab shows stopped state cleanly.

## Files touched (summary)

**Modified:**
- `cli/internal/tui/dashboard.go` — add `tabServer` constant, extend `tabNames`, add `case "7"` shortcut, add `renderServer` + `serverStatusBadge`, wire `tabServer` into the `renderContent` switch, extend `formatSSEData` to accept event type and handle `polling_*` payloads, update one call site (line 284).
- `cli/internal/tui/logs.go` — update the one `formatSSEData` call site (line 150) to pass `evt.Type`.

**New:**
- `cli/internal/tui/format_sse_test.go` — table-driven tests for the formatter.

## Open items resolved at implementation

These are deliberately small things confirmed when actually editing:

1. **Exact placement of `case "7"`** — slot it after the existing `case "6"` arm in `handleKey`. Verified in plan.
2. **Position of `tabServer` in `tabNames`** — appended last (rightmost) so existing tab muscle memory (1-6) is unchanged.
3. **Color names from `styles.go`** — `colorOk`, `colorWarning`, `colorMuted` exist (verified at exploration). `colorBadge` etc. may also be used in `serverStatusBadge` for the dot character; mirror what `renderStatus` does.

## Out of scope (explicit)

- TUI cannot spawn the daemon: no Restart action, no spawn helper, no `Process.start` equivalent.
- No editing of listen URL, prompts, or any other config from within the TUI in this PR.
- No reorganization of the existing tab list. Activity, Logs, Config remain top-level and unchanged.
- No daemon-side changes. PR 1 already shipped `polling_*` event constants and emission. This PR only consumes them.
- No new TUI dependencies (`bubbles/textinput`, `huh`, etc.) — the design avoids any feature that would require them.

## Relationship to PR 1 (#406)

This PR can be **merged before, after, or independently of PR 1**:

- The unit tests in `format_sse_test.go` exercise the formatter against literal strings (`"polling_started"`, `"polling_completed"`); they pass regardless of whether the daemon is currently emitting those events.
- The Server tab itself reads only existing daemon state (`/config`, `/health` via the shared status, uptime).
- The manual verification step that observes live `polling_*` events in the Activity tab requires PR 1 (or its equivalent daemon build) to be deployed against the running daemon. If PR 1 is not yet merged at the time this PR ships, that one verification step is moot until PR 1 lands — the rest still works.
