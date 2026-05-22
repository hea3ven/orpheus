# Agent Orchestration CLI — MVP Spec

## 1. Purpose

This MVP is a hardened, deterministic version of the existing prompt-based Beads → worktree → agent → PR → merge-sync workflow.

The MVP is **not** the full orchestration platform described in `agent_orchestration_cli_product_brief.md`.

The first product goal is:

> Turn a fragile prompt-driven Beads → worktree → agent → PR → merge-sync workflow into a reliable CLI-controlled orchestration loop.

The MVP bet is:

> Users do not first need a smarter planning agent. They need reliable operational control over many small coding-agent task runs.

The differentiator for the MVP is reliability, visibility, and deterministic execution around existing agent workflows — not autonomous planning.

---

## 2. Primary User

The first user is a solo power user / staff-engineer-style operator who wants to coordinate background coding agents across repo-local tasks while retaining PR-based control.

---

## 3. MVP Product Boundary

### 3.1 MVP Includes

- Beads-first task backend.
- Repo-local tasks as the core execution unit.
- Global repository registry.
- Global action queue/status across registered repos.
- Global named agent profiles backed by shell command templates.
- Deterministic branch and worktree creation.
- One stable branch/worktree per task.
- Multiple execution attempts/runs per task.
- Agent-facing context and completion commands.
- Orpheus-owned commits during completion.
- Orchestrator-created PRs using the `gh` CLI.
- Explicit PR/task reconciliation with `orpheus task sync`.
- Merged-PR sync that closes Beads tasks.
- `run-ready` with a global concurrency limit and explicit confirmation.
- Human-editable global state files.
- A single global mutation-lock concept for state-changing operations.

### 3.2 MVP Excludes

- Automated planning.
- Automated task decomposition.
- Native task store replacing Beads.
- User-configurable task backends.
- Cross-repo project dependency scheduling.
- Review comment parsing and agent relaunch.
- Auto-merge.
- Complex file-scope conflict detection.
- Local validation/test enforcement before PR creation.
- Required test reporting.
- Full attachable terminal sessions.
- Long-running daemon.
- Web UI.
- Task creation wrapper over Beads.
- Repo-specific agent profile overrides.
- Configurable worktree base directory.
- Configurable PR base branch beyond the registered repo default branch.

---

## 4. Core MVP Loop

```text
Beads task
  ↓
Orpheus selects/claims task
  ↓
Orpheus creates or reconciles deterministic branch/worktree
  ↓
Orpheus launches configured named background agent
  ↓
Agent fetches task context with `orpheus agent context`
  ↓
Agent implements code only
  ↓
Agent signals completion with `orpheus agent done`
  ↓
Orpheus commits changes
  ↓
Run is marked PR-ready
  ↓
Orpheus attempts task sync
  ↓
Orpheus creates PR if missing
  ↓
Task is considered in review by presence of PR URL
  ↓
Human reviews/merges PR on GitHub
  ↓
`orpheus task sync --all` polls PR state
  ↓
Merged PR closes the Beads task
  ↓
Worktree can be cleaned up later
```

---

## 5. Architectural Principles

### 5.1 Daemonless-First, Daemon-Compatible

The MVP does **not** require a long-running daemon.

Instead:

- Each operation is exposed as an explicit CLI command.
- State-changing commands acquire a global mutation lock conceptually.
- Commands are idempotent/recoverable where possible.
- PR polling/reconciliation is an explicit batch command.
- A future daemon can call the same internal services on a schedule.

Example future daemon loop:

```text
every N minutes:
  run task sync --all service
```

The daemon must not be required for MVP correctness.

### 5.2 Beads Owns Task Lifecycle

Beads is the authoritative task lifecycle store in the MVP.

Beads owns:

- task id
- title
- description
- status
- priority
- dependencies/readiness
- native started/completed timestamps where available
- closed/done state

Orpheus owns:

- registered repositories
- named agent profiles
- branch/worktree mechanics
- run attempts
- process/log artifacts
- completion handshakes
- PR creation/polling mechanics
- global mutation lock
- status/action queue projection

Orpheus must not maintain a second authoritative task lifecycle table.

### 5.3 Backend-Neutral Agent Contract

Agents should not know that Beads is the task backend.

Agent-facing commands and context should speak in terms of:

