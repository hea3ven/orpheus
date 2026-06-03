---
description: Research and plan a new feature into an actionable bead task or epic
argument-hint: "[feature]"
---
Plan a new feature and create the corresponding Beads issue(s) only after explicit approval.

Feature to plan: $ARGUMENTS

If no feature was provided, first ask me what feature we want to plan. Do not proceed until I answer.

Scope and constraints:
- This prompt is for planning new work, not reviewing or updating existing work.
- Do not pick work from `bd ready`, claim, assign, prioritize, close, defer, or change status.
- Final beads must be actionable and product/architecture focused.
- Avoid low-level implementation details: exact files, line numbers, mechanical coding steps, or step-by-step coding checklists.
- Include enough context for an implementer to understand purpose, boundaries, interfaces, data flow, dependencies, and acceptance criteria.
- Prefer clear outcomes and constraints over implementation prescriptions.

Process:
1. Research project context first.
   - Read relevant docs, README/AGENTS guidance, existing prompts, and related code or packages.
   - Search existing beads only for related context or possible duplicates; do not use backlog views as a TODO list.
   - If a question can be answered by exploring the codebase, explore the codebase instead of asking me.
   - Summarize the discovered context and any assumptions before grilling me.

2. Run a grill-me style scope interview.
   - Interview me relentlessly until we reach shared understanding of the feature.
   - Ask one question at a time.
   - For each question, provide your recommended answer and why.
   - Challenge assumptions directly when scope is vague, too broad, incorrectly sequenced, or coupled to the wrong module.
   - Resolve product behavior, module responsibilities, interfaces, data flow, ownership, sequencing, dependencies, non-goals, and acceptance criteria.

3. Decide task vs epic.
   - After the scope is clear, assess whether this should be a single task or an epic with multiple child tasks.
   - Recommend one option with rationale.
   - Ask me to choose between:
     - Single task: one actionable bead is enough.
     - Epic: create a parent epic and multiple child tasks.
   - Do not create beads until I approve the structure.

4. Refine the proposed bead(s).
   - If single task: refine the task until its purpose, boundaries, interfaces, dependencies, and acceptance criteria are clear.
   - If epic: first propose the child task breakdown and get approval. Then refine each child task one by one; do not move to the next child until the current one is complete and actionable.
   - For each bead, define:
     - Title
     - Type (`task` or `epic`)
     - Description / problem statement
     - Scope and non-goals
     - Product or architecture decisions captured
     - Dependencies and sequencing
     - Acceptance criteria
   - Keep descriptions concise but complete; do not include implementation checklists.

5. Present the creation plan for approval.
   - Show the exact epic/task titles, descriptions, acceptance criteria, parent-child relationships, and dependencies you intend to create.
   - Ask for explicit approval before running any `bd create` command.
   - If I request changes, revise the plan and ask again.

6. Create the approved beads.
   - For a single task, create one bead with `bd create ... --type task`.
   - For an epic, create the parent with `bd create ... --type epic`, then create children with `bd create ... --type task --parent <epic-id>`.
   - Add dependencies with `--deps` only when sequencing is explicit and approved.
   - Use `--description` and `--acceptance` (or `--body-file` if content is long) to preserve the approved content.
   - Do not run `bd dolt push`.

Output:
- Summarize the research context, key decisions, final decomposition, and created bead IDs.
- If no beads were created because approval was not given, summarize the current proposed plan and open questions.
