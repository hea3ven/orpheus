# Agent Orchestration CLI — Product & Architecture Brief

## 1. Vision / Pitch

This project is a CLI-first orchestration layer for software development with AI coding agents.

Its core purpose is to bring **structure, oversight, and human control** to agent-driven development.

The product should stand out from other agent coding tools by not trying to replace the developer, remove review, or turn the whole workflow into an opaque autonomous pipeline. Instead, it should put the **operator** — the human developer, tech lead, or reviewer — firmly in control.

The central pitch:

> A structured orchestration CLI for coordinating existing coding agents across plans, tasks, projects, repositories, worktrees, PRs, reviews, and human decisions — while keeping the operator in control.

The tool should make agentic development feel less like “letting an agent run loose” and more like operating a well-organized development system.

It should help answer questions such as:

- What are we trying to build?
- Is this a single task or a multi-PR project?
- Which tasks are ready?
- Which tasks are blocked?
- Which repositories are involved?
- Which agent is working on what?
- What PRs are awaiting review?
- What review comments need follow-up?
- What human decisions are required?
- What work can safely run in parallel?
- What should not run yet?

The goal is not maximum autonomy. The goal is **controlled autonomy**.

---

## 2. Positioning

Most AI coding workflows tend to fall into one of two extremes:

1. **Manual single-agent coding**
   - The user talks to one agent at a time.
   - The user manually tracks branches, tasks, PRs, and context.
   - Parallel work is hard to coordinate.

2. **Highly autonomous coding pipelines**
   - Agents are given broad goals and run with limited structure.
   - Work may become difficult to inspect or control.
   - The human may lose visibility over why tasks are happening, which dependencies exist, or what should be reviewed first.

This idea sits in the middle.

It is an **operator-controlled orchestration system**.

The orchestrator should:

- Use existing coding agents instead of creating a new custom agent runtime.
- Add structure around planning, task decomposition, project decomposition, dependency management, worktree isolation, PR creation, review, and merge detection.
- Keep the human in charge of important decisions.
- Support both single-repository and multi-repository development.
- Coordinate background agents without requiring the user to babysit every process.
- Let the user attach to active agents when direct guidance is needed.

The differentiator is not “the smartest coding agent.”

The differentiator is:

> The safest, clearest, most operator-controlled way to run many coding agents across real development workflows.

---

## 3. Executive Summary

This idea is for a structured agent orchestration CLI that coordinates existing coding agents such as Claude Code, Codex, OpenCode, or similar tools, without requiring the orchestrator itself to become a custom agent framework.

The core thesis is:

> Use existing agent harnesses for actual implementation work, but add a structured orchestration layer around planning, task decomposition, dependency management, repository coordination, PR lifecycle, review handling, and task completion.

The system should help a human developer move from a high-level idea to:

1. A plan
2. A complexity assessment
3. Either a single executable task or a larger project
4. A dependency-aware task graph
5. Parallel or sequential agent execution
6. Isolated branches/worktrees
7. PR creation
8. Human review
9. Review-response cycles
10. Merge detection
11. Task/project completion

Unlike a fully autonomous “pipe coding” workflow, this system is meant to preserve structure, checkpoints, reviewability, and human control.

---

## 4. Motivation

Existing coding agents are powerful, but running multiple agents manually can become chaotic.

The user has tested Gas Town and likes some of its ideas, especially:

- Coordinating existing agent tools
- Not building a custom agent runtime from scratch
- Giving agents a small startup prompt that tells them to run a CLI command to fetch richer context

However, the desired system should avoid going fully into unstructured autonomous coding. Instead, it should provide a workflow that is more deliberate, staged, inspectable, and compatible with human review.

The main pain points this tool should address are:

### 4.1 Unstructured Agent Work

Agents can jump directly into coding without enough planning.

This creates problems:

- Work may not be decomposed cleanly.
- Scope may grow unexpectedly.
- Dependencies may be unclear.
- The human may not know whether the work should be one PR or several PRs.

### 4.2 Manual Multi-Agent Coordination

Running several coding agents manually is possible, but difficult.

The user has to track:

- Which agent is working on which task
- Which branch each agent uses
- Which PR belongs to which task
- Which tasks are blocked
- Which tasks are safe to run in parallel
- Which agents need review feedback
- Which repositories are involved

The orchestrator should make this explicit.

### 4.3 Loss of Context Between Agent Runs

Agents may start without understanding the project workflow.

A restarting agent may not know:

- Current task state
- Relevant dependencies
- Project conventions
- Previous review comments
- The correct branch or worktree
- Whether another agent has changed related code

The system needs a way to provide fresh context on demand.

### 4.4 Expensive Idle Agent Sessions

Keeping an agent running while a PR waits for human review wastes tokens and compute.

The orchestrator should be responsible for watching PR state and reactivating agents only when needed.

When a PR is in review:

- The agent can stop.
- The task state becomes `in_review`.
- The daemon or orchestrator watches PR state.
- If comments arrive, the task can be reactivated.
- If the PR is merged, the task can be marked done.

### 4.5 Need for Human-Controlled PR Flow

The human should remain responsible for review and merge decisions.

Agents should:

- Implement tasks
- Create PRs
- Address review comments
- Report status

Humans should:

