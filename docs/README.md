# Documentation

Orpheus is an unreleased, single-user MVP. These guides describe the implemented
workflow, not a stable public interface. Keep them concise and update them in
place; complete release documentation can wait until the first release.

## User guides

| Guide | Contents |
| --- | --- |
| [Tasks](user/tasks.md) | Repositories, task views, execution, branch names, stats, and recovery. |
| [Reviews](user/reviews.md) | Completion, approval, pipeline configuration, findings, and repairs. |
| [Publication](user/publication.md) | Integration flows, destinations, titles, PR content, and sync. |
| [Agent profiles](user/agents.md) | Structured Pi/Codex profiles and custom command launches. |

Build instructions and shell completion remain in the [project README](../README.md).

## Developer guides

| Guide | Contents |
| --- | --- |
| [Architecture](developer/architecture.md) | Package responsibilities, state ownership, and design decisions. Maintained through architecture reviews; check code before relying on implementation details. |
| [Domain glossary](../CONTEXT.md) | Shared project terminology. |
| [Testing](developer/testing.md) | Test ownership, fixtures, isolation, quality policy, and CI. |
| [CLI test isolation](../internal/cli/TESTING.md) | Package-local command and workflow fixtures. |
| [Diagnostic logging](developer/logging.md) | Logging ownership and safety rules. |

## Agent instructions and tools

[AGENTS.md](../AGENTS.md) defines repository working rules;
[CLAUDE.md](../CLAUDE.md) points Claude to those same rules.
[Documentation instructions](AGENTS.md) govern this directory.
The [architecture review prompt](../.pi/prompts/architecture-review.md),
[refinement prompt](../.pi/prompts/refinement.md), and
[architecture review planning skill](../.agent/skills/architecture-review-planning/SKILL.md)
are explicitly invoked tools, not descriptions of product behavior.

Architecture reviews read the previous report from the `docs` branch and verify
progress against current code and Beads. They update the architecture reference
on `main` and write a new dated report under `docs/arch-review/` in the `docs`
worktree. The report records findings, decisions, task plans, and the next-review
checklist; it is not a copy of the architecture reference. Both outputs remain
uncommitted until explicitly approved for commit.

## Historical documents

The `docs` branch preserves product briefs, MVP plans, PRDs, retrospectives,
architecture reviews, validation evidence, and deferred ideas. Those records
are not current specifications and should not be loaded as routine agent context.

Inspect them without switching this checkout:

```sh
git ls-tree -r --name-only refs/heads/docs -- docs performance
git show refs/heads/docs:docs/IDEAS.md
```

The cleanup archive was verified at commit `e3f80e59f39c7c0d15d7a581ad22061ca9c90113`.
Use that revision instead of `refs/heads/docs` if a fixed snapshot is needed.
Do not restore the archived tree wholesale: current guides and implementation
may have changed independently.
