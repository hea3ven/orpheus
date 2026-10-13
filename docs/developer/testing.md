# Testing

Orpheus separates tests by behavioral scope, not by speed or whether a test writes to disk.

## Commands

- `make test-unit` runs the explicit unit lane.
- `make test` is a compatibility alias for `make test-unit`.
- `make test-integration` runs the integration lane only.
- `make quality` runs both lanes once with coverage policy; timing is **not evaluated** locally.
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

Recording-runner unit tests can verify adapter command construction and rejection
decisions without starting Git. Keep real Git contracts for behavior that depends
on refs, worktrees, merges, pushes, or patch restoration. A coverage overlap alone
is not a reason to remove a boundary test; compare the assertions and failures it
detects.

## Memory-backed application workflows

The external `cli_test` fixtures in `internal/cli/*_workflow_test.go` construct
fresh commands through `cli.NewRootCommandWithOptions`. They run real routing,
workflow services, review decisions and stores over integration-only memory
paths. Task sources, candidate contents, commands and agents are semantic fakes.
They remain integration tests because they exercise cross-package behavior.

Workflows that reach review must use the real `review.RunPipeline`, not supply
terminal pipeline outcomes. Keep real child-process
streaming, cancellation, PID, environment and Git snapshot contracts separate.

Completion and publication journeys also use the memory-backed application.
They assert operator output, completion and finalization facts, task closure,
PR content, and retry outcomes through semantic task, Git, and PR collaborators.
Real commits, pushed refs, upstream tracking, and failed pushes have focused Git
contracts.

Pull-request sync and conflict recovery use the same application fixture. Git
fakes track branch heads, conflicts, pending merges, pushes, and rollback;
PR fakes track identity, lifecycle state, and provider failures. Keep durable
checkpoint/ref, local merge, separate push, rollback, and GH argument/parsing
contracts at the adapter boundary.

See [CLI test isolation](../../internal/cli/TESTING.md) for constructor and fixture
usage.

## Real Beads relationship contracts

Beads unit and adapter-contract tests live with the implementation in
`internal/tasksource/beads`. Cross-package workflows remain in `internal/cli`.

`TestIntegrationAdapterContractBeadsRelationshipContracts` shares one initialized workspace
across independent cases. Each case creates and checks its own task IDs, with
subtest-prefixed titles and no whole-database assertions. Cases run serially in
map iteration order. The destructive schema-repair contract owns a separate
workspace. Keep environment sanitization and command translation in their
focused contracts rather than adding more database initializations.

To run one case alone:

```bash
go test -tags=integration ./internal/tasksource/beads -run '^TestIntegrationAdapterContractBeadsRelationshipContracts$/^TaskBackendCreateRecordsBlockingDependencies$'
```

## Structural membership

Integration source files use `//go:build integration`, and their top-level test bodies begin with one of the three `TestIntegration...` category prefixes above. Untagged test bodies whose names do not begin `TestIntegration` are unit tests. `internal/testlane` validates the lane convention so every top-level body is selected by exactly one lane. The build constraint is the membership mechanism; category names support review and focused runs but do not create new lanes.

## Single-pass local quality report

`make quality` reads candidate `.quality.yml` without writing it. It runs unit,
then integration once, with `-count=1`, `-parallel=1`, `-covermode=set`, and
`-coverpkg=./...`. Go uses its default package concurrency (no `-p` override).
`ORPHEUS_COVERAGE_RUN=1` fixes incidental launcher-ancestry detection so coverage
instrumentation is consistent. `make check` adds formatting, lint, and build
without rerunning either lane.

Reports distinguish command wall time (developer wait, diagnostic only) from
selected package work (the sum of elapsed times for packages that ran selected
tests, including overlapping packages). Packages without selected tests do not
contribute work. Timings are reported, but local timing is **not evaluated**;
there are no tracked machine-specific timing ceilings.

Coverage floors and refresh thresholds remain blocking. Package coverage
additions/removals require an explicit policy update. JSON and Markdown reports
are written to `artifacts/test-coverage/report.json` and `report.md`. A failed
lane does not prevent the other lane from running; failure reports retain raw
JSON, stderr, and decoded failing-test evidence.

## Updating coverage policy

