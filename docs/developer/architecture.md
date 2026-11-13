# Architecture

## High-Level Overview

Orpheus is a daemonless Go CLI that coordinates coding-agent work across registered repositories. Each invocation builds the required services, reads the current local state, performs one explicit operation, and exits. Long-running work is limited to attached agent or review processes started by the invoking command.

The architecture uses a pragmatic layered structure:

- `cmd/orpheus` and `internal/cli` form the executable, composition, and presentation layer.
- `internal/workflow` owns built-in progression; `internal/review`, `internal/agent`, `internal/agentexec`, and `internal/doctor` provide specialized execution and diagnostic services.
- `internal/work` defines I/O-free work history and invariants. `internal/workstate` persists that history; `internal/taskstate` coordinates typed mutations and derives service views.
- `internal/task`, `internal/tasktarget`, `internal/taskbranch`, `internal/readiness`, `internal/status`, `internal/taskstats`, and `internal/publication` provide task contracts, policies, and projections.
- `internal/tasksource/{beads,gig}`, `internal/git`, and `internal/pullrequest` adapt task sources and external tools. `internal/registry`, `internal/state`, `internal/pathutil`, and `internal/logging` provide local infrastructure.

State ownership is intentionally split. The configured task source, Beads or Gig, is authoritative for task identity, lifecycle, relations, and evolving Orpheus Git facts stored in task metadata. Orpheus owns repository registration and workflow history: prepared context, executions, handoffs, retained output identities, review evidence, publication consent and delivery, external observations, and closure/cleanup receipts. Registry and workflow records use human-readable YAML below XDG roots; managed Gig task data uses an embedded SQLite database.

Runtime integrations include `bd`, `git`, `gh`, configured agent executables, and optional review tools such as Hunk. Go dependencies include Cobra, `yaml.v3`, and the Gig SDK with its pure-Go SQLite driver.

## Main Runtime Flow

1. CLI captures invocation dependencies and environment, resolves XDG paths, loads the registry, and selects a Beads or Gig adapter through source-neutral task contracts.
2. `workflow.Controller.Advance` reconciles interrupted attached work and derives the next action from the current workflow's last typed phase. It selects, reserves, executes, and reloads results in one application-owned loop.
3. Preparation establishes a task branch and retained baseline in either the registered repository root or a dedicated worktree. Work-directory selection does not choose the publication flow.
4. Implementation executes the selected agent. Its own completion handoff and observed successful child exit permit `ImplementationService` to capture an ordinary Git revision. Output is saved before review admission, even when hook leftovers subsequently prevent a clean checkout.
5. Review evaluates that retained output against captured requirements, baseline/delta endpoints, destination, and required review slots. Checks, reviewers, and manual steps are read-only. Repairs append Implementation and fresh Review phases within the same workflow.
6. A passed review prepares a separate publication decision. Explicit consent identifies actual committed input, cumulative publication content, flow, destination, and PR draft/ready mode where applicable. Authorized publication packages public history in place and records observed delivery. `task run` stops after delivery; `task done` only retries authorized delivery.
7. Sync reconciles External review and Finalization. PR merge observations or verified direct delivery permit source closure; cleanup retries independently. Status and workflow inspection consume the same controller-selected next actions.

A confirmed `task reset` schedules a new workflow without checkout effects. Its next Preparation fetches the default branch, records a reset target before destructive work, and retries that target after partial failure. Earlier work remains history without lending the new workflow approval or consent.

The independent `doctor` flow scans registered repositories and Orpheus-owned task state for recoverable local inconsistencies. Its first diagnostic correlates missing supported harness usage facts, currently Codex and Pi, for implementation, review-agent, and terminal sync-conflict resolution executions with local session logs and mutates task state only when the operator supplies `--fix` and the match is safe.

## Dependency Boundaries

This sketch emphasizes ownership rather than enumerating every direct import:

```text
cmd/orpheus -> cli (composition, commands, terminal I/O)
                 -> workflow (progression, decisions, effect coordination)
                      -> review / agent / agentexec (specialized execution)
                      -> task contracts / Git and PR boundaries
                      -> taskstate (typed mutations and service projections)
                      -> workstate (validated load/save)
                           -> work (history and invariants)
                           -> state (generic storage and locks)
                 -> registry -> task.RepositorySource
                 -> tasksource/beads or tasksource/gig -> task contracts
                 -> status / taskstats / doctor (operator projections and diagnostics)
```