- task
- repository
- branch
- worktree
- completion contract

They should not instruct the agent to run `bd`, edit Beads metadata, or understand Beads internals.

### 5.4 Existing Agents, Shell Command Profiles

The MVP does not implement first-class provider integrations for Claude Code, Codex, OpenCode, Pi, or others.

Instead, it supports globally configured named agent profiles. Each profile is a shell command.

Example:

```yaml
default_agent: junior

agents:
  junior:
    command: "pi --model cheap --prompt-file ~/.config/orpheus/prompts/implementation.md"

  architect:
    command: "pi --model smart --prompt-file ~/.config/orpheus/prompts/implementation.md"
```

The command receives context through environment variables and `orpheus agent context`.

---

## 6. State and Storage

### 6.1 Human-Editable Global State

The MVP uses human-editable files for Orpheus global state.

It should not use BadgerDB, SQLite, or another embedded database in the MVP.

Recommended layout:

```text
~/.config/orpheus/
  config.yaml

~/.local/share/orpheus/
  repos/
    <repo-id>/
      repo.yaml
      beads/              # only for Orpheus-managed Beads mode
      worktrees/
      runs/
        <task-id>/
          <run-id>.yaml
      logs/
        <task-id>/
          <run-id>.log
```

The exact file schema may evolve, but files should remain inspectable and manually repairable.

### 6.2 Global Config

Global config contains named agents and the default agent.

Example:

```yaml
default_agent: junior

agents:
  junior:
    command: "pi --model cheap --prompt-file ~/.config/orpheus/prompts/implementation.md"

  architect:
    command: "pi --model smart --prompt-file ~/.config/orpheus/prompts/implementation.md"
```

MVP agent profiles are global only.

There are no repo-specific agent profile overrides in the MVP.

### 6.3 Repo Registry

Each registered repo has a global repo record.

Example:

```yaml
id: myadmin-7f3a9c
name: myadmin
path: /home/mati/Documents/prog/myadmin
remote: git@github.com:org/myadmin.git
default_branch: main
beads_mode: local
beads_prefix: myadmin
```

The repo id must be collision-safe. The display name alone is not enough because different repos can share the same basename.

The MVP requires unique Beads prefixes across registered repos. Repos with duplicate Beads prefixes are unsupported in the MVP and should fail registration.

### 6.4 Beads Mode

At repo registration time, Orpheus detects Beads mode.

- If the repo already has a valid Beads setup, use `beads_mode: local`.
- Otherwise, create/use Orpheus-managed Beads under global data and use `beads_mode: managed`.

The MVP stores only the mode, not a custom Beads path.

Resolution is deterministic:

```text
if beads_mode == local:
  run bd commands in repo.path

if beads_mode == managed:
  run bd commands in ~/.local/share/orpheus/repos/<repo-id>/beads
```

Beads mode is detected at repo registration time and does not automatically change later.

### 6.5 Beads Metadata

Use native Beads idioms first. Store the least redundant metadata possible.

Recommended Orpheus task metadata in Beads:

```text
orpheus.branch
orpheus.worktree
orpheus.pr_url
```

Do not store a single `orpheus.run_id` on the task. A task can have many Orpheus runs.

Avoid redundant metadata such as both PR URL and PR number. PR URL is enough for MVP.

### 6.6 Run Records

An Orpheus run is one execution attempt for a task.

A task can have many runs:

```text
Task 1 ─── * OrpheusRun
```

Run records are internal Orpheus state and live in global files.

Example run record:

```yaml
id: run_20260512_101500_a8f2
repo_id: myadmin-7f3a9c
task_id: myadmin-123
attempt: 1
agent: junior
command_snapshot: "pi --model cheap --prompt-file ~/.config/orpheus/prompts/implementation.md"
status: done
pid: 12345
log_path: /home/mati/.local/share/orpheus/repos/myadmin-7f3a9c/logs/myadmin-123/run_20260512_101500_a8f2.log
started_at: 2026-05-12T10:15:00Z
ended_at: 2026-05-12T10:32:00Z
exit_code: 0
summary: Add password reset endpoint
details: Implemented endpoint, token generation, and expiry validation.
pr_ready: true
```

`pr_ready` belongs to the successful run attempt, not to the Beads task.

Recovery rule:

