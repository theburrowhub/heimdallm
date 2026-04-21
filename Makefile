# ── Platform detection ─────────────────────────────────────────────────────────
OS := $(shell uname -s)

DAEMON_BIN  := $(shell pwd)/daemon/bin/heimdallm

ifeq ($(OS),Darwin)
  FLUTTER_DEVICE   := macos
  FLUTTER_BUILD    := flutter_app/build/macos/Build/Products
  APP_BUNDLE       := $(FLUTTER_BUILD)/Release/Heimdallm.app
  # Detect local Developer ID Application certificate automatically
  SIGNING_IDENTITY ?= $(shell security find-identity -v -p codesigning 2>/dev/null \
	| grep "Developer ID Application" | head -1 | sed 's/.*"\(.*\)".*/\1/')
else
  FLUTTER_DEVICE   := linux
  FLUTTER_BUILD    := flutter_app/build/linux/x64/release
  APP_BUNDLE       := $(FLUTTER_BUILD)/bundle
endif

.PHONY: build-daemon build-app build-web test test-docker dev dev-daemon dev-stop \
        release-local package-macos install-service verify-linux run-linux \
        setup up up-build up-daemon up-build-daemon down logs logs-daemon \
        ps restart clean _check-docker _check-env _post-up-hints

# ── Build ─────────────────────────────────────────────────────────────────────

build-daemon:
	cd daemon && make build

build-app:
	cd flutter_app && flutter build $(FLUTTER_DEVICE) --release

# Flutter Web bundle, consumed by docker/Dockerfile.web (served via Nginx).
# --base-href=/ matches the Nginx server block that expects assets at the root.
build-web:
	cd flutter_app && flutter build web --release --base-href=/

# ── Test ──────────────────────────────────────────────────────────────────────

test:
	cd daemon && make test
	cd flutter_app && flutter test

# ── Sandboxed Go tests (EDR-safe) ─────────────────────────────────────────────
#
# Runs `go vet` + `go test` for the daemon inside an official Go container,
# so corporate EDR agents (Elastic Security, CrowdStrike, SentinelOne, …)
# never see ephemeral *.test binaries appearing in /var/folders/.../go-build/.
#
# Hardening (share with IT/Security if asked):
#   - Image pinned by SHA256 digest (GO_DOCKER_IMAGE below)
#   - Repo mounted READ-ONLY (:ro) — container cannot modify sources
#   - Go cache redirected to /tmp/heimdallm-gocache on the host
#     (never touches ~/.cache/go-build or ~/go/pkg/mod)
#   - Runs as the invoking user (--user $(id -u):$(id -g)), no root in container
#   - --rm → container and its tmpfs are destroyed on exit
#   - No ports exposed, no host env vars forwarded
#
# AI AGENTS: this is the default for Go tests on this repo. Do not run
# `go test` directly on the host unless explicitly asked. See AGENTS.md.
#
# Usage:
#   make test-docker
#   make test-docker GO_TEST_ARGS="-run TestFoo ./internal/config/..."

GO_DOCKER_IMAGE ?= golang:1.21-alpine@sha256:2414035b086e3c42b99654c8b26e6f5b1b1598080d65fd03c7f499552ff4dc94
GO_TEST_ARGS    ?= -timeout 60s -count=1 ./...

test-docker:
	@command -v docker >/dev/null || { echo "❌  Docker is required. Install from https://docs.docker.com/get-docker/"; exit 1; }
	@mkdir -p /tmp/heimdallm-gocache /tmp/heimdallm-home
	@echo "▶  Running Go vet + tests inside $(GO_DOCKER_IMAGE)"
	docker run --rm \
	  --user "$(shell id -u):$(shell id -g)" \
	  -v "$(shell pwd):/src:ro" \
	  -v "/tmp/heimdallm-gocache:/tmp/.cache" \
	  -v "/tmp/heimdallm-home:/tmp/home" \
	  -w /src/daemon \
	  -e HOME=/tmp/home \
	  -e GOCACHE=/tmp/.cache/go-build \
	  -e GOMODCACHE=/tmp/.cache/gomod \
	  $(GO_DOCKER_IMAGE) \
	  sh -c "go vet ./... && go test $(GO_TEST_ARGS)"

