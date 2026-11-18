# Workflow work model

`internal/work` defines shared, I/O-free domain values and validation.
`internal/workstate` is the single validated, atomic persistence boundary used by
application orchestration. `internal/taskstate` projects transient service/operator views and retains shared
workflow-policy and supplemental telemetry recording; its projections are never persisted.
Task sources retain task definitions and lifecycle authority. Review-specific
captured requirements belong to the immutable review scope. `workflow.Controller`
owns built-in progression; specialized services execute agents, review pipelines,
Git/PR effects, and source closure. CLI supplies task identity and flag choices,
composes collaborators, and renders semantic results. `Controller.Advance` owns
recovery, workflow-dependent option validation, and a single phase loop: select
current work, execute its phase boundary, and observe its semantic outcome before
selecting another phase. Within a phase, each operation's committed result passes
directly to its next selection. Handlers do not invoke the next phase. There is
no persisted cursor.

Phase behavior lives under `internal/workflow/{preparation,implementation,review,delivery,externalreview,finalization}`.
The parent `internal/workflow` orchestrates phases; child packages import neither
that parent nor CLI. Shared models, storage, agent execution, publication policy
and external adapters remain outside this subtree.

`internal/workflow/phaseexec` supplies the transient operation protocol and shared runner.
Required preparation saves precede effects; one ordinary result update records
observations, including when execution returns an error. The runner returns the
committed version directly. Effects that allow independent child/checkpoint
writes reload before recording their parent result. Failed saves never expose an
unsaved candidate to the next selection. `delivery.Service.Next` owns Publication
initialization, input refresh, packaging, PR/direct delivery, recovery, metadata
recording, and path-specific private-ref release. Its typed `Respond` operations
validate consent, flow changes, and restoration/manual-repair answers. Workflow
handles delivered outcomes and appends External review or Finalization; `task done`
cannot initialize Publication from Review. `implementation.Dispatch.Next` owns
implementation admission, frozen agent selection, execution reservation, attached
execution, and completion/process interpretation. It returns an admitted context
requirement to workflow, which selects Preparation before resuming Implementation.
`implementation.Service` uses this boundary for retained-output capture.
`review.Admission.Next` owns target/metadata and retained-checkout admission,
fresh pipeline selection, prior-finding input, fresh reservation, captured
requirements/destination, and fresh/resumed execution configuration. `review.Continuation.Next` captures
forward requirements from the recorded pipeline's live named position while
preserving started slots and historical contracts. Their operation results feed
selection directly with the committed version. `review.Assessments` owns typed
manual submissions and automated finding answers,
including legal choices, owner/version checks, live configuration and retained
revision validation. Workflow projects those requests and routes answers without
interpreting or applying them. Review configuration and committed-candidate
observation also live in `internal/workflow/review`. `finalization.Service.Next` selects
source closure and cleanup operations; sync consumes its semantic result while
retaining batch mutation ownership. Closure and cleanup receipts use separate
ordinary updates, so cleanup failures cannot erase closure.

`externalreview.Service.Next` owns PR observation, branch-maintenance selection,
checkpointing, resolver execution, local completion, push, and native recovery.
Workflow supplies the sync invocation's branch-update policy, handles semantic
open/updated/merged/closed-unmerged outcomes, and alone selects Finalization.
Batch sync retains its coordinated mutation scope. Its resolver checkpoints use
`Runner.CommitHeld`; operations that permit those independent writes explicitly
reload before applying their ordinary result, including a nil result update.
Resolver process and usage observations remain separate saves, in that order,
with both attempted even when one fails. Doctor consumes the same phase-owned
push and rollback changes. The old taskstate branch-sync mutation API is removed;
its projection and all retained branch-sync history remain unchanged.

`preparation.Service.Next` owns checkout setup, baseline retention, authorized
reset resolution/application, source activation, and context establishment.
Reset targets are saved before destructive effects; successful reset establishment
consumes authorization before source activation. Ordinary setup retains its
existing activation-before-context-save order. Setup and reservation operations
hold mutation ownership around their effects and use each committed result
directly. Workflow retains route authorization and repair admission/budgeting;
Implementation owns target/mode validation and its reservation change.

