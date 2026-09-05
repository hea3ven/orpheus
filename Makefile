.PHONY: build test test-unit test-integration quality quality-policy-update coverage coverage-audit fmt lint check

INTEGRATION_TEST_PATTERN := ^TestIntegration
INTEGRATION_TEST_ARGS := -tags=integration -run '$(INTEGRATION_TEST_PATTERN)'

build:
	go build ./cmd/orpheus

# Unit tests are package-owned and hermetic: no installed Git, Beads, gh,
# Codex, or Pi executable is needed.
test-unit:
	go test ./...

# Kept for callers that used the original routine test command.
test: test-unit

test-integration:
	@command -v bd >/dev/null 2>&1 || { echo "Beads integration tests require bd; install Beads or ensure bd is on PATH." >&2; exit 1; }
	go test $(INTEGRATION_TEST_ARGS) ./...

# quality runs each lane exactly once and derives tests, coverage, and timing
# decisions from the reviewed repository policy.
quality:
	go run ./cmd/testcoverage

# Collect five complete coverage-instrumented samples, update materially stale
# bounds, and leave the .quality.yml diff for review.
quality-policy-update:
	go run ./cmd/testcoverage -update-policy

# Kept for callers that used the original coverage command.
coverage: quality

# This intentionally profiles every integration scenario separately; do not use it
# on routine pull requests.
coverage-audit:
	go run ./cmd/testcoverage -audit-scenarios

fmt:
	go fmt ./...

lint:
	GOTOOLCHAIN=go1.26.3 golangci-lint run ./...

check: fmt quality lint build