```text
latest successful run has pr_ready=true
AND task has no orpheus.pr_url
=> orpheus task sync <task-id> should attempt PR creation
```

---

## 7. Repository Registration

### 7.1 Interactive Registration

`orpheus repo add <path>` is an interactive registration wizard in the MVP.

It should detect values and ask the user to confirm them.

Suggested detected values:

- repository path
- git remote
- default branch
- Beads mode
- Beads prefix
- display name

Default branch detection should prefer `origin/HEAD`, then current branch as fallback. The user should be able to confirm or correct it during the wizard.

### 7.2 Registration Output

After `repo add`, the repo should be fully usable.

If Beads mode is managed, Orpheus initializes the managed Beads database during registration.

### 7.3 Beads Directory Escape Hatch

The MVP does not include `orpheus task create`.

Instead, provide:

```bash
orpheus repo beads-dir <repo-name-or-prefix>
```

For local Beads mode, this prints the repo path.

For managed Beads mode, this prints the Orpheus-managed Beads directory.

Users can manually create tasks with:

```bash
cd "$(orpheus repo beads-dir myadmin)"
bd create ...
```

---

## 8. Task Backend

### 8.1 Internal Interface

The codebase should include a narrow internal `TaskBackend` abstraction, even though Beads is the only MVP implementation.

The abstraction should be small and Beads-shaped.

Possible capabilities:

```text
get_task(task_id)
list_tasks()
list_ready()
claim_task(task_id)
set_metadata(task_id, key, value)
get_metadata(task_id, key)
close_task(task_id)
```

The goal is to avoid leaking `bd` commands through all of Orpheus and to keep agent-facing context backend-neutral.

### 8.2 MVP Implementation

The only MVP implementation is:

```text
BeadsTaskBackend
```

No user-configurable backend selection in MVP.

---

## 9. Commands

### 9.1 Agent-Facing Commands

Agent-facing commands are meant to be run inside an Orpheus-dispatched agent environment.

They infer context from environment variables and validate against the current directory/git state.

```bash
orpheus agent context
orpheus agent done --summary "Short summary" --details "Detailed summary"
```

### 9.2 Operator-Facing Task Commands

Use verb-first task command style.

```bash
orpheus task list
orpheus task ready
orpheus task run <task-id>
orpheus task run --agent <agent-name> <task-id>
orpheus task run-ready --limit <n>
orpheus task run-ready --limit <n> --agent <agent-name>
orpheus task run-ready --limit <n> --yes
orpheus task sync <task-id>
orpheus task sync --all
orpheus task logs <task-id>
```

### 9.3 Repo Commands

```bash
orpheus repo add <path>
orpheus repo list
orpheus repo beads-dir <repo-name-or-prefix>
```

### 9.4 Global Status

```bash
orpheus status
```

`status`, `task list`, and `task ready` operate across all registered repos by default.

---

## 10. Task ID Resolution

Beads task ids have prefixes.

During repo registration, Orpheus records each repo's Beads prefix.

Task commands resolve repo by parsing the task id prefix:

```bash
orpheus task run myadmin-123
```

resolves to the registered repo whose `beads_prefix` is `myadmin`.

The MVP assumes unique Beads prefixes. Duplicate-prefix repos are unsupported.

---

## 11. Global Mutation Lock

The MVP has a single global mutation-lock concept.

All state-changing commands must acquire it.

Examples:

- `repo add`
- `task run`
- `task run-ready`
- `agent done`
- `task sync`
- `task sync --all`

The exact lock implementation is an implementation detail and can be decided later.

Read-only commands such as `status`, `task list`, `task ready`, `task logs`, and `agent context` do not need to mutate state.

---

## 12. Worktree and Branch Model

### 12.1 Stable Execution Target

One task maps to one stable branch and one stable worktree.

```text
Task
  ├── orpheus.branch
  ├── orpheus.worktree
  ├── orpheus.pr_url
  └── OrpheusRun[]
```

Multiple execution attempts reuse the same branch/worktree by default.

### 12.2 Worktree Location

MVP worktrees always live under Orpheus global data.

Example:

```text
~/.local/share/orpheus/repos/<repo-id>/worktrees/<task-id>-<slug>
```

No configurable worktree base directory in MVP.

### 12.3 Branch Naming

Branch names should be deterministic and include the task id.

Example:

```text
task/<task-id>-<slug>
```

The exact slug algorithm is implementation detail, but the chosen branch should be stored in Beads metadata as `orpheus.branch`.

### 12.4 Branch Base

When creating a task branch for the first time, Orpheus should fetch the default branch from origin and create from the remote default branch.

Conceptual flow:

```bash
git fetch origin <default_branch>
git worktree add <worktree> -b <branch> origin/<default_branch>
```

This avoids mutating the user's local default branch.

If the task branch already exists but the worktree is missing, Orpheus should recreate the worktree from the existing branch.

### 12.5 Self-Healing Run Setup

`orpheus task run <task-id>` should reconcile setup before launching an agent.

If a task is runnable and has no active run, no PR-ready latest run, and no PR URL:

- claim/mark the task in progress through the task backend
- derive/store branch/worktree if absent
- verify whether the expected worktree exists
- recreate missing worktree if safe
- create missing branch from fetched default branch if safe
- launch a new run attempt

Dangerous inconsistent states should fail loudly with repair guidance rather than being auto-repaired.

Examples of dangerous states:

- expected worktree path exists but is not a git worktree
- expected worktree belongs to the wrong branch
- expected branch is checked out in another worktree in an incompatible way
- Beads metadata conflicts with actual git state

---

## 13. Agent Launching

### 13.1 Named Agent Profiles

Agents are globally configured named shell commands.

Task run examples:

```bash
orpheus task run myadmin-123
orpheus task run --agent junior myadmin-123
orpheus task run --agent architect myadmin-123
```

Resolution:

1. If `--agent` is provided, use that named profile.
2. Otherwise use `default_agent`.
3. If no matching agent is configured, fail with setup guidance.

The run record stores both the agent name and command snapshot.

### 13.2 Agent Environment

When launching an agent, Orpheus sets environment variables:

```bash
ORPHEUS_REPO_ID=<repo-id>
ORPHEUS_TASK_ID=<task-id>
ORPHEUS_WORKTREE=<absolute-worktree-path>
ORPHEUS_BRANCH=<branch-name>
```

A future implementation may also set `ORPHEUS_RUN_ID`, but it is not required by the MVP command contract.

The agent process runs with cwd set to `ORPHEUS_WORKTREE`.

### 13.3 Agent Responsibilities

The agent is responsible for implementation only.

The agent should not be responsible for:

- selecting tasks
- claiming tasks
- creating worktrees
- choosing branch names
- switching branches
- creating PRs
- editing task-tracker state directly
- deciding lifecycle transitions

---

## 14. Agent Context

`orpheus agent context` returns backend-neutral task context.

It should not mention Beads or instruct the agent to run `bd`.

Example shape:

```markdown
# Orpheus Agent Context

## Task
ID: myadmin-123
Title: Add password reset endpoint
Description: ...
Acceptance Criteria: ...
Notes: ...

## Execution Contract
- You are working in the correct worktree for this task.
- Work only on this task.
- Do not create or switch branches.
- Do not create a pull request.
- Do not modify task-tracker state directly.
- When implementation is complete, run:
  orpheus agent done --summary "<max 80 chars>" --details "<what changed>"

## Repository
Branch: task/myadmin-123-add-password-reset
Base branch: main
Worktree: /home/mati/.local/share/orpheus/repos/myadmin-7f3a9c/worktrees/myadmin-123-add-password-reset
```

The context should include the task description, acceptance criteria, notes, and useful repository/worktree constraints.

It should not include broad planning/project context in the MVP.

---

## 15. Completion Handshake

### 15.1 Command

Agents signal completion with:

```bash
orpheus agent done --summary "Short summary" --details "Detailed summary"
```

`--summary` is required and should be short enough for one-line status and commit messages, approximately 80 characters max.

`--details` is used for run inspection and PR body generation.

The MVP does not require `--tests` and does not require local validation reporting.

### 15.2 Validation

`agent done` must validate:

- `ORPHEUS_TASK_ID` is present or explicit context is available
- current working directory is inside `ORPHEUS_WORKTREE`
- `ORPHEUS_WORKTREE` matches the task's stored `orpheus.worktree`
- current git branch matches `orpheus.branch`
- task is not closed
- task does not already have `orpheus.pr_url`

