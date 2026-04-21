# Heimdallm

> AI-powered GitHub automation for macOS and Linux — reviews your pull requests, triages your issues, and can even open implementation PRs for you. Uses Claude, Gemini, Codex, or OpenCode under the hood, posts everything back as your GitHub account, and keeps you informed via a native menu-bar app or a Flutter Web UI.

![Heimdallm dashboard](assets/icon.png)

---

## What it does

Heimdallm runs in the background and does three things, in parallel, at your configured poll interval:

### 1. PR reviews
Watches the PRs where you're requested as a reviewer, runs an AI code review, and submits it to GitHub as your account. No copy-pasting, no manual prompting.

### 2. Issue triage & auto-implement
Fetches issues from monitored repos, classifies them by label (`review_only` vs `auto_implement` vs `skip` vs `blocked`), and for the develop-track ones optionally **creates a branch, commits the change, and opens a PR** against your default branch — fully autonomous on the issues you mark for it. Issues can declare dependencies on other issues/PRs; Heimdallm holds them in a `blocked` state until the prerequisites close, then promotes them automatically.

### 3. Self-monitoring UI
A Flutter Web dashboard (`:3000`) with Dashboard, PR list, Issue list, prompt/agent editor, live config editor, and a live log stream. Opens alongside the daemon in Docker mode.

### Headline features

- **Automatic reviews** — polls `review-requested:@me` on GitHub and submits reviews as your account
- **Issue pipeline** — label-driven triage + optional auto-implement with branch/commit/PR cycle
- **Issue dependencies** — mark downstream work with a `blocked` label; declare deps via a `## Depends on` body section *or* GitHub's native sub-issues; Heimdallm auto-promotes when all blockers close
- **Configurable prompts** — general review, security audit, performance, architecture, or your own with `{diff}` `{title}` `{author}` `{comments}` placeholders, managed from the web UI at `/agents`
- **Two feedback modes** — *single* (one consolidated review) or *multi* (one GitHub comment per issue + summary), globally and per repo
- **Per-repo overrides** — different AI agent, prompt, and feedback mode per repository
- **Topic-based auto-discovery** — tag repos with a GitHub topic and Heimdallm monitors them without editing config
- **Severity gating** — only `high` severity triggers `REQUEST_CHANGES`; everything else approves with informational notes
- **Native desktop** — macOS menu-bar app, system notifications, dark mode, no Electron
- **Web UI** — Flutter Web dashboard served by Nginx with system / light / dark theme toggle, live SSE updates
- **Docker mode** — single `make up` spins up daemon + web UI for server/team deployments

---

## Installation

### macOS (DMG)

