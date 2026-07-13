# Mid-MVP Architecture Review

Date: 2026-07-13

## Purpose

This document records a point-in-time architecture review of Orpheus during the MVP. It assesses the architecture described in `docs/ARCHITECTURE.md` against the current Go packages, their production dependencies, major runtime flows, and the active project plan in Beads.

The review focused on package responsibility, dependency direction, data and lifecycle ownership, frontend independence, task-source neutrality, and opportunities to simplify the architecture. It also records the decisions made during follow-up planning and the Beads created or updated as a result.

This is a review and planning record, not the canonical architecture description. `docs/ARCHITECTURE.md` remains the current architecture reference and contains durable decisions rather than implementation planning.

## Scope And Method

The review covered:

- `docs/ARCHITECTURE.md` and adjacent product and workflow documentation;
- all production packages under `internal/` and the `cmd/orpheus` entry point;
- package imports and the main task run, review, finalization, synchronization, agent, status, registry, and persistence paths;
- large production files as indicators of mixed ownership;
- direct external-process integration points for `bd`, `git`, `gh`, agents, and review commands;
- existing open and in-progress Beads related to the findings;
- decisions about future frontends, future task sources, lifecycle ownership, repository models, schema compatibility, interfaces, and composition.

The architecture analysis was performed on `main` at `51ea1cc` (`feat: resume manual review gates from task run (#23)`). Before the review changes were finalized, they were rebased onto `7cbff7a`, which added local Codex usage diagnostics and open-PR branch updates. The canonical architecture document was updated for those additions; they did not change the findings or planning decisions below. There were uncommitted architecture-review changes during the analysis, including the new `docs/ARCHITECTURE.md` and the small code cleanups described below.

## Current Architecture

Orpheus is a daemonless Go CLI. Each command invocation resolves local configuration and state, constructs its dependencies, performs one explicit operation, and exits. Attached coding-agent and review processes are the only long-running work owned by an invocation.

The current package structure is pragmatic and acyclic:

- `cmd/orpheus` is the process entry point.
- `internal/cli` owns Cobra commands, terminal interaction, presentation, and composition.
- `internal/workflow`, `internal/review`, `internal/agent`, and `internal/doctor` implement task, review, agent, and local diagnostic use cases.
- `internal/task`, `internal/taskstate`, `internal/readiness`, `internal/status`, and `internal/publication` own core models, persisted transitions, shared policies, and projections.
- `internal/beads`, `internal/git`, and `internal/pullrequest` adapt external command-line tools.
- `internal/registry`, `internal/state`, and `internal/logging` provide local configuration, persistence infrastructure, locking, and diagnostics.

### Runtime Flow

1. CLI code resolves XDG paths and loads the registered repositories.
2. Registered repository data is translated into task repository sources, and the Beads adapter supplies backend-neutral task data.
3. Workflow code validates readiness, prepares one of the supported Git targets, updates backend metadata, and records the run in task state.
4. Agent code resolves a profile, launches the attached process, renders validated context, and records completion and usage facts.
5. Review code runs the selected read-only pipeline and persists review steps and findings.
6. Workflow code publishes a passed review by committing and pushing directly or by creating a pull request.
7. Synchronization reconciles pull-request state, updates open task branches, closes merged backend tasks, and records local audit facts.
8. Doctor scans registered local task state and can safely recover missing Codex usage facts after explicit approval.
9. Status combines backend snapshots and Orpheus-owned state into the operator action queue.

### State Ownership

The state split is intentional and should be preserved:

- The configured task source, currently Beads, owns task identity, lifecycle status, relations, and task metadata.
- Orpheus owns the repository registry and per-task execution aggregate: target, runs, agent execution facts, completions, reviews, finalization facts, and audit events.
- `registry.Repo` is the persisted repository schema. Application code should consume a validated repository projection instead of independently copying persisted fields.
- Only the latest task-state schema is supported before a stable release. No migration mechanism is maintained for the current single-user installation.

### Current Pressure Points

The largest production files show where behavior has accumulated:

| File | Approximate lines | Architectural signal |
|---|---:|---|
| `internal/cli/task.go` | 4,170 | Presentation, wiring, review lifecycle, follow-up, and finalization coordination are mixed. |
| `internal/taskstate/taskstate.go` | 2,689 | One aggregate owns many valid transitions; size alone does not prove incorrect ownership. |
| `internal/workflow/finalization.go` | 1,448 | Publication and recovery behavior is broad but cohesive enough to defer mechanical splitting. |
| `internal/git/worktree.go` | 1,013 | Multiple target variants share Git mechanics; active sync work may add pressure. |
| `internal/review/runner.go` | 817 | Pipeline execution, manual interaction adapters, subprocesses, and persistence are closely coupled. |

