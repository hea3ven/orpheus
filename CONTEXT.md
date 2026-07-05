# Orpheus Context

Orpheus is a CLI-first orchestration layer for coordinating existing coding agents across repository-local tasks, review boundaries, and publication while keeping a human operator in control.

## People and Control

**Operator**:
The engineer who chooses work, reviews outcomes, decides when work is safe to publish, and remains accountable for merge/finalization decisions.
_Avoid_: User, developer, driver

**Agent**:
An executable coding-agent instance launched by Orpheus to perform focused implementation work for one task; it runs through a harness and may use a configured model.
_Avoid_: Worker, bot, provider, harness

**Harness**:
The external coding-agent runtime or CLI that Orpheus executes for an agent, such as Pi or Codex.
_Avoid_: Provider, agent, model

**Model**:
The LLM selected for an agent through its harness configuration.
_Avoid_: Provider, harness, agent

**Provider**:
The organization or service that supplies a model or model pricing; it is not the agent runtime Orpheus executes.
_Avoid_: Harness, agent, runtime

**Agent Profile**:
A named launch configuration for an agent; it may specify the harness, model, command, arguments, and prompt interpolation used to start the agent, but not what task should be done.
_Avoid_: Agent type, provider, runtime

## Work Model

**Epic**:
A repository-scoped feature or goal large enough to be broken into multiple tasks.
_Avoid_: Project, initiative, plan

**Task**:
A repository-scoped unit of work that Orpheus can dispatch to an agent and track as one coherent reviewable change.
_Avoid_: Issue, ticket, job

**Dependency**:
A relationship where one task cannot safely proceed until another task is complete.
_Avoid_: Blocker link, prerequisite

**Registered Repository**:
A repository that Orpheus knows how to locate, inspect through its task source, and apply repository-specific workflow policies to.
_Avoid_: Repo record, source, checkout

**Task Source**:
The authoritative source of task lifecycle truth that Orpheus reads from and updates for task status, dependencies, lifecycle timestamps, and completion.
_Avoid_: Task backend, task provider, task store, Orpheus database

**Beads**:
The MVP task source for Orpheus; it owns the authoritative task lifecycle while Orpheus owns orchestration around that lifecycle.
_Avoid_: Internal task store, issue DB

## Execution and Review

**Run**:
An agent execution focused on implementing code changes for a task.
_Avoid_: Session, job, invocation, review step

**Agent Execution**:
One recorded execution of an agent for usage and timing statistics, covering runs and review-agent steps.
_Avoid_: Provider execution, harness run

**Session**:
A harness-provided identity or log stream used to correlate an agent execution with usage data.
_Avoid_: Run, agent execution, task

**Review-Agent Step**:
An agent execution inside a task review pipeline, focused on evaluating completed task work rather than implementing the original task.
_Avoid_: Run, PR review, provider step

**Agent Usage**:
The measured or estimated resource use of an agent execution, such as tokens, active agent working time, and estimated cost.
_Avoid_: Billing, exact cost

**Estimated Cost**:
An API-equivalent cost estimate calculated from recorded token usage and explicit pricing metadata; it is not guaranteed to match subscription billing or vendor invoices.
_Avoid_: Exact cost, billed cost

**Unknown Usage**:
An agent usage result where Orpheus cannot reliably determine usage values and records the reason instead of inventing or hiding numbers.
_Avoid_: Zero usage, missing data, harness failure, session failure

**Active Agent Working Time**:
The elapsed time an agent execution is actively running.
_Avoid_: Full task time, implementation lifecycle time, wall-clock task time

**Full Task Time**:
The elapsed time from task creation in the task source to finalization or task-source closure.
_Avoid_: Active agent working time, implementation lifecycle time

**Implementation Lifecycle Time**:
The elapsed time from the first Orpheus dispatch for a task to finalization or task-source closure.
_Avoid_: Full task time, active agent working time

**Agent Context**:
The task-source-agnostic task and repository guidance that Orpheus gives an agent for the current run.
_Avoid_: Prompt, task dump, Beads context

**Agent Completion**:
The point where an agent reports that implementation work is finished and hands Orpheus the summary and descriptions needed for review and publication; it makes the task ready for task review, not publication or task completion.
_Avoid_: Completion handshake, task completion, done state, merge readiness

**Task Target**:
The immutable task-level combination of work directory and integration flow, locked when Orpheus starts the task's first run.
_Avoid_: Run target, mode, execution mode, workflow type

**Work Directory**:
The checkout where an agent edits files for a task, either the registered repository root or a dedicated Orpheus worktree.
_Avoid_: Workspace, folder, working copy

**Worktree**:
A dedicated Git worktree created or reused by Orpheus as a task's isolated work directory.
_Avoid_: Workspace, clone, checkout

**Task Branch**:
The deterministic feature branch associated with a task for pull-request-based work.
_Avoid_: Work branch, feature branch, implementation branch

**Integration Flow**:
The chosen way a reviewed task is integrated: through a task branch and pull request, or directly on the default branch.
_Avoid_: Branch mode, review mode

**Task Review**:
The operator-side review gate after agent completion and before publication or finalization; a passed task review authorizes finalization or publication.
_Avoid_: Local review, PR review, code review, task approval, approval

**Review Finding**:
An issue found during task review that may block publication or finalization, or require follow-up work before task review can pass.
_Avoid_: PR comment, task, bug

**Publication**:
The act of pushing reviewed work out of the task review boundary, either by pushing a task branch and creating or recovering a pull request, or by pushing the registered default branch.
_Avoid_: Finalization, sync, release, deploy

**Finalization**:
The Orpheus workflow step that records the consequences of reviewed work after publication, such as task-source closure or local audit facts.
_Avoid_: Publication, sync, completion

**Pull Request**:
The external review object for feature-branch work after task review has passed.
_Avoid_: Review, publication, merge request

**Sync**:
The reconciliation step where Orpheus observes recorded external review state and updates the task source when the outcome changes.
_Avoid_: Publication, polling, PR creation

## Status and Policy

**Status Projection**:
Orpheus' local operator-facing classification of tasks using task-source facts, run facts, review state, and policy state.
_Avoid_: Task status, task lifecycle, dashboard

**Ready to Run**:
A status projection for tasks that Orpheus considers eligible for agent execution under its local readiness policy.
_Avoid_: Backend ready, bd ready, available

**Blocked**:
A status projection for tasks that are waiting on incomplete task dependencies.
_Avoid_: Needs attention, error, policy failure

**Reviewing**:
A status projection for tasks at a review boundary, including task review before publication and external pull-request review after publication.
_Avoid_: In review, approved, completed

**Needs Attention**:
A status projection for tasks that require operator correction before Orpheus can proceed safely.
_Avoid_: Error, repository failure, blocked

**Publication Policy**:
A per-repository rule set that guides agent summaries and determines how completion summaries become commit subjects or pull-request titles.
_Avoid_: Repository publication policy, commit template, PR template, Jira policy

**Tracking Reference**:
A task-source reference to another tracking system, such as a work-ticket key, used by repository publication policies when required.
_Avoid_: External reference, Jira ID, ticket number, task id
