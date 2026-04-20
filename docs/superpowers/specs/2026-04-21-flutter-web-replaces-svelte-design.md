# Flutter Web replaces SvelteKit web_ui

**Date:** 2026-04-21
**Status:** Draft
**Scope:** Replace the SvelteKit `web_ui/` with a Flutter Web build of `flutter_app/`, served by Nginx behind a token-injecting reverse proxy.

---

## Motivation

Heimdallm ships two independent UI codebases:

| UI | Tech | Purpose |
|---|---|---|
| `flutter_app/` | Flutter (macOS/Linux) | Desktop app |
| `web_ui/` | SvelteKit + Node.js | Web dashboard for Docker deployments |

Both implement the same screens (dashboard, PR detail, issues, config, logs, agents) against the same daemon REST + SSE API. Every new feature must be built twice.

**Goal:** eliminate `web_ui/` by compiling `flutter_app/` for the web platform, unifying both UIs into one Dart codebase. The web target is internal-only (accessed when the daemon runs in Docker), so CanvasKit bundle size and SEO are non-concerns.

---

## Architecture

### Before

```
browser :3000 ──► SvelteKit (Node.js) ──proxy──► daemon :7842
                  reads /data/api_token
                  injects X-Heimdallm-Token
```

### After

```
browser :3000 ──► Nginx
                  ├─ /           → Flutter Web static assets (build/web/)
                  ├─ /api/*      → proxy_pass daemon :7842, injects token
                  └─ /events     → proxy_pass daemon :7842/events, injects token
```

- **Nginx** replaces both the SvelteKit server and the Node.js runtime.
- **Token injection** stays server-side: Nginx reads the token from env var (or file) and sets the `X-Heimdallm-Token` header on every proxied request. The browser never sees the token.
- **The daemon is unchanged** — no CORS, no new auth endpoints.

---

## Tasks

### Phase 1: Platform abstraction layer

Create a thin abstraction that isolates desktop-only code from the shared codebase.

#### 1.1 Create `lib/core/platform/` service layer

```
lib/core/platform/
  platform_services.dart          # abstract interface + conditional export
  platform_services_desktop.dart  # real: tray, window, notifier, PID, signals
  platform_services_web.dart      # no-op stubs
```

**Interface (abstract class `PlatformServices`):**
- `Future<void> initWindow()`
- `Future<void> initTray(ApiClient api)`
- `Future<void> initNotifications()`
- `Future<bool> ensureSingleInstance()`
- `void listenForActivationSignal()`
- `void sendNotification({String title, String body, int? prId})`
- `void onWindowClose()` / `void setPreventClose(bool)`
- `bool get isDesktop`

**Desktop implementation:** moves existing code from `main.dart` lines 20-156 and `core/tray/tray_menu.dart` into the desktop implementation.

**Web implementation:** all methods are no-ops or return immediately. `isDesktop` returns `false`.

**Conditional export** in `platform_services.dart`:
```dart
export 'platform_services_web.dart'
    if (dart.library.io) 'platform_services_desktop.dart';
```

#### 1.2 Adapt `ApiClient` for web

Currently `ApiClient` (`lib/core/api/api_client.dart`) reads the token from `~/.local/share/heimdallm/api_token` using `dart:io`. On web:

- **No token needed** — Nginx injects it server-side.
- **Base URL is relative** — `/api/` instead of `http://127.0.0.1:7842/`.

Changes:
- Extract token-loading and URI building into overridable methods or use conditional import.
- On web: `_uri(path)` returns `Uri.parse('/api$path')`, `_apiToken()` returns `null`.
- On desktop: unchanged (reads token from file, uses `localhost:7842`).

#### 1.3 Adapt `SseClient` for web

Same pattern as ApiClient:
- On web: connect to `/events` (relative), no token header.
- On desktop: unchanged (`http://127.0.0.1:7842/events` with file-based token).

#### 1.4 Adapt `main.dart` bootstrap

Current boot sequence (desktop):
1. `_ensureSingleInstance()` — PID file, SIGUSR1
2. `_listenForActivationSignal()` — process signals
3. `_setupWindow()` — window_manager
4. `_setupTray()` — tray_manager
5. `localNotifier.setup()` — native notifications
6. `_BootstrapApp._boot()` — detect token → write config → find daemon binary → spawn daemon → health check

**Web boot sequence:**
1. Health check (`GET /api/health` via Nginx) → success → go to `/`.
2. If unhealthy → show error screen ("daemon not reachable").

Steps 1-5 and daemon spawn are skipped entirely. The `_BootstrapApp` widget uses `PlatformServices.isDesktop` to choose the desktop vs web boot path.

#### 1.5 Remove `dart:io` from shared code

12 files import `dart:io`. After the platform layer:
- `main.dart` — uses PlatformServices, no direct `dart:io`.
- `api_client.dart` — conditional import for token/URI.
- `sse_client.dart` — conditional import for token/URI.
- `daemon_lifecycle.dart` — only imported on desktop (behind conditional).
- `first_run_setup.dart` — only imported on desktop (behind conditional).
- `gh_cli.dart` — only imported on desktop (behind conditional).
- `core/tray/tray_menu.dart` — moved into `platform_services_desktop.dart`.
- `config_providers.dart` — guard daemon-spawn code with `PlatformServices.isDesktop`.
- `config_screen.dart` — verify; may only need Platform for display logic.

### Phase 2: Enable Flutter Web platform

#### 2.1 Add web platform support

```bash
cd flutter_app && flutter create --platforms web .
```

This generates `web/index.html`, `web/manifest.json`, `web/favicon.png`.

#### 2.2 Customize `web/index.html`

