# Testing

Orpheus separates tests by behavioral scope, not by speed or whether a test writes to disk.

## Commands

- `make test-unit` runs the explicit unit lane.
- `make test` is a compatibility alias for `make test-unit`.
- `make test-integration` runs the integration lane only.
- `make quality` runs both lanes once with coverage policy; timing is **not evaluated** locally.
- `make check` formats code, runs the single-pass quality report, lints, and builds the CLI. Each test lane still runs only once.
- `make timing-check BASE=<ref>` explicitly compares a working-tree snapshot with a fresh measurement of that exact base on the local host. It is never part of routine checks.

## Test value and ownership

Prefer coherent journeys through public application behavior. Keep focused unit
and adapter tests for distinct contracts such as parsing, calculations, ownership,
failure recovery, and persistence. Do not mirror each production function or
branch with a test, or repeat a successful workflow at several layers.

Before adding a test or table case, identify:

1. The observable contract it protects, including relevant state and external effects.
2. A plausible incorrect implementation that its assertions would reject.
3. The existing test that owns that behavior, and whether extending it is sufficient.
4. Any distinct contract that justifies a separate test or a different layer.

Extend the existing owner when the case protects the same contract. Group related
inputs in a table when they share an operation and assertions, with each outcome
named clearly. Keep consequential setup and expected values visible. Do not merge
unrelated behaviors into a large test or fixture just to reduce test counts.

Compare assertions and failure modes, not executed lines or test counts. Before
removing a duplicate, identify the test retaining each consequential assertion;
keep any distinct protection that has no replacement. Reviewers should consider
extension, consolidation, and removal as well as additions. Every request for an
additional test must name the missing regression and explain why existing tests
would miss it. Put relevant reasoning in the change or review summary, without
requiring justification comments on every test.

