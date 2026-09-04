# Testing

Orpheus separates tests by behavioral scope, not by speed or whether a test writes to disk.

## Commands

- `make test-unit` runs the explicit unit lane.
- `make test` is a compatibility alias for `make test-unit`.
- `make test-integration` runs the integration lane only.
- `make quality` runs both lanes once with coverage and timing policy.
- `make check` formats code, runs the single-pass quality report, lints, and builds the CLI. Each test lane still runs only once.

## Unit lane

A unit test exercises package-owned logic with injected collaborators or fakes. It requires only Go and may use isolated temporary files. It must not require or start Git, Beads, gh, Codex, Pi, or any other child executable.

## Canonical temporary directories

All per-test temporary directories must come from
`testutil.CanonicalTempDir(t)`. It returns a clean absolute path after resolving
all existing symlink components, so a path created through macOS `/tmp` compares
consistently with paths reported through `/private/tmp`.

Do not call `t.TempDir()` directly. GolangCI-Lint and the repository AST
validator enforce this invariant during `make check`. The helper is the sole
boundary permitted to call `testing.TB.TempDir`.

Use stable non-temporary fixture roots such as `/fixture/...` for fake paths.
An absolute `/tmp/...` path token anywhere in a test fixture string needs a
same-line, specific explanation because its path identity must be intentionally
irrelevant:

```go
const fixturePath = "/tmp/example" // orpheus:allow-absolute-tmp-path -- Path identity is intentionally irrelevant to this isolated fixture.
```

A direct `t.TempDir()` call is only permitted when path identity is intentionally
irrelevant and must be suppressed on the same line for both enforcement layers:

```go
_ = t.TempDir() //nolint:forbidigo // Path identity is intentionally irrelevant to this isolated fixture.
```

## Integration lane

An integration test verifies a cross-package workflow or an isolated local process contract. This includes real Git and Beads behavior, compiled CLI behavior, and child-process contracts. It may use temporary directories, local bare Git remotes, and fake executables, but it remains network-free and credential-free.

`make test-integration` requires `git` and `bd` on `PATH`. Tests that need `dolt` skip when it is unavailable. No lane may read operator data, use live networks or credentials, or run a real model agent.

## Structural membership

Integration source files use `//go:build integration`, and their top-level test bodies begin with `TestIntegration`. Untagged test bodies whose names do not have that prefix are unit tests. `internal/testlane` validates this convention so every top-level body is selected by exactly one lane. The build constraint is the membership mechanism; the integration name filter only limits execution to structurally tagged integration bodies.

## Single-pass quality report

`make quality` is the routine local and CI quality command. It runs the unit and
integration lanes serially exactly once with `-coverpkg=./...`; the same decoded
`go test -json` streams supply test outcomes, failure evidence, test-event
counts, suite timings, timings for packages that actually run selected tests,
and coverage profiles. Suite timing is the sum of those decoded package test
elapsed times; command wall time is retained as a diagnostic but does not affect
the timing gate, so cold compilation cannot masquerade as a test regression.
Packages with no selected tests are excluded because their process startup
timing is too noisy to be a useful performance signal.
Only suite timing failures block the quality gate. A suite is over budget only
when its selected-test total is strictly above the suite budget. Package timings
continue to be compared with their retained package budgets. A package overrun
is a non-blocking warning that identifies its lane, package, current measurement,
baseline, budget, and amount over budget. The JSON report stores those entries
under `decision.warnings`, command output labels them `warning (non-blocking)`,
and the pull-request summary repeats them. Coverage findings show the trusted
baseline percentage, current percentage, change in percentage points, and the
significance threshold. Suite failures and coverage decisions retain relevant
package warnings for diagnosis. The complete report is written to
`artifacts/test-coverage/report.json`, with a Markdown summary beside it at
`artifacts/test-coverage/report.md`. If a lane fails, the command still runs
the other lane and writes a partial report containing stderr, raw JSON output,
and decoded failing-test output before returning failure. `make
coverage` remains a compatibility alias.

