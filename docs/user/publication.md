# Publication

Publication requires both a passed [task review](reviews.md) and explicit consent
for its exact publication plan. `task run` retains committed reviewed output and
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

Consent covers only the displayed input and plan. A later input or plan change
requires another decision, including a change after confirmation but before
delivery. An unchanged interrupted operation keeps its valid grant and reconciles
observed effects. A review pass, `agent done`, or commit existence alone never
authorizes delivery or closure.

`orpheus task done <task-id>` is only a retry entry for an already authorized
intent. It consumes committed input without creating another implementation
revision. Do not edit state to manufacture authorization.

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

Only `pull-request` and `direct-merge` are valid non-empty values. New plans use
repository configuration, global configuration, then `pull-request`. At the
publication prompt, `i` changes flow; manual review offers no delivery choices.

Initial review entry captures the registered default branch as the workflow's
fixed destination. Later review, publication, sync, and default/configuration
edits do not retarget it. There is no destination editor. Selecting another
base/destination needs a future dedicated flow.

The same destination branch advancing does not itself require re-review. Its
current tip is refreshed/disclosed for consent; an interrupted direct merge
reconciles its retained checkpoint rather than mistaking its own merge for a new
plan. This never permits force-pushing over unexpected remote work, stale
writes, bypassing active executions, discarding dirty files, or unresolved
conflicts. Accepted operations needing reconciliation cannot be casually retargeted.

| Flow | What Orpheus does |
| --- | --- |
| `pull-request` | Squash authorized input in the existing task checkout, push the recorded public commit, and create/recover the operation's PR, and record verified delivery. The task stays open until merge reconciliation. |
| `direct-merge` | Prepare public history once, locally sync the fixed destination, obtain fresh consent for changed output, record an ordinary no-fast-forward integration merge, push only the destination, then close the task after verified delivery. |

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
such as `feat: add task filters`, and publication uses the summary unchanged.
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

Title templates support only `{{summary}}` and `{{external_ref}}`. The reference
is inserted after whitespace normalization; Orpheus does not contact Jira or
validate Jira-key syntax. The example renders `[TREX-1234] <summary>` for both
the PR title. Ordinary implementation commits retain each effort's own handoff summary/description; publication does not rewrite them.

A template requiring a missing external reference makes the task need attention
and blocks dispatch before worktree creation or agent launch. Plan preparation checks
again before freezing publication content. Restore the reference
with `task edit --external-ref`, then retry the pending operation.

## PR body and review-process summary

After repairs, the PR title and leading body still use the original implementation
completion, not a repair's narrower summary. By default, Orpheus appends a concise
review-process section with attempts, finding outcomes, and repair summaries. It
does not copy complete finding descriptions or repair PR bodies.

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

Sync reconciles recorded PRs; it does not create new ones. It closes tasks whose
PRs merged and records the outcome locally. For open PRs, an explicit task-ID sync
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

Publication records now describe one in-place initial operation, its public commit,
PR creation intent, and private-ref release. Obsolete PR-update target fields and
parallel-history operation records are not supported. Plans require explicit
input, provenance, comparison, and destination context; review-owned publication
drafts are not accepted.
Existing records missing these fields, containing obsolete delivery choices, or
using earlier authorization digests need an explicit, inspectable conversion
before use. New grants also bind the selected feature-completion reference.
Do not discard history to bypass loading. This change does not convert or modify
operator records.
