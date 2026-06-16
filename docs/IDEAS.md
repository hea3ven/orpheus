# Ideas Deferred Beyond the Current MVP

This file collects ideas that are **not** part of the current Orpheus MVP scope, but are worth remembering.

The current MVP direction is intentionally narrower: a CLI-controlled, Beads-first, daemonless workflow centered on repo registration, global task visibility, deterministic worktrees/branches, interactive attached single-task dispatch, agent context/completion, Orpheus-owned commits, PR creation, PR polling, merge sync, and hardening of that single-task lifecycle.

The items below come from:

- The original product brief: [`2026-05-07-agent_orchestration_cli_product_brief.md`](./2026-05-07-agent_orchestration_cli_product_brief.md)
- The current MVP spec boundary: [`2026-05-12-agent_orchestration_cli_mvp_spec.md`](./2026-05-12-agent_orchestration_cli_mvp_spec.md)
- The milestone 3 scope discussion on 2026-06-01
- The MVP retrospective on 2026-06-16: [`2026-06-16-mvp_retrospective.md`](./2026-06-16-mvp_retrospective.md)

---

## Background / detached agents

Background agents are **not** part of the current MVP after the milestone 3 simplification.

The current M3 direction is to focus on **interactive attached agents** that run in the user’s current terminal, inherit stdin/stdout/stderr, and block until the agent exits.

Deferred background-agent ideas:

- Orpheus launches a configured agent process in the background.
- Orpheus captures logs/transcripts from that process.
- Orpheus records process IDs and process state.
- Orpheus supervises active background runs.
- Orpheus reconciles process status after crashes or restarts.
- Orpheus can show logs with a command such as:

  ```bash
  orpheus task logs <task-id>
  ```

- Orpheus supports retrying failed detached runs.
- Orpheus can run several tasks without the user babysitting each process.
- Background agents are best for tasks that are:
  - well defined
  - have clear acceptance criteria
  - require little or no human interaction
  - should independently produce a PR

Original conceptual command:

```bash
orchestrator run TASK-123 --mode background
```

Important implementation concern discovered during discussion:

- For `pi`, running a non-interactive prompt like:

  ```bash
  pi -p '<prompt>'
  ```

  does not stream or show the full interactive transcript. It outputs only the final message from the LLM.
- That may make background log capture less useful or more difficult than expected.
- This is one reason background agents were moved out of M3/MVP in favor of attached interactive runs.

---

## Full attachable terminal sessions

The current MVP can support attached execution in the **current terminal**, but full attachable terminal-session management is deferred.

Deferred ideas:

- Active agents are sessions that the human can attach to and interact with directly.
- Active agents are useful when:
  - the task is ambiguous
  - the human wants to guide the implementation
  - the agent needs frequent decisions
  - the user wants an interactive coding session
- The system could support commands such as:

  ```bash
  orchestrator agent start TASK-123 --mode active
  orchestrator agent attach TASK-123
  ```

- Active and background modes should eventually use the same task, project, worktree, and PR lifecycle.
- The difference between the modes is how much direct human interaction is expected while the agent is running.

---

## Task run session tracking and resume

Persisting and resuming provider-specific agent sessions is deferred beyond the MVP.

Idea from the 2026-06-16 MVP retrospective:

- Orpheus could keep track of the interactive session created by `task run`.
- If the operator runs `task run` again for a task that already has a previous run, Orpheus could restore or resume that session instead of starting from scratch.
- The operator could then ask the same agent session to make follow-up changes or answer questions about the prior implementation.

Why this is deferred:

- Session identity and resume semantics are provider-specific.
- Each agent CLI may expose different behavior for session ids, transcripts, cwd restoration, prompts, and interactive/non-interactive resume.
- Some agents may not support reliable resume at all.
- Orpheus should first keep task, branch, worktree, PR, and review state as the durable source of truth.

Potential future behavior:

