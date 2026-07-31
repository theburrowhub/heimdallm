BINARY  = bin/heimdallm
CMD     = ./cmd/heimdallm
# Stamped into main.version at build time; "dev" when not provided.
VERSION ?= dev

.PHONY: build test test-race lint clean dev

build:
	go build -ldflags "-X main.version=$(VERSION)" -o $(BINARY) $(CMD)

test:
	go test ./... -timeout 60s

test-race:
	go test ./... -race -timeout 60s

lint:
	go vet ./...

clean:
	rm -rf bin/

dev:
	go run $(CMD)
