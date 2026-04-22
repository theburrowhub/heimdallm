# Heimdallm Configuration Guide

Full reference for all settings, environment variables, and deployment options.

---

## Table of Contents

1. [Overview](#1-overview)
2. [Server](#2-server)
3. [Repository Monitoring](#3-repository-monitoring)
4. [Local Directory Resolution](#4-local-directory-resolution)
5. [PR Review Pipeline](#5-pr-review-pipeline)
6. [Issue Tracking](#6-issue-tracking)
7. [AI Agents](#7-ai-agents)
8. [PR Creation Metadata](#8-pr-creation-metadata)
9. [Authentication](#9-authentication)
10. [Docker Deployment](#10-docker-deployment)
11. [Retention](#11-retention)
12. [CLI](#12-cli)
13. [Full config.toml Reference](#13-full-configtoml-reference)

---

## 1. Overview

Heimdallm reads configuration from three sources, in order of precedence:

```
Environment variables  >  config.toml  >  Built-in defaults
```

**Environment variables** (`HEIMDALLM_*`) are the primary configuration mechanism for Docker deployments. Set them in `docker/.env`.

**config.toml** is an optional TOML file mounted at `/config/config.toml` inside the container. It supports richer structures (per-repo overrides, per-org PR metadata) that cannot be expressed as flat env vars. The web UI's Configuration screen edits this file live.

**HTTP API** — any field in `config.toml` can also be updated at runtime via `PUT /config`. Changes take effect on the next poll cycle without a container restart.

### Config sources at a glance

| What you want to configure | Recommended source |
|---|---|
| Tokens and secrets | `docker/.env` (env vars) |
| Simple daemon settings | `docker/.env` (env vars) |
| Per-repo AI overrides | `config.toml` or web UI |
| Per-org PR metadata | `config.toml` or web UI |
| Agent/prompt profiles | Web UI at `/agents` |

---

## 2. Server

Controls the HTTP interface the daemon listens on.

```toml
[server]
port      = 7842
bind_addr = "0.0.0.0"
```

| TOML field | Env var | Default | Description |
|---|---|---|---|
| `port` | `HEIMDALLM_PORT` | `7842` | TCP port the daemon listens on |
| `bind_addr` | `HEIMDALLM_BIND_ADDR` | `0.0.0.0` | Interface to bind (use `127.0.0.1` to restrict to localhost) |

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

### Poll interval

```bash
HEIMDALLM_POLL_INTERVAL=5m   # valid: 1m, 5m, 30m, 1h
```

```toml
[github]
poll_interval = "5m"
```

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

### Default `/repos/{repo-name}` fallback

When `HEIMDALLM_LOCAL_DIR_BASE` is set in `docker/.env`, the compose file bind-mounts your host's repos root to `/repos` inside the container (read-only). The daemon then falls back to `/repos/{short-repo-name}` for any repo that doesn't match the base list.

```bash
# docker/.env — mount your host repos root
HEIMDALLM_LOCAL_DIR_BASE=/Users/you/projects
```

The corresponding volume mount in `docker-compose.yml`:

```yaml
volumes:
  - ${HEIMDALLM_LOCAL_DIR_BASE}:/repos:ro
```

After `make down && make up`, any repo at `/Users/you/projects/api` is automatically accessible at `/repos/api` inside the container. On Docker the env var must be a single path (compose volumes take one source); on desktop the daemon reads the same env var and accepts a comma-separated list of paths for multi-workspace setups.

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
HEIMDALLM_EXECUTION_TIMEOUT=20m   # default: 5m
```

```toml
[ai]
execution_timeout = "20m"
```

The per-agent override takes precedence when set (see [AI Agents](#7-ai-agents)).

---

## 6. Issue Tracking

The issue tracking pipeline fetches open GitHub issues from monitored repos, classifies them by label, and — depending on the classification — posts an AI analysis comment (`review_only`) or creates a branch, commits the fix, and opens a PR (`develop` / `auto_implement`).

### Enabling

```bash
# docker/.env
HEIMDALLM_ISSUE_TRACKING_ENABLED=true
```

```toml
[github.issue_tracking]
enabled = true
```

### Filter mode

Controls how the `organizations` and `assignees` filters combine:

| Value | Behaviour |
|---|---|
| `exclusive` | Issue must match **all** configured filters (AND) |
| `inclusive` | Issue must match **any** configured filter (OR) |

```bash
HEIMDALLM_ISSUE_FILTER_MODE=exclusive
```

```toml
filter_mode = "exclusive"
```

### Label classification

Labels are matched case-insensitively. Precedence from highest to lowest:

```
skip_labels  >  blocked_labels  >  review_only_labels  >  develop_labels  >  default_action
```

| Field | Env var | Description |
|---|---|---|
| `skip_labels` | `HEIMDALLM_ISSUE_SKIP_LABELS` | Issues with these labels are ignored entirely |
| `blocked_labels` | `HEIMDALLM_ISSUE_BLOCKED_LABELS` | Issues held until all dependencies close, then promoted |
| `review_only_labels` | `HEIMDALLM_ISSUE_REVIEW_ONLY_LABELS` | AI posts a triage comment, no implementation |
| `develop_labels` | `HEIMDALLM_ISSUE_DEVELOP_LABELS` | AI implements the issue (branch + commit + PR) |
| `default_action` | `HEIMDALLM_ISSUE_DEFAULT_ACTION` | Applied when no label matches; `ignore` or `review_only` |

```bash
HEIMDALLM_ISSUE_DEVELOP_LABELS=enhancement,feature,bug
HEIMDALLM_ISSUE_REVIEW_ONLY_LABELS=question,discussion,analysis
HEIMDALLM_ISSUE_SKIP_LABELS=wontfix,duplicate,invalid
HEIMDALLM_ISSUE_DEFAULT_ACTION=ignore
```

```toml
[github.issue_tracking]
develop_labels     = ["enhancement", "feature", "bug"]
review_only_labels = ["question", "discussion", "analysis"]
skip_labels        = ["wontfix", "duplicate", "invalid"]
default_action     = "ignore"
```

### Scope filters

Restrict which issues the pipeline processes:

```bash
HEIMDALLM_ISSUE_ORGANIZATIONS=myorg
HEIMDALLM_ISSUE_ASSIGNEES=myusername
```

```toml
[github.issue_tracking]
organizations = ["myorg"]
assignees     = ["myusername"]
```

### Dependency-based issue promotion

Mark downstream issues `blocked` until their prerequisites close, then promote them automatically.

```bash
HEIMDALLM_ISSUE_BLOCKED_LABELS=blocked
HEIMDALLM_ISSUE_PROMOTE_TO_LABEL=ready   # defaults to first develop_label when unset
```

Declare dependencies in the issue body:

```markdown
## Depends on
- #42
- other-org/shared-repo#57
```

Or use GitHub's native sub-issues feature. Heimdallm reads both sources and unions the results.

When all dependencies are `closed`, the daemon removes the blocked label, adds the promote-to label, and leaves an audit comment.

---

## 7. AI Agents

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
execution_timeout      = "20m"          # per-agent override

[ai.agents.gemini]
model = "gemini-2.5-pro"

[ai.agents.codex]
model         = "codex-mini"
approval_mode = "full-auto"

[ai.agents.opencode]
model = "anthropic/claude-sonnet-4"
```

**Important:** `bare = true` disables OAuth authentication. Use it only when authenticating via `ANTHROPIC_API_KEY`, never with `CLAUDE_CODE_OAUTH_TOKEN`.

`dangerously_skip_perms` cannot be set via the HTTP API for security reasons — it must be set in `config.toml` directly.

### Prompt categories

Each repo can use different agent profiles for different pipeline stages:

| Prompt field | Pipeline stage | Description |
|---|---|---|
| `prompt` | PR Review | The agent profile used when reviewing pull requests |
| `issue_prompt` | Issue Triage | The agent profile used for issue classification and analysis |
| `implement_prompt` | Development | The agent profile used for auto-implement code generation |

Prompt profiles are managed in the web UI at `/agents`. Assign them per-repo:

```toml
[ai.repos."myorg/api"]
prompt           = "security-review-profile-id"
issue_prompt     = "issue-triage-profile-id"
implement_prompt = "backend-impl-profile-id"
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

## 8. PR Creation Metadata

When the issue pipeline creates an implementation PR (`auto_implement`), Heimdallm applies metadata — reviewers, labels, assignee, draft status — from a three-level hierarchy:

```
per-repo  >  per-org  >  global defaults
```

Each field resolves independently. A per-repo `pr_assignee` does not block the per-org `pr_reviewers` from applying.

### Global defaults

```bash
# docker/.env
HEIMDALLM_PR_REVIEWERS=alice,bob
HEIMDALLM_PR_LABELS=auto-generated,heimdallm
HEIMDALLM_PR_ASSIGNEE=myusername
HEIMDALLM_PR_DRAFT=false
```

```toml
# config.toml — flat fields under [ai]
[ai]
pr_reviewers = ["alice", "bob"]
pr_labels    = ["auto-generated", "heimdallm"]
pr_assignee  = "myusername"
pr_draft     = false
```

Alternatively, use the nested `[ai.pr_metadata]` section (flat fields take precedence when both are set):

```toml
[ai.pr_metadata]
reviewers = ["alice", "bob"]
labels    = ["auto-generated"]
assignee  = "myusername"
draft     = false
```

### Per-org overrides

Applied to all repos in the org unless a per-repo override exists:

```toml
[ai.orgs."myorg"]
pr_reviewers = ["alice", "bob", "carol"]
pr_labels    = ["auto-generated", "ai-platform"]
pr_assignee  = "myusername"
pr_draft     = false

[ai.orgs."other-org"]
pr_reviewers = ["dave"]
pr_labels    = ["auto-generated"]
```

### Per-repo overrides

```toml
[ai.repos."myorg/api"]
pr_reviewers = ["carol"]
pr_assignee  = "deploybot"
pr_labels    = ["api-team", "auto-generated"]
pr_draft     = true
```

### Team reviewers

Request review from a GitHub team by using the `org/team-name` format:

```toml
[ai.repos."myorg/api"]
pr_reviewers = ["myorg/backend-team", "alice"]
```

---

## 9. Authentication

### GitHub token

Required. The daemon uses this token to read PRs, post reviews, and (for `auto_implement`) push branches and open PRs.

```bash
# docker/.env
GITHUB_TOKEN=ghp_your_token_here
```

**Required scopes:**

| Scope | Why |
|---|---|
| `repo` | Read private repos, post reviews, create branches and PRs |
| `workflow` | Required when `auto_implement` pushes commits that touch `.github/workflows/` files. Without this scope, pushes to workflow files are silently rejected by GitHub |
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

## 10. Docker Deployment

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
| `$HEIMDALLM_LOCAL_DIR_BASE` | `/repos` (read-only) | Host repos root for full-repo analysis |
| SSH agent socket | `/ssh-agent` (read-only) | SSH agent for git operations in `auto_implement` |

The config volume is a **named volume** (not a bind mount). This is intentional — a bind mount would be owned by root on the host, which blocked the daemon from writing `config.toml`. The image chowns `/config` to the `heimdallm` user during build.

### SSH agent forwarding

`auto_implement` pushes branches over SSH. Forward your host's SSH agent into the container:

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

## 11. Retention

Controls how long reviewed PR records are kept in the SQLite database.

```bash
HEIMDALLM_RETENTION_DAYS=90
```

```toml
[retention]
max_days = 90
```

The purge runs on each poll cycle. Records older than `max_days` are deleted. Set to `0` to disable purging.

### Log rotation

The daemon mirrors its structured logs to `/data/heimdallm.log` for the web UI's live log view. The file is size-rotated to prevent filling the volume.

```bash
HEIMDALLM_LOG_MAX_MB=50    # max size before rotation (default: 50 MiB)
HEIMDALLM_LOG_KEEP=3       # rotated backups to keep: .log.1, .log.2, .log.3
```

Worst-case disk use: `(HEIMDALLM_LOG_KEEP + 1) × HEIMDALLM_LOG_MAX_MB`.

---

## 12. CLI

`heimdallm-cli` is a terminal client for the Heimdallm daemon. Use it to inspect status, list PRs and issues, trigger manual reviews, and tail live events.

### Installation

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

### Commands

| Command | Description |
|---|---|
| `heimdallm-cli status` | Daemon state, uptime, monitored repos, stats summary |
| `heimdallm-cli prs` | List reviewed PRs (filter with `--severity info\|low\|medium\|high`) |
| `heimdallm-cli issues` | List triaged issues (filter with `--severity`) |
| `heimdallm-cli review-pr <id>` | Trigger a manual review for a PR by its internal ID |
| `heimdallm-cli review-issue <id>` | Trigger a manual review for an issue by its internal ID |
| `heimdallm-cli follow` | Stream real-time SSE events (like `tail -f`; add `--json` for raw JSON) |
| `heimdallm-cli config` | Print the daemon's running configuration as JSON |
| `heimdallm-cli stats` | Review statistics: totals, by severity, by CLI, top repos, timing |
| `heimdallm-cli dashboard` | Live terminal dashboard |

---

## 13. Full config.toml Reference

```toml
# Heimdallm configuration
# All values can be set via environment variables.
# Environment variables take precedence over this file.
# This file is optional; the daemon generates one on first boot from env vars.

# ── Server ──────────────────────────────────────────────────────────────────

[server]
port      = 7842        # env: HEIMDALLM_PORT
bind_addr = "0.0.0.0"  # env: HEIMDALLM_BIND_ADDR

# ── GitHub ───────────────────────────────────────────────────────────────────

[github]
# Poll interval for PR/issue checks. Valid: 1m, 5m, 30m, 1h.
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

# ── Issue tracking ───────────────────────────────────────────────────────────

# [github.issue_tracking]
# enabled    = false                    # env: HEIMDALLM_ISSUE_TRACKING_ENABLED
# filter_mode = "exclusive"             # "exclusive" (AND) | "inclusive" (OR)
#                                       # env: HEIMDALLM_ISSUE_FILTER_MODE
# default_action = "ignore"             # "ignore" | "review_only"
#                                       # env: HEIMDALLM_ISSUE_DEFAULT_ACTION
# organizations  = ["myorg"]            # env: HEIMDALLM_ISSUE_ORGANIZATIONS
# assignees      = ["myusername"]       # env: HEIMDALLM_ISSUE_ASSIGNEES
# develop_labels     = ["enhancement", "feature", "bug"]
#                                       # env: HEIMDALLM_ISSUE_DEVELOP_LABELS
# review_only_labels = ["question", "discussion", "analysis"]
#                                       # env: HEIMDALLM_ISSUE_REVIEW_ONLY_LABELS
# skip_labels        = ["wontfix", "duplicate", "invalid"]
#                                       # env: HEIMDALLM_ISSUE_SKIP_LABELS
# blocked_labels     = ["blocked"]      # env: HEIMDALLM_ISSUE_BLOCKED_LABELS
# promote_to_label   = "ready"          # env: HEIMDALLM_ISSUE_PROMOTE_TO_LABEL
#                                       # defaults to first develop_labels entry

# ── AI ────────────────────────────────────────────────────────────────────────

[ai]
# Available CLIs: claude, gemini, codex, opencode
primary  = "claude"   # env: HEIMDALLM_AI_PRIMARY
# fallback = "gemini" # env: HEIMDALLM_AI_FALLBACK

# Review feedback mode.
review_mode = "single"   # "single" | "multi" — env: HEIMDALLM_REVIEW_MODE

# Global execution timeout for AI CLI calls.
# execution_timeout = "20m"   # default: 5m — env: HEIMDALLM_EXECUTION_TIMEOUT

# Generate LLM-produced PR titles and descriptions for auto_implement PRs.
# generate_pr_description = false

# ── Per-CLI settings (optional) ──────────────────────────────────────────────

# [ai.agents.claude]
# model                  = "claude-sonnet-4-20250514"
# max_turns              = 0
# effort                 = "high"         # low | medium | high | max
# permission_mode        = "auto"         # default | auto | acceptEdits | dontAsk
# bare                   = false          # WARNING: disables OAuth — use ANTHROPIC_API_KEY
# dangerously_skip_perms = false          # cannot be set via HTTP API
# no_session_persistence = false
# execution_timeout      = "20m"          # per-agent override (overrides [ai].execution_timeout)

# [ai.agents.gemini]
# model = "gemini-2.5-pro"

# [ai.agents.codex]
# model         = "codex-mini"
# approval_mode = "full-auto"

# [ai.agents.opencode]
# model = "anthropic/claude-sonnet-4"

# ── Global PR creation metadata defaults ─────────────────────────────────────
# Applied when auto_implement creates a PR.
# Resolution priority: per-repo > per-org > global defaults.
# Each field resolves independently.
# env: HEIMDALLM_PR_REVIEWERS, HEIMDALLM_PR_LABELS, HEIMDALLM_PR_ASSIGNEE, HEIMDALLM_PR_DRAFT

# pr_reviewers = ["alice", "myorg/backend-team"]
# pr_labels    = ["auto-generated", "heimdallm"]
# pr_assignee  = "myusername"
# pr_draft     = false

# ── Per-org PR metadata overrides ────────────────────────────────────────────
# Applied to all repos in the org unless overridden per-repo.

# [ai.orgs."myorg"]
# pr_reviewers = ["alice", "bob"]
# pr_labels    = ["auto-generated", "myorg-team"]
# pr_assignee  = "myusername"
# pr_draft     = false

# [ai.orgs."other-org"]
# pr_reviewers = ["carol"]

# ── Per-repo AI overrides ─────────────────────────────────────────────────────
# Each field is optional and inherits from the org or global level when absent.

# [ai.repos."myorg/api"]
# primary          = "claude"
# fallback         = "gemini"
# review_mode      = "multi"
# local_dir        = "/repos/api"  # container path; mount via HEIMDALLM_LOCAL_DIR_BASE
# prompt           = "security-profile"   # agent profile for PR reviews
# issue_prompt     = "triage-profile"     # agent profile for issue triage
# implement_prompt = "impl-profile"       # agent profile for auto_implement
# pr_reviewers     = ["carol"]
# pr_assignee      = "deploybot"
# pr_labels        = ["api-team"]
# pr_draft         = false

# ── Retention ─────────────────────────────────────────────────────────────────

[retention]
max_days = 90   # env: HEIMDALLM_RETENTION_DAYS; set to 0 to disable purging
```