`phaseexec.AttachedAgents` installs the mandatory synchronous PID checkpoint for
implementers, reviewers, direct-publication resolvers, and PR branch resolvers.
It retains attached streaming, cancellation, and reaping through the existing
agent boundary. Shared process-liveness inspection lives beside it; Implementation
and Review own stale-execution reconciliation. Implementation's independent
handoff endpoint applies its phase-owned completion change in the agent command's
existing mutation scope. Its result recorder attempts usage before process facts,
even when either save fails, then the outer operation adopts independently saved
state before returning. Review owns its reviewer PID, process and usage changes.
Its shared result recorder attempts process before usage and retains both error
causes; a failed first save never suppresses the second. Read-only failure and
checkout-recovery facts remain atomic with the observed reviewer process result.
`review.PriorFindings.Next` owns fresh-review blocker decisions, validating the
complete answer before recording each disposition at its existing independent
save boundary. `review.FollowUps.Next` owns submitted proposal selection, native
task creation and receipts, creation-failure decisions, and receipt-before-report
ordering. Its next operation derives pending creation from saved finding receipts,
not a persisted queue; workflow consumes its completed or aborted outcome before
entering Publication. Both use typed `Respond` operations and return each committed
state directly to selection. Their moved taskstate finding-decision, native-receipt,
and review-abort endpoints have been removed. `review.CheckoutRecovery.Next`
reconstructs the retained rollback request; its typed `Respond` operation performs
only explicitly confirmed restoration and records its actual result. Failed or
interrupted restoration remains pending, and successful restoration still stops
the invocation without changing failed review evidence or granting approval.
Review owns read-only/admission-failure changes; the corresponding taskstate
mutation endpoints are removed.
`review.PipelineExecution.Next` selects each configured slot's ordinary operation,
typed command confirmation, Manual Assessment or automated-finding conversation,
and semantic stop outcome. `Respond` validates the specific pending conversation
and translates accepted answers into operations, not nested execution loops.
`RunPipeline` drives this boundary with the existing specialized frontend hooks.
Admission passes its committed snapshot directly into execution. The pipeline
uses the shared runner rather than a storage-backed lifecycle interface. Check reservation precedes execution, and its result and generated
findings share one ordinary update. Manual assessment entry and inspection-command
observations remain separate from submission; canceled inspection commands retain
their actual interruption observations. Reviewer reservation precedes attached
execution, and the released operation adopts independent PID, findings, process,
and usage writes. Each committed result feeds the next pipeline selection without
a post-save reload. Accepted attached answers refresh the task-source and input
identity at the existing response boundary, then use Review's ordinary response
operation for the single submission save. Only agent/checkpoint writes require
independent-write adoption. Automated finding commands commit Review-owned authored-content
changes immediately through the runner, retaining their stable step owner and the
existing mutation-lock retry boundary. They cannot write manual edits. Reviewer
recovery selects process inspection through `review.ExecutionRecovery.Next` and
records a proven interruption with one result update; an unsuccessful receipt
stops without exposing unsaved recovery or retrying the effect. Review imports no
workflow or CLI code, and taskstate no longer implements those recording paths.
Review also owns manual comparison inspection, the typed
presentation context, draft construction, and conversion of manual and automated
answers into phase submissions. It captures the manual token from committed state
after command observations; workflow only forwards typed answers through its
existing response facade. CLI retains terminal rendering and editing.

Review and Implementation lifecycle mutations are owned by their phase packages;
taskstate's old reservation, step, manual-decision, completion, PID, process and
phase-failure endpoints are removed. The explicit review evaluation composes the
same phase-owned changes. Tests seed synthetic history through `workfixture`,
which is not an application lifecycle API. No saved work fields or history were
removed, and no migration is required.

## Built-in progression