1. Download `Heimdallm-vX.Y.Z.dmg` from [Releases](https://github.com/theburrowhub/heimdallm/releases)
2. Open the DMG and drag **Heimdallm** to **Applications**
3. Open Terminal and run once:
   ```bash
   xattr -cr /Applications/Heimdallm.app
   ```
4. Double-click Heimdallm in Applications

> **Requires**: macOS 13+ (Apple Silicon or Intel), `gh` CLI authenticated (`gh auth login`).

### Linux

Download from [Releases](https://github.com/theburrowhub/heimdallm/releases) and install for your distro:

| Package | Distros | Command |
|---------|---------|---------|
| `.deb` | Ubuntu, Debian, Mint, Pop!\_OS | `sudo dpkg -i heimdallm_X.Y.Z_amd64.deb` |
| `.rpm` | Fedora, RHEL, openSUSE | `sudo rpm -i heimdallm-X.Y.Z-1.x86_64.rpm` |
| `.AppImage` | Arch, NixOS, any distro | `chmod +x Heimdallm-X.Y.Z-x86_64.AppImage && ./Heimdallm-X.Y.Z-x86_64.AppImage` |

Installs to `/opt/heimdallm/` with a desktop entry and `/usr/bin/heimdallm` symlink.

> **Requires**: `gh` CLI authenticated (`gh auth login`). Token stored via GNOME Keyring / KDE Wallet (`secret-tool`), or `~/.config/heimdallm/.token` as fallback.

### Linux (from source, Docker-built)

Developers with a clone of this repo who want a native install without the full Flutter toolchain on the host:

```bash
make install-linux           # build via Docker, install to ~/.local/
make uninstall-linux         # remove app, keep config + data
make uninstall-linux PURGE=1 # also wipe ~/.config + ~/.local/share state
```

`make install-linux` runs `verify-linux` (which builds the `heimdallm-verify` Docker image), extracts the Flutter bundle + Go daemon from the image, and stages them into `~/.local/opt/heimdallm/` with a `~/.local/bin/heimdallm` symlink, a desktop entry, and four icon sizes under `~/.local/share/icons/hicolor/`. No sudo. No host Flutter SDK. The first install also seeds `~/.config/heimdallm/.token` from `$GITHUB_TOKEN` or `gh auth token` so the app launches cleanly from the OS app launcher on first run.

> **Requires**: Docker running, `gh` CLI authenticated (or `$GITHUB_TOKEN` exported), Ubuntu 22.04 / Debian 12+ / Fedora / Arch or similarly current distro. Same binary-compatibility envelope as the CI-built `.deb`.

### Docker

For headless/server deployment, Heimdallm runs as a Docker container with all four AI CLIs bundled. The repository ships Make wrappers around `docker compose` so you don't have to remember the compose path.

#### 1. Prerequisites

- **Docker Desktop** (or Docker Engine + compose plugin) running.
- A **GitHub token** with `repo` scope (or `public_repo` for public-only). Two options:
  - If you already use `gh` CLI: just run `gh auth token` to print the token you already have — no need to create a new PAT.
  - Otherwise: create one at https://github.com/settings/tokens.
- **Credentials for your chosen AI provider** (at least one):
  - **Claude — subscription (Max / Pro / Team)**: run `claude setup-token` on your host (interactive — opens a browser) and copy the `sk-ant-oat…` token it prints. This becomes `CLAUDE_CODE_OAUTH_TOKEN`. No billing setup required if your Anthropic subscription covers Claude Code.
  - **Claude — pay-as-you-go API key**: create one at https://console.anthropic.com/settings/keys. This becomes `ANTHROPIC_API_KEY`.
  - **Gemini**: https://aistudio.google.com/apikey → `GEMINI_API_KEY`. Or reuse your host's browser OAuth — see [Reusing your host's AI authentication](#reusing-your-hosts-ai-authentication).
  - **OpenAI / Codex**: https://platform.openai.com/api-keys → `OPENAI_API_KEY` or `CODEX_API_KEY`.
  - **OpenRouter** (for OpenCode): https://openrouter.ai/keys → `OPENROUTER_API_KEY`.

#### 2. Configure

```bash
cp docker/.env.example docker/.env

# Quick-fill GITHUB_TOKEN from your existing gh auth (skip if you made a PAT):
echo "GITHUB_TOKEN=$(gh auth token)" >> docker/.env
```

Then open `docker/.env` in your editor and set at minimum:
- `HEIMDALLM_AI_PRIMARY` — `claude` | `gemini` | `codex` | `opencode`
- The credential matching that primary (see below)
- `HEIMDALLM_REPOSITORIES` — `owner/repo1,owner/repo2` (or leave empty if using `HEIMDALLM_DISCOVERY_TOPIC`)

**Filling in the Claude credential:**

- **Subscription (recommended if you have Claude Max / Pro / Team):**
  1. On your host, run **interactively** (not inside `$(...)`):
     ```bash
     claude setup-token
     ```
     It opens a browser, prints some ANSI text, and finally emits a line that starts with `sk-ant-oat…`.
  2. Copy only that `sk-ant-oat…` line and paste it manually into `docker/.env`:
     ```
     CLAUDE_CODE_OAUTH_TOKEN=sk-ant-oat...
     ```
  3. Leave `ANTHROPIC_API_KEY` empty. Do **not** set `bare = true` in `config.toml` — it disables OAuth.

  > **Why you can't inline this:** `claude setup-token` is interactive and writes prompts + colour codes to stdout. `$(claude setup-token)` captures all of it and corrupts `.env`.

- **API key (pay-as-you-go):** paste your Anthropic key directly:
  ```
  ANTHROPIC_API_KEY=sk-ant-...
  ```
  Leave `CLAUDE_CODE_OAUTH_TOKEN` empty.

For non-Claude providers see [Reusing your host's AI authentication](#reusing-your-hosts-ai-authentication) (Gemini OAuth reuse) or just set the relevant `*_API_KEY` variable from the Prerequisites list.

See [`docker/.env.example`](docker/.env.example) for every supported variable including issue-tracking, topic-based discovery, and web UI settings.

#### 3. Start the stack

```bash
make up                # daemon + web UI (recommended)
# or:
make up-daemon         # daemon only, no web UI
# after `git pull` on main:
make up-build          # same as `make up` but rebuilds from local source
make up-build-daemon   # same as `make up-daemon` but rebuilds from local source
```

`make up` refuses to start if `docker/.env` is missing and prints the exact copy-from-template command. The web container waits for the daemon's healthcheck before accepting traffic, so the first UI request never races a half-initialised daemon.

> **Tracking `main`?** `make up` reuses the last cached image. The published `:latest` on GHCR only refreshes when release-please cuts a tag, so between a PR merge and the next release you'll still be running the old binary. Use `make up-build` to rebuild from your checkout.

> **Port already in use?** If `make up` fails with `bind: address already in use` for port `3000` (common collision: a local Next.js / Vite / Grafana dev server) or `7842`, override the host-side port in `docker/.env` and re-run:
> ```bash
> echo "HEIMDALLM_WEB_PORT=3100" >> docker/.env   # for the web UI
> echo "HEIMDALLM_PORT=7843"      >> docker/.env   # for the daemon
> make up
> ```
> To see what's holding the port: `lsof -iTCP:3000 -sTCP:LISTEN`.

#### 4. Verify

```bash
make ps                          # show container status
curl http://localhost:7842/health  # -> {"status":"ok"}
```

Then open the web UI:

- macOS: `open http://localhost:3000`
- Linux: `xdg-open http://localhost:3000`
- Or browse to `http://localhost:3000` manually.

The `web` container reads the daemon's API token from the shared `heimdallm-data` volume automatically — no manual copy needed.

After `make up` completes, the target prints a summary of which AI credentials were picked up from `docker/.env` and flags the optional knobs (full-repo analysis, topic discovery) that are currently off. Use it as a checklist before opening the UI.

#### 4b. Opt-in: full-repo analysis

By default the AI agent reviews a PR using only the diff it gets from the GitHub API. That's enough for most reviews. If you want the agent to explore the surrounding code (grep sibling files, read the call graph, check imports, etc.), you need to give it a directory on the daemon's side to `cd` into.

On Docker the daemon is containerised, so "a directory on the daemon's side" means a path inside that container. Mount your host repos root into the container as `/repos` via an env var:

```bash
# In docker/.env
HEIMDALLM_REPOS_DIR=/Users/you/projects       # macOS / Linux
# or
HEIMDALLM_REPOS_DIR=/home/you/code
```

```bash
make down && make up
```

Then in the web UI, go to a repo's detail screen and under **Local directory** enter the path inside the container — typically `/repos/<repo-name>` (matching the sub-directory of your host's projects root). When unset, the compose file mounts an empty placeholder at `/repos` so the feature is a silent no-op and doesn't break anything.

The mount is read-only. The agent can read every file under that directory but cannot modify your working tree.

On the desktop app this is automatic: the `Browse` button opens the native picker and stores the host path directly — no mount needed.

#### 5. Day-to-day commands

```bash
make logs          # tail logs from daemon + web
make logs-daemon   # daemon only
make restart       # restart both containers
make down          # stop and remove containers (data volume persists)
make setup         # (optional) copy the daemon API token into docker/.env.
                   #  Only needed if you want to call the daemon's HTTP API
                   #  from outside Docker (scripts, CI, `curl` from the host).
                   #  The web UI does NOT need this — it reads the token from
                   #  the shared volume automatically.
```

The Docker image is published to `ghcr.io/theburrowhub/heimdallm:latest` on every release — `make up` pulls it automatically when the `build:` contexts haven't changed locally.

#### Reusing your host's AI authentication

If you already authenticate the AI CLIs on your host, you can reuse those
credentials inside Docker instead of pasting an API key into `.env`. The
daemon runs its **own** bundled CLIs inside the container — it never shells
out to the binaries on your host — but the container can read the same
OAuth tokens the host CLIs wrote.

**Claude Code (Max / Pro / Team subscription):**

1. On your host, run once:
   ```bash
   claude setup-token
   ```
   Copy the long-lived token it prints.
2. Add it to `docker/.env`:
   ```
   CLAUDE_CODE_OAUTH_TOKEN=<paste token>
   ```
   Leave `ANTHROPIC_API_KEY` empty. `docker-compose.yml` already forwards
   `CLAUDE_CODE_OAUTH_TOKEN` into the container.
3. Do **not** set `bare = true` in `config.toml` — it disables OAuth and
   forces API-key mode.

**Gemini CLI (browser OAuth):**

1. Authenticate `gemini` on your host the normal way. Credentials land in
   `~/.gemini/`.
2. In `docker/docker-compose.yml`, uncomment the line under the `heimdallm`
   service's `volumes:` block:
   ```yaml
   - ~/.gemini:/home/heimdallm/.gemini:ro
   ```
   The mount is read-only, so the container cannot clobber your host
   credentials.
3. Leave `GEMINI_API_KEY` empty in `docker/.env`.

**Codex / OpenCode:** host-auth reuse is not wired up yet — use API keys
(`OPENAI_API_KEY`, `CODEX_API_KEY`, `OPENROUTER_API_KEY`) in `docker/.env`.

`make up` picks up the changes on the next start. Full reference (including
Vertex AI service-account mode for Gemini) in
[`docs/e2e-test-guide.md`](docs/e2e-test-guide.md).

#### Auto-discover repositories by topic

Instead of listing every repository in `HEIMDALLM_REPOSITORIES`, you can tag
the repos you want Heimdallm to watch with a GitHub **topic** and let the
daemon discover them:

```bash
HEIMDALLM_DISCOVERY_TOPIC=heimdallm-review
HEIMDALLM_DISCOVERY_ORGS=your-org,your-other-org   # required
HEIMDALLM_DISCOVERY_INTERVAL=15m                    # optional, defaults to 15m
```

Any non-archived repo inside one of the listed orgs that carries the topic is
merged into the monitored set on each discovery cycle. Transient Search API
errors fall back to the last known-good list, so an outage never empties the
set silently. The static `HEIMDALLM_REPOSITORIES` list keeps working — the
two sources are merged (deduplicated) at poll time.

#### Dependency-based issue promotion

For multi-step work that must land in order, Heimdallm can hold downstream
issues out of the pipeline until their prerequisites close, then promote
them automatically.

**Enable it** by declaring one or more "blocked" labels:

```bash
HEIMDALLM_ISSUE_BLOCKED_LABELS=blocked
# optional — defaults to the first HEIMDALLM_ISSUE_DEVELOP_LABELS entry:
HEIMDALLM_ISSUE_PROMOTE_TO_LABEL=ready
```

**Declare dependencies** in either (or both) of two ways — Heimdallm
reads both on every poll and unions the results:

1. **Markdown `## Depends on` section** in the issue body:

    ```markdown
    ## Depends on
    - #42
    - other-org/shared#57
    ```

    Same-repo refs use `#N`. Cross-repo refs use `owner/repo#N` — works
    with any repo your `GITHUB_TOKEN` can read (cross-org included). The
    heading is case-insensitive and accepts an optional trailing colon.
    Multiple refs per bullet are fine.

2. **GitHub native sub-issues** attached to the parent via the issue UI
   or REST API. Available since the sub-issues GA; fully same-owner,
   cross-repo supported (`org/repo-a#1` can have `org/repo-b#2` as a
   sub-issue — but **not** `other-org/repo#3`, GitHub refuses that).
   No extra declaration in the body needed.

Either source alone is enough to mark an issue as having dependencies.
Refs that appear in both are deduped, and the sub-issue's state
pre-populates the dep cache so Heimdallm spends one fewer GitHub API
call on shared refs.

**How it runs.** Every poll cycle, for each issue carrying a blocked
label:

1. Parse the `## Depends on` bullets from the body.
2. Call GitHub's sub-issues REST endpoint for the same issue.
3. Union the refs from both sources; dedup.
4. For each unique ref, fetch state via the GitHub API (cached within
   the cycle).
5. If all are `closed` (merged PRs count as closed), remove the blocked
   label(s), add `HEIMDALLM_ISSUE_PROMOTE_TO_LABEL`, and leave an audit
   comment listing each dep and its state at check time.
6. The same poll cycle's fetch pass then classifies the promoted issue
   normally and dispatches it to triage or auto-implement.

Issues with a blocked label but **no declared deps in either source**
stay blocked — the daemon won't guess when to unblock them. Remove the
label manually to opt out. If the sub-issues API errors transiently on
a given issue, Heimdallm skips that issue for the current cycle rather
than risk promoting on incomplete information.

Classification precedence is
`skip > blocked > review_only > develop > default_action`, so an issue
tagged with both a blocked and a develop label stays blocked until
promotion. The feature is **opt-in**: leave `HEIMDALLM_ISSUE_BLOCKED_LABELS`
empty and nothing about the existing pipeline changes.

### Automated install (for agents / scripts)

See [LLM-HOW-TO-INSTALL.md](LLM-HOW-TO-INSTALL.md) for a step-by-step guide suitable for Claude Code, shell scripts, or any automation tool.

On first launch Heimdallm detects your `gh` CLI token automatically and sets itself up.

---

## Architecture

The **Go daemon** (`heimdalld`, port `7842`) is the engine. It polls GitHub for PRs and issues, dispatches work to the configured AI CLI, posts reviews or opens implementation PRs, and broadcasts state to any connected UI over SSE.

Two first-party UIs talk to it over HTTP:

- **Flutter desktop app** — macOS menu-bar + dashboard, system notifications. Ships inside the `.dmg` / Linux packages.
- **Flutter Web UI** — browser dashboard on port `3000`, served by Nginx, ships as a second Docker container alongside the daemon.

```
Flutter app ─┐
             ├──→ HTTP / SSE ──→  heimdalld  ──→  GitHub API
Web UI     ──┘                       │
                                     ├──→  PR review pipeline   ──→  POST /reviews
                                     ├──→  Issue triage pipeline
                                     └──→  Auto-implement       ──→  branch + commits + PR
                                                 │
                                      claude / gemini / codex / opencode CLI
```

In **Docker mode** the daemon runs standalone with the web UI container as an optional-but-recommended companion (brought up by default with `make up`). Configuration is via environment variables (`HEIMDALLM_*`) or a mounted `config.toml`, and can be edited live from the web UI at `/config`.

---

## Development

### Prerequisites

- Go 1.21+
- Flutter 3.x (stable)
- `gh` CLI authenticated
- macOS 13+ with Xcode **or** Linux with GTK 3 dev libraries

### Running locally

Two workflows — pick based on what you're changing:

**Flutter desktop app or daemon Go code** — native toolchain:
```bash
git clone https://github.com/theburrowhub/heimdallm && cd heimdallm
make dev          # builds daemon + launches Flutter app in debug mode
```
`make dev` compiles the daemon, embeds it, and launches the Flutter app with `HEIMDALLM_DAEMON_PATH` pointing to the local binary. First run detects your `gh` token and writes `~/.config/heimdallm/config.toml`.

**Web UI or Docker deployment** — containerised:
```bash
cp docker/.env.example docker/.env    # fill in GITHUB_TOKEN + provider key
make up                                # daemon + web UI
make logs                              # follow both services
```
For iterating on the Flutter Web bundle against a running daemon:
```bash
make build-web    # compile Flutter → web/; then `make up-build` to bake into the Nginx image
```

### Other targets

```bash
make test          # Run Go + Flutter test suites on the host
make test-docker   # Run Go tests inside a pinned Docker image (EDR-safe)
make dev-daemon    # Run daemon only (debug API at localhost:7842)
make dev-stop      # Stop the running daemon
make up                # Docker: bring up daemon + web UI
make up-build          # Docker: same as `up`, rebuild from local source (use on main)
make up-daemon         # Docker: daemon only
make up-build-daemon   # Docker: same as `up-daemon`, rebuild from local source
make down          # Docker: stop and remove containers
make logs          # Docker: follow all services
make restart       # Docker: bounce both containers
make ps            # Docker: container status
make setup         # Docker: copy daemon API token into docker/.env (optional)
make release-local # Build signed + notarized DMG and publish GitHub release
make verify-linux      # Linux: build the heimdallm-verify Docker image (full CI pipeline inside)
make run-linux         # Linux: launch the Flutter bundle from inside the Docker image (X11)
make install-linux     # Linux: native install to ~/.local/ (no sudo, Docker-built)
make uninstall-linux   # Linux: remove the native install (add PURGE=1 to wipe config+data)
```

> **Working on a laptop with corporate EDR (Elastic Security, CrowdStrike, …)?**
> Use `make test-docker` instead of `make test` for the Go suite. `go test`
> compiles ephemeral `*.test` binaries that EDR heuristics flag as malware;
> running inside Docker keeps those artefacts in the container's tmpfs.
> See [`AGENTS.md`](AGENTS.md) for the full rationale and the hardening
> details to share with your security team.

### Project structure

```
heimdallm/
├── daemon/                  Go background service (port 7842)
│   └── internal/
│       ├── github/          GitHub API client (PRs, issues, diffs, reviews)
│       ├── executor/        AI CLI runner (claude, gemini, codex, opencode)
│       ├── pipeline/        PR-review orchestration (fase 1)
│       ├── issues/          Issue triage + auto-implement (fase 2)
│       ├── discovery/       Topic-based repo auto-discovery
│       ├── store/           SQLite (prs, issues, reviews, agents)
│       ├── scheduler/       Poll loop, grace windows
│       ├── server/          HTTP + SSE API
│       └── keychain/        Host credential storage
├── flutter_app/             macOS / Linux / Web UI
│   ├── lib/
│   │   ├── features/
│   │   │   ├── dashboard/   Reviews tab (My Reviews / My PRs)
│   │   │   ├── repositories/Repo management + per-repo config
│   │   │   ├── agents/      Review prompt library
│   │   │   └── stats/       Review statistics
│   │   └── core/
│   │       ├── api/         HTTP + SSE client
│   │       └── setup/       First-run setup, token detection
│   └── web/                 Flutter Web build output (served by Nginx on port 3000)
├── docker/                  Docker deployment
│   ├── Dockerfile           Daemon image (Go + Node + 4 AI CLIs)
│   ├── docker-compose.yml   daemon + web UI services
│   ├── .env.example         Environment variable reference
│   └── scripts/             Local test runner (smoke/github/e2e)
├── AGENTS.md                Policy for AI coding agents working on this repo
├── .github/workflows/       Tests, build, release-please, Docker publish
└── Makefile                 Both native and Docker workflows
```

---

## Releasing

```bash
# Automatic version bump from conventional commits
# (Actions → "Tag release" → Run workflow)

# Or manually
make release-local VERSION=v1.2.3
```

The `tag.yml` workflow analyses commits since the last tag:
- `feat!:` / `BREAKING CHANGE` → major bump
- `feat:` → minor bump
- `fix:`, `refactor:`, etc. → patch bump

Pushing the tag triggers `build.yml`, which builds, signs, notarizes (if Developer ID configured), and publishes the DMG to GitHub Releases.

Alternatively, **release-please** (via `release.yml`) automates this: it reads conventional commits, opens a Release PR with changelog, and on merge creates the tag + GitHub Release. This also triggers the Docker image build and push to `ghcr.io/theburrowhub/heimdallm`.

---

## License

MIT
