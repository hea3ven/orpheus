# M6 PR Merge Sync Lifecycle Validation

This note records the M6 validation scope for the full worktree/team PR lifecycle: an agent completes a PR-ready branch, `task sync` creates or recovers a PR, later syncs poll the recorded PR URL, and merged PRs close the backend task with a local audit event.

Automated validation remains hermetic. It does not require live GitHub, network access, real GitHub authentication, or a real remote hosting provider. The tests use isolated XDG roots, temporary Git repositories, local bare `origin` remotes for push behavior, fake `bd` responses, and a shimmed `gh` executable for PR create/recover/view outcomes.

## Automated Coverage

The end-to-end creation path is covered by `TestWorktreeSyncFlowEndToEnd` in `internal/cli/completion_flows_e2e_test.go`:

- `orpheus task run <task-id>` creates the deterministic worktree/team branch and worktree;
- the attached fake agent calls `orpheus agent done` and records a successful completion block;
- `orpheus task sync <task-id>` pushes the task branch to a local bare `origin`;
- fake `gh pr list` reports no existing PR and fake `gh pr create` returns a PR URL;
- sync stores `orpheus.pr_url` through Beads metadata;
- the initial PR creation invocation does not immediately poll the newly stored PR URL;
- `orpheus status` projects the task under `Reviewing` with the stored PR URL;
- a later `orpheus task sync <task-id>` polls the stored PR URL and reports it still open.

Focused CLI coverage in `internal/cli/task_test.go` validates:

- PR-ready worktree task push and PR creation without immediate `gh pr view`: `TestTaskSyncPushesPRReadyTaskBranch`;
- existing branch PR recovery without duplicate creation: `TestTaskSyncRecoversExistingBranchPR`;
- existing PR URL polling even without local run/completion facts or a live worktree: `TestTaskSyncPollsExistingPRURLWithoutPushOrMutation`;
- merged PR polling, backend close, and local `task_closed_due_to_pr_merged` audit event: `TestTaskSyncClosesBackendAndRecordsLocalAuditForMergedPR`;
- closed-unmerged PRs, malformed/missing provider PR URLs, inaccessible repositories, invalid stored PR URLs, and provider/auth failures return errors without backend close, metadata writes, or local audit events: `TestTaskSyncExistingPRErrorsDoNotMutateBackendOrAudit`;
- closed backend tasks skip without PR polling: `TestTaskSyncSkipsClosedTaskWithoutPRPolling`;
- `task sync --all` finds PR-ready and existing-PR candidates, groups created/open/merged/error results, continues after per-task and per-repo failures, and returns non-zero when any failure occurs: `TestTaskSyncAllGroupsCrossRepoResultsAndReturnsNonZeroAfterFailures`.

Focused workflow coverage in `internal/workflow/sync_test.go` keeps lower-level orchestration behavior explicit:

- existing PR polling takes precedence over local run-state eligibility;
- open, merged, closed-unmerged, unsupported-state, and provider-error PR outcomes are handled without unwanted push/create calls;
- merged PR close failures do not write the post-close audit event;
- post-close audit failures are surfaced after backend close;
- `SyncAll` scans PR-boundary tasks across repositories and continues after candidate and repository failures.

## Locking Note

For MVP simplicity, `task sync --all` holds the global mutation lock continuously across candidate scanning and the subsequent per-task sync operations. This prevents competing Orpheus mutations from interleaving with a batch reconciliation, but it also means slow provider calls, slow `bd` commands, or slow Git operations can block other Orpheus mutations until the batch completes.

This behavior is documented as an intentional MVP tradeoff. The validation task does not require a dedicated concurrency test beyond the current workflow-level lock coverage.

## Optional Manual Smoke Checklist

Use this checklist only when intentionally validating against a real GitHub repository with a configured `origin`, installed `gh`, and authenticated GitHub CLI session.

```text
orpheus task run <task-id>
orpheus agent done --summary "..." --description "..." --detailed-description "..."
orpheus task sync <task-id>
# merge the PR on GitHub
orpheus task sync <task-id>
orpheus status
bd show <task-id>
```

Expected manual results:

- branch exists on `origin`;
- GitHub PR exists and is merged by the human;
- the second sync detects the merged PR;
- backend task is closed;
- local task-state audit event records that the task was closed because the PR was merged;
- `status` remains local-only and shows closed backend state after the backend snapshot updates.
