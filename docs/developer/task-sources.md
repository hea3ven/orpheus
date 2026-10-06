# Task sources

Adapters live in `internal/tasksource/{beads,gig}` and implement the contracts in
`internal/task`. Registry translation supplies repository identity, prefix,
source kind, storage directory, and maintenance ownership. The CLI chooses the
adapter. Workflow, readiness, status projection, and execution history do not
import gig or Beads types.

Registration persists `task_source`. New records default to `gig` with
`task_mode: managed` and `task_prefix`. Beads records use their existing
`beads_mode` and `beads_prefix` fields. A missing discriminator means Beads only
for existing records. This fallback does not rewrite task data. Source switching
and task-data migration are not supported.

## Embedded gig

The production module pins the unmodified `github.com/NeerajG03/gig` v0.7.0 SDK
and requires Go 1.26.1 or newer. Its SQLite driver is pure Go. No C toolchain,
gig CLI, server, hooks, or global gig config is required. The adapter uses public
SDK methods, not its own SQL queries, schema management, or event persistence.

Registration initializes a database in an exclusively created managed directory.
Initialization failures remove only that attempt's directory. Existing directories
are neither adopted nor deleted. If saving the registry fails after initialization,
the command reports the unregistered storage path for inspection before retrying.

Every operation opens and closes an SDK store. Before opening existing storage,
the adapter rejects missing, empty, and nonregular database files. Paths containing
`%`, `?`, or `#` are rejected before filesystem changes or SDK calls because the
SDK builds file URIs without escaping paths. The SDK owns schema migrations,
including migrations during reads. The former adapter application marker does
not need removal; existing task data remains usable without a local reset.
Replacing or deleting storage between filesystem preflight and SDK opening is
unsupported external interference, not a race the adapter can prevent.

## Mapping and native behavior

Gig-native fields remain native. Design, acceptance criteria, external reference,
owner, and supplemental start/completion timestamps live under `orpheus.gig`.
Git facts use `orpheus.branch`, `orpheus.worktree`, and `orpheus.pr_url`. Updates
preserve unrelated metadata and unknown fields within the adapter namespace.
Execution, review, and usage history remain in Orpheus task state.

IDs and source events belong to the SDK. Child IDs use its hierarchical suffixes.
Native persisted timestamps have second precision; supplemental metadata timestamps
can retain finer precision. `List` supplies supported type/parent filters, then
`task.ListFilter.Matches` applies date and case-insensitive ID/title filtering.
SDK `Search` is not equivalent because it searches descriptions rather than IDs.

Relationships come from `Children`, `ListDependencies`, and `ListDependents`.
`from_id` depends on `to_id`; only `blocks` edges count as blockers. Parent links
remain separate. Pair-based dependency edits reject nonblocking edges rather
than overwriting or deleting them. Shared services retain graph validation.
Only the pinned SDK's exact `Get` absence error maps to `task.ErrNotFound`.
Orpheus mutation preconditions return `task.ErrMutationConflict`. Required
relationship-read failures fail the operation rather than returning partial detail.

Dispatch uses `UpdateStatus`, not `Claim`, after persisting branch/worktree facts.
The SDK advances an immediate open parent when a child starts, without recursively
starting ancestors. The adapter does not suppress this behavior or invent a start
timestamp for an unobserved automatic transition. Existing Orpheus dispatch and
epic-start gates still require the immediate parent to be in progress, so normal
commands generally do not exercise automatic parent advancement.

`CloseTask` refuses nonterminal children and opens native blocked dependents once
all blockers are terminal. Gig treats cancelled as terminal; Orpheus still requires
closed prerequisites and direct children. Current commands do not create cancelled
or native blocked states. Orpheus projects blocking from dependencies instead.
Manually introduced native statuses do not gain additional shared workflow
semantics. These differences are accepted for evaluation, without changing Beads.

## Managed writers and partial operations

The Orpheus mutation lock serializes participating managed writers. Lock coverage
is incomplete; some entry points do not participate. Expanding that coverage is
separate work, not an adapter transaction guarantee. Direct external writes are
unsupported unless the operator accepts responsibility. Concurrent uncoordinated
metadata updates can overwrite each other. Reads assembled from multiple SDK
calls are not transactional snapshots.

Creation plus dependencies, edits, and dispatch involve multiple SDK calls.
Locks do not roll back completed calls after a later failure or process exit.
Preflights catch invalid references and conflicting edge types before writes,
but later failures can leave partial results:

- A creation failure after insertion returns the created ID and instructions to
  inspect and repair it with `task edit`, rather than repeat creation.
- An edit can persist content or some edges before another edge fails. Inspect
  the item; retrying the same additions/removals is safe for unchanged blocking
  relationships.
- Dispatch saves execution pointers and the start-attempt timestamp before status.
  A failed status write can leave these on an open task. A matching retry keeps
  the timestamp and completes the transition.
- Closure can commit before automatic unblocking fails. Inspect both the task and
  its dependents. A no-op closure retry does not replay failed secondary effects.

Source creation and Orpheus review-history persistence are not exactly once.
A crash or history-write failure can leave an unrecorded follow-up task. Inspect
the source before repeating follow-up creation to avoid duplicates.

SDK methods have no context parameter. The adapter checks cancellation between
calls but cannot directly cancel an in-flight SDK call. Physical storage failures
and malformed adapter metadata remain errors. Required IDs, titles, and creation/
update timestamps are checked after decoding, but the SDK tolerates some malformed
labels and optional timestamps as empty values. Some secondary effects and event
writes have weaker error reporting. The adapter does not duplicate its decoder or
repair SDK internals to eliminate these limits.

## Validation ownership

Gig adapter contracts exercise isolated SDK storage, mapping, native lifecycle
effects, migrations, missing/corrupt storage, and recovery from partial operations.
Test-only SQL injects faults or corruption; production uses only the SDK. CLI
workflows retain semantic sources and simulated agents/services for registration
through completion and sync in mixed gig/Beads workspaces. Binary coverage checks
production embedding without task-source executables. Neither lane uses network
services. See [testing](testing.md) for lane rules.
