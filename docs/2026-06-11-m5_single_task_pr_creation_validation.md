# M5 Single Task PR Creation Validation

This note records the final M5 validation scope for turning one PR-ready worktree/team task into a GitHub pull request. Automated validation is hermetic: it does not require live GitHub, network access, GitHub authentication, PR polling, merge detection, task close, or `task sync --all`.

## Automated validation

The end-to-end coverage lives in `internal/cli/completion_flows_e2e_test.go`. Focused CLI sync coverage lives in `internal/cli/task_test.go`, and lower-level orchestration coverage lives in `internal/workflow/sync_test.go`.

The tests use the repository's current CLI integration patterns:

- isolated `XDG_CONFIG_HOME` and `XDG_DATA_HOME` roots;
- temporary Git repositories;
- local bare Git remotes for push validation;
- fake or stateful `bd` executables for task reads and metadata writes;
- a fake `gh` executable for PR list/create behavior;
- an `orpheus` helper executable that re-enters the test CLI during attached-agent flows.

## Automated coverage map

Validated by `TestWorktreeSyncFlowEndToEnd`:

- `orpheus task run <task-id>` creates the deterministic worktree/team branch and worktree;
- the attached fake agent calls `orpheus agent done` and records a successful completion block;
- the completion includes a commit SHA matching the worktree branch head;
- `orpheus task sync <task-id>` pushes the task branch to the local bare `origin`;
- fake `gh pr list` reports no existing PR;
- fake `gh pr create` returns a PR URL;
- sync stores `orpheus.pr_url` through Beads metadata;
- `orpheus status` projects the task under `Reviewing` with the stored PR URL;
- a second `orpheus task sync <task-id>` exits successfully as already in review;
- the rerun does not call fake `gh` again and does not write duplicate PR URL metadata.

Validated by focused sync tests:

- PR-ready worktree task push and PR creation: `TestTaskSyncPushesPRReadyTaskBranch`;
- existing branch PR recovery without duplicate creation: `TestTaskSyncRecoversExistingBranchPR`;
- local idempotence when `orpheus.pr_url` already exists: `TestTaskSyncSkipsExistingPRURLWithoutPushOrProvider`;
- non-PR-ready skip cases: `TestSyncServiceSkipsNonEligibleTasks`;
- main/solo local-ready skip behavior: `TestTaskSyncSkipsMainSoloLocalReadyTask`;
- branch run at repo root skip behavior: `TestTaskSyncSkipsBranchRunAtRepoRoot`;
- status projection for tasks with PR URLs: `TestStatusGroupsLocalTaskSnapshots`.

## Optional manual smoke checklist

Use this checklist only when intentionally validating against a real GitHub repository with a configured `origin`, installed `gh`, and authenticated GitHub CLI session.

```text
orpheus task run <task-id>
orpheus agent done --summary "..." --description "..." --detailed-description "..."
orpheus task sync <task-id>
orpheus status
bd show <task-id>
orpheus task sync <task-id>
```

Expected manual results:

- branch exists on `origin`;
- GitHub PR exists;
- task metadata has `orpheus.pr_url`;
- `orpheus status` shows the task in review;
- rerun does not create a duplicate PR.