The controller derives current work from the last workflow, phase, and actual
review-step occurrence. Preparation establishes context; Implementation produces
a safely completed retained revision; Review evaluates that revision. A pass
enters Publication without granting consent. PR delivery enters External review,
which waits for explicit reconciliation; observed direct delivery skips it and
enters Finalization. Finalization uses the preceding phase's observation and
retries source closure without repeating publication. `task run` stops after
verified delivery; only sync/background processing executes External review and
Finalization, including closure and cleanup retries.

Required repairs append another Implementation in the same workflow, then a new
Review from the first step of the currently resolved pipeline. Resuming a manual
assessment and requesting changes uses this same loop; quitting the prompt leaves
that assessment unanswered rather than aborting it. Invocation budget accounting
survives every phase transition. Existing stopping,
finding-decision, and per-invocation repair-budget rules remain in force. A PR URL
cannot override controller-selected implementation or repair. Configured slot
keys/kinds remain in captured contracts; history addresses never become offsets
in a live pipeline. Continuation resolves the recorded pipeline and current
step's name/type against live configuration, then advances only after that
position. Missing identities stop with configuration-repair advice. Fixed phase
kinds identify built-in phases; there is no configurable phase graph.

For an active review, workflow selects Review's continuation boundary, which
appends a forward contract preserving the started-slot prefix and replacing only
unstarted requirements with the live suffix. Old contracts, step addresses, results, findings, and scope remain intact.
A moved earlier name encountered later can have another required slot without
relabeling its earlier attempt. Neither earlier steps nor their agent profiles
are revalidated. Publication continuation never resolves earlier review
configuration, so unchanged accepted selections remain valid. Fresh post-repair review
still captures the full current pipeline and begins at step one.

The controller derives expected owner/version preconditions internally; callers
need not inspect or supply a route before advancement. Reservation rechecks those
preconditions under mutation protection; review reservation also checks the exact
version inside its atomic mutation. Attached work runs without
the global lock, allowing child callbacks at the reserved parent/index address.
Only that owner may record completion/results. Conflicting phase/step advancement
is refused rather than attached to newer work. Interruption reconciliation remains
a separate implementation boundary.

## Frontend contract

`Controller.Inspect` (or `InspectTask` for an already loaded snapshot) returns
copied semantic context, the selected next action, and pending typed interactions.
`Advance` supplies synchronous `Progress` notifications. Inspection is read-only:
it does not reconcile processes or reserve work. Phase/step attempts and revision,
scope, execution, publication, PR, and closure facts remain workflow-owned.

`Controller.Respond` binds a typed assessment, finding batch, or publication
response to the inspected owner/kind/state version under mutation protection.
Answers persist as the existing assessment, finding dispositions, or exact-plan
consent; there is no second decision queue. Finding batches and complete per-step
manual submissions save atomically. CLI edits a pure `review.ManualDraft` rather
than a state-backed recorder. Its response token is captured after inspection
command observations and before editing; edits do not refresh it. Submitted
findings, staged promotions, and the final Manual Assessment are validated against
the combined candidate and saved once. New edits have no durable finding address
before submission; existing finding addresses remain stable. Automated findings,
command observations, and later native follow-up task receipts keep their own
recording boundaries. Pause, EOF, cancellation, or pre-submission exit discard new
draft edits without deleting existing findings or answering the assessment.
Stale/mismatched
responses cannot authorize different work. Selection changes can reserve fresh
review or append a new pending publication phase, but never executes review. A separate typed
publication-recovery response explicitly confirms restoration and retries
packaging under the original operation; ordinary consent responses have no Git
effects.
Review and publication decisions verify the exact retained checkout before saving;
publication consent also checks current publication input and destination context.
Attached review and publication prompts use this internal response boundary.
There is no public noninteractive decision command. Continuation after a recorded
answer skips that completed step.

CLI owns phase/step header repetition, labels, ANSI styling, display-width wrapping,
manual-review body sections, and rolling output tails. Review execution reports
step boundaries, stopping notices, and output dispositions, while its output port
carries raw child streams separately. A failed header settles an unstarted check
or reviewer; an unanswered assessment remains pending. Sync supplies observed PR
lifecycle facts through the same progress renderer without becoming another phase.

