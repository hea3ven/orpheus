# Orpheus MVP Implementation Milestones

This document breaks the Orpheus MVP into useful implementation milestones. Each milestone should produce a coherent, testable slice of functionality rather than a purely technical layer.

The intended implementation order is:

1. Repo Registry + Beads Discovery
2. Global Task Visibility
3. Agent Profiles + Single Task Dispatch
4. Agent Context + Completion + Orpheus Commit
5. Single Task PR Creation
6. PR Polling + Merge Sync
7. Batch Scheduling: `run-ready`
8. Failure, Retry, and Recovery Hardening
9. MVP Polish + Documentation

The goal is to validate the main assumptions incrementally:

- Orpheus can discover and use Beads-backed task sources.
- Orpheus can present a global action queue across repos.
- Orpheus can create deterministic task worktrees and launch agents.
- Agents can complete work through an Orpheus-owned handshake.
- Orpheus can create PRs and later sync merged PRs back into Beads.
- Multiple ready tasks can be launched safely under operator control.

---

## Milestone 1 — Repo Registry + Beads Discovery

### Goal

Validate that Orpheus can register repositories, detect Beads mode, and create the global machine-local state layout.

### Features

Commands:

```bash
orpheus repo add <path>
orpheus repo list
orpheus repo beads-dir <repo-name-or-prefix>
```

Implementation scope:

- Go CLI skeleton.
- Global config/data path resolution.
- Human-editable state file layout.
- Repo registry.
- Interactive `repo add` wizard.
- Git repo detection.
- Git remote detection.
- Default branch detection with user confirmation.
- Beads mode detection:
  - local if repo has Beads
  - managed otherwise
- Global Beads initialization for repos without local Beads.
- Beads prefix detection/storage.
- Duplicate Beads prefix rejection.
- `repo beads-dir` escape hatch.

### Useful By Itself Because

Orpheus can already be used as a registry of repositories and their Beads locations.

### Validation

```bash
orpheus repo add ~/dev/my-repo
orpheus repo list
cd "$(orpheus repo beads-dir my-repo)"
bd list
```

Success criteria:

- Local-Beads repo resolves to the repo path.
- Non-Beads repo gets a managed Beads directory under Orpheus global data.
- Duplicate Beads prefixes are rejected.
- Repository records are human-readable files.

---

## Milestone 2 — Global Task Visibility

### Goal

Validate that Orpheus can see Beads tasks across all registered repos and present a global action queue.

### Features

Commands:

```bash
orpheus task list
orpheus task ready
orpheus status
```

Implementation scope:

- Narrow internal `TaskBackend` interface.
- Beads-backed implementation using the `bd` CLI.
- Task ID to repo resolution through Beads prefix.
- Cross-repo task snapshot/listing.
- Orpheus readiness projection for `task ready`, derived from task snapshots rather than backend-native ready commands.
- Local-only `status` projection.
- Basic Beads metadata read support.
- Structured local read diagnostics with source and operation.

Initial status groups:

```text
Ready to run
Working
Blocked
In review
Done / closed
Unknown / needs attention
```

M2 readiness rules:

```text
status == open
AND no non-empty orpheus.pr_url
AND every dependency id resolves within the same repository snapshot
AND every resolved dependency has status == closed
=> Ready to run
```

Known non-closed dependencies place an item in `Blocked`. Missing dependencies place an item in `Unknown / needs attention`.

No agent launching yet.

### Useful By Itself Because

This already gives the user a global Orpheus action queue across registered task backends.

### Validation

Create tasks manually:

```bash
cd "$(orpheus repo beads-dir my-repo)"
bd create "Example task"
```

Then:

```bash
orpheus task list
orpheus task ready
orpheus status
```

Success criteria:

- Tasks from multiple registered repos appear together.
- `task ready` and `status` use the same Orpheus readiness semantics.
- Closed items are available to the status projection for `Done / closed`.
- `status` does not call GitHub.
- Task IDs resolve by prefix.

Validation notes: [M2 Global Task Visibility Validation](./2026-05-31-m2_global_task_visibility_validation.md).

---

## Milestone 3 — Agent Profiles + Single Task Dispatch

### Goal

Validate that Orpheus can deterministically claim a task, create/recover worktree state, and launch a configured agent command.

### Features

Commands:

```bash
orpheus task run <task-id>
orpheus task run --agent <agent-name> <task-id>
orpheus task logs <task-id>
```

Implementation scope:

- Global config with named agent profiles.
- Default agent profile.
- Global mutation-lock concept.
- Task run eligibility checks:
  - task exists
  - task not closed
  - task has no PR URL
  - no active run
  - latest successful run is not PR-ready
