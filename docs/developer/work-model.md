# Workflow work model

`internal/work` defines shared, I/O-free domain values and validation. Existing
services and task-state storage have not adopted them. This package does not
change CLI behavior or operator records. Persistence, conversion, configuration
resolution, progression, prompting, PR discovery, and external effects stay with
consumers. Task title, description, and tracking metadata remain in the task
source and are read there by consumers; workflows do not retain a task-definition
snapshot. Review-specific captured requirements remain part of the review scope.

## One ownership tree

```text
State.Workflows[]
  Workflow
    ReviewContracts[]                  immutable required slots/configuration keys
    Phases[]                           ordered occurrences of started work
      PreparationPhase                 one established context and task baseline
      ImplementationPhase              one feature/repair agent effort
        Execution / Completion / Output
      ReviewPhase                      captured contract and exact review scope
        Steps[]                        actual check/agent/manual attempts
      PublicationPhase                 plan, direct consent, observed delivery
      ExternalReviewPhase              existing PR, waiting or maintaining its branch
        BranchSyncs[]                  append-only synchronization efforts
          Checkpoint                   local/remote heads and incoming merge source
          ConflictResolution?          actual files and agent execution, if needed
          LocalCommit? / Push?         local result and independent remote confirmation
          Rollback?                    verified restoration after failure
      FinalizationPhase                merge/delivery prerequisite, closure, cleanup
```

PR work follows publication, external review, then finalization. Direct merge
follows publication straight to finalization. Publication owns committing and
pushing the initial PR or merging/pushing the integration destination. Finalization
owns task-source closure and cleanup, not another publication. The destination
can differ from the registered default branch.

Phase, review-step, process-result, output, and assessment alternatives are closed
Go interfaces. There are no generic `Interaction`, `Operation`, `Facts`, or
one-element step wrappers. Only task review has pipeline steps. Branch syncs are
concrete branch-maintenance history, not another review pipeline. Fixed display
names and aggregate statuses are not stored.

Output belongs to its implementation. Findings belong to task-review steps;
there are no external comment, thread, or finding records. Reported finding text
and classification stay immutable. `Dispositions` retains append-only decisions;
`Disposition()` derives the last choice. Reasoned choices require nonblank reasons
and ordered observation times. Only the initial reported `RepairRequired` choice
may lack a time; later reopenings must be dated.

Plan, consent, and initial delivery belong to publication. Branch context and PR
identity follow that owner; each sync retains only its changing Git inputs and
results. There are no parallel output/finding/publication registries or producer
backlinks to keep synchronized.

## Addresses, not generated identities

Required positions are ordinary zero-based `int` values; zero selects the first
element. Optional positions use `*int`, with `nil` meaning absent. Optional whole
references use pointers such as `*OutputRef`, not optional fields inside them.
Negative, out-of-range, wrong-kind, and forward references are rejected.

`OutputRef` addresses an implementation phase. `FindingRef` adds required review
step and finding positions. `ExecutionRef` selects implementation, a review step,
or a branch sync's resolver. Its optional `Step` and `BranchSync` are mutually
exclusive. PR identity is the native provider URL; repository/task IDs, native
sessions, and Git commit/tree/ref values retain their external meaning.

Referenced lists are append-only: never sort, insert before, compact, delete, or
reuse an address. Reset appends a workflow. An implementation retry appends a
phase; a review retry appends a step for its configured slot; another branch-sync
effort appends a sync. Required contract slots are not placeholder started work.

`ActiveWorkflow` derives the last workflow and recorded closure. `LastPhase`
never falls back from empty current work to an earlier occurrence. Child callback
addresses include repository/task, workflow, phase, and the appropriate step or
sync position. Only the current running agent, pending last check, or unanswered
last manual assessment accepts new results. Earlier, terminal, and pre-reset
child tokens fail. A superseded workflow may retain its pending manual/check tail,
but not a running process or unresolved branch sync. No cursor is persisted.

## Review and feature output

`ReviewOutcome` validates the scope and derives the result from the captured
contract and the **last attempt for every required slot**. It never substitutes
an earlier success. Missing slots and reports remain pending. Invalid evidence
is an error, not approval.

- Failed checks block unless explicitly waived with a nonblank reason and time;
  the failed exit remains intact and the effective result is waived, not passed.
- A reviewer must exit successfully and supply its report. Process success does
  not erase blockers or explicitly unresolved findings. Reviewer failures cannot
  be waived into successful evidence.
- Manual approval cannot override another failed requirement. Its alternative
  cannot also carry requested-change findings.
- Config edits append contracts for new work; they do not reinterpret historical
  approvals or turn a successful prefix into a complete pipeline.

A handoff may arrive before process exit. Output capture, including explicit
unchanged-input reuse, needs that effort's own valid completion and safe successful
exit with an explicit code/time. Review entry must follow the safe boundary of
both evaluated and delta output, including every effort in a reuse chain. Reuse
must name the effort's exact earlier input, not borrow another effort's handoff.

