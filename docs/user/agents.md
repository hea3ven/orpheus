# Agent profiles

Configure profiles in `$XDG_CONFIG_HOME/orpheus/config.yaml`, normally
`~/.config/orpheus/config.yaml`. Profiles select how to launch an agent, not which
task it should perform. Orpheus supplies a bootstrap instruction to read
`orpheus agent context` for the current task and execution constraints.

## Structured profiles

Structured profiles let Orpheus construct the command and capture supported
session usage:

```yaml
agents:
  defaults:
    implementer: codex-implementer
    reviewer: codex-reviewer
    sync_conflict_resolver: codex-reviewer
  profiles:
    codex-implementer:
      harness: codex
      model: gpt-5.4
      thinking: high
      interactive: true
    codex-reviewer:
      harness: codex
      model: gpt-5.4-mini
      interactive: false
    pi-implementer:
      harness: pi
      model: openai-codex/gpt-5.5
      thinking: high
      interactive: true
```

Choose models available through your installed harness and account. `model` is
required for structured profiles. `thinking` is optional. Override the implementer
for one task with `orpheus task run --agent pi-implementer <task-id>`.

Codex profiles launch `codex` interactively or `codex exec` non-interactively,
with `--model` and `--dangerously-bypass-approvals-and-sandbox`. These launches
bypass Codex approvals and sandboxing; use them only in a trusted environment.
When set, `thinking` becomes `-c model_reasoning_effort=<thinking>`.

Pi profiles launch `pi --model <model> --name <session_name> <prompt>`, add
`--thinking <thinking>` when configured, and use `--print` for non-interactive
execution. Orpheus formats session names as `(<task-id>) <task title>`, or just
`(<task-id>)` when no title exists.

`agents.defaults.sync_conflict_resolver` is optional. If unset, open-PR sync
conflict repair uses the implementer profile. The reviewer default applies to
`agent_review` pipeline steps.

## Supplemental instructions

Structured profiles accept literal `prompt_append` text after Orpheus' bootstrap
prompt. One-line and YAML multiline strings are supported; blank values are
ignored. For example, add this field to a structured reviewer profile:

```yaml
prompt_append: |
  Review module boundaries, dependency direction, and data ownership.
  Explain concrete implementation risks rather than style preferences.
```

The effective prompt also appears in `ORPHEUS_AGENT_PROMPT`. Supplemental text
does not replace the current task context or reporting contract.

## Raw commands

Use raw profiles for a custom launch contract:

```yaml
agents:
  defaults:
    implementer: custom
  profiles:
    custom:
      command: pi
      args:
        - --name
        - "{{session_name}}"
        - "{{prompt}}"
```

Raw profiles run the configured executable and arguments without inferring a
harness, model, or telemetry support, even if the command is `pi` or `codex`.
`{{session_name}}` is available wherever `{{prompt}}` is supported. Put custom
instructions in the prompt argument; raw profiles cannot use `prompt_append` or
mix `command`/`args` with structured `harness`/`model` settings.

See [tasks](tasks.md#stats-and-usage) for usage reliability and cost estimates,
and [reviews](reviews.md#optional-follow-up-session-resumption) for opt-in repair
session resumption.
