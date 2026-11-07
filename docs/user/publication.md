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
implementation, repair, and sync keep their normal review/safety contracts.

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
| `pull-request` | Package the authorized input tree into public history, push the exact public commit, create/recover or update the selected PR, and record verified delivery. The task stays open until merge reconciliation. |
| `direct-merge` | Verify the authorized input and captured destination checkpoint, create an ordinary no-fast-forward integration merge, push the destination, then close the task. It does not push the task branch or call `gh`. |

A direct-merge conflict is aborted without pushing the destination or closing the
task. Resolve the conflict outside Orpheus before retrying. Recorded task commits,
merge commits, pushes, and closure facts let retries continue after partial success.
Do not try to switch flows to recover a partially published task.

## Local revisions and public history

Local implementation/review commits stay retained independently of public history.
The first PR publication squashes the selected tree onto the task baseline. Later
publications append a commit to the verified previous public head, including when
you amended or rewrote local history. They do not rewrite the remote branch.
Inspection distinguishes reviewed output, selected input/provenance, the public
commit, and earlier delivery records. Publishing an older revision never marks
newer local output as pushed.

Packaging uses ordinary hooks and signing in an operation-owned Git worktree;
it does not change your checkout. If packaging changes the authorized tree,
Orpheus retains and reports the actual commit/tree without pushing. Select the
reported committed input on the owned task branch if you want it delivered, then
use `task run` for a new proceed-or-abort decision. It is not silently treated as
reviewed output.

Commit, push, and PR effects have retained operation intent. Interrupted retries
reconcile the same public commit and PR before repeating effects. Unexpected
remote heads, changed PR branch pairs, and merged/closed PRs stop publication for
reconciliation rather than overwriting work or creating another PR. Exact-head
leases protect append-only pushes against concurrent ref changes; they do not
authorize history rewriting. PR text keeps the selected original feature intent
and discloses operator-accepted review gaps, including on updates.

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
incorporates the integration destination into the task branch. Batch sync leaves a
branch unchanged when it would merge cleanly and updates it only when conflict
repair is required.

Conflict repair uses `agents.defaults.sync_conflict_resolver`, falling back to
`agents.defaults.implementer` when unset. See [agent profiles](agents.md).
Closed-task worktree cleanup and recovery are described in [tasks](tasks.md#recovery-and-cleanup).

Existing repository-root work recorded on the default branch is not automatically
moved to a task branch during publication. Preserve its checkout and inspect its
recorded context before repairing it manually; do not clear task history or reset
files to force a retry. No operator state is converted by this change.

Publication plans now require explicit retained input, provenance, comparison,
and destination context; review-owned publication drafts are no longer accepted.
Existing records missing these fields, containing obsolete delivery choices, or
using earlier authorization digests need an explicit, inspectable conversion
before use. New grants also bind the selected feature-completion reference. Do not discard history to bypass
loading. This change does not convert or modify operator records.