- Approve plans
- Approve generated task/project structure
- Review PRs
- Request changes
- Merge PRs
- Make architectural decisions

---

## 5. Product Vision

The tool is an **agent orchestration CLI** for software development workflows.

It acts as a structured coordinator between:

- The human developer/operator
- Existing coding agents
- A task system
- A project system
- Git branches and worktrees
- Pull requests
- Review comments
- Multiple repositories
- Project-specific context
- A daemon coordination layer

The CLI does not need to be the coding agent. Instead, it should:

1. Help generate a plan.
2. Classify the plan as a single task or a larger project.
3. Convert plans into tasks or projects.
4. Track dependencies, including cross-repository dependencies.
5. Assign ready tasks to agents.
6. Launch the appropriate external agent harness.
7. Provide each agent with a standardized task prompt and a way to fetch more context.
8. Ensure each task produces a branch and PR in the correct repository.
9. Pause execution while the PR is under human review.
10. Resume the right agent or launch a new agent when review comments need to be addressed.
11. Mark tasks as done when PRs are merged.
12. Unblock dependent tasks as the graph progresses.
13. Coordinate state through a daemon so multiple CLI invocations, agents, and processes share the same source of truth.

---

## 6. Design Principles

### 6.1 Put the Operator in Control

The human operator should always be able to understand and control the system.

The system should make visible:

- What is planned
- What is running
- What is blocked
- What is waiting for review
- What depends on what
- Which repositories are affected
- Which agents are active
- Which background agents are running
- Which PRs need attention
- Which human decisions are required

The tool should not hide development activity behind a vague “agent is working” state.

### 6.2 Bring Structure to Agentic Development

The orchestrator should make agent-driven development follow a structured workflow:

```text
Request
  ↓
Plan
  ↓
Complexity analysis
  ↓
Task or project creation
  ↓
Dependency graph
  ↓
Agent execution
  ↓
PR
  ↓
Human review
  ↓
Review response or merge
  ↓
Completion / unblocking
```

The structure is the product.

### 6.3 Use Existing Agents, Do Not Build a New Agent Runtime

The orchestrator should not require implementing a new agent using an SDK, custom tools, custom memory, or a large system prompt.

Instead, it should integrate with existing coding agents such as:

- Claude Code
- Codex
- OpenCode
- Other CLI-based coding agents

Each external agent should be treated as a provider behind a common interface.

The orchestration system controls **what work should be done and when**. The agent provider controls **how that agent process is launched and instructed**.

### 6.4 Common Agent Abstraction

The orchestrator should define a generic agent abstraction that is independent from any specific implementation.

At minimum, the abstraction should support:

- Agent provider type
- Initial prompt
- Working directory
- Target task
- Context command or instructions
- Expected output behavior
- Branch/PR expectations
- Resume or follow-up behavior

Example conceptual abstraction:

```text
AgentRun
- provider: codex | claude-code | opencode | custom
- task_id: TASK-123
- workspace_path: /path/to/worktree
- initial_prompt: string
- context_command: orchestrator context TASK-123
- branch_name: task/TASK-123-short-name
- expected_result: code changes + PR
```

The provider-specific implementation would translate this into the right command-line invocation for that agent.

### 6.5 Agents Should Fetch Context Through the CLI

A strong idea from Gas Town is that newly launched agents receive a small instruction telling them to run a CLI command to understand their environment and task.

This should be a core part of this design.

Rather than injecting a massive prompt directly into every agent, the orchestrator can give the agent something like:

```text
You are working on task TASK-123.
Before doing anything else, run:

  orchestrator agent-context TASK-123

Follow the instructions returned by that command.
```

The command can return:

- Task description
- Project description
- Current status
- Dependencies
- Relevant files
- Repository information
- Project conventions
- Branch naming rules
- PR expectations
- Review history
- Acceptance criteria
- Commands to run
- Current constraints
- Instructions for reporting progress
- Relevant messages from the daemon messaging layer

This makes the agent startup lightweight and lets the orchestrator remain the source of truth.

### 6.6 Structured Workflow Over Fully Autonomous Coding

The system should not jump directly from a user request to implementation.

Instead, it should enforce a staged workflow:

1. Planning
2. Complexity and scope analysis
3. Task or project creation
4. Dependency graph creation
5. Task execution
6. Pull request creation
7. Human review
8. Review response
9. Merge detection
10. Task completion and unblocking
11. Project progress update

This keeps the system understandable and allows the human to intervene at important checkpoints.

---

## 7. Core Workflow

The workflow should begin with a plan, but the plan should not always be converted directly into many tasks.

After planning, the orchestrator should perform a **complexity and scope analysis** to decide the right unit of execution.

The system should distinguish between:

- **Task**: A single focused unit of work that can usually be completed in one branch and one PR.
- **Project**: A larger body of work composed of multiple tasks, usually requiring multiple PRs, sequencing, dependencies, and iterative review.

This creates an important decision point:

```text
Human request
  ↓
Plan
  ↓
Complexity / scope analysis
  ↓
Single-task workflow OR project workflow
```

A small change might become one task and one PR.

A larger feature might become a project containing multiple tasks, each producing its own PR and advancing the overall project incrementally.

---

## 8. Planning Stage

The first interaction is between the human and a planning agent.

Goal:

