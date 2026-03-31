BINARY = bin/auto-pr-daemon
CMD    = ./cmd/auto-pr-daemon

.PHONY: build test lint clean dev

build:
	go build -o $(BINARY) $(CMD)

test:
	go test ./... -race -timeout 60s

lint:
	go vet ./...

clean:
	rm -rf bin/

dev:
	go run $(CMD)