```bash
orpheus task run TASK-123          # starts a provider session
orpheus task run TASK-123          # offers to resume the previous session
orpheus task resume TASK-123       # explicit resume command, if added
orpheus task ask TASK-123 "..."    # query/follow up through the stored session, if supported
```

Implementation notes to preserve:

- Store provider name, run id, provider session id, cwd, branch/worktree target, and transcript/log pointers where available.
- Keep provider session metadata attached to Orpheus run records, not as the authoritative task lifecycle.
- Fall back to launching a fresh agent with task context when a provider cannot resume.
- Avoid assuming the same session is required for correctness; resumed sessions should be convenience, not the only recovery path.

---

## Terminal runner nodes and daemon-dispatched panes

The long-term terminal-runner idea is deferred beyond the MVP.

Detailed idea from the milestone 3 discussion:

- The user may have several terminals open.
- Orpheus could provide a command that sets up a terminal to listen to the daemon.
- When the user runs a task, the task is dispatched to one of those listening terminals.
- The agent is executed in that terminal.
- The user might set up `tmux` manually with four panes.
- Each pane could run this future “agent runner node” command.
- When the user runs multiple tasks, or when the daemon detects available tasks, Orpheus dispatches tasks to each pane.
- In a more distant future, Orpheus could control `tmux` itself.
- Orpheus could create panes automatically.
- Orpheus could manage the pool of available panes/runners.

Possible future shape:

```text
Terminal 1: orpheus runner listen
Terminal 2: orpheus runner listen
Terminal 3: orpheus runner listen
Terminal 4: orpheus runner listen

orpheus task run-ready --limit 4
  ↓
daemon dispatches ready tasks to idle runner terminals
```

This is related to, but larger than, basic attached runs. The MVP should not require a daemon, terminal listener, or `tmux` control.

---

## Cheap utility agents for generated text

Cheap utility agents are deferred beyond the current MVP.

Examples mentioned:

- Generate commit descriptions.
- Generate PR titles.
- Generate PR descriptions.
- Generate other simple summaries or metadata.

The current MVP should use deterministic text based on `orpheus agent done --summary` and `--details`, plus simple templates, rather than calling an LLM for utility text.

Existing personal workflow example from `../commit.sh`:

```bash
#! /bin/bash

if [[ -z "$TASK_ID" ]]; then 
	echo TASK_ID not set
fi
commit_desc=$(pi -p --model 'openai-codex/gpt-5.4-mini:medium' "Summarize in 80 characters for the commit description, the current working tree changes, which are the implementation for bead $TASK_ID. Use the format \"<type(fix,feat,test,chore,conf,etc)>: <description>\", do not include the bead id. Do not mention tests even if they are included in the changes.")
git commit -m "$commit_desc"
bd update $TASK_ID --status closed
```

Details to preserve from that workflow:

- It requires `TASK_ID` in the environment.
- It uses `pi -p`.
- It uses model `openai-codex/gpt-5.4-mini:medium`.
- It asks for an 80-character commit description.
- It asks for the format:

  ```text
  <type(fix,feat,test,chore,conf,etc)>: <description>
  ```

- It tells the model not to include the bead id.
- It tells the model not to mention tests even if tests are included in the changes.
- It commits with:

  ```bash
  git commit -m "$commit_desc"
  ```

- It closes the Bead with:

  ```bash
  bd update $TASK_ID --status closed
  ```

Potential future integration options:

- Optional configured command for commit-message generation.
- Optional configured command for PR title generation.
- Optional configured command for PR body generation.
- Utility-agent profiles separate from implementation-agent profiles.
- Cheap model profile for simple text generation.
- Smart model profile for planning/design/review.

---

## User-configurable pipelines

Pipeline abstraction is deferred until after the MVP.

Discussion points to preserve:

- “Running a task” is not the only type of agent dispatch Orpheus may eventually support.
- Orpheus may also dispatch an interactive agent to define and refine tasks.
- There is not only an “implement feature pipeline”.
- A possible implementation pipeline is:

  ```text
  run agent to implement
    ↓
  create PR
    ↓
  finish task
  ```

