# ── Platform detection ─────────────────────────────────────────────────────────
OS := $(shell uname -s)

# Anchored to this Makefile's directory for the same reason as PKILL_ESCAPE
# below: with $(shell pwd) a `make -f /path/to/Makefile` from elsewhere pointed
# dev-stop at a daemon path that does not exist. Identical from the repo root.
DAEMON_BIN  := $(abspath $(dir $(lastword $(MAKEFILE_LIST))))/daemon/bin/heimdallm

# pkill/pgrep -f treat their pattern as an ERE, so a path containing regex
# metacharacters silently fails to match — a $HOME or checkout directory with
# `+`, `(` or `[` in it would leave the daemon running with no error. The script
# is the single implementation, shared with scripts/test-linux-install.sh, which
# asserts it against a metacharacter-laden path.
#
# Anchored to this Makefile's own directory rather than the cwd: with a relative
# path, `make -f /path/to/Makefile` from elsewhere cannot find the script, and
# since the callers now abort on an empty pattern that would turn an unusual
# invocation into a hard failure of dev-stop and install-linux.
REPO_ROOT_DIR := $(dir $(lastword $(MAKEFILE_LIST)))
PKILL_ESCAPE = $(REPO_ROOT_DIR)scripts/pkill-escape.sh

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

.PHONY: build-daemon build-app build-web build-cli test test-cli lint-cli test-web-tooling test-compose-isolation \
        test-smoke test-github test-e2e test-web dev-cli test-docker dev dev-daemon dev-stop \
        coverage coverage-daemon coverage-cli coverage-flutter coverage-check coverage-ci test-coverage-gate \
        release-local package-macos install-service verify-linux run-linux test-install-macos \
        test-install-linux \
        install-macos uninstall-macos install-linux uninstall-linux \
        setup up up-build up-daemon up-build-daemon down logs logs-daemon \
        ps restart clean clean-clones _check-docker _check-buildkit _check-env \
        up-instance down-instance logs-instance ps-instances _check-instance-name \
        _check-instance-port \
        _check-macos _check-macos-user _check-linux _post-up-hints

# ── Build ─────────────────────────────────────────────────────────────────────

# Version of the current checkout, stamped into locally-built daemon binaries
# (main.version). Distinct from VERSION below, which computes the NEXT release
# tag for release-local. The leading "v" is stripped so all build paths report
# the same format as goreleaser's {{.Version}} (e.g. "0.7.10", not "v0.7.10").
# Release paths must override this with the version being released:
#   make build-daemon GIT_VERSION=0.7.11
# Only v-prefixed tags are considered (--match 'v*'): repos accumulate
# non-version tags (backups, accidental `git tag list`) that would otherwise
# win the describe and get stamped into the binary.
# Lazily expanded (=) so git describe only runs for targets that use it.
GIT_VERSION = $(shell (git describe --tags --match 'v*' --always --dirty 2>/dev/null || echo dev) | sed 's/^v//')

build-daemon:
	cd daemon && make build VERSION=$(GIT_VERSION)

build-app:
	cd flutter_app && flutter build $(FLUTTER_DEVICE) --release

build-cli:
	$(MAKE) -C cli build VERSION=$(GIT_VERSION)

# Flutter Web bundle, consumed by docker/Dockerfile.web (served via Nginx).
# --base-href=/ matches the Nginx server block that expects assets at the root.
build-web:
	cd flutter_app && flutter build web --release --base-href=/

# ── Test ──────────────────────────────────────────────────────────────────────

test:
	cd daemon && make test
	cd flutter_app && flutter test
	$(MAKE) -C cli test

test-cli:
	$(MAKE) -C cli test

lint-cli:
	$(MAKE) -C cli lint

test-install-macos:
	@sh -n scripts/macos-install.sh
	@sh -n scripts/test-macos-install.sh
	@./scripts/test-macos-install.sh

# Guards the pkill invocations in dev-stop / install-linux / uninstall-linux.
# Static-plus-behavioural; starts no daemon and touches no installed files.
test-install-linux:
	@sh -n scripts/test-linux-install.sh
	@./scripts/test-linux-install.sh

test-web-tooling:
	@sh docker/scripts/tests/test-web-tooling.sh

# Static/fake-Docker regression suite for the destructive Compose test wrappers.
# It does not build images, start containers, or touch daemon data.
test-compose-isolation:
	./docker/scripts/tests/test-compose-isolation.sh

test-smoke:
	./docker/scripts/test-local.sh smoke

test-github:
	./docker/scripts/test-local.sh github

test-e2e:
	./docker/scripts/test-local.sh e2e

