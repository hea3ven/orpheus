---
description: Review and refine a bead and its children, focusing on completeness, architecture decisions, interfaces, and dependencies
argument-hint: "<bead-id>"
---
Review bead $1 and its children, focusing on completeness, architecture decisions, module boundaries, interfaces, and dependency graph correctness.

Scope:
- If bead $1 is a task, review that bead alone.
- If bead $1 is an epic with children, review each child task one by one. Fully flesh out the details for one task and reach shared understanding before moving on to the next task.
- If bead $1 is an epic without children, first reach shared understanding of the child tasks that need to be created. After I approve creating those tasks, review each newly proposed/created task one by one until each is complete, actionable and has the correct dependencies.
- Evaluate whether each reviewed bead has enough context to be actionable.
- Focus on product decisions, module responsibilities, interfaces, data flow, ownership, sequencing, and dependencies.
- Once shared understanding is reached and I approve the proposed changes, update only the bead contents/descriptions. Do not claim, assign, prioritize, close, defer, or change bead status.

Avoid:
- Asking about low-level implementation details such as specific files, line numbers, exact edits, or mechanical coding steps.
- Adding low-level implementation details to beads.
- Turning beads into step-by-step coding checklists.
- Asking questions whose only purpose is to determine implementation mechanics.

Process:
- Grill me on any architecture or product decision that must be explicit before implementation can proceed.
- Challenge assumptions. If a bead appears too broad, too vague, incorrectly sequenced, or coupled to the wrong module, call that out directly and suggest a cleaner decomposition.
- For epics with existing children, do not move to the next child task until the current task's purpose, boundaries, interfaces, dependencies, and acceptance criteria are clear.
- For epics without children, first propose the child task breakdown and get approval before creating or updating any beads. Then review each child task using the same one-at-a-time refinement process.
- Present proposed bead creations/updates for approval before applying them.

Output:
- Provide a detailed summary of the review, including completeness gaps, missing decisions, interface/module boundary concerns, dependency graph issues, and sequencing risks.
- After approval, update the relevant bead contents/descriptions and summarize what changed.