> Convert a high-level feature or change request into a coherent implementation plan.

Inputs:

- Human request
- Repository context
- Current project architecture
- Known constraints
- Relevant documents or conventions
- Possible cross-repository implications

Outputs:

- Problem summary
- Goals
- Non-goals
- Proposed implementation strategy
- Risk areas
- Suggested phases
- Questions or assumptions
- Initial complexity estimate
- Recommendation: single task or project

The planning agent should not necessarily make code changes. Its primary job is to produce a clear plan that can be inspected and refined.

Example planning output:

```text
Plan: Add user notification preferences

Goals:
- Add backend persistence for notification preferences.
- Expose API endpoints to read/update preferences.
- Add frontend settings UI.
- Add tests.

Non-goals:
- Do not implement push notification delivery.
- Do not change existing email sending logic.

Risks:
- Existing user settings model may need migration.
- Frontend state management conventions should be checked.
```

---

## 9. Complexity and Scope Analysis Stage

After a plan is created, the system should analyze its implementation complexity before deciding how to execute it.

Goal:

> Decide whether the plan should become a single task or a larger project made of multiple tasks.

The analysis should consider:

- Number of files or modules likely affected
- Whether the change crosses frontend/backend boundaries
- Whether the change crosses repository boundaries
- Whether database/schema changes are involved
- Whether APIs or contracts need to be introduced or changed
- Whether work can be merged incrementally
- Whether multiple PRs would reduce risk
- Whether tasks can run in parallel
- Whether the human should review architecture before implementation

Possible outcomes:

### Outcome A: Single Task

The plan is simple enough to become one task and one PR.

Examples:

- Small bug fix
- Localized refactor
- Simple UI adjustment
- Adding one isolated endpoint
- Updating documentation

### Outcome B: Project

The plan is complex enough to become a project containing multiple tasks and PRs.

Examples:

- Full feature implementation
- Frontend and backend changes
- Database migration plus API plus UI
- Multi-stage refactor
- Cross-repository API producer/consumer change
- Any change where independent PRs make review safer

Conceptually:

```text
Plan
  ├── Single Task
  │     └── One PR
  │
  └── Project
        ├── Task 1 → PR 1
        ├── Task 2 → PR 2
        ├── Task 3 → PR 3
        └── Task N → PR N
```

---

## 10. Project Creation Stage

When a plan is classified as a project, the orchestrator should create a project entity that groups related tasks together.

A project should include:

- Project title
- Original human request
- Approved plan
- Goals
- Non-goals
- Implementation strategy
- Repositories involved
- Task list
- Task dependency graph
- Cross-repository dependency graph when applicable
- Overall progress
- Related PRs
- Current blockers
- Human decisions needed

The project becomes the parent container for the work. Tasks are the executable children.

Example:

```text
PROJECT-001: Add notification preferences

Tasks:
- TASK-001: Add database model
- TASK-002: Add backend API endpoints
- TASK-003: Add frontend settings UI
- TASK-004: Add tests
- TASK-005: Update documentation
```

---

## 11. Task Decomposition Stage

For project-sized work, a second agent or workflow step takes the plan and breaks it into executable tasks.

For single-task work, this stage can simply produce one well-defined task.

Goal:

> Convert a plan into either one executable task or a project task graph that can be executed safely, possibly in parallel.

Each task should include:

- Title
- Description
- Acceptance criteria
- Scope boundaries
- Repository
- Expected files or modules touched
- Dependencies
- Cross-repository dependencies if relevant
- Suggested agent type/provider
- Estimated size
- Risk level
- Test expectations
- PR expectations

Tasks should be small enough for one agent to complete in a single focused run.

Example task output:

```text
TASK-001: Add notification preferences database model

Description:
Create backend persistence for per-user notification preferences.

Acceptance criteria:
- Preferences are stored per user.
- Migration is added.
- Existing user model behavior is not broken.
- Unit tests cover default preference creation.

Repository:
- backend-api

Dependencies:
- None

Blocks:
- TASK-002
- TASK-004

Risk:
- Medium

Estimated size:
- Medium
```

---

## 12. Dependency Graph Stage

The orchestrator should maintain dependencies between tasks.

This is important because some tasks can run in parallel, while others must wait until foundational changes are merged.

Example:

```text
TASK-001: Backend data model
  ├── TASK-002: Backend API endpoints
  └── TASK-004: Tests for preference defaults

TASK-002: Backend API endpoints
  └── TASK-003: Frontend settings UI
```

The system should only dispatch tasks whose dependencies are complete.

A task is considered ready when:

- All dependencies are merged or marked complete.
- No conflicting task is actively modifying the same area.
- The task has enough detail to execute.
- The repository is in a valid state.
- The target worktree and branch can be prepared safely.

---

## 13. Multi-Repository Coordination

The orchestrator should be aware of multiple repositories, not just one local codebase.

This is especially valuable in microservice or multi-application environments where a single product change may require coordinated work across several repositories.

Example:

```text
PROJECT-001: Add billing plan limits

Repositories:
- billing-service
- account-api
- web-dashboard
- docs

Tasks:
- TASK-001: Add plan limit model in billing-service
- TASK-002: Expose plan limits through account-api
- TASK-003: Consume plan limits in web-dashboard
- TASK-004: Update public documentation
```