## One ownership tree

```text
State.Workflows[]
  Workflow
    Publication?                       cumulative commit subject/body and PR description
    ReviewContracts[]                  immutable required slots/configuration keys
    Phases[]                           ordered occurrences of started work
      PreparationPhase                 one established context and task baseline
      ImplementationPhase              one feature/repair agent effort
        Execution / Completion / Output
      ReviewPhase                      captured contract and exact review scope
        Steps[]                        actual check/agent/manual attempts
      PublicationPhase                 selected input/review/flow/destination, consent and delivery
        Operation                      recovery input ref, recorded public commit, PR creation intent
        PreparedFrom? / SyncFrom?      direct continuation references, not copied efforts
        DirectSync?                    local intent, actual public result, resolver facts
        Integration?                   destination intent and actual result before push
        PrivateRefsReleasedAt?         direct preparation / PR-delivery release boundary
      ExternalReviewPhase              existing PR, waiting or maintaining its branch
        Outcome?                       durable merged / closed-unmerged observation
        BranchSyncs[]                  append-only synchronization efforts
          Checkpoint                   local/remote heads and incoming merge source
          ConflictResolution?          actual files and agent execution, if needed
          LocalCommit? / Push?         local result and independent remote confirmation
          Rollback?                    verified restoration after failure
      FinalizationPhase                merge/delivery prerequisite, closure intent, closure, cleanup
```

PR work follows publication, external review, then finalization. Direct merge
follows publication straight to finalization. Implementation owns ordinary retained
code commits. Publication consumes explicitly authorized committed input, performing one
in-place public squash, then initial PR delivery or local destination sync and
recorded destination integration. Direct sync does not rerun task review; changed
output requires fresh consent and retains honest unreviewed provenance. Finalization
owns task-source closure and cleanup, not another publication. Initial review
entry establishes the fixed destination branch from the registered default;
subsequent default edits cannot retarget the workflow.

Phase, review-step, process-result, output, and assessment alternatives are closed
Go interfaces. The persisted model has no generic `Interaction`, `Operation`,
`Facts`, or one-element step wrappers. Only task review has pipeline steps. Branch syncs are
concrete branch-maintenance history, not another review pipeline. Fixed display
names and aggregate statuses are not stored.

Output belongs to its implementation. Findings belong to task-review steps;
there are no external comment, thread, or finding records. Reported finding text
and classification stay immutable. Manual steps retain previously recorded findings
while unanswered and after approval, requested changes, or abort; answering does
not move finding addresses. New manual findings append atomically with the
complete assessment; a Manual Review Draft is never persisted.
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

Selection, consent, and initial delivery belong to publication. Branch context and PR
identity follow that owner; each sync retains only its changing Git inputs and
results. There are no parallel output/finding/publication registries or producer
backlinks to keep synchronized.

## Addresses, not generated identities

Required positions are ordinary zero-based `int` values; zero selects the first
element. Optional positions use `*int`, with `nil` meaning absent. Optional whole
references use pointers such as `*OutputRef`, not optional fields inside them.
Negative, out-of-range, wrong-kind, and forward references are rejected.

`OutputRef` addresses captured implementation output or a review-owned revision
selection. `FindingRef` adds required review step and finding positions. `ExecutionRef` selects implementation, a review step,
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

- Failed checks block unless dispositions address their blocking findings. A
  finding waiver requires a reason and time. It does not rewrite the failed exit
  or waive the check as a whole.
- A successful reviewer exit satisfies its slot unless recorded findings require
  changes. The report stores findings only. Process success does not erase
  findings or establish that an earlier finding was verified; reviewer failures
  cannot be waived into a pass.
- Manual approval cannot override another failed requirement or accept its own
  unresolved required findings. Its step retains advisory/separate-task findings
  and later decisions without rewriting the original assessment.
