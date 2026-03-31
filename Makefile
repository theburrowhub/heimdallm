DAEMON_BIN  := $(shell pwd)/daemon/bin/heimdallr
FLUTTER_APP := flutter_app/build/macos/Build/Products

.PHONY: build-daemon build-app test dev dev-daemon dev-stop package-macos install-service clean

# ── Build ────────────────────────────────────────────────────────────────────

build-daemon:
	cd daemon && make build

build-app:
	cd flutter_app && flutter build macos --release

# ── Test ─────────────────────────────────────────────────────────────────────

test:
	cd daemon && make test
	cd flutter_app && flutter test

# ── Local development ────────────────────────────────────────────────────────
#
# make dev        — flujo completo: compila daemon, lo arranca en background
#                   e inicia la app Flutter lista para probar.
#
#                   Si es la primera vez: la app mostrará la pantalla de
#                   configuración donde introduces el token y los repos.
#                   Al pulsar "Guardar e iniciar Heimdallr" el daemon arrancará
#                   con la config generada.
#
#                   Si ya tienes config en ~/.config/heimdallr/config.toml:
#                   el daemon arranca solo y la app abre directamente el dashboard.
#
# make dev-daemon — arranca solo el daemon (sin la app Flutter)
# make dev-stop   — para el daemon

dev: build-daemon dev-stop
	@echo "▶  Arrancando daemon en background..."
	@GITHUB_TOKEN="$${GITHUB_TOKEN}" $(DAEMON_BIN) &
	@sleep 0.5
	@echo "▶  Lanzando Heimdallr..."
	cd flutter_app && HEIMDALLR_DAEMON_PATH=$(DAEMON_BIN) flutter run -d macos

dev-daemon: build-daemon dev-stop
	@echo "▶  Daemon en http://localhost:7842 (Ctrl-C para parar)"
	GITHUB_TOKEN="$${GITHUB_TOKEN}" $(DAEMON_BIN)

dev-stop:
	@pkill -f "$(DAEMON_BIN)" 2>/dev/null && echo "↓  Daemon parado" || true

# ── Packaging ────────────────────────────────────────────────────────────────

package-macos: build-daemon build-app
	cp $(DAEMON_BIN) \
	  "$(FLUTTER_APP)/Release/heimdallr.app/Contents/MacOS/heimdallr"
	create-dmg \
	  --volname "Heimdallr" \
	  --window-size 540 380 \
	  --icon-size 128 \
	  --app-drop-link 380 185 \
	  "dist/heimdallr.dmg" \
	  "$(FLUTTER_APP)/Release/heimdallr.app"

install-service: build-daemon
	$(DAEMON_BIN) install

clean:
	cd daemon && make clean
	cd flutter_app && flutter clean
