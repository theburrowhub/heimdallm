# AGENTS.md

Instructions for AI coding agents (Claude Code, Cursor, Copilot, …) and human
collaborators working on this repo. These rules override the agent's default
workflow — follow them exactly.

## Go tests must run in Docker

**Always use `make test-docker` for the Go daemon test suite.**

```bash
make test-docker
# narrow down to specific packages/tests:
make test-docker GO_TEST_ARGS="-run TestDiscovery ./internal/discovery/..."
```

### Why this is non-negotiable

`go test` compiles a transient binary named `<pkg>.test` in
`/var/folders/.../go-build/` (macOS) or `/tmp/go-build...` (Linux), then runs
it. These tests:

- open random local ports via `httptest.NewServer`,
- send HTTP requests with `Authorization: Bearer <token>` headers,
- exit within milliseconds.

That signature matches heuristics used by corporate EDR agents
(Elastic Security, CrowdStrike, SentinelOne, Defender for Endpoint, …).
The EDR will kill the process mid-test — surfaced to the user as
`signal: killed` — and send a **malware alert** to the security team, who
will then open a ticket with the developer. **This has already happened
once in this repo (2026-04-17).** We do not want to do it again.

Running the tests inside a Docker container means the ephemeral binaries
live in the container's tmpfs, not the host filesystem. The host EDR never
sees them.

### What `make test-docker` guarantees

Hardening details are inlined in the Makefile target. Summary:

| Concern | Guarantee |
|---|---|
| Image | Official `golang:1.21-alpine`, **pinned by SHA256 digest** (not a mutable tag) |
| Host filesystem access | Single mount: repo at `/src`, **read-only** (`:ro`) |
| Build cache | Redirected to `/tmp/heimdallm-gocache/` — does not touch `~/.cache` or `~/go` |
| Privileges | Runs as invoking user (`--user $(id -u):$(id -g)`) — no root in the container |
| Container persistence | `--rm` — destroyed on exit, no `*.test` artefacts on the host |
| Network | Default bridge (needed for `go mod download` on first run) |
| Ports | None exposed |

If IT/Security asks how we run Go tests on their laptops, point them at
this section and the `test-docker` target.

## When `go test` on the host is OK

Never, on a corporate laptop with an EDR, unless Security has explicitly
whitelisted `$GOCACHE` and `/var/folders/.../go-build/`. Raising a ticket
for that exception is preferable to bypassing the rule.

On personal machines, CI runners, or Docker-in-Docker sandboxes, `go test`
directly is fine. The CI workflow (`.github/workflows/tests.yml`) runs on
`macos-14` GitHub Actions runners and uses `setup-go` natively — that
environment has no EDR.

## Flutter tests

Flutter tests do not trip EDR heuristics (Dart VM, no ephemeral native
binaries). Run them normally:

```bash
cd flutter_app && flutter test
# or combined with Go tests (note: `make test` runs Go on the host):
make test
```

**Prefer `make test-docker && cd flutter_app && flutter test`** over
`make test` on EDR-protected endpoints.

## Flutter platform abstraction layer

The Flutter app builds three ways: **macOS desktop**, **Linux desktop**, and **web** (served by Nginx in the Docker deployment). Shared code under `lib/features/` and `lib/shared/` must compile for all three — which means **no `dart:io`, no `tray_manager`, no `window_manager`, no `local_notifier`, no `Process`, no `Platform.environment`, no `File` / `Directory` / `ProcessSignal`** in shared code.

Every OS-touching capability goes through `lib/core/platform/platform_services.dart`:

```
lib/core/platform/
├── platform_services.dart              # abstract interface + conditional export
├── platform_services_stub.dart         # noSuchMethod-forwarding fallback (compile time only)
├── platform_services_desktop.dart      # dart:io, tray, window, notifier, Process — selected when dart.library.io is available
├── platform_services_web.dart          # no-ops + `/api` base URL — selected when dart.library.html is available
└── platform_services_provider.dart     # Riverpod provider for the factory
```

The conditional `export` in `platform_services.dart` picks the right impl at compile time, so the web bundle never ships tray/window/notifier code. Shared code gets the implementation via `ref.watch(platformServicesProvider)` or `PlatformServices.create()`.

### Rules for adding code

1. **Any new capability that might touch the OS goes on `PlatformServices` first**, with implementations in both `platform_services_desktop.dart` and `platform_services_web.dart` (web is usually a no-op or the relative-URL variant). Add a matching field / capture to `test/core/platform/fake_platform_services.dart` so tests can observe it.
2. **Never import `dart:io`, `Process`, `Platform`, `File`, `Directory`, `ProcessSignal`, or the desktop-only plugins (`tray_manager`, `window_manager`, `local_notifier`, `file_picker`'s `getDirectoryPath`) from anywhere under `lib/features/` or `lib/shared/`.** These are only reachable from `lib/core/platform/platform_services_desktop.dart` and the handful of helpers it imports (`core/daemon/`, `core/setup/`, `core/tray/`).
3. **Run `make build-web` before committing** any change that touches shared code. It's the canary that catches dart:io leaks before they reach the Dockerfile build stage. The full gate is:
   ```bash
   cd flutter_app && flutter test && flutter analyze
   make build-web
   ```
4. **For UI bits that only make sense on one platform** (e.g., the "Browse for directory" button in the repo detail screen): gate the widget on `kIsWeb` from `package:flutter/foundation.dart` locally rather than adding another method to `PlatformServices`. Reserve the interface for capabilities, not for widget-tree conditionals.

### Full-repo analysis (Docker only)

The daemon can run the AI agent with a CWD set to a local directory (full-repo analysis vs diff-only review). On desktop this is automatic. In Docker the daemon is containerised, so the operator bind-mounts host repos via `HEIMDALLM_REPOS_DIR` in `docker/.env`; `docker-compose.yml` resolves it to `/repos:ro` inside the container. In the UI the path to enter is `/repos/<name>`, not the host path. Keep this in mind when writing user-visible copy about paths.

### Nginx + compose

The web service (`docker/docker-compose.yml`) builds from `flutter_app/Dockerfile.web` — a multi-stage image that produces the Flutter Web bundle inside `ghcr.io/cirruslabs/flutter:stable` and serves it from `nginx:1.27-alpine`. The entrypoint at `flutter_app/docker-entrypoint.d/10-heimdallm-token.sh` reads the daemon's API token from `/data/api_token` (shared volume) and injects it as `X-Heimdallm-Token` on every `/api/*` call. A stale `HEIMDALLM_API_TOKEN` in `docker/.env` used to shadow this behavior; that env var is no longer passed through. If you need to override the token for a test, use `docker compose -e HEIMDALLM_API_TOKEN=... up web`.

## Conventional commits

`release-please` reads commit prefixes to bump semver and generate the
changelog. Stick to: `feat:`, `fix:`, `refactor:`, `docs:`, `chore:`,
`ci:`, `test:`. A `feat!:` or trailer `BREAKING CHANGE:` forces a major
bump.

## Do not

- Run `go test` / `go build` on the host on a corporate laptop with EDR.
- Stage `.DS_Store` in commits.
- Push directly to `main` — always open a PR.
- Skip hooks (`--no-verify`) without an explicit user instruction.
- Invent a different Docker image/tag for tests — the pinned digest in the
  Makefile is the canonical one. Bump it deliberately, in its own commit,
  when upgrading Go.
