# Workflow work model

`internal/work` defines shared, I/O-free domain values and validation.
`internal/workstate` is the single validated, atomic persistence boundary used by
application orchestration. `internal/taskstate` coordinates typed mutations and
projects transient service/operator views; its projections are never persisted.
Task sources retain task definitions and lifecycle authority. Review-specific
captured requirements belong to the immutable review scope. Configuration,
progression, prompting, Git/PR discovery, and external observations stay with the
existing services; there is no shared phase controller or persisted cursor.

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
follows publication straight to finalization. Implementation owns ordinary retained
code commits. Publication consumes already committed reviewed output, pushing the
task branch or merging/pushing the integration destination. Finalization
owns task-source closure and cleanup, not another publication. The destination
can differ from the registered default branch.

Phase, review-step, process-result, output, and assessment alternatives are closed
Go interfaces. There are no generic `Interaction`, `Operation`, `Facts`, or
one-element step wrappers. Only task review has pipeline steps. Branch syncs are
concrete branch-maintenance history, not another review pipeline. Fixed display
names and aggregate statuses are not stored.

Output belongs to its implementation. Findings belong to task-review steps;
there are no external comment, thread, or finding records. Reported finding text
and classification stay immutable. Manual steps retain findings while unanswered
and after approval, requested changes, or abort; answering does not move finding
addresses. New findings may append only while that manual step is unanswered.
Approval cannot accept its own unresolved required findings or override another
failed requirement.

`Dispositions` retains append-only decisions; `Disposition()` derives the last
choice. Advisory promotion is a dated decision, not a classification rewrite.
Reasoned choices require nonblank reasons and ordered observation times. Only the
initial reported `RepairRequired` choice may lack a time; later reopenings must be
dated. Separate-task findings may retain an immutable authored `FollowUpProposal`
and a once-recorded `CreatedFollowUp` with the observed native task ID/time.
Proposals are future intent, not snapshots of task-source definitions. Creation
does not copy lifecycle authority away from the task source.

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
an earlier success. Missing slots and unresolved process outcomes remain pending.
Invalid evidence is an error, not approval.

- Failed checks block unless explicitly waived with a nonblank reason and time;
  the failed exit remains intact and the effective result is waived, not passed.
- A successful reviewer exit satisfies its slot unless recorded findings require
  changes, matching the application's current behavior. No separate report
  submission is required or fabricated. Extra report narratives and explicit
  per-finding verifications are not currently collected; their model fields stay
  empty. Process success does not erase blockers or explicitly unresolved findings.
  Reviewer failures cannot be waived into successful evidence.
- Manual approval cannot override another failed requirement or accept its own
  unresolved required findings. Its step retains advisory/separate-task findings
  and later decisions without rewriting the original assessment.
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

A publication plan selects a review and inherits its output and captured requirements.
An approved manual step may establish the final observed destination and flow
without rerunning automated slots. Its `ApprovedReview.Publication` records that
actual choice; the original automated scope/destination remains immutable. Blocked
or aborted tentative edits are discarded. The plan inherits this effective
approved destination, or the automated destination when there is no override. It owns the chosen PR/create-update or direct-merge target and
authored text. Review success is not publication consent.

`AuthorizePublication` records the first explicit grant and evidence. Its computed
`IntentDigest` binds repository/task, workflow/publication address, reviewed
scope/output/contract, baseline, target, content, and grant. This is an intent
seal, not a generated PlanID or proof of who supplied the operator evidence.
It binds both the automated scope destination and any approved final destination.
Changed intent needs another occurrence and fresh consent; state-version changes
alone do not invalidate it.

Authorization requires a passing review **at the grant time**. Required results,
check waivers, owned finding decisions, and decisions reached through unresolved
verification references must already support acceptance. Future waivers cannot
authorize earlier grants. Retained consent uses that historical view, while
`ReviewOutcome` still reflects current decisions. Later dismissal/reopening does
not rewrite the original grant or delivery. A retained receipt is not permission
to ignore current blockers when performing new effects.

