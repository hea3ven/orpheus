.PHONY: build test test-integration fmt lint check

build: check
	go build ./cmd/orpheus

test:
	go test ./...

test-integration:
	@command -v bd >/dev/null 2>&1 || { echo "Beads integration tests require bd; install Beads or ensure bd is on PATH." >&2; exit 1; }
	go test -tags=integration -run '^TestIntegration' ./...

fmt:
	go fmt ./...

lint:
	golangci-lint run ./...

check: fmt test test-integration lint
