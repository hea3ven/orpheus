# Review pipelines

`orpheus task run` continues into review automatically after the attached agent
records a successful completion with `orpheus agent done`. Automated pipeline
steps run without operator input until the review passes, exhausts its bounded
fix budget, fails operationally, or reaches a manual step.

Check and agent-review blockers are kept by default during autonomous execution.
Orpheus dispatches a targeted implementer follow-up, records which findings the
run targets, and starts a fresh review attempt after the fix records completion.
The global `reviews.max_autonomous_review_attempts` setting defaults to `4`.
The initial review counts toward that limit, so the default permits at most
three targeted fix runs before a fourth blocked review stops and preserves the
open blockers for explicit continuation.

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

For automated-only pipelines that pass from `task run`, Orpheus creates every
valid separate-task review proposal as a Bead before publication/finalization.
If any follow-up task cannot be created, publication stops as an operational
review failure; fix the backend issue and rerun `task review`.

Pipelines with a manual step keep separate-task proposal selection under
operator control during the resumed `task review`.
