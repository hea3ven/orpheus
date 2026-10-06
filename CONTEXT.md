# Orpheus Context

Orpheus is a CLI-first orchestration layer for coordinating existing coding agents across repository-local tasks, review boundaries, and publication while keeping a human operator in control.

## People and Control

**Operator**:
The engineer who chooses work, reviews outcomes, decides when work is safe to publish, and remains accountable for merge/finalization decisions.
_Avoid_: User, developer, driver

**Agent**:
A coding-agent instance launched by Orpheus for task implementation, review, or sync-conflict resolution; it runs through a harness and may use a configured model.
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
A supported task source for Orpheus; it owns the authoritative task lifecycle while Orpheus owns orchestration around that lifecycle.
_Avoid_: Internal task store, issue DB

**Gig**:
The default task source for newly registered repositories; it owns task lifecycle facts while Orpheus owns execution and review history.
_Avoid_: Execution store, review store

## Execution and Review

**Run**:
An agent execution focused on implementing code changes for a task.
_Avoid_: Session, job, invocation, review step

**Agent Execution**:
One recorded execution of an agent for usage and timing statistics, covering implementation runs, review-agent steps, and sync-conflict resolution.
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
An API-equivalent estimate calculated from recorded usage and pricing metadata, or a cost estimate reported by the harness. Neither is guaranteed to match subscription billing or vendor invoices.
_Avoid_: Exact cost, billed cost

**Unknown Usage**:
An agent usage result where Orpheus cannot reliably determine usage values and records the reason instead of inventing or hiding numbers.
_Avoid_: Zero usage, missing data, harness failure, session failure

**Active Agent Working Time**:
The elapsed time while an agent process is running, including any interactive waits within that execution. It is not a measure of model compute time.
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

**Work Directory**:
The checkout selected on first dispatch where an agent edits files for a task, either the registered repository root or a dedicated Orpheus worktree. It remains fixed across the task's runs even if the checked-out branch changes during publication.
_Avoid_: Workspace, folder, working copy

**Worktree**:
A dedicated Git worktree created or reused by Orpheus as a task's isolated work directory.
_Avoid_: Workspace, clone, checkout

**Task Branch**:
The deterministic feature branch that carries a task's reviewed changes through publication. Worktree-based tasks use it during implementation, while repository-root tasks may materialize it only after review.
_Avoid_: Work branch, feature branch, implementation branch

**Integration Flow**:
The way reviewed task work is integrated: through a pull request or by merging the task branch into the integration destination. Configured defaults and any task-specific manual-review choice determine it independently of the work directory.
_Avoid_: Task target, publishing target, branch mode, review mode

**Integration Destination**:
The branch that receives reviewed task work, either as the pull-request base or the destination of a direct merge. It defaults to the repository's registered default branch unless the operator selects another destination during manual review.
_Avoid_: Work directory, task branch, integration flow

**Task Review**:
The configured review gate after agent completion and before publication or finalization, using automated steps, manual decisions, or both. A passed task review authorizes finalization or publication.
_Avoid_: Local review, PR review, code review, task approval, approval

**Review Finding**:
An issue found during task review that may block publication or finalization, or require follow-up work before task review can pass.
_Avoid_: PR comment, task, bug

**Publication**:
The act of pushing reviewed work out of the task review boundary, either by pushing a task branch and creating or recovering a pull request, or by pushing the integration destination after a direct merge.
_Avoid_: Finalization, sync, release, deploy

**Finalization**:
The Orpheus workflow step that records the consequences of reviewed work after publication, such as task-source closure or local audit facts.
_Avoid_: Publication, sync, completion

**Pull Request**:
The external review object for feature-branch work after task review has passed.
_Avoid_: Review, publication, merge request

**Sync**:
The reconciliation step for published tasks, combining external pull-request state with task-source updates and integration-destination changes to open task branches. It does not create new pull requests.
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
The rules that guide agent summaries and determine how completion summaries become commit subjects or pull-request titles. Repository overrides refine machine-wide defaults.
_Avoid_: Repository publication policy, commit template, PR template, Jira policy

**Tracking Reference**:
A task-source reference to another tracking system, such as a work-ticket key, used by repository publication policies when required.
_Avoid_: External reference, Jira ID, ticket number, task id
