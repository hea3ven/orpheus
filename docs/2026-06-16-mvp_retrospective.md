# Orpheus MVP Retrospective — 2026-06-16

This note records what has been built so far, what the current implementation taught us about the actual workflow, and what should change in the remaining MVP scope.

## Data sources reviewed

- MVP spec: [`2026-05-12-agent_orchestration_cli_mvp_spec.md`](./2026-05-12-agent_orchestration_cli_mvp_spec.md)
- MVP milestones: [`2026-05-16-mvp_implementation_milestones.md`](./2026-05-16-mvp_implementation_milestones.md)
- Validation notes through M5:
  - [`2026-05-22-m1_repo_registration_validation.md`](./2026-05-22-m1_repo_registration_validation.md)
  - [`2026-05-31-m2_global_task_visibility_validation.md`](./2026-05-31-m2_global_task_visibility_validation.md)
  - [`2026-06-03-m3_interactive_single_task_dispatch_validation.md`](./2026-06-03-m3_interactive_single_task_dispatch_validation.md)
  - [`2026-06-08-m4_dual_completion_validation.md`](./2026-06-08-m4_dual_completion_validation.md)
  - [`2026-06-11-m5_single_task_pr_creation_validation.md`](./2026-06-11-m5_single_task_pr_creation_validation.md)
- Current code paths for task dispatch, completion, finalization, PR sync, status projection, and worktree setup.
- Recent implementation history, including PR creation, PR polling, merge sync, `task sync --all`, `task dir`, and main/solo finalization work.

## What has been built so far

### Completed or substantially complete MVP slices

1. **Repo registry and Beads discovery**
   - `repo add`, `repo list`, and `repo beads-dir` exist.
   - Local and managed Beads modes are supported.
   - Registry state is YAML and remains inspectable/editable.
   - Duplicate repo/path/prefix detection is covered.

2. **Global task visibility**
   - `task list`, `task ready`, `task show`, and `status` exist.
   - Orpheus uses its own local readiness projection rather than delegating to `bd ready`.
   - Cross-repo snapshots, dependency classification, PR metadata projection, and partial repo diagnostics are covered.

3. **Attached single-task dispatch**
   - `task run <task-id>` launches an attached agent in the current terminal.
   - Default dispatch prepares an Orpheus deterministic task branch/worktree.
   - Agent profiles support command plus args and prompt interpolation.
   - Run attempts and trace events are persisted in local Orpheus task-state files.
   - A global mutation lock exists around setup/finalization boundaries.

4. **Dual completion workflows**
   - `agent context` renders target-aware, backend-neutral instructions.
   - `agent done` records summary, description, detailed description, and completion facts.
   - Worktree/team completion commits changes on the task branch.
   - Main/solo completion leaves repo-root changes for local review.
   - `task done` finalizes reviewed main/solo work by committing, pushing the registered default branch, and closing the backend task.

5. **Single-task PR creation**
   - `task sync <task-id>` can push a PR-ready task branch and create or recover a GitHub PR through `gh`.
   - PR body/title are derived from the recorded completion handoff.
   - `orpheus.pr_url` is written back to task metadata.
   - Re-running sync is idempotent when the PR URL is already recorded.

6. **PR polling and merge sync**
   - Tasks with recorded PR URLs are polled through the PR provider.
   - Open PRs remain in review.
   - Merged PRs close the backend task and record a local audit event.
   - `task sync --all` scans registered repositories for PR-boundary tasks and continues after per-task failures.

### Intentional changes from the original MVP plan

- The MVP shifted from background/detached agents to **attached interactive runs**.
- `task logs` is not central because attached runs stream directly to the terminal.
- `run-ready` was deferred because batch scheduling is less useful until background agents, runner nodes, or an explicit local-review gate exist.
- The implementation now has two practical workflow targets:
  - **worktree/team**: dedicated worktree + task branch + PR flow.
  - **main/solo**: repo root + default branch + local review/finalize flow.

## Workflow observations from using Orpheus to build Orpheus

### 1. Local human review before PR creation is critical

The PR boundary is where other people and GitHub review become involved. For this workflow, the operator wants to inspect the local code changes before creating a PR. That means Orpheus should not treat "agent completed successfully" as equivalent to "safe to publish for review".

Current behavior mostly allows manual review because `task sync <task-id>` is explicit, but there are two gaps:

- `task sync --all` can create PRs for all PR-ready tasks, which can bypass per-task human local review.
- Status currently reports worktree/team completion as `Needs attention: needs PR`, which does not clearly communicate "review locally first, then create PR".

MVP implication:

- Worktree/team completion should project as a **local review needed** state, not merely a generic needs-attention state.
- Creating a PR should be an explicit post-review operator action.
- Batch sync should not create new PRs by default unless there is an explicit reviewed/approved signal or an explicit create-PRs option.

A minimal MVP-safe interpretation is:

```text
agent done
  ↓
local review needed
  ↓
human inspects task dir / branch and optionally edits
  ↓
explicit per-task PR creation/sync
  ↓
GitHub review
```

### 2. Work directory and branch/review flow are separate axes

The current implementation has useful modes, but the names and mechanics conflate separate concerns:

| Axis | Values |
|---|---|
| Work directory | registered repo root, dedicated Orpheus worktree |
| Branch/integration flow | default branch/local finalization, feature branch/PR flow |

Current supported combinations:

| Work directory | Branch/integration flow | Current state |
|---|---|---|
| Dedicated worktree | Feature branch + PR | Supported by default `task run` |
| Repo root | Default branch + no PR | Supported by `task run --main` + `task done` |

Desired MVP combinations:

| Work directory | Branch/integration flow | MVP decision |
|---|---|---|
| Dedicated worktree | Feature branch + PR | Keep supported |
| Repo root | Default branch + no PR | Keep supported for very small/solo changes |
| Repo root | Feature branch + PR | Add support; useful for quick tasks that still need a PR |
| Dedicated worktree | Default branch + no PR | Probably disallow; it adds complexity without matching the current workflow |

Repo-root work is valuable for small/quick implementations because the operator can stay in the normal checkout. However, repo-root work should not necessarily imply default-branch direct finalization. The missing combination is **repo root + feature branch + PR**.

MVP implication:

- Model dispatch target as two dimensions internally, even if the CLI keeps compatibility aliases.
- Treat current `--main` as an alias for something like `repo-root + default-branch/local`.
- Add a repo-root feature-branch PR target for quick tasks that should still go through PR review.

Possible future CLI shapes, names TBD:

```bash
orpheus task run <task-id>                         # worktree + feature branch + PR
orpheus task run --main <task-id>                  # repo root + default branch + local done, legacy alias
orpheus task run --work-dir repo-root --flow pr <task-id>
orpheus task run --work-dir repo-root --flow local <task-id>
```

or:

```bash
orpheus task run --target worktree-pr <task-id>
orpheus task run --target root-pr <task-id>
orpheus task run --target root-local <task-id>
```

### 3. `task dir` is an important review affordance

`task dir <task-id>` is useful because it gives the operator a direct way to enter the correct review target without remembering whether the task used a worktree or the repo root.

MVP implication:

- Keep `task dir` as part of the core local-review workflow.
- Documentation should use it before PR creation:

```bash
cd "$(orpheus task dir <task-id>)"
git status
git diff HEAD~1..HEAD   # or equivalent local review command
orpheus task sync <task-id>
```

### 4. Human edits after agent completion need explicit handling

For worktree/team tasks, `agent done` commits the agent's changes. During local review, the human may make small fixes before creating the PR.

Current sync has an explicit MVP limitation: it pushes the task branch but does not verify that the recorded completion commit is still the branch head. This makes local edits possible if the human commits manually, but the state model does not clearly distinguish agent completion from human review edits.

MVP implication:

- Before PR creation, Orpheus should at least detect a dirty worktree and refuse with guidance.
- Longer-term, Orpheus may need a small `task review` / `task approve` / `task amend` concept to record that local review happened.
- The local review gate does not need to enforce tests in the MVP, but it should prevent accidental publication of unreviewed or dirty work.

## Proposed remaining MVP scope adjustments

### Treat these as done, pending documentation cleanup

- M1 Repo Registry + Beads Discovery
- M2 Global Task Visibility
- M3 Attached Single Task Dispatch
- M4 Agent Context + Completion + Orpheus Commit / Main-Solo Finalization
- M5 Single Task PR Creation
- M6 PR Polling + Merge Sync

A dedicated M6 validation note now records the current PR polling, merge-sync, and `task sync --all` coverage: [`2026-06-16-m6_pr_polling_merge_sync_validation.md`](./2026-06-16-m6_pr_polling_merge_sync_validation.md).

### Add/adjust remaining MVP milestones

#### New M7 — Local review gate before PR creation

Goal: make local human review an explicit workflow boundary before publishing PRs.

Scope:

- Rename/project status for worktree completion as local review needed / ready for PR after review.
- Ensure `task sync --all` does not create new PRs by default without explicit operator intent.
- Consider a per-task confirmation prompt or explicit reviewed marker for PR creation.
- Detect dirty worktree state before PR creation and provide repair guidance.
- Document the review flow using `task dir`.

#### New M8 — Repo-root feature-branch PR target

Goal: support quick repo-root implementations without forcing direct default-branch finalization.

Scope:

- Add a target equivalent to repo root + feature branch + PR.
- Keep current worktree + feature branch + PR target.
- Keep current repo root + default branch + local finalization target.
- Disallow worktree + default branch + local finalization unless a future workflow proves it useful.
- Update target classification and agent context wording to reflect work-dir and branch/review axes.

#### Revised M9 — Hardening and recovery

Goal: make the supported single-task workflows dependable.

Scope:

- Dirty worktree/repo-root diagnostics around local review and PR creation.
- Recorded completion commit vs branch HEAD consistency checks.
- Safer retry guidance for stale running attempts.
- Clear recovery for PR creation failures, closed-unmerged PRs, missing branches/worktrees, and metadata mismatches.
- M6 validation note and regression checklist.

#### Revised M10 — MVP polish and documentation

Goal: make the MVP understandable and usable from a fresh checkout.

Scope:

- README quickstart for the adjusted workflows.
- Example config and agent profiles.
- Documentation for:
  - worktree + feature branch + PR;
  - repo root + feature branch + PR;
  - repo root + default branch + local finalization;
  - local review before PR creation;
  - `task dir`, `agent context`, `agent done`, `task sync`, `task done`, `task sync --all`.
- Manual validation checklist covering one full PR lifecycle and one local-finalization lifecycle.

### Keep deferred beyond MVP

- Batch `run-ready` scheduling.
- Background/detached agents and log capture.
- Full attachable/resumable terminal sessions.
- Provider-specific session resume.
- Post-implementation agent review step.
- Review comment parsing and relaunch.
- Required local validation/test reporting.

## Bottom line

The MVP has proven the core vertical path from Beads task to agent run to completion to PR/merge sync. The main scope correction is not more automation; it is a better human-control boundary:

> Agent completion should produce local reviewable work. PR creation should happen only after the operator has reviewed that work locally.

The second scope correction is to separate execution location from integration flow:

> Repo root vs worktree and default branch vs feature branch/PR are independent workflow choices. The MVP should support all useful combinations and reject the confusing one.