- Config edits append contracts for forward continuation or fresh work; they do
  not reinterpret historical approvals. A successful prefix cannot stand in for
  pending forward requirements. Only a live review may select an appended
  contract, and every started slot must keep its name/type and address. Once the
  workflow leaves review, its contract selection is frozen.

A handoff may arrive before process exit. Output capture, including explicit
unchanged-input reuse, needs that effort's own valid completion and safe successful
exit with an explicit code/time. Review entry must follow the safe boundary of
both evaluated and delta output, including every effort in a reuse chain. Reuse
must name the effort's exact earlier input, not borrow another effort's handoff.
Fresh review retains the clean task-branch tip when it differs from the captured
implementation commit. The scope references the original output and owns the
new selection; the handoff remains on the implementation phase. Review evidence,
repair inputs, and publication decisions refer to the evaluated revision.

Code output records native Git commit/tree identity and an owned retention ref.
Private objects remain retained through review. Direct delivery releases obsolete
private refs after durable public preparation, before local sync or remote writes;
PR publication releases them after durable delivery. Public branches/checkpoints
retain unfinished output; historical
identity/ref strings remain data, not a permanent reachability promise.
Completion details, requirements, finding text, publication text, and evidence
are strings. Required text must be nonblank; optional narratives use empty strings
for absence. There is no content-store interface or report/plan artifact output.

`FeatureCompletion` selects the first safely captured implementation handoff for
review context, not publication narrative; repairs keep their own rationale.
`LatestOutput` selects the most recent captured implementation output or its
review-selected revision, independently of delivery. After initial PR publication, later work starts
on the public task branch rather than a discarded private revision. Branch sync
commits are not feature completion or newly reviewed implementation output.
`RepairPhases` derives reverse navigation from repair targets. Neither a
whole-task pass nor a PR merge changes an individual finding's disposition.

## Publication consent

Workflow owns optional `PublicationContent` (subject, body, PR description),
initialized by the first accepted handoff. Completion owns only its effort's
local commit subject/body and reviewer explanation. Later handoffs patch only
supplied publication fields. The first explicit operator proceed locks all
publication text for the workflow; passing review does not. Historical consent
keeps this lock across renewed publication attempts and local-only handoffs.

A publication phase directly references a passed review and retains actual input,
provenance, comparison, observed destination tip, and target. It owns consent and
delivery/recovery facts, not a separate plan or duplicated authored text.
`PublicationInput.Code` is not an implementation phase or output-producer reference.
It records reviewed, operator-selected, or explicitly unreviewed direct-sync
output without inventing another implementation handoff. Review owns evaluated
scope/evidence and findings, not publication content. The branch established by
initial review is invariant across later phases, although its observed tip may
advance.

`delivery.FlowAnswer` changes pending flow, not content, destination or review.
A flow change appends an unconsented occurrence; identical selections are no-ops.
Both integration flows package with `PublicationContent.CommitMessage()`, never
PR text. The title policy derives the PR title from the publication subject.
PR-only rendering adds configured review-process and review-gap sections to the
maintained description at use time. Configuration remains live; context loads it
on every invocation without snapshot, freezing or invalidation machinery.

The Delivery phase observes clean committed task-branch HEAD and retains
operator commits under commit-keyed, workflow-owned refs without changing files,
the index, or HEAD. Added, amended, or rewritten input is disclosed with its
relationship and patch to the reviewed commit. A mismatch alone does not start
review. Proceed explicitly authorizes that gap; abort leaves the decision pending.

Publication does not compare current task requirements with captured review
requirements. The operator decides when manual requirement edits warrant repeating
review; they do not reserve Review phases, invalidate consent, or block delivery
continuation and explicitly authorized recovery. Historical scopes and consent
remain immutable, while newly started reviews capture current requirements.
Continuation requires the exact retained review revision.

`AuthorizePublication` records the first grant and evidence. `IntentDigest` binds
repository/task and publication phase address, historical review scope/output/contract, baseline,
actual retained input/provenance/comparison, publication destination context,
content, target, and grant. Reviewed and publication destination tips are distinct
facts. A new input/intent/context needs a new phase and decision; old grants remain
immutable history. Configuration changes alone do not retrospectively alter them.

