# Publication

Publication requires both a passed [task review](reviews.md) and explicit consent
for its exact publication input, content, flow, and destination. `task run` retains committed reviewed output and
stops at a distinct publication decision, including after fully automated review.
The prompt displays the reviewed revision/evidence separately from the actual
publication commit/tree, authored content, flow, fixed destination, and intended
effects. Use `orpheus task run <task-id>` with interactive stdin. Choosing
`p=proceed` records consent and delivers in that same invocation, including when
resuming a pending decision. `b=abort` or `q=pause` leaves publication pending.

After review, you may add commits, amend a commit, or rewrite history on the owned
task branch. If its clean committed HEAD differs from reviewed output, Orpheus
shows both commits, their relationship, and a diff before asking proceed or abort.
Proceed explicitly accepts that review gap; it does not claim that operator
changes passed review. The actual selected input is retained with operator
provenance, without creating an agent effort, completion, or review result.

Uncommitted and untracked output is not included: commit or otherwise resolve it
yourself before publication. Orpheus does not stage, capture, discard, or silently
publish those files. Review admission and reviewer read-only checks remain strict.
Changed task requirements still require fresh review, and controller-produced
implementation and repair keep their normal review/safety contracts. Local direct-delivery
sync is the explicit no-task-review exception described below.

Noninteractive stdin returns a nonzero interaction-required error without reading
piped answers. The decision remains pending for an interactive `task run`.
Redirecting stdout/stderr is supported. EOF and cancellation never grant consent.
Read-only inspection remains available:

```sh
orpheus task show workflow op-123 --json
```

Consent covers only the displayed input and publication choices. A later input or delivery-choice change
requires another decision, including a change after confirmation but before
delivery. An unchanged interrupted operation keeps its valid grant and reconciles
observed effects. A review pass, `agent done`, or commit existence alone never
authorizes delivery or closure.

`orpheus task done <task-id>` is only a retry entry for an already authorized
intent. It consumes committed input without creating another implementation
revision. It never performs source closure or local worktree cleanup. Do not edit
state to manufacture authorization.

The work directory and integration flow are independent. Both worktree and
repository-root tasks publish a reviewed task branch. Choosing `--repo-root`
does not select direct merge. Preparation selects the task branch before the
agent starts; publication uses that recorded branch without moving changes from
the default branch.

## Integration flow and destination

The default flow is `pull-request`. Set a global default in Orpheus `config.yaml`:

```yaml
publication:
  integration_flow: direct-merge
```

Inspect, override, or clear a repository default:

```sh
orpheus repo config get my-repo integration-flow
orpheus repo config set my-repo integration-flow direct-merge
orpheus repo config set my-repo integration-flow ''
```

Only `pull-request` and `direct-merge` are valid non-empty values. New publication attempts use
repository configuration, global configuration, then `pull-request`. At the
publication prompt, `i` changes flow; manual review offers no delivery choices.

Initial review entry captures the registered default branch as the workflow's
fixed destination. Later review, publication, sync, and default/configuration
edits do not retarget it. There is no destination editor. Selecting another
base/destination needs a future dedicated flow.

The same destination branch advancing does not itself require re-review. Its
current tip is refreshed/disclosed for consent; an interrupted direct merge
reconciles its retained checkpoint rather than mistaking its own merge for a new
selection. This never permits force-pushing over unexpected remote work, stale
writes, bypassing active executions, discarding dirty files, or unresolved
conflicts. Accepted operations needing reconciliation cannot be casually retargeted.

| Flow | What Orpheus does |
| --- | --- |
| `pull-request` | Squash authorized input in the existing task checkout, push the recorded public commit, and create/recover the operation's PR, and record verified delivery. The task stays open until merge reconciliation. |
| `direct-merge` | Prepare public history once, locally sync the fixed destination, obtain fresh consent for changed output, record an ordinary no-fast-forward integration merge, push only the destination, and record verified delivery. Source closure waits for sync. |

Direct delivery synchronizes the destination into the public task branch locally.
Conflicts use the configured sync-conflict resolver. Neither clean sync nor resolver
edits run another task-review pipeline; the actual output and resolver execution
are recorded as not separately reviewed. Changed input receives fresh proceed-or-abort
consent. Direct delivery never queries/creates a PR or pushes the task branch.
Destination advancement that invalidates a checkpoint stops for reconciliation,
not overwrite, retargeting, or an automatic review/retry loop.

The integration commit is saved before destination push. Lost responses reconcile
that exact result; verified delivery permits separately retryable source closure.
Closure retries do not squash, sync, merge, or push again.

## In-place public history and recovery

