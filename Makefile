.PHONY: build test test-unit test-integration quality quality-policy-update quality-baseline quality-baseline-force coverage coverage-baseline coverage-audit \
	test-perf test-perf-integration test-perf-baseline test-perf-integration-baseline \
	test-perf-baseline-update test-perf-integration-baseline-update fmt lint check

PERF_SAMPLES ?= 5
TEST_TIMING_BASELINE ?= performance/test-timing-baseline.json
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

# quality runs each lane exactly once and derives tests, aggregate coverage, and
# CI timing decisions from those coverage-instrumented executions.
quality:
	go run ./cmd/testcoverage

# Collect five complete coverage-instrumented samples, update materially stale
# bounds, and leave the .quality.yml diff for review.
quality-policy-update:
	go run ./cmd/testcoverage -update-policy

# Temporary compatibility targets for the hosted gate's legacy baseline.
quality-baseline:
	go run ./cmd/testcoverage -write-baseline

# This is only for a maintainer's documented post-force-merge recovery.
# It deliberately generates no commits or hooks.
quality-baseline-force:
	go run ./cmd/testcoverage -write-baseline -force-baseline

# Compatibility aliases for the original coverage workflow.
coverage: quality

coverage-baseline: quality-baseline

# This intentionally profiles every integration scenario separately; do not use it
# on routine pull requests.
coverage-audit:
	go run ./cmd/testcoverage -audit-scenarios

# TEST_TIMING_OUTPUT can override the default artifacts/test-timing report path.
test-perf:
	go run ./cmd/testtiming --lane unit --samples $(PERF_SAMPLES) --baseline $(TEST_TIMING_BASELINE)

test-perf-integration:
	go run ./cmd/testtiming --lane integration --samples $(PERF_SAMPLES) --baseline $(TEST_TIMING_BASELINE)

# Regenerate a lane baseline only from a complete set of stable samples.
test-perf-baseline:
	go run ./cmd/testtiming --lane unit --samples $(PERF_SAMPLES) --baseline $(TEST_TIMING_BASELINE) --replace-baseline

test-perf-integration-baseline:
	go run ./cmd/testtiming --lane integration --samples $(PERF_SAMPLES) --baseline $(TEST_TIMING_BASELINE) --replace-baseline

# Optimization work may update the recorded median only when it lowers a budget.
test-perf-baseline-update:
	go run ./cmd/testtiming --lane unit --samples $(PERF_SAMPLES) --baseline $(TEST_TIMING_BASELINE) --update-baseline

test-perf-integration-baseline-update:
	go run ./cmd/testtiming --lane integration --samples $(PERF_SAMPLES) --baseline $(TEST_TIMING_BASELINE) --update-baseline

fmt:
	go fmt ./...

lint:
	GOTOOLCHAIN=go1.26.3 golangci-lint run ./...

check: fmt quality lint build
