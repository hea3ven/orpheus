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

`make quality` is the routine local and CI command. It reads `.quality.yml` and
never writes it. The command runs the unit and integration lanes serially once
with `-coverpkg=./...`. The decoded `go test -json` streams provide test
outcomes, failure evidence, test-event counts, package timings, and coverage
profiles. Suite timing is the sum of package elapsed times for packages that ran
selected tests. Command wall time remains diagnostic only, so compilation time
does not count against the test ceiling.

The policy has a coverage floor and suite timing ceiling for each lane. It also
has a coverage floor for every production package and a timing ceiling for every
package that runs selected tests. A value below a coverage floor or above a
timing ceiling fails. Movement inside the configured refresh bands passes.
Movement that crosses a refresh threshold without violating a bound returns
`policy_update_required`. Package additions and removals require the same
explicit update.

The complete JSON report is written to
`artifacts/test-coverage/report.json`, with a Markdown summary beside it. If a
lane fails, the command still runs the other lane and writes a partial report
with stderr, raw JSON output, and decoded failing-test output. `make coverage`
remains a compatibility alias.

## Updating the policy

Run `make quality-policy-update` when routine quality reports stale bounds or
when a reviewed regression needs new bounds. The command runs five complete,
coverage-instrumented samples of each lane. It requires matching test counts,
coverage package structure, and selected-test package structure across all five
samples. Any failed, incomplete, or inconsistent sample stops the update before
`.quality.yml` is written.

The updater uses median suite and package timings. It changes only bounds whose
refresh threshold was crossed, plus package additions and removals. Coverage
floors retain 0.5 percentage points of lane headroom and 2 percentage points of
package headroom. Timing ceilings retain the greater of 25 percent or 0.5
seconds for suites, and the greater of 50 percent or 0.25 seconds for packages.
Bounds may move up or down. Review the resulting `.quality.yml` diff before
committing it.

## Pull-request quality gate

[`.github/workflows/quality-gate.yml`](../.github/workflows/quality-gate.yml)
runs for every pull request against GitHub's synthetic merge commit. The job
runs `make quality` once, then runs lint and build without rerunning tests. The
checked-in `.quality.yml` from that merge commit is the only quality policy.
There is no base-branch policy comparison or automatic policy edit.

The report status is one of:

- `pass` when tests and bounds pass and no bound needs refreshing;
- `policy_update_required` when measurements or package structure crossed a
  refresh threshold;
- `coverage_regression` when a lane or package is below its coverage floor;
- `timing_budget_exceeded` when a suite or package is above its timing ceiling;
- `test_failed` when a lane or policy-update sample did not complete.

A missing report, malformed policy, failed setup, or inconsistent command result
is an execution failure. Lint and build failures also fail the required job.
The workflow publishes the current report and command diagnostics as short-lived
artifacts, but no report becomes input to a later run.

`make coverage-audit` remains an on-demand command. It profiles each integration
top-level scenario separately and reports runtime, containment in the full
integration profile, similarity to other scenarios, and exclusive statements.
It is too expensive for routine pull requests.
