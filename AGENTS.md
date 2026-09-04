# Agent Instructions

## Validation

Run `make check` for complete validation at the end of a change. It runs formatting, the unit lane once, the integration lane once, and linting.

`make test-unit` is the explicit package-owned unit lane; `make test` remains its compatibility alias. Unit tests use injected collaborators and require only Go: they do not invoke Git, Beads, gh, Codex, or Pi executables. `make test-integration` executes only cross-package workflows and isolated local Git, Beads, compiled-CLI, or child-process contracts. It requires `git` and `bd` on `PATH` (some schema tests also skip unless `dolt` is available).

Integration test sources use `//go:build integration`, and their top-level test functions begin with `TestIntegration`; untagged non-prefixed tests are units. The convention validator rejects any top-level body that would be omitted from or selected by both lanes. Build constraints, not the `-run` filter alone, assign lane membership. Both lanes are network-free, credential-free, isolated from operator data, and prevent real model agents; using temporary disk alone does not make a test an integration. Live evaluations such as `orpheus eval review-context` are not routine test validation and must never be invoked implicitly. See `docs/testing.md` for the full guide.

After making changes, run `make quality`. If it reports `policy_update_required` or an intentional bound violation, run `make quality-policy-update` and review the `.quality.yml` diff.
When merging, resolve `.quality.yml` from the reviewed policy changes rather than combining measured values. Run `make quality-policy-update` only when the merged result requires it.

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
