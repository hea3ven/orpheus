---
name: architecture-review-planning
description: Run a recurring repository architecture review from current documentation and code through decision-making and approved Beads planning. Use this skill whenever the user asks to repeat, refresh, continue, document, or plan an architecture review, assess package responsibilities or dependencies, compare architecture progress with a prior review, or turn architecture findings into Beads. Always begin by finding the latest prior architecture review and verifying progress against current code, docs, and Beads.
---

# Architecture Review And Planning

Run a rigorous, evidence-based architecture review and convert approved findings into durable documentation and actionable Beads. Treat the repository's implemented code as the source of truth, current architecture documentation as the intended description, and dated review documents as historical baselines.

Do not skip the prior-review progress assessment. A recurring review must explain what changed since the last review before proposing new work.

## Hard Boundaries

- Read and follow the repository `AGENTS.md` files before reviewing or editing.
- Do not use `bd ready` or backlog views to select work.
- Search Beads only for prior-review items, related context, and duplicates.
- Do not create, update, close, defer, reprioritize, or add dependencies to a Bead until the user explicitly approves the exact operation and content.
- Keep parent-child links structural. Add a sequencing dependency only when later work cannot be implemented or validated first.
- If A must complete before B, run `bd dep add B A`, then verify from B with `bd show B --json` that A has `dependency_type: "blocks"`.
- Do not put planning tables or transient status in the canonical architecture document. Keep those in a dated architecture-review report.
- Prefer small, reversible package moves over rewrites or pattern adoption without a concrete driver.
- Do not treat file size, task status, or documentation claims alone as evidence that architecture is correct.

## Workflow

### 1. Review The Previous Architecture Review

This is always the first substantive step.

1. Find dated reports under `docs/arch-review/` and select the newest review that predates the current review run, including an earlier report from the same date when applicable. If none exists, state that this is the baseline review.
2. Read the complete prior report, especially:
   - architecture baseline;
   - findings and decisions;
   - proposed tasks and affected packages;
   - Bead IDs and dependency decisions;
   - deferred questions and the next-review checklist.
3. Inspect every referenced Bead directly with `bd show <id> --json`. Do not infer progress from a backlog summary.
4. Verify the implementation in current code, tests, package imports, and documentation. A closed Bead is not sufficient evidence, and an open Bead may contain partial implementation.
5. Classify each prior item:
   - **Implemented**: intended ownership and boundary are present and tested.
   - **Partially implemented**: meaningful progress exists, but acceptance or dependency direction is incomplete.
   - **Not started**: intended architecture is not present.
   - **Superseded**: another decision or implementation replaced the recommendation.
   - **No longer valid**: assumptions changed and the recommendation should be retired.
6. Record evidence and explain architectural impact. Identify stale Beads or docs, but do not modify them without approval.
7. Summarize progress to the user before beginning the new review.

### 2. Establish The Current Baseline

Read:

- root and scoped `AGENTS.md` files;
- `docs/ARCHITECTURE.md` and relevant ADRs, PRDs, retrospectives, and workflow docs;
- repository entry points and package list;
- production import relationships;
- package-level tests around important boundaries;
- Git status and the relevant branch or working-tree diff;
- external process and persistence integration points.

Capture:

- runtime flow and external integrations;
- package responsibilities and dependency direction;
- domain and persisted models;
- data ownership and state transitions;
- composition and cross-cutting concerns;
- likely axes of evolution, including future frontends and adapters;
- large files only as signals requiring ownership analysis.

State the exact scope reviewed and important exclusions.

### 3. Assess Architecture

Evaluate each package and boundary through these lenses:

- responsibility and cohesion;
- coupling and dependency direction;
- domain model fit and vocabulary;
- interface ownership and contract size;
- persistence and state authority;
- lifecycle orchestration ownership;
- adapter ownership for external tools;
- frontend and framework leakage;
- configuration, logging, errors, and concurrency;
- test architecture and executable boundary checks;
- reversibility and cost of likely future changes.

For every finding, provide concrete evidence, impact, a recommendation, and trade-offs. Offer alternatives for substantial changes. Avoid style findings unless they expose unclear ownership or coupling.