The compact `coverage/test-coverage-baseline.json` contains only repository and
package coverage aggregates, test-event counts, baseline timings and their
budgets, and policy. It
does not track source blocks, coordinates, files, or hit inventories. Coverage
is evaluated independently for each lane at repository and package scope.
Changes inside the configured significance and denominator-drift bands pass
without baseline churn. Significant improvements, package/test structure
changes, and significant denominator drift require a generated refresh;
significant regressions fail.

Use `make quality-baseline` (or the compatibility alias `make
coverage-baseline`) for an eligible refresh. Generation is sorted and
repeatable, refuses to replace a baseline after a prohibited coverage regression
or suite timing failure, and preserves every existing timing budget. A
package-only timing warning does not block an eligible coverage or structure
refresh. New timed packages receive a budget from their first
coverage-instrumented measurement. Faster measurements neither require refresh
nor ratchet a budget down.

## Pull-request quality gate

[`.github/workflows/quality-gate.yml`](../.github/workflows/quality-gate.yml)
runs for every pull request, including documentation-only changes, and its
`Pull-request quality gate` job is the required branch-protection check. It
checks out GitHub's pull-request merge SHA (the synthetic merge result), reads
`coverage/test-coverage-baseline.json` from the pull request event's exact base
commit, and passes that file as the trusted comparison baseline. It therefore
never treats a pull request's edit to its own baseline as the prior state.
Superseded runs for the same pull request are cancelled.

The job runs `make quality` once, then runs `make lint` and `make build`
separately. `build` is intentionally a compilation-only target, so neither
lint nor build starts another test run. The report's decoded test executions
supply the unit and integration test results, independent coverage, and timing
checks. Its job summary starts with a table for the quality, lint, and build
results, followed by a lane coverage and timing table. Blocking issues and
warnings have separate tables. The per-package coverage and selected-test timing
table is collapsed by default, as are command logs for failed checks. Coverage
regressions include the size of the drop and the configured significance
threshold. The summary never includes source files, coverage blocks, or hit
inventories. Complete and partial reports, plus raw setup/provisioning, quality,
lint, and build logs, are uploaded for every job outcome. Setup diagnostics are
initialized before checkout so failed downloads, checksum checks, and extraction
remain available.

The gate writes a clear final result in its summary:

- `pass` succeeds.
- `refresh_required` fails until the tracked baseline is regenerated from the
  pull request's result.
- `coverage_regression`, `timing_budget_exceeded`, and `test_failed` fail with
  their recorded diagnostics. `timing_budget_exceeded` is reserved for a unit
  or integration suite total strictly above its suite budget. Package overruns
  are non-blocking warnings.
- A missing report, failed setup, unreadable base baseline, or inconsistent
  command result is labelled `execution_failure` and fails.
- A lint or build failure also fails the same required job without rerunning
  either test lane.

GitHub branch protection must require only this job name after the workflow is
introduced; remove the old `Single-pass test quality` / coverage-only required
check rather than requiring both. The detailed multi-sample timing commands
below remain separate and are not part of this routine pull-request gate.

### Baseline refresh and exceptions

For ordinary eligible coverage, package, test-structure, or denominator drift,
run `make quality-baseline` in the pull request and include the generated
compact `coverage/test-coverage-baseline.json`. CI compares the synthetic merge
result to the trusted base first, then accepts that file only when it exactly
matches the generated current aggregates while preserving trusted timing
budgets. This avoids routine exact-coverage refresh churn while still allowing
significant intentional drift.

A significant coverage regression is strict: the base-branch comparison fails
even when a pull request changes its baseline. Fix the regression rather than
editing the baseline. A maintainer may use GitHub's force-merge or
administrative branch-protection override only as an explicit human exception;
the workflow has no automatic bypass and never creates commits.

After such an override, immediately check out the resulting `main` commit and
run `make quality-baseline-force`. This deliberately creates a new compact
baseline from the already force-merged mainline state, so it is unavailable as
a way to evade the pull-request comparison. Inspect the report and make a
separate human-authored baseline commit. The next pull request then uses that
post-merge baseline as its trusted prior. Do not use this target for ordinary
refreshes or to conceal a regression before merge.

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
