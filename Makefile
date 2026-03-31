DAEMON_BIN  := $(shell pwd)/daemon/bin/heimdallr
FLUTTER_APP := flutter_app/build/macos/Build/Products
APP_BUNDLE  := $(FLUTTER_APP)/Release/Heimdallr.app

# Detect local Developer ID Application certificate automatically
SIGNING_IDENTITY ?= $(shell security find-identity -v -p codesigning 2>/dev/null \
	| grep "Developer ID Application" | head -1 | sed 's/.*"\(.*\)".*/\1/')

.PHONY: build-daemon build-app test dev dev-daemon dev-stop \
        release-local package-local install-service clean

# ── Build ─────────────────────────────────────────────────────────────────────

build-daemon:
	cd daemon && make build

build-app:
	cd flutter_app && flutter build macos --release

# ── Test ──────────────────────────────────────────────────────────────────────

test:
	cd daemon && make test
	cd flutter_app && flutter test

# ── Local development ─────────────────────────────────────────────────────────
#
# make dev         — build daemon + run Flutter in debug mode
# make dev-daemon  — run daemon only (for API debugging)
# make dev-stop    — stop the running daemon

dev: build-daemon dev-stop
	@echo "▶  Lanzando Heimdallr..."
	cd flutter_app && HEIMDALLR_DAEMON_PATH=$(DAEMON_BIN) flutter run -d macos

dev-daemon: build-daemon dev-stop
	@echo "▶  Daemon en http://localhost:7842 (Ctrl-C para parar)"
	GITHUB_TOKEN="$${GITHUB_TOKEN}" $(DAEMON_BIN)

dev-stop:
	@pkill -f "$(DAEMON_BIN)" 2>/dev/null && echo "↓  Daemon parado" || true

# ── Local release (sign + notarize + DMG + GitHub release) ───────────────────
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
#       xcrun notarytool store-credentials "heimdallr-notary" \
#         --apple-id YOUR@EMAIL.COM --team-id TEAMID --password APP_SPECIFIC_PWD
#   - gh CLI authenticated: gh auth login

VERSION ?= $(shell \
	LAST=$$(git describe --tags --abbrev=0 2>/dev/null || echo "v0.0.0"); \
	VER=$${LAST\#v}; \
	MAJ=$$(echo $$VER | cut -d. -f1); \
	MIN=$$(echo $$VER | cut -d. -f2); \
	PAT=$$(echo $$VER | cut -d. -f3); \
	echo "v$$MAJ.$$MIN.$$((PAT+1))")

release-local: _check-signing _check-gh build-daemon
	@echo ""
	@echo "╔══════════════════════════════════════════════╗"
	@echo "║  Heimdallr local release                     ║"
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

	# ── 5. Create DMG ─────────────────────────────────────────────────────────
	@echo "▶  Creating DMG..."
	mkdir -p dist
	hdiutil create \
	  -volname "Heimdallr $(VERSION)" \
	  -srcfolder "$(APP_BUNDLE)" \
	  -ov -format UDZO \
	  "dist/Heimdallr-$(VERSION).dmg"

	# ── 6. Notarize DMG ───────────────────────────────────────────────────────
	@echo "▶  Submitting for notarization (this takes a few minutes)..."
	xcrun notarytool submit "dist/Heimdallr-$(VERSION).dmg" \
	  --keychain-profile "heimdallr-notary" \
	  --wait
	xcrun stapler staple "dist/Heimdallr-$(VERSION).dmg"
	@echo "✓  Notarization complete"

	# ── 7. Create git tag ─────────────────────────────────────────────────────
	@echo "▶  Creating tag $(VERSION)..."
	git tag -a "$(VERSION)" -m "Release $(VERSION)"
	git push origin "$(VERSION)"

	# ── 8. Publish GitHub release ─────────────────────────────────────────────
	@echo "▶  Publishing GitHub release..."
	gh release create "$(VERSION)" \
	  "dist/Heimdallr-$(VERSION).dmg" \
	  --title "Heimdallr $(VERSION)" \
	  --generate-notes \
	  --verify-tag
	@echo ""
	@echo "✅  Released Heimdallr $(VERSION)"
	@echo "    https://github.com/$$(gh repo view --json nameWithOwner -q .nameWithOwner)/releases/tag/$(VERSION)"

# ── Guards ────────────────────────────────────────────────────────────────────

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

# ── Install LaunchAgent service ───────────────────────────────────────────────

install-service: build-daemon
	$(DAEMON_BIN) install

# ── CI packaging (used by GitHub Actions) ─────────────────────────────────────

package-macos: build-daemon build-app
	cp $(DAEMON_BIN) "$(APP_BUNDLE)/Contents/MacOS/heimdalld"
	codesign --force --deep --sign - "$(APP_BUNDLE)"
	mkdir -p dist
	hdiutil create \
	  -volname "Heimdallr" \
	  -srcfolder "$(APP_BUNDLE)" \
	  -ov -format UDZO \
	  "dist/Heimdallr.dmg"

clean:
	cd daemon && make clean
	cd flutter_app && flutter clean