Input is rechecked before recording consent and again on delivery continuation.
Clean input changes renew the decision; dirt, wrong checkout, and ownership
conflicts refuse delivery without discarding anything. A direct
merge with independently verified retained merge evidence reconciles the original
checkpoint and consent. Without such effects, same-branch tip advancement can
refresh publication context without replaying review. Native remote-write guards
remain authoritative; proceed does not authorize force pushes or stale overwrites.
Existing pushes pin their source to the authorized commit (or verified integration
result), not a mutable local branch, so a late local ref change cannot substitute
unconsented code. Normal hooks and signing apply. Initial PR packaging rewrites the task branch in
place onto its recorded baseline; it never allocates another physical checkout.

Authorization requires a passing review **at the grant time**. Required results
and owned finding decisions must already support acceptance. Future finding
waivers cannot authorize earlier grants. Retained consent uses that historical view, while
`ReviewOutcome` still reflects current decisions. Later dismissal/reopening does
not rewrite the original grant or delivery. A retained receipt is not permission
to ignore current blockers when performing new effects.

The CLI displays a separate publication decision after every review pass.
Interactive stdin enables operator decisions independently of output redirection.
Noninteractive input returns `review.ErrInteractionRequired` with resumption
advice; executors propagate it without failing or rejecting the pending review.
Manual-command confirmation, manual assessment, finding dispositions, fresh-review
blocker dispositions, and post-review proposal prompts follow the same input gate.
EOF and cancellation retain pending work; partial EOF answers are never applied.

The internal `Respond` method records ordinary consent without effects. After interactive
authorization succeeds, the same `task run` invocation advances delivery.
A pause or failed answer never advances publication. Output failure cannot erase
a recorded grant or observed delivery. Accepted unchanged retries use their
recorded operation and consent without resolving earlier configuration again.

## Initial PR operation and private-history release

The supported PR journey publishes once. Its operation retains input under an
owned recovery ref, then saves intent before local rewriting. Ordinary `git
reset --soft` and `git commit` package the authorized tree in the task checkout.
Delivery records the public commit/input mapping before push or PR writes.
A durable PR creation intent distinguishes recovery of this operation from
arbitrary PR import. Failed lookup is not absence; ambiguous provider outcomes
are observed before retry. Direct merge owns separate local-sync and integration
checkpoints. It reuses Git preparation and merge operations, not PR-sync orchestration.
Packaging takes its parent from the prepared baseline. Integration takes its source
from the recorded sync result and its destination from the publication, which must
agree with that sync. The actual integration result is saved before destination push.

Delivery receipts retain the observation time and, for PRs, the native PR reference.
The PR commit and tree come from public preparation, or from the selected input
for older deliveries without recorded packaging. That fallback does not invent a
packaging intent or result. Direct delivery identifies the original public
preparation (including across a continuation) and the actual integration result,
not just the synchronized source.

An operation without a recorded public commit requires a typed recovery decision.
CLI discloses the saved target, current HEAD, checkout/index changes, and untracked
paths covered by restoration. Explicit confirmation restores the saved input and
retries packaging. Manual repair, pause, EOF, cancellation, or noninteractive stdin
leaves checkout and state untouched. After manual repair, an explicit continuation
selects the actual clean checkout for fresh consent. Direct recovery after public
preparation uses recorded public/sync/destination checkpoints, not private objects.
This is recovery of the same publication,
not a workflow reset or an automatic reflog/tree-equivalence classifier.

A recorded public commit is reused on unchanged retries. Its expected changed
identity is not compared to the reviewed commit as if it were a new operator
selection. Hook-produced tree changes instead create a fresh decision for the
actual committed input before delivery. Once initial PR delivery is complete,
later fixes and sync extend public history without another squash loop.