# ── Local development ─────────────────────────────────────────────────────────
#
# make dev         — build daemon + run Flutter in debug mode
# make dev-daemon  — run daemon only (for API debugging)
# make dev-stop    — stop the running daemon

dev: build-daemon dev-stop
	@echo "▶  Lanzando Heimdallm..."
	cd flutter_app && HEIMDALLM_DAEMON_PATH=$(DAEMON_BIN) flutter run -d $(FLUTTER_DEVICE)

dev-daemon: build-daemon dev-stop
	@echo "▶  Daemon en http://localhost:7842 (Ctrl-C para parar)"
	GITHUB_TOKEN="$${GITHUB_TOKEN}" $(DAEMON_BIN)

dev-stop:
	@pkill -f "$(DAEMON_BIN)" 2>/dev/null && echo "↓  Daemon parado" || true
	@UI_PID_FILE="$$HOME/.local/share/heimdallm/ui.pid"; \
	 if [ -f "$$UI_PID_FILE" ]; then \
	   UI_PID=$$(cat "$$UI_PID_FILE"); \
	   kill "$$UI_PID" 2>/dev/null && echo "↓  UI parada (PID $$UI_PID)" || true; \
	   rm -f "$$UI_PID_FILE"; \
	 fi

# ── Local release (macOS only: sign + notarize + DMG + GitHub release) ───────
#
# Builds a fully signed, notarized .dmg and creates a GitHub release.
# Uses the Developer ID Application certificate from your local Keychain.
#
# Usage:
#   make release-local                    # auto-detect next semver from git log
#   make release-local VERSION=v1.2.3     # explicit version
#
# Prerequisites:
#   - Apple Developer Program membership
#   - Developer ID Application certificate installed in Keychain
#   - App-specific password stored in Keychain:
#       xcrun notarytool store-credentials "heimdallm-notary" \
#         --apple-id YOUR@EMAIL.COM --team-id TEAMID --password APP_SPECIFIC_PWD
#   - gh CLI authenticated: gh auth login

