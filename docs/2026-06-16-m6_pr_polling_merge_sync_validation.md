# M6 PR Polling + Merge Sync Validation

This note records the current validation scope for Milestone 6: polling existing PR URLs, closing backend tasks after merged PRs, and syncing all registered PR-boundary tasks.

Automated validation is hermetic: it uses fake PR providers, fake/stateful task backends, temporary repositories, and isolated Orpheus state. It does not require live GitHub, network access, or GitHub authentication.

## Automated coverage

Primary coverage lives in:

- `internal/workflow/sync_test.go`
- `internal/cli/task_test.go`
- `internal/cli/completion_flows_e2e_test.go`

Validated by workflow-level tests:

- `TestSyncServicePollsOpenPRWithoutLocalEligibility`
  - a task with `orpheus.pr_url` is polled even when local PR-creation eligibility is not rechecked;
  - open PRs remain in review and do not mutate backend state.
- `TestSyncServiceClosesTaskAndRecordsAuditForMergedPR`
  - a merged PR closes the backend task;
  - the local task-state audit event records the merged PR observation.
- `TestSyncServiceMergedPRCloseAndAuditFailures`
  - backend close failures and audit-recording failures are surfaced clearly.
- `TestSyncServiceClosedTaskSkipsWithoutPRPolling`
  - already closed backend tasks are skipped instead of polling PR state.
- `TestSyncServiceExistingPRFailuresAreHardErrors`
  - inaccessible, closed-unmerged, or unsupported provider PR states are hard sync errors.
- `TestSyncServiceSyncAllScansPRBoundaryTasksAndContinuesAfterFailures`
  - `task sync --all` scans registered repositories for PR-boundary tasks;
  - candidate-level failures do not stop unrelated candidates from syncing.
- `TestSyncServiceSyncAllReportsCleanNonSyncableActionAsSkipped`
  - clean non-syncable tasks are reported as skipped rather than causing a batch failure.
- `TestSyncServiceSyncAllHoldsGlobalLockAcrossScanAndSync`
  - batch sync holds the global mutation lock across scanning and per-task sync.

Validated by CLI-level tests:

- `TestTaskSyncPollsExistingPRURLWithoutPushOrMutation`
  - `orpheus task sync <task-id>` polls an existing PR URL without pushing or rewriting metadata.
- `TestTaskSyncClosesBackendAndRecordsLocalAuditForMergedPR`
  - CLI sync closes the backend task and records local audit facts when the PR is merged.
- `TestTaskSyncAllCreatesAndPollsPRBoundaryTasks`
  - `orpheus task sync --all` creates PRs for PR-ready tasks and polls tasks already in review.
- `TestTaskSyncAllReturnsNonZeroAfterCandidateError`
  - batch sync renders successful results and still returns nonzero when one candidate fails.

## Current behavior summary

Single-task sync:

```text
if task has orpheus.pr_url:
  poll provider state
  if open: report still in review
  if merged: close backend task and record audit event
  if closed unmerged/inaccessible/unsupported: fail clearly
else if latest successful worktree/team completion is PR-ready:
  push branch, recover/create PR, store orpheus.pr_url
else:
  skip with a reason
```

Batch sync:

```text
for every registered repo:
  scan active non-epic tasks
  include tasks with orpheus.pr_url
  include PR-ready worktree/team completions without PR URLs
  sync candidates under the global mutation lock
  group created/recovered PRs, open PRs, merged/closed tasks, skipped tasks, and errors
```

## Retrospective caveat

The 2026-06-16 MVP retrospective identified a workflow issue: local human review before PR creation is critical. The current `task sync --all` behavior can create PRs for all PR-ready tasks, so the remaining MVP scope should add a local review gate or otherwise prevent batch PR creation by default.

Until that adjustment is implemented, operators should prefer explicit per-task `orpheus task sync <task-id>` after local review, and use `task sync --all` carefully.

## Optional manual smoke checklist

Use this only with a real GitHub repository, configured `origin`, installed `gh`, and an authenticated GitHub CLI session.

Open PR polling:

```bash
orpheus task sync <task-id-with-open-pr-url>
orpheus status
```

Expected result:

- the command reports the PR is still open for review;
- backend task remains non-closed.

Merged PR sync:

```bash
# after merging the GitHub PR
orpheus task sync <task-id>
orpheus status --full
bd show <task-id>
```

Expected result:

- the command reports the PR is merged;
- backend task is closed;
- status shows the task as done/closed.

Batch sync:

```bash
orpheus task sync --all
```

Expected result:

- open PRs, merged PRs, skipped tasks, and errors are grouped clearly;
- unrelated tasks continue syncing after per-task failures.
