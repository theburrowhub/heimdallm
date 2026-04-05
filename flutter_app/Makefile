# ── Platform detection ─────────────────────────────────────────────────────────
OS := $(shell uname -s)

ifeq ($(OS),Darwin)
  DEVICE := macos
else
  DEVICE := linux
endif

.PHONY: build test gen run clean

gen:
	dart run build_runner build --delete-conflicting-outputs

build:
	flutter build $(DEVICE) --release

test:
	flutter test

run:
	flutter run -d $(DEVICE)

clean:
	flutter clean
