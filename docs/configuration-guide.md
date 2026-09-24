# Heimdallm Configuration Guide

Full reference for all settings, environment variables, and deployment options.

---

## Table of Contents

1. [Overview](#1-overview)
2. [Server](#2-server)
3. [Repository Monitoring](#3-repository-monitoring)
4. [Local Directory Resolution](#4-local-directory-resolution)
5. [PR Review Pipeline](#5-pr-review-pipeline)
6. [AI Agents](#6-ai-agents)
7. [Authentication](#7-authentication)
8. [Docker Deployment](#8-docker-deployment)
9. [Retention](#9-retention)
10. [CLI](#10-cli)
11. [Distribution Formats](#11-distribution-formats)
12. [Circuit Breakers](#12-circuit-breakers)
13. [Merge Tracking](#13-merge-tracking)
14. [Polling](#14-polling)
15. [Multiple Instances](#15-multiple-instances)
16. [Full config.toml Reference](#16-full-configtoml-reference)

---

## 1. Overview

Heimdallm reads configuration from three sources, in order of precedence:

```
Environment variables  >  config.toml  >  Built-in defaults
```

**Environment variables** (`HEIMDALLM_*`) are the primary configuration mechanism for Docker deployments. Set them in `docker/.env`.

**config.toml** is an optional TOML file mounted at `/config/config.toml` inside the container. It supports richer structures (per-repo overrides, per-org PR metadata) that cannot be expressed as flat env vars. The web UI's Settings screen (`/config`, reachable from the app-shell settings action) edits this file live.

**HTTP API** — any field in `config.toml` can also be updated at runtime via `PUT /config`. Changes take effect on the next poll cycle without a container restart.

### Config sources at a glance

| What you want to configure | Recommended source |
|---|---|
| Tokens and secrets | `docker/.env` (env vars) |
| Simple daemon settings | `docker/.env` (env vars) |
| Per-repo AI overrides | `config.toml` or web UI |
| Per-org PR metadata | `config.toml` or web UI |
| Agent/prompt profiles | Web UI Prompts screen at `/prompts` (`/agents` still redirects) |

---

## 2. Server

Controls the HTTP interface the daemon listens on.

```toml
[server]
port      = 7842
bind_addr = "0.0.0.0"   # every interface; the default is 127.0.0.1
```

| TOML field | Env var | Default | Description |
|---|---|---|---|
| `port` | `HEIMDALLM_PORT` | `7842` | TCP port the daemon listens on |
| `bind_addr` | `HEIMDALLM_BIND_ADDR` | `127.0.0.1` | Interface to bind. Loopback by default, so nothing on the network can reach the daemon until this is widened — set `0.0.0.0` for every interface, or one routable address. §15.8 covers what this means for a cluster |

The daemon exposes a health endpoint at `GET /health` — returns `{"status":"ok"}` when running. Docker Compose uses this for its `healthcheck`.

---

## 3. Repository Monitoring

Heimdallm watches repositories through two complementary mechanisms that are **merged at poll time**:

```
monitored = (static_list ∪ discovered) − non_monitored
```

### Static list

List repositories explicitly in `HEIMDALLM_REPOSITORIES` or `config.toml`:

```bash
# docker/.env
HEIMDALLM_REPOSITORIES=myorg/api,myorg/backend,myorg/frontend
```

```toml
# config.toml
[github]
repositories = ["myorg/api", "myorg/backend", "myorg/frontend"]
```

Explicit repo lists in `config.toml` or `HEIMDALLM_REPOSITORIES` win over
runtime discovery state saved in SQLite. A repo explicitly listed as monitored
will not be disabled by an older `non_monitored` store row.

### Topic-based discovery

Instead of (or in addition to) a static list, tag GitHub repositories with a topic and let the daemon discover them automatically.

```bash
# docker/.env
HEIMDALLM_DISCOVERY_TOPIC=heimdallm-review
HEIMDALLM_DISCOVERY_ORGS=myorg,my-other-org   # required when topic is set
HEIMDALLM_DISCOVERY_INTERVAL=15m              # optional, defaults to 15m
```

```toml
# config.toml
[github]
discovery_topic    = "heimdallm-review"
discovery_orgs     = ["myorg", "my-other-org"]
discovery_interval = "15m"
```

Topics must follow GitHub's format: lowercase letters, digits, and hyphens, up to 50 characters. See the [GitHub topics docs](https://docs.github.com/repositories/classifying-your-repository-with-topics).

`discovery_orgs` is required when `discovery_topic` is set — it bounds the GitHub Search API scope and prevents accidentally scanning all of GitHub.

The discovery list refreshes on its own `discovery_interval` (independent of `poll_interval`) because the GitHub Search API has stricter rate limits than the REST API.

### Non-monitored blacklist

Repos in `non_monitored` are excluded from the final set even if they appear in the static list or are discovered by topic. The web UI uses this to remember repos you've explicitly disabled without losing them from the list.

```toml
[github]
non_monitored = ["myorg/archived-repo", "myorg/internal-mirror"]
```

A repo-specific `[ai.repos."owner/name"]` section configures how a repo is
processed; it does not override an explicit `non_monitored` entry. If both are
present, automatic processing stays disabled while the AI override remains
available for manual runs. The daemon logs a warning for this overlap at
startup or config reload.

Older Heimdallm versions could persist auto-discovery decisions in the same
SQLite `non_monitored` setting used by the UI. Because that legacy data does
not record whether an entry was automatic or an intentional user toggle,
Heimdallm never deletes or re-enables these entries during an upgrade. Review
the warning and re-enable the repository from the Repositories screen only
after confirming that automatic reviews are desired.

If a repository is disabled while an automatic review is already running,
the computed result is kept pending rather than discarded. Re-enabling the
repository resumes publication when the PR still has the same HEAD commit.
If the HEAD changed while disabled, the stale result is retired and the
outstanding review request can evaluate the replacement commit instead.
Retry publication is anchored to the reviewed commit SHA.

### Poll interval

```bash
HEIMDALLM_POLL_INTERVAL=5m   # any time.ParseDuration value in [1m, 24h], e.g. 3m, 10m
```

```toml
[github]
poll_interval = "5m"
```

### Repo / org rename propagation

When a repository or its parent organisation is renamed on GitHub, Heimdallm needs to flip every record keyed on the old slug — otherwise rows for the OLD slug keep accumulating in the store while new poll data lands under the NEW slug, per-repo `[ai.repos."old/name"]` overrides stop applying, and stale working dirs linger on disk.

A low-frequency probe queries GitHub for each monitored repo's canonical `full_name` and dispatches a reconciler when it differs. The reconciler runs the rename through a single SQLite transaction (`prs`, `activity_log`, `watch_state`, plus an audit row in `repo_renames`), rewrites the config TOML (including `[ai.repos."<old>"]` and `[ai.orgs."<old-org>"]` when the org changed), purges the old worktree so the next acquire clones fresh, and emits an `repo_renamed` SSE event for the dashboard.

```toml
[ai]
# Default 1h. "0" disables the probe entirely; operators can still
# trigger renames manually via POST /admin/repo-rename.
repo_rename_check_interval = "1h"
```

| Knob | Default | Purpose |
|---|---|---|
| `ai.repo_rename_check_interval` | `1h` | Probe cadence (`0` disables) |

Manual trigger for emergencies (idempotent against the probe — re-running with the same pair after the audit row is in place is a no-op):

```bash
curl -X POST http://localhost:23456/admin/repo-rename \
  -H "X-Heimdallm-Token: $HEIMDALLM_API_TOKEN" \
  -d '{"old_repo": "acme/legacy", "new_repo": "acme/modern"}'
```

**Caveat — the TOML rewrite is lossy for surface details.** Just like the existing `PATCH /config` endpoints, the rename pipeline round-trips `config.toml` through a generic decoder/encoder, which means:

- Comments anywhere in the file are dropped.
- Key order inside a table follows the encoder, not the original file.
- Blank lines between sections are not preserved.

If you maintain `config.toml` by hand and care about comments or layout, keep a separate annotated copy as your source of truth — the daemon's view of `config.toml` is its parsed structure, not its bytes on disk.

---

## 4. Local Directory Resolution

By default the AI agent reviews a PR using only the diff from the GitHub API. Giving it a local directory lets it explore surrounding code — grep sibling files, trace imports, read test coverage.

The daemon resolves a local directory for each repo using this precedence:

```
per-repo local_dir  >  local_dir_base list  >  /home/heimdallm/repos/{repo-name}  >  empty (diff-only)
```

> **Security constraint:** The daemon's executor rejects any `workdir` outside the `heimdallm` user's home directory (`/home/heimdallm`) and `/tmp`. All repo mounts **must** target a path under `/home/heimdallm/` — using `/repos` at the filesystem root will fail with `workdir … is outside the user home directory and /tmp — rejected for security`.

### `local_dir_base` — base path list

Set one or more base directories. The daemon checks `{base}/{repo-name}` in order and uses the first match.

```bash
# docker/.env
HEIMDALLM_LOCAL_DIR_BASE=/home/heimdallm/repos/ai-platform,/home/heimdallm/repos
```

```toml
# config.toml
[github]
local_dir_base = ["/home/heimdallm/repos/ai-platform", "/home/heimdallm/repos"]
```

Put more-specific paths first. For example, if `ai-api-specs` lives under a monorepo workspace and everything else lives under `/home/heimdallm/repos`:

```toml
local_dir_base = ["/home/heimdallm/repos/ai-platform-workspace/workspace", "/home/heimdallm/repos"]
```

### Per-repo `local_dir` override

Set a specific path for a single repo in `config.toml` or the web UI:

```toml
[ai.repos."myorg/api"]
local_dir = "/home/heimdallm/repos/api"
```

### Default `/home/heimdallm/repos/{repo-name}` fallback

When `HEIMDALLM_LOCAL_DIR_BASE` is set in `docker/.env`, the compose file bind-mounts your host's repos root to `/home/heimdallm/repos` inside the container (read-only). The daemon then falls back to `/home/heimdallm/repos/{short-repo-name}` for any repo that doesn't match the base list.

```bash
# docker/.env — mount your host repos root
HEIMDALLM_LOCAL_DIR_BASE=/Users/you/projects
```

The corresponding volume mount in `docker-compose.yml`:

```yaml
volumes:
  - ${HEIMDALLM_LOCAL_DIR_BASE}:/home/heimdallm/repos:ro
```

After `make down && make up`, any repo at `/Users/you/projects/api` is automatically accessible at `/home/heimdallm/repos/api` inside the container. On Docker the env var must be a single path (compose volumes take one source — commas in the value break the mount); on desktop the daemon reads the same env var and accepts a comma-separated list of paths for multi-workspace setups.

---

## 5. PR Review Pipeline

### Review mode

Controls how the AI's findings are posted back to GitHub:

| Mode | Behaviour |
|---|---|
| `single` | One consolidated review body (default) |
| `multi` | One GitHub comment per issue, plus a summary |

```bash
HEIMDALLM_REVIEW_MODE=single
```

```toml
[ai]
review_mode = "single"   # "single" or "multi"
```

Override per-repo:

```toml
[ai.repos."myorg/api"]
review_mode = "multi"
```

### Execution timeout

How long the daemon waits for an AI CLI call to complete before killing it.

```bash
HEIMDALLM_EXECUTION_TIMEOUT=30m   # optional exceptional override; default: 20m
```

```toml
[ai]
execution_timeout = "30m"
```

The per-agent override takes precedence when set (see [AI Agents](#6-ai-agents)).

---

## 6. AI Agents

### Primary and fallback

```bash
HEIMDALLM_AI_PRIMARY=claude     # claude | gemini | codex | opencode
HEIMDALLM_AI_FALLBACK=gemini    # optional
```

```toml
[ai]
primary  = "claude"
fallback = "gemini"
```

### Per-agent configuration

Fine-tune each AI CLI under `[ai.agents.<name>]`:

```toml
[ai.agents.claude]
model                  = "claude-sonnet-4-20250514"
max_turns              = 0              # 0 = not set (use CLI default)
effort                 = "high"         # low | medium | high | max
permission_mode        = "auto"         # default | auto | acceptEdits | dontAsk
bare                   = false          # --bare (disables OAuth, requires API key)
dangerously_skip_perms = false          # --dangerously-skip-permissions
no_session_persistence = false          # --no-session-persistence
execution_timeout      = "30m"          # exceptional per-agent override

[ai.agents.gemini]
model         = "gemini-2.5-pro"
approval_mode = "auto_edit"    # default | auto_edit | plan (yolo is forbidden)

[ai.agents.codex]
model         = "codex-mini"
approval_mode = "never"       # Codex --ask-for-approval value. Legacy full-auto maps to never.

[ai.agents.opencode]
model = "anthropic/claude-sonnet-4"
```

**Important:** `bare = true` disables OAuth authentication. Use it only when authenticating via `ANTHROPIC_API_KEY`, never with `CLAUDE_CODE_OAUTH_TOKEN`.

For security reasons, the HTTP API can only set `dangerously_skip_perms` to
`false` (reducing privilege). Enabling it requires a direct edit to
`config.toml`.

`extra_flags` uses a fail-closed allowlist per CLI. Only reviewed presentation,
output, resource-tuning and restrictive options are accepted; unknown flags and options
that can alter approval, sandbox, permissions, sessions, trusted directories,
files, tools, policy or external configuration are rejected. Heimdallm validates
the list while loading configuration and again immediately before creating the
subprocess. Model, effort, turn-limit, permission and approval settings must use
their typed fields; legacy model/effort/turn flags are migrated on load with a
warning. Unsafe legacy fields are ignored individually rather than preventing
startup or discarding unrelated stored settings. When execution falls back to a
different CLI, provider-specific options from the unavailable primary are not
forwarded.

### Review prompts

The `prompt` field picks the agent profile used when reviewing pull requests.
Prompt profiles are managed in the web UI Prompts screen at `/prompts`
(`/agents` remains a compatibility alias). Assign them per-repo:

```toml
[ai.repos."myorg/api"]
prompt = "security-review-profile-id"
```

### Per-repo agent assignment

Override the global AI agent for a specific repo:

```toml
[ai.repos."myorg/frontend"]
primary     = "codex"
fallback    = "claude"
review_mode = "multi"
```

---

## 7. Authentication

### GitHub token

Required. The daemon uses this token to read PRs, post reviews, and (for merge tracking) update branches, push conflict resolutions and merge.

```bash
# docker/.env
GITHUB_TOKEN=ghp_your_token_here
```

**Required scopes:**

| Scope | Why |
|---|---|
| `repo` | Read private repos, post reviews, update and merge PRs |
| `workflow` | Required when merge tracking pushes a conflict resolution or branch update that touches `.github/workflows/` files. Without this scope, pushes to workflow files are silently rejected by GitHub |
| `public_repo` | Alternative to `repo` if you only monitor public repos |

**Creating a PAT:**

1. Go to https://github.com/settings/tokens
2. Click **Generate new token (classic)**
3. Select `repo` + `workflow` scopes
4. Copy the token and paste it into `docker/.env`

If you already use the `gh` CLI, reuse its token:

```bash
echo "GITHUB_TOKEN=$(gh auth token)" >> docker/.env
```

### Claude Code

Two authentication options:

**Option A: API key (pay-as-you-go)**

```bash
ANTHROPIC_API_KEY=sk-ant-...
```

Get a key at https://console.anthropic.com/settings/keys.

**Option B: OAuth token (Max / Pro / Team subscription)**

```bash
CLAUDE_CODE_OAUTH_TOKEN=sk-ant-oat...
```

Generate the token interactively on your host (do not use `$(...)` — the command is interactive and outputs colour codes):

```bash
claude setup-token
```

Copy only the `sk-ant-oat...` line it prints and paste it into `docker/.env`.

**Do not** set `bare = true` in `config.toml` when using OAuth — `bare` disables OAuth and forces API-key mode.

### Other AI CLIs

| CLI | Env var | Where to get it |
|---|---|---|
| Gemini | `GEMINI_API_KEY` | https://aistudio.google.com/apikey |
| Codex / OpenAI | `OPENAI_API_KEY` or `CODEX_API_KEY` | https://platform.openai.com/api-keys |
| OpenCode (OpenRouter) | `OPENROUTER_API_KEY` | https://openrouter.ai/keys |

OpenCode also accepts `ANTHROPIC_API_KEY` or `OPENAI_API_KEY` depending on your configured provider.

**Reusing Gemini browser OAuth from your host:**

If you've already authenticated `gemini` on your host, uncomment the volume mount in `docker/docker-compose.yml`:

```yaml
volumes:
  - ~/.gemini:/home/heimdallm/.gemini:ro
```

Leave `GEMINI_API_KEY` empty. The container reads your host's OAuth tokens read-only.

---

## 8. Docker Deployment

### docker-compose.yml overview

The compose file defines two services:

| Service | Container name | Default port | Description |
|---|---|---|---|
| `heimdallm` | `heimdallm` | `7842` | Go daemon, AI CLIs, core engine |
| `web` | `heimdallm-web` | `3000` | Flutter Web UI served by Nginx |

The `web` service depends on the daemon's healthcheck (`/health`) before accepting traffic.

### Volume mounts

| Volume | Mount path | Description |
|---|---|---|
| `heimdallm-data` (named) | `/data` | SQLite database and API token |
| `heimdallm-config` (named) | `/config` | `config.toml` (daemon-owned, web UI edits here) |
| `$HEIMDALLM_LOCAL_DIR_BASE` | `/home/heimdallm/repos` (read-only) | Host repos root for full-repo analysis |
| SSH agent socket | `/ssh-agent` (read-only) | SSH agent for merge-tracking git operations |

The config volume is a **named volume** (not a bind mount). This is intentional — a bind mount would be owned by root on the host, which blocked the daemon from writing `config.toml`. The image chowns `/config` to the `heimdallm` user during build.

### SSH agent forwarding

Merge tracking's `resolve_conflicts` pushes branches over SSH. Forward your host's SSH agent into the container:

**macOS (Docker Desktop):**

Docker Desktop exposes the host agent at a fixed path. The compose file uses it by default:

```yaml
- ${HEIMDALLM_SSH_AUTH_SOCK:-/run/host-services/ssh-auth.sock}:/ssh-agent:ro
```

No extra configuration needed on macOS.

**Linux:**

Set `HEIMDALLM_SSH_AUTH_SOCK` to your agent socket path in `docker/.env`:

```bash
# docker/.env
HEIMDALLM_SSH_AUTH_SOCK=/run/user/1000/keyring/ssh
# or
HEIMDALLM_SSH_AUTH_SOCK=$SSH_AUTH_SOCK
```

### Day-to-day commands

```bash
make up                # start daemon + web UI (pulls latest image)
make up-build          # same, but rebuilds from local source
make up-daemon         # daemon only (no web UI)
make down              # stop containers (data volume persists)
make restart           # bounce both containers
make logs              # tail logs from all services
make logs-daemon       # daemon logs only
make ps                # show container status
make setup             # copy API token into docker/.env (for external API calls)
```

### Web UI port collision

If port `3000` or `7842` is already in use:

```bash
echo "HEIMDALLM_WEB_PORT=3100" >> docker/.env
echo "HEIMDALLM_PORT=7843"      >> docker/.env
make up
```

---

## 9. Retention

Controls how long reviewed PR records are kept in the SQLite database.

```bash
HEIMDALLM_RETENTION_DAYS=90
```

```toml
[retention]
max_days = 90
```

Review/activity records older than `max_days` are deleted. Managed auto-clones
for repos that are no longer monitored are also purged after `max_days`, but
only when their `.heimdallm-managed` marker is present. Set to `0` to disable
purging.

### Log rotation

The daemon mirrors its structured logs to `/data/heimdallm.log` for the web UI's live log view. The file is size-rotated to prevent filling the volume.

```bash
HEIMDALLM_LOG_MAX_MB=50    # max size before rotation (default: 50 MiB)
HEIMDALLM_LOG_KEEP=3       # rotated backups to keep: .log.1, .log.2, .log.3
```

Worst-case disk use: `(HEIMDALLM_LOG_KEEP + 1) × HEIMDALLM_LOG_MAX_MB`.

---

## 10. CLI

`heimdallm-cli` is a terminal client for the Heimdallm daemon. Use it to inspect status, list PRs, trigger manual reviews, and tail live events.

### Installation

**Homebrew:**

```bash
brew install theburrowhub/tap/heimdallm-cli
```

**Binary download:**

Download the appropriate archive from [GitHub Releases](https://github.com/theburrowhub/heimdallm/releases) (look for `heimdallm-cli_*`):

```bash
# macOS (Apple Silicon)
curl -L https://github.com/theburrowhub/heimdallm/releases/latest/download/heimdallm-cli_darwin_arm64.tar.gz | tar xz
mv heimdallm-cli /usr/local/bin/

# macOS (Intel)
curl -L https://github.com/theburrowhub/heimdallm/releases/latest/download/heimdallm-cli_darwin_amd64.tar.gz | tar xz
mv heimdallm-cli /usr/local/bin/

# Linux (amd64)
curl -L https://github.com/theburrowhub/heimdallm/releases/latest/download/heimdallm-cli_linux_amd64.tar.gz | tar xz
mv heimdallm-cli /usr/local/bin/
```

### Connection

All commands accept `--host` and `--token` flags, or their environment variable equivalents:

| Flag | Env var | Default |
|---|---|---|
| `--host` | `HEIMDALLM_HOST` | `http://localhost:7842` |
| `--token` | `HEIMDALLM_TOKEN` | _(empty — read-only commands work without a token)_ |

```bash
export HEIMDALLM_HOST=http://myserver:7842
export HEIMDALLM_TOKEN=your-api-token
```

Get the API token after `make up`:

```bash
make setup   # prints the token and copies it into docker/.env
# or:
docker exec heimdallm cat /data/api_token
```

### Local development

The CLI is a separate Go module under `cli/`. It uses Cobra for commands and
Bubble Tea + Lipgloss for the dashboard, and it talks to the daemon through
`cli/internal/api/client.go`.

From the repository root:

```bash
make build-cli    # builds cli/bin/heimdallm-cli
make test-cli     # host-safe CLI tests
make lint-cli     # go vet for the CLI module
make dev-daemon   # run the daemon at http://localhost:7842
make dev-cli      # run heimdallm-cli dashboard
```

Set `HEIMDALLM_HOST` and `HEIMDALLM_TOKEN` when testing against a non-local
daemon:

```bash
HEIMDALLM_HOST=https://heimdallm.example.com HEIMDALLM_TOKEN=... make dev-cli
```

### Commands

| Command | Description |
|---|---|
| `heimdallm-cli status` | Daemon state, uptime, monitored repos, stats summary |
| `heimdallm-cli prs` | List reviewed PRs (filter with `--severity info\|low\|medium\|high`) |
| `heimdallm-cli review-pr <id>` | Trigger a manual review for a PR by its internal ID |
| `heimdallm-cli follow` | Stream real-time SSE events (like `tail -f`; add `--json` for raw JSON) |
| `heimdallm-cli config` | Print the daemon's running configuration as JSON |
| `heimdallm-cli stats` | Review statistics: totals, by severity, by CLI, top repos, timing |
| `heimdallm-cli dashboard` | Live terminal dashboard |

### TUI dashboard keybindings

The dashboard tabs are Activity, PRs, Merges, Config, Stats, Server, and Instances.

| Key | Action |
|---|---|
| `tab`, `l`, `right` | Move to the next tab |
| `h`, `left` | Move to the previous tab |
| `1`-`7` | Jump directly to a tab |
| `r` | Refresh data |
| `s` | Stop the daemon (opens a confirmation prompt) |
| `q`, `ctrl+c` | Quit |
| `j`, `down` | Move or scroll down |
| `k`, `up` | Move or scroll up |
| `pgdn`, `pgup` | Page through long lists or detail views |
| `g` | Jump to the top of the current list |
| `G` | Follow the live Logs tab |
| `enter` | Open PR details |
| `esc` | Close an open detail view |
| `y` / `n` | Confirm or cancel daemon shutdown |

---

## 11. Distribution Formats

Heimdallm ships as several artifact types, each built by the tooling best
suited for it:

| Format | Platform | Built with |
|---|---|---|
| Docker image (GHCR) | Linux | GoReleaser |
| `.deb` / `.rpm` | Linux | GoReleaser nfpms |
| `.AppImage` | Linux | appimagetool |
| CLI binaries + Homebrew | Linux, macOS, Windows | GoReleaser |
| `.dmg` | macOS | create-dmg |

### macOS DMG

The macOS `.dmg` is built in its own CI job (`build-macos`) on a `macos-14`
runner, separate from GoReleaser. GoReleaser cannot handle this artifact
because the build requires:

- A **macOS runner** (GoReleaser runs on Linux)
- A **Flutter build** targeting macOS (not a standalone Go binary)
- **Ad-hoc code signing** with Apple entitlements
- **`create-dmg`** for the installer image with a custom window layout

The DMG is published to the GitHub release on every tagged version alongside
all other artifacts.

See [docs/release-pipeline.md](release-pipeline.md) for the full pipeline
architecture.

---

## 12. Circuit Breakers

Circuit breakers cap completed PR reviews to prevent cost-runaway loops. Failed PR-review executions do not consume review quota because they did not produce a review. Instead, automatic retries on the same PR HEAD use a persistent exponential cooldown: 5 minutes after the first incomplete execution, doubling to a maximum of 6 hours. Failed or still-running executions are also limited to 20 starts per repository in a rolling hour by default, independently from completed reviews; reaching that limit defers automatic work without emitting a circuit-breaker trip. A manual **Re-review** bypasses both retry waits, but another failure still extends and consumes the protections for the next automatic retry. The defaults are deliberately conservative — high-volume workflows must raise caps explicitly. There is currently no way to express "unlimited" through TOML; set a large value (e.g. `99999`) if you need near-unbounded behaviour.

```toml
[circuit_breaker]
per_pr_24h       = 3    # max reviews on the same PR HEAD SHA in any 24 h window
per_repo_hr      = 20   # max PR reviews on the same repo in any 1 h window
per_review_failure_repo_hr = 20 # max failed/in-flight review executions per repo in any 1 h window
```

| Field | Default | Description |
|---|---|---|
| `per_pr_24h` | `3` | Reviews on the same PR HEAD SHA over a 24 h window. A new commit gets its own allowance. |
| `per_repo_hr` | `20` | PR reviews across the same repo over a 1 h window. |
| `per_review_failure_repo_hr` | `20` | Failed or still-running PR-review executions across the same repo over a 1 h window. This retry-only limit never emits `circuit_breaker_tripped`. |

All zero values are treated as "unset" and substituted with the defaults above. There is no separate env-var mapping for circuit breaker fields — set them in `config.toml`.

### Per-org and per-repo circuit breaker overrides

All three fields are resolvable per org and per repo via `[ai.orgs."org".circuit_breaker]` and `[ai.repos."org/repo".circuit_breaker]`, following the same `repo > org > global` precedence as all other `[ai.*]` overrides. Only fields present in the override section are applied; absent fields inherit from the next level.

```toml
# Tighten the per-PR cap for a high-activity repo
[ai.repos."my-org/my-repo".circuit_breaker]
per_pr_24h = 2

# Loosen the per-repo PR cap for an org with frequent pushes
[ai.orgs."my-org".circuit_breaker]
per_repo_hr = 40
```

---

## 13. Merge Tracking

Merge tracking watches the open pull requests **you** authored or are assigned to, works out exactly what is stopping each one from merging, and — at whatever level of automation you configure — moves them along.

> **Every automation is off by default.** A config that does not mention `[merge_tracking]` never touches a repository. Turning `enabled` on gives you the reporting — the four automations are separate switches on top of that.

### What it reports

For every tracked PR, Heimdallm records an explainable decision and shows it in the Merge screen in the app shell, on the PR detail view, in `heimdallm-cli merges`, and in the TUI:

- whether the PR is ready to merge, and if not, **why** — named specifically, not as a code;
- the full list of CI checks with state, whether each one gates the merge, the app that ran it and a link to its log;
- required checks that branch protection demands but which **never reported** — invisible in GitHub's own UI, and a common cause of a PR that seems stuck for no reason.

A PR blocked by CI is called out prominently: a coloured band on its row naming the failing check, a count badge on the tab, and the affected rows sorted to the top.

### The four automations

| Setting | What Heimdallm does |
|---|---|
| `enable_auto_merge` | Turns on **GitHub's own** auto-merge, so GitHub merges the PR the moment every requirement is satisfied. Pinned to the commit that was evaluated. |
| `update_branch` | Brings a PR up to date when it falls behind its base. When enabled, Heimdallm detects that state independently of GitHub's aggregate merge verdict and updates the branch before other automation. It uses `PUT /pulls/{n}/update-branch`; if the base requires linear history GitHub refuses, Heimdallm falls back to a local rebase and a lease-protected force-push. |
| `resolve_conflicts` | Has the configured agent resolve merge conflicts in an ephemeral worktree, then force-pushes. **See the warning below.** |
| `merge` | Merges the PR itself once every requirement is met. |

### Configuration reference

```toml
[merge_tracking]
enabled            = false   # master switch — false = merge tracking entirely off
enable_auto_merge  = false   # arm GitHub's native auto-merge
update_branch      = false   # bring out-of-date branches up to date
resolve_conflicts  = false   # let the agent resolve conflicts (force-pushes!)
merge              = false   # merge when every requirement is met
merge_method       = "squash"  # squash | merge | rebase
include_assigned   = false   # also track PRs assigned to you but authored by someone else
require_approval   = false   # demand an approval even where the repo does not
poll_interval      = ""      # empty inherits [polling]/[github].poll_interval
max_prs_per_tick   = 20      # bounds the API spend of one cycle
max_update_attempts  = 3     # per observed head; a successful update resets it
max_resolve_attempts = 2     # per PR, per head commit
max_merge_attempts   = 3     # per PR, per head commit
action_cooldown    = "10m"   # between write actions on one PR
resolve_timeout    = "30m"   # wall clock for one conflict-resolution agent run
resolve_effort     = "high"  # low | medium | high | max
```

| Field | Default | Description |
|---|---|---|
| `enabled` | `false` | Master switch and kill-switch. When `false` a poll cycle makes **zero** GitHub calls. |
| `enable_auto_merge` | `false` | Arm GitHub's native auto-merge on PRs that do not have it. |
| `update_branch` | `false` | Update a branch that has fallen behind its base. When disabled, an independently detected stale base does not by itself stop `enable_auto_merge` or `merge`; GitHub can still block them when branch protection requires an up-to-date branch. |
| `resolve_conflicts` | `false` | Run the configured agent on merge conflicts. **This force-pushes to your branch.** |
| `merge` | `false` | Merge directly once every requirement is met. |
| `merge_method` | `"squash"` | `squash`, `merge` or `rebase`. Must also be enabled on the repository; if it is not, the PR is reported as blocked rather than failing on every attempt. Validated at boot — an invalid value stops the daemon rather than producing a 422 from GitHub once per cycle forever. |
| `include_assigned` | `false` | Track PRs you did not author but are assigned to. Being an assignee does not imply authorship, so this is off by default. |
| `require_approval` | `false` | Refuse to merge without an approving review at the current commit, even where the repository requires none. Useful on personal repos with no branch protection. |
| `poll_interval` | inherit | Cadence of the reconciler. Empty inherits `[polling].poll_interval`, then `[github].poll_interval`. Re-read every cycle, so a change takes effect without a restart. |
| `max_prs_per_tick` | `20` | How many PRs one cycle evaluates. Each costs one GraphQL query, so this is the knob that bounds API spend. |
| `max_update_attempts` | `3` | Branch-update attempts for the currently observed head commit. Any successful update creates a new head and resets this counter, so it caps repeated failures for one head rather than successful updates over the PR's lifetime. |
| `max_resolve_attempts` | `2` | Conflict-resolution attempts per head commit. Reset by a push. |
| `max_merge_attempts` | `3` | Merge attempts per head commit. Reset by a push. |
| `action_cooldown` | `"10m"` | Minimum gap between write actions on one PR. |
| `resolve_timeout` | `"30m"` | Wall clock for one conflict-resolution agent run. |
| `resolve_effort` | `"high"` | Agent effort for conflict resolution: `low`, `medium`, `high`, `max`. |

### Per-org and per-repo overrides

Every field except `poll_interval` and `max_prs_per_tick` — which bound the single reconciler loop, not a repo — supports the usual `repo > org > global` precedence.

```toml
[merge_tracking]
enabled = true          # report everywhere
merge   = false         # but merge nowhere

# Update branches across the org, still no merging
[merge_tracking.orgs."my-org"]
update_branch = true

# One repo we trust enough to merge on its own
[merge_tracking.repos."my-org/my-repo"]
merge        = true
merge_method = "rebase"
```

These can also be set over HTTP: `PATCH /config/merge_tracking/repos/{repo}` and `PATCH /config/merge_tracking/orgs/{org}`.

### ⚠️ About `resolve_conflicts`

This is the highest-blast-radius setting in Heimdallm: an AI agent rewrites your branch and force-pushes it.

To do that the agent has to edit files without stopping to ask, so the conflict-resolution run is **put into write mode whenever you left the CLI's permission setting empty**: Claude gets `permission_mode = "acceptEdits"`, Codex `approval_mode = "never"`, Gemini `approval_mode = "auto_edit"`. A value you set in `[ai.agents.<cli>]` is kept as is. PR reviews are unaffected — only this run is promoted.

What bounds it:

- the agent works in an **ephemeral, Heimdallm-managed worktree** — never your own checkout;
- the push uses `--force-with-lease` pinned to the exact commit the branch was at when Heimdallm started, so a push that landed meanwhile makes git refuse rather than overwrite it;
- the attempt is **discarded without pushing** if the agent leaves unmerged paths, leaves conflict markers in the file contents, or touches any file that was not in conflict;
- the PR gets a comment naming the commit the branch was at beforehand, so a bad resolution is one `git reset --hard` away from undone;
- a PR whose head lives in someone else's fork is never written to at all.

What it cannot do is judge whether the resolution is *correct*. Review the result. Pair it with `merge = false` until you trust it on a given repo.

### Worked example: reporting only

The safest useful setup. Heimdallm tells you why each of your PRs is stuck and never touches anything.

```toml
[merge_tracking]
enabled          = true
include_assigned = true
```

### Worked example: hands-off on one repo

```toml
[merge_tracking]
enabled           = true
enable_auto_merge = true   # let GitHub merge when it can, everywhere

[merge_tracking.repos."my-org/my-repo"]
update_branch     = true
resolve_conflicts = true
merge             = true
require_approval  = true   # never merge this one without a human approval
```

### What blocks a merge

The Merge screen in the app shell and `heimdallm-cli merges` report one of these reasons. They are stable identifiers, and the UI renders each as a sentence.

| Reason | Meaning | What to do |
|---|---|---|
| `checks_failing` | A required check is failing | Fix the check. The detail names it. |
| `checks_pending` | Required checks are still running | Nothing — it merges on its own when they pass. |
| `required_check_missing` | Branch protection requires a check that never reported | Usually a workflow that did not trigger. |
| `checks_unknown` | More checks than Heimdallm reads in one pass | Nothing automatic will happen; merge by hand. |
| `changes_requested` | A reviewer requested changes | Address them. Heimdallm blocks on a standing change request even where GitHub would not. |
| `review_required` / `insufficient_approvals` | Not enough approvals at the current commit | Get a review. A push invalidates earlier approvals. |
| `pending_reviewers` | A requested reviewer has not responded | Wait, or unrequest them. |
| `unresolved_threads` | An open review conversation | Resolve it. Heimdallm blocks on these even without `requiresConversationResolution`. |
| `conflicts` | Conflicts with the base branch | Enable `resolve_conflicts`, or fix it yourself. |
| `behind_base` | GitHub reports that a protected branch must be updated, or `update_branch` is enabled and Heimdallm independently detects that the head is behind the base. The independent check matters because GitHub can report `BLOCKED` or `DIRTY` instead of `BEHIND`. | Wait for the configured update, enable `update_branch`, or update the branch yourself. |
| `draft` | The PR is a draft | Heimdallm never acts on drafts. Mark it ready. |
| `blocked_by_protection` | GitHub says blocked and nothing above explains it | Usually CODEOWNERS or a rule the token cannot read. |
| `in_merge_queue` / `merge_queue_configured` | The base branch uses a merge queue | Nothing — GitHub owns the merge. Heimdallm never merges directly past a queue. |
| `cross_fork` | The head branch is in another fork | Reported only; Heimdallm cannot push there. |
| `insufficient_permission` | No write access | Nothing Heimdallm can do. |
| `merge_method_not_allowed` | `merge_method` is disabled on the repo | Change `merge_method`, or enable it on GitHub. |
| `auto_merge_unavailable` | The repo has auto-merge switched off, or GitHub refuses to queue it on a PR it would merge right now | Enable auto-merge on the repo, or set `merge = true` so Heimdallm merges it directly. |
| `mergeability_unknown` | GitHub has not finished computing | Transient; re-checked shortly. Never treated as mergeable. |
| `head_sha_moved` | A commit landed mid-action | Transient; re-evaluated next cycle. |
| `attempt_cap_reached` | The per-commit attempt cap was hit | A new push resets it. |

### Adding your own PR

The **Merge** screen in the sidebar/rail/drawer has its own **Track a PR** button. Use that one, not the Add PR
action in Activity: that action routes through the review pipeline, which
refuses any pull request the authenticated account authored — and Heimdallm
authenticates as *you*, so that is every PR you open. Pasting your own PR there
records a `self_authored` skip and nothing else.

`POST /merge-tracking/add` stores the PR, verifies the repository's effective
merge-tracking configuration by enrolling it, adds the repository to the
monitored list and stops. A disabled repository is rejected before that list is
changed. No review is triggered. Whether the PR is really yours is settled by
the next evaluation, against GitHub's own view of author and assignees.

### Per-repository and per-organisation overrides

Every field except `poll_interval` and `max_prs_per_tick` supports
`repo > org > global` precedence. The **Merge Tracking** cards on organization
and repository detail pages expose every scoped field, show where each value is
inherited from, and let each override be reset independently. The repo list
shows a green LED whose tooltip names where the master `enabled` value came
from.

`include_assigned` is the one field whose *widening* direction costs something:
discovery runs one search for every repository at once, so Heimdallm asks GitHub
for assigned PRs whenever **any** enabled repository wants them, and drops the
ones each repository does not.

### Token permissions

Merge tracking reads `baseRef.branchProtectionRule`, which needs **admin** on the repository. Without it GitHub answers with the data plus a `FORBIDDEN` error on that one field; Heimdallm tolerates that, marks the protection as unreadable, and falls back to GitHub's own `mergeStateStatus` and `reviewDecision`, which are computed with the rules applied. Nothing breaks — it just cannot show the underlying rule.

Everything else needs the same `repo` scope the rest of Heimdallm uses.

---

## 14. Polling

The `[polling]` table tunes how the daemon schedules its fetch cycles. All fields are optional — omitting the section entirely reproduces the prior behaviour with no change in how the daemon polls.

```toml
[polling]
poll_interval              = "5m"   # inherits [github].poll_interval when unset
discovery_interval         = "5m"
tier3_interval             = "30s"
rate_limit_safety_threshold = 100
use_etag                   = true
```

| Field | Default | Description |
|---|---|---|
| `poll_interval` | inherits `[github].poll_interval` | Base poll cadence. When unset, the value from `[github].poll_interval` (or its env var `HEIMDALLM_POLL_INTERVAL`) is used. Setting `[polling].poll_interval` overrides the `[github]` field for the polling subsystem. |
| `discovery_interval` | `"5m"` | How often the topic-discovery pass runs to find newly-tagged repos. Independent of `poll_interval`. |
| `tier3_interval` | `"30s"` | Cadence of the Tier 3 state-check loop that re-checks watched PRs (open/closed/merged transitions). |
| `rate_limit_safety_threshold` | `100` | Core-remaining floor. When the GitHub core rate-limit remaining count drops below this number, non-critical polling (discovery, Tier 3 state checks) is throttled until the rate-limit window resets. The critical path (PR review) is not blocked by this threshold. |
| `use_etag` | `true` | Send `If-None-Match` / `ETag` conditional-request headers on list endpoints. A `304 Not Modified` response reuses the cached body without counting against the rate limit. Disable only if your GitHub proxy strips ETag headers. |

> **Reload behaviour:** Every field takes effect on the next `PUT /config` or file reload, without a restart. `use_etag` and `rate_limit_safety_threshold` are re-applied to the live GitHub client and rate limiter; `tier3_interval` resets its ticker in place (the new value is picked up on the next tick, so shortening a long interval takes effect after at most one more tick at the old cadence).

> **Validation:** All three durations are validated at load and on reload. `poll_interval` and `discovery_interval` must be between `1m` and `24h` — the same floor `[github].poll_interval` enforces, since `[polling].poll_interval` takes precedence over it and would otherwise be a way around the quota guard. `tier3_interval` accepts `1s`–`1h` (it drives a local scan, not GitHub traffic). `rate_limit_safety_threshold` must not be negative. An invalid value fails the reload with an error rather than silently falling back to the default.

> **Unconfigured = no change:** A missing `[polling]` section is equivalent to setting every field to its default. There is no opt-in required — existing deployments that do not add this section continue to behave exactly as before.

---

## 15. Multiple Instances

An **instance** is a Heimdallm daemon running on another machine or in another
container. One instance acts as the **hub**: it holds the registry of the
others, decides which instance owns which repositories, pushes shared
configuration to the rest, and serves the single web UI.

Everything in this section is opt-in. A `config.toml` with no `[cluster]`
section behaves exactly as a single-daemon install always has — the routing
layer reports that this daemon owns every repository, and the control-plane
endpoints are not mounted at all.

### 15.1 How the work is divided

Each daemon polls GitHub for itself, but only **acts** on the repositories
routed to it. Discovery stays global on purpose: every instance still learns
about every repository, so the UI shows the whole estate, and only reviewing
and merging is narrowed. That partition — not a distributed lock —
is what stops two daemons reviewing the same pull request.

Ownership resolves in this order:

```
repository rule  >  organization rule  >  default_instance
```

A `worker` enforces this locally with its own copy of the rules, which the hub
**pushes** to it (`PUT /cluster/partition`) after every reload — an operator
never edits a worker's `config.toml` by hand. Until a worker has received
that push at least once, it holds no rules to enforce and — unlike a `hub` or
`standalone` daemon, which default to owning everything when nothing is
configured — a `worker` defaults to owning **nothing**: it waits rather than
guessing. This asymmetry exists because a worker has no registry of its own
to fall back on; the hub is the only authority on the partition, and treating
"no rules yet" as "mine by default" is precisely how a worker with a stale or
missing push ends up reviewing repositories that were never routed to it
(theburrowhub/heimdallm#769). A worker sitting idle logs a WARN naming `PUT
/cluster/partition` — that means exactly this: it has not received its
partition yet. Trigger a reload on the hub (or "apply to all instances") to
push it; see §15.4 for when that happens automatically and its one gap
(a worker that was unreachable during the last reload).

### 15.2 Configuration

```toml
[cluster]
role             = "hub"          # standalone (default) | hub | worker
instance_id      = ""             # generated on first boot, kept in <data dir>/instance_id
instance_name    = "main"         # defaults to the hostname
default_instance = "hub-1"        # owns everything no rule claims
probe_interval   = "30s"          # how often the hub health-checks the others
discovery        = "off"          # off (default) | mdns; see 15.8

# Instances are a TOML map keyed by id, not an array of tables: the config
# schema validator rejects arrays of tables, and a map makes id uniqueness a
# property of the format rather than something validation has to enforce.
[cluster.instances.hub-1]
name     = "Local hub"
base_url = "http://127.0.0.1:7842"
# Exactly one token source. token_env and token_file keep the secret out of a
# file the UI rewrites.
token_file = "~/.local/share/heimdallm/api_token"
labels     = ["macos", "local"]

[cluster.instances.srv-a]
name      = "srv-a"
base_url  = "http://srv-a.local:7842"   # a hostname, not a pinned IP - see below
token_env = "HEIMDALLM_SRV_A_TOKEN"
enabled   = true                  # omit or true to participate

takeover_after_failed_probes = 3   # consecutive failed probes before the hub
                                   # does an unreachable owner's work itself

[cluster.routing]
mode             = "assignment"   # assignment (default) | dispatch
round_robin_pool = ["hub-1", "srv-a"]   # empty = every enabled instance
round_robin_ops  = ["review", "merge"]

[cluster.routing.orgs]
theburrowhub = "srv-a"

[cluster.routing.repos]
"theburrowhub/heimdallm" = "hub-1"
```

A `worker`'s `config.toml` needs only `role = "worker"` — its `instance_id`,
`default_instance` and `[cluster.routing.orgs]`/`[cluster.routing.repos]` are
filled in by the hub's push, not hand-written. Registering a worker
(`[cluster.instances.<id>]` on the hub, `base_url` and a token) is what
identifies it to the hub; the worker itself never carries `[cluster.instances]`
or `[cluster.routing]` written by an operator.

Environment equivalents, for containers that should not carry a per-instance
`config.toml`: `HEIMDALLM_CLUSTER_ROLE`, `HEIMDALLM_INSTANCE_ID`,
`HEIMDALLM_INSTANCE_NAME`, `HEIMDALLM_CLUSTER_DEFAULT_INSTANCE`,
`HEIMDALLM_CLUSTER_PROBE_INTERVAL`, `HEIMDALLM_CLUSTER_DISCOVERY`,
`HEIMDALLM_CLUSTER_TAKEOVER_AFTER_FAILED_PROBES`.

**`base_url` takes a hostname, and it usually should.** Validation only requires
an absolute `http`/`https` URL with a host and no credentials, query or
fragment — so an mDNS `.local` name, a DNS record or a Tailscale name all work,
and what the hub stores is the name rather than an address.

Prefer one of those, a static address or a DHCP reservation over a literal IP
from a lease. Nothing ever rewrites a stored `base_url`: it changes only when
someone edits it. So when a laptop or any DHCP host picks up a new address, an
entry holding a literal IP goes stale and that instance becomes permanently
unreachable from the hub while it carries on working perfectly — which is the
situation §15.3.1 exists to contain, and containing it is not the same as
avoiding it. Section 15.8 covers discovering instances by name instead.

**A name is re-resolved when the connection to it breaks, not on every probe.**
The hub's HTTP client keeps idle connections keyed by host and port as written,
so while the address behind a name keeps answering, that connection is reused
and the name is never looked up again. With the default 30s `probe_interval`
the connection never sits idle long enough to be recycled either.

That is the right behaviour for the case this is here for — a host that changes
address stops answering on the old one, the connection fails, the name resolves
again — but it sets two expectations worth having:

- **Recovery costs one failed probe.** Expect roughly one `probe_interval`
  between the address changing and the instance going green again, not an
  instant switch. The hub logs `instance became unreachable` and then
  `instance recovered`; a single pair of those around an address change is the
  system working, not a fault.
- **An old address that still answers goes on being used.** If a machine keeps
  its previous address alongside the new one, the hub has no reason to look the
  name up again and will stay on the old one indefinitely. Harmless, but it is
  why a half-finished address change can look like nothing happened.

The same point applies to testing this: pointing a name at a different address
proves nothing unless the previous address actually stops answering. A DHCP
lease that is renewed rather than released, or an address added without
removing the old one, leaves the original reachable and the hub will never
notice the change.

### 15.3 Routing modes

| Mode | What it does |
|---|---|
| `assignment` (default) | Repositories are partitioned; each daemon polls, reviews and merges only what it owns. Round robin is used to hand out repositories that have no rule yet. |
| `dispatch` | Everything `assignment` does, plus: each operation triggered by hand rotates across the pool, regardless of which instance owns the repository. |

**`dispatch` is not a distributed lock.** What keeps two daemons off the same
repository is the ownership partition; what stops the hub sending the same work
twice is a dispatch ledger keyed on `(operation, target, head SHA)`. Each
daemon's own `reviews_in_flight` claims remain per-daemon. If you need two
instances acting on the same repository concurrently, this feature does not
provide that guarantee.

### 15.3.1 When an instance is unreachable but still working

The hub decides ownership from its routing rules, and reachability from its
health probe. Those are separate facts, and a network partition is where they
come apart: an instance whose address changed, whose VPN dropped, or that a
firewall rule now blocks is unreachable *from the hub* while still reaching
GitHub and reviewing every repository it owns.

Two rules keep that from producing two reviews on one pull request:

| Rule | What it does |
|---|---|
| `takeover_after_failed_probes` (default 3) | The hub leaves an unreachable owner's work alone until that owner has failed this many consecutive probes. Below the threshold it defers — the owner's own poll loop covers its repositories, so nothing is lost. Raise it to defer for longer; a very large value means "never take over, alert me instead". |
| The same threshold on rejected dispatches | The health probe is unauthenticated; the calls that hand work over are not. An instance that answers `/health` while rejecting every dispatch — cluster token rotated on the remote, the repository missing from that remote's own config, a permission error — would otherwise stay "healthy" forever and its work would be done by nobody. Consecutive rejections of the same operation on the same repository are counted separately and escalate on the same threshold, with a `dispatch_rejected` takeover that points at the token rather than at `base_url`. A daemon that is not a hub has no probe history at all, so for it a rejected dispatch always means "handle it here". |
| The publish-boundary check | Immediately before submitting a review, a daemon asks GitHub whether a review carrying the Heimdallm footer, **published under the daemon's own GitHub login**, is already anchored to the same commit. If one is, it does not publish a second, and records the existing review against its own local row. This is the only check that survives a partition, because GitHub is the one place both instances can still reach. The login scope is what separates a peer from a colleague: two daemons sharing one account are one reviewer, and the second review is a duplicate; two operators each running a daemon as themselves are two reviewers GitHub asked for separately, and each publishes its own. A daemon that could not resolve its own login publishes rather than guess. **Consequence for clusters:** this check deduplicates a takeover only when the hub and the taken-over instance authenticate as the same GitHub account. With one account per instance (§15.4), `takeover_after_failed_probes` is the only defence against a second review. |

When the hub does take over, it logs a `WARN` naming the repository and the
instance, and emits an `instance_takeover` SSE event, once per repository per
outage. Treat that event as *"this repository may be being reviewed twice"*,
not merely as *"that instance is down"* — the underlying cause is very often a
stale `base_url`.

What is still not guaranteed, in two parts.

The publish-boundary check is check-then-act, not an atomic claim: GitHub
offers no compare-and-swap on reviews, and two partitioned instances share no
other store. Two daemons that cross that boundary within the same API round
trip of each other both see no peer review and both submit, and two reviews are
published. What the check buys is the size of that window — one request,
instead of the several minutes a review takes — not its elimination.

Separately, two daemons that start reviewing the same commit each spend their
own AI budget before either reaches the boundary. That cost is bounded to once
per commit: the losing daemon's local row is retired against the review that
already exists, recording the reviewed SHA, so the next poll cycle skips the
commit instead of re-reviewing it.

The ownership partition, not this check, is what keeps either from happening
routinely. If neither is acceptable for your estate, set
`takeover_after_failed_probes` high enough that takeover never happens on its
own and treat `instance_takeover` as a page.

### 15.3.2 How a dispatched review identifies its PR

A PR's local row ID (`prs.id` in each daemon's own SQLite database) is an
autoincrement counter private to that daemon — it has no meaning on any other
instance, and two independent daemons routinely assign the *same* id to two
completely different pull requests. When the hub hands a review to the
instance a repository is routed to, it therefore never sends that id: it sends
the PR's cluster-stable identity instead — its GitHub PR id (`github_id`),
falling back to `repo` + `number` — over `POST /cluster/prs/review`. The
receiving instance resolves (or, if it has never seen the PR before, adopts
it exactly as `POST /prs/add` does) that identity against its *own* store and
triggers the review using the row ID it finds there.

This closes a failure mode where a dispatch sent an id meaningful only to the
hub: the receiving instance either found no such row (surfacing as a spurious
"Review Failed — PR not found" notification, with the reviewer never actually
running) or, worse, found a row that id happened to match locally and
reviewed the wrong pull request. `POST /prs/{id}/review` still exists and
still takes a local row ID — it is what the GUI and CLI use against an
instance whose id space they already queried — but is never used for
cross-instance dispatch.

### 15.4 What propagates, and what does not

"Apply to all instances" pushes the settings every instance should agree on:
`[ai]` prompts and overrides, `review_mode`, `[polling]`, `[circuit_breaker]`,
`[merge_tracking]` (scoped overrides included) and `[retention]`.

These are **never** sent, because they describe one machine:

| Key | Why it stays local |
|---|---|
| `server.port`, `server.bind_addr`, `server.max_concurrent_workers` | Pushing a port to another host does not fail loudly; it silently breaks that daemon |
| `github.token` | Each instance authenticates as itself. Note that the publish-boundary duplicate check (§15.3.1) only recognises reviews published under the same login, so instances with different accounts are not deduplicated by it |
| `github.repositories`, `github.non_monitored` | Runtime discovery state, merged below the store layer — overwriting them fights the discovery loop on every push |
| `ai.local_dir_base`, `ai.local_dirs_detected` | Filesystem paths that only exist on one host |
| `cluster.*` | Identity and the registry itself; only the hub owns these |

A push never aborts on the first failure: one machine rebooting must not hide
that the others were updated, so the result is reported per instance and the
API answers `207 Multi-Status`.

**The one exception to `cluster.*` staying local: the partition itself.**
`instance_id`, `default_instance` and `[cluster.routing.orgs]`/
`[cluster.routing.repos]` travel to each `worker` through a separate channel,
`PUT /cluster/partition`, not through "apply to all instances" — that endpoint
replaces the whole partition atomically (identity and rules together, so a
worker can never apply one half without the other) and, unlike the general
push, is available on a non-hub daemon, since a worker is its only real
audience. The hub sends it automatically after every reload — including the
one triggered by editing routing rules in the UI — so a worker converges
without an operator remembering to click anything. A worker that was
unreachable during that reload only receives the partition on the *next* one;
it is not (yet) re-pushed the moment it comes back, so an operator who just
brought a worker back up may want to trigger a reload (or a manual "apply to
all instances") rather than wait. The registry, tokens, `role` and every other
`cluster.*` key remain exactly as local as the table above says.

**Mixed-version clusters.** A worker running a daemon old enough to predate
`POST /cluster/prs/review` (§15.3.2) answers it with 404, which the dispatch
code treats as a failed hand-off: the hub logs the failure and reviews the PR
locally instead, so review coverage is never lost, but that worker will not
receive its routed work until it is updated. A worker running a daemon old
enough to predate `PUT /cluster/partition` answers that request with 404. The
hub falls back to
folding the partition into an ordinary config patch, unless that worker's own
reported identity (from `/health`) does not match the id it is registered
under — applying rules under an identity the worker does not recognise as
itself would leave it filtering with the wrong id, so the hub withholds the
push instead and logs which instance and which two ids disagree. Either way
the fix is the same: update that worker. Once it runs a version that
understands `PUT /cluster/partition`, the next push resolves both the
identity and the rules together.

The publish-boundary check (§15.3.1) also tolerates a mixed-version cluster:
it recognises review bodies in the format daemons older than v0.8.15 publish
(the `🤖 Heimdallm AI Review` heading and the unlinked `· Reviewed by
Heimdallm` footer) as well as the current footer, so a same-account instance
still on an old build counts as having claimed the commit.

The footer itself now names the build that produced it —
`🤖 Reviewed by [Heimdallm](https://theburrowhub.github.io/heimdallm/) (v0.8.22)`
— which is exactly what lets an operator tell, from the PR alone, which
instance in a mixed-version cluster published a given review. The version
sits after the link, outside the `Reviewed by [Heimdallm]` marker the guard
above matches on, so it plays no part in cross-instance recognition. A binary
built without the `-X main.version=...` stamp (`cd daemon && make build` with
no `VERSION`, or a bare `go build ./cmd/heimdallm`) reports the source
default verbatim, `(dev)`, rather than a fabricated version number.

### 15.5 The hub proxies; the UI talks to one origin

The app never opens a connection to a remote daemon. Every read for another
instance goes through the hub at `/instances/{id}/proxy/*`, which swaps in that
instance's own token on the way out. The alternative — CORS enabled on every
daemon plus every instance's token shipped to the browser — is more work and
strictly worse for security. Aggregation still happens in the client: the UI
fetches each instance's list through the proxy and merges them, so every row
keeps its origin badge and one instance being down degrades the view instead of
breaking it.

Only a whitelist of paths is forwarded. `POST /shutdown` is deliberately not
among them: the desktop app can respawn the daemon it manages, but nothing
would bring a remote one back.

### 15.6 GitHub API budget

Rate-limit state, the ETag cache and the request breaker are **per process**.
Several daemons sharing one GitHub token will each believe they have the full
hourly budget. Give each instance its own token, or keep the combined poll rate
under the shared quota.

### 15.7 Running another instance with Docker

The main `docker-compose.yml` pins its container name and volume pair, so it can
only describe one daemon. `docker-compose.instance.yml` parameterises both:

```bash
make up-instance NAME=b PORT=7843
docker exec heimdallm-b cat /data/api_token    # register it from the hub
make logs-instance NAME=b
make down-instance NAME=b
```

Each instance gets its own Compose project, hence its own containers and
volumes. There is no second `web` service: the UI is served once, by the hub.

### 15.8 Discovering instances on the local network

Off by default. Turn it on per daemon:

```toml
[cluster]
discovery = "mdns"    # off (default) | mdns
```

Environment equivalent: `HEIMDALLM_CLUSTER_DISCOVERY`. The Instances screen in the app shell also
offers a one-click switch on the hub.

Only the address the listener actually bound is advertised. With
`server.bind_addr` set to one specific interface, that is the only address
published — a machine with a VPN or a second NIC does not offer peers an address
where nothing is listening.

**The daemon must be listening somewhere a peer can reach it.** `server.bind_addr`
defaults to `127.0.0.1`, and on that setting nothing else on the network can
connect — so a daemon would advertise an address it then refuses. It declines to
advertise at all in that case and logs why, because the alternative is a machine
that simply never appears in the hub's list with nothing to explain it. Set
`server.bind_addr` to a routable LAN address, or to `0.0.0.0`, on any instance
you want discovered — a loopback, link-local or multicast bind is refused, and
refused for the same reasons a hub would decline to dial it. Browsing is unaffected: a hub bound to loopback can still find and
register peers, it just cannot be found itself.

The advertised port is the one the listener actually bound, not
`server.port` — the listener is claimed once at startup and no reload rebinds
it, so a live port edit takes effect on restart and discovery keeps telling the
truth in the meantime.

A peer stays in the list for a few minutes after it was last heard from. mDNS
is lossy and one browse is a two-second window, so a peer missing from a single
scan is far more likely to be a dropped packet than a daemon that left — and
dropping it immediately made instances flicker in and out of the list. The
trade is that a daemon which shuts down cleanly lingers until its entry
expires.

A daemon with discovery on advertises itself as `_heimdallm._tcp.local`,
carrying its instance id, name, role and version. A **hub** with discovery on
also browses, and lists what it finds under *"found on this network, not
registered"*, with a button to register each one.

The point is not saving a few keystrokes. A discovered instance is registered
by its mDNS hostname rather than the address it answered from, and a name is
resolved again once the connection behind it fails — so when that machine picks
up a new DHCP lease, the hub follows it within about one `probe_interval` and
with no operator action. A pinned IP never does, and a stale one means the other
instances take over its repositories while it is still reviewing them. See
§15.2 for what "follows it" costs and does not cover.

The hub also notices when an *already registered* instance answers somewhere
its `base_url` no longer points, and offers to correct it — one click, on the
instance's own card.

#### Discovery only proposes

mDNS is unauthenticated: anything on the LAN can advertise `_heimdallm._tcp`
and claim to be any instance. The design assumes that.

| Guard | What it stops |
|---|---|
| Nothing is registered automatically | An advertiser can put a name in a list; only an operator can put it in the registry |
| The API token never travels over mDNS | It has to reach you out of band, exactly as before |
| Identity comes from the daemon, not the advertisement | The hub fetches the peer's `/health` and uses the id **it** reports; the TXT record is discarded. A peer that does not answer, or will not name itself, is never offered |
| Registration pins the discovered id | If the machine at that address is no longer the one that was found, the registration is refused rather than silently pointed at whatever is there now |
| An address is never rewritten for you | A moved instance is a proposal on a card, so a rogue advertiser cannot redirect the hub by claiming a known id |
| Only `<name>.local` is ever probed | An advertised hostname is turned into a URL and fetched, so anything else would be a request-forgery primitive handed to the subnet. A name like `metadata.google.internal` is refused, not resolved, and redirects are refused too |
| Verification connects to the advertised address, never to whatever the name resolves to | A `.local` name constrains what a peer is *called*, not where it *points*: mDNS resolution is unauthenticated too, so anyone on the link could answer a query for `peer.local` with an address only the hub can reach — a cloud metadata endpoint being the obvious target. The probe dials an address published in the packet, and loopback, link-local and multicast addresses are refused |
| And only on the link the advertisement came from | Refusing those address classes is not enough on a multi-homed hub, which can reach a VPN that an attacker on the LAN cannot. The advertised address must share a locally attached network with the packet's source, and the check fails closed: a source on no attached network allows nothing, because a UDP source address is easy to spoof and any exception would just be the way around the rule. One consequence worth knowing: a routed mDNS relay will not work, which is consistent with discovery being link-local anyway |
| A browse is bounded | One sender cannot make the hub hold unlimited records or open unlimited connections: records and peers are capped per scan and verification runs through a fixed pool |

Turning discovery **on** is still a decision worth making deliberately:
announcing a service on a shared corporate network is a choice, which is why
the default is off rather than on.

#### Where it does not reach

**Containers.** mDNS does not cross Docker's default bridge — which is how the
daemon is usually deployed. A container with discovery on will typically see
nothing and be seen by nothing. Use `network_mode: host`, or set
`HEIMDALLM_CLUSTER_DISCOVERY=off` and address instances by hostname. The daemon
logs a warning at startup when it detects this combination.

**A registered `.local` name still resolves at dial time.** Discovery hands the
operator an address; from then on it is an ordinary `base_url`, resolved by the
host's resolver like any other hostname. That resolution is not authenticated —
which is true of DNS as well, and is the accepted trade for addressing an
instance by name rather than by a pinned IP. What the checks above bound is the
*unauthenticated* part: what discovery will propose, and what it will connect to
before an operator has decided anything.

**macOS needs Local Network permission, and denies it silently.** On recent
macOS the system gates multicast and local-subnet traffic per application. The
signed `Heimdallm.app` the installer deploys is registered for it and prompts
the first time, so a normal install is fine — but a daemon started as a bare
binary, which is what `make dev-daemon` does, is not registered at all. It
cannot be granted the permission from System Settings because it never appears
in the list, and the failure has no symptom: the socket binds, every read times
out, and nothing is logged. Discovery simply never finds anything.

If you are running the daemon directly and see an empty list where you expect
peers, check `dns-sd -B _heimdallm._tcp` in a terminal that does have the
permission. If that shows the peers and the daemon does not, this is why. Run
the packaged app, or run the daemon with `sudo` for a one-off test.

**Anything past the subnet.** mDNS is link-local by definition. It will not find
a machine in another VLAN, another office or a cloud VPC. Discovery is a
convenience within one network, **not** a replacement for DNS, a VPN or
Tailscale — and `base_url` accepts all of those, so a cross-site cluster is
configured exactly as it is today.

### 15.9 CLI

`cli.toml` gains an instance map alongside the flat `host`/`token` pair, which
keeps working and is presented as an instance called `local`:

```toml
default_instance = "srv-a"

[instances.srv-a]
name  = "Server A"
host  = "http://10.0.0.11:7842"
token = "..."
```

```bash
heimdallm-cli instances                       # the fleet, with health and versions
heimdallm-cli instances use srv-a             # default target for later commands
heimdallm-cli --instance srv-a prs            # one-off override
heimdallm-cli routing                         # who owns what
heimdallm-cli routing set-repo acme/tools srv-a
heimdallm-cli routing set-repo acme/tools     # clear the rule
heimdallm-cli propagate-config                # push shared config to the rest
```

With several instances configured and no `default_instance`, commands fail with
an error naming the choices rather than guessing — sending a review to the wrong
machine is worse than refusing to pick.

---

## 16. Full config.toml Reference

```toml
# Heimdallm configuration
# All values can be set via environment variables.
# Environment variables take precedence over this file.
# This file is optional; the daemon generates one on first boot from env vars.

# ── Server ──────────────────────────────────────────────────────────────────

[server]
port      = 7842        # env: HEIMDALLM_PORT
# Default is "127.0.0.1". Shown widened because a daemon on loopback is
# unreachable from any other machine, and declines to advertise itself for
# discovery — see 15.8.
bind_addr = "0.0.0.0"   # env: HEIMDALLM_BIND_ADDR

# ── GitHub ───────────────────────────────────────────────────────────────────

[github]
# Poll interval for PR checks. Any time.ParseDuration value in [1m, 24h], e.g. 3m, 10m.
poll_interval = "5m"   # env: HEIMDALLM_POLL_INTERVAL

# Static list of repos to monitor.
# env: HEIMDALLM_REPOSITORIES (comma-separated)
repositories = ["myorg/api", "myorg/frontend"]

# Repos that are known but excluded from active monitoring.
# The web UI populates this when you disable a repo.
non_monitored = []

# Topic-based auto-discovery. Any repo in discovery_orgs carrying this
# GitHub topic is merged into the monitored set every discovery_interval.
# discovery_topic    = "heimdallm-review"       # env: HEIMDALLM_DISCOVERY_TOPIC
# discovery_orgs     = ["myorg"]                 # env: HEIMDALLM_DISCOVERY_ORGS (comma-separated)
# discovery_interval = "15m"                     # env: HEIMDALLM_DISCOVERY_INTERVAL

# Base directories for auto-resolving local_dir per repo.
# Checks {base}/{repo-name} in order; first match wins.
# env: HEIMDALLM_LOCAL_DIR_BASE (comma-separated)
# local_dir_base = ["/home/heimdallm/repos/ai-platform/workspace", "/home/heimdallm/repos"]

# ── AI ────────────────────────────────────────────────────────────────────────

[ai]
# Available CLIs: claude, gemini, codex, opencode
primary  = "claude"   # env: HEIMDALLM_AI_PRIMARY
# fallback = "gemini" # env: HEIMDALLM_AI_FALLBACK

# Review feedback mode.
review_mode = "single"   # "single" | "multi" — env: HEIMDALLM_REVIEW_MODE

# Global execution timeout for AI CLI calls.
# execution_timeout = "30m"   # default: 20m — env: HEIMDALLM_EXECUTION_TIMEOUT

# Where managed clones live when local_dir is unset (see below).
# clone_dir = "/home/heimdallm/repos/worktrees"

# When true, a review that finds ANY issue is published as a COMMENT instead of
# an APPROVE (a high-severity review is still REQUEST_CHANGES; a clean review
# still approves). Overridable per org ([ai.orgs.*]) and per repo ([ai.repos.*]).
# Default: false.
# never_approve_with_issues = false

# Minimum finding severity that triggers the never_approve_with_issues
# downgrade: "low", "medium" or "high". Unset/empty = "medium", so reviews
# whose findings are all low-severity nits still approve and the findings
# stay visible in the review body. Set "low" to downgrade on any finding at
# all. Overridable per org ([ai.orgs.*]) and per repo ([ai.repos.*]).
# never_approve_min_severity = "medium"

# When local_dir is unset, Heimdallm prepares a managed shallow clone for agent
# context under clone_dir. If clone_dir is also unset, the default is
# os.TempDir()/heimdallm/<org>/<repo>. Existing directories are mutated only
# when they contain Heimdallm's .heimdallm-managed marker; local_dir and
# local_dir_base checkouts are treated as operator-owned.
# AI CLIs always run with this directory as their process cwd. When the
# installed CLI advertises a supported repo-context flag in --help, Heimdallm
# also passes that flag (for example Claude --add-dir, Gemini
# --include-directories, Codex --cd); otherwise it safely falls back to cwd.
# Managed clone cleanup is marker-protected and authenticated:
# DELETE /config/clones                         # all managed clones in configured clone dirs
# DELETE /config/clones/<url-escaped org/repo>
# make clean-clones                            # calls DELETE /config/clones

# ── Per-CLI settings (optional) ──────────────────────────────────────────────

# [ai.agents.claude]
# model                  = "claude-sonnet-4-20250514"
# max_turns              = 0
# effort                 = "high"         # low | medium | high | max
# permission_mode        = "auto"         # default | auto | acceptEdits | dontAsk
# bare                   = false          # WARNING: disables OAuth — use ANTHROPIC_API_KEY
# dangerously_skip_perms = false          # HTTP may disable; enable only in config.toml
# no_session_persistence = false
# execution_timeout      = "30m"          # exceptional per-agent override

# [ai.agents.gemini]
# model         = "gemini-2.5-pro"
# approval_mode = "auto_edit"    # default | auto_edit | plan (yolo is forbidden)

# [ai.agents.codex]
# model         = "codex-mini"
# approval_mode = "never"

# [ai.agents.opencode]
# model = "anthropic/claude-sonnet-4"

# ── Per-org overrides ────────────────────────────────────────────────────────
# Applied to all repos in the org unless overridden per-repo.
# Each field is optional and inherits from global defaults when absent.

# [ai.orgs."myorg"]
# primary = "gemini"
# fallback = "claude"
# review_mode = "multi"
# prompt = "org-pr-review-profile"
# clone_dir = "/home/heimdallm/repos/myorg-worktrees"
# never_approve_with_issues = false
# never_approve_min_severity = "medium"
#
# # Per-org circuit breaker override (optional, fields overlay the global baseline)
# [ai.orgs."myorg".circuit_breaker]
# per_repo_hr = 40

# [ai.orgs."other-org"]
# primary = "codex"

# ── Per-repo AI overrides ─────────────────────────────────────────────────────
# Each field is optional and inherits from the org or global level when absent.

# [ai.repos."myorg/api"]
# primary          = "claude"
# fallback         = "gemini"
# review_mode      = "multi"
# local_dir        = "/home/heimdallm/repos/api"  # container path; mount via HEIMDALLM_LOCAL_DIR_BASE
# prompt           = "security-profile"   # agent profile for PR reviews
#
# # Per-repo circuit breaker override (optional, fields overlay the org/global baseline)
# [ai.repos."myorg/api".circuit_breaker]
# per_pr_24h = 2

# ── Circuit breakers ──────────────────────────────────────────────────────────
# Caps completed PR reviews. 0 = use the default.
# There is no "unlimited" setting — use a large value (e.g. 99999) if needed.
# See §12 Circuit Breakers in the guide for per-org/per-repo override syntax.

# [circuit_breaker]
# per_pr_24h        = 3    # max reviews on the same PR HEAD SHA in any 24 h window
# per_repo_hr       = 20   # max PR reviews on the same repo in any 1 h window
# per_review_failure_repo_hr = 20 # max failed/in-flight review executions per repo in any 1 h window

# ── Merge tracking ────────────────────────────────────────────────────────────
# Watches the PRs you authored or are assigned to, reports exactly what is
# blocking each merge, and — at whatever level you configure — moves them along.
# Every automation defaults to false; omitting this section is a full no-op.
# See §13 Merge Tracking in the guide for the block-reason reference and the
# per-org/per-repo override syntax.

# [merge_tracking]
# enabled              = false    # master switch; false = zero GitHub calls
# enable_auto_merge    = false    # arm GitHub's own auto-merge
# update_branch        = false    # update branches that fall behind their base
# resolve_conflicts    = false    # agent resolves conflicts — FORCE-PUSHES to your branch
# merge                = false    # merge once every requirement is met
# merge_method         = "squash" # squash | merge | rebase
# include_assigned     = false    # also track PRs assigned to you but authored by others
# require_approval     = false    # demand an approval even where the repo does not
# poll_interval        = ""       # empty inherits [polling]/[github].poll_interval
# max_prs_per_tick     = 20       # one GraphQL query per PR — this bounds API spend
# max_update_attempts  = 3        # per observed head; a successful update resets it
# max_resolve_attempts = 2
# max_merge_attempts   = 3
# action_cooldown      = "10m"    # between write actions on one PR
# resolve_timeout      = "30m"    # wall clock for one conflict-resolution run
# resolve_effort       = "high"   # low | medium | high | max

# Per-org override example:
# [merge_tracking.orgs."my-org"]
# update_branch = true

# Per-repo override example:
# [merge_tracking.repos."my-org/my-repo"]
# merge            = true
# require_approval = true

# ── Retention ─────────────────────────────────────────────────────────────────

[retention]
max_days = 90   # env: HEIMDALLM_RETENTION_DAYS; set to 0 to disable purging

# ── Multiple instances (see section 15) ───────────────────────────────────────
#
# Omit this section entirely for a single-daemon install: with no [cluster] the
# routing layer reports that this daemon owns every repository and the
# control-plane endpoints are not mounted.

# [cluster]
# role             = "hub"    # standalone (default) | hub | worker; env: HEIMDALLM_CLUSTER_ROLE
# instance_id      = ""       # generated on first boot into <data dir>/instance_id
# instance_name    = "main"   # env: HEIMDALLM_INSTANCE_NAME; defaults to the hostname
# default_instance = "hub-1"  # owns every repository no rule claims
# probe_interval   = "30s"    # how often the hub health-checks the others
# discovery        = "off"    # off (default) | mdns; env: HEIMDALLM_CLUSTER_DISCOVERY

# One entry per instance, keyed by id. Exactly one token source each.
# [cluster.instances.hub-1]
# name       = "Local hub"
# base_url   = "http://127.0.0.1:7842"
# token_file = "~/.local/share/heimdallm/api_token"
# labels     = ["macos"]

# [cluster.instances.srv-a]
# base_url  = "http://srv-a.local:7842"   # prefer a hostname over a pinned IP
# token_env = "HEIMDALLM_SRV_A_TOKEN"
# enabled   = true

# [cluster.routing]
# mode             = "assignment"          # assignment | dispatch
# round_robin_pool = ["hub-1", "srv-a"]    # empty = every enabled instance
# round_robin_ops  = ["review", "merge"]

# [cluster.routing.orgs]
# "my-org" = "srv-a"

# The hub writes round-robin assignments here, so they are sticky and editable.
# [cluster.routing.repos]
# "my-org/my-repo" = "hub-1"
```