Cross-repository dependencies should be explicit:

```text
TASK-001 billing-service API contract
  ↓
TASK-002 account-api integration
  ↓
TASK-003 web-dashboard UI usage
```

This lets the orchestrator sequence work safely.

It can ensure the API-producing repository is changed, reviewed, and merged before launching the task that consumes that API in another repository.

A project should therefore be able to contain tasks from multiple repositories, and each task should carry repository-specific execution information:

```text
Task
- project_id
- repository
- base_branch
- task_branch
- worktree_path
- dependencies
- dependent_tasks
- related_pr
```

---

## 14. Agent Execution Stage

When a task is ready, the orchestrator assigns it to an agent provider.

The orchestrator should:

1. Create or select a workspace/worktree.
2. Create a branch for the task.
3. Launch the selected agent provider.
4. Give the agent a minimal startup prompt.
5. Instruct the agent to fetch task context from the CLI.
6. Let the agent perform the implementation.
7. Require the agent to create or prepare a PR.

The agent should work within clearly defined boundaries.

The agent should know:

- What task it owns
- What project the task belongs to
- What repository it is working in
- What files or modules are likely relevant
- What it should avoid touching
- Which tasks it depends on
- Which tasks depend on it
- How to run tests
- How to summarize its changes
- How to create the PR
- How to communicate blockers

---

## 15. Pull Request Stage

Every implementation task should produce a pull request.

The PR becomes the checkpoint between autonomous agent work and human control.

The PR should include:

- Task ID
- Project ID if applicable
- Summary of changes
- Acceptance criteria checklist
- Tests run
- Known limitations
- Links to dependent tasks
- Any important implementation notes

Example PR body:

```markdown
## Task
TASK-001: Add notification preferences database model

## Project
PROJECT-001: Add notification preferences

## Summary
- Added notification_preferences table.
- Added default preference creation for new users.
- Added tests for default preference values.

## Acceptance Criteria
- [x] Preferences are stored per user.
- [x] Migration is added.
- [x] Existing user model behavior is not broken.
- [x] Unit tests cover default preference creation.

## Tests
- go test ./...

## Notes
This PR does not add API endpoints. That is covered by TASK-002.
```

---

## 16. Review Waiting Stage

Once the PR is created, the agent should stop.

This is an important product decision.

The system should not keep the agent process alive while the PR is waiting for review, because that wastes tokens and compute.

Instead:

- The orchestrator tracks the PR.
- The task status becomes `in_review`.
- The agent process exits.
- The human reviews the PR normally.
- The orchestrator watches for PR state changes.

This reinforces the operator-control model.

The agent does not own the workflow. The orchestrator and human do.

---

## 17. Review Response Stage

If the human leaves comments on the PR, the orchestrator should reactivate work on that task.

This can happen by:

1. Launching a new agent instance for the same task.
2. Providing it with the current task context.
3. Including PR comments and review feedback.
4. Asking it to address only the requested changes.
5. Pushing updates to the same branch.
6. Returning the PR to review.

The system should not assume the same agent session still exists.

This reinforces the idea that agents are disposable workers and the orchestrator/task system is the persistent source of truth.

---

## 18. Merge Detection and Task Completion

When the PR is merged, the orchestrator should:

1. Mark the task as `done`.
2. Record the merged commit or PR reference.
3. Update the dependency graph.
4. Unblock dependent tasks.
5. Dispatch newly ready tasks if the workflow is active.
6. Update parent project progress if applicable.

This allows the system to progress through the task graph without requiring the human to manually update every task.

---

## 19. Proposed Work Item Model

The system should model both projects and tasks.

A **project** represents a larger goal that may require several PRs.

A **task** represents an executable unit that an agent can work on directly.

```text
Project
  ├── Task
  ├── Task
  └── Task
```

For simple work, the project layer may be optional:

```text
Task
  └── PR
```

For complex work, the project layer becomes important:

```text
Project
  ├── Task → PR
  ├── Task → PR
  └── Task → PR
```

This gives the orchestrator flexibility. It can avoid unnecessary overhead for small work while still supporting structured, iterative delivery for larger work.

---

## 20. Proposed Task Lifecycle

A task could move through the following statuses:

| Status | Meaning |
|---|---|
| `draft` | Task exists but is not ready for execution. |
| `blocked` | Task is waiting on dependencies. |
| `ready` | Task can be picked up by an agent. |
| `assigned` | Task has been assigned to an agent run. |
| `in_progress` | Agent is actively working on it. |
| `pr_open` | Agent created a PR. |
| `in_review` | PR is waiting for human review. |
| `changes_requested` | Review comments require follow-up work. |
| `updating` | Agent is addressing review comments. |
| `approved` | PR is approved but not merged yet. |
| `merged` | PR has been merged. |
| `done` | Task is complete and dependencies can be unblocked. |
| `failed` | Agent could not complete the task. |
| `cancelled` | Task was intentionally stopped. |

A simplified lifecycle:

```text
draft
  ↓
blocked ── dependencies complete ──→ ready
  ↓                                ↓
assigned → in_progress → pr_open → in_review
                                      ↓
                         changes_requested → updating → in_review
                                      ↓
                                  approved
                                      ↓
                                   merged
                                      ↓
                                    done
```

---

## 21. Project Lifecycle