Both publication flows perform one initial squash in the existing task work directory,
for both repository-root and dedicated-worktree tasks. The task branch moves from
the authorized input to one public commit on the recorded task baseline. It stays
on public history afterward. No additional packaging worktree or private working
branch is created. Later PR fixes and sync extend that public history; they do not
enter another initial squash or PR-update packaging loop.

Before rewriting, Orpheus saves the exact committed input under a temporary
recovery ref and records operation intent. It uses ordinary hooks and signing,
then records the resulting public commit before any push or PR write. The public
commit's new SHA is an expected packaging result, not an operator review gap.
If hooks change the authorized tree, delivery stops, shows the actual committed
input on the next `task run`, and requires fresh consent without inventing review.

If packaging stops before its public commit is durably recorded, `task run`
displays the saved reset target and the checkout/index changes and untracked
paths that restoration would discard. Choose `r` to explicitly restore that input
and retry, or `m` to leave everything untouched for manual repair and a later
retry. After repairing a clean committed checkout, choose `c` to continue with
a fresh publication decision instead of forcing a later reset. Ignored files are kept. Pause, partial/complete EOF, cancellation, and
noninteractive stdin never authorize restoration. Orpheus does not infer or
reconstruct an unrecorded commit from intermediate trees or reflogs.

Once a public commit is recorded, unchanged retries reuse it and reconcile push
and PR outcomes without squashing again. PR creation intent is durable before the
provider write; only that interrupted operation's PR can be recovered. Unrelated
remote heads or PRs, retargeting, and merged/closed PRs require reconciliation,
not overwrite or another PR. Changed input before delivery needs fresh consent;
changing an already pushed public checkout requires reconciling public history,
not another initial squash.

Direct delivery releases obsolete private refs immediately after public preparation
and its selected-input mapping are durable, before sync or delivery. Public
checkpoints keep live output reachable. After preparation, recovery restores only
recorded public/sync/destination checkpoints, never discarded private objects.
The prompt identifies the directory, branch, reset target, and discarded changes;
manual repair has the same explicit `c` continuation. Cleanup retries neither
re-squash nor depend on successful remote delivery.

Before verified delivery, changing the committed task input or local destination
invalidates a recorded integration. `task run` offers explicit recovery and keeps
the old result as history. For manual repair, restore the local destination to
the disclosed checkpoint or observed destination, then choose `c` to select the
clean task input for fresh consent. This does not squash again or rerun task review.

For PR publication, only after push/PR delivery is verified and durably recorded does Orpheus release
the workflow's obsolete private revision/input refs, including the recovery ref.
Cleanup can be retried with `task done` without repeating delivery. Descriptions,
review evidence, provenance, and commit IDs remain inspectable in task state,
but those historical Git objects need not remain available. Operator refs and
other tasks' refs are preserved. Normal Git garbage collection handles eventual
reclamation; Orpheus does not force-prune objects or expire shared reflogs.

## Commit and PR titles

Global policy belongs in `$XDG_CONFIG_HOME/orpheus/config.yaml`, normally
`~/.config/orpheus/config.yaml`:

```yaml
publication:
  summary_guidance: "Write a capitalized release-note summary, 80 characters or fewer."
  summary_guidance_style: capitalized
  title_template: "[{{external_ref}}] {{summary}}"
```

Each field resolves independently from repository override, global value, then
built-in default. Without configuration, agents receive typed-summary guidance,
such as `feat: add task filters`. The publication commit uses its maintained subject unchanged;
the PR title derives from that subject through the title template.
New registrations inherit global defaults unless explicitly overridden.

```sh
orpheus repo config get my-repo
orpheus repo config set my-repo summary-style capitalized
orpheus repo config set my-repo title-template '[{{external_ref}}] {{summary}}'
orpheus repo config set my-repo summary-guidance 'Write a short capitalized summary.'
orpheus task edit op-123 --external-ref TREX-1234
```

Custom `summary-guidance` overrides named-style guidance. It guides the agent;
it is not a validator for generated prose. Set any repository field to `''` to
inherit its global value again.

Title templates support only `{{summary}}` (the publication commit subject) and
`{{external_ref}}`. The reference is inserted after whitespace normalization;
Orpheus does not contact Jira or validate Jira-key syntax. The example renders
`[TREX-1234] <publication subject>` for the PR title, not the commit message.
Ordinary implementation commits retain each effort's local subject/body.

A template requiring a missing external reference makes the task need attention
and blocks dispatch before worktree creation or agent launch. PR delivery checks
current title policy before packaging or pushing. Restore the reference with
`task edit --external-ref`, then retry the pending operation.

