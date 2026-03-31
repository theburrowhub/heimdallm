# auto-pr: GitHub PR Auto-Review System — Design Spec

**Date:** 2026-03-31
**Status:** Approved

---

## 1. Overview

Desktop application that automatically reviews GitHub Pull Requests using local AI CLI tools (Claude Code, Gemini CLI, Codex). Composed of two strictly separated components: a Go daemon (core logic) and a Flutter desktop app (UI client).

**Use cases covered:**
- PRs where the user is a requested reviewer
- PRs created or assigned by the user
- All PRs across monitored repositories (tech lead mode)

---

## 2. Architecture

```
┌─────────────────────────────────────────────────────────┐
│                   Flutter Desktop App                    │
│  Dashboard │ PR Detail │ Config │ Review Trigger        │
│              HTTP + SSE client                          │
└───────────────────────┬─────────────────────────────────┘
                        │ localhost:{port} (default 7842)
┌───────────────────────▼─────────────────────────────────┐
│                  auto-pr Daemon (Go)                     │
│                                                         │
│  ┌──────────┐  ┌──────────┐  ┌──────────────────────┐  │
│  │  Poller  │  │  Worker  │  │    HTTP Server       │  │
│  │ (ticker) │→ │ Pipeline │  │  REST + SSE /events  │  │
│  └──────────┘  └────┬─────┘  └──────────────────────┘  │
│                     │                                   │
│  ┌──────────────────▼──────────────────────────────┐    │
│  │            CLI Executor                         │    │
│  │  claude │ gemini │ codex  (which + exec)        │    │
│  └──────────────────────────────────────────────────┘   │
│                                                         │
│  ┌──────────────────────────────────────────────────┐   │
│  │  SQLite  (prs, reviews, configs)                 │   │
│  └──────────────────────────────────────────────────┘   │
│                                                         │
│  Keychain / env-var → GitHub token                      │
└─────────────────────────────────────────────────────────┘
         ↕ GitHub REST API v3
```

**Communication:** REST + Server-Sent Events (SSE). The daemon exposes REST for CRUD operations and a `/events` SSE endpoint for real-time push notifications to the Flutter app.

**Service mode:** macOS LaunchAgent (`~/Library/LaunchAgents/com.auto-pr.daemon.plist`). Flutter app starts/stops the daemon if not running. Daemon continues running after app closes.

---

## 3. Daemon (Go)

### 3.1 Package Structure

```
daemon/
├── cmd/auto-pr-daemon/main.go
├── internal/
│   ├── config/        # TOML config + validation
│   ├── github/        # API client (polling, fetch diff)
│   ├── pipeline/      # Review pipeline orchestrator
│   ├── executor/      # AI CLI detection and execution
│   ├── store/         # SQLite repository layer
│   ├── server/        # HTTP REST + SSE broker
│   ├── scheduler/     # Configurable ticker (1m/5m/30m/1h)
│   └── notify/        # macOS notifications (osascript)
├── launchagent/       # .plist template
└── Makefile
```

### 3.2 Review Pipeline

```
Fetch PR metadata (GitHub API)
  → Fetch diff (GitHub API)
  → Normalize diff (truncate to ~8k tokens if oversized)
  → Build JSON prompt
  → Select CLI (per-repo config > global primary > fallback)
  → Execute CLI with 5-minute timeout
  → Parse JSON response
  → Store in SQLite
  → Emit SSE event
```

### 3.3 Configuration (`~/.config/auto-pr/config.toml`)

```toml
[server]
port = 7842                     # daemon HTTP port

[github]
poll_interval = "5m"            # 1m | 5m | 30m | 1h
repositories = ["org/repo1", "org/repo2"]

[ai]
primary = "claude"
fallback = "gemini"

[ai.repos."org/repo1"]
primary = "codex"

[retention]
max_days = 90                   # 0 = keep forever
```

### 3.4 AI Prompt Strategy

Force JSON output from every CLI:

```json
{
  "summary": "...",
  "issues": [
    { "file": "...", "line": 42, "description": "...", "severity": "high" }
  ],
  "suggestions": ["..."],
  "severity": "low|medium|high"
}
```

### 3.5 CLI Detection

```go
// Use `which <cli>` to detect availability
// Try: claude → gemini → codex in priority order
// Per-repo overrides take precedence over global config
```

### 3.6 REST API

| Method | Route | Description |
|--------|-------|-------------|
| GET | `/health` | Daemon status |
| GET | `/prs` | List PRs with latest review |
| GET | `/prs/{id}` | PR detail + review |
| POST | `/prs/{id}/review` | Force re-review |
| GET | `/config` | Current config |
| PUT | `/config` | Update config |
| GET | `/events` | SSE stream |

**SSE event types:** `pr_detected`, `review_started`, `review_completed`, `review_error`

### 3.7 Notifications

macOS native notifications via `osascript`:
- PR detected
- Review started / completed
- Errors

---