Production package imports are acyclic. `internal/work` has no project-package dependencies or I/O. `workstate` imports `work` and generic `state`, never workflow or CLI. `taskstate` consumes that persistence boundary rather than maintaining a second state format. Workflow and specialized executors do not import Cobra or CLI. Source-specific adapters are selected in composition, not imported by workflow, readiness, or status policy.

The layering is pragmatic, not strict dependency inversion: application services use concrete Git request/result types behind consumer interfaces, and configuration/profile services use registry and state infrastructure. Status imports workflow for its shared continuation view. Role-specific agent preparation belongs to `agent.PreparedExecution`; workflow and review reserve its selected facts, while callers supply streams and observation callbacks. Process execution and usage capture share `agent.Execute`.

## Package Responsibilities

### `cmd/orpheus`

- Provides the process entry point, executes the root Cobra command, prints terminal errors, and maps command failure to a non-zero process exit.

### `internal/agent`

- Integrates configured coding-agent profiles by loading and validating implementer and reviewer profile configuration, interpolating raw command arguments, and constructing structured Codex launch commands.
- Owns `PreparedExecution` for implementation, reviewer, and conflict-resolution launches, including profile selection, eligible repair-session selection, purpose-specific environment, and launch assembly. Prepared command, prompt, session, and usage baseline remain unchanged between reservation and launch; persistence and phase-success decisions stay with workflow/review.
- Owns the agent-facing execution contract: it resolves active implementation or review context from environment, registry, backend, task-state, and execution-target facts; renders backend-neutral prompts; and records idempotent implementation completion handoffs.
- Captures agent execution telemetry where supported, currently by correlating Codex and Pi session logs, records Pi-reported estimated cost when present, and estimates API-equivalent cost for recognized non-Pi models.

### `internal/agentexec`

- Defines the harness-neutral process execution contract used by implementation, review, and conflict-resolution agents.
- Runs attached children with caller-supplied streams, environment, directory, and cancellation; reports direct-child exit observations after reaping and draining output.
- Separates process startup and observed exit from best-effort usage capture and application success. It does not choose profiles, workflow progression, or publication.

### `internal/tasksource/{beads,gig}`

The parent directory is not a Go package; shared models and contracts remain in `internal/task`.

- Beads adapts `bd` reads, mutations, metadata, relationships, and local or managed workspaces while sanitizing its command environment.
- Gig uses the pinned public SDK and managed SQLite storage, not a Gig executable or adapter-owned SQL/schema machinery. The SDK owns native lifecycle effects and schema upgrades.
- Both translate native facts into source-neutral contracts and preserve unrelated metadata. Cross-repository dependency and parent links live on the consuming task or child, without proxy tasks or a second relationship store.
- Multi-call source operations and source/history writes are not transactions; partial outcomes require explicit inspection and retry. See [task sources](task-sources.md).

### `internal/cli`

- Defines the Cobra command tree and operator-facing and agent-facing commands.
- Composes invocation-scoped dependencies explicitly, captures environment and paths, selects adapters, and supplies execution streams and frontend callbacks.
- Owns terminal tables, workflow phase/step headers, manual context, rolling output tails, input gates, watch behavior, help, and diagnostic/output separation. It renders semantic views and submits typed decisions; the controller selects lifecycle progression.

### `internal/doctor`

- Runs local, cross-repository diagnostics over registered repositories and Orpheus-owned task state, returning structured outcomes for CLI rendering.
- Owns safe repair policy for recoverable local facts. The current diagnostic re-correlates missing Codex and Pi execution usage from local session logs and persists only unique or safely disambiguated implementation, review-agent, or terminal sync-conflict matches when explicitly requested.

### `internal/git`

- Adapts repository inspection, branches/worktrees, ordinary commits, retained refs, publication packaging, synchronization, integration, pushes, and cleanup.
- Prepares the task branch before implementation in both directory modes. Captures output with normal hooks/signing and retains actual commit/tree identities for review and recovery.
- Separates retained-object verification from checkout admission. Admission checks expected branch/commit and ordinary Git status, respecting repository configuration and ignored build output.
- Performs destructive review/publication recovery only through explicitly authorized application paths. Native non-force worktree removal preserves unsafe or dirty operator work.

### `internal/logging`