Code output retains native Git commit/tree identity and an owned retention ref.
Completion details, requirements, finding text, publication text, and evidence
are strings. Required text must be nonblank; optional narratives use empty strings
for absence. There is no content-store interface or report/plan artifact output.

`FeatureCompletion` selects the first safely captured implementation handoff;
repairs keep their own rationale. `LatestOutput` selects captured implementation
output independently of delivery: local R3 can coexist with PR R2. Branch sync
commits are not feature completion or newly reviewed implementation output.
`RepairPhases` derives reverse navigation from repair targets. Per-finding local
verification requires an explicit authoritative statement, not an inferred flag
from a whole-task pass or PR merge. Later authoritative negative evidence can
reverse an earlier positive statement.

## Publication consent

A publication plan selects a review and inherits its output, requirements, scope,
and destination. It owns the chosen PR/create-update or direct-merge target and
authored text. Review success is not publication consent.

`AuthorizePublication` records the first explicit grant and evidence. Its computed
`IntentDigest` binds repository/task, workflow/publication address, reviewed
scope/output/contract, baseline, target, content, and grant. This is an intent
seal, not a generated PlanID or proof of who supplied the operator evidence.
Changed intent needs another occurrence and fresh consent; state-version changes
alone do not invalidate it.

Authorization requires a passing review **at the grant time**. Required results,
check waivers, owned finding decisions, and decisions reached through unresolved
verification references must already support acceptance. Future waivers cannot
authorize earlier grants. Retained consent uses that historical view, while
`ReviewOutcome` still reflects current decisions. Later dismissal/reopening does
not rewrite the original grant or delivery. A retained receipt is not permission
to ignore current blockers when performing new effects.

Publication is an idempotent application action: find/verify a matching PR,
import it when present, and create only when confirmed absent. Failed lookup is
not absence. Unchanged retries establish delivery under the same intent/consent,
without a command/API attempt journal. Initial PR delivery preserves the reviewed
tree and intended prior public head. Direct delivery requires observed destination
push, not only a local merge.

## External review and branch sync

External review retains the published PR reference and actual `BranchSyncs`, not
PR polling history or future feedback features. An empty array is ordinary waiting
for merge. Current sync may make a clean merge or invoke the conflict resolver;
those are alternatives within the same branch-sync concept.

Each sync records its starting local/remote heads and destination merge source.
Optional conflict resolution owns the files and native `AgentExecution`, including
process/session/usage/cost facts and uncertainty. Purpose follows containment:
this is branch maintenance, not a feature implementation or agent completion.
Current sync does not rerun task review or obtain another publication decision,
and its result must not be presented as having passed the original task review.

`LocalCommit` records the observed local result, including an already-current
head. `Push` independently confirms that exact head remotely. Their timestamps
are observation times, not Git author dates. A missing/failed remote lookup is
not successful delivery. Retrying a push completes that same entry; a new merge
or resolver effort appends another entry. Earlier checkpoints, files, execution
identity, and results remain history.

Verified rollback records the actual restored local/remote heads and must match
the checkpoint. It cannot coexist with a confirmed push. A failed resolver or
incomplete push remains unresolved until safely pushed or rolled back; later
syncs, phases, and reset cannot strand that work. Only the last sync of the live
last phase may be unfinished. Supplemental telemetry may still fill unknown
measurements without rewriting known ones.

## Finalization

PR finalization records the minimal implemented prerequisite in `PRAcceptance`:
the provider confirmed that the PR attached to the selected external-review phase
merged. No provider head, merge commit, approval, comment, or thread state is
invented. Confirmation must follow publication and settled branch maintenance;
a superseded publication/stage cannot authorize closure. Closed-unmerged PRs must
be refused by application reconciliation, not converted into merge confirmation.

Direct finalization addresses publication's observed destination push and needs
no external-review phase. Both flows retain actual task-source closure and later
cleanup observations. Merge/delivery confirmation alone does not claim the source
was closed. Dirty/retained worktrees do not undo closure, and cleanup can append
after closure or reset without repeating publication.

## Adoption boundaries

`Validate` checks a snapshot. `ValidateSuccessor` additionally guards referenced
history, frozen contracts/scopes/intent, known execution facts, handoffs, and
results. It permits current work to finish, new work/decisions to append, unknown
telemetry to fill, and later cleanup. Supply an **independent** prior snapshot;
shared-pointer mutation cannot be detected after the evidence was overwritten.

Validation does not authenticate observations, run Git, choose the next action,
or replace expected-version atomic saves. Storage owns advancing `StateVersion`,
separately from its serialization format. Future persistence/callback decoders
must reject omitted or null required index fields before constructing values:
plain integers cannot distinguish omission from an intentional zero. Optional
fields retain `nil`; closed alternatives need explicit decoding.

Operator-data conversion remains separate, explicit work. Any unmatched source
records must be preserved outside authoritative workflow state, without inventing
approval or associations. No conversion report is part of the shared model, and
no record conversion, compatibility reader, or migration framework is introduced.
