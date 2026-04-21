# Flutter Web replaces SvelteKit web_ui

**Issue:** [#125](https://github.com/theburrowhub/heimdallm/issues/125)
**Date:** 2026-04-21
**Status:** Design — ready for plan

## Problem

Today Heimdallm ships two frontends: a Flutter desktop app under `flutter_app/` and a SvelteKit web app under `web_ui/`. Every UI change has to be coded twice, drifts between the two are routine, and the Node runtime in `web_ui/` adds ~200 MB to every Docker deploy. The web target is internal-only (Docker), so the historical justifications for SvelteKit — SEO, bundle size, SSR — don't apply here.

## Goal

Build `flutter_app/` as Flutter Web and serve the static bundle from Nginx. Nginx also terminates the `X-Heimdallm-Token` authentication on the server side and proxies `/api/*` + `/events` to the daemon unchanged. Delete `web_ui/` entirely. No daemon changes.

```
Before: browser → SvelteKit (Node, proxy + SSR) → daemon
After:  browser → Nginx (static + proxy + token)  → daemon
```

## Non-goals

- Daemon (Go) changes — no CORS, no new auth, no new endpoints.
- CI workflows for Flutter Web builds — follow-up issue.
- Mobile targets (Android/iOS) — out of scope, not requested.
- A browser UX for entering the token manually — the token lives in the Docker volume, not in the browser.
- Touching the `docs/index.html` landing page.

## Architecture

### Platform abstraction layer

All desktop-only behavior moves behind a `PlatformServices` interface, selected at compile time via conditional imports. Shared code (features, routers, ApiClient, SseClient) never imports `dart:io`, never references `tray_manager`/`window_manager`/`local_notifier`, never calls `Process`/`File`/`ProcessSignal`. Those symbols exist only inside the desktop implementation and are tree-shaken out of the web bundle.

```
lib/core/platform/
├── platform_services.dart               # abstract interface + factory export
├── platform_services_stub.dart          # compile-fail stub; never used at runtime
├── platform_services_desktop.dart       # dart:io, tray, window, notifier, Process
└── platform_services_web.dart           # no-ops / browser equivalents
```

`platform_services.dart` re-exports the right implementation:

```dart
export 'platform_services_stub.dart'
    if (dart.library.io) 'platform_services_desktop.dart'
    if (dart.library.html) 'platform_services_web.dart';
```

The interface covers exactly what `main.dart` and the shared code actually need today:

| Capability | Desktop | Web |
|---|---|---|
| `ensureSingleInstance()` | PID file + SIGUSR1 kill/signal existing | no-op |
| `listenForActivationSignal(onActivate)` | registers SIGUSR1 handler | no-op |
| `setupWindow(title, size)` | window_manager init + show + focus | no-op |
| `setupTray(icon, menu)` | tray_manager init | no-op |
| `setupNotifier()` | local_notifier init | no-op |
| `showNotification(title, body)` | local_notifier | `dart:html.Notification` if permission else no-op |
| `ensureDaemonRunning(config)` | health-check loop + `Process.start` if needed | no-op (daemon is the neighboring container) |
| `apiBaseUrl()` | `http://127.0.0.1:7842` | `/api` (relative; browser origin + Nginx `/api/*` location) |
| `apiToken()` | reads `~/.local/share/heimdallm/api_token` | `null` (Nginx injects) |
| `ssePath()` | _not needed_ — `SseClient` takes its path (`/events`, `/logs/stream`) from its constructor and prepends `apiBaseUrl`. On web that naturally resolves to `/api/events`, `/api/logs/stream` without any extra routing. |
| `quit()` | `exit(0)` | no-op (browser tab close) |

Features consume `PlatformServices` via a Riverpod provider. Tests override the provider with a fake.

### ApiClient / SseClient

Both already use `package:http`, so the code changes are minimal:

- Take `apiBaseUrl` and `apiToken` from `PlatformServices` instead of hardcoding `http://127.0.0.1:7842` and reading the file via `dart:io`.
- URL building becomes `Uri.parse('$apiBaseUrl$path')`, where `path` is daemon-relative (e.g., `/health`, `/prs`).
  - Desktop: `apiBaseUrl = "http://127.0.0.1:7842"` → final URL `http://127.0.0.1:7842/health` (unchanged from today).
  - Web: `apiBaseUrl = "/api"` → final URL `/api/health` (resolved against the browser origin); Nginx's `/api/` location strips the prefix before forwarding to the daemon.
- SSE follows the same rule. `SseClient` takes its path from the constructor (the dashboard uses `/events`, the logs screen uses `/logs/stream`); on web both naturally become `/api/events` and `/api/logs/stream`. Nginx's `/api/` location strips the prefix and forwards to the daemon. This is a small deviation from the SvelteKit setup (which had a dedicated `/events` route) but it means one Nginx location covers every daemon path, and the `/logs/stream` SSE endpoint — which the SvelteKit app only reaches through its `/api/[...path]` catch-all anyway — works the same way.
- On web, `apiToken` is `null` → header omitted by the client; Nginx injects it.
- On desktop, the behavior is byte-for-byte identical to today.

The file-based token read that currently lives inside `ApiClient` / `SseClient` moves into `platform_services_desktop.dart`. Shared code no longer touches `dart:io`.

### main.dart bootstrap split

```dart
Future<void> main() async {
  WidgetsFlutterBinding.ensureInitialized();
  final platform = await PlatformServices.create();
  await platform.ensureSingleInstance();
  platform.listenForActivationSignal(_onActivate);
  await platform.setupWindow(title: 'Heimdallm', size: Size(1200, 800));
  await platform.setupTray(iconPath: 'assets/tray.png', menu: _trayMenu());
  await platform.setupNotifier();
  await platform.ensureDaemonRunning(bootConfig);
  runApp(ProviderScope(
    overrides: [platformServicesProvider.overrideWithValue(platform)],
    child: const HeimdallmApp(),
  ));
}
```

On web, every `platform.*` call above is a no-op; only `runApp` actually runs.

### Nginx + Docker

`flutter_app/Dockerfile.web` — multi-stage:

1. **Build stage:** `ghcr.io/cirruslabs/flutter:stable` (or equivalent pinned by digest), `flutter pub get`, `flutter build web --release --base-href="/"`.
2. **Runtime stage:** `nginx:alpine` (pinned by SHA256 digest, consistent with Makefile policy), copies `build/web/` into `/usr/share/nginx/html`, copies `nginx.conf.template` and `docker-entrypoint.d/` scripts.

`flutter_app/nginx.conf.template`:

```nginx
server {
  listen 3000;
  server_name _;

  root /usr/share/nginx/html;
  index index.html;

  # SPA deep-link fallback (go_router path-based URLs)
  location / {
    try_files $uri $uri/ /index.html;
  }

  # Single proxy for all daemon traffic (HTTP + SSE).
  # proxy_buffering off is mandatory for SSE (/events, /logs/stream); the tiny
  # cost on regular GETs is negligible.
  location /api/ {
    proxy_pass ${DAEMON_URL}/;
    proxy_set_header X-Heimdallm-Token "${API_TOKEN}";
    proxy_set_header Host $host;
    proxy_http_version 1.1;
    proxy_buffering off;
    proxy_cache off;
    proxy_read_timeout 24h;
    chunked_transfer_encoding on;
  }

  # Healthcheck — cheap static endpoint
  location = /healthz {
    access_log off;
    return 200 "ok\n";
  }
}
```

`docker-entrypoint.d/10-heimdallm-token.sh` — resolves the token once at container start, matching SvelteKit's order:

```sh
#!/bin/sh
set -eu

: "${DAEMON_URL:=http://heimdallm:7842}"
TOKEN_FILE="${HEIMDALLM_API_TOKEN_FILE:-/data/api_token}"

if [ -n "${HEIMDALLM_API_TOKEN:-}" ]; then
  API_TOKEN="$HEIMDALLM_API_TOKEN"
elif [ -r "$TOKEN_FILE" ]; then
  API_TOKEN="$(tr -d '\n\r' < "$TOKEN_FILE")"
else
  echo "heimdallm-web: no token found — /api and /events will 502" >&2
  API_TOKEN=""
fi

export API_TOKEN DAEMON_URL
envsubst '${API_TOKEN} ${DAEMON_URL}' \
  < /etc/nginx/templates/heimdallm.conf.template \
  > /etc/nginx/conf.d/default.conf
```

The official `nginx:alpine` image already supports `/docker-entrypoint.d/*.sh`. The template lives under `/etc/nginx/templates/` (convention, not the auto-render path, because we want the script to decide precedence). The rendered file lands in `/etc/nginx/conf.d/default.conf` before Nginx starts.

### docker-compose.yml changes

Replace the `web` service:

```yaml
web:
  build:
    context: ../flutter_app
    dockerfile: Dockerfile.web
  container_name: heimdallm-web
  restart: unless-stopped
  ports:
    - "${HEIMDALLM_WEB_PORT:-3000}:3000"
  environment:
    - DAEMON_URL=http://heimdallm:${HEIMDALLM_PORT:-7842}
    - HEIMDALLM_API_TOKEN=${HEIMDALLM_API_TOKEN:-}
  volumes:
    - heimdallm-data:/data:ro
  depends_on:
    heimdallm:
      condition: service_healthy
  healthcheck:
    test: ["CMD", "wget", "-qO-", "http://localhost:3000/healthz"]
    interval: 30s
    timeout: 5s
    start_period: 10s
    retries: 3
```

Everything else (volumes, daemon service, network) stays exactly as today. The host port mapping (`3000:3000`) is unchanged so existing bookmarks and docs keep working.

## Testing strategy

**Rigid TDD applies.** Each code change lands after a failing test that documents the behavior.

### Unit tests — Flutter

- `test/core/platform/platform_services_factory_test.dart` — on the VM (desktop) the factory returns `DesktopPlatformServices`; injecting a `FakePlatformServices` via Riverpod works.
- `test/core/platform/fake_platform_services.dart` — a test double exposing captured calls (`setupWindow`, `showNotification`, etc.) so feature tests can assert platform interactions.
- `test/core/api/api_client_test.dart` — with a fake `PlatformServices.apiBaseUrl = ""` and `apiToken = null`, the client sends relative URLs with no token header; with desktop values it sends the current absolute URL + token. Add cases for both.
- `test/core/api/sse_client_test.dart` — same split: relative `/events`, no token header when on web.
- Existing feature widget tests continue to work because they already inject fake API clients; we wire them to take a fake `PlatformServices` where they currently bypass it.

### Build-level test

- `flutter build web --release` compiles with zero errors. This is the canary that proves no `dart:io` leaked into shared code. Added as a Make target `make build-web` and exercised manually before merge (CI comes in the follow-up issue).

### Integration tests — Docker

- `docker/scripts/test-web.sh` — new script. Spins up the test compose file, waits for `heimdallm-web` to be healthy, then curls through the Nginx:
  - `GET /` → 200 + `Content-Type: text/html` (Flutter shell).
  - `GET /main.dart.js` → 200 + `application/javascript`.
  - `GET /api/health` → 200 (daemon health through the proxy, confirms token injection works).
  - `GET /api/events` with `Accept: text/event-stream` → 200 + `Content-Type: text/event-stream` within 2 s.
  - `GET /prs/123` (unknown path) → 200 + HTML (SPA fallback works).
  - `GET /healthz` → 200 `ok`.
- Extends `docker-compose.test.yml` with an override for the new `web` service so the script can target it in isolation.

### Desktop regression

- `make test-docker` (Go daemon) — must stay green.
- `cd flutter_app && flutter test` — must stay green.
- Manual smoke (`flutter run -d macos`): tray icon appears, window opens, single-instance relaunch focuses existing window, notifications work, daemon auto-launches.

## Migration phases (commits on the branch)

The branch is `feat/flutter-web-replaces-svelte`. One PR, but structured as phase-shaped commits so review is digestible.

1. **Platform abstraction layer** — interface + stub + desktop + web impls; refactor `ApiClient`, `SseClient`, `main.dart`, `first_run_setup`, `gh_cli`, `daemon_lifecycle`, `tray_menu`, `config_providers`, `config_screen` to consume `PlatformServices`. Tests land with the code. At this point `flutter test` passes and desktop still runs.
2. **Enable Flutter Web** — `flutter create --platforms web .`, customize `web/index.html` (Heimdallm title, favicon from `web_ui/static/`), verify `flutter build web --release` succeeds. Commit the `web/` directory.
3. **Nginx + Docker** — add `flutter_app/Dockerfile.web`, `flutter_app/nginx.conf.template`, `flutter_app/docker-entrypoint.d/10-heimdallm-token.sh`, swap the `web` service in `docker/docker-compose.yml`, add `docker/scripts/test-web.sh`, mirror the override in `docker-compose.test.yml`.
4. **Cleanup** — `git rm -r web_ui/`, remove any references to `web_ui/` in the Makefile, docs, scripts, `.dockerignore`, and release configs.

## Files touched (concrete list)

**New:**
- `flutter_app/lib/core/platform/platform_services.dart`
- `flutter_app/lib/core/platform/platform_services_stub.dart`
- `flutter_app/lib/core/platform/platform_services_desktop.dart`
- `flutter_app/lib/core/platform/platform_services_web.dart`
- `flutter_app/lib/core/platform/platform_services_provider.dart`
- `flutter_app/test/core/platform/platform_services_factory_test.dart`
- `flutter_app/test/core/platform/fake_platform_services.dart`
- `flutter_app/web/` (generated)
- `flutter_app/Dockerfile.web`
- `flutter_app/nginx.conf.template`
- `flutter_app/docker-entrypoint.d/10-heimdallm-token.sh`
- `docker/scripts/test-web.sh`

**Modified:**
- `flutter_app/lib/main.dart`
- `flutter_app/lib/core/api/api_client.dart`
- `flutter_app/lib/core/api/sse_client.dart`
- `flutter_app/lib/core/setup/first_run_setup.dart`
- `flutter_app/lib/core/setup/gh_cli.dart`
- `flutter_app/lib/core/daemon/daemon_lifecycle.dart`
- `flutter_app/lib/core/tray/tray_menu.dart`
- `flutter_app/lib/features/config/config_providers.dart`
- `flutter_app/lib/features/config/config_screen.dart`
- `flutter_app/pubspec.yaml` (no new runtime deps; `tray_manager`/`window_manager`/`local_notifier` stay — Flutter Web strips them when unused via conditional imports)
- `flutter_app/test/core/api/api_client_test.dart`
- `flutter_app/test/core/api/sse_client_test.dart`
- `docker/docker-compose.yml`
- `docker/docker-compose.test.yml`
- `Makefile` (add `build-web` target, remove any `web_ui/` refs)
- `README.md` and `LLM-HOW-TO-INSTALL.md` (only if they mention `web_ui/`)

**Deleted:**
- `web_ui/` (entire directory)

## Acceptance criteria

- `docker compose up` in `docker/` starts `heimdallm` and `heimdallm-web`, both healthchecks go green within 60 s.
- All existing routes load through `http://localhost:3000`: `/`, `/prs`, `/prs/:id`, `/issues`, `/issues/:id`, `/config`, `/logs`, `/agents`, `/repositories/:id`.
- `/events` streams live — creating a PR review on the daemon side updates the PR list within 2 s on the browser.
- `flutter run -d macos` still launches the desktop app with tray + window + notifier + single-instance semantics intact.
- `web_ui/` no longer exists in the tree.
- `docker image inspect heimdallm-web --format '{{.Size}}'` reports ≤ 50 MB.
- `cd flutter_app && flutter test` green.
- `make test-docker` green.
- `docker/scripts/test-web.sh` green.

## Risks and mitigations

| Risk | Mitigation |
|---|---|
| A `dart:io` import sneaks into shared code and `flutter build web` breaks late. | `make build-web` is the canary, run at the end of phase 1 and again after every subsequent change. Mandatory green before merge. |
| Nginx SSE buffering closes the stream early. | `proxy_buffering off` + `proxy_read_timeout 24h` documented in the template; integration test asserts `Content-Type: text/event-stream` arrives within 2 s and stays open. |
| Flutter Web bundle exceeds 50 MB with CanvasKit. | CanvasKit renderer ships at ~25 MB; total image with Alpine Nginx is ~30–35 MB empirically. If we overshoot, switch to HTML renderer (`--web-renderer html`) in the Dockerfile. |
| Deep links (`/prs/123`) 404 on refresh. | `try_files $uri $uri/ /index.html;` in the Nginx location block, exercised by integration test. |
| Token file isn't readable at Nginx start (mount race). | The web service `depends_on` the daemon with `condition: service_healthy`; the daemon always writes the token before it reports healthy. The entrypoint script also logs a clear warning if the token is missing. |
| Desktop single-instance logic regresses (SIGUSR1 on Linux, different on macOS). | `DesktopPlatformServices` preserves current behavior byte-for-byte; the refactor is a pure move, and the existing manual smoke check in the acceptance criteria catches regressions. |
| A desktop-only pubspec dep (`tray_manager`, `window_manager`, `local_notifier`) fails to compile for web even when shared code never imports it. | Verified empirically at the end of phase 1 via `make build-web`. If a package's native plugin code breaks the web build, the mitigation is to declare it as a desktop-only conditional dep using `flutter:` `platforms:` metadata, or move the impl behind a thin wrapper that itself lives only in the desktop conditional import. No known case for these three packages at their current versions — they declare no web plugin platform, so `flutter build web` simply excludes them. |

## Open questions

None at design time. Implementation questions will surface during the plan phase.