`make quality-policy-update` is an explicit five-sample operation. Failed,
incomplete, or inconsistent samples prevent writes. Within a revision, selected
test counts (including per-package counts), coverage package statement totals,
selected-test package structure, and normalized commands must agree. Do not mix
scheduling modes. Only materially stale coverage floors and package structure
are updated, retaining 0.5 percentage points of lane headroom and 2 percentage
points of package headroom. Review the `.quality.yml` diff. The updater never
rewrites timing tolerances or produces absolute timing bounds.

## Pull-request quality gate

[The workflow](../../.github/workflows/quality-gate.yml) is bounded to twenty
minutes. It builds one collector from GitHub's exact tested synthetic merge,
then uses it for both the event's exact base commit and that merge. Detached
checkouts are isolated, source IDs and cleanliness are checked before and after
each collection, and the synthetic merge's first parent must be the exact base.
The historical base policy is never loaded for measurement or comparison.

Both revisions use the same tools, selectors, coverage instrumentation,
scheduling, and cache regime: fresh independent build caches, one shared module
cache, and one excluded full warm-up per revision. The first measured pair runs
base then merge. If any suite or common package exceeds its allowance, four
additional pairs alternate order (merge/base, base/merge, merge/base,
base/merge), and five-sample medians determine timing. Any failed measurement
aborts; there are no selective retries. Counts and package structure must agree
within each revision, but legitimate differences across revisions are allowed.

Whole-lane work includes new packages; common packages are compared separately.
Added/removed selected-test packages are reported. Ceilings retain three-decimal
rounding and strict `measured > ceiling` boundaries:

- Lane: base median + `max(50%, 0.5 seconds)`.
- Package: base median + `max(75%, 0.25 seconds)`.

Source identity is distinct from environment compatibility. Per-sample metadata
records tool/collector hashes and versions, effective Go configuration, CPU
identity/affinity, kernel boot identity, process limits, host memory, and all visible cgroup v2 ancestor
CPU, cpuset, memory, and PID constraints. An absent controller needs affirmative
parent-delegation evidence. Missing required files, hidden cgroup ancestors,
non-v2 resource layouts, or incompatible configurations leave timing
**unevaluated**, blocking CI without claiming a regression.

Sample metadata is published before tests start, command streams are written as
they arrive, and atomic report replacement preserves the last complete report.
In-progress comparison reports retain first-pair coverage even if confirmation is
interrupted. Candidate coverage is assessed against candidate policy from the first complete
post-warm-up candidate sample. Confirmation cannot replace those findings with
later coverage. Tests/execution, coverage, and timing have independent report
statuses. Coverage regression and refresh requirements still block even when
timing passes. Timing is `pass`, `timing_budget_exceeded` only after confirmation,
or `unevaluated` when evidence is missing, failed, incomplete, or incompatible.
Routine local timing instead reports `not_evaluated` and is non-blocking.

Lint and build run on the tested merge without rerunning tests. The workflow
retains available warm-up/sample reports, metadata, profiles, raw command streams,
setup/command diagnostics, and failure evidence as short-lived artifacts, including
on failure or cancellation. No artifact is input to a later run. The comparator
also has a sixteen-minute process deadline, with process-group cancellation, to
leave time for enforcement and upload. CI never edits coverage policy.

Comparator decision/resource tests are hermetic units. Focused local Git and
controlled-process adapter contracts prove detached checkout identity, isolation,
collector invocation, partial-report retention, workflow enforcement, and cancellation without real
model agents, credentials, networks, or operator state.

`make coverage-audit` remains an on-demand command. With no selectors, it explicitly profiles every integration top-level scenario and reports runtime, containment in the full integration profile, similarity to other selected scenarios, and exclusive statements. It is too expensive for routine pull requests and is never part of `make quality` or CI.

Reviewers can select packages and a Go test-name regular expression before any per-scenario profiles start:

```bash
make coverage-audit COVERAGE_AUDIT_ARGS="-audit-package ./internal/git -audit-run '^TestIntegrationAdapterContract'"
```

Repeat `-audit-package` for more than one package. For example:

```bash
make coverage-audit COVERAGE_AUDIT_ARGS="-audit-package ./internal/git -audit-package ./internal/tasksource/beads -audit-run 'Publication|BeadsRelationship'"
```

The unfiltered full-suite audit remains explicit:

```bash
make coverage-audit
```