- Constructs structured application-diagnostic loggers and a safe discard implementation. Terminal verbosity and opt-in file severity are independent; neither sink captures agent transcripts or raw child output.
- Owns file event rendering and append-only writes to `$XDG_STATE_HOME/orpheus/orpheus.log` (default `~/.local/state/orpheus/orpheus.log`). It opens/closes per event, so commands need no shutdown hook. File-storage failures warn once and disable that sink without failing the operation or disabling terminal diagnostics.
- CLI owns loading the shared logging configuration and resolving the optional file destination; isolated evaluations skip normal file logging. Rotation and cleanup remain operator-managed.

### `internal/publication`

- Loads shared publication defaults and validates integration flows and title policy, including supported placeholders and external-reference requirements.
- Resolves PR draft defaults and renders title policy; workflow owns the accepted per-publication choices and their effects.

### `internal/pullrequest`

- Defines provider-neutral PR requests and lifecycle observations and adapts them through `gh`.
- Supports branch lookup, initial creation with selected draft mode, content updates, and lifecycle polling with actionable diagnostics. Recovery does not change an existing PR's draft/ready status.
- Does not model external review comments, threads, or their triage.

### `internal/readiness`

- Evaluates backend-neutral readiness policies that are shared by dispatch and status projection. The current policy gates a child task on the state and type of its immediate parent epic and explains blocked or inconsistent relationships.

### `internal/registry`

- Persists repository identity, paths, task-source kind/storage/prefix, Git defaults, branch/publication policy, PR mode and review-process overrides, and review-pipeline selection and aliases.
- Resolves deterministic local resources and translates persisted `registry.Repo` records into validated, source-neutral `task.RepositorySource` values through `TaskRepositorySource` and `TaskRepositorySources`.

### `internal/review`

- Loads and validates ordered check, manual, and agent-review pipelines with a built-in manual fallback.
- Executes steps and records actual results/findings through typed state ports. Semantic notifications and raw output ports leave terminal formatting in CLI.
- Requires a clean checkout at the identified retained revision before and after steps. A mutation fails review and preserves the changed checkout; a durable recovery choice blocks further review, repair, or publication until explicitly handled.
- Confirmed rollback restores the disclosed branch/commit and Git-visible changes, verifies success, and leaves the failed review as evidence. EOF, cancellation, and noninteractive input cannot authorize restoration.

### `internal/state`

- Resolves and confines XDG-backed configuration and data paths, creates owned directories, and provides strict YAML reads plus atomic YAML replacement for package-owned state.
- Separately resolves the XDG state root for optional diagnostics without moving existing config/data locations or creating storage during resolution.
- Provides the fail-fast global mutation lock used to serialize state-changing repository, dispatch, completion, synchronization, and finalization operations.

### `internal/status` and `internal/taskstats`

- Status combines task-source snapshots and local history into the shared operator queue/inventory, retaining relationship context and partial repository failures. Workflow-owned tasks use the controller's next-action view before task-wide metadata.
- Task statistics derive throughput, implementation/review outcomes, model cohorts, execution usage, and estimated costs from source facts and projected history. Unknown measurements remain distinct from zero.

### `internal/task`

- Defines source-neutral task/repository models, metadata, lifecycle statuses, relationships, and narrow read/mutation capabilities.
- Resolves prefixed IDs and aggregates reads while preserving source ownership, relationship context, and partial failures. Shared authoring/relationship services validate cross-repository dependencies, epic membership, and activation/completion waits.

### `internal/taskstate`

- Coordinates typed workflow mutations and derives transient run, review, event, and finalization views for application services and presentation.
- Records callbacks and observations at exact workflow/phase/step or branch-sync addresses. A handoff does not imply successful exit, captured output, review, or consent.
- Delegates authoritative persistence to `workstate`; flattened views and their aggregate statuses are never a second persisted authority.

### `internal/tasktarget` and `internal/taskbranch`

- Task-target policy owns reusable execution-target identity, expected-target calculation, and reconciliation shared by workflow, agent, CLI, and status consumers.
- Task-branch policy validates and renders branch templates. Established branch identity survives later template changes; Git owns actual branch preparation and inspection.

### `internal/work`

- Defines the I/O-free ownership tree and snapshot/successor invariants. Workflows contain append-only typed phase occurrences; only review phases contain configured step attempts.
- Separates process observations, completion, output, review scope/results, finding dispositions, publication selection/consent/delivery, PR outcomes, and source closure.
- Uses validated parent/index references rather than generated-ID registries or a persisted cursor. Captured contracts and known historical facts cannot be rewritten by a successor.

### `internal/workstate`

