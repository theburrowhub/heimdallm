BINARY = bin/heimdallm-cli
CMD    = ./cmd/heimdallm-cli

# Stamped into main.version. Left as "dev" for a bare `make -C cli build`.
#
# Invariant: the root Makefile passes GIT_VERSION here and to daemon/Makefile
# from the same variable (build-cli and build-daemon), so the CLI and the daemon
# always report the same format (e.g. "0.8.0"). If that variable is renamed or
# its format changes, both call sites must move together — a CLI stamped from a
# different source than the daemon makes the Server tab's Daemon/CLI comparison
# meaningless, which is the whole point of showing them side by side.
VERSION ?= dev

.PHONY: build test test-race lint clean dev

build:
	@mkdir -p $(dir $(BINARY))
	go build -ldflags "-X main.version=$(VERSION)" -o $(BINARY) $(CMD)

test:
	go test -timeout 60s ./...

test-race:
	go test -race -timeout 60s ./...

lint:
	go vet ./...

clean:
	rm -rf bin/

dev:
	go run $(CMD) dashboard
