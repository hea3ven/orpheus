.PHONY: build test fmt lint check

build: check
	go build ./cmd/orpheus

test:
	go test ./...

fmt:
	go fmt ./...

lint:
	golangci-lint run ./...

check: fmt test lint