A project could move through the following statuses:

| Status | Meaning |
|---|---|
| `draft` | Project idea exists but has not been planned. |
| `planning` | A plan is being created. |
| `awaiting_approval` | Human needs to approve or edit the plan/task graph. |
| `ready` | Project has approved tasks and can begin execution. |
| `active` | At least one task is running or in review. |
| `blocked` | Project is blocked by dependency, failure, or human decision. |
| `completed` | All required tasks are done. |
| `cancelled` | Project was intentionally stopped. |

A project’s status should mostly derive from its child tasks, but the human should be able to override or pause it.

---

## 22. Main System Components

### 22.1 CLI

The CLI is the main user interface.

Possible commands:

```bash
orchestrator init
orchestrator plan "Build notification preferences"
orchestrator projects list
orchestrator projects status PROJECT_ID
orchestrator tasks create-from-plan PLAN_ID
orchestrator tasks list
orchestrator tasks graph
orchestrator run TASK_ID
orchestrator run-ready
orchestrator agent-context TASK_ID
orchestrator agent attach TASK_ID
orchestrator messages list --project PROJECT_ID
orchestrator pr sync
orchestrator status
```

The CLI should support both human-facing and agent-facing commands.

Human-facing examples:

```bash
orchestrator status
orchestrator tasks graph
orchestrator run-ready
orchestrator agent attach TASK-123
```

Agent-facing examples:

```bash
orchestrator agent-context TASK-123
orchestrator task update TASK-123 --status in_progress
orchestrator task note TASK-123 "Found existing API pattern in users module"
orchestrator agent inbox TASK-123
```

---

## 23. Daemon Architecture

The CLI can be backed by a daemon process that acts as the coordination layer between human CLI commands, active agents, background agents, task state, worktrees, and PR synchronization.

When the user runs the CLI, it should check whether the daemon is running. If it is not running, the CLI can start it automatically.

```text
User CLI command
  ↓
Check daemon status
  ↓
Start daemon if needed
  ↓
Send command/request to daemon
  ↓
Daemon updates state, schedules agents, or returns status
```

The daemon can own long-lived coordination concerns:

- Task and project state
- Agent run state
- Worktree registry
- PR polling or webhook handling
- Message routing
- Background agent supervision
- Recovery after crashes
- Cross-process locking
- Event log

The CLI remains the user-facing control surface, while the daemon becomes the source of runtime coordination.

The daemon is especially useful because multiple processes may exist at once:

- Human CLI sessions
- Background agent processes
- Active attachable agent sessions
- PR synchronization loops
- Review-comment handlers
- Message routing processes

Without a daemon, these processes would need to coordinate through files and locks alone. With a daemon, there can be one runtime authority for coordination.

---

## 24. Orchestrator Core

The orchestrator core owns the workflow logic.

Responsibilities:

- Task state machine
- Project state machine
- Dependency graph
- Cross-repository dependency graph
- Agent scheduling
- Provider abstraction
- Workspace/worktree management
- PR status synchronization
- Review comment detection
- Event handling
- Persistence

The core should be provider-agnostic.

It should know that a task needs to be run by an agent, but it should not be tightly coupled to Codex, Claude Code, OpenCode, or any specific tool.

---

## 25. Agent Provider Interface

Each agent tool should be implemented as a provider.

Provider responsibilities:

- Launch the external agent process
- Format the initial prompt appropriately
- Pass working directory and environment variables
- Support task execution
- Support review-response execution
- Capture logs or transcripts if available
- Return success/failure status to the orchestrator

Conceptual interface:

```text
AgentProvider
- name
- supports_interactive_mode
- supports_non_interactive_mode
- launch(task_context)
- resume(task_context)
- stop(run_id)
```

Provider examples:

```text
CodexProvider
ClaudeCodeProvider
OpenCodeProvider
CustomShellProvider
```

---

## 26. Active Agents and Background Agents

The system should support two broad agent modes.

### 26.1 Active Agents

Active agents are sessions that the human can attach to and interact with directly.

They are useful when:

- The task is ambiguous
- The human wants to guide the implementation
- The agent needs frequent decisions
- The user wants an interactive coding session

Conceptually:

```bash
orchestrator agent start TASK-123 --mode active
orchestrator agent attach TASK-123
```

### 26.2 Background Agents

Background agents are launched and supervised by the orchestrator.

They are useful when:

- The task is well defined
- Acceptance criteria are clear
- The agent can work independently
- The result should be a PR

Conceptually:

```bash
orchestrator run TASK-123 --mode background
```

The important distinction is that both modes use the same task, project, worktree, and PR lifecycle. The difference is how much direct human interaction is expected while the agent is running.

---

## 27. Daemon Messaging Layer

The daemon could provide a messaging layer for coordination between:

- Background agents
- Active agents
- The human
- The orchestrator scheduler
- Review/comment handlers

This messaging layer gives disposable agent processes a way to communicate without becoming the persistent source of truth.

Possible message types:

```text
agent → human: I found an ambiguity and need a decision.
agent → agent: TASK-123 changed the API contract; see updated notes.
human → agent: Prefer option B for this implementation.
orchestrator → agent: Review comments are available; address them now.
agent → orchestrator: Task is complete and PR is ready.
```

The messaging layer could support:

- Direct messages to a specific agent run
- Messages attached to a task
- Messages attached to a project
- Broadcasts to all agents in a project
- Human decision requests
- Agent status updates
- Review feedback delivery

Example conceptual commands:

```bash
orchestrator messages list --project PROJECT-001
orchestrator messages send --task TASK-123 "Use the new API shape from TASK-001"
orchestrator agent inbox TASK-123
```

For background agents, the startup context command can include any unread messages relevant to the task. This avoids requiring the agent to remain alive just to receive updates.

---

## 28. Task Store

The system needs persistent task storage.

Options:

### Option A: Use Beads

Pros:

- CLI-friendly
- Designed for agents and local repositories
- Git-backed issue/task tracking
- Works well with agent workflows

Cons:

- Ties the system to Beads conventions
- May need abstraction if supporting other ticketing systems later

### Option B: Build a Simple Native Task Store

Pros:

- Full control
- Easier to design exactly around this workflow
- Can be optimized for dependency graphs and PR state

Cons:

- More work
- Reinvents task management
- May become a maintenance burden

### Option C: Abstract Over Task Backends

Pros:

- Can support Beads initially
- Later support GitHub Issues, Jira, Linear, or local files
- Keeps architecture flexible

Cons:

- More abstraction early
- Risk of overengineering

Recommended direction:

> Start with a narrow task abstraction, then implement Beads or a simple local store as the first backend.

The interface should be designed so that other task systems can be added later.

---

## 29. Repository Registry

For multi-repository coordination, the orchestrator needs to know which repositories belong to the local workspace.

The repository registry could store:

- Repository name
- Local path
- Remote URL
- Default branch
- Hosting provider
- PR provider configuration
- Agent provider preferences
- Worktree base directory
- Labels or capabilities

Example:

```yaml
repositories:
  billing-service:
    path: ~/dev/company/billing-service
    default_branch: main
    provider: github
    worktree_dir: .worktrees

  web-dashboard:
    path: ~/dev/company/web-dashboard
    default_branch: main
    provider: github
    worktree_dir: .worktrees
```

This allows projects and tasks to reference repositories by name.

---

## 30. PR Integration

The orchestrator should integrate with the repository hosting provider.

Initial target is likely GitHub.

Responsibilities:

- Create PRs or detect PRs created by agents
- Link PRs to tasks
- Read PR state
- Detect review comments
- Detect requested changes
- Detect approvals
- Detect merge
- Update task state accordingly

Possible commands:

```bash
orchestrator pr sync
orchestrator pr status TASK-123
orchestrator pr comments TASK-123
```

---

## 31. Workspace / Branch / Worktree Manager

To safely run multiple agents, each task should probably run in its own branch and possibly its own worktree.

Responsibilities:

- Create worktree for task
- Create task branch
- Ensure branch starts from the correct base
- Avoid overlapping workspaces
- Clean up after merge
- Rebase or refresh task branches when dependencies merge
- Store worktree metadata on the task
- Store agent session metadata on the task
- Support resuming or reconstructing work after failures

Example branch naming:

```text
task/TASK-123-notification-preferences-model
```

Example worktree path:

```text
.worktrees/TASK-123-notification-preferences-model
```

Each task should know where its work is happening:

```text
Task
- repository: backend-api
- branch: task/TASK-123-notification-preferences-model
- worktree_path: .worktrees/TASK-123-notification-preferences-model
- agent_provider: codex
- agent_session_id: optional provider-specific session reference
- last_agent_run_id: RUN-456
```

This makes the orchestrator capable of recovering from interrupted processes, failed agents, crashed terminals, or daemon restarts.

---

## 32. Agent Startup Context Pattern

A key idea is to give each agent a minimal startup instruction and let it fetch rich context from the orchestrator CLI.

### Minimal Startup Prompt

```text
You are an implementation agent working inside this repository.

Your assigned task is TASK-123.

Before making changes, run:

  orchestrator agent-context TASK-123

Follow the instructions returned by that command exactly.
Do not work on unrelated tasks.
When finished, create or update the PR for this task.
```

### Context Command Output

The command could return structured markdown:

```markdown
# Agent Context for TASK-123

## Task
Add notification preferences database model.

## Status
ready

## Project
PROJECT-001: Add notification preferences

## Repository
backend-api

## Dependencies
None.

## Scope
You may modify:
- backend/db/migrations
- backend/users
- backend/notifications

Avoid modifying:
- frontend/*
- deployment/*

## Acceptance Criteria
- Add persistence for notification preferences.
- Preferences are per-user.
- Default preferences are created for new users.
- Tests cover default preference creation.

## Commands
Run:
- go test ./...

## Messages
No unread messages.

## PR Instructions
Create a PR against main.
Include:
- Summary
- Acceptance criteria checklist
- Tests run
- Known limitations
```

This pattern makes the orchestrator the source of truth and gives agents a repeatable way to recover context.

---

## 33. Parallelism and Conflict Avoidance

The orchestrator should not simply run all tasks at once.

It should decide which tasks are safe to run in parallel.

Inputs to scheduling:

- Task dependencies
- Cross-repository dependencies
- Repository ownership
- Expected files/modules touched
- Current active tasks
- PRs in review
- Merge status of dependencies
- Risk level
- Human-configured concurrency limit