test-web:
	./docker/scripts/test-web.sh

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

# Debian-based rather than alpine: the image needs a `git` binary. The gitops
# tests drive real repositories, and on alpine they skipped — so the entire git
# layer (checkout, rebase, conflict detection, force-with-lease) contributed
# nothing to the coverage profile even though the native macOS job ran it.
# Untested-in-coverage force-push code is not a trade worth making for a
# smaller image. Same official golang family, still pinned by digest.
GO_DOCKER_IMAGE ?= golang:1.25@sha256:699337d620559a59b4a2bb298ad59611e535d2ee755a34cf2d2a98f37578dc80
GO_TEST_ARGS    ?= -timeout 60s -count=1 ./...
GO_COVERAGE_PROFILE ?=
GO_CONTAINER_COVERAGE_PROFILE := /tmp/heimdallm-daemon-coverage.out
GO_COVERAGE_ARGS := -covermode=atomic -coverpkg=./... \
                    -coverprofile=$(GO_CONTAINER_COVERAGE_PROFILE) $(GO_TEST_ARGS)

# Single definition of the canonical, EDR-safe container boundary used by
# every daemon test mode. In particular, coverage must not grow a second
# docker invocation that can drift from test-docker's pinned/read-only setup.
GO_DOCKER_RUN = docker run --rm \
	--user "$(shell id -u):$(shell id -g)" \
	-v "$(shell pwd):/src:ro" \
	-v "/tmp/heimdallm-gocache:/tmp/.cache" \
	-v "/tmp/heimdallm-home:/tmp/home" \
	-w /src/daemon \
	-e HOME=/tmp/home \
	-e GOCACHE=/tmp/.cache/go-build \
	-e GOMODCACHE=/tmp/.cache/gomod \
	$(GO_DOCKER_IMAGE)

# Coverage profiles share one stable location so the exact same gate command
# can run locally and in CI. The daemon profile is the exception to native
# generation: it is produced in the canonical pinned container below and
# streamed out over stdout, so /src remains read-only.
COVERAGE_DIR             ?= .coverage
DAEMON_COVERAGE_PROFILE  ?= $(COVERAGE_DIR)/daemon.out
CLI_COVERAGE_PROFILE     ?= $(COVERAGE_DIR)/cli.out
FLUTTER_COVERAGE_PROFILE ?= $(COVERAGE_DIR)/flutter/lcov.info
COVERAGE_BASELINE        ?= .github/coverage-baseline.json
COVERAGE_BASE_REF        ?= origin/main

test-docker:
	@command -v docker >/dev/null || { echo "❌  Docker is required. Install from https://docs.docker.com/get-docker/"; exit 1; }
	@mkdir -p /tmp/heimdallm-gocache /tmp/heimdallm-home
ifeq ($(strip $(GO_COVERAGE_PROFILE)),)
	@echo "▶  Running Go vet + tests inside $(GO_DOCKER_IMAGE)"
	$(GO_DOCKER_RUN) sh -c "go vet ./... && go test $(GO_TEST_ARGS)"
else
	@mkdir -p "$(dir $(GO_COVERAGE_PROFILE))"
	@echo "▶  Running Go vet + coverage inside $(GO_DOCKER_IMAGE)"
	@set -eu; \
	  tmp="$$(mktemp "$(GO_COVERAGE_PROFILE).tmp.XXXXXX")"; \
	  trap 'rm -f "$$tmp"' EXIT HUP INT TERM; \
	  $(GO_DOCKER_RUN) \
	    sh -c "go vet ./... 1>&2 && go test $(GO_COVERAGE_ARGS) 1>&2 && cat $(GO_CONTAINER_COVERAGE_PROFILE)" \
	    >"$$tmp"; \
	  test -s "$$tmp"; \
	  mv "$$tmp" "$(GO_COVERAGE_PROFILE)"
endif

# Produce all three profiles without running the policy gate. This is useful
# when inspecting coverage locally or when updating the ratchet baseline.
coverage: coverage-daemon coverage-cli coverage-flutter

coverage-daemon:
	@mkdir -p "$(dir $(DAEMON_COVERAGE_PROFILE))"
	@$(MAKE) test-docker GO_COVERAGE_PROFILE="$(abspath $(DAEMON_COVERAGE_PROFILE))"

coverage-cli:
	$(MAKE) -C cli coverage COVERAGE_PROFILE="$(abspath $(CLI_COVERAGE_PROFILE))"

