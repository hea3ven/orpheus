# M4 Dual Completion Flow Validation

This note records the final M4 validation scope for the two first-class completion workflows. It intentionally validates local behavior only: no real GitHub, PR creation, PR polling, merge sync, network authentication, or operator state is required.

## Automated validation

The end-to-end coverage lives in `internal/cli/completion_flows_e2e_test.go`.

The tests use the repository's current CLI integration patterns:

- isolated `XDG_CONFIG_HOME` and `XDG_DATA_HOME` roots;
- temporary Git repositories;
- local bare Git remotes for push validation;
- configured fake agent profiles;
- a fake stateful `bd` executable for task reads, updates, and close calls;
- an `orpheus` helper executable that re-enters the test CLI while the fake attached agent is still running.

## Worktree/team checklist

Flow:

```text
orpheus task run <task-id>
agent runs orpheus agent context
agent edits file
agent runs orpheus agent done --summary ... --details ...
orpheus status
```

Validated outcomes:

- launch prompt is the minimal bootstrap prompt and tells the agent to run `orpheus agent context`;
- prompt excludes task/repository details that should only be available from `agent context`;
- `agent context` renders target-aware worktree/team context;
- `agent done` records summary, details, completed_at, and commit;
- worktree changes are committed and the worktree is clean;
- latest run attempt succeeds;
- `status` shows `Needs attention` with detail `needs PR`;
- no PR URL is written;
- backend task remains non-closed for later PR creation.

## Main/solo checklist

Flow:

```text
orpheus task run --main <task-id>
agent runs orpheus agent context
agent edits file
agent runs orpheus agent done --summary ... --details ...
human review/adjustment adds local changes
orpheus task done <task-id>
orpheus status --full
```

Validated outcomes:

- `agent context` renders target-aware main/solo context;
- `agent done` records summary, details, and completed_at without committing;
- repo-root changes remain uncommitted after agent completion;
- `status` shows `Reviewing` with detail `local review; run task done`;
- human review is represented by an additional uncommitted local change;
- `task done` commits reviewed changes using the recorded completion message;
- `task done` pushes the registered default branch to the local bare remote;
- the fake backend refuses close while reviewed changes are uncommitted or before the push is visible, validating close-after-push order;
- backend task closes only after commit and push;
- final `status --full` shows the task as done/closed.