The CLI intentionally stops at an unconsented prepared publication plan. A passed
review or retained commit cannot authorize delivery. The separate publication
consent UX belongs to T8; there is no interim consent flag or confirmation bypass.
Backend delivery contracts can consume an explicitly authorized plan, but ordinary
`task run` does not manufacture that grant.

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
separately from its serialization format. Persistence and callback decoders
reject omitted or null required index fields before constructing values:
plain integers cannot distinguish omission from an intentional zero. Optional
fields retain `nil`; closed alternatives need explicit decoding.

### Persistence boundary

`internal/workstate.Store` serializes the domain tree directly, with explicit
`kind`/`value` alternatives, required integer addresses, and separate serialization
and persistence versions. Missing or null required addresses, unknown fields,
ambiguous alternatives, and multiple YAML documents are rejected. Optional index
pointers preserve the difference between absent and explicit zero.

`Save` holds a document-specific lock, independently reloads the prior state,
checks the expected version and `ValidateSuccessor`, and atomically replaces the
file only after validation. It returns the next version without mutating its input.
This lock can be held inside the application's progression lock.
`workstate.Encode`/`Decode` expose the same validated codec without advancing the
persistence version or choosing progression.

Only the new workflow format is supported. There is no old-format reader,
converter, import path, or historical exception to domain requirements. Before
using it, the operator archives or discards old local task-state files and starts
with empty workflow history; task definitions and lifecycle remain in Beads/Gig.
See [local workflow state](../user/tasks.md#local-workflow-state) for the storage
boundary. The application never deletes or rewrites unsupported files.

### Git output boundary

Typed `taskstate.Store` mutations record implementation callbacks at stable
parent/index addresses: the effort's own completion, direct child PID, and actual
observed exit. Terminal, pre-reset, cross-task, and wrong-kind callbacks are refused.
The first live handoff is authoritative; repeats are no-ops, not diagnostic journals.
Recording a handoff or exit neither captures output nor grants review permission.
`workflow.ImplementationService` owns capture using those independent facts.

`agentexec.AttachedLauncher` reports the direct child's exit code and observation
time only after reaping and draining output, including cancellation and PID-recording
failure. `agent.Execute` keeps that observation separate from usage and launcher
errors. Missing, conflicting, backdated, or wrong-child observations cannot become
implicit successful exits. Usage capture still runs for every started child.

The implementation service validates the live last implementation's own
completion and observed successful exit before any Git effect. Under the global
mutation lock, it consumes the prepared baseline or exact earlier input and saves
only the captured output at the loaded persistence version. A failed save leaves
Git retention available for reconciliation on retry. Explicit unchanged-input
reuse keeps its own handoff and inherits the original producer's retention.
Already captured output is reverified without another commit or state write.

`git.CaptureRevision` takes that effort's exact input commit, attached branch,
completion message, and effort-owned ref namespace. The adapter does not itself
authenticate a handoff or grant review/publication permission.

Capture stages completed output and invokes ordinary `git commit`, retaining the
input and actual result under owned refs. Hooks, signing, author configuration,
and content filters apply normally. Unchanged output retains the actual input
without an empty commit; the caller must preserve the effort's own completion and
choose domain reuse only when that input addresses earlier implementation output.

A stable effort-specific reflog action and input checkpoint distinguish recoverable
interrupted commits from unrelated branch movement. Recovery requires one ordinary
commit on the recorded input, the matching branch/reflog, and a verified checkout
and index. Missing proof, unfinished Git operations, or hook leftovers stop review.
Commit failures are reported even if reconciliation finds a retained commit.
Hook leftovers are preserved for inspection; only a verified checkout matching
the retained commit can authorize review.

`git.VerifyRevision` checks the recorded commit, branch attachment, retention ref,
index tree, tracked content and executable/symlink modes, and unignored untracked
files. Hidden index flags and submodules are refused rather than treated as clean.
`review.CaptureCommittedCandidate` composes that admission with verified raw-file
and index rollback, including canceled reviewers. The application pipeline uses this committed candidate for every check, reviewer,
and manual command. Agent context exposes full-task baseline/output and optional
repair-delta input/output endpoints, together with captured requirements.

`git.VerifyRetainedRevision` verifies retained commit/tree and owned-ref identity
without requiring the checkout to represent that revision. It does not establish
a safe review checkout.
