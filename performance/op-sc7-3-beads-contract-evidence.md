# Shared real Beads relationship contracts

## Scope and isolation

`internal/beads/beads_adapter_test.go` now initializes one real Beads
workspace for five independent relationship cases. The real task backend and
update service remain in use. No production code changed.

Each case creates its own tasks, prefixes titles with its full subtest name,
and uses only the returned IDs. No case assumes an empty database or reads
another case's tasks. A map varies case order. Cases run serially to avoid
concurrent database writers, and each remains selectable with `go test -run`.

The schema-repair case stays a separate top-level test with a separate database.
It still deletes the latest migration record, commits that change with Dolt,
and checks that the managed backend can read the retained task.

Both workspaces reuse the existing `isolatedBeadsRunner` helper for a temporary
HOME and XDG config root, disabled global/system Git configuration, and explicit
Git identities. Initialization and related-edge setup/readback now use that
runner too. Direct Dolt commands receive the same isolated environment.

The design HTML named in the task was absent from this checkout. This change
follows the task contract and `docs/testing.md`.

## Preserved assertions

The relationship cases share `TestIntegrationAdapterContractBeadsRelationshipContracts`.
Following the ownership repair, the adapter subtests call `beads.TaskBackend` directly.
Parent-cycle rejection remains in the task-service unit `TestUpdateServiceRejectsParentDescendantCyclesBeforeMutation`.

| Subtest suffix | Contract |
| --- | --- |
| `TaskBackendReadsParentRelationship` | Creating a child records the parent relationship, verified by a subsequent backend read. |
| `TaskBackendSupportsCrossTypeBlockingDependencies` | Task-to-epic and epic-to-task updates return exactly the requested blocking dependency. |
| `TaskBackendDoesNotRemoveRelatedDependency` | Removing a blocking dependency does not remove a related edge; real `bd dep list --json` still returns that edge and type. |
| `TaskBackendRejectsNonBlockingDependencyBeforeContentMutation` | Adding an existing related edge as blocking fails before changing the title. |
| `TaskBackendCreateRecordsBlockingDependencies` | Creating with a blocker persists the dependency, verified by a subsequent backend read. |

Environment sanitization remains covered by
`TestIntegrationAdapterContractCommandRunnerSanitizesBeadsEnvironment`. Exact adapter command
translation remains in `internal/beads/beads_test.go`, including
`TestTaskBackendCreatePassesGraphAndOptionalFields`,
`TestTaskBackendUpdateUsesOrpheusBlockingEdgeAcrossTypes`, and the related-edge
preservation and pre-mutation rejection contracts. None were removed or changed.

## Measurements

The baseline is `cde958b94ffa808df43b95734351e93143214085`. Both versions were
compiled with `go test -c -tags=integration ./internal/beads` on the same host,
using Go 1.27.1, Beads 1.2.2 and Dolt 2.3.4. Compilation is excluded. Each binary
ran with `-test.count=1 -test.v` and the exact filters recorded in
[the timing and trace summary](op-sc7-3-beads-contract-timing.json).

The measured selection includes all five relationship cases and schema repair,
not the separate workspace-initialization contracts. Runs alternated before and
after to reduce host-load bias. Both binaries received an isolated home and Git
configuration. No tracing wrappers were present in the timed runs.

| Wall time | Before | After |
| --- | ---: | ---: |
| Pair 1 | 34.133s | 21.147s |
| Pair 2 | 34.971s | 19.649s |
| Pair 3 | 33.889s | 21.136s |
| Median | 34.133s | 21.136s |

Median wall time fell by 13.0s, or 38.1%. Earlier samples varied with host load;
these are paired final-source measurements, not a new timing-policy bound.

A separate trace put forwarding wrappers for `bd`, `git` and `dolt` first on
PATH. Each wrapper invoked the previously resolved real binary with unchanged
arguments and streams, then recorded argv, cwd, elapsed time and exit code as
JSONL. This observes PATH-resolved commands, including Git calls made by Beads;
it is not a system-wide exec trace. Wrapper overhead is excluded from the wall
times above. The checked-in summary retains every Beads and Git initialization
record with normalized workspace names, plus operation counts.

| Traced commands | Before | After |
| --- | ---: | ---: |
| Relationship `bd init` | 5 | 1 |
| Schema-repair `bd init` | 1 | 1 |
| `git init` | 6 | 2 |
| `git commit` | 6 | 2 |
| All Git invocations | 387 | 259 |
| Non-initialization Beads invocations | 45 | 45 |
| Dolt invocations | 5 | 5 |
| All traced invocations | 443 | 311 |

Both traces contain 13 creates, 25 shows, five dependency commands and two lists.
The savings remove setup, not adapter exercises. The six traced `bd init` calls
totaled 22.46s before; the two remaining calls totaled 8.68s after. These totals
include wrapper overhead and nested Git time, so they must not be added to Git
durations or substituted for uninstrumented wall time.

Raw traces, compiled binaries, timing logs and the local measurement driver are
retained under `artifacts/test-coverage/beads-contracts/`.

## Validation

- All five relationship cases also pass when selected individually.
- Three complete relationship runs with `-test.count=3 -test.shuffle=on` pass
  in two observed case orders, starting with either the cycle or cross-type
  case. The map varies subtest order; Go's shuffle flag controls top-level order.
- `make check` and a separate `make quality` pass, with 1,100 unit events and
  506 integration events. Beads integration coverage remains 66.67%.
- `go vet ./...`, `go vet -tags=integration ./...` and integration-tagged
  `golangci-lint` pass.
- No policy update is required. Quality reports only a non-blocking warning that
  integration timing is below its refresh floor. Coverage bounds are unchanged.

## Ownership follow-up

The relationship contract now calls `beads.TaskBackend` directly. Parent-cycle rejection remains in `internal/task` units; real parent readback, cross-type edges, related-edge preservation, and rejection before content mutation remain in Beads. See [the ownership assertion map](op-sc7-8-contract-ownership.md).
