BINARY = bin/heimdallm-cli
CMD    = ./cmd/heimdallm-cli

# Stamped into main.version; "dev" for a bare `make -C cli build`. The root
# Makefile must keep passing the same GIT_VERSION to build-cli and build-daemon,
# or the Server tab's Daemon/CLI rows stop being comparable.
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
