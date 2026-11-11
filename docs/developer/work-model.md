# Workflow work model

`internal/work` defines shared, I/O-free domain values and validation.
`internal/workstate` is the single validated, atomic persistence boundary used by
application orchestration. `internal/taskstate` coordinates typed mutations and
projects transient service/operator views; its projections are never persisted.
Task sources retain task definitions and lifecycle authority. Review-specific
captured requirements belong to the immutable review scope. `workflow.Controller`
owns built-in progression; specialized services execute agents, review pipelines,
Git/PR effects, and source closure. CLI supplies task identity and flag choices,
composes collaborators, and renders semantic results. `Controller.Advance` owns
recovery, workflow-dependent option validation, and a single phase loop: select
current work, reserve it, execute its handler, and reload owned results before
selecting again. Handlers do not invoke the next phase. There is no persisted cursor.

## Built-in progression

The controller derives current work from the last workflow, phase, and actual
review-step occurrence. Preparation establishes context; Implementation produces
a safely completed retained revision; Review evaluates that revision. A pass
enters Publication without granting consent. PR delivery enters External review,
which waits for explicit reconciliation; observed direct delivery skips it and
enters Finalization. Finalization consumes recorded delivery/acceptance and
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

For an active review, the controller appends a forward contract preserving the
started-slot prefix and replacing only unstarted requirements with the live
suffix. Old contracts, step addresses, results, findings, and scope remain intact.
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
consent; there is no second decision queue. Finding batches
save atomically. Pause and cancellation retain pending work; stale/mismatched
responses cannot authorize different work. Selection changes can reserve fresh
review or append a new pending publication phase, but never executes review. A separate typed
publication-recovery response explicitly confirms restoration and retries
packaging under the original operation; ordinary consent responses have no Git
effects.
Review and publication decisions verify the exact retained checkout before saving;
publication consent also checks current task requirements and destination context.
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

Selection, consent, and initial delivery belong to publication. Branch context and PR
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
review context, not publication narrative; repairs keep their own rationale. `LatestOutput` selects captured implementation
output independently of delivery. After initial PR publication, later work starts
on the public task branch rather than a discarded private revision. Branch sync
commits are not feature completion or newly reviewed implementation output.
`RepairPhases` derives reverse navigation from repair targets. Per-finding local
verification requires an explicit authoritative statement, not an inferred flag
from a whole-task pass or PR merge. Later authoritative negative evidence can
reverse an earlier positive statement.

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

`PublicationFlowResponse` changes pending flow, not content, destination or review.
A flow change appends an unconsented occurrence; identical selections are no-ops.
Both integration flows package with `PublicationContent.CommitMessage()`, never
PR text. The title policy derives the PR title from the publication subject.
PR-only rendering adds configured review-process and review-gap sections to the
maintained description at use time. Configuration remains live; context loads it
on every invocation without snapshot, freezing or invalidation machinery.

The publication controller observes clean committed task-branch HEAD and retains
operator commits under commit-keyed, workflow-owned refs without changing files,
the index, or HEAD. Added, amended, or rewritten input is disclosed with its
relationship and patch to the reviewed commit. A mismatch alone does not start
review. Proceed explicitly authorizes that gap; abort leaves the decision pending.

Changed requirements still reserve fresh review of controller output under current
configuration. Review admission is exact; the operator override is not used for
controller repairs or sync. This requirements path does not copy publication
content/targets into Review.

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

Authorization requires a passing review **at the grant time**. Required results,
check waivers, owned finding decisions, and decisions reached through unresolved
verification references must already support acceptance. Future waivers cannot
authorize earlier grants. Retained consent uses that historical view, while
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
authorization succeeds, the same `task run` invocation advances delivery. Changed
requirements follow fresh review before another publication decision. A pause or failed answer never
advances publication. Output failure cannot erase a recorded grant or observed
delivery. Accepted unchanged retries use their recorded operation and consent
without resolving earlier configuration again.

## Initial PR operation and private-history release

The supported PR journey publishes once. Its operation retains input under an
owned recovery ref, then saves intent before local rewriting. Ordinary `git
reset --soft` and `git commit` package the authorized tree in the task checkout.
The controller records the public commit/input mapping before push or PR writes.
A durable PR creation intent distinguishes recovery of this operation from
arbitrary PR import. Failed lookup is not absence; ambiguous provider outcomes
are observed before retry. Direct merge owns separate local-sync and integration
checkpoints. It reuses Git preparation and merge operations, not PR-sync orchestration. Integration results
are saved before destination push; closure consumes verified delivery only.

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
released private objects. No existing operator data is converted by this change.

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

PR finalization consumes the selected external-review phase's persisted merged
`Outcome` through `PRAcceptance`. The acceptance retains that observation's time
and evidence; source closure is not part of PR observation. No provider head, merge
commit, approval, comment, or thread state is invented. Confirmation must follow publication and settled branch maintenance;
a superseded publication/stage cannot authorize closure. Closed-unmerged PRs must
be refused by application reconciliation, not converted into merge confirmation.

Direct finalization addresses publication's observed destination push and needs
no external-review phase or PR query. Sync scans for both evidence forms and for
closed tasks with remaining cleanup. Finalization saves `ClosureIntentAt` before
calling the source and reads the actual source lifecycle before every closure
attempt. Already-closed source state repairs a missing local receipt rather than
repeating closure. Both flows retain actual task-source closure and later cleanup
observations. Merge/delivery confirmation alone does not claim the source
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
converter, import path, or historical exception to domain requirements. Unsupported
records require an explicit, inspectable conversion outside the application before
use. Preserve their history; do not
discard operator data to bypass loading. Task lifecycle remains in Beads/Gig.
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
commit/tree verification. Hook leftovers are preserved and block review without
erasing output or launching another implementer. After operator cleanup,
continuation reviews the recorded commit.
`review.CaptureCommittedCandidate` composes that admission with verified raw-file
and index rollback, including canceled reviewers. The application pipeline uses this committed candidate for every check, reviewer,
and manual command. Agent context exposes full-task baseline/output and optional
repair-delta input/output endpoints, together with captured requirements.

`git.VerifyRetainedRevision` verifies retained commit/tree and owned-ref identity
without requiring the checkout to represent that revision. It does not establish
a safe review checkout.

Serialization format 2 stores this ownership directly. Earlier records require
explicit, history-preserving local adoption; no automatic migration is provided.
See [local-state adoption](../user/publication.md#local-state-adoption).
