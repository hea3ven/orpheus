# M7 Local Review Gate Validation

This note records the validation scope for the unified local-review gate. Completed-but-unpublished work stays in the existing `Reviewing` status group and uses detail text to tell the operator what to do next; no new status group or persistent reviewed marker is introduced.

## Command Sequence

Worktree/team PR flow:

```text
orpheus task run <task-id>
agent runs orpheus agent context
agent edits files
agent runs orpheus agent done --summary ... --description ... --detailed-description ...
orpheus status
orpheus task done <task-id>
orpheus status
orpheus task sync <task-id>
```

Expected results:

- after `agent done`, `status` shows the task under `Reviewing` with detail `local review; run task done`;
- `task done` commits reviewed work in the task worktree, pushes the task branch, creates or recovers the PR, and stores `orpheus.pr_url`;
- after `task done`, `status` still shows the task under `Reviewing`, but the detail is the PR URL;
- after the PR is merged, `task sync <task-id>` closes the backend task and records a local audit event.

Main/solo local finalization flow:

```text
orpheus task run --main <task-id>
agent runs orpheus agent context
agent edits files
agent runs orpheus agent done --summary ... --description ... --detailed-description ...
orpheus status
orpheus task done <task-id>
orpheus status --full
```

Expected results:

- after `agent done`, repo-root changes remain local for human review;
- `status` shows `local review; run task done`;
- `task done` commits reviewed repo-root changes, pushes the registered default branch, closes the backend task, and records finalization facts;
- final `status --full` shows the task in `Done / closed`.

## Automated Coverage

Primary end-to-end coverage lives in `internal/cli/completion_flows_e2e_test.go`:

- `TestWorktreeLocalReviewTaskDonePRFlowEndToEnd` covers `task run`, `agent done`, local review status detail, reviewed `task done` PR publication, PR URL status detail, open PR polling, merged PR sync, backend close, and local audit recording.
- `TestMainCompletionFlowEndToEnd` covers the existing main/solo flow under the unified `task done` model.

Focused status and task coverage lives in:

- `internal/status/status_test.go`
  - completed worktree/team tasks without PR URLs project to `Reviewing` with `local review; run task done`;
  - completed main/solo tasks without PR URLs project to the same existing group and detail;
  - tasks with `orpheus.pr_url` keep the PR URL as status detail.
- `internal/cli/status_test.go`
  - rendered status output includes `local review; run task done` for local review and PR URLs for PR review.
- `internal/cli/task_test.go`
  - `TestTaskDonePublishesPRReadyTaskBranch` and `TestTaskDoneRecoversExistingBranchPR` cover reviewed worktree publication;
  - `TestTaskDoneCommitsPushesClosesAndRecordsFinalization` covers main/solo finalization;
  - `TestTaskSyncAllPollsPRBoundaryTasks` and `TestTaskSyncAllGroupsCrossRepoResultsAndReturnsNonZeroAfterFailures` cover that batch sync polls existing PRs without publishing local-review tasks.
- `internal/workflow/sync_test.go`
  - `TestSyncServiceSkipsPRCreationForEligibleWorktreeCompletion` and `TestSyncServiceSyncAllIgnoresTasksWithoutPRURL` keep the local-review gate explicit below the CLI layer.