- Set title to "Heimdallm".
- Set favicon/icons to Heimdallm branding (reuse `assets/icon.png`).
- Use CanvasKit renderer (default) — internal tool, no bundle-size concern.

#### 2.3 Verify `flutter build web --release` compiles

Gate: zero compilation errors. Run and verify the app loads in a browser against a local daemon.

### Phase 3: Nginx + Docker

#### 3.1 Create `flutter_app/nginx.conf.template`

```nginx
server {
    listen 3000;

    location / {
        root /usr/share/nginx/html;
        try_files $uri $uri/ /index.html;
    }

    location /api/ {
        proxy_pass http://heimdallm:${HEIMDALLM_PORT}/;
        proxy_set_header X-Heimdallm-Token "${HEIMDALLM_API_TOKEN}";
        proxy_set_header Host $host;
    }

    location /events {
        proxy_pass http://heimdallm:${HEIMDALLM_PORT}/events;
        proxy_set_header X-Heimdallm-Token "${HEIMDALLM_API_TOKEN}";
        proxy_http_version 1.1;
        proxy_set_header Connection "";
        proxy_buffering off;
        proxy_cache off;
        proxy_read_timeout 86400s;
    }
}
```

Token is injected via `envsubst` at container start.

#### 3.2 Create `flutter_app/Dockerfile.web`

```dockerfile
# ── Build stage ──────────────────────────────────────────────────────
FROM ghcr.io/cirruslabs/flutter:stable AS build
WORKDIR /app
COPY . .
RUN flutter build web --release

# ── Runtime stage ────────────────────────────────────────────────────
FROM nginx:alpine AS runtime
COPY --from=build /app/build/web /usr/share/nginx/html
COPY nginx.conf.template /etc/nginx/templates/default.conf.template

# envsubst in the official nginx:alpine image reads *.template from
# /etc/nginx/templates/ and writes the resolved output to
# /etc/nginx/conf.d/ at container start — no custom entrypoint needed.

ENV HEIMDALLM_API_TOKEN=""
ENV HEIMDALLM_PORT="7842"

EXPOSE 3000
```

Entrypoint reads `/data/api_token` as fallback if `HEIMDALLM_API_TOKEN` is empty. Add a small `docker-entrypoint.d/` script:

```sh
#!/bin/sh
# 10-load-token.sh — read token from shared volume if not set via env
if [ -z "$HEIMDALLM_API_TOKEN" ] && [ -f /data/api_token ]; then
  export HEIMDALLM_API_TOKEN=$(cat /data/api_token)
fi
```

Result: image ~30 MB (vs ~180 MB for the Node.js image).

#### 3.3 Update `docker/docker-compose.yml`

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
    - HEIMDALLM_API_TOKEN=${HEIMDALLM_API_TOKEN:-}
    - HEIMDALLM_PORT=${HEIMDALLM_PORT:-7842}
  volumes:
    - heimdallm-data:/data:ro
  depends_on:
    heimdallm:
      condition: service_healthy
  healthcheck:
    test: ["CMD", "wget", "-qO-", "http://localhost:3000/"]
    interval: 30s
    timeout: 5s
    start_period: 10s
    retries: 3
```

Healthcheck switches from Node.js script to `wget` (available in nginx:alpine).

### Phase 4: Cleanup

#### 4.1 Delete `web_ui/` directory

Remove entirely: `web_ui/`, including its `Dockerfile`, `package.json`, `src/`, `svelte.config.js`, etc.

#### 4.2 Update CI workflows

- If any workflow references `web_ui/`, update or remove those steps.
- Add `flutter build web` validation to PR checks (optional, can be a follow-up).

#### 4.3 Update `docs/index.html` (landing page)

No change — the static landing page in `docs/` is independent of the web dashboard.

---

## Files created

| File | Purpose |
|---|---|
| `lib/core/platform/platform_services.dart` | Abstract interface + conditional export |
| `lib/core/platform/platform_services_desktop.dart` | Desktop implementation (tray, window, notifier, PID) |
| `lib/core/platform/platform_services_web.dart` | Web no-op stubs |
| `flutter_app/web/` (directory) | Flutter web platform (generated by `flutter create`) |
| `flutter_app/nginx.conf.template` | Nginx config with token injection |
| `flutter_app/Dockerfile.web` | Multi-stage: Flutter build + nginx:alpine |

## Files modified

| File | Change |
|---|---|
| `lib/main.dart` | Use PlatformServices; split desktop/web boot path |
| `lib/core/api/api_client.dart` | Conditional import for token/URI (web: relative, no token) |
| `lib/core/api/sse_client.dart` | Conditional import for token/URI (web: relative, no token) |
| `lib/core/daemon/daemon_lifecycle.dart` | Only imported on desktop |
| `lib/core/setup/first_run_setup.dart` | Only imported on desktop |
| `lib/core/setup/gh_cli.dart` | Only imported on desktop |
| `lib/features/config/config_providers.dart` | Guard daemon-spawn with isDesktop |
| `lib/features/config/config_screen.dart` | Guard any Platform checks |
| `docker/docker-compose.yml` | Replace web service (SvelteKit → Nginx + Flutter) |
| `pubspec.yaml` | No dep changes needed (desktop deps are platform-filtered by Flutter) |

## Files deleted

| File/Directory | Reason |
|---|---|
| `web_ui/` (entire directory) | Replaced by Flutter Web |

---

## Out of scope

- Modifying the daemon (Go) — no CORS, no auth changes.
- The static landing page (`docs/index.html`) — unrelated.
- Adding CI for Flutter Web builds — can be a follow-up issue.
- Theme/dark-mode parity with SvelteKit — Flutter app already has dark theme support.