coverage-flutter:
	@mkdir -p "$(dir $(FLUTTER_COVERAGE_PROFILE))"
	@echo "▶  Generating Flutter coverage"
	@set -eu; \
	  profile="$(abspath $(FLUTTER_COVERAGE_PROFILE))"; \
	  tmp="$$(mktemp "$$profile.tmp.XXXXXX")"; \
	  trap 'rm -f "$$tmp"' EXIT HUP INT TERM; \
	  (cd flutter_app && flutter test --coverage --coverage-path="$$tmp"); \
	  test -s "$$tmp"; \
	  mv "$$tmp" "$$profile"

test-coverage-gate:
	python3 -m unittest discover -s scripts/tests -p 'test_coverage_gate.py' -v

# Validate profiles that already exist. Keeping generation separate makes it
# possible to rerun/debug the policy without paying for all three test suites.
coverage-check:
	python3 scripts/coverage_gate.py $(if $(strip $(GITHUB_STEP_SUMMARY)),--summary "$(GITHUB_STEP_SUMMARY)") \
	  --base-ref "$(COVERAGE_BASE_REF)" \
	  --daemon-profile "$(DAEMON_COVERAGE_PROFILE)" \
	  --cli-profile "$(CLI_COVERAGE_PROFILE)" \
	  --flutter-profile "$(FLUTTER_COVERAGE_PROFILE)" \
	  --baseline "$(COVERAGE_BASELINE)"

# One entry point for the complete local/CI coverage check. The recursive make
# is intentional: it sequences the gate after every profile even under -j.
coverage-ci: coverage
	@$(MAKE) coverage-check

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

dev-cli:
	$(MAKE) -C cli dev

dev-stop:
	@# -x is REQUIRED: without it the pattern also matches this recipe's own
	@# `sh -c` command line, so pkill SIGTERMs the shell running it and dev-stop
	@# aborts — taking `make dev` / `make dev-daemon` with it, whether or not a
	@# daemon was running. The dev daemon is spawned with no arguments, so its
	@# cmdline equals the pattern exactly. Guarded by scripts/test-linux-install.sh.
	@DAEMON_RE=$$($(PKILL_ESCAPE) "$(DAEMON_BIN)") || exit 1; \
	[ -n "$$DAEMON_RE" ] || { echo "❌  pkill-escape.sh produced an empty pattern"; exit 1; }; \
	pkill -x -f "$$DAEMON_RE" 2>/dev/null && echo "↓  Daemon parado" || true
	@UI_PID_FILE="$$HOME/.local/share/heimdallm/ui.pid"; \
	 if [ -f "$$UI_PID_FILE" ]; then \
	   UI_PID=$$(cat "$$UI_PID_FILE"); \
	   kill "$$UI_PID" 2>/dev/null && echo "↓  UI parada (PID $$UI_PID)" || true; \
	   rm -f "$$UI_PID_FILE"; \
	 fi

clean-clones:
	@: "$${HEIMDALLM_API_TOKEN:?set HEIMDALLM_API_TOKEN to the daemon API token}"
	@HEIMDALLM_SERVER_URL="$${HEIMDALLM_SERVER_URL:-http://localhost:7842}"; \
	  curl -fsS -X DELETE \
	    -H "X-Heimdallm-Token: $$HEIMDALLM_API_TOKEN" \
	    "$$HEIMDALLM_SERVER_URL/config/clones"; \
	  echo

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

release-local: _check-macos _check-signing _check-gh
	@# Stamp the daemon with the version being released, not the checkout's
	@# git-describe (which still points at the PREVIOUS tag at this point).
	$(MAKE) build-daemon GIT_VERSION=$(VERSION:v%=%)
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

_check-macos-user: _check-macos
	@if [ "$$(id -u)" -eq 0 ]; then \
	  echo "❌  Do not install or uninstall Heimdallm as root."; \
	  echo "    Run the macOS target as your normal user, without 'sudo make'."; \
	  exit 1; \
	fi

_check-linux:
	@if [ "$$(uname -s)" != "Linux" ]; then \
	  echo "❌  This target requires Linux."; \
	  echo "    On macOS, use 'make release-local' or 'make run-linux'."; \
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