After independently verified push/PR delivery is saved, cleanup removes the
workflow's obsolete `refs/orpheus/revisions/<repository>/<task>/<workflow>/` and
`publication-inputs` refs, including capture inputs, outputs, earlier selected inputs,
and the temporary recovery ref. It preserves operator refs, unrelated workflows,
and historical task-state data. The public branch retains its commit. Cleanup
records `PrivateRefsReleasedAt` only after success and can retry without repeating
delivery. It neither prunes objects nor expires shared reflogs. Loading history,
inspection, delivery/finalization retries, and public-branch sync require no
released private objects.

## External review and branch sync

External review retains the published PR reference, actual `BranchSyncs`, and one
terminal provider `Outcome`, not open-PR polling history or future feedback features.
Sync persists merged or closed-unmerged observations independently of Finalization.
A recorded closed-unmerged outcome requires operator direction, never closure.
An empty branch-sync array is ordinary waiting for merge. Current sync may make a clean merge or invoke the conflict resolver;
those are alternatives within the same branch-sync concept.

Each sync records its starting local/remote heads and destination merge source.
Optional conflict resolution owns the files and native `AgentExecution`, including
process/session/usage/cost facts and uncertainty. Purpose follows containment:
this is branch maintenance, not a feature implementation or agent completion.
Current sync does not rerun task review or obtain another publication decision,
and its result must not be presented as having passed the original task review.

`LocalCommit` records the observed local result, including an already-current
head. `Push` independently confirms that result remotely without copying its
head. Both timestamps are observation times, not Git author dates. A missing/failed remote lookup is
not successful delivery. Retrying a push completes that same entry; a new merge
or resolver effort appends another entry. Earlier checkpoints, files, execution
identity, and results remain history.

A rollback receipt confirms native restoration to that synchronization's
checkpoint and retains the time and reason, not another copy of those heads.
It cannot coexist with a confirmed push. A failed resolver or incomplete push
remains unresolved until safely pushed or rolled back; later
syncs, phases, and reset cannot strand that work. Only the last sync of the live
last phase may be unfinished. Supplemental telemetry may still fill unknown
measurements without rewriting known ones.

## Finalization

PR finalization follows the external-review occurrence with a persisted merged
outcome. Its publication and PR come from that review's reference. Confirmation
must follow delivery and settled branch maintenance; a superseded publication
cannot authorize closure. Closed-unmerged outcomes require operator direction.

Direct finalization follows the publication with an independently verified
destination delivery receipt. A local integration result alone cannot authorize
closure. Both flows resolve the prerequisite at the finalization occurrence's
position in the same workflow, not from current Git state or task metadata.
There is no stored acceptance payload or selector.

Finalization saves closure intent before calling the source. It reconciles the
source lifecycle before every closure attempt; an independently confirmed
already-closed task repairs a missing receipt without repeating the mutation.
Closure retains its own time and reason. Cleanup is separate and retryable, even
after closure or reset. Cleanup observations use the owning workflow's prepared
directory and preserve their own times, evidence, and retention reasons.

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
and persistence versions. Unknown fields in format 2 are ignored without
interpreting their values, including in nested objects and envelopes. Reading
never rewrites the file; the next successful save writes only current fields.
Missing or invalid required addresses, unsupported kinds, malformed YAML,
duplicate keys in decoded mappings, and multiple documents are rejected.
Optional index pointers preserve the difference between absent and explicit zero.

`Save` holds a document-specific lock, independently reloads the prior state,
checks the expected version and `ValidateSuccessor`, and atomically replaces the
file only after validation. It returns the next version without mutating its input.
This lock can be held inside the application's progression lock.
`workstate.Encode`/`Decode` expose the same validated codec without advancing the
persistence version or choosing progression.