These measurements were used as evidence, not as line-count limits. The review rejected a general file-splitting task in favor of moving code only when responsibility changes provide a cohesive destination.

## Strengths To Preserve

### Acyclic Dependency Graph

The production package graph is acyclic. Core models and infrastructure generally sit below adapters and application packages, and CLI code remains the outermost layer. This provides a workable base for incremental boundary improvements without a rewrite.

### Backend-Neutral Task Model

`internal/task` defines task and repository projections plus narrow capability interfaces. `internal/beads` translates Beads data into those contracts. This is the correct direction for supporting other task sources later.

### Clear Split Between Backend And Orpheus State

Task-source state and Orpheus execution history have distinct authorities. This avoids pretending that one persistence mechanism owns facts it cannot validate and supports safe retry and reconciliation behavior.

### Conservative Git And Publication Behavior

Git target setup rejects dirty, divergent, or mismatched state before unsafe mutation. Finalization records partial success so retries can recover. The architecture tasks must preserve these behavioral contracts.

### Narrow Interfaces At Consumers

Application services already use several capability-oriented interfaces. During this review, the broad `taskstate.Service` interface was removed and `internal/agent` received a consumer-owned `ContextStateLoader` containing only `Load`. This is the preferred interface-ownership pattern.

## Findings

### High: Review Lifecycle Orchestration Remains In CLI

Evidence:

- `internal/cli/task.go` coordinates review start and resume, interactive decisions, review findings, follow-up task creation, autonomous repair, and finalization.
- `internal/review` is already a pipeline engine, and `internal/workflow` already owns dispatch, finalization, and sync lifecycle behavior.
- `internal/cli/task.go` is substantially larger than every other production file.

Impact:

- Application behavior cannot be reused by a future frontend without reproducing Cobra-oriented control flow.
- Safety-critical state transitions and retry rules are difficult to test below the command layer.
- Continued review features increase coupling and make the CLI file harder to change safely.

Decision:

- `internal/workflow` is the long-term owner of complete task lifecycle orchestration.
- `internal/review` remains the review-pipeline engine and read-only candidate enforcer.
- CLI retains parsing, terminal prompts, progress rendering, and typed-outcome adaptation.
- The refactor follows completion of the bounded review-fix loop and is behavior-preserving.

Outcome: created `op-40p.24`, dependent on `op-40p.23`.

### Medium: Execution-Target Policy Is Owned By Workflow

Evidence:

- `internal/workflow/target.go` defines target identity, expected target calculation, metadata reconciliation, task-state reconciliation, and review-lifecycle classification.
- `internal/agent` imports `internal/workflow` to interpret execution targets.
- CLI and status also depend on workflow target types and classifiers.

Impact:

- A reusable policy is coupled to a larger orchestration package.
- Agent context depends on workflow even when it only needs target facts.
- Target validation can drift among dispatch, agent context, status, and finalization consumers.

Decision:

- Introduce `internal/tasktarget` for execution-target identity, expected-target calculation, and reconciliation.
- Keep completion readiness and review-lifecycle classification in `internal/workflow`.
- Keep target inspection and mutation in `internal/git`.
- Perform a behavior-preserving extraction before M9 target-diagnostic hardening.

Outcome: created standalone P1 task `op-ije`.

Beads cannot represent an epic depending on a standalone task, so the intended ordering before `op-34l` is documented in `op-ije` rather than graph-enforced.

### Medium: Agent Profile And Process Execution Responsibilities Are Coupled

Evidence:

- `internal/agent` owns profile configuration, command snapshots, prompts, context, attached launch, and Codex-specific telemetry.
- Implementation and review agents share launch behavior, and future conflict-resolution agents need the same infrastructure.
- Structured Codex profile work already requires package-owned command construction across implementation and review paths.

Impact:

- Harness-specific profile logic and harness-neutral process execution evolve together unnecessarily.
- Reusable launch behavior is harder to consume from additional agent workflows.
- Direct launch details can leak back into CLI or review code.

Decision:

- `internal/agent` owns profile schema, command construction, prompts, context, and harness telemetry.
- New `internal/agentexec` owns harness-neutral execution requests, attached process launch, stdio, cancellation, and execution outcomes.
- Implementation and review agents use the same launch contract.

Outcome: expanded and renamed existing task `op-d1y.7`.

