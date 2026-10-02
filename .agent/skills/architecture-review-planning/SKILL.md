---
name: architecture-review-planning
description: Run a recurring repository architecture review from current documentation and code through decision-making and approved Beads planning. Use this skill whenever the user asks to repeat, refresh, continue, document, or plan an architecture review, assess package responsibilities or dependencies, compare architecture progress with a prior review, or turn architecture findings into Beads. Begin with the latest review report on the docs branch, verify progress against current code and Beads, update docs/developer/architecture.md on main, and write a new dated review report on docs.
---

# Architecture Review And Planning

Run an evidence-based architecture review and convert approved findings into durable documentation and actionable Beads. Treat implemented code as the source of truth. Maintain two distinct artifacts:

- `docs/developer/architecture.md` on `main` describes the implemented architecture and durable design decisions.
- `docs/arch-review/YYYY-MM-DD-architecture-review-<phase>.md` on `docs` records each review's findings, decisions, Beads, validation, and next-review checklist. It is a review and planning record, not a copy of the architecture document.

Each review starts by checking progress against the previous report. Read the archive for that comparison without treating its older descriptions as current specifications.

## Hard Boundaries

- Read and follow the repository `AGENTS.md` files before reviewing or editing.
- Do not use `bd ready` or backlog views to select work.
- Search Beads only for prior-review items, related context, and duplicates.
- Do not create, update, close, defer, reprioritize, or add dependencies to a Bead until the user explicitly approves the exact operation and content.
- Keep parent-child links structural. Add a sequencing dependency only when later work cannot be implemented or validated first.
- If A must complete before B, run `bd dep add B A`, then verify from B with `bd show B --json` that A has `dependency_type: "blocks"`.
- Keep verified architecture and durable decisions in the architecture reference on `main`. Keep findings, planning tables, Bead lists, and review-specific status in the dated report on `docs`.
- Use separate worktrees for the two outputs. Do not switch the operator's current checkout, merge source branches into the archive, or overwrite existing reports. Commits and pushes still require explicit approval.
- Orpheus is an unreleased, single-user MVP. Do not introduce backward-compatibility layers, migrations, or legacy support without an explicit requirement.
- Prefer small, reversible package moves over rewrites or pattern adoption without a concrete driver.
- Do not treat file size, task status, or documentation claims alone as evidence that architecture is correct.

## Workflow

### 1. Review the previous architecture review

This is the first substantive step after reading repository instructions.

1. Find reports with `git ls-tree -r --name-only refs/heads/docs -- docs/arch-review`. Select the newest report that predates this review, including an earlier report from the same date. If the branch is unavailable, ask the operator for access rather than silently skipping the comparison. If the branch contains no report, state that this is the baseline review.
2. Read the complete report with `git show refs/heads/docs:<report-path>`. Include its findings, decisions, affected packages, Bead IDs, dependencies, deferred questions, and next-review checklist.
3. Inspect referenced Beads directly with `bd show <id> --json`. Do not infer progress from a backlog summary or assume that a closed Bead proves the intended boundary exists.
4. Verify each prior item against current code, tests, imports, and `docs/developer/architecture.md`. Classify it as implemented, partially implemented, not started, superseded, or no longer valid, with evidence and architectural impact.
5. Summarize progress before proposing new work. Identify stale Beads without modifying them unless the user approves the exact operation.

Use `git worktree list --porcelain` to locate the `main` and `docs` checkouts, not
assumed directory names. If the archive worktree contains a newer uncommitted
report, ask whether it should be the comparison baseline instead of silently
ignoring it.

### 2. Inspect the current implementation

Read:

- root and scoped `AGENTS.md` files;
- `docs/developer/architecture.md`, `CONTEXT.md`, and relevant current guides from `docs/README.md`;
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

Produce both outputs, following the instructions in their respective worktrees:

1. Update `docs/developer/architecture.md` in the `main` worktree with verified
   implementation changes and agreed durable design decisions. Keep it undated,
   preserve unreviewed sections, and exclude task plans and review-specific status.
2. Create a new `docs/arch-review/YYYY-MM-DD-architecture-review-<phase>.md` in
   the `docs` worktree. Use a distinct phase or suffix for multiple reviews on the
   same date; never overwrite an earlier report.

Confirm each worktree's branch and status before writing, and preserve unrelated
changes. If either worktree is unavailable, ask the operator to provide it or
approve creating one. If the reviewed code is on another branch, agree on how to
apply the canonical update to `main`; do not describe unmerged changes as already
implemented there. Never switch the active checkout or merge code into `docs` to
store a report.

Writing the report is part of the requested review. Leave both outputs uncommitted
unless the operator explicitly requests commits or pushes. Report their paths and
pending status so the archive report can be committed before the next review.

Include in the dated report:

1. purpose, scope, date, reviewed branch and revision, and method;
2. previous report path/revision and evidence-backed progress assessment;
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

### 8. Validate And Report

Follow the validation scope in `AGENTS.md`. Run `make check` when the review changes code, tests, dependencies, executable scripts, or build/test configuration. Do not run tests, quality checks, formatting, linting, or builds for documentation-only edits or read-only analysis unless explicitly requested.

For documentation changes, check only the affected documents, such as links and `git diff --check`. Inspect imports when needed as review evidence, not as a reason to run the validation suite.

Finish with:

- previous-review progress summary;
- highest-impact current findings;
- decisions made;
- Bead operations and verified dependencies;
- architecture reference and dated report paths, their branches, and uncommitted status;
- other files changed;
- checks run and any limitations.

## Review Quality Checks

Before completing the work, verify:

- Every package-movement recommendation has a clear new owner.
- Every dependency recommendation points toward the more stable abstraction.
- No recommendation duplicates an existing Bead without explaining why.
- No task was created without exact approval.
- Task dependencies use the blocked-to-prerequisite direction.
- Acceptance criteria test outcomes and boundaries, not arbitrary line counts.
- The maintained architecture document describes verified implementation and agreed durable decisions, not unimplemented proposals.
- The new report on `docs` separates verified implementation from proposed work and records the prior-review progress assessment.
- The next review can identify all findings, Beads, decisions, and follow-up checks from that report.
- The architecture reference remains on `main`; dated review reports remain on `docs`.