- Other pipelines may have different steps.
- Pipelines could be customizable or defined by the user.
- Pipeline abstraction should be considered after the MVP or in later milestones.

Future pipeline examples:

```text
Planning pipeline:
  human request
    ↓
  planning agent
    ↓
  human approval
    ↓
  task/project creation

Task refinement pipeline:
  rough task
    ↓
  interactive refinement agent
    ↓
  human approval
    ↓
  ready task

Implementation pipeline:
  ready task
    ↓
  implementation agent
    ↓
  Orpheus commit
    ↓
  PR creation
    ↓
  review sync
    ↓
  merge sync

Review-response pipeline:
  PR comments detected
    ↓
  follow-up agent
    ↓
  same branch updated
    ↓
  PR returned to review

Utility pipeline:
  git diff / run metadata
    ↓
  cheap utility agent
    ↓
  commit title / PR title / PR description
```

---

## Automated planning

Automated planning is not part of the current MVP.

Original product-brief planning stage:

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

Planning-agent behavior:

- The planning agent should not necessarily make code changes.
- Its primary job is to produce a clear plan that can be inspected and refined.

Example planning output from the product brief:

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

## Complexity and scope classification

Automatic complexity/scope classification is deferred.

Original goal:

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

Conceptual shape:

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

## Project model

A first-class project model is deferred.

Original product brief:

- The system should model both projects and tasks.
- A project represents a larger goal that may require several PRs.
- A task represents an executable unit that an agent can work on directly.

Conceptual structures:

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

Project lifecycle statuses from the product brief:

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

## Automated task decomposition

Automated task decomposition is deferred.

Original product-brief task decomposition stage:

- For project-sized work, a second agent or workflow step takes the plan and breaks it into executable tasks.
- For single-task work, this stage can simply produce one well-defined task.

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

## Task creation wrapper over Beads

The current MVP does not include an Orpheus task-creation wrapper over Beads.

Deferred ideas:

- `orpheus task create ...`
- `orpheus tasks create-from-plan PLAN_ID`
- Manual task creation through Orpheus rather than directly through `bd`.
- Task import from markdown.
- Creating tasks from an approved plan.
- Creating a project and its task graph from an approved plan.
- Agents creating new tasks when they discover extra work.
- Agents modifying task dependencies.

The MVP keeps Beads as the authoritative task lifecycle store and uses Beads directly for task creation.

---

## Native task store and configurable task backends

Native task storage and user-configurable backends are deferred.

Original task-store options:

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

Recommended long-term direction from the brief:

> Start with a narrow task abstraction, then implement Beads or a simple local store as the first backend.

Potential future backends:

- Beads
- GitHub Issues
- Jira
- Linear
- Local files
- Native Orpheus store

---

## Cross-repository project dependency scheduling

The current MVP can register multiple repositories and show a global task queue, but cross-repository project dependency scheduling is deferred.

Original multi-repository coordination idea:

- The orchestrator should be aware of multiple repositories, not just one local codebase.
- This is valuable in microservice or multi-application environments where a single product change may require coordinated work across several repositories.

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

The orchestrator could sequence work safely by ensuring the API-producing repository is changed, reviewed, and merged before launching the task that consumes that API in another repository.

A project should eventually be able to contain tasks from multiple repositories, and each task should carry repository-specific execution information:

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

Deferred cross-repo features:

- Cross-repository dependency graph.
- Cross-repository dependency scheduling.
- Cross-repository execution automation.
- Repository graph.
- API contract handoff.
- Multi-repo progress visualization.
- Coordinated release notes.
- Versioning or compatibility checks.
- Deciding whether cross-repository consuming tasks should wait for a producer PR to be merged, or can target a branch/reference.

---

## Batch scheduling and `run-ready`