## Publication content

The workflow owns three authored fields: publication commit subject, publication
commit body, and cumulative PR description. Each agent completion separately owns
its local commit subject/body and reviewer-facing technical explanation.

The first accepted `agent done` seeds publication subject/body from the local
message unless explicitly overridden, and requires the initial PR description.
Later handoffs preserve publication content unless fields are explicitly supplied;
each supplied field replaces only itself. Agents should maintain one cumulative
PR description, not author a full PR description for every repair. See the
[handoff commands](reviews.md#completion-and-approval).

Publication content stays editable through handoffs after review passes, until
the first explicit operator proceed. Then all three fields are read-only for the
workflow. Renewed consent, recovery, and unchanged publication retries do not
unlock or replace them. Later handoffs may still describe local work without
publication updates. There is no standalone publication-content editor.

Both integration flows construct their public squash message solely from the
publication commit subject and body. Neither the PR description, technical
explanations, nor generated review sections are automatically copied into it.
The PR title derives from the publication subject through title policy; PR
creation uses the cumulative description. Inspect authored content with
`orpheus agent context` during an active agent effort or
`orpheus task show workflow <task-id>` at any phase (`--json` is also available).
Fresh and resumed agents see the same current content. `agent context` reads
current configuration every time; no configuration snapshot, freezing, change
detection, or configuration-triggered invalidation is involved.

## PR description and review-process section

By default Orpheus appends a concise, PR-only review-process section with attempts,
finding outcomes, and local repair commit messages. It does not copy complete
finding descriptions or technical explanations. A disclosed publication review
gap is also PR-only. These generated sections do not alter the authored cumulative
description or publication commit text.

Disable that generated section globally:

```yaml
reviews:
  include_pr_review_process: false
```

Or override it per repository:

```sh
orpheus repo config get my-repo include-pr-review-process
orpheus repo config set my-repo include-pr-review-process false
orpheus repo config set my-repo include-pr-review-process true
orpheus repo config set my-repo include-pr-review-process ''
```

Clearing the override inherits the global setting. This setting changes only the
generated PR section, not the supplied body, titles, review state, or execution.

## Sync after publication

```sh
orpheus task sync op-123
orpheus task sync --all
```

`task run` stops after verified delivery in either flow. Sync/background processing
executes External review and Finalization, not another publication. It also finds
verified direct deliveries without a PR URL and retries cleanup for closed tasks.

External review leaves open PRs waiting and durably records terminal provider
outcomes before any source-closure effect. A merged outcome permits Finalization;
a closed-unmerged outcome requires operator direction and never closes the source.
Finalization records closure intent, then reconciles the authoritative task source
before retrying closure. Lost responses or failed local recording do not repeat
completed delivery. Retained worktree cleanup is separately retryable with sync.
Task-source lifecycle, delivery, PR outcomes, and closure remain distinct facts.
The operator coordinates remote merges with ongoing local work; PR observations
do not replace controller-selected implementation or repair.

Sync does not create PRs. For open PRs, an explicit task-ID sync
incorporates the integration destination into the public task branch. It does not
need discarded private revision objects or initial squash machinery. Batch sync
leaves a branch unchanged when it would merge cleanly and updates it only when
conflict repair is required. Content-changing sync review/authorization and
post-publication repair routing retain their existing owners; this publication
change does not add that automation.

Conflict repair uses `agents.defaults.sync_conflict_resolver`, falling back to
`agents.defaults.implementer` when unset. See [agent profiles](agents.md).
Closed-task worktree cleanup and recovery are described in [tasks](tasks.md#recovery-and-cleanup).

Existing repository-root work recorded on the default branch is not automatically
moved to a task branch during publication. Preserve its checkout and inspect its
recorded context before repairing it manually; do not clear task history or reset
files to force a retry. No operator state is converted by this change.

## Local-state adoption

Workflow records use serialization format 2. Publication content now belongs to
`Workflow.Publication`; completion records contain local `CommitSubject` and
`CommitBody`, not a PR description. Publication phases directly retain selection,
review, destination, target, consent, and delivery/recovery facts; there is no
separate publication plan or copied publication text.

Earlier records and authorization digests are not automatically migrated. Before
using this build with existing task state, preserve the original records and
explicitly adopt the new shape: select the cumulative publication subject/body
and PR description, retain each effort's local message and technical explanation,
and preserve execution, review, delivery, and recovery history. Old consent
digests cannot simply be copied into the new representation. No conversion tool
or workflow reset is provided here; do not delete history, fabricate observations,
or discard a checkout to bypass loading. This change does not modify operator
records.
