BINARY = bin/heimdallm-cli
CMD    = ./cmd/heimdallm-cli

.PHONY: build test test-race lint clean dev

build:
	@mkdir -p $(dir $(BINARY))
	go build -o $(BINARY) $(CMD)

test:
	go test ./... -timeout 60s

test-race:
	go test ./... -race -timeout 60s

lint:
	go vet ./...

clean:
	rm -rf bin/

dev:
	go run $(CMD) dashboard