_check-buildkit: _check-docker
	@docker compose version >/dev/null 2>&1 || { \
	  echo "❌  Docker Compose v2 is required for Flutter Web builds."; \
	  exit 1; \
	}
	@docker buildx version >/dev/null 2>&1 || { \
	  echo "❌  Docker Buildx/BuildKit is required for Flutter Web builds."; \
	  exit 1; \
	}
	@if [ "$${DOCKER_BUILDKIT:-1}" = "0" ]; then \
	  echo "⚠  DOCKER_BUILDKIT=0 is overridden with 1 for the Flutter Web build."; \
	fi

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
	 if [ -z "$${HEIMDALLM_LOCAL_DIR_BASE:-}" ]; then \
	   echo "ℹ  Full-repo analysis is OFF (agent only sees the PR diff)."; \
	   echo "   To enable, add to $(DOCKER_ENV):"; \
	   echo "       HEIMDALLM_LOCAL_DIR_BASE=/absolute/path/to/your/projects"; \
	   echo "   Then \`make down && make up\`. In the UI use /home/heimdallm/repos/<name> as Local Directory."; \
	 else \
	   echo "✓  Full-repo analysis enabled: $${HEIMDALLM_LOCAL_DIR_BASE} → /home/heimdallm/repos (read-only)"; \
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

up: _check-env _check-buildkit
	DOCKER_BUILDKIT=1 HEIMDALLM_VERSION=$(GIT_VERSION) docker compose -f $(COMPOSE_FILE) up -d
	@$(MAKE) --no-print-directory _post-up-hints

# Like `up` but rebuilds images from local source (use after `git pull` on main).
up-build: _check-env _check-buildkit
	DOCKER_BUILDKIT=1 HEIMDALLM_VERSION=$(GIT_VERSION) docker compose -f $(COMPOSE_FILE) up -d --build --pull always
	@$(MAKE) --no-print-directory _post-up-hints

up-daemon: _check-env
	HEIMDALLM_VERSION=$(GIT_VERSION) docker compose -f $(COMPOSE_FILE) up -d heimdallm

# Like `up-daemon` but rebuilds the daemon image from local source.
up-build-daemon: _check-env
	HEIMDALLM_VERSION=$(GIT_VERSION) docker compose -f $(COMPOSE_FILE) up -d --build --pull always heimdallm

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

# ── Extra daemon instances ────────────────────────────────────────────────────
#
# The main compose file pins container_name and one volume pair, so it can only
# describe a single daemon. These wrappers layer docker-compose.instance.yml on
# top, giving each extra instance its own Compose project (hence its own
# containers and volumes) keyed on NAME.
#
#   make up-instance NAME=b PORT=7843
#   make logs-instance NAME=b
#   make down-instance NAME=b
#   make ps-instances
#
# Register the result from the hub with `heimdallm-cli instances` or the GUI's
# Instances screen; its API token is at `docker exec heimdallm-b cat
# /data/api_token`.
INSTANCE_COMPOSE_FILES := -f docker/docker-compose.yml -f docker/docker-compose.instance.yml

_check-instance-name:
	@test -n "$(NAME)" || { echo "❌  NAME is required, e.g. make up-instance NAME=b PORT=7843"; exit 1; }
	@echo "$(NAME)" | grep -Eq '^[a-z0-9][a-z0-9-]*$$' \
	  || { echo "❌  NAME must be lowercase alphanumerics and hyphens (it becomes a container and volume name)"; exit 1; }
	@test "$(NAME)" != "heimdallm" || { echo "❌  NAME 'heimdallm' collides with the main stack"; exit 1; }

_check-instance-port:
	@test -n "$(PORT)" || { echo "❌  PORT is required, e.g. make up-instance NAME=b PORT=7843"; exit 1; }
	@echo "$(PORT)" | grep -Eq '^[0-9]+$$' \
	  || { echo "❌  PORT must be a number, got '$(PORT)'"; exit 1; }
	@test "$(PORT)" -ge 1 -a "$(PORT)" -le 65535 \
	  || { echo "❌  PORT must be in 1-65535, got '$(PORT)'"; exit 1; }
	@test "$(PORT)" != "$${HEIMDALLM_PORT:-7842}" \
	  || { echo "❌  PORT $(PORT) is the main stack's host port; pick another"; exit 1; }

up-instance: _check-env _check-buildkit _check-instance-name _check-instance-port
	DOCKER_BUILDKIT=1 HEIMDALLM_VERSION=$(GIT_VERSION) \
	  HEIMDALLM_STACK_NAME=$(NAME) \
	  HEIMDALLM_COMPOSE_DAEMON_HOST_PORT=$(PORT) \
	  docker compose -p heimdallm-$(NAME) $(INSTANCE_COMPOSE_FILES) up -d heimdallm
	@echo ""
	@echo "▶  Instance '$(NAME)' is on http://localhost:$(PORT)"
	@echo "   Token: docker exec heimdallm-$(NAME) cat /data/api_token"
	@echo "   Register it from the hub: heimdallm-cli instances"

down-instance: _check-docker _check-instance-name
	HEIMDALLM_STACK_NAME=$(NAME) \
	  docker compose -p heimdallm-$(NAME) $(INSTANCE_COMPOSE_FILES) down