A task can run if:

1. All dependencies are complete.
2. It does not overlap heavily with an active task.
3. It has clear acceptance criteria.
4. The target base branch is current enough.
5. The repository/worktree can be prepared safely.

Possible conflict strategies:

### Conservative Mode

Only run tasks in parallel if they touch clearly separate areas.

Best for early MVP.

### Optimistic Mode

Run more tasks in parallel and resolve conflicts later.

Useful when agents are cheap or tasks are independent.

### Manual Approval Mode

The orchestrator proposes a batch of ready tasks and the human approves which to run.

Good for maintaining control.

---

## 34. Human-in-the-Loop Model

The human should remain in control of important decisions.

Human checkpoints:

1. Approve or edit the initial plan.
2. Approve or edit the complexity classification.
3. Approve whether the work is a single task or project.
4. Approve or edit generated tasks.
5. Decide how many agents can run in parallel.
6. Review PRs.
7. Merge PRs.
8. Cancel or reprioritize tasks.
9. Decide when to rerun failed tasks.
10. Resolve ambiguity that agents surface through messages.

The system can automate coordination, but should not remove the human from architecture and merge decisions.

---

## 35. Suggested MVP

The MVP should prove the core loop without overbuilding.

### MVP Goal

> Given a human request, the CLI can create a plan, classify the work as either a single task or project, run ready tasks with existing coding agents, produce PRs, pause for review, react to comments, and mark tasks complete after merge.

### MVP Features

1. Local project initialization
2. Plan creation
3. Complexity/scope classification: single task vs project
4. Task model with dependencies
5. Project model for multi-task work
6. Manual task creation or task import from markdown
7. Agent provider abstraction with one initial provider
8. `agent-context` command
9. Git branch/worktree creation
10. Launch an agent for a task
11. Require PR creation
12. Sync PR status from GitHub
13. Detect merge and mark task done
14. Unblock dependent tasks
15. Track project progress based on child task completion
16. Simple daemon for coordination and locking
17. Store task-owned worktree/session metadata

### MVP Non-Goals

- Full web UI
- Complex task backend abstraction
- Many agent providers
- Fully autonomous planning
- Advanced conflict resolution
- Automatic merging
- Full cross-repository execution automation
- Deep custom agent SDK implementation

---

## 36. Possible Future Features

### 36.1 Multiple Agent Providers

Support several coding agent tools behind one abstraction.

Example:

```bash
orchestrator run TASK-123 --provider codex
orchestrator run TASK-124 --provider claude-code
orchestrator run TASK-125 --provider opencode
```

### 36.2 Provider Selection Rules

Automatically choose an agent provider based on task type.

Examples:

- Frontend task → OpenCode
- Backend task → Codex
- Refactor task → Claude Code
- Review-only task → cheaper model/provider

### 36.3 Agent Specialization Without Custom Agent Prompts

Instead of defining full custom agents, the system could define task profiles.

Example profiles:

- Planner
- Task decomposer
- Backend implementer
- Frontend implementer
- Reviewer
- Test fixer
- Documentation updater

These profiles would mainly influence the task context and startup prompt, while still relying on the external agent’s native behavior.

### 36.4 Review Agent

A separate agent could review generated PRs before the human does.

This review should be advisory only.

It could check:

- Scope creep
- Missing tests
- Obvious architectural issues
- Acceptance criteria coverage
- Risky changes

### 36.5 Web Dashboard

A future UI could show:

- Task graph
- Active agents
- Background agents
- PR statuses
- Blocked tasks
- Review queues
- Agent logs
- Human actions needed
- Multi-repository project status

### 36.6 Automation Rules

The orchestrator could support rules such as:

```yaml
max_parallel_agents: 3
require_human_approval_before_run: true
auto_run_ready_tasks: false
auto_request_review_agent: true
auto_cleanup_worktrees_after_merge: true
```

### 36.7 Cross-Repository Projects

Future versions could deeply support projects that span multiple repositories.

This would include:

- Repository graph
- Cross-repository dependency tracking
- API contract handoff
- Multi-repo progress visualization
- Coordinated release notes
- Versioning or compatibility checks

### 36.8 Rich Messaging and Decision Requests

The daemon messaging system could evolve into a structured decision layer.

Example:

```text
Decision Request
- project_id: PROJECT-001
- task_id: TASK-003
- from_agent: RUN-789
- question: Should the UI use the new endpoint directly or go through the existing settings store?
- options:
  - A: Use endpoint directly
  - B: Add settings store abstraction
- recommendation: B
- status: awaiting_human
```

---

## 37. Open Questions

### 37.1 Product Questions

1. Should the tool start as purely CLI, or should it eventually have a local web UI?
2. Should task creation be fully automatic, or should the human approve generated tasks before execution?
3. Should the orchestrator ever auto-merge PRs, or should merging always be manual?
4. Should agents be allowed to create new tasks when they discover extra work?
5. Should agents be allowed to modify task dependencies?
6. Should active agents be part of the MVP, or should the MVP only support background agents?
7. Should multi-repository support be part of the MVP, or should the MVP only prepare the architecture for it?

### 37.2 Architecture Questions