## 4. Flutter App

### 4.1 Package Structure

```
flutter_app/
├── lib/
│   ├── main.dart
│   ├── core/
│   │   ├── api/          # HTTP client + SSE listener
│   │   ├── models/       # PR, Review, Config (json_serializable)
│   │   └── daemon/       # Lifecycle (start/stop/health check)
│   ├── features/
│   │   ├── dashboard/    # PR list with severity badges
│   │   ├── pr_detail/    # Diff viewer + full review
│   │   ├── config/       # Settings UI
│   │   └── notifications/ # In-app toast notifications
│   └── shared/
│       └── widgets/      # SeverityBadge, StatusChip, etc.
├── pubspec.yaml
└── macos/
```

### 4.2 Screens

**Dashboard** — Table of PRs with columns: repo, title, author, severity (color-coded `low/medium/high`), review status, time. "Review Now" button per row.

**PR Detail** — Split panel: left shows summary + issues + suggestions; right shows diff with syntax highlighting.

**Config** — Form fields: GitHub token (written to Keychain), monitored repos, poll interval, primary/fallback AI agent, retention period.

### 4.3 State Management

**Riverpod** — reactive providers consuming the SSE stream. When a `review_completed` event arrives, the PR provider invalidates and the UI refreshes automatically.

### 4.4 Daemon Lifecycle

On app start: `GET /health`. If no response → launch daemon process with `Process.start()`. On app close: daemon keeps running (managed by LaunchAgent).

---

## 5. Security

### 5.1 GitHub Token

- **Primary:** macOS Keychain via `security` CLI (`add-generic-password` / `find-generic-password`)
- **Fallback:** `GITHUB_TOKEN` environment variable
- Token is **never** written to `config.toml` or logs
- Flutter writes to Keychain directly; daemon reads on startup

### 5.2 Log Safety

- Configurable log level (`info/debug/error`)
- Log rotation with `lumberjack`
- No sensitive data in logs (tokens, full diffs)

---

## 6. Data — SQLite Schema

```sql
CREATE TABLE prs (
  id          INTEGER PRIMARY KEY,
  github_id   INTEGER UNIQUE NOT NULL,
  repo        TEXT NOT NULL,
  number      INTEGER NOT NULL,
  title       TEXT NOT NULL,
  author      TEXT NOT NULL,
  url         TEXT NOT NULL,
  state       TEXT NOT NULL,    -- open/closed/merged
  updated_at  DATETIME NOT NULL,
  fetched_at  DATETIME NOT NULL
);

CREATE TABLE reviews (
  id          INTEGER PRIMARY KEY,
  pr_id       INTEGER NOT NULL REFERENCES prs(id),
  cli_used    TEXT NOT NULL,    -- claude/gemini/codex
  summary     TEXT NOT NULL,
  issues      TEXT NOT NULL,    -- JSON array
  suggestions TEXT NOT NULL,    -- JSON array
  severity    TEXT NOT NULL,    -- low/medium/high
  created_at  DATETIME NOT NULL
);

CREATE TABLE configs (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);
```

**Retention job:** Runs on daemon startup. `DELETE FROM reviews WHERE created_at < datetime('now', '-N days')` where N = `retention.max_days`. If `max_days = 0`, retention is disabled.

---

## 7. Testing

### Daemon (Go)
- **Unit tests:** each internal package (`executor`, `pipeline`, `github`, `store`)
- **Integration tests:** real SQLite in-memory (`?mode=memory`), GitHub API mocked with `httptest.Server`
- **CLI executor tests:** fake binaries in `testdata/bin/` returning predefined JSON
- **Target coverage:** 70% minimum

### Flutter
- **Widget tests:** Dashboard, PR Detail, Config screens
- **Unit tests:** models, API client with `MockClient`
- **Integration:** smoke test against real daemon started in test setup

---

## 8. Packaging (macOS)

```
auto-pr.app/
└── Contents/
    ├── MacOS/
    │   ├── auto-pr             # Flutter app binary
    │   └── auto-pr-daemon      # Embedded Go binary
    ├── Resources/
    └── Info.plist
```

**LaunchAgent** (`~/Library/LaunchAgents/com.auto-pr.daemon.plist`):
```xml
<key>ProgramArguments</key>
<array>
  <string>/Applications/auto-pr.app/Contents/MacOS/auto-pr-daemon</string>
</array>
<key>RunAtLoad</key><true/>
<key>KeepAlive</key><true/>
```

---

## 9. Makefile Targets

```makefile
make build-daemon      # go build ./cmd/auto-pr-daemon
make build-app         # flutter build macos
make test              # go test ./... + flutter test
make package-macos     # .app bundle + .dmg via create-dmg
make install-service   # installs LaunchAgent plist
make dev               # daemon in watch mode (air)
```

---

## 10. Out of Scope (v1)

- Linux packaging (planned for v2)
- Web UI
- Multi-user / team features
- AI model selection beyond CLI detection
