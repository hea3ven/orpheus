# Review pipelines

`orpheus task run` continues into review automatically after the attached agent
records a successful completion with `orpheus agent done`. Automated pipeline
steps run unattended only while they pass or produce no operator decisions; a
check or agent-review blocker prompts for an explicit keep, downgrade, or
waive/cancel decision from both `task run` and `task review`.

`orpheus agent done` requires the usual summary, commit description, and detailed
PR body source plus exactly one technical explanation source:
`--technical-explanation <markdown>` or `--technical-explanation-file <path>`.
The technical explanation is stored with the implementation or targeted follow-up
completion and is rendered into review-agent context so reviewers can understand
the code-change rationale without changing PR title or body selection.

Keeping a check or agent-review blocker preserves it, dispatches a targeted
implementer follow-up, records which findings the run targets, and starts a
fresh review attempt after the fix records completion. Downgrades and waivers
require reasons and keep their persisted semantics. If blocker-decision input is
unavailable, the current attempt is marked blocked with an interrupted decision
flag; Orpheus launches no fix and recovery starts with a fresh
`orpheus task review <task-id>`. The global
`reviews.max_autonomous_review_attempts` setting defaults to `4`. The initial
review counts toward that limit, so the default permits at most three targeted
fix runs before a fourth blocked review stops and preserves the open blockers
for explicit continuation.

When the next step is manual, Orpheus stops before running that step, records the
latest review attempt as `waiting_for_manual`, and stores the pending step name.
Resume it with:

```bash
orpheus task review <task-id>
```

The resumed `task review` continues the same authoritative attempt at the
pending manual step. It does not rerun completed steps. If a paused attempt
exists, `task review --pipeline` may only resolve to the stored pipeline; a
different override is rejected without replacing the paused state.

If a resumed review later launches an autonomous fix after a manual approval,
the next review starts again from step 1. Any earlier manual gate must pass
again before publication.

Orpheus selects a task review pipeline in this order:

1. `orpheus task run --pipeline <name-or-alias> <task-id>` or
   `orpheus task review --pipeline <name-or-alias> <task-id>`
2. the repository `review-pipeline` config value
3. global `reviews.default_pipeline` in Orpheus `config.yaml`
4. the built-in `default` manual local-review pipeline

Global pipelines are defined under `reviews.pipelines` in Orpheus config:

```yaml
reviews:
  default_pipeline: standard
  max_autonomous_review_attempts: 4
  include_pr_review_process: true
  pipelines:
    standard:
      steps:
        - kind: manual
          name: local-review
    go:
      steps:
        - kind: check
          name: test
          command: make
          args: ["test"]
```

Step names are trimmed when config loads and must be unique within a pipeline.
Different pipelines may reuse the same step name.

## Pull-request review process section

For reviewed feature-branch publication, Orpheus builds the PR title and leading
body from the original implementation completion. By default, it then appends a
generated `## Review process` section summarizing review attempts, finding
outcomes, and targeted follow-up run summaries.

Set the global default in Orpheus `config.yaml`:

```yaml
reviews:
  include_pr_review_process: false
```

When the global value is unset, Orpheus preserves existing behavior and includes
the section. A repository override can include or omit the section regardless of
the global default:

```bash
orpheus repo config get my-repo include-pr-review-process
orpheus repo config set my-repo include-pr-review-process false
orpheus repo config set my-repo include-pr-review-process true
```

Clear the repository override to inherit the global default again:

```bash
orpheus repo config set my-repo include-pr-review-process ''
```

This option controls only the generated review-process section. It does not
change the detailed PR body supplied by the implementation agent, PR titles,
commit messages, review-state persistence, or review pipeline execution.

## Repository default pipeline

Inspect a repository's stored and effective review pipeline:

```bash
orpheus repo config get my-repo review-pipeline
```

Set the repository default pipeline to a named global pipeline:

```bash
orpheus repo config set my-repo review-pipeline go
```

Clear the repository default and return to global or built-in fallback behavior:

```bash
orpheus repo config set my-repo review-pipeline ''
```

`repo config set` rejects unknown pipeline names before it updates the registry.

## Repository pipeline aliases

Aliases are repo-local CLI shorthand for named global pipelines. They do not define inline repo-local steps.

Create or replace an alias:

```bash
orpheus repo config set my-repo review-pipeline-alias.quick go
```

Use the alias when reviewing a task in that repository:

```bash
orpheus task review --pipeline quick my-task
```

Use the alias for a one-off implementation-and-review run:

```bash
orpheus task run --pipeline quick my-task
```

The review state records the resolved pipeline name, such as `go`, not the alias.

Delete an alias by setting it to an empty value:

```bash
orpheus repo config set my-repo review-pipeline-alias.quick ''
```

Show all configured repository values, including aliases:

```bash
orpheus repo config get my-repo
```

## Separate-task proposals

When a passing review attempt contains separate-task proposals, Orpheus uses the
same operator selection flow from both `task run` and `task review`: choose
numbered proposals, `a=all`, or `n=none`. Selected proposals become Beads before
publication/finalization. If any selected follow-up task cannot be created, the
operator can continue without that task or stop publication, fix the backend
issue, and rerun `task review`.
