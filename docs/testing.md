# Testing

Orpheus separates tests by behavioral scope, not by speed or whether a test writes to disk.

## Commands

- `make test-unit` runs the explicit unit lane.
- `make test` is a compatibility alias for `make test-unit`.
- `make test-integration` runs the integration lane only.
- `make quality` runs both lanes once with coverage and timing policy.
- `make check` formats code, runs the single-pass quality report, lints, and builds the CLI. Each test lane still runs only once.

## Behavioral assertions

Do not test logging. Do not add logging-only scenarios or assert log messages,
levels, structured fields, correlation identifiers, timing fields, or log
redaction. When removing logging checks from a mixed scenario, retain its
functional assertions about returned errors, operator-facing command output,
state changes, and external effects. Persisted task audit events and usage
records are application data, not logs, and remain part of the test contract.

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

## Integration review categories

The integration lane has three human-reviewed categories. They do not change test selection, quality policy, timing, coverage, or CI reporting.

### Workflow integration

A workflow integration test is a fast, almost-E2E user journey through the in-process CLI. It lives in `internal/cli`, uses `package cli_test`, and reuses the shared workflow fixtures. Names begin `TestIntegrationWorkflow` and files end in `*_workflow_test.go`. Real command routing, services, review pipelines, and memory-backed stores compose the application; semantic collaborators replace external effects. Do not add a second service-level workflow layer. Focused owner logic belongs in unit tests, and concrete boundaries belong in owner-local adapter contracts.

`TestIntegrationWorkflowTaskRunCompletionLeavesTaskAwaitingManualReview` is the reference example. It runs the real CLI routing and workflow services while task, Git, command, and agent behavior come from semantic collaborators.

### Adapter contract

An adapter contract proves translation or behavior at one concrete boundary by calling its owning package directly. Do not call a CLI command or a higher-level service to prove a downstream adapter. Such scenarios must separate workflow decisions from the owner-local contract. Supporting imports and domain types are allowed; ownership concerns the behavior under test, not the number of packages imported. Real Git repositories, Beads databases, fake executable processes, filesystem permissions, environment propagation, streams, cancellation, PIDs, and exit statuses belong here when the assertion depends on them. Names begin `TestIntegrationAdapterContract` and scenario files end in `*_adapter_test.go`. Helper-only files do not need this suffix.

Examples include `TestIntegrationAdapterContractPublicationCommitAndPushPreserveMessageAndRemoteRef`, which checks real local Git refs and commits, and `TestIntegrationAdapterContractAttachedLauncherStreamsBeforeExitAndReapsCanceledChild`, which checks OS process streaming and reaping.

The review package owns its candidate snapshot and Hunk-command adapters. Their direct contracts remain in review even though snapshot restoration uses Git underneath. The taskbranch package owns rendered branch names and checks their compatibility against Git's grammar. Neither case requires moving the test into the Git package.

### Binary E2E

A binary E2E test compiles and starts a binary because compilation, startup, packaging, exit handling, or recursive process behavior is part of the contract. Names begin `TestIntegrationBinaryE2E`. Keep this category small. Do not use a compiled binary to retest workflow branches already covered through application composition.

`TestIntegrationBinaryE2ETaskRunUsesSeparateTaskProposalSelection` compiles `cmd/orpheus` because controlled implementer and reviewer children call back into that binary. `TestIntegrationBinaryE2ECustomNamedTestBinaryRetainsSafetyGate` compiles a custom-named Go test binary, and `TestIntegrationBinaryE2EProductionBinaryNamedTestDoesNotEnableTestMode` compiles a production probe. Both check startup-time test-process detection.

### Review checklist

First name the production operation and its owning package. For every real filesystem or executable dependency, ask which assertion would become invalid with a semantic fake. Keep the boundary only when the answer names filesystem identity, persistence, permissions, command translation, process lifecycle, or compiled-binary behavior. Otherwise move the scenario to workflow composition or a unit test. A temporary directory, a fixture script, or an existing helper is not enough justification.