Batch scheduling is deferred after the M3 simplification because it depends much more naturally on background agents, detached runs, or runner nodes.

Previously planned command examples:

```bash
orpheus task run-ready --limit <n>
orpheus task run-ready --limit <n> --agent <agent-name>
orpheus task run-ready --limit <n> --yes
```

Original batch-scheduling behavior:

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
- Preview exact tasks before launching.
- Launch no more than the configured limit.
- Avoid duplicating already-running, PR-ready, or in-review tasks.
- Concurrent invocations cannot schedule the same task twice once the mutation lock is implemented.

Related automation-rule ideas:

```yaml
max_parallel_agents: 3
require_human_approval_before_run: true
auto_run_ready_tasks: false
auto_request_review_agent: true
auto_cleanup_worktrees_after_merge: true
```

---

## Advanced parallelism and conflict avoidance

Complex scheduling and conflict avoidance are deferred.

The orchestrator should eventually decide which tasks are safe to run in parallel.

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

Best for early versions.

### Optimistic Mode

Run more tasks in parallel and resolve conflicts later.

Useful when agents are cheap or tasks are independent.

### Manual Approval Mode

The orchestrator proposes a batch of ready tasks and the human approves which to run.

Good for maintaining control.

Other deferred conflict/workspace ideas:

- Complex file-scope conflict detection.
- Expected-file/module overlap detection before dispatch.
- Avoiding overlapping workspaces.
- Refreshing task branches when dependencies merge.
- Rebasing task branches after dependency merges.

---

## Long-running daemon

A long-running daemon is not part of the current MVP.

Original daemon architecture:

- The CLI can be backed by a daemon process that acts as the coordination layer between human CLI commands, active agents, background agents, task state, worktrees, and PR synchronization.
- When the user runs the CLI, it should check whether the daemon is running.
- If the daemon is not running, the CLI can start it automatically.

Conceptual flow:

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

The daemon is especially useful because multiple processes may exist at once:

- Human CLI sessions
- Background agent processes
- Active attachable agent sessions
- PR synchronization loops
- Review-comment handlers
- Message routing processes

Without a daemon, these processes would need to coordinate through files and locks alone. With a daemon, there can be one runtime authority for coordination.

Possible future daemon loop from the MVP spec:

```text
every N minutes:
  run task sync --all service
```

Daemon responsibilities from the recommended architecture:

- process coordination
- event loop
- cross-process locking
- message routing
- background agent supervision
- PR polling / webhook handling

Daemon polling/event mechanism questions to preserve:

- Should the daemon use polling?
- Should it use filesystem events?
- Should it use webhooks?
- Should it use a mix?

---

## Daemon messaging layer

The daemon messaging layer is deferred.

Original idea:

The daemon could provide a messaging layer for coordination between:

- Background agents
- Active agents
- The human
- The orchestrator scheduler
- Review/comment handlers

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

## Rich decision requests

A structured decision layer is deferred.

Example from the original brief:

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

Related human-in-the-loop checkpoints to preserve:

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

---

## First-class agent provider integrations

The current MVP uses global named shell-command profiles, not first-class provider integrations.

Deferred provider abstraction:

At minimum, the abstraction should support:

- Agent provider type
- Initial prompt
- Working directory
- Target task
- Context command or instructions
- Expected output behavior
- Branch/PR expectations
- Resume or follow-up behavior

Conceptual abstraction:

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

Provider-specific implementations would translate this into the right command-line invocation for each agent.

Agent provider interface from the brief:

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

Provider responsibilities:

- Launch the external agent process.
- Format the initial prompt appropriately.
- Pass working directory and environment variables.
- Support task execution.
- Support review-response execution.
- Capture logs or transcripts if available.
- Return success/failure status to the orchestrator.

---

## Multiple agent providers

Multiple first-class agent providers are deferred.

Future examples:

```bash
orchestrator run TASK-123 --provider codex
orchestrator run TASK-124 --provider claude-code
orchestrator run TASK-125 --provider opencode
```