logs-instance: _check-docker _check-instance-name
	HEIMDALLM_STACK_NAME=$(NAME) \
	  docker compose -p heimdallm-$(NAME) $(INSTANCE_COMPOSE_FILES) logs -f heimdallm

ps-instances: _check-docker
	@docker ps --filter 'name=heimdallm' --format 'table {{.Names}}\t{{.Status}}\t{{.Ports}}'

# ── CI packaging (used by GitHub Actions) ─────────────────────────────────────

package-macos: _check-macos build-daemon build-app
	cp $(DAEMON_BIN) "$(APP_BUNDLE)/Contents/MacOS/heimdalld"
	# Re-sign WITH the entitlements — a bare `codesign --sign -` strips the
	# entitlements `flutter build` applied, dropping the
	# files.user-selected.read-write capability so file_picker's native
	# directory picker silently no-ops. Mirrors the release.yml build-macos job.
	codesign --force --deep --sign - \
	  --entitlements flutter_app/macos/Runner/Release.entitlements \
	  "$(APP_BUNDLE)"
	mkdir -p dist
	hdiutil create \
	  -volname "Heimdallm" \
	  -srcfolder "$(APP_BUNDLE)" \
	  -ov -format UDZO \
	  "dist/Heimdallm.dmg"

# ── Native macOS install / uninstall (release DMG) ────────────────────────────
#
# Downloads a release DMG and installs the validated app bundle transactionally
# into /Applications. The helper owns launchd state, rollback, selective sudo,
# and cleanup; keep these recipes thin so Make never evaluates macOS commands at
# parse time.
#
# Usage:
#   make install-macos
#   make install-macos RELEASE=v0.7.5
#   make uninstall-macos
#   make uninstall-macos PURGE=1
#
# RELEASE/PURGE supplied on the make command line or inherited from the
# environment already reach the recipe environment; do not export them
# globally, where they could leak into unrelated targets.

install-macos: _check-macos-user
	@./scripts/macos-install.sh install

uninstall-macos: _check-macos-user
	@./scripts/macos-install.sh uninstall

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
	docker build --platform linux/amd64 -f Dockerfile.linux-verify --build-arg VERSION=$(GIT_VERSION) -t heimdallm-verify .
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

# ── Native Linux install / uninstall (user-local, no sudo) ────────────────────
#
# Extracts the Flutter bundle and Go daemon from the heimdallm-verify Docker
# image (built by `make verify-linux`) and stages them into ~/.local/ so the
# app launches like any other desktop Linux application. No host Flutter
# toolchain required; Docker does the build.
#
# Layout written:
#   ~/.local/opt/heimdallm/                  # bundle root (matches CI .deb)
#     heimdallm                              # Flutter binary
#     heimdalld                              # Go daemon (copied from /app/daemon/bin/heimdallm in image, renamed)
#   ~/.local/bin/heimdallm                   # → symlink into the bundle
#   ~/.local/share/applications/com.theburrowhub.heimdallm.desktop
#   ~/.local/share/icons/hicolor/{48,128,256,512}x{same}/apps/heimdallm.png
#
# The Flutter app (DaemonLifecycle.defaultBinaryPath in
# flutter_app/lib/core/daemon/daemon_lifecycle.dart) resolves the daemon as
# "heimdalld" next to its own binary, so the rename at install time is what
# makes the spawn work without any env var override.
#
# Binary compatibility: the heimdallm-verify image is ubuntu:22.04, so the
# binaries link dynamically against that distro's glibc/gtk versions. Works
# on any reasonably current Debian/Ubuntu/Fedora/Arch; hosts with a much
# older libc may see missing-symbol errors at first launch.
#
# Usage:
#   make install-linux
#
# To remove: see `uninstall-linux` below.

EXTRACT_DIR := /tmp/heimdallm-install-extract