1. Should Beads be the first task backend?
2. Should the task store live in the repo, outside the repo, or both?
3. Should each task use a git worktree by default?
4. How should the orchestrator detect file/module conflicts before dispatching tasks?
5. How much provider-specific behavior belongs in each agent provider adapter?
6. How much state should live in the daemon vs persisted storage?
7. Should the daemon use polling, filesystem events, webhooks, or a mix?
8. How should agent session IDs be stored when different providers expose different resume models?

### 37.3 Workflow Questions

1. What should happen when an agent fails halfway through a task?
2. Should a failed task be retried by the same provider or a different provider?
3. Should review comments be grouped and sent to one follow-up agent run?
4. Should the orchestrator require tests before PR creation?
5. Should dependency tasks wait for merge, or is PR approval enough to unblock them?
6. Should cross-repository consuming tasks wait for a producer PR to be merged, or can they target a branch/reference?
7. Should background agents be allowed to message each other directly, or should all messages be task/project-scoped?

---

## 38. Recommended Initial Architecture

A practical initial architecture could look like this:

```text
Agent Orchestration CLI
│
├── CLI Commands
│   ├── plan
│   ├── tasks
│   ├── projects
│   ├── run
│   ├── status
│   ├── agent-context
│   ├── agent attach
│   ├── messages
│   └── pr sync
│
├── Daemon
│   ├── process coordination
│   ├── event loop
│   ├── cross-process locking
│   ├── message routing
│   ├── background agent supervision
│   └── PR polling / webhook handling
│
├── Orchestrator Core
│   ├── task state machine
│   ├── project state machine
│   ├── dependency graph
│   ├── cross-repository dependency graph
│   ├── scheduler
│   ├── event handling
│   └── persistence
│
├── Task Backend
│   ├── local files or Beads
│   └── future: GitHub Issues / Linear / Jira
│
├── Repository Registry
│   ├── known repositories
│   ├── repository paths
│   ├── default branches
│   └── provider settings
│
├── Agent Providers
│   ├── Codex provider
│   ├── Claude Code provider
│   ├── OpenCode provider
│   └── future providers
│
├── Git Workspace Manager
│   ├── branches
│   ├── worktrees
│   ├── per-task workspace metadata
│   └── cleanup
│
└── PR Integration
    ├── GitHub API / gh CLI
    ├── review comments
    ├── approvals
    └── merge detection
```

---

## 39. Opinionated MVP Recommendation

The strongest version of the MVP is not a general-purpose agent framework.

It is a **structured local development orchestrator**.

Recommended MVP stack:

- CLI-first
- GitHub-first
- One repository at a time initially, but with a repository abstraction designed for multi-repo projects
- One or two agent providers
- Local task store or Beads
- Git worktrees per task
- Task-owned worktree and agent session metadata
- Simple daemon for coordination and state locking
- Background agent mode first, active attachable agents later
- Human-approved plans and tasks
- Agent-created PRs
- Human-reviewed merges
- Orchestrator-managed PR status sync

Recommended first workflow:

```text
1. Human writes feature request.
2. Planning agent creates a plan.
3. Human approves/edits plan.
4. Complexity analysis classifies the plan.
5. If simple, create one task.
6. If complex, create a project with multiple tasks and dependencies.
7. Human approves/edits generated task or project structure.
8. Orchestrator runs ready tasks.
9. Each task creates a PR.
10. Agent exits.
11. Human reviews PR.
12. Orchestrator detects comments or merge.
13. If comments exist, agent is relaunched to address them.
14. If merged, task is marked done.
15. Dependent tasks become ready.
16. Project progress updates as child tasks are completed.
```

---

## 40. Short Positioning Statement

> A CLI-first orchestration layer for software teams and solo developers who want to coordinate multiple existing coding agents through a structured planning, task, project, repository, PR, and review workflow — without building custom agents or giving up human control.

Alternative shorter pitch:

> Bring operator control, structure, and visibility to multi-agent software development.

Another version:

> The agent orchestration layer for developers who want AI coding agents to work like a coordinated development team, not an unmanaged swarm.

---

## 41. Possible Names / Concepts

Placeholder names:

- Agent Foreman
- TaskForge
- CodeConductor
- AgentRail
- Worktree Orchestrator
- Branchline
- AgentOps CLI
- Taskline
- PR Pilot
- Codeyard

Conceptual metaphor:

- The human is the product/technical lead.
- The orchestrator is the project manager and dispatcher.
- The daemon is the coordination office.
- Tasks are units of work.
- Projects are coordinated work packages.
- Agents are disposable workers.
- PRs are the review boundary.
- The task graph is the production schedule.
- Worktrees are isolated workstations.

---

## 42. Key Insight

The most important idea is that the orchestrator should own **structure, state, oversight, and workflow**, while existing coding agents own implementation.

That separation is what makes the system powerful:

- The orchestrator does not need to become a complex agent runtime.
- Agents can be swapped or added over time.
- Task state survives agent restarts.
- Project state survives across multiple PRs.
- Human review remains central.
- Parallel work can be managed safely.
- Cross-repository work can be sequenced explicitly.
- PRs become the boundary between autonomous work and controlled integration.
- The daemon can coordinate processes without making agents long-lived sources of truth.

This creates a middle path between manual single-agent coding and fully autonomous multi-agent coding.

The product’s strongest identity is:

> Human-controlled orchestration for structured agentic development.