A controlled executable is not sufficient justification for an adapter label. In workflows, observe semantic requests and effects; keep argv, protocol decoding, environment, streams, and process-lifecycle assertions at the concrete adapter. Preserve an exact old-to-new assertion map when splitting a mixed scenario. Do not preserve shell-command counts when the actual requirement is one logical snapshot or excluding an unrelated repository.

When one file owns one category, use a descriptive suffix such as `_workflow_test.go`, `_adapter_test.go`, or `_binary_e2e_test.go`. The top-level category prefix remains the quickest way to identify mixed legacy files and select a family with `-run`.

See [the CLI-entry migration map](../performance/op-sc7-8-cli-workflows.md) for consolidated journeys and owner-unit splits.

See [the integration curation record](../performance/op-sc7-8-integration-curation.md) for package ownership, retained boundary reasons, and the removed helper assertion map.

## Memory-backed application workflows

The external `cli_test` fixtures in `internal/cli/*_workflow_test.go` construct
fresh commands through `cli.NewRootCommandWithOptions`. They run real routing,
workflow services, review decisions and stores over integration-only memory
paths. Task sources, candidate contents, commands and agents are semantic fakes.
They remain integration tests because they exercise cross-package behavior.

Workflows that reach review must use the real `review.RunPipeline`, not supply
terminal pipeline outcomes. Keep real child-process
streaming, cancellation, PID, environment and Git snapshot contracts separate.
See [review and repair migration evidence](../performance/op-sc7-5-review-workflow-evidence.md)
for the assertion map and measurements.

Completion and publication journeys also use the memory-backed application.
They assert operator output, completion and finalization facts, task closure,
PR content, and retry outcomes through semantic task, Git, and PR collaborators.
Real commits, pushed refs, upstream tracking, and failed pushes have focused Git
contracts. See [finalization and publication migration evidence](../performance/op-sc7-6-finalization-evidence.md)
for the assertion map, contract owners, and before/after measurements.

Pull-request sync and conflict recovery use the same application fixture. Git
fakes track branch heads, conflicts, pending merges, pushes, and rollback;
PR fakes track identity, lifecycle state, and provider failures. Keep durable
checkpoint/ref, local merge, separate push, rollback, and GH argument/parsing
contracts at the adapter boundary. See [sync migration evidence](../performance/op-sc7-7-sync-evidence.md)
for the assertion map, retained contracts, and measured replacements.

## Real Beads relationship contracts

`TestIntegrationAdapterContractBeadsRelationshipContracts` shares one initialized workspace
across independent cases. Each case creates and checks its own task IDs, with
subtest-prefixed titles and no whole-database assertions. Cases run serially in
map iteration order. The destructive schema-repair contract owns a separate
workspace. Keep environment sanitization and command translation in their
focused contracts rather than adding more database initializations.

See [the Beads contract measurements](../performance/op-sc7-3-beads-contract-evidence.md)
for the assertion map, timings and subprocess counts. To run one case alone:

```bash
go test -tags=integration ./internal/beads -run '^TestIntegrationAdapterContractBeadsRelationshipContracts$/^TaskBackendCreateRecordsBlockingDependencies$'
```

## Structural membership

Integration source files use `//go:build integration`, and their top-level test bodies begin with one of the three `TestIntegration...` category prefixes above. Untagged test bodies whose names do not begin `TestIntegration` are unit tests. `internal/testlane` validates the lane convention so every top-level body is selected by exactly one lane. The build constraint is the membership mechanism; category names support review and focused runs but do not create new lanes.

## Single-pass quality report

`make quality` is the routine local and CI command. It reads `.quality.yml` and
never writes it. The command runs the unit lane once, then the integration lane
once, with `-count=1` and `-coverpkg=./...`. Within each lane, Go schedules
independent packages concurrently using its default package limit. The runner
does not set `-p`; an explicit `GOFLAGS=-p=1` remains useful for a serial control.
It retains `-parallel=1` to serialize tests marked with `t.Parallel` inside each
package until the remaining isolation work is complete. This does not serialize
goroutines started by a test.

