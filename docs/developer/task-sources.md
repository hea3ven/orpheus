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

`repo add --prefix` selects the prefix for new managed storage and bypasses the
prefix prompt. Otherwise the shared registration wizard prompts interactively or
uses the repository ID noninteractively. Local Beads prefixes remain authoritative;
an explicit value must match. Registration validates selected prefixes and registry
collisions before initializing storage, passes the selected prefix to initialization,
and persists it for subsequent task-source construction. Managed Beads prefix
validation rejects values its CLI would rewrite, avoiding registry/storage drift.

## Cross-repository dependencies

Shared services resolve prerequisite IDs against all registered prefixes, reject
ambiguous ownership, and traverse the owning sources before adding graph edges.
Cross-repository references are stored on the consumer under
`orpheus.cross_repository_blocks` in task-source metadata. Beads and gig expose
these IDs alongside native blocking edges. No proxy items or execution-state
relationship records are created. Existing source data needs no migration.

Views read prerequisite lifecycle facts from their owning source, never from a
consumer-supplied related row. Repository and content filters select output only;
referenced context remains available without listing excluded repositories.
Only closed prerequisites satisfy readiness. Removal uses the consumer's stored
IDs and does not require access to the owner. Native dependents remain local;
there is no cross-repository reverse index or source-native automatic unblocking.

## Cross-repository epic membership

A remote parent is stored on its child under `orpheus.cross_repository_parent`.
Native local parents remain native; reparenting replaces the previous link rather
than adding another parent or changing repository ownership. Both adapters project
this metadata into `Relations.ParentID` and apply parent filters after projection.
Clearing a remote link does not require its owner's source to be available.

Shared readers discover direct children across every registered source. There is
no reverse membership index. Epic views retain context separately from output
candidates; incomplete inventories mark progress incomplete, and closure requires
all inventories plus all declared direct children to be available and closed.
Graph validation distinguishes activation from completion: children wait for
parent activation, while epic completion waits for child completion. This rejects
mixed parent/dependency waits without rejecting ordinary membership.

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
states. `task block` and `task unblock` use native blocked status for tasks and epics.
Status projection recognizes native `blocked` as Blocked,
as well as blocking from dependencies and parent gates, without adding lifecycle
mutations. Other manually introduced native statuses do not gain additional shared
workflow semantics. These differences are accepted for evaluation, without changing Beads.

## Explicit blocking

The workflow blocking service inspects execution/review history read-only under
the managed mutation lock, then calls the source's narrow block/unblock capability.
It accepts open or inactive in-progress work, including attached executions whose
recorded processes are provably absent. Live or unverifiable executions and
review/publication boundaries refuse mutation. An unfinished targeted repair may
retain historical reviews. Epic blocking does not inspect or stop children; the
immediate-parent gate prevents new starts and projects waiting children as Blocked.
Initial dispatch rereads the parent's authoritative lifecycle under that same lock,
before Git setup or source/history writes, so an earlier eligibility snapshot
cannot authorize a start after epic blocking succeeds. Epic activation (`task start`)
holds that lock across its source reads, parent/dependency checks, and status write.
In-progress retries keep their existing policy.

`orpheus.pre_block_status` belongs to task-source metadata, not Orpheus state.
Beads updates it together with native status and removes it on explicit unblock.
Gig saves it before the status write alongside `orpheus.gig.block_after_event`, a
native event-ID cursor. Restoration is valid only for that single subsequent
blocked transition. Intervening native status transitions invalidate it, including
automatic reopening followed by another native block in the same timestamp second.
Missing restoration information defaults to open; an already nonblocked item is
never transitioned by unblock. No dependencies or Git facts are edited.

Gig blocking can save restoration metadata before a failed status write; retrying
block records the current lifecycle again. Unblocking writes status before
removing restoration metadata. A cleanup failure reports that unblocking already
succeeded; stale metadata cannot restore an ended blocked interval. This preserves
native automatic reopening rather than implementing a persistent manual hold.

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