Potential providers:

- Claude Code
- Codex
- OpenCode
- Pi
- Other CLI-based coding agents
- Custom shell providers

The system should use existing coding agents instead of creating a new custom agent runtime.

The orchestrator controls what work should be done and when. The agent provider controls how the process is launched and instructed.

---

## Provider selection rules

Automatic provider selection is deferred.

Examples from the product brief:

- Frontend task → OpenCode
- Backend task → Codex
- Refactor task → Claude Code
- Review-only task → cheaper model/provider

---

## Agent specialization / task profiles

Agent specialization beyond simple named command profiles is deferred.

Original idea:

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

---

## Review agent

An advisory review agent is deferred.

Original idea:

- A separate agent could review generated PRs before the human does.
- This review should be advisory only.

It could check:

- Scope creep
- Missing tests
- Obvious architectural issues
- Acceptance criteria coverage
- Risky changes

Additional idea from the 2026-06-16 MVP retrospective:

- After an implementation run succeeds, Orpheus could optionally run a second agent-review step before the human creates the PR.
- This review should happen on the local branch/worktree and should be advisory only.
- The output could be a local review note, checklist, or suggested follow-up prompt.
- The human remains the required reviewer before PR creation; the review agent should not publish or merge work on its own.

Possible future flow:

```text
implementation agent done
  ↓
optional review agent checks local diff
  ↓
human reviews locally
  ↓
human explicitly creates PR
```

Related automation rule:

```yaml
auto_request_review_agent: true
```

---

## Review comment parsing and agent relaunch

Review comment parsing and relaunching agents to address comments are deferred.

Original review-response stage:

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

Deferred PR/review commands:

```bash
orchestrator pr comments TASK-123
orchestrator pr status TASK-123
```

Deferred review states:

- `changes_requested`
- `updating`
- `approved`

Deferred behavior:

- Detect requested changes.
- Detect approvals.
- Group review comments and send them to one follow-up agent run.
- Decide whether review comments should be grouped and sent to one follow-up agent run.
- Deliver review feedback through the daemon messaging layer.

---

## Auto-merge

Auto-merge is deferred.

The product principle remains that humans should:

- Review PRs.
- Request changes.
- Merge PRs.
- Make architectural decisions.

Open question from the original brief:

> Should the orchestrator ever auto-merge PRs, or should merging always be manual?

---

## Local validation and required test reporting

Validation/test enforcement before PR creation is deferred.

Deferred ideas:

- Require local validation before PR creation.
- Require tests before PR creation.
- Required test reporting.
- Store test results in run records.
- Include tests run in PR bodies.
- Have task context tell agents exactly which commands to run.

Original PR body example included:

```markdown
## Tests
- go test ./...
```

Open workflow question:

> Should the orchestrator require tests before PR creation?

---

## Rich PR integration beyond create/poll/merge

The current MVP keeps PR integration narrow.

Deferred PR integration responsibilities from the product brief:

- Create PRs or detect PRs created by agents.
- Link PRs to tasks.
- Read PR state.
- Detect review comments.
- Detect requested changes.
- Detect approvals.
- Detect merge.
- Update task state accordingly.

Deferred commands:

```bash
orchestrator pr sync
orchestrator pr status TASK-123
orchestrator pr comments TASK-123
```

Desired PR body details from the product brief:

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

## Configurable repository and PR settings

Several repository and PR configuration options are deferred.

Deferred repository registry fields from the product brief:

- Repository name
- Local path
- Remote URL
- Default branch
- Hosting provider
- PR provider configuration
- Agent provider preferences
- Worktree base directory
- Labels or capabilities

Example future registry shape:

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

Specific deferred settings from the current MVP spec:

- Repo-specific agent profile overrides.
- Configurable worktree base directory.
- Configurable PR base branch beyond the registered repo default branch.
- Rich hosting-provider configuration beyond the initial GitHub/`gh` path.

---