Environment variables are context hints, not ultimate authority. Orpheus should verify against the repo registry, task backend metadata, and git state.

### 15.3 Orpheus-Owned Commit

Orpheus creates the commit during `agent done`.

MVP behavior:

1. Check git status.
2. Stage changes with normal Git ignore behavior.
3. Create a standardized commit.
4. Store run result with `pr_ready: true`.
5. Attempt task sync, which may create the PR.

The agent should not be required to commit changes.

Example commit message:

```text
myadmin-123: Add password reset endpoint

Implemented endpoint, token generation, and expiry validation.

Task: Add password reset endpoint
Orpheus-Task: myadmin-123
```

Commit message customization is future work.

### 15.4 No Local Test Enforcement

The MVP does not run tests automatically and does not require test reporting.

Quality boundary:

```text
Agent discretion + repository instructions + CI + human PR review
```

---

## 16. PR Integration

### 16.1 MVP PR Provider

The MVP uses the `gh` CLI for GitHub PR creation and polling.

Assumptions:

- GitHub only.
- `gh` is installed.
- User is authenticated with `gh`.
- Repository remote works with `gh`.

The code should still isolate this behind a small internal PR provider interface.

Conceptual interface:

```text
PrProvider
- create_pr(repo_path, branch, base, title, body) -> pr_url
- get_pr(pr_url) -> PrState
```

MVP implementation:

```text
GhCliPrProvider
```

A future SDK/API implementation can replace it later.

### 16.2 PR Base Branch

MVP PRs always target the registered repo `default_branch`.

No per-task PR base branch in MVP.

### 16.3 PR Creation

PR creation happens during `task sync` when:

```text
latest successful run has pr_ready=true
AND task has no orpheus.pr_url
```

Conceptual flow:

```bash
git push -u origin <branch>
gh pr create --base <default_branch> --head <branch> --title ... --body-file ...
```

When PR creation succeeds, store the PR URL in Beads metadata:

```text
orpheus.pr_url
```

Presence of `orpheus.pr_url` on a non-closed task means the task is in review.

### 16.4 PR Body

The PR body should include:

- task id
- task title
- task description or acceptance criteria if available
- agent short summary
- agent detailed summary
- note that PR was created by Orpheus

Tests are not required in MVP PR body unless naturally included in details.

---

## 17. Task Sync

### 17.1 Single Task Sync

```bash
orpheus task sync <task-id>
```

Reconciles one task against run records and GitHub PR state.

MVP behavior:

1. If latest successful run has `pr_ready=true` and task has no `orpheus.pr_url`, create PR.
2. If task has `orpheus.pr_url`, query GitHub.
3. If PR is merged, close the task in Beads.
4. If PR is open, report it as in review.
5. If PR is missing, closed unmerged, or inaccessible, report clearly.

No review-comment handling in MVP.

### 17.2 Sync All

```bash
orpheus task sync --all
```

Runs task sync logic across all registered repos.

It should handle both:

- tasks that are implementation-complete and need PR creation
- tasks that already have PR URLs and need PR polling

It should report skipped/errors grouped by reason.

This command is the MVP replacement for daemon PR polling.

---

## 18. Status and Action Queue

`orpheus status` is the main user-facing view.

It should be local and fast. It should not query GitHub or perform network calls in the MVP.

Network reconciliation is explicit through:

```bash
orpheus task sync --all
```

`status` should project from:

- repo registry
- task backend state
- Beads metadata
- Orpheus run records
- cheap local process state if available

Recommended groups:

```text
Ready to run
Running
Failed / needs retry
Implementation complete / needs PR
In review
Blocked
Done count / recently done
```

Examples of projection rules:

```text
Beads ready + no active run + no pr_url
=> Ready to run

latest run active
=> Running

latest run failed + no pr_url
=> Failed / needs retry

latest successful run pr_ready=true + no pr_url
=> Implementation complete / needs PR

task has orpheus.pr_url + task not closed
=> In review

task closed
=> Done
```

---

## 19. Running Tasks

### 19.1 Single Task Run

```bash
orpheus task run <task-id>
orpheus task run --agent <agent-name> <task-id>
```

Behavior:

1. Acquire global mutation lock.
2. Resolve task id to repo by Beads prefix.
3. Ensure task exists.
4. Ensure task is not closed.
5. Ensure task has no `orpheus.pr_url`.
6. Ensure latest successful run is not already PR-ready.
7. Ensure no active run exists.
8. Claim/mark task in progress via task backend.
9. Reconcile branch/worktree.
10. Create new run attempt.
11. Launch selected named agent with `ORPHEUS_*` environment.
12. Store run/process/log information.

### 19.2 Retry Behavior

The MVP supports multiple execution attempts per task.

If a prior run failed and the task has no PR URL and no PR-ready latest run, `task run` may launch a new attempt.

Retries reuse the same branch/worktree by default.

Older logs and run records remain available.

### 19.3 Run Ready

```bash
orpheus task run-ready --limit <n>
orpheus task run-ready --limit <n> --agent <agent-name>
orpheus task run-ready --limit <n> --yes
```

Behavior:

1. Acquire global mutation lock.
2. Query ready tasks across registered repos.
3. Filter tasks that already have PRs, active runs, or PR-ready latest runs.
4. Select up to `N`.
5. Print exact tasks that will run.
6. Ask for confirmation by default.
7. Skip confirmation only with `--yes`.
8. Claim/setup/launch each selected task.

This command must not continuously auto-run tasks without operator confirmation in the MVP.

---

## 20. Logs

Each run should have a log file under global Orpheus data.

Example:

```text
~/.local/share/orpheus/repos/<repo-id>/logs/<task-id>/<run-id>.log
```

`orpheus task logs <task-id>` should show or locate logs for the task's run attempts.

The exact UX can be simple in MVP.

---

## 21. Cleanup

Cleanup is not central to the first implementation slice.

Eventually, after a PR is merged and the Beads task is closed, Orpheus should be able to clean up:

- worktree
- local task branch, when safe
- old logs/run records according to retention config

Cleanup should be explicit or configurable, not destructive by surprise.

---

## 22. Recommended Go Architecture

The MVP should be implemented in Go.

Suggested package layout:

```text
cmd/orpheus/
  main.go

internal/
  app/              # command orchestration/use cases
  config/           # global config + repo registry
  repo/             # registered repo model/resolution
  task/             # backend-neutral task model/interface
  beads/            # BeadsTaskBackend using bd CLI
  git/              # git/worktree operations
  agent/            # named profiles + process launch
  runs/             # run attempt store
  pr/               # PR provider interface + gh CLI implementation
  status/           # action queue projection
  lock/             # global mutation lock abstraction
```

A command framework such as Cobra is appropriate because Orpheus has nested commands.

---

## 23. Recommended Implementation Slices

### Slice 1 — Repo Registry and Task Visibility

```text
orpheus repo add <path>
orpheus repo list
orpheus repo beads-dir <repo>
orpheus task list
orpheus task ready
orpheus status
```

This validates:

- global state layout
- repo registration
- Beads mode detection
- Beads prefix resolution
- cross-repo task listing

### Slice 2 — Task Run and Agent Launch

```text
orpheus task run <task-id>
orpheus task run-ready --limit N
```

This validates:

- global mutation lock concept
- task claiming
- deterministic branch/worktree creation
- named agent profile resolution
- process spawning
- run records/logs
- `ORPHEUS_*` environment

### Slice 3 — Agent Context and Completion

```text
orpheus agent context
orpheus agent done --summary ... --details ...
```

This validates:

- backend-neutral task context
- environment/cwd/git validation
- Orpheus-owned commit
- run completion metadata
- PR-ready recovery flag

### Slice 4 — PR Creation and Sync

```text
orpheus task sync <task-id>
orpheus task sync --all
```

This validates:

- `gh` CLI integration
- PR creation
- PR URL metadata
- PR polling
- merged PR detection
- Beads task closure

---

## 24. Future Expansion Path

Once the hardened Beads workflow is reliable, the product can expand toward the broader brief:

1. Repo epics / parent-child Beads workflows.
2. Workspace/global cross-repo epics.
3. Automated planning and task decomposition.
4. Multiple first-class provider integrations.
5. Review-comment sync and relaunch.
6. Rich dependency scheduling.
7. Conflict detection.
8. Configurable validation/test gates.
9. Configurable commit message templates.
10. Configurable worktree locations.
11. Web dashboard.
12. Long-running daemon using the same internal services.
13. Native task backend or additional task backend implementations.
