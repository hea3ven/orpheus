# Orpheus

Orpheus is a CLI-first orchestration layer for coordinating existing coding agents across repository-local tasks, review boundaries, and publication while keeping a human operator in control.

## Language

### People and Control

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
A named configuration describing how an agent is launched, distinct from the task it performs.
_Avoid_: Agent type, provider, runtime

### Work Model

**Epic**:
A repository-scoped feature or goal large enough to be broken into multiple tasks.
_Avoid_: Project, initiative, plan

**Task**:
A repository-scoped unit of work with one coherent outcome, such as a code change, research result, or implementation plan.
_Avoid_: Issue, ticket, job

**Dependency**:
A relationship where one task cannot safely proceed until another task is complete.
_Avoid_: Blocker link, prerequisite

**Registered Repository**:
A repository registered with Orpheus for task discovery and repository-specific workflow policies.
_Avoid_: Repo record, source, checkout

**Task Source**:
The authoritative source of task lifecycle facts, including status, dependencies, timestamps, and completion.
_Avoid_: Task backend, task provider, task store, Orpheus database

**Beads**:
A supported task source for Orpheus; it owns the authoritative task lifecycle while Orpheus owns orchestration around that lifecycle.
_Avoid_: Internal task store, issue DB

**Gig**:
A supported task source for Orpheus; it owns task lifecycle facts while Orpheus owns execution and review history.
_Avoid_: Execution store, review store

### Workflow

**Workflow Configuration**:
The shared phases, steps, and progression rules that govern workflows for tasks.
_Avoid_: Workflow, work cycle, workflow definition, agent profile, review pipeline

**Workflow**:
One instance of a workflow configuration applied to a task, with its own progress and history. Pauses and repairs continue that workflow; a deliberate reset creates another.
_Avoid_: Work cycle, workflow run, agent execution, session

**Workflow Reset**:
A confirmed restart of task work from a chosen baseline that discards local checkout changes and begins a new workflow. Earlier workflow records remain history.
_Avoid_: Repair, resume, step retry

**Phase**:
A part of a task workflow with a distinct purpose and an explicit condition for finishing, such as implementation, review, or publication.
_Avoid_: Status, command, step

**Phase Attempt**:
One occurrence of a phase within a workflow. Returning to review after a repair begins another review phase attempt.
_Avoid_: Run, workflow, step attempt

**Workflow Step**:
A defined unit of work within a phase, performed by Orpheus, an agent, an external tool, or the operator.
_Avoid_: Phase, command, agent execution

**Step Attempt**:
One effort to perform a workflow step. Retrying a step creates another attempt without discarding the earlier result.
_Avoid_: Run, phase attempt, session

**Work Artifact**:
A retained result of task work, such as a research report, implementation plan, or agent completion.
_Avoid_: Log, agent execution, task

**Revision**:
An identified, retained version of task output, such as code, a plan, or research, with its producing effort identified. Review and publication refer to that version; recording it implies neither review approval nor publication consent.
_Avoid_: Candidate, work checkpoint, latest changes, work directory, completion summary

**Checkpoint Branch**:
The local branch retaining a workflow's code revisions, independent of the chosen work directory and distinct from the task branch selected for publication.
_Avoid_: Task branch, integration destination, publication branch

**Task Baseline**:
The recorded starting version against which the cumulative changes of a workflow are measured. It is distinct from the branch that will receive published changes.
_Avoid_: Integration destination, previous revision, review delta

**Review Delta**:
The changes between a particular earlier revision and the current revision, presented to focus review. Reviewing that delta does not by itself establish that the entire task satisfies review requirements.
_Avoid_: Task diff, review approval, latest changes

**Review Scope**:
The task requirements and comparison context under which a revision is evaluated, including the relevant baseline and integration destination for code changes.
_Avoid_: Revision, task description, branch name

**Workflow Interaction**:
A point in a task workflow that requires an operator decision before work can continue.
_Avoid_: Prompt, manual phase, terminal input

**Publication Plan**:
The chosen revision, publication content, integration flow, and integration destination for delivering reviewed work.
_Avoid_: Review approval, finalization, agent completion

**Publication Consent**:
The operator's decision authorizing publication of a reviewed revision according to a particular publication plan.
_Avoid_: Review approval, agent completion, task completion

