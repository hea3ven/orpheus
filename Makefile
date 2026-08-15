.PHONY: build test test-unit test-integration test-perf test-perf-integration \
	test-perf-baseline test-perf-integration-baseline \
	test-perf-baseline-update test-perf-integration-baseline-update fmt lint check

PERF_SAMPLES ?= 5
TEST_TIMING_BASELINE ?= performance/test-timing-baseline.json
INTEGRATION_TEST_PATTERN := ^TestIntegration
INTEGRATION_TEST_ARGS := -tags=integration -run '$(INTEGRATION_TEST_PATTERN)'

build: check
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

# TEST_TIMING_OUTPUT can override the default artifacts/test-timing report path.
test-perf:
	go run ./cmd/testtiming --lane unit --samples $(PERF_SAMPLES) --baseline $(TEST_TIMING_BASELINE)

test-perf-integration:
	go run ./cmd/testtiming --lane integration --samples $(PERF_SAMPLES) --baseline $(TEST_TIMING_BASELINE)

# Create a baseline only when bringing timing checks to a new repository copy.
test-perf-baseline:
	go run ./cmd/testtiming --lane unit --samples $(PERF_SAMPLES) --baseline $(TEST_TIMING_BASELINE) --init-baseline

test-perf-integration-baseline:
	go run ./cmd/testtiming --lane integration --samples $(PERF_SAMPLES) --baseline $(TEST_TIMING_BASELINE) --init-baseline

# Optimization work may update the recorded median only when it lowers a budget.
test-perf-baseline-update:
	go run ./cmd/testtiming --lane unit --samples $(PERF_SAMPLES) --baseline $(TEST_TIMING_BASELINE) --update-baseline

test-perf-integration-baseline-update:
	go run ./cmd/testtiming --lane integration --samples $(PERF_SAMPLES) --baseline $(TEST_TIMING_BASELINE) --update-baseline

fmt:
	go fmt ./...

lint:
	golangci-lint run ./...

check: fmt test-unit test-integration lint
