# TUI Server Tab Implementation Plan (#397, PR 2/2)

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:subagent-driven-development` (recommended) or `superpowers:executing-plans` to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a 7th "Server" tab to the TUI dashboard that consolidates operational status (running/stopped, version, uptime, listen URL display) plus a small polish so the daemon's `polling_started` / `polling_completed` events render with kind/count/duration instead of raw JSON.

**Architecture:** Single-file change to `cli/internal/tui/dashboard.go` (new tab constant, key shortcut, render dispatch arm, `renderServer` + `serverStatusBadge` helpers, extended `formatSSEData`) plus a one-line update at the existing call site in `cli/internal/tui/logs.go` and a new `cli/internal/tui/format_sse_test.go`. No new dependencies. ~100-150 net lines of Go.

**Tech Stack:** Go 1.25, Bubble Tea + Lipgloss (already in `cli/go.mod`).

**Standing rules from the user:**
- Never commit to `main`. Work on `feat/tui-server-tab-397` inside `.worktrees/tui-server-397`.
- Verification runs through the cached `heimdallm-verify` Docker image (no host Go).
- Each plan task ends in a commit; surface code for review before pushing.

---

## File Structure

### Modified
- `cli/internal/tui/dashboard.go` — add `tabServer` constant + `tabNames` entry + `case "7"` shortcut + render-dispatch arm + `renderServer` + `serverStatusBadge` + extend `formatSSEData` signature and add `polling_*` cases.
- `cli/internal/tui/logs.go` — update the one `formatSSEData` call site at line 150 to pass `evt.Type`.

### New
- `cli/internal/tui/format_sse_test.go` — table-driven tests for the formatter.

### Style names (from `cli/internal/tui/styles.go` — already defined, do NOT add)
- `colorSuccess` — green (running indicator).
- `colorWarning` — orange (stopping indicator, also currently used by `renderStatus` for both `stopping...` and `refreshing...`).
- `colorMuted` — grey (stopped / unavailable).
- The spec mentioned `colorOk`; that name does **not** exist in this codebase. Use `colorSuccess`.

---

## Task ordering rationale

TDD-first for the parser change (well-bounded, easy to test in isolation), then the new tab, then verification. Three tasks total — the work is small.

---

### Task 1: TDD `formatSSEData` extension

**Files:**
- Modify: `cli/internal/tui/dashboard.go` (the `formatSSEData` function at line 1158-1190; one call site at line 284).
- Modify: `cli/internal/tui/logs.go` (one call site at line 150).
- Create: `cli/internal/tui/format_sse_test.go`.

- [ ] **Step 1.1: Write the failing test**

Create `cli/internal/tui/format_sse_test.go`:

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
			name:      "issue_review_completed with repo + issue_number",
			eventType: "issue_review_completed",
			data:      `{"repo":"acme/foo","issue_number":7}`,
			wantType:  "issue",
			wantInfo:  "acme/foo Issue #7",
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
			name:      "polling_started with empty repos list",
			eventType: "polling_started",
			data:      `{"kind":"prs","repos":[]}`,
			wantType:  "",
			wantInfo:  "prs (0 repos)",
		},
		{
			name:      "unknown event with no recognizable fields falls back to raw",
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

- [ ] **Step 1.2: Run test to verify it fails**

Run: `docker run --rm -v "$PWD:/app" -w /app/cli heimdallm-verify go test ./internal/tui -run TestFormatSSEData -v 2>&1 | tail -10`
Expected: FAIL with a build error like `too many arguments in call to formatSSEData` (because the existing signature is `formatSSEData(data string)`, not `formatSSEData(eventType, data)`).

- [ ] **Step 1.3: Update `formatSSEData` signature and add `polling_*` cases**

In `cli/internal/tui/dashboard.go`, replace the existing function at line 1158-1190:

```go
func formatSSEData(data string) (itemType string, info string) {
	var m map[string]any
	if err := json.Unmarshal([]byte(data), &m); err != nil {
		return "", data
	}

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

with the new version:

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

- [ ] **Step 1.4: Update the two call sites**

In `cli/internal/tui/dashboard.go` line 284, change:

```go
		itemType, info := formatSSEData(msg.Data)
```

to:

```go
		itemType, info := formatSSEData(msg.Type, msg.Data)
```

In `cli/internal/tui/logs.go` line 150, change:

```go
		_, line.Target = formatSSEData(evt.Data)
```

to:

```go
		_, line.Target = formatSSEData(evt.Type, evt.Data)
```

- [ ] **Step 1.5: Run tests to verify they pass**

Run: `docker run --rm -v "$PWD:/app" -w /app/cli heimdallm-verify go test ./internal/tui -run TestFormatSSEData -v 2>&1 | tail -15`
Expected: All 7 sub-tests PASS.

- [ ] **Step 1.6: Run the full `cli` test suite to confirm no regressions**

Run: `docker run --rm -v "$PWD:/app" -w /app/cli heimdallm-verify go test ./... -count=1 -timeout=60s 2>&1 | tail -10`
Expected: all packages PASS (or "no test files" for packages without tests). No build errors.

- [ ] **Step 1.7: Commit**

```bash
git add cli/internal/tui/dashboard.go cli/internal/tui/logs.go cli/internal/tui/format_sse_test.go
git commit -m "feat(tui): render polling_* events with kind/count/duration

formatSSEData gains an eventType parameter and dedicated cases for
polling_started ('<kind> (<n> repos)') and polling_completed
('<kind> <count> items in <ms>ms'). Without this they fell through
to raw JSON in the Activity tab. Updates the two call sites
(dashboard.go and logs.go) for the new signature.

Refs #397"
```

---

### Task 2: Add the Server tab

**Files:**
- Modify: `cli/internal/tui/dashboard.go` (tab enum at lines 18-25, `tabNames` at line 27, key handler around line 461-478, `renderContent` switch at line 727-740; add new `renderServer` + `serverStatusBadge` near the other render functions).

- [ ] **Step 2.1: Add the `tabServer` constant and tab label**

In `cli/internal/tui/dashboard.go`, replace the `const ( … )` block at lines 18-25 with:

```go
const (
	tabActivity tab = iota
	tabPRs
	tabIssues
	tabConfig
	tabStats
	tabLogs
	tabServer
)
```

and replace `tabNames` at line 27 with:

```go
var tabNames = []string{"Activity", "PRs", "Issues", "Config", "Stats", "Logs", "Server"}
```

- [ ] **Step 2.2: Add the number-key shortcut**

In `cli/internal/tui/dashboard.go`, find the existing `case "1":` ... `case "6":` block in `handleKey` (lines 461-478). After the `case "6":` arm and before the closing `}`, add:

```go
		case "7":
			d.activeTab = tabServer
			d.cursor = 0
```

- [ ] **Step 2.3: Wire the new tab into the render dispatch**

In `cli/internal/tui/dashboard.go`, in `renderContent` (around line 727-740), add a new case after `case tabLogs:` and before the closing `}`:

```go
	case tabServer:
		return d.renderServer(height)
```

So the full switch becomes:

```go
	switch d.activeTab {
	case tabActivity:
		return d.renderActivity(height)
	case tabPRs:
		return d.renderPRs(height)
	case tabIssues:
		return d.renderIssues(height)
	case tabConfig:
		return d.renderConfig(height)
	case tabStats:
		return d.renderStats(height)
	case tabLogs:
		return d.renderLogs(height)
	case tabServer:
		return d.renderServer(height)
	}
```

- [ ] **Step 2.4: Implement `serverStatusBadge`**

In `cli/internal/tui/dashboard.go`, add a new helper. Place it just before `renderServer` (which is added in the next step). The function should mirror the existing `renderStatus` pattern at line 657-672:

```go
func (d *Dashboard) serverStatusBadge() string {
	if d.shutdownInFlight {
		return lipgloss.NewStyle().Foreground(colorWarning).Bold(true).Render("● stopping...")
	}
	if !d.connected {
		return lipgloss.NewStyle().Foreground(colorMuted).Render("● stopped")
	}
	return lipgloss.NewStyle().Foreground(colorSuccess).Bold(true).Render("● running")
}
```

- [ ] **Step 2.5: Implement `renderServer`**

In `cli/internal/tui/dashboard.go`, add `renderServer` immediately after `serverStatusBadge` (insert both near the other `render*` functions, e.g. just before `renderStats` at line 914, or just after `renderLogs` — anywhere alongside its peers):

```go
func (d *Dashboard) renderServer(height int) string {
	var b strings.Builder

	b.WriteString(headerStyle.Render("  Server"))
	b.WriteString("\n")
	b.WriteString("  " + strings.Repeat("─", 64))
	b.WriteString("\n")

	mutedNote := lipgloss.NewStyle().Foreground(colorMuted)

	// Status row
	b.WriteString(fmt.Sprintf("  %-10s %s\n", "Status", d.serverStatusBadge()))

	// Version row — d.version is the build-time version passed into NewDashboard
	version := d.version
	if version == "" {
		version = mutedNote.Render("(unknown)")
	}
	b.WriteString(fmt.Sprintf("  %-10s %s\n", "Version", version))

	// Uptime row
	uptime := time.Since(d.startTime).Truncate(time.Second).String()
	b.WriteString(fmt.Sprintf("  %-10s %s\n", "Uptime", uptime))

	// Bind addr / port — sourced from d.config (last successful /config fetch)
	bindAddr := "(unavailable)"
	port := "(unavailable)"
	if d.config != nil {
		if v, ok := d.config["bind_addr"].(string); ok && v != "" {
			bindAddr = v
		} else {
			bindAddr = mutedNote.Render("(default: 127.0.0.1)")
		}
		if n := toInt(d.config["server_port"]); n != 0 {
			port = fmt.Sprintf("%d", n)
		}
	}

	b.WriteString(fmt.Sprintf("  %-10s %s   %s\n", "Bind addr", bindAddr,
		mutedNote.Render("(read-only — edit ~/.config/heimdallm/config.toml)")))
	b.WriteString(fmt.Sprintf("  %-10s %s   %s\n", "Port", port,
		mutedNote.Render("(read-only)")))

	b.WriteString("\n")

	// Help line — only show Stop hint when daemon is up.
	if d.connected && !d.shutdownInFlight {
		b.WriteString("  " + helpStyle.Render("[s] Stop daemon   [r] Refresh"))
	} else if d.shutdownInFlight {
		b.WriteString("  " + helpStyle.Render("[r] Refresh"))
	} else {
		b.WriteString("  " + helpStyle.Render("[r] Refresh   (start the daemon from your shell)"))
	}
	b.WriteString("\n\n")

	b.WriteString("  ")
	b.WriteString(mutedNote.Render(
		"Restarting requires running heimdalld again from your shell"))
	b.WriteString("\n  ")
	b.WriteString(mutedNote.Render(
		"or service manager (TUI cannot spawn the daemon)."))
	b.WriteString("\n")

	return b.String()
}
```

- [ ] **Step 2.6: Build to verify wiring**

Run: `docker run --rm -v "$PWD:/app" -w /app/cli heimdallm-verify go build ./...`
Expected: builds clean.

- [ ] **Step 2.7: Run the full `cli` test suite**

Run: `docker run --rm -v "$PWD:/app" -w /app/cli heimdallm-verify go test ./... -count=1 -timeout=60s 2>&1 | tail -10`
Expected: all PASS.

- [ ] **Step 2.8: Run `go vet` and `gofmt -d` to catch any subtle issues**

Run: `docker run --rm -v "$PWD:/app" -w /app/cli heimdallm-verify go vet ./... 2>&1 | tail -5`
Expected: empty output (no vet issues).

Run: `docker run --rm -v "$PWD:/app" -w /app/cli heimdallm-verify gofmt -d ./internal/tui 2>&1 | head -20`
Expected: empty output (no formatting deltas).

- [ ] **Step 2.9: Commit**

```bash
git add cli/internal/tui/dashboard.go
git commit -m "feat(tui): add Server tab with operational pane (#397)

A 7th tab consolidating daemon status (running indicator, version,
uptime) and listen URL display (bind addr + port, read-only). Help
line surfaces the existing s/S Stop binding and r/R Refresh. Pane
explains restart must be done from the shell or service manager —
the TUI cannot spawn the daemon.

Reachable via number key 7 or via the existing tab/h/l navigation.

Refs #397"
```

---

### Task 3: Final verification

**Files:** none (verification only).

- [ ] **Step 3.1: Run `make verify-linux` from the worktree**

Run: `cd /home/vbueno/Desarrollo/workspaces/heimdallm-002/.worktrees/tui-server-397 && make verify-linux 2>&1 | tail -10`
Expected: exit 0, "✅ Linux build verification passed".

- [ ] **Step 3.2: Targeted `cli` checks against the cached image**

Run: `docker run --rm -v "$PWD:/app" -w /app/cli heimdallm-verify go test ./... -count=1 -timeout=60s 2>&1 | tail -8`
Expected: every package `ok` or `[no test files]`. The new `TestFormatSSEData` must appear and PASS.

Run: `docker run --rm -v "$PWD:/app" -w /app/cli heimdallm-verify go vet ./... 2>&1 | tail -5`
Expected: empty.

Run: `docker run --rm -v "$PWD:/app" -w /app/cli heimdallm-verify gofmt -d ./internal/tui 2>&1 | head -10`
Expected: empty.

- [ ] **Step 3.3: Manual smoke check**

Build the CLI from the worktree:

```bash
cd /home/vbueno/Desarrollo/workspaces/heimdallm-002/.worktrees/tui-server-397
docker run --rm -v "$PWD:/app" -w /app/cli heimdallm-verify go build -o /tmp/heimdallm-cli ./cmd/heimdallm-cli
```

(Or build natively if the host has Go installed.) Run it against a local daemon and walk through:

1. Press `7` → confirm Server tab is visible with Running indicator, version, uptime, bind addr `127.0.0.1`, port `7842`.
2. Confirm the help line shows `[s] Stop daemon   [r] Refresh`.
3. Press `s` → confirm `Stop daemon? y/n` confirmation appears in the status bar.
4. Press `n` → confirm cancel.
5. Press `s`, then `y` → confirm Server tab indicator transitions to `● stopping...` (warning color) then `● stopped` (muted) once the daemon goes away. Confirm the Stop hint disappears.
6. Restart the daemon manually (`./heimdalld &` in another shell) and wait ~10s for the next tick → confirm the indicator returns to `● running`.
7. Wait for a poll cycle (~60s default) → switch to **Activity** tab → confirm `polling_started` and `polling_completed` events appear with formatted info (`prs (N repos)`, `prs N items in Mms`), not raw JSON.
8. Confirm `tab` and `shift+tab` cycle through all 7 tabs in order.

If any of these fail, fix the regression in a follow-up task or commit. Otherwise no commit needed for Task 3.

- [ ] **Step 3.4: Mark plan complete**

The branch is ready for review. Do not push or open a PR until the user explicitly approves (per standing rules — wait for explicit user authorization before pushing or creating PRs).

---

## Spec coverage check

| Spec section | Covered by |
|---|---|
| Component 1: Tab constant + label | Task 2, Step 2.1 |
| Component 2: Number-key shortcut for `7` | Task 2, Step 2.2 |
| Component 3: `renderServer(height int) string` | Task 2, Step 2.5 |
| Component 4: `serverStatusBadge() string` | Task 2, Step 2.4 |
| Component 5: `formatSSEData` extension | Task 1, Step 1.3 |
| Component 6: `format_sse_test.go` (new) | Task 1, Step 1.1 |
| Edge cases: daemon stopped, malformed payload, key collisions, port type | Implicit in `renderServer` and `formatSSEData` cases; tested via Task 1 (parser) and verified manually in Task 3 |
| Out-of-scope items | Respected: no listen URL editing; no Restart action; no tab reorganization; no daemon-side changes; no new dependencies |

No spec sections are unaccounted for.

## Self-review notes

- **Type / signature consistency:** `formatSSEData(eventType, data string)` is used identically in Task 1 (impl + test + both call sites). `serverStatusBadge() string` and `renderServer(height int) string` are used as defined in Task 2.
- **Style names:** `colorSuccess`, `colorWarning`, `colorMuted` exist in `styles.go` (verified at exploration). The spec mentioned `colorOk`; this plan uses `colorSuccess` (the actual constant). Captured under "File structure" at the top.
- **No placeholders.** Each step has the actual code or command. `(default: 127.0.0.1)` and `(unavailable)` are explicit fallbacks.
- **Frequent commits.** Two implementation commits + one verification task. Each commit leaves the tree compilable.
- **Spec relationship to PR 1:** noted at the top — this PR can ship before, after, or independently. Live `polling_*` event verification (Step 3.3 part 7) requires the daemon from PR 1; the unit tests don't.
