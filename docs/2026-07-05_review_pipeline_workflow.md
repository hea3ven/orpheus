# Review Pipeline Workflow

This guide describes the operator-facing workflow for completed task work. The review pipeline is the approval boundary between an agent's implementation run and publication or finalization.

## Normal Lifecycle

The standard path is:

```text
orpheus task run <task-id>
# implementation agent inspects context, edits files, then runs:
orpheus agent done --summary ... --description ... --detailed-description ...

orpheus task review <task-id>
# if the review passes, Orpheus publishes or finalizes through the same internal path used by task done
```

`task run` prepares the task target, records an attached run attempt, and launches the configured implementer. `agent done` records the completion summary, commit description, and pull-request body source. After that, the task is ready for local review, not direct publication.

`task done` requires the latest review attempt to have passed. Direct `task done` after `agent done` is refused because publication must have a durable local approval record. Once review has passed, `task done` remains useful as the retry command if publication or finalization failed after approval.

## Default Manual Review

If no review pipeline is configured, `task review` uses the built-in manual pipeline:

```yaml
steps:
  - kind: manual
    name: local-review
```

The operator reviews the candidate changes and records one of these outcomes:

- approve, which records a passed review and then finalizes;
- blocking finding, which records current-task work that must be fixed before approval;
- advisory finding, which records non-blocking feedback;
- separate-task finding, which records follow-up work that should become its own task;
- abort, which leaves the task waiting for another `task review`.

Manual review commands, when configured, run inside the review step after confirmation. Manual prompts collect findings directly; reviewers do not use `agent review add` for manual steps.

## Configured Pipelines

`task review` selects a pipeline in this order:

1. `orpheus task review --pipeline <name>`
2. the repository `review_pipeline` registry setting
3. `reviews.default_pipeline` in Orpheus config
4. the built-in manual `local-review` step

Configured pipelines are ordered step lists. Step kinds are:

- `check`: runs a command as a read-only review check. Exit code 0 passes. A non-zero exit records a blocking finding for that check and stops the pipeline.
- `manual`: prompts the operator for approval and findings. A manual command may be attached for guided local inspection.
- `agent_review`: launches the configured review agent with review-specific context. The attached agent records findings with `orpheus agent review add`.

Each step prints a header such as `== Review step: ai-review (agent_review) ==`. Interactive output is meant to show useful step context while bounding noisy command output so failures remain inspectable without overwhelming the terminal.

Review steps are read-only. If a review step mutates the candidate changes, Orpheus treats that as an operational review failure and restores the pre-step candidate snapshot where possible.

## Findings and Failures

Review findings describe product or code feedback:

- Blocking findings stop approval. Check and agent-review blockers prompt for an explicit keep, downgrade, or waive/cancel decision from both `task run` and `task review`. Keeping an automated blocker triggers bounded targeted fixes and fresh review attempts. Manual blockers stop for the operator.
- Advisory findings are recorded but do not block approval.
- Separate-task findings do not block approval by themselves. During review, Orpheus can create standalone Beads for selected candidates.

Operational review failures are different from code or product blockers. Examples include a missing check executable, an attached review agent process failure, invalid pipeline configuration, or a read-only mutation failure. These project as operator attention. Fix the review process or environment, then rerun `orpheus task review <task-id>`.

## Follow-Up Runs

When the latest review is blocked by open current-task findings, `task run` enters follow-up mode automatically. There is no `--follow-up` flag. The new run targets the open blocking findings, records that targeting in task state, and keeps the task on the same implementation target.

Within one `task run` or `task review` invocation, Orpheus asks the operator to classify automated blockers. `keep` preserves a blocker, dispatches the selected implementer for targeted fixes, and starts a fresh review from step 1 after each completed fix. `downgrade` converts the finding to advisory with a required reason. `waive`/`cancel` records a required waiver reason. The global `reviews.max_autonomous_review_attempts` setting bounds the keep/fix loop and defaults to `4`; the initial review counts, so the default allows at most three fix runs before the fourth blocked review stops.

If blocker-decision input disappears, Orpheus marks the current attempt blocked with an interrupted automated-decision flag, performs no publication/finalization, and launches no targeted fix. Recovery is a fresh `orpheus task review <task-id>`, which reruns the selected pipeline in a new authoritative attempt.

If the budget is exhausted, Orpheus preserves the latest blockers and audit history, marks the blocked review as autonomous-budget-exhausted, and tells the operator to explicitly continue with a fresh command. A new `task run` or `task review` invocation receives a fresh configured budget. Older review attempts remain audit history; the latest attempt controls status and follow-up behavior.

## Inspecting Review State

Use:

```text
orpheus task review show <task-id>
```

This is the inspection surface for persisted review state. It shows the latest authoritative review attempt, executed steps, findings, resolution state, autonomous budget exhaustion, interrupted automated blocker decisions, created follow-up Beads, and the next command.

Separate-task findings can be converted into Beads during `task review`. Created tasks include provenance in their description identifying the source task, repository, review attempt, and finding index. `task review show` lists those created follow-up tasks.

## Publication and Retry

When review passes, `task review` records a passed review and invokes the same internal finalization path as `task done`:

- repo-root default-branch work is committed, pushed, closed in the backend, and recorded locally;
- task-branch work is committed, pushed, published as a pull request, and recorded locally.

If publication or finalization fails after review has passed, the passed review remains valid. Fix the publication problem, such as authentication or remote push failure, then run:

```text
orpheus task done <task-id>
```

You do not need to rerun review just to retry publication.

For pull requests created after review follow-up runs, the PR title and leading body come from the original implementation completion, not from the follow-up completion. Orpheus appends a concise review-process section that records review attempts, finding outcomes, and follow-up run summaries without copying full finding descriptions or the follow-up run's detailed PR body.

## Status Guidance

Status groups and details tell the operator which command comes next:

- `Reviewing` with `local review; run task review`: implementation completed and needs approval.
- `Reviewing` with `review blocker decision interrupted; run task review`: automated-blocker classification lost input; start a fresh review.
- `Idle` with `review blocked by N finding(s); run task run`: open blocking findings need follow-up implementation.
- `Idle` with `review blocked after autonomous attempt budget by N finding(s); run task run to continue`: bounded autonomous repair stopped; explicitly continue to grant a fresh budget.
- `Reviewing` with `review blockers targeted; run task review`: follow-up work has targeted the blockers and needs another review.
- `Reviewing` with `review aborted; run task review`: review was stopped intentionally; rerun review when ready.
- `Needs attention` with `review failed operationally; run task review`: fix the review process or environment, then rerun review.
- `Reviewing` with `review passed; run task done`: approval exists and finalization can be retried or completed.
- `Needs attention` with `review passed; publication failed; fix publication issue, then run task done`: approval exists, but publication/finalization needs repair and retry.

## Deferred V1 Non-Goals

These ideas remain out of scope for the current workflow:

- reviewing updates to an already-published pull request after `orpheus.pr_url` is set;
- enforcing an exact reviewed tree hash at `task done`;
- durable local commits immediately after `agent done`;
- `task done --force`, `--skip-review`, or another review bypass;
- empty review pipelines;
- a dedicated no-change close workflow.
