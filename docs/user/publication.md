# Publication

Publication requires a passed [task review](reviews.md). `task run` publishes
after approval; `orpheus task done <task-id>` retries publication or finalization
if it failed. `agent done` records an implementation handoff, not a commit or PR.

The work directory and integration flow are independent. Both worktree and
repository-root tasks publish a reviewed task branch. Choosing `--repo-root`
does not select direct merge.

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

Only `pull-request` and `direct-merge` are valid non-empty values. Selection order
is the task's manual-review choice, repository configuration, global
configuration, then `pull-request`.

At the manual review prompt, enter `i` to keep or change the flow and destination.
The destination defaults to the registered default branch. A named alternative
must already exist on `origin`; Orpheus verifies it rather than creating it.
Pipelines without a manual step use configured flow defaults and the registered
default branch.

Before publication changes Git or the task source, Orpheus records the resolved
flow and destination. Retries keep those values even if configuration changes.
They cannot be changed after publication starts.

| Flow | What Orpheus does |
| --- | --- |
| `pull-request` | Commit the task branch, push it, create or recover a PR against the destination, and record its URL. The task stays open until merge reconciliation. |
| `direct-merge` | Commit the task branch, refresh the destination from `origin`, create a no-fast-forward merge, push the destination, then close the task. It does not push the task branch or call `gh`. |

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
the commit subject and PR title.

A template requiring a missing external reference makes the task need attention
and blocks dispatch before worktree creation or agent launch. Publication checks
again before committing, pushing, or calling the PR provider. Restore the reference
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