install-linux: _check-linux verify-linux
	@command -v docker >/dev/null 2>&1 || { echo "❌  Docker is required. Install from https://docs.docker.com/get-docker/"; exit 1; }
	@echo "▶  Extracting Heimdallm artifacts from heimdallm-verify image..."
	@rm -rf "$(EXTRACT_DIR)"
	@mkdir -p "$(EXTRACT_DIR)"
	@CID=$$(docker create heimdallm-verify) ; \
	 trap 'docker rm "$$CID" >/dev/null 2>&1 || true' EXIT ; \
	 docker cp "$$CID:/app/flutter_app/build/linux/x64/release/bundle/." "$(EXTRACT_DIR)/bundle/" && \
	 docker cp "$$CID:/app/daemon/bin/heimdallm" "$(EXTRACT_DIR)/daemon"
	@echo "▶  Staging Heimdallm into $$HOME/.local/opt/heimdallm..."
	rm -rf "$$HOME/.local/opt/heimdallm"
	mkdir -p "$$HOME/.local/opt/heimdallm"
	cp -r "$(EXTRACT_DIR)/bundle/." "$$HOME/.local/opt/heimdallm/"
	cp "$(EXTRACT_DIR)/daemon" "$$HOME/.local/opt/heimdallm/heimdalld"
	chmod +x "$$HOME/.local/opt/heimdallm/heimdalld"
	@# Fork-bomb guard: same check CI's release pipeline runs.
	@# If both binaries are byte-identical, the "spawn heimdalld" call from
	@# DaemonLifecycle would re-exec the Flutter app and hundreds of instances
	@# would spawn on first launch.
	@if cmp -s "$$HOME/.local/opt/heimdallm/heimdallm" "$$HOME/.local/opt/heimdallm/heimdalld"; then \
	  echo "❌  Both binaries are identical — case-collision fork-bomb state. Aborting."; \
	  exit 1; \
	fi
	rm -rf "$(EXTRACT_DIR)"
	mkdir -p "$$HOME/.local/bin"
	ln -sf "$$HOME/.local/opt/heimdallm/heimdallm" "$$HOME/.local/bin/heimdallm"
	@for SIZE in 48 128 256 512; do \
	  ICON_DIR="$$HOME/.local/share/icons/hicolor/$${SIZE}x$${SIZE}/apps"; \
	  mkdir -p "$$ICON_DIR"; \
	  cp "flutter_app/assets/icons/$${SIZE}.png" "$$ICON_DIR/heimdallm.png"; \
	done
	@DESKTOP_DIR="$$HOME/.local/share/applications"; \
	mkdir -p "$$DESKTOP_DIR"; \
	printf '%s\n' \
	  '[Desktop Entry]' \
	  'Name=Heimdallm' \
	  'Comment=AI-powered GitHub PR review agent' \
	  "Exec=$$HOME/.local/opt/heimdallm/heimdallm" \
	  'Icon=heimdallm' \
	  'Type=Application' \
	  'Categories=Development;' \
	  'StartupWMClass=com.theburrowhub.heimdallm' \
	  'StartupNotify=true' \
	  > "$$DESKTOP_DIR/com.theburrowhub.heimdallm.desktop"
	@# Seed ~/.config/heimdallm/.token so the daemon can start when the app is
	@# launched from the OS app launcher (which does not inherit $$GITHUB_TOKEN
	@# from the user's shell). Skipped if the file already exists — respects
	@# manual overrides. Non-fatal if no token source is available.
	@if [ ! -s "$$HOME/.config/heimdallm/.token" ]; then \
	  TOK="" ; SRC="" ; \
	  if [ -n "$$GITHUB_TOKEN" ]; then \
	    TOK="$$GITHUB_TOKEN" ; SRC='$$GITHUB_TOKEN env' ; \
	  elif command -v gh >/dev/null 2>&1 ; then \
	    GH_TOK=$$(gh auth token 2>/dev/null || true) ; \
	    if [ -n "$$GH_TOK" ]; then \
	      TOK="$$GH_TOK" ; SRC='gh auth token' ; \
	    fi ; \
	  fi ; \
	  if [ -n "$$TOK" ]; then \
	    mkdir -p "$$HOME/.config/heimdallm" && \
	    ( umask 077 && printf '%s\n' "$$TOK" > "$$HOME/.config/heimdallm/.token" ) && \
	    echo "    Seeded $$HOME/.config/heimdallm/.token from $$SRC" ; \
	  else \
	    echo "" ; \
	    echo "⚠  No GitHub token found — first launch will fail until you provide one." ; \
	    echo "   Set GITHUB_TOKEN in your shell, run 'gh auth login', or write" ; \
	    echo "   the token to ~/.config/heimdallm/.token (mode 600) manually." ; \
	  fi ; \
	fi
	@# Best-effort launcher cache refresh (silent no-op if tools missing).
	@command -v update-desktop-database >/dev/null 2>&1 && \
	  update-desktop-database "$$HOME/.local/share/applications/" 2>/dev/null || true
	@command -v gtk-update-icon-cache >/dev/null 2>&1 && \
	  gtk-update-icon-cache -q -t "$$HOME/.local/share/icons/hicolor/" 2>/dev/null || true
	@# Stop the outgoing daemon — see #661. Replacing the binary above does not
	@# touch the process already serving it (Linux unlink-while-running), and
	@# DaemonLifecycle.ensureRunning() adopts any healthy daemon on the port
	@# instead of spawning the one just installed. Left alone, the previous
	@# release keeps running indefinitely: stale review logic, stale version in
	@# the Status tab, no indication anything is wrong.
	@#
	@# Runs last, so a failure earlier in this target never takes down a working
	@# daemon. SIGTERM (pkill's default) is the daemon's own graceful path — it
	@# drains HTTP and sweeps in-flight agents.
	@#
	@# -x is REQUIRED, not a refinement: with -f alone the pattern also matches
	@# the recipe's own `sh -c` command line, which contains the path — pkill
	@# SIGTERMs the shell running it and the target dies mid-recipe (`|| true`
	@# does not help; the signal hits the shell, not pkill). -x demands the whole
	@# cmdline equal the pattern, which the daemon satisfies (spawned with no
	@# arguments) and the longer shell cmdline does not. Guarded by
	@# scripts/test-linux-install.sh.
	@DAEMON_RE=$$($(PKILL_ESCAPE) "$$HOME/.local/opt/heimdallm/heimdalld") || exit 1; \
	[ -n "$$DAEMON_RE" ] || { echo "❌  pkill-escape.sh produced an empty pattern — the"; \
	  echo "   previous daemon is still running; stop it before launching."; exit 1; }; \
	if pkill -x -f "$$DAEMON_RE" 2>/dev/null; then \
	  echo ""; \
	  echo "↓  Stopped the previously running daemon (it was serving the old binary)."; \
	  for _ in 1 2 3 4 5 6 7 8 9 10; do \
	    pgrep -x -f "$$DAEMON_RE" >/dev/null 2>&1 || break; \
	    sleep 1; \
	  done; \
	  if pgrep -x -f "$$DAEMON_RE" >/dev/null 2>&1; then \
	    echo "⚠  It has not exited after 10s — stop it manually, or it will keep"; \
	    echo "   serving the old binary."; \
	  else \
	    echo "   Start the new one from the app: Server → Status → Start server."; \
	  fi; \
	fi
	@echo ""
	@echo "✅  Heimdallm installed:"
	@echo "    Bundle:  $$HOME/.local/opt/heimdallm/"
	@echo "    Symlink: $$HOME/.local/bin/heimdallm"
	@echo "    Desktop: $$HOME/.local/share/applications/com.theburrowhub.heimdallm.desktop"
	@echo "    Icons:   $$HOME/.local/share/icons/hicolor/<size>x<size>/apps/heimdallm.png"
	@echo ""
	@echo "    Launch with: heimdallm  (or via your app launcher)"
	@case ":$$PATH:" in \
	  *":$$HOME/.local/bin:"*) ;; \
	  *) echo ""; \
	     echo "⚠  $$HOME/.local/bin is not on your PATH."; \
	     echo "   Add this to ~/.bashrc or ~/.zshrc:"; \
	     echo "     export PATH=\"\$$HOME/.local/bin:\$$PATH\"" ;; \
	esac