### Medium: Production Git Execution Has Multiple Owners

Evidence:

- Most Git subprocesses are owned by `internal/git`.
- `internal/review/candidate.go` directly invokes Git for snapshot and restoration operations.
- `internal/cli/task.go` directly invokes Git for staged-change detection and concise status.

Impact:

- Exit-code interpretation, error formatting, cancellation, and future diagnostics can diverge.
- The documented adapter boundary is not enforced.
- Review restoration safety depends on command behavior outside the Git adapter.

Decision:

- Move remaining production Git execution behind focused semantic `internal/git` APIs.
- Keep candidate snapshot policy and filesystem snapshot data in `internal/review`.
- Do not expose a generic raw `RunGit` escape hatch or redesign the command runner in this task.

Outcome: created standalone task `op-wtc`.

### Medium: Registry Translation And Repository Projection Are Assembled In CLI

Evidence:

- CLI code maps `registry.Repo` fields into `task.RepositorySource` and clones review aliases.
- Task commands repeatedly combine registry, task-source, and Beads construction concerns.
- Future task sources are expected, although none are planned now.

Impact:

- Persisted registry schema leaks into presentation code.
- A future frontend would need to reproduce source translation.
- Parallel field mapping can drift between CLI and agent paths.

Decision:

- `internal/registry` owns translation from persisted `registry.Repo` values to validated task repository projections and sources.
- `internal/task` remains backend-neutral and must not import `internal/registry`.
- `internal/beads` retains source-specific workspace and command details.

Outcome: expanded `op-yo3.1`; existing sequencing to `op-yo3.2` and `op-yo3.3` remains unchanged.

### Medium: Per-Invocation Dependency Wiring Is Repeated

Evidence:

- Command handlers repeatedly resolve paths, construct registry stores, load task contexts, create task-state stores, and construct backend factories.
- Logger creation occurs at the root, but lower-level diagnostics work needs explicit propagation through adapters and services.
- Package-level mutable constructor overrides are used as test seams in CLI code.

Impact:

- Cross-cutting dependencies can be configured inconsistently within one command.
- Adding safe diagnostics would produce repetitive wiring and more global test seams.
- Composition responsibilities are harder to identify and extend.

Decision:

- Keep `internal/cli` as the current composition root.
- Add a small unexported per-invocation dependency structure for stable dependencies and factories.
- Build use-case services explicitly and load mutable application data when needed.
- Do not add a DI framework, service locator, mutable singleton, or frontend-neutral bootstrap package yet.

Outcome: expanded and renamed `op-cy5.1`; `op-cy5.2` and `op-cy5.3` still depend on it.

### Low: Package Boundaries Are Documented But Not Executable

Evidence:

- `docs/ARCHITECTURE.md` describes responsibilities and dependency direction.
- Go prevents import cycles, but it does not prevent valid-yet-undesired imports or direct external-process calls.

Impact:

- Completed refactors could regress gradually as policy or subprocess execution moves back into consumers.

Decision:

- Do not create a standalone architecture-test task.
- Add focused executable boundary checks to each refactor Bead once the intended package move exists.
- Examples include preventing target policy from returning to workflow, preventing agent subprocess launch outside `agentexec`, and preventing production Git subprocesses outside `internal/git`.

Outcome: acceptance criteria were added to the applicable Beads.

### Low: Large Files Need Cohesive Moves, Not Mechanical Splits

Decision:

- Do not create a broad file-splitting Bead.
- Split files only when approved ownership changes provide a cohesive destination.
- Reassess `taskstate.go` and `workflow/finalization.go` after the planned refactors; size alone is not a sufficient reason to move code.

Outcome: file cohesion requirements were incorporated into related tasks, especially `op-40p.24`.

## Decisions Recorded

The review resolved the following architecture decisions:

1. Other frontends are expected eventually, but none are planned now. Application behavior must still remain independent of Cobra and terminal I/O.
2. `internal/workflow` owns complete task lifecycle orchestration; `internal/review` owns pipeline execution.
3. Other task sources are expected. `internal/task` remains source-neutral, and source-specific details remain in adapters and registry configuration.
4. `registry.Repo` remains the persisted repository schema and is translated at the registry boundary into one validated application projection.
5. Only the latest task-state schema is supported before stable release. No migration machinery or migration tests are required.
6. Interfaces should be narrow and owned by consumers when they express a consumer capability.
7. Dependency wiring remains explicit and manual once per invocation. No DI framework is warranted.
8. Package-boundary checks should follow the approved package moves instead of freezing the current graph prematurely.
9. Low-effort improvements can be performed now; broader moves should be documented and scheduled through scoped Beads.