Coverage is a diagnostic and a blocking safety floor, not a reason for a test to
exist. When a floor fails, first look for missing behavioral protection. If a
deliberate removal changes coverage without losing a meaningful contract, explain
that trade-off and follow the [explicit policy update](#updating-coverage-policy)
process. Do not pad the suite, retain dead APIs for coverage, or lower floors
automatically.

## Behavioral assertions

Call the production operation and assert its observable results against
independent expectations. Do not generate expected output with the production
renderer or repeat the implementation's algorithm in the assertion. Prefer
semantic output checks over full snapshots of incidental wording, colors, or
column positions. Exact output and arguments are appropriate when their format
is itself a documented command or adapter protocol contract.

Use these checks when designing assertions:

- For parsers, assert returned values, not only a nil error. For lists and output,
  assert expected items and exclusions independently before comparing two views;
  two empty results can agree while both are wrong.
- For copying and ownership, mutate a copied pointee, slice, or map and verify the
  original is unchanged. Replacing a pointer does not prove a deep copy.
- For refusal, rollback, or retry, assert the error or outcome and the state whose
  preservation or recovery is part of the contract. Relevant checks include file
  bytes, reloaded persisted state, local and remote refs, and the Git index. File
  existence or an unchanged HEAD alone does not prove preservation.
- For selection, include an adversarial candidate that defeats a shortcut. A
  newer incompatible run after the expected run makes "choose the last run"
  fail when compatibility is required.
- For injected failures, check error identity with `errors.Is` where applicable
  and the absence of unintended effects. Several opaque error strings through
  the same fake do not exercise distinct provider states or parsing behavior.
- For process identity, ordering, and cancellation, use controlled handshakes,
  the child's reported PID, and bounded waits. A sleep or a positive PID alone
  does not prove the claimed lifecycle behavior.

Fakes replace external effects, not the production decision under test. A fake
that returns a terminal workflow outcome cannot prove that workflow, and a no-op
collaborator cannot prove a concrete filesystem or process isolation boundary.

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

Recording-runner unit tests can verify exact arguments when command translation
is the adapter's boundary contract. Do not prescribe an internal sequence of Git
commands or script every response to claim that stateful safety is proven. Keep
real Git contracts for behavior that depends on refs, the index, worktrees,
merges, pushes, or patch restoration. A coverage overlap alone is not a reason to
remove a boundary test; compare the assertions and failures it detects.

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

## Explicit local timing comparison

Run from the repository root with an explicit locally available revision:

```sh
make timing-check BASE=main   # Or a tag, full commit ID, or HEAD.
```

The supplied base is resolved once to an exact commit; there is no merge-base
lookup, remote fetch, or substitution of main. The candidate captures raw on-disk
bytes, executable modes, and symlinks for paths tracked in HEAD or the index,
plus non-ignored untracked files. Missing paths are deletions; disk contents win
over staged versions, even for staged additions/deletions. Repository, local,
and global ignore rules exclude only untracked paths. Avoid editing during
capture: files are read in turn, not atomically across the checkout. Later edits
cannot change the snapshot. Submodules, nested repositories, special files, and
symlinked tracked parent directories are unsupported and fail clearly.

The checkout, index, refs, and worktree registrations remain read-only. A private
repository holds both detached measurement checkouts, with Git filters and
line-ending/encoding conversions disabled. One collector is built from the
immutable candidate and its `.quality.yml` supplies policy; base policy is never
loaded. The CI engine handles sampling, coverage, environment validation,
thresholds, and reporting. Source, test, package, and policy differences are
allowed across subjects, while tools, Go configuration, and resources must match.

Prerequisites: Go, Git, Beads, Dolt, sufficient temporary disk, and Linux with
fully observable cgroup v2 ancestry. Unsupported hosts/containers leave timing
**unevaluated**, not passed. Tools are not provisioned; module downloads may need
network access, as in CI, but tests remain network- and credential-free. Each
invocation has fresh independent build/fixture-binary caches, a shared module
cache, and fresh base evidence; no persistent baseline is reused. Two excluded
warm-ups and one measured pair cost eight lane runs. Suspected violations add
four alternating pairs (twenty-four lane runs total), with five-sample medians.
Setup and collection have a sixteen-minute deadline. A busy host can produce
noisy results; never weaken policy for that machine.

Evidence is retained in a new `orpheus-timing-report-*` system temporary directory,
printed before setup. `snapshot.json` identifies source HEAD, raw tree, deterministic
snapshot commit, and the self-contained `candidate.bundle`. To restore raw bytes
elsewhere without repository filters:

```sh
git clone --no-checkout --branch candidate /path/to/candidate.bundle /path/to/new-checkout
printf '* -filter -text -ident -working-tree-encoding\n' > /path/to/new-checkout/.git/info/attributes
git -C /path/to/new-checkout checkout --detach candidate
```

Private checkouts/caches are removed on normal exit, failure, and handled
interruption; reports, streams, available samples, and the bundle remain.
SIGINT/SIGTERM cancel the active process group. Forced termination may leave
private temporary state, never operator changes. Comparisons exit zero only when
execution, candidate coverage, and timing all pass. Confirmed timing violations,
coverage violations/refresh needs, failed tests, and execution/evidence failures
exit nonzero. Invalid evidence leaves timing **unevaluated**, not a regression;
confirmation never replaces first-sample candidate coverage.

For custom output, run the collector with `-compare-worktree -compare-base <ref>
-output /outside/new-directory/report.json`. The containing directory must be new
and outside the checkout and its Git metadata; the system temporary directory
must also be outside those roots.
Remove retained reports explicitly when no longer needed; later runs never read them.

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
scheduling, and cache regime: fresh independent Go build and fixture-binary
caches, one shared module cache, and one excluded full warm-up per revision.
The compiled cross-package fixture uses `ORPHEUS_TEST_BINARY_CACHE` only when
supplied by the comparator; otherwise its binary remains test-owned temporary
state. Go validates build inputs on every sample, while the revision-local
output path avoids relinking unchanged inputs after warm-up. Each sample still
executes the binary and checks a fresh coverage profile.
The first measured pair runs
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