### 4. Resolve Decisions With The User

Maintain a decision map with:

- settled decisions;
- open questions;
- assumptions;
- affected findings and packages.

Ask one focused question at a time when decisions depend on one another. Give a recommended answer and explain why. Resolve expected evolution, ownership, compatibility, sequencing, non-goals, and acceptable effort before planning tasks.

Examples of decisions that commonly matter:

- future frontend expectations;
- future task or integration sources;
- canonical repository and task models;
- lifecycle orchestration owner;
- schema compatibility and migration policy;
- interface ownership;
- explicit composition versus a framework;
- which low-effort work should happen now and which larger work should be deferred.

### 5. Map Findings To Existing Beads

Before proposing new Beads:

1. Inspect related open, in-progress, and recently closed epics and tasks.
2. Determine whether each finding:
   - is already resolved;
   - belongs in an existing task's scope or acceptance criteria;
   - needs a new child under a genuinely related epic;
   - needs a standalone task;
   - should remain a documented decision with no task.
3. Do not place work under an unrelated epic merely to schedule it earlier.
4. Avoid standalone horizontal tasks for tests, documentation, or file splitting when they can protect a cohesive functional or architectural change.
5. Present a table sorted by effort (`XS`, `S`, `M`, `L`, `XL`) with:
   - finding or task;
   - criticality;
   - affected packages;
   - reason;
   - recommended Bead placement;
   - dependency or sequencing implications.

Do not modify Beads during this mapping stage.

### 6. Plan Beads One At A Time

After the user approves placement decisions, fully define each resulting Bead or update.

For each item, specify:

- exact title and type;
- parent epic, if any;
- priority, effort, and criticality;
- affected packages;
- problem statement and outcome;
- scope and non-goals;
- package responsibility and dependency decisions;
- sequencing and dependencies;
- behavior-preservation requirements;
- acceptance criteria, including applicable import or adapter boundary checks.

Preserve product acceptance criteria when expanding an existing Bead. Do not let an architecture refinement erase the user-visible outcome that justified the original task.

Ask for explicit approval of the exact content and Beads operation. Only then create or update it. Verify the stored issue and every dependency after writing.

If Beads cannot represent an approved relationship, stop, explain the constraint, recommend the least-distorting alternative, and obtain approval before changing the plan.

### 7. Update Documentation

Create a new point-in-time report under:

`docs/arch-review/YYYY-MM-DD-architecture-review-<phase>.md`

Follow scoped documentation instructions. Never overwrite an older dated report.

Include:

1. purpose, scope, date, branch or revision, and method;
2. progress assessment against the previous review;
3. current architecture and dependency/data-flow summary;
4. strengths worth preserving;
5. findings ordered by severity with evidence and decisions;
6. decisions and assumptions resolved with the user;
7. immediate code or documentation changes completed;
8. proposed architecture tasks sorted by effort and criticality;
9. Beads created, updated, closed, or intentionally not created;
10. verified dependency relationships and unrepresentable sequencing notes;
11. validation performed and limitations;
12. a concrete checklist for the next architecture review.

Update `docs/ARCHITECTURE.md` only when current implemented architecture or a durable evolution decision changed. Do not add a roadmap, status table, Bead list, or dated planning narrative to it.

### 8. Validate And Report

For code or documentation changes, run the repository-required validation commands. For this repository that means:

```bash
make fmt
make test
make lint
```

Also run focused checks appropriate to the review artifacts, such as skill validation, Markdown inspection, import-graph checks, and `git diff --check`.

Finish with:

- previous-review progress summary;
- highest-impact current findings;
- decisions made;
- Bead operations and verified dependencies;
- files changed;
- checks run and any limitations.

## Review Quality Checks

Before completing the work, verify:

- Every package-movement recommendation has a clear new owner.
- Every dependency recommendation points toward the more stable abstraction.
- No recommendation duplicates an existing Bead without explaining why.
- No task was created without exact approval.
- Task dependencies use the blocked-to-prerequisite direction.
- Acceptance criteria test outcomes and boundaries, not arbitrary line counts.
- The dated report separates current implementation from proposed architecture.
- The next review can identify all Beads and decisions without reconstructing the conversation.
