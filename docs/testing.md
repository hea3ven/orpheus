# Testing

Orpheus separates tests by behavioral scope, not by speed or whether a test writes to disk.

## Commands

- `make test-unit` runs the explicit unit lane.
- `make test` is a compatibility alias for `make test-unit`.
- `make test-integration` runs the integration lane only.
- `make check` formats code, runs each lane once, then lints.

## Unit lane

A unit test exercises package-owned logic with injected collaborators or fakes. It requires only Go and may use isolated temporary files. It must not require or start Git, Beads, gh, Codex, Pi, or any other child executable.

## Integration lane

An integration test verifies a cross-package workflow or an isolated local process contract. This includes real Git and Beads behavior, compiled CLI behavior, and child-process contracts. It may use temporary directories, local bare Git remotes, and fake executables, but it remains network-free and credential-free.

`make test-integration` requires `git` and `bd` on `PATH`. Tests that need `dolt` skip when it is unavailable. No lane may read operator data, use live networks or credentials, or run a real model agent.

## Structural membership

Integration source files use `//go:build integration`, and their top-level test bodies begin with `TestIntegration`. Untagged test bodies whose names do not have that prefix are unit tests. `internal/testlane` validates this convention so every top-level body is selected by exactly one lane. The build constraint is the membership mechanism; the integration name filter only limits execution to structurally tagged integration bodies.

## Timing baseline

`make test-perf` and `make test-perf-integration` collect five uncached samples by default and compare median timings against `performance/test-timing-baseline.json`. A report includes lane-specific environment metadata, test-event counts, package timings, and the full JSON output plus assertion output for a failed sample. Failed or incomplete reports are retained but never checked against or used to update a baseline. A changed test-event count fails the comparison so an obsolete baseline cannot look comparable.

Use `make test-perf-baseline` or `make test-perf-integration-baseline` only after a complete, stable set of samples to regenerate that lane's baseline. The corresponding `*-update` target only ratchets budgets down after an optimization; a changed test-event count requires regeneration instead.