Only workflow serialization format 2 is supported. There is no old-format reader,
converter, import path, or historical exception to domain requirements. Unsupported
records require an explicit, inspectable conversion outside the application before
use. Preserve their history; do not
discard operator data to bypass loading. Task lifecycle remains in Beads/Gig.
See [local workflow state](../user/tasks.md#local-workflow-state) for the storage
boundary. The application never deletes or rewrites unsupported files.

### Git output boundary

Implementation-owned changes committed through `phaseexec.Runner` record callbacks
at stable parent/index addresses: the effort's own completion, direct child PID,
and actual observed exit. Execution duration is derived from start and observed
exit times, not persisted. Running, interrupted, and start-failed executions
have no measured duration. Terminal, pre-reset, cross-task, and wrong-kind
callbacks are refused.
The first live handoff is authoritative; repeats are no-ops, not diagnostic journals.
Recording a handoff or exit neither captures output nor grants review permission.
`implementation.Service` owns capture using those independent facts.

`agent.PreparedExecution` owns profile selection, repair-session selection and
launch assembly for implementation, Review-Agent Steps and conflict resolution.
Workflows record its selected execution facts before starting it. Launch uses the
prepared command, prompt and session unchanged; it does not reload configuration
or select a session again. Workflows retain persistence and purpose-specific
success decisions; callers supply streams, usage capture and child-PID recording.

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
Already captured output verifies retention only, without another commit or state
write. Checkout cleanliness cannot prevent saving successful implementation output.

`git.CaptureRevision` takes that effort's exact input commit, attached branch,
completion message, and effort-owned ref namespace. The adapter does not itself
authenticate a handoff or grant review/publication permission.

Capture stages completed output and invokes ordinary `git commit`, retaining the
input and actual result under owned refs. Hooks, signing, author configuration,
and content filters apply normally. Unchanged output retains the actual input
without an empty commit; the caller must preserve the effort's own completion and
choose domain reuse only when that input addresses earlier implementation output.

A stable effort-specific reflog action and input checkpoint distinguish recoverable
interrupted commits from unrelated branch movement. If output is not yet retained,
recovery requires one ordinary commit on the recorded input and the matching
branch/reflog. Already-retained output is reused without staging or committing
later leftovers, even with a dirty checkout. Commit failures are reported even if
reconciliation finds a retained commit. The controller finishes capture and saves
output before attempting review admission; review validation never fills missing
implementation bookkeeping.

`git.CheckCheckout` checks supplied commit and branch expectations and ordinary
Git status: no staged, unstaged, or non-ignored untracked changes. Review entry
and continuation, publication selection and packaging, repository-root preparation,
branch sync, and direct integration share these cleanliness semantics. Git's
configured line-ending conversion, executable-mode handling, and index flags are
respected; ignored build output is allowed. Observation does not reject unfinished
operation markers. Capture and publication history rewriting separately refuse
unfinished operations that could be incorporated into their commits.

`git.VerifyRevision` combines checkout admission with separate retained
commit/tree verification when resuming a reserved review. Hook leftovers can
block fresh admission without erasing the captured output or launching another
implementer. If the operator commits those leftovers, the next fresh review
selects the clean branch tip and reports it before saving the reservation. The
implementation handoff remains tied to the original commit. For a paused check,
the controller checks the branch commit before capturing forward requirements.
A changed task-branch commit selects fresh admission; after verifying and
reporting the clean tip, admission stops the old assessment before applying
prior-finding dispositions. Those dispositions cannot authorize continuation
or publication of the old review. Unresolved checkout recovery still runs first.

`review.CaptureCommittedCandidate` composes that admission with verified raw-file
and index rollback, including canceled reviewers. The application pipeline uses this committed candidate for every check, reviewer,
and manual command. Agent context exposes full-task baseline/output and optional
repair-delta input/output endpoints, together with captured requirements.

`git.VerifyRetainedRevision` verifies retained commit/tree and owned-ref identity
without requiring the checkout to represent that revision. It does not establish
a safe review checkout.

Serialization format 2 stores this ownership directly. Existing format-2
records load without conversion: removed mapping fields and their payloads
(including old finalization acceptance and recovery prose) are ignored.
Reads do not rewrite records; normal saves omit retired fields and preserve
recognized facts, including publication consent and its digest. Unsupported
formats still require [local-state adoption](../user/publication.md#local-state-adoption).