- Native Beads claim/in-progress behavior.
- Deterministic branch naming.
- Deterministic global worktree path.
- Store in Beads metadata:
  - `orpheus.branch`
  - `orpheus.worktree`
- Branch creation from fetched `origin/<default_branch>`.
- Worktree creation under global Orpheus data.
- Self-healing setup:
  - recreate missing worktree if safe
  - reuse existing branch/worktree
- Run record creation.
- Agent process launch with:
  - `ORPHEUS_REPO_ID`
  - `ORPHEUS_TASK_ID`
  - `ORPHEUS_WORKTREE`
  - `ORPHEUS_BRANCH`
- Log capture.

### Useful By Itself Because

A dummy or real shell command can be dispatched into an isolated task worktree.

Example dummy agent:

```yaml
default_agent: echoer

agents:
  echoer:
    command: "sh -c 'env | grep ORPHEUS; pwd; git branch --show-current'"
```

### Validation

```bash
orpheus task run myrepo-123
orpheus task logs myrepo-123
```

Success criteria:

- Task is claimed.
- Branch/worktree are created.
- Agent command runs in the worktree.
- Env vars are present.
- Logs are available.
- Re-running after a failed dummy run reuses the same worktree.

Validation notes: [M3 Interactive Single Task Dispatch Validation](./2026-06-03-m3_interactive_single_task_dispatch_validation.md).

---

## Milestone 4 — Agent Context + Completion + Orpheus Commit

### Goal

Validate the core agent contract: the agent gets backend-neutral task context, implements changes, and Orpheus commits the result.

### Features

Commands:

```bash
orpheus agent context
orpheus agent done --summary "Short summary" --description "Concise commit body" --detailed-description "Markdown PR body"
```

Enhancements:

```bash
orpheus status
orpheus task logs <task-id>
```

Implementation scope:

- Backend-neutral agent context.
- No Beads terminology exposed to the agent.
- Agent completion validation:
  - env task id exists
  - cwd is inside expected worktree
  - worktree matches task metadata
  - branch matches task metadata
  - task is not closed
  - task has no PR URL
- Orpheus-owned commit:
  - stage changes
  - commit with standardized message
- Run completion update:
  - status done
  - summary
  - description
  - detailed_description
  - `pr_ready: true`
- Status group:
  - `Implementation complete / needs PR`

### Useful By Itself Because

A real agent can now complete implementation work and leave a committed branch ready for PR creation, even before GitHub integration exists.

### Validation

Example test agent:

```yaml
agents:
  test-agent:
    command: >
      sh -c 'orpheus agent context;
             echo hello >> ORPHEUS_TEST.txt;
             orpheus agent done --summary "Add test file" --description "Created ORPHEUS_TEST.txt" --detailed-description "Created ORPHEUS_TEST.txt"'
```

Then:

```bash
orpheus task run --agent test-agent myrepo-123
orpheus status
```

Success criteria:

- Agent context is correct.
- Commit is created by Orpheus.
- Run record has `pr_ready: true`.
- Status shows task needs PR creation.
- Beads has branch/worktree metadata, but not a single run id.

---

## Milestone 5 — Single Task PR Creation

### Goal

Validate that Orpheus can turn a PR-ready run into a GitHub PR.

### Features

Command:

```bash
orpheus task sync <task-id>
```

Implementation scope:

- Internal PR provider interface.
- `gh` CLI PR provider.
- PR body generation.
- Git push of task branch.
- PR creation against registered default branch.
- Store in Beads metadata:
  - `orpheus.pr_url`
- Status projection:
  - task with PR URL and not closed → `In review`

`task sync <task-id>` behavior:

```text
if latest successful run has pr_ready=true
and task has no orpheus.pr_url:
  push branch
  create PR
  store PR URL
```

### Useful By Itself Because

A single task can now move from Bead to GitHub PR.

### Validation

```bash
orpheus task sync myrepo-123
orpheus status
bd show myrepo-123
```

Success criteria:

- Branch is pushed.
- GitHub PR is created.
- Bead gets `orpheus.pr_url`.
- Re-running `task sync` is idempotent and does not create duplicate PRs.

---

## Milestone 6 — PR Polling + Merge Sync

### Goal

Validate the full single-task lifecycle through merge.

### Features

Commands:

```bash
orpheus task sync <task-id>
orpheus task sync --all
```

Enhancements:

```bash
orpheus status
```

Implementation scope:

- PR polling with `gh`.
- Detect open PR.
- Detect merged PR.
- Close Beads task when PR is merged.
- Sync all registered repos/tasks.
- Error/skipped grouping.
- Recovery behavior:
  - PR URL exists but inaccessible
  - PR closed unmerged
  - PR already merged
  - Beads close failure

