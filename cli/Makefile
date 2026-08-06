BINARY = bin/heimdallm-cli
CMD    = ./cmd/heimdallm-cli

# Stamped into main.version; "dev" for a bare `make -C cli build`. The root
# Makefile must keep passing the same GIT_VERSION to build-cli and build-daemon,
# or the Server tab's Daemon/CLI rows stop being comparable.
#
# ?= is not enough on its own: a command-line VERSION= counts as defined even
# when empty, so it would stamp an empty version — and cobra then drops the
# --version flag entirely, rather than reporting "dev". `override` is required
# because a plain assignment here would lose to the command line.
VERSION ?= dev
ifeq ($(strip $(VERSION)),)
override VERSION := dev
endif

COVERAGE_PROFILE ?= $(abspath ../.coverage/cli.out)

.PHONY: build test test-race coverage lint clean dev

build:
	@mkdir -p $(dir $(BINARY))
	go build -ldflags "-X main.version=$(VERSION)" -o $(BINARY) $(CMD)

test:
	go test -timeout 60s ./...

test-race:
	go test -race -timeout 60s ./...

coverage:
	@mkdir -p "$(dir $(COVERAGE_PROFILE))"
	@set -eu; \
	  tmp="$$(mktemp "$(COVERAGE_PROFILE).tmp.XXXXXX")"; \
	  trap 'rm -f "$$tmp"' EXIT HUP INT TERM; \
	  go test -timeout 60s -count=1 -covermode=atomic -coverpkg=./... \
	    -coverprofile="$$tmp" ./...; \
	  test -s "$$tmp"; \
	  mv "$$tmp" "$(COVERAGE_PROFILE)"

lint:
	go vet ./...

clean:
	rm -rf bin/

dev:
	go run $(CMD) dashboard
