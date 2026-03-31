# Heimdallr

> AI-powered GitHub PR review agent for macOS — reviews your pull requests automatically using Claude, Gemini or Codex, posts the review directly to GitHub, and keeps you informed via native notifications.

![Heimdallr dashboard](assets/icon.png)

---

## What it does

Heimdallr runs silently in your menu bar. It watches the GitHub PRs where you're requested as a reviewer, runs an AI code review on each one, and submits the review back to GitHub — no copy-pasting, no manual prompting.

- **Automatic reviews** — polls `review-requested:@me` on GitHub and submits reviews as your GitHub account
- **Configurable prompts** — general review, security audit, performance, architecture, or your own custom instructions with `{diff}` `{title}` `{author}` placeholders
- **Per-repo overrides** — different AI agent and prompt per repository
- **Severity gating** — only `high` severity triggers `REQUEST_CHANGES`; everything else approves with informational notes
- **Native macOS** — menu bar icon, system notifications, dark mode, no Electron

---

## Installation

### From GitHub Releases (recommended)

1. Download `Heimdallr-vX.Y.Z.dmg` from [Releases](https://github.com/theburrowhub/heimdallr/releases)
2. Open the DMG and drag **Heimdallr** to **Applications**
3. Open Terminal and run:
   ```bash
   xattr -cr /Applications/Heimdallr.app
   ```
4. Double-click Heimdallr in Applications

On first launch Heimdallr detects your `gh` CLI token automatically and sets itself up.

> **Requires**: macOS 13+ (Apple Silicon or Intel), `gh` CLI authenticated (`gh auth login`).

### Automated install (for agents / scripts)

See [LLM-HOW-TO-INSTALL.md](LLM-HOW-TO-INSTALL.md) for a step-by-step guide suitable for Claude Code, shell scripts, or any automation tool.

---

## Architecture

```
Heimdallr.app/
├── Heimdallr          ← Flutter macOS UI
└── heimdalld          ← Go daemon (background service)
```

The **Go daemon** (`heimdalld`) runs as a background process on port `7842`. It polls GitHub, runs the AI CLI, posts reviews, and broadcasts events over SSE. The **Flutter app** is a thin UI client — it talks to the daemon over HTTP and shows a menu bar icon, dashboard, and notifications.

```
Flutter app  ←→  HTTP/SSE  ←→  heimdalld  ←→  GitHub API
                                     ↓
                              claude / gemini / codex CLI
```

---

## Development

### Prerequisites

- Go 1.21+
- Flutter 3.x (stable)
- `gh` CLI authenticated
- macOS 13+ with Xcode

### Running locally

```bash
# Clone
git clone https://github.com/theburrowhub/heimdallr && cd heimdallr

# Run (builds daemon + launches app in debug mode)
make dev
```

`make dev` compiles the daemon, embeds it, and launches the Flutter app with `HEIMDALLR_DAEMON_PATH` pointing to the local binary. On first run the app detects your `gh` token and creates `~/.config/heimdallr/config.toml`.

### Other targets

```bash
make test          # Run Go + Flutter test suites
make dev-daemon    # Run daemon only (debug API at localhost:7842)
make dev-stop      # Stop the running daemon
make release-local # Build signed + notarized DMG and publish GitHub release
```

### Project structure

```
heimdallr/
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
├── .github/workflows/
│   ├── tests.yml            CI: Go + Flutter tests on PR/main
│   ├── build.yml            CI: build + release on version tags
│   └── tag.yml              Manual: compute semver tag from conventional commits
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

---

## License

MIT
