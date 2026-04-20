# Heimdallm

> AI-powered GitHub PR review agent for macOS and Linux — reviews your pull requests automatically using Claude, Gemini or Codex, posts the review directly to GitHub, and keeps you informed via native notifications.

![Heimdallm dashboard](assets/icon.png)

---

## What it does

Heimdallm runs silently in your menu bar. It watches the GitHub PRs where you're requested as a reviewer, runs an AI code review using Claude, Gemini, Codex, or OpenCode, and submits the review back to GitHub — no copy-pasting, no manual prompting.

- **Automatic reviews** — polls `review-requested:@me` on GitHub and submits reviews as your GitHub account
- **Configurable prompts** — general review, security audit, performance, architecture, or your own custom instructions with `{diff}` `{title}` `{author}` placeholders
- **Two feedback modes** — *single* (one consolidated review) or *multi* (one GitHub comment per issue + summary review), configurable globally and per repo
- **Per-repo overrides** — different AI agent, prompt, and feedback mode per repository
- **Severity gating** — only `high` severity triggers `REQUEST_CHANGES`; everything else approves with informational notes
- **Native desktop** — menu bar icon (macOS), system notifications, dark mode, no Electron
- **Docker mode** — runs headless as a Docker container for server/CI deployments

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

### Docker

For headless/server deployment, Heimdallm runs as a Docker container with all four AI CLIs bundled. The repository ships Make wrappers around `docker compose` so you don't have to remember the compose path.

#### 1. Prerequisites

- **Docker Desktop** (or Docker Engine + compose plugin) running.
- A **GitHub personal access token** with `repo` scope (or `public_repo` for public-only). Create one at https://github.com/settings/tokens.
- An **API key for your chosen AI provider** — at least one of:
  - Claude: https://console.anthropic.com/settings/keys (or `CLAUDE_CODE_OAUTH_TOKEN` via `claude setup-token`)
  - Gemini: https://aistudio.google.com/apikey
  - OpenAI / Codex: https://platform.openai.com/api-keys
  - OpenRouter (for OpenCode): https://openrouter.ai/keys

#### 2. Configure

```bash
cp docker/.env.example docker/.env
# Edit docker/.env — at minimum fill in:
#   GITHUB_TOKEN
#   HEIMDALLM_AI_PRIMARY      (claude | gemini | codex | opencode)
#   <provider>_API_KEY        (matching your primary)
#   HEIMDALLM_REPOSITORIES    (owner/repo1,owner/repo2 — or leave empty if
#                              using HEIMDALLM_DISCOVERY_TOPIC)
```

See [`docker/.env.example`](docker/.env.example) for every supported variable including issue-tracking, topic-based discovery, and web UI settings.

#### 3. Start the stack

```bash
make up            # daemon + web UI (recommended)
# or:
make up-daemon     # daemon only, no web UI
```

`make up` refuses to start if `docker/.env` is missing and prints the exact copy-from-template command. The web container waits for the daemon's healthcheck before accepting traffic, so the first UI request never races a half-initialised daemon.

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

### Automated install (for agents / scripts)

See [LLM-HOW-TO-INSTALL.md](LLM-HOW-TO-INSTALL.md) for a step-by-step guide suitable for Claude Code, shell scripts, or any automation tool.

On first launch Heimdallm detects your `gh` CLI token automatically and sets itself up.

---

## Architecture

```
Heimdallm.app/
├── Heimdallm          ← Flutter macOS UI
└── heimdalld          ← Go daemon (background service)
```

The **Go daemon** (`heimdalld`) runs as a background process on port `7842`. It polls GitHub, runs the AI CLI, posts reviews, and broadcasts events over SSE. The **Flutter app** is a thin UI client — it talks to the daemon over HTTP and shows a menu bar icon, dashboard, and notifications.

```
Flutter app  ←→  HTTP/SSE  ←→  heimdalld  ←→  GitHub API
                                     ↓
                              claude / gemini / codex CLI
```

In **Docker mode**, the daemon runs standalone without the Flutter UI. Configuration is via environment variables (`HEIMDALLM_*`) or a mounted `config.toml`.

---

## Development

### Prerequisites

- Go 1.21+
- Flutter 3.x (stable)
- `gh` CLI authenticated
- macOS 13+ with Xcode **or** Linux with GTK 3 dev libraries

### Running locally

```bash
# Clone
git clone https://github.com/theburrowhub/heimdallm && cd heimdallm

# Run (builds daemon + launches app in debug mode)
make dev
```

`make dev` compiles the daemon, embeds it, and launches the Flutter app with `HEIMDALLM_DAEMON_PATH` pointing to the local binary. On first run the app detects your `gh` token and creates `~/.config/heimdallm/config.toml`.

### Other targets

```bash
make test          # Run Go + Flutter test suites on the host
make test-docker   # Run Go tests inside a pinned Docker image (EDR-safe)
make dev-daemon    # Run daemon only (debug API at localhost:7842)
make dev-stop      # Stop the running daemon
make release-local # Build signed + notarized DMG and publish GitHub release
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
├── daemon/                  Go background service
│   └── internal/
│       ├── github/          GitHub API client (PRs, diffs, submit reviews)
│       ├── executor/        AI CLI runner (claude, gemini, codex)
│       ├── pipeline/        Review orchestration
│       ├── store/           SQLite (prs, reviews, agents)
│       └── server/          HTTP + SSE API
├── flutter_app/             macOS UI
│   └── lib/
│       ├── features/
│       │   ├── dashboard/   Reviews tab (My Reviews / My PRs)
│       │   ├── repositories/Repo management + per-repo config
│       │   ├── agents/      Review prompt library
│       │   └── stats/       Review statistics
│       └── core/
│           ├── api/         HTTP + SSE client
│           └── setup/       First-run setup, token detection
├── docker/                  Docker deployment
│   ├── Dockerfile           Multi-stage build (Go + Node.js + 4 AI CLIs)
│   ├── docker-compose.yml   Production deployment
│   ├── .env.example         Environment variable reference
│   └── scripts/             Local test runner (smoke/github/e2e)
├── .github/workflows/
│   ├── tests.yml            CI: Go + Flutter tests on PR/main
│   ├── build.yml            CI: build + release on version tags
│   ├── tag.yml              Manual: compute semver tag from conventional commits
│   ├── docker-publish.yml   CI: Docker build validation on PRs
│   └── release.yml          CI: release-please + Docker push to GHCR
└── Makefile
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
