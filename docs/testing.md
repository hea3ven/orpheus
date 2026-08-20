# Testing

Orpheus separates tests by behavioral scope, not by speed or whether a test writes to disk.

## Commands

- `make test-unit` runs the explicit unit lane.
- `make test` is a compatibility alias for `make test-unit`.
- `make test-integration` runs the integration lane only.
- `make quality` runs both lanes once with coverage and timing policy.
- `make check` formats code, runs the single-pass quality report, then lints.

## Unit lane

A unit test exercises package-owned logic with injected collaborators or fakes. It requires only Go and may use isolated temporary files. It must not require or start Git, Beads, gh, Codex, Pi, or any other child executable.

## Integration lane

An integration test verifies a cross-package workflow or an isolated local process contract. This includes real Git and Beads behavior, compiled CLI behavior, and child-process contracts. It may use temporary directories, local bare Git remotes, and fake executables, but it remains network-free and credential-free.

`make test-integration` requires `git` and `bd` on `PATH`. Tests that need `dolt` skip when it is unavailable. No lane may read operator data, use live networks or credentials, or run a real model agent.

## Structural membership

Integration source files use `//go:build integration`, and their top-level test bodies begin with `TestIntegration`. Untagged test bodies whose names do not have that prefix are unit tests. `internal/testlane` validates this convention so every top-level body is selected by exactly one lane. The build constraint is the membership mechanism; the integration name filter only limits execution to structurally tagged integration bodies.

## Single-pass quality report

`make quality` is the routine local and CI quality command. It runs the unit and
integration lanes serially exactly once with `-coverpkg=./...`; the same decoded
`go test -json` streams supply test outcomes, failure evidence, test-event
counts, suite and package timings, and coverage profiles. The complete report is
written to `artifacts/test-coverage/report.json`. If a lane fails, the command
still runs the other lane and writes a partial report containing stderr, raw
JSON output, and decoded failing-test output before returning failure. `make
coverage` remains a compatibility alias.

The compact `coverage/test-coverage-baseline.json` contains only repository and
package coverage aggregates, test-event counts, timing budgets, and policy. It
does not track source blocks, coordinates, files, or hit inventories. Coverage
is evaluated independently for each lane at repository and package scope.
Changes inside the configured significance and denominator-drift bands pass
without baseline churn. Significant improvements, package/test structure
changes, and significant denominator drift require a generated refresh;
significant regressions fail.

Use `make quality-baseline` (or the compatibility alias `make
coverage-baseline`) for an eligible refresh. Generation is sorted and
repeatable, refuses to replace a baseline after a prohibited coverage regression
or timing failure, and preserves every existing timing budget. New timed
packages receive a budget from their first coverage-instrumented measurement.
Faster measurements neither require refresh nor ratchet a budget down.

Pull-request CI reads the baseline from the base branch as the trusted prior.
A coverage regression against that prior fails even if the pull request edits
its tracked baseline. When the trusted comparison requires refresh, CI accepts
only the generated current aggregates while retaining trusted timing budgets.
This uses only `contents: read` and the job summary.

`make coverage-audit` is deliberately on-demand. It profiles each integration
top-level scenario separately and reports runtime, containment in the full
integration profile, Jaccard similarity with the other scenarios, and exclusive
statements. It is too expensive for routine pull requests.

## Detailed timing baseline

The single-pass quality baseline enforces routine CI timing budgets. Separately,
`make test-perf` and `make test-perf-integration` preserve the detailed
multi-sample workflow: they collect five uncached samples by default and compare
median timings against `performance/test-timing-baseline.json`. A report includes
lane-specific environment metadata, test-event counts, package timings, and the
full JSON output plus assertion output for a failed sample. Failed or incomplete
reports are retained but never checked against or used to update a baseline. A
changed test-event count fails the comparison so an obsolete baseline cannot
look comparable.

Use `make test-perf-baseline` or `make test-perf-integration-baseline` only after a complete, stable set of samples to regenerate that lane's baseline. The corresponding `*-update` target only ratchets budgets down after an optimization; a changed test-event count requires regeneration instead.