## Immediate Changes Completed During The Review

The following low-effort changes were implemented before planning the deferred work:

- Removed the broad `taskstate.Service` interface.
- Added consumer-owned `agent.ContextStateLoader` with only the required `Load` method.
- Updated CLI consumers to use the concrete task-state store or the narrow consumer contract.
- Removed obsolete task-state migration-script guidance from production errors and tests.
- Added `docs/ARCHITECTURE.md` with the current package graph, responsibilities, state ownership, runtime flow, and durable evolution decisions.

Validation completed after these code and documentation changes:

- `make fmt`
- `make test`
- `make lint`
- `go vet ./...`
- `git diff --check`

## Planning Outcomes

The final architecture work is sorted by effort below.

| Effort | Criticality | Bead | Packages | Outcome And Reason |
|---|---|---|---|---|
| S | Medium | `op-ije` | `tasktarget`, `workflow`, `agent`, `cli`, `status` | Created standalone P1 task to establish canonical target policy before M9 hardening. |
| S | Medium | `op-wtc` | `git`, `review`, `cli` | Created standalone P2 task to enforce one production Git adapter boundary. |
| M | Medium | `op-d1y.7` | `agent`, new `agentexec`, `review`, `workflow`, `cli` | Updated existing stats child because structured profiles already require shared implementation/review launch infrastructure. |
| L | Medium | `op-yo3.1` | `registry`, `task`, `beads`, `cli` | Updated existing task-management child to move source translation out of CLI while adding backend-neutral creation. |
| L | Medium | `op-cy5.1` | `cli`, `logging`, `state`, `registry`, `taskstate`, `task`, `beads`, `git` | Updated diagnostics foundation to establish explicit per-invocation wiring. |
| XL | High | `op-40p.24` | `workflow`, `review`, `cli`, `taskstate`, `task`, agent execution contracts | Created behavior-preserving child after `op-40p.23` to move complete review lifecycle orchestration below Cobra. |

Additional Beads outcomes:

- Closed `op-n5u` because its migration execution test had been deleted and only the latest schema is supported.
- Updated `op-d1y` and `op-d1y.5` to remove migration-documentation requirements.
- Did not create a standalone import-boundary-test Bead.
- Did not create a general large-file-splitting Bead.
- Did not add planning tables to `docs/ARCHITECTURE.md`.

## Intended Sequencing

- `op-ije` should complete before the target-hardening work in `op-34l`. This is documented rather than graph-enforced because Beads does not allow an epic to depend directly on a task.
- `op-40p.24` depends on `op-40p.23`; the dependency was added and verified in the correct direction.
- `op-cy5.2` and `op-cy5.3` continue to depend on `op-cy5.1`.
- `op-yo3.2` and `op-yo3.3` continue to depend on `op-yo3.1`.
- The standalone Git consolidation task has no dependency on active sync work because that work already uses `internal/git`.

## Risks And Follow-Up Guidance

- Do not implement `op-40p.24` concurrently with unresolved lifecycle changes in `op-40p.23`; stabilize behavior first.
- Treat package moves as behavior-preserving unless a separate product decision explicitly changes behavior.
- Prefer semantic adapter APIs over exported raw subprocess runners.
- Avoid compatibility aliases that leave old package ownership intact after a move.
- Add boundary checks only for the intended post-refactor dependency direction.
- Reassess the architecture after several tasks complete. A future review should start by comparing this report, the listed Beads, current package imports, and actual code movement rather than assuming task status proves architectural progress.

## Review Baseline For Next Time

The next architecture review should explicitly answer:

1. Which of `op-ije`, `op-wtc`, `op-d1y.7`, `op-yo3.1`, `op-cy5.1`, and `op-40p.24` are implemented, partially implemented, superseded, or still open?
2. Did the implementation satisfy the package-boundary acceptance criteria, or only the product behavior?
3. Has review lifecycle code moved out of CLI after `op-40p.23` stabilized?
4. Does `internal/agentexec` exist with the intended dependency direction?
5. Is target policy canonical, or is target classification still duplicated?
6. Are all production Git subprocesses owned by `internal/git`?
7. Does registry translation have one owner outside CLI?
8. Is per-invocation wiring explicit and small, or has it become a service locator?
9. After these moves, do `taskstate.go`, `workflow/finalization.go`, or other large files still indicate mixed ownership?
10. Do `docs/ARCHITECTURE.md` and executable boundary checks match the implemented graph?
