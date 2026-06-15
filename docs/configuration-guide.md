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
13. [Distribution Formats](#13-distribution-formats)
14. [Circuit Breakers](#14-circuit-breakers)
15. [Autonomous Mode](#15-autonomous-mode)
16. [Polling](#16-polling)
17. [Full config.toml Reference](#17-full-configtoml-reference)

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

A low-frequency probe queries GitHub for each monitored repo's canonical `full_name` and dispatches a reconciler when it differs. The reconciler runs the rename through a single SQLite transaction (`prs`, `issues`, `activity_log`, `watch_state`, plus an audit row in `repo_renames`), rewrites the config TOML (including `[ai.repos."<old>"]` and `[ai.orgs."<old-org>"]` when the org changed), purges the old worktree so the next acquire clones fresh, and emits an `repo_renamed` SSE event for the dashboard.

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
HEIMDALLM_EXECUTION_TIMEOUT=20m   # default: 5m
```

```toml
[ai]
execution_timeout = "20m"
```

The per-agent override takes precedence when set (see [AI Agents](#7-ai-agents)).

---

## 6. Issue Tracking

The issue tracking pipeline fetches open GitHub issues from monitored repos, classifies them by label, and moves them through the stage sequence `triage` (`review_only`) -> `refinement` -> `development` (`develop` / `auto_implement`).

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
skip_labels  >  blocked_labels  >  review_only_labels  >  refinement_labels  >  develop_labels  >  default_action
```

| Field | Env var | Description |
|---|---|---|
| `skip_labels` | `HEIMDALLM_ISSUE_SKIP_LABELS` | Issues with these labels are ignored entirely |
| `blocked_labels` | `HEIMDALLM_ISSUE_BLOCKED_LABELS` | Issues held until all dependencies close, then promoted |
| `review_only_labels` | `HEIMDALLM_ISSUE_REVIEW_ONLY_LABELS` | AI posts a triage comment, no implementation |
| `refinement_labels` | `HEIMDALLM_ISSUE_REFINEMENT_LABELS` | AI reads the repo and posts a structured implementation plan |
| `develop_labels` | `HEIMDALLM_ISSUE_DEVELOP_LABELS` | AI implements the issue (branch + commit + PR) |
| `default_action` | `HEIMDALLM_ISSUE_DEFAULT_ACTION` | Applied when no label matches; `ignore` or `review_only` |

```bash
HEIMDALLM_ISSUE_DEVELOP_LABELS=enhancement,feature,bug
HEIMDALLM_ISSUE_REFINEMENT_LABELS=needs-plan
HEIMDALLM_ISSUE_REVIEW_ONLY_LABELS=question,discussion,analysis
HEIMDALLM_ISSUE_SKIP_LABELS=wontfix,duplicate,invalid
HEIMDALLM_ISSUE_DEFAULT_ACTION=ignore
```

```toml
[github.issue_tracking]
develop_labels     = ["enhancement", "feature", "bug"]
refinement_labels  = ["needs-plan"]
review_only_labels = ["question", "discussion", "analysis"]
skip_labels        = ["wontfix", "duplicate", "invalid"]
default_action     = "ignore"
```

### Stage promotion

Promotion changes only GitHub labels; the next poll cycle executes the newly visible stage. This keeps manual API/UI/CLI promotion, auto-promotion, and manual label swaps on GitHub on the same path.

| From | To | Trigger |
|---|---|---|
| `triage` / `review_only` | `refinement` | Manual Promote, `auto_promote_triage = true`, or replacing the label on GitHub |
| `refinement` | `development` | Manual Promote, `auto_promote_refinement = true`, or replacing the label on GitHub |

Manual promotion from triage falls back to `develop_labels` only for legacy configs that have no `refinement_labels`. Auto-promotion does not skip stages: when `auto_promote_triage` is unset, it defaults on only if `refinement_labels` is configured; when `auto_promote_refinement` is unset, it defaults on only if `develop_labels` is configured. If an auto-promote flag is `true` but the target label is not configured, the daemon logs a warning and leaves the issue in its current stage.

```toml
[ai]
auto_promote_triage = true       # unset = true only when refinement_labels is configured
auto_promote_refinement = true   # unset = true only when develop_labels is configured
```

> **Warning — `default_action = "review_only"` can cause re-processing loops and excessive API costs.**
>
> When `default_action` is set to `review_only`, **any issue that passes scope filters but does not match any label list** will be triaged by the AI on every poll cycle. The triage posts a comment, which bumps the issue's `updated_at` timestamp on GitHub, which in turn causes the daemon to consider the issue "updated" on the next cycle — creating an infinite loop.
>
> **Recommended:** Set `default_action = "ignore"` and use **explicit labels** to control which issues are processed:
>
> | Label | Action |
> |---|---|
> | A dedicated develop label (e.g. `heimdallm-develop`) | Auto-implement: creates branch + PR |
> | A dedicated refinement label (e.g. `heimdallm-refine`) | Deep planning: AI reads the repo and posts subtasks |
> | A dedicated triage label (e.g. `heimdallm-triage`) | Review only: AI analyses and comments once |
> | No matching label | Ignored (safe default) |
>
> This ensures issues are only processed when you explicitly opt them in, preventing runaway costs from repeated AI invocations. Using generic labels like `bug` or `enhancement` in `develop_labels` or `review_only_labels` is discouraged because these are commonly assigned to many issues and can trigger unintended mass processing.
>
> **Tier 2 polling concurrency (`ai.tier2_repo_concurrency`)**
>
> Per-repo issue polling inside a single Tier 2 issue tick runs in parallel up to `ai.tier2_repo_concurrency` repos at a time (default `5`). The GitHub API rate limiter still throttles network usage; this knob controls wall-clock parallelism. Set higher on a fast network with many monitored repos; set to `1` to force the legacy sequential behaviour. PR fetch and issue processing also run on independent tickers — each tier has its own goroutine with its own `time.Ticker`, and a `time.Ticker` drops redundant ticks when the previous run is still in flight. So a slow issue cycle never delays PR detection, and one tier never blocks the other even if its run exceeds `poll_interval`.
>
> Newly auto-discovered repos have their first PR review **deferred by one poll cycle** so the Flutter UI receives `repo_discovered` before `review_started`. The cost is one tick of latency on the very first review of a brand-new repo; the alternative was a race where the UI rendered "review in progress" for a repo it had not yet learned about.

> **Example of a safe, explicit configuration:**
>
> ```toml
> [github.issue_tracking]
> enabled        = true
> filter_mode    = "exclusive"
> default_action = "ignore"
> organizations  = ["myorg"]
> assignees      = ["myusername"]
> develop_labels     = ["heimdallm-develop"]
> refinement_labels  = ["heimdallm-refine"]
> review_only_labels = ["heimdallm-triage"]
> skip_labels        = ["wontfix", "duplicate", "invalid"]
> ```

> **When `auto_implement` produces no changes**
>
> If the agent runs to completion but leaves the working tree untouched (because the issue lacks enough context, the prompt's "leave untouched if you cannot implement" escape hatch fired, etc.), the daemon reaches a terminal state rather than retrying on every poll. The fallback comment posted on the issue carries a hidden `<!-- heimdallm:done -->` marker so the fetcher's marker scan skips the issue on subsequent ticks. The SSE event surfaced to the UI is `issue_review_error` with `reason: "auto_implement_no_changes"`, rendered as a needs-attention card — not a clean success.
>
> To reopen the issue for another auto-implement attempt, post a comment containing `<!-- heimdallm:retry -->` (or remove the develop label to stop here). The retry marker overrides the done marker and forces reprocessing. Issues stored before this behaviour landed (no marker on the comment) keep skipping with reason `auto_implement produced no changes (historical row, no done marker); add retry marker to reprocess` until you post the retry marker manually.

> **Security — `auto_implement` and untrusted issue authors**
>
> The body, title, and quoted comments of every processed issue are user-submitted input. When `auto_implement` is enabled, that input becomes part of the prompt sent to an AI CLI with **write access** to the repository checkout. A maliciously crafted issue (the classic "prompt injection" attack) could try to instruct the AI to read sensitive files from the worktree and embed them in the resulting commit.
>
> Heimdallm applies layered defenses:
>
> - The prompt now declares a **trust boundary**: issue title/body/comments are tagged as untrusted, wrapped in fenced regions, and the AI is told explicitly not to follow instructions found inside them. Any attempt to inject a forged closing fence is neutralised before the prompt is sent.
> - Before pushing, `CommitAll` scans the staged file list against a **sensitive-path denylist** covering common secret shapes: dotenv files (`.env`, `.env.*`), private keys and certificates (`*.pem`, `*.key`, `*.crt`, `*.cer`, `*.p12`, `*.pfx`, `*.gpg`, `*.asc`), keystores (`*.jks`, `*.keystore`, `*.kdbx`), VPN/wallet (`*.ovpn`, `wallet.dat`), SSH private keys (`id_rsa`, `id_dsa`, `id_ecdsa`, `id_ed25519` — public `.pub` variants are allowed), `credentials` / `credentials.*` / `.git-credentials`, `kubeconfig`, `.npmrc`, `.netrc`, `.pypirc`, shell history (`.bash_history`, `.zsh_history`), `service-account*.json`, `terraform.tfvars` / `.tfvars.*`. The operator's own `config.toml` is refused only when written at the repository root. Match is case-insensitive (so `.ENV` and `ID_RSA` are also caught). A hit aborts the commit, resets the index, removes the offending files from the worktree, and emits `slog.Warn` with the path and pattern so operators can audit prompt-injection attempts. Symlinks are refused outright even if their basename is innocuous.
>
> These defenses reduce blast radius but do not eliminate it. Two operational guidelines still apply:
>
> 1. Restrict `auto_implement` (the `develop` stage) to repositories where **all issue authors are trusted collaborators**. Public repositories accepting issues from anonymous reporters should keep `develop` disabled and rely on `triage` / `refinement` for visibility instead.
> 2. The daemon's worktree contains only the cloned repository, so the AI cannot read files outside it. Keep operator secrets (HEIMDALLM token, GitHub PAT, etc.) outside any monitored clone.

> **Review-state vigilance on `auto_implement` PRs**
>
> Once `auto_implement` creates a PR the daemon used to stop watching it. Issue #482 fixes that: Tier 3 now observes the PR's aggregated external review state and emits the `pr_review_state_changed` SSE event when it flips between `APPROVED`, `CHANGES_REQUESTED`, `COMMENTED` and the daemon-internal `FIX_PUSHED`. The state is surfaced on the issue detail and the dashboard tile as a coloured chip, so an operator sees the moment a reviewer leaves feedback without polling GitHub manually.
>
> The observation layer is **always on** and costs zero AI tokens — it adds one `GET /pulls/{n}/reviews` call per Tier 3 tick per PR that auto_implement created (PRs marked with a non-zero `auto_implement_issue_id` in the store).
>
> Two opt-in flags add agentic responses on top of the observation:
>
> ```toml
> [ai.review_response]
> enabled         = false   # default — off. Flip to true to opt in.
> per_pr_lifetime = 5       # max responder runs per PR ever
> cooldown_secs   = 300     # min seconds between two runs on the same PR
>
> [ai.review_fix]
> enabled         = false   # default — off. Flip to true to opt in.
> per_pr_lifetime = 3       # max fix runs per PR ever
> cooldown_secs   = 300
> ```
>
> When `ai.review_response.enabled = true`, a reviewer leaving a `COMMENTED` review triggers the Responder: the agent reads the latest non-bot comment, generates a short conversational reply, and posts it on the PR. The reply is review-only — the agent has no Edit/Write tool — and the reviewer's text passes through the same `UNTRUSTED USER COMMENTS` sanitisation fence the issue triage pipeline uses (#478). After `per_pr_lifetime` responses on the same PR, the Responder emits an `issue_review_error` with `reason="review_response_cap_exceeded"` and stops — there is no way to lift the cap from configuration; the counter is persistent on the PR row.
>
> When `ai.review_fix.enabled = true`, a reviewer leaving a `CHANGES_REQUESTED` review triggers the FixRunner. The daemon reserves a per-execution worktree via `repoctx` (#461), fetches the PR's head ref, checks it out at the current tip, runs the agent with the same write-mode permissions as `auto_implement`, and if the working tree changes, commits and pushes back to the same head branch. The follow-up commit is announced via a PR comment so the reviewer sees what landed. After a successful push the PR's external review state flips to `FIX_PUSHED` so the runner does not re-fire on the same CR; a reviewer submitting a fresh CR after the push flips the state back to `CHANGES_REQUESTED` and the cycle can repeat. The lifetime cap (default 3) terminates the cycle for good once reached.
>
> If the agent inspects the request and decides not to apply it (out of scope, already addressed, or unclear) the working tree stays clean — the daemon posts an advisory comment explaining the decision but does NOT push and does NOT mark `FIX_PUSHED`. A reviewer who supplies more context can re-trigger the runner within the cooldown + lifetime cap.
>
> Cost ceiling guarantees, in plain English:
>
> | Feature | Off-by-default | Counter persisted | Max AI invocations per PR |
> |---|---|---|---|
> | Observation (Tier 3 reviews fetch) | n/a (always on) | n/a | 0 |
> | `review_response` | yes | yes (`review_response_count`) | `per_pr_lifetime` (default 5) |
> | `review_fix` | yes | yes (`review_fix_count`) | `per_pr_lifetime` (default 3) |
>
> A misconfigured TOML (`per_pr_lifetime = 0` or negative) falls back to the constant default rather than meaning "unlimited" — flipping `enabled` is the only way to opt in, and the caps cannot be silently uncapped. An operator who wants to retry beyond the cap zeroes the counter in SQLite (`UPDATE prs SET review_response_count = 0 WHERE id = ?`).

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

`assignees` is owner scope, not just a sort hint. When issue tracking is
enabled and no assignees are configured, Heimdallm uses the authenticated
GitHub login for that machine. That means each daemon processes only issues
assigned to its operator by default; unassigned issues are ignored even if they
carry a Heimdallm stage label. To hand work to another operator, triage can
assign the issue to that user and move it to `refinement`; that user's
Heimdallm can then continue directly from `refinement` without repeating
triage.

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

### Operator smoke test

To verify the staged issue flow end-to-end against a real repository, walk a throwaway issue through `triage` → `refinement` → `development`:

1. Assign the test issue to exactly one user — the current Heimdallm operator — before adding any stage label. `assignees` is owner scope (see above), so the daemon will only pick up issues assigned to its operator.
2. Add the configured triage label (e.g. `heimdallm-triage`) to enter the flow.
3. Let Heimdallm promote through `triage` → `refinement` → `development` using the configured stage labels. If `auto_promote_triage` or `auto_promote_refinement` is disabled in your config, promote manually between stages.
4. If the resulting auto-implement PR is only a smoke-test artifact, close it without merging.

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

Applied to all repos in the org unless a per-repo override exists. Resolution is
field-by-field: `ai.repos."org/repo"` wins over `ai.orgs."org"`, which wins
over global defaults.

```toml
[ai.orgs."myorg"]
primary      = "gemini"
fallback     = "claude"
review_mode  = "multi"
prompt       = "org-pr-review-profile"
issue_prompt = "org-issue-triage-profile"
implement_prompt = "org-implementation-profile"
refinement_timeout = "30m"
triage_owner = "alice"
clone_dir = "/home/heimdallm/repos/myorg-worktrees"
auto_promote_triage = true
auto_promote_refinement = false
generate_pr_description = true
never_approve_with_issues = false   # true = comment instead of approving when any finding is raised
never_approve_min_severity = "low"  # findings below this severity don't trigger the downgrade

pr_reviewers = ["alice", "bob", "carol"]
pr_labels    = ["auto-generated", "ai-platform"]
pr_assignee  = "myusername"
pr_draft     = false

[ai.orgs."myorg".issue_tracking]
enabled            = true
develop_labels     = ["heimdallm-develop"]
refinement_labels  = ["heimdallm-refine"]
review_only_labels = ["heimdallm-triage"]
skip_labels        = ["wontfix"]

[ai.orgs."other-org"]
primary = "codex"
pr_reviewers = ["dave"]
pr_labels    = ["auto-generated"]
```

`local_dir` is also accepted at org scope because org overrides share the same
resolution path as repo overrides, but prefer `local_dir_base` or per-repo
`local_dir` unless every repo in the org should use the same checkout path.

Scoped overrides distinguish "unset" from "set to empty/false":

- Omit `enabled` under `ai.orgs.*.issue_tracking` or
  `ai.repos.*.issue_tracking` to inherit. If labels are present and `enabled`
  is omitted, Heimdallm still treats that scope as enabled, preserving the
  historical labels-imply-enabled behaviour.
- Set `enabled = false` to explicitly disable issue tracking at that scope,
  even when labels are also present.
- Omit list fields such as `pr_reviewers`, `pr_labels`, `develop_labels`, or
  `review_only_labels` to inherit. Set them to `[]` to explicitly clear the
  inherited list.

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
| `$HEIMDALLM_LOCAL_DIR_BASE` | `/home/heimdallm/repos` (read-only) | Host repos root for full-repo analysis |
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

## 12. CLI

`heimdallm-cli` is a terminal client for the Heimdallm daemon. Use it to inspect status, list PRs and issues, trigger manual reviews, and tail live events.

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
| `heimdallm-cli issues` | List triaged issues (filter with `--severity`) |
| `heimdallm-cli review-pr <id>` | Trigger a manual review for a PR by its internal ID |
| `heimdallm-cli review-issue <id>` | Trigger a manual review for an issue by its internal ID |
| `heimdallm-cli follow` | Stream real-time SSE events (like `tail -f`; add `--json` for raw JSON) |
| `heimdallm-cli config` | Print the daemon's running configuration as JSON |
| `heimdallm-cli stats` | Review statistics: totals, by severity, by CLI, top repos, timing |
| `heimdallm-cli dashboard` | Live terminal dashboard |

### TUI dashboard keybindings

The dashboard tabs are Activity, PRs, Issues, Config, Stats, Logs, and Server.

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
| `enter` | Open PR or issue details |
| `esc` | Close an open detail view |
| `p` | Promote a promotable issue |
| `y` / `n` | Confirm or cancel daemon shutdown |

---

## 13. Distribution Formats

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

## 14. Circuit Breakers

Circuit breakers cap AI invocations to prevent cost-runaway loops. The defaults are deliberately conservative — high-volume workflows must raise caps explicitly. There is currently no way to express "unlimited" through TOML; set a large value (e.g. `99999`) if you need near-unbounded behaviour.

```toml
[circuit_breaker]
per_pr_24h       = 3    # max reviews on the same PR HEAD SHA in any 24 h window
per_repo_hr      = 20   # max PR reviews on the same repo in any 1 h window
per_issue_24h    = 3    # max triages on the same issue in any 24 h window
per_issue_repo_hr = 10  # max issue triages on the same repo in any 1 h window
per_impl_repo_hr  = 5   # max auto_implement (development) runs per repo in any 1 h window
```

| Field | Default | Description |
|---|---|---|
| `per_pr_24h` | `3` | Reviews on the same PR HEAD SHA over a 24 h window. A new commit gets its own allowance. |
| `per_repo_hr` | `20` | PR reviews across the same repo over a 1 h window. |
| `per_issue_24h` | `3` | Issue triages on the same issue over a 24 h window. |
| `per_issue_repo_hr` | `10` | Issue triages across the same repo over a 1 h window. Tighter than the PR cap because each triage is a full-context agent run. |
| `per_impl_repo_hr` | `5` | Auto-implement (development) runs per repo in any 1 h window. The per-issue breaker only counts triages (`review_only`), leaving development uncapped at the issue level; this field is the breadth guard for autonomous mode. |

All zero values are treated as "unset" and substituted with the defaults above. There is no separate env-var mapping for circuit breaker fields — set them in `config.toml`.

### Per-org and per-repo circuit breaker overrides

All five fields are resolvable per org and per repo via `[ai.orgs."org".circuit_breaker]` and `[ai.repos."org/repo".circuit_breaker]`, following the same `repo > org > global` precedence as all other `[ai.*]` overrides. Only fields present in the override section are applied; absent fields inherit from the next level.

```toml
# Tighten the development breadth guard for a high-activity repo
[ai.repos."my-org/my-repo".circuit_breaker]
per_impl_repo_hr = 3

# Loosen the per-repo PR cap for an org with frequent pushes
[ai.orgs."my-org".circuit_breaker]
per_repo_hr = 40
```

---

## 15. Autonomous Mode

Autonomous mode turns Heimdallm into a fully-unattended end-to-end agent: it picks up issues, implements them, opens PRs, and — when configured — merges approved PRs without any human touch. Safety relies entirely on circuit breakers (see [§14 Circuit Breakers](#14-circuit-breakers)) and single-flight locks (at most one agent run per issue at a time); there is intentionally no per-day task cap. `skip_labels` and `blocked_labels` are always respected regardless of autonomous settings.

> **Autonomous mode is opt-in and off by default.** Every flag defaults to `false` (or its documented string default). Flip `enabled = true` only when you are ready for unattended operation.

### Configuration reference

```toml
[autonomous]
enabled          = false    # master switch — false = autonomous mode off entirely
auto_merge       = false    # merge gate — even approved-clean PRs are not merged unless true
merge_method     = "squash" # squash | merge | rebase (ignored when auto_merge = false)
take_others_tasks = false   # pick up issues assigned to other users (cascade bucket 3)
reassign_on_take  = false   # when taking another user's task, add the bot as co-assignee
dev_max_turns    = 0        # agent max turns for development; 0 = no practical cap
dev_effort       = "high"   # agent effort level: low | medium | high | max
dev_timeout      = "45m"    # timeout for the development agent run
claim_lease      = "2h"     # per-issue claim lease + failure/no-progress cooldown
```

| Field | Default | Description |
|---|---|---|
| `enabled` | `false` | Master switch and kill-switch. When `false`, autonomous mode is entirely inactive regardless of all other fields. |
| `auto_merge` | `false` | Merge gate. Built into the pipeline but **disabled by default**. Even when an approved, clean PR is detected, nothing is merged unless this is explicitly `true`. |
| `merge_method` | `"squash"` | Merge strategy to use when `auto_merge = true`. Accepted values: `squash`, `merge`, `rebase`. |
| `take_others_tasks` | `false` | When `false` (the default), the agent processes only issues assigned to the configured operator. Set to `true` to also pick up issues assigned to other users (cascade bucket 3 in the task resolver). |
| `reassign_on_take` | `false` | When taking another user's task (`take_others_tasks = true`), add the bot as a co-assignee (the original assignee is kept) and post an agent-generated coordination comment on the issue. |
| `dev_max_turns` | `0` | Maximum agent turns for the development stage. `0` means no practical cap (the underlying CLI default applies). |
| `dev_effort` | `"high"` | Agent effort level passed to the AI CLI for the development stage. Accepted values: `low`, `medium`, `high`, `max`. |
| `dev_timeout` | `"45m"` | Wall-clock timeout for a single development agent run. Generous by default to accommodate complex implementations. |
| `claim_lease` | `"2h"` | Per-issue claim lease, expressed as a Go duration. When the poller picks up an issue it records a lease expiring `now + claim_lease`; the selector treats any issue with an active (un-expired) lease as ineligible. This prevents two daemon ticks (or two daemons across a restart) from driving the same issue concurrently, and — because the lease is **kept** when a Drive fails or makes no progress — it doubles as the failure/no-progress **cooldown** that prevents a retry-storm on a persistently failing issue. The lease is cleared early once a PR is created (the open-PR guard takes over re-selection) and otherwise expires on its own, so a crash mid-Drive never sticks the claim permanently and needs no manual operator step. **It must exceed the longest possible Drive** (triage + refinement + development timeouts combined); the `2h` default comfortably exceeds the `45m` `dev_timeout` plus the lighter triage/refinement stages. |

**Default behaviour summary:** all bools (`enabled`, `auto_merge`, `take_others_tasks`, `reassign_on_take`) default to `false` via Go's zero value — they are not given non-zero defaults by `applyAutonomousDefaults`. The string fields (`merge_method`, `dev_effort`, `dev_timeout`, `claim_lease`) receive explicit non-zero defaults.

### Per-org and per-repo overrides

Every field supports the same `repo > org > global` precedence used throughout the rest of the config. Override only the fields you need to change; absent fields inherit from the parent level.

```toml
[autonomous]
enabled          = false
auto_merge       = false
merge_method     = "squash"
take_others_tasks = true
reassign_on_take  = true
dev_effort       = "high"
dev_timeout      = "45m"

# Enable autonomous mode for the whole org, but keep auto_merge off
[autonomous.orgs."my-org"]
enabled = true

# Enable autonomous mode for a single repo and also allow merging
[autonomous.repos."my-org/my-repo"]
enabled    = true
auto_merge = true
```

Precedence: `autonomous.repos."org/repo"` > `autonomous.orgs."org"` > global `[autonomous]`.

### Worked example: enabling autonomous mode for a single repo conservatively

```toml
# Global autonomous block — keep everything off
[autonomous]
enabled          = false
auto_merge       = false
dev_effort       = "high"
dev_timeout      = "45m"

# Opt a single repo in, with a conservative implementation cap
[autonomous.repos."my-org/my-repo"]
enabled       = true
auto_merge    = false    # review PRs but don't merge automatically
dev_max_turns = 30       # cap agent turns to bound cost

# Pair with a tight circuit breaker for that repo
[ai.repos."my-org/my-repo".circuit_breaker]
per_impl_repo_hr = 2    # at most 2 development runs per hour
```

With this setup, Heimdallm will autonomously triage issues and implement them in `my-org/my-repo`, but a human must approve and merge the resulting PRs. The circuit breaker limits development runs to 2 per hour regardless of how many issues arrive.

---

## 16. Polling

The `[polling]` table tunes how the daemon schedules its fetch cycles. All fields are optional — omitting the section entirely reproduces the prior behaviour with no change in how the daemon polls.

```toml
[polling]
poll_interval              = "5m"   # inherits [github].poll_interval when unset
min_interval               = "1m"
max_interval               = "15m"
adaptive                   = false
discovery_interval         = "5m"
tier3_interval             = "30s"
rate_limit_safety_threshold = 100
use_etag                   = true
use_graphql                = false
```

| Field | Default | Description |
|---|---|---|
| `poll_interval` | inherits `[github].poll_interval` | Base poll cadence. When unset, the value from `[github].poll_interval` (or its env var `HEIMDALLM_POLL_INTERVAL`) is used. Setting `[polling].poll_interval` overrides the `[github]` field for the polling subsystem. |
| `min_interval` | `"1m"` | Shortest interval the adaptive scheduler will use for an actively-changing repo. Has no effect when `adaptive = false`. |
| `max_interval` | `"15m"` | Longest interval the adaptive scheduler will back off to for idle repos. Has no effect when `adaptive = false`. |
| `adaptive` | `false` | When `true`, repos that have seen no new events for several consecutive cycles gradually back off from `min_interval` toward `max_interval`. Repos that receive new events reset to `min_interval`. This reduces rate-limit consumption when monitoring many quiet repos. |
| `discovery_interval` | `"5m"` | How often the topic-discovery pass runs to find newly-tagged repos. Independent of `poll_interval`. |
| `tier3_interval` | `"30s"` | Cadence of the Tier 3 observation loop (review-state polling on `auto_implement` PRs). |
| `rate_limit_safety_threshold` | `100` | Core-remaining floor. When the GitHub core rate-limit remaining count drops below this number, non-critical polling (discovery, Tier 3 observation, adaptive back-off checks) is throttled until the rate-limit window resets. Critical paths (PR review, issue triage) are not blocked by this threshold. |
| `use_etag` | `true` | Send `If-None-Match` / `ETag` conditional-request headers on list endpoints. A `304 Not Modified` response reuses the cached body without counting against the rate limit. Disable only if your GitHub proxy strips ETag headers. |
| `use_graphql` | `false` | Fetch issue lists via the GraphQL `search(type:ISSUE)` API instead of REST `/search/issues`. GraphQL requests consume from the separate GraphQL rate-limit budget (5,000 points/hour), leaving the core REST budget for other operations. Falls back to REST automatically on any GraphQL error. |

> **Reload behaviour:** All fields except `tier3_interval` and the adaptive `min_interval`/`max_interval` bounds take effect on the next `PUT /config` or file reload without a restart. Changes to `tier3_interval` and `min_interval`/`max_interval` are applied at daemon start; a restart is needed for those three to take effect after a live config change.

> **Unconfigured = no change:** A missing `[polling]` section is equivalent to setting every field to its default. There is no opt-in required — existing deployments that do not add this section continue to behave exactly as before.

### Example: adaptive polling with GraphQL enabled (conservative)

```toml
[polling]
min_interval               = "2m"
max_interval               = "20m"
adaptive                   = true
rate_limit_safety_threshold = 200   # throttle earlier on rate-constrained installations
use_etag                   = true
use_graphql                = true
```

This setup lets idle repos drift to a 20-minute cycle, saving roughly 80 % of poll calls on dormant repos, while keeping active repos at the 2-minute minimum. GraphQL consumes from the separate 5,000-point budget, and ETags ensure 304s on unchanged endpoints cost zero REST points.

---

## 17. Full config.toml Reference

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
# Poll interval for PR/issue checks. Any time.ParseDuration value in [1m, 24h], e.g. 3m, 10m.
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
#                                       # WARNING: "review_only" causes re-processing loops
#                                       # (see §6 Issue Tracking). Use "ignore" + explicit labels.
# organizations  = ["myorg"]            # env: HEIMDALLM_ISSUE_ORGANIZATIONS
# assignees      = ["myusername"]       # env: HEIMDALLM_ISSUE_ASSIGNEES
#                                       # empty defaults to the authenticated GitHub login
# develop_labels     = ["enhancement", "feature", "bug"]
#                                       # env: HEIMDALLM_ISSUE_DEVELOP_LABELS
# refinement_labels  = ["refine"]
#                                       # env: HEIMDALLM_ISSUE_REFINEMENT_LABELS
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
# refinement_timeout = "30m"  # deep issue refinement — env: HEIMDALLM_REFINEMENT_TIMEOUT

# Issue pipeline ownership and promotion defaults.
# triage_owner = "alice"
# clone_dir = "/home/heimdallm/repos/worktrees"
# auto_promote_triage = true      # unset = true only when refinement_labels is configured
# auto_promote_refinement = true  # unset = true only when develop_labels is configured

# Generate LLM-produced PR titles and descriptions for auto_implement PRs.
# generate_pr_description = false

# When true, a review that finds ANY issue is published as a COMMENT instead of
# an APPROVE (a high-severity review is still REQUEST_CHANGES; a clean review
# still approves). Overridable per org ([ai.orgs.*]) and per repo ([ai.repos.*]).
# Default: false.
# never_approve_with_issues = false

# Minimum finding severity that triggers the never_approve_with_issues
# downgrade: "low", "medium" or "high". Unset/empty = "low" (any finding
# downgrades). With "medium", reviews whose findings are all low-severity
# nits still approve. Overridable per org ([ai.orgs.*]) and per repo
# ([ai.repos.*]).
# never_approve_min_severity = "low"

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
# execution_timeout      = "20m"          # per-agent override (overrides [ai].execution_timeout)

# [ai.agents.gemini]
# model         = "gemini-2.5-pro"
# approval_mode = "auto_edit"    # default | auto_edit | plan (yolo is forbidden)

# [ai.agents.codex]
# model         = "codex-mini"
# approval_mode = "never"

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

# ── Per-org overrides ────────────────────────────────────────────────────────
# Applied to all repos in the org unless overridden per-repo.
# Each field is optional and inherits from global defaults when absent.

# [ai.orgs."myorg"]
# primary = "gemini"
# fallback = "claude"
# review_mode = "multi"
# prompt = "org-pr-review-profile"
# issue_prompt = "org-issue-triage-profile"
# implement_prompt = "org-implementation-profile"
# refinement_timeout = "30m"
# triage_owner = "alice"
# clone_dir = "/home/heimdallm/repos/myorg-worktrees"
# auto_promote_triage = true
# auto_promote_refinement = false
# generate_pr_description = true
# never_approve_with_issues = false
# never_approve_min_severity = "low"
# pr_reviewers = ["alice", "bob"]
# pr_labels    = ["auto-generated", "myorg-team"]
# pr_assignee  = "myusername"
# pr_draft     = false
#
# [ai.orgs."myorg".issue_tracking]
# enabled = true
# develop_labels = ["heimdallm-develop"]
# refinement_labels = ["heimdallm-refine"]
# review_only_labels = ["heimdallm-triage"]
# skip_labels = ["wontfix"]
#
# # Per-org circuit breaker override (optional, fields overlay the global baseline)
# [ai.orgs."myorg".circuit_breaker]
# per_repo_hr = 40

# [ai.orgs."other-org"]
# primary = "codex"
# pr_reviewers = ["carol"]

# ── Per-repo AI overrides ─────────────────────────────────────────────────────
# Each field is optional and inherits from the org or global level when absent.

# [ai.repos."myorg/api"]
# primary          = "claude"
# fallback         = "gemini"
# review_mode      = "multi"
# local_dir        = "/home/heimdallm/repos/api"  # container path; mount via HEIMDALLM_LOCAL_DIR_BASE
# prompt           = "security-profile"   # agent profile for PR reviews
# issue_prompt     = "triage-profile"     # agent profile for issue triage
# implement_prompt = "impl-profile"       # agent profile for auto_implement
# pr_reviewers     = ["carol"]
# pr_assignee      = "deploybot"
# pr_labels        = ["api-team"]
# pr_draft         = false
#
# # Per-repo circuit breaker override (optional, fields overlay the org/global baseline)
# [ai.repos."myorg/api".circuit_breaker]
# per_impl_repo_hr = 3

# ── Circuit breakers ──────────────────────────────────────────────────────────
# Caps AI invocations to prevent cost-runaway loops. 0 = use the default.
# There is no "unlimited" setting — use a large value (e.g. 99999) if needed.
# See §14 Circuit Breakers in the guide for per-org/per-repo override syntax.

# [circuit_breaker]
# per_pr_24h        = 3    # max reviews on the same PR HEAD SHA in any 24 h window
# per_repo_hr       = 20   # max PR reviews on the same repo in any 1 h window
# per_issue_24h     = 3    # max triages on the same issue in any 24 h window
# per_issue_repo_hr = 10   # max issue triages on the same repo in any 1 h window
# per_impl_repo_hr  = 5    # max auto_implement (development) runs per repo in any 1 h window

# ── Autonomous mode ───────────────────────────────────────────────────────────
# Fully-unattended end-to-end mode. All bools default to false.
# Safety relies on circuit breakers; there is no per-day task cap.
# skip_labels and blocked_labels are always respected.
# See §15 Autonomous Mode in the guide for per-org/per-repo override syntax.

# [autonomous]
# enabled           = false   # master switch / kill-switch
# auto_merge        = false   # merge gate; built but OFF by default
# merge_method      = "squash" # squash | merge | rebase (used only when auto_merge = true)
# take_others_tasks = false   # enable cascade bucket 3 (others' assigned issues)
# reassign_on_take  = false   # add bot as co-assignee when taking another user's task
# dev_max_turns     = 0       # 0 = no practical cap
# dev_effort        = "high"  # low | medium | high | max
# dev_timeout       = "45m"   # wall-clock timeout for a development agent run

# Per-org override example:
# [autonomous.orgs."my-org"]
# enabled = true

# Per-repo override example:
# [autonomous.repos."my-org/my-repo"]
# enabled    = true
# auto_merge = true

# ── Retention ─────────────────────────────────────────────────────────────────

[retention]
max_days = 90   # env: HEIMDALLM_RETENTION_DAYS; set to 0 to disable purging
```
