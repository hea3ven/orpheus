# Orpheus

**Orpheus brings structure, visibility, and control to AI coding-agent orchestration.**

Orpheus is a CLI-first orchestration layer for coordinating AI coding agents across tasks, worktrees, and pull requests while keeping the human operator in control.

Inspired by the mythic Orpheus charming wild forces into motion, this project focuses on taming scattered agent runs into a predictable development workflow.

## What Orpheus does

- Coordinates coding-agent work from task to PR
- Creates deterministic branches and worktrees
- Tracks agent runs across repositories
- Keeps humans in control of decisions, review, and merges
- Prioritizes visibility and operational safety over unchecked autonomy

## Status

Early MVP design and implementation planning.

## Documentation

- [Repository publication titles](docs/2026-06-23_repo_publication_titles.md) explains how to configure Jira-style commit and pull-request titles, preserve defaults, and recover from a missing task reference.

## Agent profile session names

Task-run agent profiles can interpolate `{{session_name}}` anywhere `{{prompt}}` is supported. Orpheus formats the value as `(<task_id>) <task title>`, or `(<task_id>)` when the task has no title.

Pi-style native naming:

```yaml
agents:
  defaults:
    implementer: pi
  profiles:
    pi:
      command: pi
      args:
        - --name
        - "{{session_name}}"
        - "{{prompt}}"
```

Codex-style prompt prefix:

```yaml
agents:
  defaults:
    implementer: codex
  profiles:
    codex:
      command: codex
      args:
        - "{{session_name}} - {{prompt}}"
```

## License

MIT. See [LICENSE](LICENSE).