VERSION ?= $(shell \
	LAST=$$(git describe --tags --abbrev=0 2>/dev/null || echo "v0.0.0"); \
	VER=$${LAST\#v}; \
	MAJ=$$(echo $$VER | cut -d. -f1); \
	MIN=$$(echo $$VER | cut -d. -f2); \
	PAT=$$(echo $$VER | cut -d. -f3); \
	echo "v$$MAJ.$$MIN.$$((PAT+1))")

release-local: _check-macos _check-signing _check-gh build-daemon
	@echo ""
	@echo "╔══════════════════════════════════════════════╗"
	@echo "║  Heimdallm local release                     ║"
	@echo "╠══════════════════════════════════════════════╣"
	@echo "║  Version  : $(VERSION)"
	@echo "║  Identity : $(SIGNING_IDENTITY)"
	@echo "╚══════════════════════════════════════════════╝"
	@echo ""

	# ── 1. Flutter release build (Xcode uses your local signing config) ───────
	@echo "▶  Building Flutter app (release)..."
	cd flutter_app && flutter build macos --release \
	  --build-name="$$(echo $(VERSION) | sed 's/^v//')" \
	  --build-number="$$(date +%Y%m%d%H%M)"

	# ── 2. Embed daemon inside .app ───────────────────────────────────────────
	@echo "▶  Embedding daemon..."
	cp $(DAEMON_BIN) "$(APP_BUNDLE)/Contents/MacOS/heimdalld"
	chmod +x "$(APP_BUNDLE)/Contents/MacOS/heimdalld"

	# ── 3. Sign daemon binary with Developer ID ───────────────────────────────
	@echo "▶  Signing daemon binary..."
	codesign --force --options runtime \
	  --sign "$(SIGNING_IDENTITY)" \
	  "$(APP_BUNDLE)/Contents/MacOS/heimdalld"

	# ── 4. Re-sign the full .app bundle ──────────────────────────────────────
	@echo "▶  Signing .app bundle..."
	codesign --force --deep --options runtime \
	  --sign "$(SIGNING_IDENTITY)" \
	  "$(APP_BUNDLE)"

	@echo "▶  Verifying signature..."
	codesign --verify --verbose=2 "$(APP_BUNDLE)"
	spctl --assess --verbose "$(APP_BUNDLE)" 2>&1 || \
	  echo "⚠  spctl warning (expected before notarization)"

	# ── 5. Create DMG with create-dmg (nice installer UI) ────────────────────
	@echo "▶  Creating DMG..."
	@command -v create-dmg >/dev/null || (echo "Installing create-dmg..."; brew install create-dmg)
	mkdir -p dist
	$(eval DMG_ARGS := \
	  --volname "Heimdallm $(VERSION)" \
	  --window-pos 200 120 --window-size 660 400 \
	  --icon-size 128 \
	  --icon "Heimdallm.app" 165 185 \
	  --hide-extension "Heimdallm.app" \
	  --app-drop-link 495 185)
	$(if $(wildcard assets/dmg-background.png), \
	  $(eval DMG_ARGS := --background assets/dmg-background.png $(DMG_ARGS)))
	create-dmg $(DMG_ARGS) "dist/Heimdallm-$(VERSION).dmg" "$(APP_BUNDLE)"

	# ── 6. Notarize DMG ───────────────────────────────────────────────────────
	@echo "▶  Submitting for notarization (this takes a few minutes)..."
	xcrun notarytool submit "dist/Heimdallm-$(VERSION).dmg" \
	  --keychain-profile "heimdallm-notary" \
	  --wait
	xcrun stapler staple "dist/Heimdallm-$(VERSION).dmg"
	@echo "✓  Notarization complete"

	# ── 7. Create git tag ─────────────────────────────────────────────────────
	@echo "▶  Creating tag $(VERSION)..."
	git tag -a "$(VERSION)" -m "Release $(VERSION)"
	git push origin "$(VERSION)"

	# ── 8. Publish GitHub release ─────────────────────────────────────────────
	@echo "▶  Publishing GitHub release..."
	gh release create "$(VERSION)" \
	  "dist/Heimdallm-$(VERSION).dmg" \
	  --title "Heimdallm $(VERSION)" \
	  --generate-notes \
	  --verify-tag
	@echo ""
	@echo "✅  Released Heimdallm $(VERSION)"
	@echo "    https://github.com/$$(gh repo view --json nameWithOwner -q .nameWithOwner)/releases/tag/$(VERSION)"

# ── Guards ────────────────────────────────────────────────────────────────────

_check-macos:
	@if [ "$$(uname -s)" != "Darwin" ]; then \
	  echo "❌  This target requires macOS."; \
	  exit 1; \
	fi

_check-signing:
	@if [ -z "$(SIGNING_IDENTITY)" ]; then \
	  echo ""; \
	  echo "❌  No Developer ID Application certificate found in Keychain."; \
	  echo ""; \
	  echo "    Install your certificate from:"; \
	  echo "    https://developer.apple.com/account/resources/certificates/list"; \
	  echo ""; \
	  exit 1; \
	fi
	@echo "✓  Signing identity: $(SIGNING_IDENTITY)"

_check-gh:
	@if ! gh auth status >/dev/null 2>&1; then \
	  echo "❌  gh CLI not authenticated. Run: gh auth login"; \
	  exit 1; \
	fi
	@echo "✓  gh CLI authenticated"

# ── Install LaunchAgent service (macOS) ───────────────────────────────────────

install-service: build-daemon
	$(DAEMON_BIN) install

# ── Docker compose setup (seed HEIMDALLM_API_TOKEN) ───────────────────────────
#
# The web service reads /data/api_token from the shared volume at startup, so
# most of the time no env var is needed. This target is the escape hatch: it
# pulls the token out of the running daemon container and writes it into
# docker/.env so other tooling (scripts, CI, local curl) can reuse the same
# value without digging into the volume.
#
# Usage:
#   make up-daemon && make setup
#
# Replaces any existing HEIMDALLM_API_TOKEN line rather than appending, so
# rerunning the target after a daemon reset does not leave stale duplicates.

COMPOSE_FILE := docker/docker-compose.yml
DOCKER_ENV   := docker/.env

# The setup recipe writes the token into an mktemp'd file inside docker/
# (same filesystem as the target) so the final `mv` is atomic, and chmod
# 600's it before writing so an interrupted run leaves no world-readable
# copy on disk. The trap cleans the temp up on any early exit.
setup:
	@command -v docker >/dev/null || { echo "❌  Docker is required."; exit 1; }
	@test -f $(DOCKER_ENV) || { echo "❌  $(DOCKER_ENV) missing — copy docker/.env.example first."; exit 1; }
	@docker compose -f $(COMPOSE_FILE) ps --status running --services 2>/dev/null | grep -q '^heimdallm$$' \
	  || { echo "❌  heimdallm container is not running. Start it with:"; \
	       echo "     make up-daemon"; exit 1; }
	@TOKEN=$$(docker compose -f $(COMPOSE_FILE) exec -T heimdallm cat /data/api_token 2>/dev/null | tr -d '\r\n'); \
	 if [ -z "$$TOKEN" ]; then \
	   echo "❌  /data/api_token is empty — wait for the daemon's first full startup and retry."; \
	   exit 1; \
	 fi; \
	 TMP=$$(mktemp "$(DOCKER_ENV).XXXXXX"); \
	 trap 'rm -f "$$TMP"' EXIT; \
	 chmod 600 "$$TMP"; \
	 grep -v '^HEIMDALLM_API_TOKEN=' $(DOCKER_ENV) > "$$TMP" || true; \
	 printf 'HEIMDALLM_API_TOKEN=%s\n' "$$TOKEN" >> "$$TMP"; \
	 mv "$$TMP" $(DOCKER_ENV); \
	 trap - EXIT; \
	 echo "✓  HEIMDALLM_API_TOKEN written to $(DOCKER_ENV)"

# ── Docker compose wrappers ───────────────────────────────────────────────────
#
# Thin shortcuts around `docker compose -f $(COMPOSE_FILE)`. They exist so the
# README can point newcomers at `make up` / `make logs` / `make down` instead
# of the longer compose invocation — and so the invocation stays in one place
# if the compose path changes.
#
# `up` brings both services (daemon + web) online. `up-daemon` is the escape
# hatch for operators who do not want the web UI.
#
# `make up` also validates `docker/.env` exists — the most common first-run
# mistake is forgetting to copy `.env.example`.

_check-docker:
	@command -v docker >/dev/null || { echo "❌  Docker is required. Install from https://docs.docker.com/get-docker/"; exit 1; }

_check-env: _check-docker
	@test -f $(DOCKER_ENV) || { \
	  echo "❌  $(DOCKER_ENV) missing."; \
	  echo "    Copy the template and fill in GITHUB_TOKEN + your AI API key:"; \
	  echo "    cp docker/.env.example $(DOCKER_ENV)"; \
	  exit 1; \
	}

# Prints a short post-up summary: the web URL, which AI keys are set, and
# hints for the opt-in knobs operators most often miss (full-repo analysis,
# topic discovery, issue tracking). Called after `up` / `up-build`.
_post-up-hints:
	@echo ""
	@echo "✅  Heimdallm is up at http://localhost:$${HEIMDALLM_WEB_PORT:-3000}"
	@echo "    Daemon API at http://localhost:$${HEIMDALLM_PORT:-7842} (token in /data/api_token)"
	@echo ""
	@set -a; . $(DOCKER_ENV); set +a; \
	 _set=; _unset=; \
	 for k in ANTHROPIC_API_KEY CLAUDE_CODE_OAUTH_TOKEN OPENAI_API_KEY CODEX_API_KEY GEMINI_API_KEY OPENROUTER_API_KEY; do \
	   v=$$(eval "printf %s \"\$${$$k:-}\""); \
	   if [ -n "$$v" ]; then _set="$$_set $$k"; fi; \
	 done; \
	 if [ -n "$$_set" ]; then echo "AI credentials present:$$_set"; \
	 else echo "⚠  No AI credentials set — reviews will fail. Set one in $(DOCKER_ENV):"; \
	      echo "     ANTHROPIC_API_KEY  CLAUDE_CODE_OAUTH_TOKEN  OPENAI_API_KEY  GEMINI_API_KEY"; fi
	@echo ""
	@set -a; . $(DOCKER_ENV); set +a; \
	 if [ -z "$${HEIMDALLM_REPOS_DIR:-}" ]; then \
	   echo "ℹ  Full-repo analysis is OFF (agent only sees the PR diff)."; \
	   echo "   To enable, add to $(DOCKER_ENV):"; \
	   echo "       HEIMDALLM_REPOS_DIR=/absolute/path/to/your/projects"; \
	   echo "   Then \`make down && make up\`. In the UI use /repos/<name> as Local Directory."; \
	 else \
	   echo "✓  Full-repo analysis enabled: $${HEIMDALLM_REPOS_DIR} → /repos (read-only)"; \
	 fi
	@echo ""
	@set -a; . $(DOCKER_ENV); set +a; \
	 if [ -z "$${HEIMDALLM_DISCOVERY_TOPIC:-}" ]; then \
	   echo "ℹ  Topic discovery is OFF. Add HEIMDALLM_DISCOVERY_TOPIC=<topic>"; \
	   echo "   + HEIMDALLM_DISCOVERY_ORGS=<org,org> in $(DOCKER_ENV) to auto-track repos"; \
	   echo "   that carry a GitHub topic. (Optional; skip if HEIMDALLM_REPOSITORIES is enough.)"; \
	 fi
	@echo ""
	@echo "Next: open http://localhost:$${HEIMDALLM_WEB_PORT:-3000}  ·  logs: \`make logs\`  ·  stop: \`make down\`"

up: _check-env
	docker compose -f $(COMPOSE_FILE) up -d
	@$(MAKE) --no-print-directory _post-up-hints

# Like `up` but rebuilds images from local source (use after `git pull` on main).
up-build: _check-env
	docker compose -f $(COMPOSE_FILE) up -d --build --pull always
	@$(MAKE) --no-print-directory _post-up-hints

up-daemon: _check-env
	docker compose -f $(COMPOSE_FILE) up -d heimdallm

# Like `up-daemon` but rebuilds the daemon image from local source.
up-build-daemon: _check-env
	docker compose -f $(COMPOSE_FILE) up -d --build --pull always heimdallm

down: _check-docker
	docker compose -f $(COMPOSE_FILE) down

logs: _check-docker
	docker compose -f $(COMPOSE_FILE) logs -f

logs-daemon: _check-docker
	docker compose -f $(COMPOSE_FILE) logs -f heimdallm

ps: _check-docker
	docker compose -f $(COMPOSE_FILE) ps

restart: _check-docker
	docker compose -f $(COMPOSE_FILE) restart

# ── CI packaging (used by GitHub Actions) ─────────────────────────────────────

package-macos: _check-macos build-daemon build-app
	cp $(DAEMON_BIN) "$(APP_BUNDLE)/Contents/MacOS/heimdalld"
	codesign --force --deep --sign - "$(APP_BUNDLE)"
	mkdir -p dist
	hdiutil create \
	  -volname "Heimdallm" \
	  -srcfolder "$(APP_BUNDLE)" \
	  -ov -format UDZO \
	  "dist/Heimdallm.dmg"

# ── Docker-based Linux build verification ─────────────────────────────────────
#
# Runs the full CI-equivalent pipeline inside a Docker container:
# daemon tests → flutter pub get → flutter test →
# daemon build → flutter build linux --release → verify output artifacts
#
# Works from any OS (macOS or Linux) as long as Docker is available.
#
# Usage:
#   make verify-linux

verify-linux:
	@command -v docker >/dev/null || { echo "❌  Docker is required. Install it from https://docs.docker.com/get-docker/"; exit 1; }
	@echo "▶  Building Linux verification image (this may take a few minutes on first run)..."
	docker build -f Dockerfile.linux-verify -t heimdallm-verify .
	@echo ""
	@echo "✅  Linux build verification passed"

# ── Docker-based Linux GUI runner ─────────────────────────────────────────────
#
# Launches the Heimdallm desktop app from the heimdallm-verify Docker image
# directly on the host X11 display.
#
# Requires:
#   - heimdallm-verify image (run 'make verify-linux' first)
#   - X11 display (DISPLAY env var set — XWayland counts)
#
# GPU acceleration is used when /dev/dri exists; otherwise the app falls
# back to software rendering (llvmpipe) automatically.
#
# --net=host is required so the container can reach the X11 unix socket
# and the host D-Bus session bus without complex network plumbing.
# --ipc=host is required for MIT-SHM (X11 shared memory transport),
# without which GTK falls back to slow network-style rendering.
#
# The container runs as the current user (not root) to avoid file
# ownership issues with the persisted config directory.
#
# Config is persisted to ~/.config/heimdallm between runs.
# Pass GITHUB_TOKEN to connect to GitHub:
#   GITHUB_TOKEN=ghp_... make run-linux

run-linux: LINUX_BUNDLE := /app/flutter_app/build/linux/x64/release/bundle
run-linux:
	@command -v docker >/dev/null || { echo "❌  Docker is required."; exit 1; }
	@test -n "$$DISPLAY" || { echo "❌  No DISPLAY set — need X11 (or XWayland)."; exit 1; }
	@docker image inspect heimdallm-verify >/dev/null 2>&1 || \
	  { echo "❌  Image 'heimdallm-verify' not found. Run 'make verify-linux' first."; exit 1; }
	@mkdir -p "$$HOME/.config/heimdallm" "$$HOME/.local/share/heimdallm" \
	          "$$HOME/.claude" "$$HOME/.gemini" "$$HOME/.codex" \
	          "$$HOME/.config/opencode" "$$HOME/.local/share/opencode"
	@# claude keeps its main config at $HOME/.claude.json (sibling to .claude/),
	@# not inside .claude/. Docker bind-mounts need the source to exist before
	@# start; touch is idempotent and preserves the file if it already exists.
	@touch "$$HOME/.claude.json"
	@docker rm -f heimdallm-run 2>/dev/null || true
	@ENV_FILE=$$(mktemp) ; \
	cleanup() { \
	  xhost -local:docker 2>/dev/null || true ; \
	  rm -f "$$ENV_FILE" ; \
	} ; \
	trap cleanup EXIT ; \
	\
	echo "DISPLAY=$$DISPLAY" > "$$ENV_FILE" ; \
	echo "HOME=$$HOME" >> "$$ENV_FILE" ; \
	if [ -n "$$XDG_CURRENT_DESKTOP" ]; then \
	  echo "XDG_CURRENT_DESKTOP=$$XDG_CURRENT_DESKTOP" >> "$$ENV_FILE" ; \
	fi ; \
	echo "HEIMDALLM_DAEMON_PATH=/app/daemon/bin/heimdallm" >> "$$ENV_FILE" ; \
	if [ -n "$$GITHUB_TOKEN" ]; then \
	  echo "GITHUB_TOKEN=$$GITHUB_TOKEN" >> "$$ENV_FILE" ; \
	elif command -v gh >/dev/null 2>&1; then \
	  GH_TOK=$$(gh auth token 2>/dev/null || true) ; \
	  if [ -n "$$GH_TOK" ]; then \
	    echo "GITHUB_TOKEN=$$GH_TOK" >> "$$ENV_FILE" ; \
	  fi ; \
	fi ; \
	for var in ANTHROPIC_API_KEY CLAUDE_CODE_OAUTH_TOKEN \
	           OPENAI_API_KEY CODEX_API_KEY \
	           GEMINI_API_KEY OPENROUTER_API_KEY ; do \
	  val=$$(printenv "$$var" 2>/dev/null || true) ; \
	  if [ -z "$$val" ] && [ -f docker/.env ]; then \
	    val=$$(grep "^$$var=" docker/.env 2>/dev/null | head -1 | cut -d= -f2-) ; \
	  fi ; \
	  if [ -n "$$val" ]; then \
	    echo "$$var=$$val" >> "$$ENV_FILE" ; \
	  fi ; \
	done ; \
	UID_VAL=$$(id -u) ; \
	GID_VAL=$$(id -g) ; \
	DBUS_ARGS="" ; \
	if [ -e /run/user/$$UID_VAL/bus ]; then \
	  DBUS_ARGS="-v /run/user/$$UID_VAL/bus:/run/user/$$UID_VAL/bus:ro" ; \
	  echo "DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/$$UID_VAL/bus" >> "$$ENV_FILE" ; \
	fi ; \
	GPU_ARGS="" ; \
	if [ -e /dev/dri ]; then \
	  GPU_ARGS="--device /dev/dri" ; \
	else \
	  echo "⚠  /dev/dri not found — using software rendering (llvmpipe)." ; \
	fi ; \
	GH_CONFIG_ARGS="" ; \
	if [ -d "$$HOME/.config/gh" ]; then \
	  GH_CONFIG_ARGS="-v $$HOME/.config/gh:$$HOME/.config/gh:ro" ; \
	fi ; \
	\
	echo "▶  Launching Heimdallm (Linux) via Docker..." ; \
	echo "   Close the app window or press Ctrl-C to stop." ; \
	xhost +local:docker 2>/dev/null || true ; \
	\
	docker run --rm \
	  --name heimdallm-run \
	  --env-file "$$ENV_FILE" \
	  --user "$$UID_VAL:$$GID_VAL" \
	  --security-opt apparmor=unconfined \
	  -v /tmp/.X11-unix:/tmp/.X11-unix:ro \
	  -v /run/dbus:/run/dbus:ro \
	  $$DBUS_ARGS \
	  -v "$$HOME/.config/heimdallm:$$HOME/.config/heimdallm" \
	  -v "$$HOME/.local/share/heimdallm:$$HOME/.local/share/heimdallm" \
	  -v "$$HOME/.claude:$$HOME/.claude" \
	  -v "$$HOME/.claude.json:$$HOME/.claude.json" \
	  -v "$$HOME/.gemini:$$HOME/.gemini" \
	  -v "$$HOME/.codex:$$HOME/.codex" \
	  -v "$$HOME/.config/opencode:$$HOME/.config/opencode" \
	  -v "$$HOME/.local/share/opencode:$$HOME/.local/share/opencode" \
	  $$GH_CONFIG_ARGS \
	  $$GPU_ARGS \
	  --ipc=host \
	  --net=host \
	  heimdallm-verify \
	  $(LINUX_BUNDLE)/heimdallm

clean:
	cd daemon && make clean
	cd flutter_app && flutter clean
