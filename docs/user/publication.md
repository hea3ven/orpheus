# Publication

Publication requires both a passed [task review](reviews.md) and explicit consent
for its exact publication plan. `task run` retains committed reviewed output and
stops at a distinct publication decision, including after fully automated review.
The terminal prompt displays the plan before accepting consent. Noninteractive
invocations leave it pending; tooling can inspect and respond explicitly:

```sh
orpheus task show workflow op-123 --json
orpheus task respond op-123 --interaction '<Pending.ID JSON>' \
  --action authorize --evidence 'Operator accepts this exact displayed plan'
orpheus task run op-123
```

Review the revision, passed review, content, flow, and destination in inspection
before responding. `respond` records consent without publishing; continuation
performs delivery. The terminal prompt also records consent without delivering;
run `task run` again to deliver. Pause, EOF, and cancellation never supply consent.
A stale interaction is refused, and an unchanged authorized retry retains its grant.
A review pass, `agent done`, or commit existence is not permission to push or close
a task. There is no running-agent bypass.

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
repository configuration, global configuration, then `pull-request`. The initial
destination is the registered default branch, captured in the review scope.

Flow and destination choices belong to Publication, not manual review. At the
publication prompt, `i` changes flow and `d` selects an existing branch on
`origin`. Tooling can replace a pending plan, including its title/body:

```sh
orpheus task respond op-123 --interaction '<Pending.ID JSON>' --action revise \
  --plan '{"Flow":"pull-request","DestinationBranch":"main","Content":"feat: publish work\n\nReviewed task description."}'
```

Inspect the resulting interaction before authorizing it. Content/flow edits retain
the passed review but create a new plan without consent. A changed destination
(including its observed head) or changed task requirements requires a fresh review
from the first step under current configuration. The selected publication content
and flow survive that review. Changed checkout output cannot be approved as the
previous revision; restore the reviewed checkout or obtain appropriate new review.

Plans freeze their content and choices. Configuration edits alone do not re-run
completed review or replace an accepted plan. Unchanged interrupted delivery keeps
its consent and reconciles observed effects. Once accepted, a plan cannot be
edited through the pending-decision interface; reconcile delivery before changing
intent. No confirmation bypass or automatic consent is inferred from a review pass.

| Flow | What Orpheus does |
| --- | --- |
| `pull-request` | Push the already committed reviewed task branch, create or recover a PR against the destination, and record its URL. The task stays open until merge reconciliation. |
| `direct-merge` | Verify the reviewed task output and unchanged observed destination, create an ordinary no-fast-forward integration merge, push the destination, then close the task. It does not push the task branch or call `gh`. |

A direct-merge conflict is aborted without pushing the destination or closing the
task. Resolve the conflict outside Orpheus before retrying. Recorded task commits,
merge commits, pushes, and closure facts let retries continue after partial success.
Do not try to switch flows to recover a partially published task.

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

Persisted manual assessments no longer accept embedded publication choices.
Existing local records containing those obsolete fields need an explicit,
inspectable conversion before use; do not discard task history to bypass loading.
This change does not convert or modify operator records.