The decoded `go test -json` streams provide test outcomes, failure evidence,
test-event counts, package timings, and coverage profiles. Reports distinguish:

- Command wall time, the developer's wait for each `go test` command, including
  compilation, scheduling, and coverage output. It is diagnostic only.
- Selected package work, the sum of package elapsed times for packages that ran
  selected tests. This is elapsed package work, not CPU time or a sum of test and
  subtest durations. Overlapping packages still contribute their full elapsed
  times, so work can exceed wall time. Packages with no selected tests contribute
  no timing, even if they appear in the coverage profile.

`.quality.yml` continues to govern selected package work and individual package
timings, never command wall time. Scheduling reduces waiting without treating
that reduction as removed test work. Resource contention can still affect
package timings. The JSON fields remain `wall_seconds`, `selected_test_seconds`
for selected package work, and `package_timings` for compatibility.

The policy has a coverage floor and suite timing ceiling for each lane. It also
has a coverage floor for every production package and a timing ceiling for every
package that runs selected tests. A value below a coverage floor or above a
timing ceiling fails. Coverage movement beyond a refresh threshold returns
`policy_update_required`. Timing movement below its refresh floor is a
non-blocking warning because execution speed varies by host. Package additions
and removals still require an explicit update.

The complete JSON report is written to
`artifacts/test-coverage/report.json`, with a Markdown summary beside it. If a
lane fails, the command still runs the other lane and writes a partial report
with stderr, raw JSON output, and decoded failing-test output. `make coverage`
remains a compatibility alias.

When comparing scheduling modes, use the same quality environment as well as
identical selectors and coverage flags. In particular,
`ORPHEUS_COVERAGE_RUN=1` disables incidental Linux launcher-ancestry detection.
Omitting it changes CLI coverage without changing test selection. The historical
21-statement difference came from that detection path, not package scheduling.
See [scheduling evidence](../performance/op-sc7-2-scheduling-evidence.md) for
controlled profiles and repeated comparisons.

## Updating the policy

Run `make quality-policy-update` when routine quality reports stale bounds or
when a reviewed regression needs new bounds. The command runs five complete,
coverage-instrumented samples of each lane. It requires matching test counts,
coverage package structure, and selected-test package structure across all five
samples. Any failed, incomplete, or inconsistent sample stops the update before
`.quality.yml` is written.

The updater uses median command wall times, suite work, and package timings.
Only suite work and package timings determine policy bounds. Samples must use
the same recorded command, including scheduling flags; do not mix serialized
and concurrent measurements in a policy refresh. It changes only bounds whose
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

- `pass` when tests and blocking bounds pass, possibly with a non-blocking
  timing refresh warning;
- `policy_update_required` when coverage or package structure crossed a refresh
  threshold;
- `coverage_regression` when a lane or package is below its coverage floor;
- `timing_budget_exceeded` when a suite or package is above its timing ceiling;
- `test_failed` when a lane or policy-update sample did not complete.

A missing report, malformed policy, failed setup, or inconsistent command result
is an execution failure. Lint and build failures also fail the required job.
The workflow publishes the current report and command diagnostics as short-lived
artifacts, but no report becomes input to a later run.

`make coverage-audit` remains an on-demand command. With no selectors, it explicitly profiles every integration top-level scenario and reports runtime, containment in the full integration profile, similarity to other selected scenarios, and exclusive statements. It is too expensive for routine pull requests and is never part of `make quality` or CI.

Reviewers can select packages and a Go test-name regular expression before any per-scenario profiles start:

```bash
make coverage-audit COVERAGE_AUDIT_ARGS="-audit-package ./internal/git -audit-run '^TestIntegrationAdapterContract'"
```

Repeat `-audit-package` for more than one package. For example:

```bash
make coverage-audit COVERAGE_AUDIT_ARGS="-audit-package ./internal/git -audit-package ./internal/beads -audit-run 'Publication|BeadsRelationship'"
```

The unfiltered full-suite audit remains explicit:

```bash
make coverage-audit
```