`task sync --all` handles:

```text
PR-ready tasks without PRs
+
tasks with PR URLs needing polling
```

### Useful By Itself Because

This completes the full manual lifecycle:

```text
Bead → worktree → agent → commit → PR → human merge → Orpheus closes Bead
```

### Validation

After merging a PR:

```bash
orpheus task sync --all
```

Success criteria:

- Open PR remains in review.
- Merged PR closes the Bead.
- Closed Bead no longer appears as active work.
- Errors are grouped clearly.

---

## Milestone 7 — Batch Scheduling: `run-ready`

### Goal

Validate Orpheus as a multi-task operator console.

### Features

Commands:

```bash
orpheus task run-ready --limit <n>
orpheus task run-ready --limit <n> --agent <agent-name>
orpheus task run-ready --limit <n> --yes
```

Implementation scope:

- Ready-task selection across repos using Orpheus readiness semantics.
- Global concurrency limit.
- Filtering:
  - issue type is not epic
  - status is open
  - dependencies resolve within the same repository and are closed
  - no active run
  - no PR URL
  - no latest PR-ready run
  - runnable task only
- Confirmation prompt by default.
- `--yes` non-interactive override.
- Per-command agent override.
- Batch launch of selected tasks.
- Clear launch summary.

### Useful By Itself Because

This is where Orpheus provides orchestration leverage over manually running agents one task at a time.

### Validation

```bash
orpheus task ready
orpheus task run-ready --limit 3 --agent test-agent
orpheus status
```

Success criteria:

- It previews exact tasks before launching.
- It launches no more than the limit.
- It does not duplicate already-running/PR-ready/in-review tasks.
- Concurrent invocations cannot schedule the same task twice once the mutation lock is implemented.

---

## Milestone 8 — Failure, Retry, and Recovery Hardening

### Goal

Make the MVP robust enough for daily use.

### Features

Enhancements:

```bash
orpheus task run <task-id>
orpheus status
orpheus task logs <task-id>
orpheus task sync <task-id>
```

Implementation scope:

- Failed run detection.
- Retry behavior:
  - same task
  - same branch/worktree
  - new run attempt
- Better logs UX:
  - latest log
  - list attempts
- Inconsistent worktree diagnostics.
- Safe refusal for dangerous states.
- Clear repair messages.
- Cheap process status reconciliation where available.
- Better status groups:
  - Failed / needs retry
  - Implementation complete / needs PR
  - In review
  - Needs attention

### Useful By Itself Because

This is what makes the tool dependable rather than just a happy-path demo.

### Validation

Force scenarios:

- agent exits non-zero
- worktree deleted
- branch exists but worktree missing
- PR creation fails
- `gh` unavailable
- task has PR URL but PR is closed unmerged

Success criteria:

- Orpheus does not corrupt state.
- Retry works for normal failures.
- Dangerous states produce actionable errors.
- Status tells the operator what to do next.

---

## Milestone 9 — MVP Polish + Documentation

### Goal

Make the MVP usable by someone other than the implementer.

### Features

Implementation scope:

- README quickstart.
- Example config.
- Example agent profiles.
- Example implementation prompt.
- Command help text.
- Optional `orpheus doctor` command.
- Better error messages.
- Manual testing checklist.
- Basic unit tests around:
  - repo registry
  - task ID prefix resolution
  - run record selection
  - status projection
  - slug/branch naming
- Integration test harness using temp git repos where feasible.

### Useful By Itself Because

This turns the vertical slices into a coherent MVP release.

### Validation

Fresh-machine-style test:

```bash
orpheus repo add ...
orpheus task list
orpheus task run ...
orpheus agent context
orpheus agent done ...
orpheus task sync ...
orpheus task sync --all
```

Success criteria:

- A new user can follow docs and complete one full task lifecycle.
- Failures are understandable.
- Global files are inspectable.
- No daemon is required.

---

## Dependency Summary

The recommended milestone order is linear for the core product:

```text
M1 Repo Registry + Beads Discovery
  ↓
M2 Global Task Visibility
  ↓
M3 Agent Profiles + Single Task Dispatch
  ↓
M4 Agent Context + Completion + Orpheus Commit
  ↓
M5 Single Task PR Creation
  ↓
M6 PR Polling + Merge Sync
  ↓
M7 Batch Scheduling: run-ready
  ↓
M8 Failure, Retry, and Recovery Hardening
  ↓
M9 MVP Polish + Documentation
```

Some polishing tasks can happen earlier, but the main implementation should follow this order to keep every milestone independently useful and testable.