- Owns the authoritative workflow document at `repos/<repo-id>/tasks/<task-id>.yaml`, using serialization format 2 and an independent persistence conflict version.
- Strictly decodes closed alternatives and required addresses, validates snapshots and successors against independently loaded history, and saves with a document lock, expected-version check, and atomic replacement.
- Rejects unsupported documents without converting, deleting, or rewriting operator data.

### `internal/workflow`

- `Controller` owns task-run progression, options, phase reservation, review/repair budgets, typed responses, publication, and reset scheduling. `Inspect` is read-only; `Progress` is synchronous and separate from process streams.
- Specialized dispatch, implementation-capture, review, delivery, and sync services coordinate effect/store ports. Review does not decide to publish, and delivery does not close the source.
- Sync reconciles PR outcomes and branch maintenance, executes delivery-qualified finalization, and retries source closure and local cleanup without repeating delivery.

## Durable Design Decisions

The current CLI is the only planned frontend, but application behavior must remain independent of Cobra and terminal I/O so that other frontends can be added later. Frontend-specific parsing, prompting, and rendering belong in `internal/cli`; reusable use-case transitions belong in application packages.

The built-in phases are Preparation, Implementation, Review, Publication, External review, and Finalization. Current work is derived from containment and the last occurrence, not a separately persisted cursor. Repairs stay in one workflow; reset appends another. This is an explicit sequential controller, not an arbitrary configurable workflow graph. Non-code outputs, PR import/comment automation, and a general repair-and-republish loop are not implemented by this model.

Review contracts retain the started slots and captured requirements. Continuation resolves the current pipeline and step by name/type against live configuration, preserving the started prefix and replacing only forward requirements. Earlier insertions are not retrospectively executed. Missing positions return configuration-repair advice. A repair starts a fresh review at the first step. See the [work model](work-model.md) for reference and transition details.

Review approval and publication consent are different facts. Publication can accept clean operator-selected committed output after disclosing its difference from reviewed output; it records that gap rather than fabricating a passed review. Controller-produced implementation/repair and review admission keep their own requirements. The destination branch is fixed at initial review; its observed tip may advance without retargeting the workflow.

Publication content belongs to the workflow, distinct from each effort's local commit message and technical explanation. The first explicit proceed locks that content. Accepted intent also binds actual input, review evidence/provenance, flow, destination, and PR draft mode. Configuration supplies new choices but does not rewrite retained consent or in-flight operation facts. Draft mode is a creation choice, not permission to convert an existing PR.

Both directory modes package public history in place once, using ordinary hooks/signing. Intent and recovery input are durable before rewriting; the public result is recorded before remote writes. PR delivery releases obsolete private refs only after durable delivery; direct delivery releases them after durable public preparation. Historical identities remain inspectable without promising perpetual reachability of released private objects. Changed hook output or direct-sync output requires fresh consent, not invented review evidence.

Task run stops at delivery. PR work then waits in External review; direct delivery skips that phase. Sync consumes independent acceptance evidence, records closure intent, reconciles the authoritative source, and records actual closure. Cleanup is a separately retryable consequence. Existing PR synchronization remains branch maintenance, not an implicit task-review or new-publication loop.

Explicit reset authorization schedules deferred destructive Preparation, not an immediate checkout reset. Preparation selects the fetched default-branch baseline, records its target before effects, and consumes authorization only after durable establishment. Native hard-reset/untracked-cleanup semantics apply, with no automatic stash or unfinished-work backup; ignored obstructions may be overwritten by baseline restoration. Recorded history is preserved.

One active controller per task and deliberate coordination of manual changes are operator responsibilities. Existing reservation, callback, and persistence checks protect Orpheus-owned sequencing; they are not a promise to support concurrent controllers or arbitrary external interference. Attached work releases the progression lock so child callbacks can record their own facts. External effects and YAML are not one transaction: intent, observations, and bounded reconciliation provide recovery without an exactly-once claim.

Task sources retain lifecycle authority. Beads and Gig adapters own native translation and storage behavior; application policy consumes shared contracts. Only the current workflow-state shape is supported. Unsupported records require explicit operator handling that preserves needed history; there is no automatic migration or permission to discard data on load failure.

Dependencies are wired manually once per invocation. Preserve explicit constructor/interface wiring rather than introducing a dependency-injection framework. Existing tests protect application/CLI and registry/task import direction, domain/persistence contracts, real adapter boundaries, and memory-backed CLI journeys; see [testing](testing.md).
