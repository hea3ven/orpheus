# Project Instructions for AI Agents

This file provides instructions and context for AI coding agents working on this project. Keep it aligned with `AGENTS.md`.

## Task Tracking and Follow-Up

This project does **not** use agent-driven task management for the MVP workflow.

Agents should not manage the project task lifecycle. In normal work, do **not**:

- Pick work from `bd ready` or other backlog views.
- Claim, assign, prioritize, update, close, defer, or otherwise manage beads tasks.
- Treat beads as an agent TODO list for transient implementation steps.
- Run `bd dolt push` as part of session cleanup.

Use **bd (beads)** only to record follow-up work that should persist beyond the current session, such as:

- A bug or gap discovered while working that will not be fixed now.
- A user-requested follow-up item.
- A decision, chore, or investigation that needs explicit tracking later.

When creating follow-up beads, keep them concise and include enough context for a human to decide how to schedule them. Prefer:

```bash
bd create "Short follow-up title" --description "Context, relevant files, and why this needs follow-up" --type task
```

Humans own task selection, prioritization, assignment, and closure unless they explicitly ask the agent to perform a specific beads operation.

## Commits and Pushes

Agents should not commit, pull, rebase, push, or otherwise publish work unless explicitly asked by the user.

At the end of a session, report what changed and which checks were run. If checks were not run, say so. Do not treat work as incomplete merely because it has not been committed or pushed.

## Non-Interactive Shell Commands

**ALWAYS use non-interactive flags** with file operations to avoid hanging on confirmation prompts.

Shell commands like `cp`, `mv`, and `rm` may be aliased to include `-i` (interactive) mode on some systems, causing the agent to hang indefinitely waiting for y/n input.

**Use these forms instead:**
```bash
# Force overwrite without prompting
cp -f source dest           # NOT: cp source dest
mv -f source dest           # NOT: mv source dest
rm -f file                  # NOT: rm file

# For recursive operations
rm -rf directory            # NOT: rm -r directory
cp -rf source dest          # NOT: cp -r source dest
```

**Other commands that may prompt:**
- `scp` - use `-o BatchMode=yes` for non-interactive
- `ssh` - use `-o BatchMode=yes` to fail instead of prompting
- `apt-get` - use `-y` flag
- `brew` - use `HOMEBREW_NO_AUTO_UPDATE=1` env var

## Build & Test

_Add your build and test commands here_

```bash
# Example:
# npm install
# npm test
```

## Architecture Overview

_Add a brief overview of your project architecture_

## Conventions & Patterns

_Add your project-specific conventions here_