**Publication Commit**:
The commit selected to deliver reviewed task changes through the publication plan. It may present the changes developed across several revisions as one commit while preserving the reviewed output.
_Avoid_: Revision, agent completion, review approval

**External Review**:
The evaluation of published task work through external feedback and responses, including requests for further revisions before acceptance.
_Avoid_: Integration, merge, task review, publication

### Execution and Review

**Agent Execution**:
One execution of an agent for a particular purpose, such as implementation, review, or conflict resolution, with its own outcome and usage record.
_Avoid_: Run, implementation run, provider execution, harness run

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
An API-equivalent estimate of agent usage cost, or an estimate reported by the harness. Neither is guaranteed to match subscription billing or vendor invoices.
_Avoid_: Exact cost, billed cost

**Unknown Usage**:
An agent usage result whose values cannot be reliably determined, with an explicit reason for the uncertainty.
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
The task-source-agnostic task and repository guidance that Orpheus gives an agent for the current execution.
_Avoid_: Prompt, task dump, Beads context

**Agent Completion**:
The handoff of finished implementation work from an agent to task review. It is not task completion or authorization to publish.
_Avoid_: Completion handshake, task completion, done state, merge readiness

**Work Directory**:
The checkout assigned to task work, either the registered repository root or a dedicated Orpheus worktree. Its location is independent of the branch and workflow used for that work.
_Avoid_: Workspace, folder, working copy

**Worktree**:
A dedicated Git worktree created or reused by Orpheus as a task's isolated work directory.
_Avoid_: Workspace, clone, checkout

**Task Branch**:
The branch that carries a task's reviewed changes through publication.
_Avoid_: Work branch, feature branch, implementation branch

**Integration Flow**:
The way reviewed task work is integrated: through a pull request or by merging the task branch into the integration destination. It is independent of the task's work directory.
_Avoid_: Task target, publishing target, branch mode, review mode

**Integration Destination**:
The branch that receives reviewed task work, either as the pull-request base or the destination of a direct merge.
_Avoid_: Work directory, task branch, integration flow

**Task Review**:
The evaluation of a revision through automated steps, manual decisions, or both. Passing task review satisfies the review prerequisite for publishing that revision.
_Avoid_: Local review, PR review, code review, task approval, approval

**Review Finding**:
An issue identified during task review or external review, with its origin and disposition retained. It may require changes to task output or a response before work can proceed.
_Avoid_: PR comment, task, bug

**Publication**:
The delivery of reviewed task work beyond the task review boundary to the remote repository, through a pull request or a direct merge.
_Avoid_: Finalization, sync, release, deploy

**Finalization**:
The recording of task closure and other consequences after the task workflow's required publication and acceptance of its output are established.
_Avoid_: Publication, sync, completion

**Pull Request**:
The external review object for feature-branch work after task review has passed.
_Avoid_: Review, publication, merge request

**Sync**:
The reconciliation of published task work with external pull-request state, task-source lifecycle facts, and the integration destination. It does not create new pull requests.
_Avoid_: Publication, polling, PR creation

### Status and Policy

**Status Projection**:
Orpheus' local operator-facing classification of tasks using task-source facts, agent execution facts, review state, and policy state.
_Avoid_: Task status, task lifecycle, dashboard

**Ready to Run**:
A status projection for tasks that Orpheus considers eligible for agent execution under its local readiness policy.
_Avoid_: Backend ready, bd ready, available

**Blocked**:
A status projection for work waiting on task-source blocking, incomplete dependencies, or an inactive parent epic.
_Avoid_: Needs attention, error, policy failure

**Reviewing**:
A status projection for tasks at a review boundary, including task review before publication and external pull-request review after publication.
_Avoid_: In review, approved, completed

**Needs Attention**:
A status projection for tasks that require operator correction before Orpheus can proceed safely.
_Avoid_: Error, repository failure, blocked

**Publication Policy**:
The rules for agent completion summaries and their use as commit subjects or pull-request titles.
_Avoid_: Repository publication policy, commit template, PR template, Jira policy

**Tracking Reference**:
A task-source reference to another tracking system, such as a work-ticket key, used by repository publication policies when required.
_Avoid_: External reference, Jira ID, ticket number, task id
