.PHONY: build test fmt lint

build:
	go build ./cmd/orpheus

test:
	go test ./...

fmt:
	go fmt ./...

lint:
	golangci-lint run
