BINARY = bin/heimdallm-cli
CMD    = ./cmd/heimdallm-cli

# Stamped into main.version. Overridden by the root Makefile with GIT_VERSION,
# matching how daemon/Makefile is driven, so both binaries report the same
# format (e.g. "0.8.0"). Left as "dev" for a bare `make -C cli build`.
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
