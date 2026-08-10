# Review Pipeline Workflow

This guide describes the operator-facing workflow for completed task work. The review pipeline is the approval boundary between an agent's implementation run and publication or finalization.

## Normal Lifecycle

The standard path is:

```text
orpheus task run <task-id>
# implementation agent inspects context, edits files, then records the completion:
orpheus agent done --summary 'fix: concise summary' --description 'Concise plain description' \
  --detailed-description-file /tmp/pr-body.md \
  --technical-explanation-file /tmp/technical-explanation.md

orpheus task review <task-id>
# if the review passes, Orpheus publishes or finalizes through the same internal path used by task done
```

`task run` prepares the task target, records an attached run attempt, and launches the configured implementer. `agent done` records the completion summary, commit description, and pull-request body source. After that, the task is ready for local review, not direct publication.

## Safe Agent Reporting Text

Generated text is data, not shell source. Agents must never put generated prose in a double-quoted shell argument: JSON escaping is not Bash quoting, and Bash still expands backticks, `$()`, and variables inside double quotes. Use the existing file flags for multiline or Markdown fields: `--detailed-description-file`, `--technical-explanation-file`, `--description-file`, `--task-description-file`, and `--task-acceptance-criteria-file`.

Do not put arbitrary raw text in a fixed-delimiter heredoc: a generated line equal to the delimiter (such as `EOF`) ends it and turns later lines into shell input. Instead, base64-encode generated file contents and decode each payload from a single-quoted literal, such as `printf '%s' '<base64-data>' | base64 --decode >"$file"`; standard base64 data contains no apostrophes. For unavoidable non-base64 inline fields, use single-quoted shell literals; an embedded apostrophe is written by closing and reopening the literal, for example `'O'\''Brien'`. Review agents must write temporary report files outside the candidate worktree, such as a directory created by `mktemp -d /tmp/orpheus-review.XXXXXX`. After every `agent done` or `agent review add` reporting command, verify success before exiting or retrying; do not blindly retry a finding that might already be persisted.

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

- Blocking findings stop approval. Check and agent-review blockers prompt for an explicit keep, downgrade, or waive/cancel decision from both `task run` and `task review`. Keeping an automated blocker triggers bounded targeted fixes and fresh review attempts. A manual reviewer who records blockers and chooses `finish/block` implicitly keeps every eligible recorded blocker; no second confirmation is needed before the same bounded repair loop begins.
- Advisory findings are recorded but do not block approval.
- Separate-task findings do not block approval by themselves. During review, Orpheus can create standalone Beads for selected candidates.

Operational review failures are different from code or product blockers. Examples include a missing check executable, an attached review agent process failure, invalid pipeline configuration, or a read-only mutation failure. These project as operator attention. Fix the review process or environment, then rerun `orpheus task review <task-id>`.

## Follow-Up Runs

When the latest review is blocked by open current-task findings, `task run` enters follow-up mode automatically. There is no `--follow-up` flag. The new run targets the open blocking findings, records that targeting in task state, and keeps the task on the same implementation target.

Within one `task run` or `task review` invocation, Orpheus asks the operator to classify automated blockers. `keep` preserves a blocker, while manual `finish/block` implicitly preserves the blockers recorded at that manual gate. Either decision dispatches one selected implementer follow-up for every eligible blocker from the blocked attempt, then starts a fresh review from step 1 after the fix completes. `downgrade` converts an automated finding to advisory with a required reason. `waive`/`cancel` records a required waiver reason. The global `reviews.max_autonomous_review_attempts` setting bounds the keep/fix loop and defaults to `4`; the initial review counts, so the default allows at most three fix runs before the fourth blocked review stops.

If blocker-decision input disappears, Orpheus marks the current attempt blocked with an interrupted automated-decision flag, performs no publication/finalization, and launches no targeted fix. Before any fresh authoritative attempt, Orpheus presents each open, untargeted blocker from the latest attempt for an explicit keep, addressed-manually, or waive decision. A keep preserves the old review for targeted repair; addressed-manually and waive both require distinct reasons. Interrupted disposition leaves the remaining blockers authoritative and starts no fresh review.

If the budget is exhausted, Orpheus preserves the latest blockers and audit history, marks the blocked review as autonomous-budget-exhausted, and tells the operator to explicitly continue with a fresh command. A new `task run` or `task review` invocation receives a fresh configured budget and continues eligible preserved blockers without re-confirming a manual `finish/block`; automated blockers still require their recorded explicit keep decision. Older review attempts remain audit history; the latest attempt controls status and follow-up behavior.

When an agent process actually begins, Orpheus prints a delimited header. Initial implementation runs identify their run attempt, for example `== Agent run: implementation (run attempt 1) ==`. Targeted repairs additionally identify the source review and findings, for example `== Agent run: review follow-up (run attempt 2; review attempt 1; findings 1, 2) ==`. Review step headers and publication/finalization output remain the authoritative lifecycle messages.

Each follow-up is a new Orpheus run attempt and therefore a new `agent done` completion boundary. If session resumption reuses an earlier harness conversation, a successful `agent done` already visible in that history belongs to the earlier attempt and does not satisfy the current one. After the repair, the agent must record exactly one fresh handoff for the current attempt; same-attempt repeats stay no-ops, and exiting without that handoff keeps the incomplete-follow-up retry path.

## Inspecting Review State

Use hierarchical positional arguments:

```text
orpheus task review show <task-id>
orpheus task review show <task-id> <review-attempt>
orpheus task review show <task-id> <review-attempt> <finding-number>
```

The task-level command is a concise cross-attempt authoritative-finding history. An attempt number opens that persisted attempt in detail. A finding number opens exactly one authoritative finding, referenced as `<review-attempt>/<finding-number>`, including its full description, disposition reason, suggested action, task proposal or created task, and associated follow-up runs. Inspection never changes review or task state.

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
