# Heimdallm

> AI-powered GitHub PR review agent for macOS and Linux — reviews your pull requests automatically using Claude, Gemini or Codex, posts the review directly to GitHub, and keeps you informed via native notifications.

![Heimdallm dashboard](assets/icon.png)

---

## What it does

Heimdallm runs silently in your menu bar. It watches the GitHub PRs where you're requested as a reviewer, runs an AI code review on each one, and submits the review back to GitHub — no copy-pasting, no manual prompting.

- **Automatic reviews** — polls `review-requested:@me` on GitHub and submits reviews as your GitHub account
- **Configurable prompts** — general review, security audit, performance, architecture, or your own custom instructions with `{diff}` `{title}` `{author}` placeholders
- **Two feedback modes** — *single* (one consolidated review) or *multi* (one GitHub comment per issue + summary review), configurable globally and per repo
- **Per-repo overrides** — different AI agent, prompt, and feedback mode per repository
- **Severity gating** — only `high` severity triggers `REQUEST_CHANGES`; everything else approves with informational notes
- **Native desktop** — menu bar icon (macOS), system notifications, dark mode, no Electron

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
make test          # Run Go + Flutter test suites
make dev-daemon    # Run daemon only (debug API at localhost:7842)
make dev-stop      # Stop the running daemon
make release-local # Build signed + notarized DMG and publish GitHub release
```

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
