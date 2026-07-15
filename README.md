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

- [Review pipeline workflow](docs/2026-07-05_review_pipeline_workflow.md) explains the operator path from `task run` through `agent done`, `task review`, follow-up work, approval, and publication/finalization retry.
- [Repository publication titles](docs/2026-06-23_repo_publication_titles.md) explains how to configure Jira-style commit and pull-request titles, preserve defaults, and recover from a missing task reference.
- [Review pipelines](docs/review_pipelines.md) explains automatic review after `task run`, manual gate resumption, global pipeline configuration, repository defaults, repo-local aliases, clearing behavior, and selection precedence.

## Agent profiles

Task-run agent profiles can interpolate `{{session_name}}` anywhere `{{prompt}}` is supported. Orpheus formats the value as `(<task_id>) <task title>`, or `(<task_id>)` when the task has no title.

Structured Codex profiles let Orpheus build the launch command and capture Codex usage telemetry:

```yaml
agents:
  defaults:
    implementer: codex-medium
    reviewer: codex-review
    sync_conflict_resolver: codex-sync
  profiles:
    codex-medium:
      harness: codex
      model: gpt-5.4
      thinking: high
      interactive: true
    codex-review:
      harness: codex
      model: gpt-5.4-mini
      interactive: false
    codex-sync:
      harness: codex
      model: gpt-5.4-mini
      interactive: false
```

Interactive Codex profiles launch `codex --model <model> --dangerously-bypass-approvals-and-sandbox "{{session_name}} - {{prompt}}"`. Non-interactive profiles launch the same command through `codex exec`. When `thinking` is set, Orpheus adds `-c model_reasoning_effort=<thinking>` to the Codex command.

`agents.defaults.sync_conflict_resolver` is optional. When set, `orpheus task sync <task-id>` and `orpheus task sync --all` use that profile for merge-conflict repair while syncing open PR branches. When it is unset, sync conflict repair falls back to `agents.defaults.implementer`, preserving existing configs.

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

Raw command profiles remain generic, even when they invoke `codex`. Orpheus runs the configured command exactly and does not infer Codex model, launch mode, or telemetry support from raw args:

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

Structured Pi profiles let Orpheus launch Pi with native session naming and recover Pi session telemetry:

```yaml
agents:
  defaults:
    implementer: pi-codex
    reviewer: pi-review
  profiles:
    pi-codex:
      harness: pi
      model: openai-codex/gpt-5.5
      thinking: high
      interactive: true
    pi-review:
      harness: pi
      model: openai-codex/gpt-5.4-mini
      interactive: false
```

Interactive Pi profiles launch `pi --model <model> --thinking <thinking> --name "{{session_name}}" "{{prompt}}"`. Non-interactive profiles add `--print`. Orpheus correlates supported Pi executions with JSONL sessions under `PI_CODING_AGENT_SESSION_DIR`, `PI_CODING_AGENT_DIR/sessions`, or `~/.pi/agent/sessions`, matching by cwd, session name when Pi recorded it, and execution start time.

`orpheus task stats` reports Pi assistant-message token usage from the matched session: input, cached input, output, reasoning output, and total tokens. When Pi records `usage.cost.total`, Orpheus stores and reports that value as `pi_reported_estimated`. This is a Pi-reported estimate only, not exact billing or invoice reconciliation. If Pi usage or reported cost is missing, stats keep the value unknown rather than treating it as zero.

`orpheus doctor` checks supported harness telemetry for existing task state. With `--fix`, it repairs missing Codex or Pi usage only when exactly one safe session correlation exists, or when the closest match is clearly safe. Ambiguous Pi or Codex matches remain unresolved and show candidate counts.

Use raw profiles for custom launch contracts. Use structured `harness: codex` or `harness: pi` profiles when task stats should attempt session and token capture.

## License

MIT. See [LICENSE](LICENSE).