## Advanced workspace, branch, and worktree management

Basic deterministic branch/worktree creation is in the current MVP, but advanced worktree management is deferred.

Deferred responsibilities from the original product brief:

- Clean up after merge.
- Rebase or refresh task branches when dependencies merge.
- Store agent session metadata on the task.
- Support resuming or reconstructing work after failures.
- Avoid overlapping workspaces.
- Detect file/module conflicts before dispatch.

Original task metadata shape:

```text
Task
- repository: backend-api
- branch: task/TASK-123-notification-preferences-model
- worktree_path: .worktrees/TASK-123-notification-preferences-model
- agent_provider: codex
- agent_session_id: optional provider-specific session reference
- last_agent_run_id: RUN-456
```

Example branch name:

```text
task/TASK-123-notification-preferences-model
```

Example worktree path:

```text
.worktrees/TASK-123-notification-preferences-model
```

This makes the orchestrator capable of recovering from:

- interrupted processes
- failed agents
- crashed terminals
- daemon restarts

---

## Web dashboard / web UI

A web UI is deferred.

Future dashboard could show:

- Task graph
- Active agents
- Background agents
- PR statuses
- Blocked tasks
- Review queues
- Agent logs
- Human actions needed
- Multi-repository project status

Open product question:

> Should the tool start as purely CLI, or should it eventually have a local web UI?

---

## Full custom agent SDK/runtime

A deep custom agent SDK/runtime is not part of the MVP and is not the preferred direction.

The product brief explicitly favors:

- Using existing coding agents.
- Not building a custom agent runtime from scratch.
- Giving agents a small startup prompt that tells them to run a CLI command to fetch richer context.

Deferred/non-goal:

- Deep custom agent SDK implementation.
- Replacing Claude Code/Codex/OpenCode/Pi with a new Orpheus-native coding agent.
- Custom tools, custom memory, or a large system prompt as the core implementation strategy.

---

## Fully autonomous planning and fully autonomous coding

Fully autonomous planning/coding is outside the current product direction and MVP.

The product should avoid:

- Agents being given broad goals and running with limited structure.
- Work becoming difficult to inspect or control.
- The human losing visibility over why tasks are happening.
- The human losing visibility over which dependencies exist.
- The human losing visibility over what should be reviewed first.
- Jumping directly from user request to implementation without planning/checkpoints for larger work.

The desired long-term direction is controlled autonomy, not maximum autonomy.

The product should keep the operator in control of:

- approving plans
- approving generated task/project structure
- reviewing PRs
- requesting changes
- merging PRs
- making architectural decisions

---

## Rich task lifecycle states

The current MVP maps onto Beads statuses and Orpheus projections rather than implementing a full native task state machine.

Original proposed task lifecycle:

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

Simplified lifecycle from the product brief:

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

## Agent startup context enhancements

The current MVP includes an agent context command, but richer context payloads are deferred.

Original context command could return:

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

Original minimal startup prompt:

```text
You are an implementation agent working inside this repository.

Your assigned task is TASK-123.

Before making changes, run:

  orchestrator agent-context TASK-123

Follow the instructions returned by that command exactly.
Do not work on unrelated tasks.
When finished, create or update the PR for this task.
```

Original context markdown example:

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

---

## Original broader CLI command surface

Many command ideas from the original product brief are deferred.

Possible commands from the brief:

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

## Original suggested MVP items that are no longer current MVP scope

The original product brief’s suggested MVP was broader than the current MVP.

Original MVP goal from the brief:

> Given a human request, the CLI can create a plan, classify the work as either a single task or project, run ready tasks with existing coding agents, produce PRs, pause for review, react to comments, and mark tasks complete after merge.

Original suggested MVP features that are now deferred or narrowed:

1. Local project initialization.
2. Plan creation.
3. Complexity/scope classification: single task vs project.
4. Task model with dependencies.
5. Project model for multi-task work.
6. Manual task creation or task import from markdown.
7. Agent provider abstraction with one initial provider.
8. `agent-context` command with the richer original scope.
9. Launch an agent for a task as a provider-managed/background-capable run.
10. Require PR creation by the agent.
11. Unblock dependent tasks as a project graph progresses.
12. Track project progress based on child task completion.
13. Simple daemon for coordination and locking.
14. Store task-owned worktree/session metadata.
15. React to review comments.

Original recommended first workflow that is now beyond MVP:

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

Original recommended MVP stack items that are deferred or changed:

- One repository at a time initially, but with a repository abstraction designed for multi-repo projects.
- One or two agent providers.
- Local task store or Beads.
- Simple daemon for coordination and state locking.
- Background agent mode first, active attachable agents later.
- Human-approved plans and tasks.
- Agent-created PRs.
- Orchestrator-managed PR status sync including comments.

Current direction changes from that original stack:

- Beads-first rather than native/local task store first.
- Shell-command profiles rather than first-class providers.
- Daemonless-first rather than daemon-first.
- Interactive attached M3 rather than background-agent-first M3.
- Orpheus-owned commit and PR creation rather than requiring the agent to create the PR itself.

---

## Product and architecture open questions to keep

These are not MVP implementation items, but they should remain visible.

### Product questions

1. Should the tool start as purely CLI, or should it eventually have a local web UI?
2. Should task creation be fully automatic, or should the human approve generated tasks before execution?
3. Should the orchestrator ever auto-merge PRs, or should merging always be manual?
4. Should agents be allowed to create new tasks when they discover extra work?
5. Should agents be allowed to modify task dependencies?
6. Should active agents be part of the MVP, or should the MVP only support background agents?
7. Should multi-repository support be part of the MVP, or should the MVP only prepare the architecture for it?

### Architecture questions

1. Should Beads be the first task backend?
2. Should the task store live in the repo, outside the repo, or both?
3. Should each task use a git worktree by default?
4. How should the orchestrator detect file/module conflicts before dispatching tasks?
5. How much provider-specific behavior belongs in each agent provider adapter?
6. How much state should live in the daemon vs persisted storage?
7. Should the daemon use polling, filesystem events, webhooks, or a mix?
8. How should agent session IDs be stored when different providers expose different resume models?

### Workflow questions

1. What should happen when an agent fails halfway through a task?
2. Should a failed task be retried by the same provider or a different provider?
3. Should review comments be grouped and sent to one follow-up agent run?
4. Should the orchestrator require tests before PR creation?
5. Should dependency tasks wait for merge, or is PR approval enough to unblock them?
6. Should cross-repository consuming tasks wait for a producer PR to be merged, or can they target a branch/reference?
7. Should background agents be allowed to message each other directly, or should all messages be task/project-scoped?

---

## Product naming and metaphor ideas

These are not MVP functionality, but they are product ideas from the original brief.

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

Positioning statements from the brief:

> A CLI-first orchestration layer for software teams and solo developers who want to coordinate multiple existing coding agents through a structured planning, task, project, repository, PR, and review workflow — without building custom agents or giving up human control.

> Bring operator control, structure, and visibility to multi-agent software development.

> The agent orchestration layer for developers who want AI coding agents to work like a coordinated development team, not an unmanaged swarm.

---

## Core long-term identity to preserve

The deferred ideas above all serve the original long-term identity:

- The orchestrator owns structure, state, oversight, and workflow.
- Existing coding agents own implementation.
- The orchestrator does not need to become a complex agent runtime.
- Agents can be swapped or added over time.
- Task state survives agent restarts.
- Project state survives across multiple PRs.
- Human review remains central.
- Parallel work can be managed safely.
- Cross-repository work can be sequenced explicitly.
- PRs become the boundary between autonomous work and controlled integration.
- A daemon can coordinate processes without making agents long-lived sources of truth.

Long-term product identity from the brief:

> Human-controlled orchestration for structured agentic development.