# ── Native Linux uninstall ────────────────────────────────────────────────────
#
# Removes everything install-linux created under ~/.local/, but preserves
# user configuration (~/.config/heimdallm) and runtime data
# (~/.local/share/heimdallm) by default.
#
# Usage:
#   make uninstall-linux              # app only — config and data preserved
#   make uninstall-linux PURGE=1      # also wipes ~/.config + ~/.local/share state
#
# The PURGE flag mirrors Debian's `apt remove` vs. `apt purge` distinction.

uninstall-linux: _check-linux
	@echo "▶  Uninstalling Heimdallm from $$HOME/.local/..."
	@# Stop running instances (best-effort — ignored if nothing is running).
	@#
	@# -x is REQUIRED: with -f alone the pattern also matches the recipe's own
	@# `sh -c` command line, so pkill SIGTERMs the shell running it and this
	@# target dies here — before the desktop entry, icons and symlink below are
	@# removed (`|| true` does not help; the signal hits the shell, not pkill).
	@# -x demands the whole cmdline equal the pattern, which the shell's longer
	@# cmdline never does. Guarded by scripts/test-linux-install.sh.
	@#
	@# Coverage caveat: pkill -f matches against /proc/<pid>/cmdline. This
	@# catches:
	@#   - launcher-launched Flutter apps (desktop entry Exec= is absolute,
	@#     so argv[0] = $$HOME/.local/opt/heimdallm/heimdallm — matches)
	@#   - any daemon spawned by DaemonLifecycle (always uses the absolute
	@#     path "heimdalld next to my binary" — matches)
	@#
	@# It does NOT catch terminal-launched Flutter apps invoked as just
	@# `heimdallm` on PATH — those have argv[0] = "heimdallm" and slip
	@# through. That is safe: Linux's unlink-while-running semantics mean
	@# the subsequent rm -rf of the bundle directory does not affect a
	@# running process, and the user can close the window whenever. We
	@# intentionally avoid a broader `-f 'heimdallm'` match to prevent
	@# hitting unrelated dev processes (e.g. `flutter run` of this repo).
	@#
	@# -x narrows this further: an app launched WITH arguments (e.g.
	@# `~/.local/opt/heimdallm/heimdallm --some-flag`) no longer matches, since
	@# its cmdline is longer than the pattern. Same rationale as above — the
	@# rm -rf is safe against a running process — and plain -f never actually
	@# reached that case anyway, because it killed this recipe's shell first.
	@APP_RE=$$($(PKILL_ESCAPE) "$$HOME/.local/opt/heimdallm/heimdallm" 2>/dev/null); \
	 if [ -n "$$APP_RE" ]; then pkill -x -f "$$APP_RE" 2>/dev/null || true; \
	 else echo "⚠  could not build the app pattern; leaving any running app alone"; fi
	@DAEMON_RE=$$($(PKILL_ESCAPE) "$$HOME/.local/opt/heimdallm/heimdalld" 2>/dev/null); \
	 if [ -n "$$DAEMON_RE" ]; then pkill -x -f "$$DAEMON_RE" 2>/dev/null || true; \
	 else echo "⚠  could not build the daemon pattern; leaving any running daemon alone"; fi
	@rm -f "$$HOME/.local/share/heimdallm/ui.pid"
	rm -f "$$HOME/.local/share/applications/com.theburrowhub.heimdallm.desktop"
	@for SIZE in 48 128 256 512; do \
	  rm -f "$$HOME/.local/share/icons/hicolor/$${SIZE}x$${SIZE}/apps/heimdallm.png"; \
	done
	@# Only remove the PATH shim if it's our symlink — never clobber an
	@# unrelated file that happens to share the name.
	@if [ -L "$$HOME/.local/bin/heimdallm" ]; then \
	  TARGET=$$(readlink "$$HOME/.local/bin/heimdallm"); \
	  case "$$TARGET" in \
	    "$$HOME/.local/opt/heimdallm/"*) \
	      rm -f "$$HOME/.local/bin/heimdallm"; \
	      echo "↓  Removed $$HOME/.local/bin/heimdallm" ;; \
	    *) \
	      echo "⚠  $$HOME/.local/bin/heimdallm points to $$TARGET — leaving it alone." ;; \
	  esac; \
	elif [ -e "$$HOME/.local/bin/heimdallm" ]; then \
	  echo "⚠  $$HOME/.local/bin/heimdallm exists but is not a symlink — leaving it alone."; \
	fi
	rm -rf "$$HOME/.local/opt/heimdallm"
	@# Refresh launcher caches so the stale entry disappears from menus.
	@command -v update-desktop-database >/dev/null 2>&1 && \
	  update-desktop-database "$$HOME/.local/share/applications/" 2>/dev/null || true
	@command -v gtk-update-icon-cache >/dev/null 2>&1 && \
	  gtk-update-icon-cache -q -t "$$HOME/.local/share/icons/hicolor/" 2>/dev/null || true
	@if [ "$(PURGE)" = "1" ]; then \
	  echo ""; \
	  echo "⚠  PURGE=1 — wiping user config and runtime data..."; \
	  if [ -d "$$HOME/.config/heimdallm" ]; then \
	    rm -rf "$$HOME/.config/heimdallm"; \
	    echo "    Removed $$HOME/.config/heimdallm"; \
	  fi; \
	  if [ -d "$$HOME/.local/share/heimdallm" ]; then \
	    rm -rf "$$HOME/.local/share/heimdallm"; \
	    echo "    Removed $$HOME/.local/share/heimdallm"; \
	  fi; \
	  echo ""; \
	  echo "✅  Heimdallm fully uninstalled (config and data wiped)."; \
	else \
	  echo ""; \
	  echo "✅  Heimdallm uninstalled (config and data preserved)."; \
	  echo ""; \
	  echo "    Config: $$HOME/.config/heimdallm/"; \
	  echo "    Data:   $$HOME/.local/share/heimdallm/"; \
	  echo ""; \
	  echo "    To wipe these too: make uninstall-linux PURGE=1"; \
	fi

clean:
	cd daemon && make clean
	cd flutter_app && flutter clean
