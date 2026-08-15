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

## Validation

Orpheus has two structurally selected test lanes:

- `make test-unit` runs package-owned unit tests with injected collaborators. `make test` is its compatibility alias. This lane requires only Go; it never requires or invokes Git, Beads, gh, Codex, or Pi executables.
- `make test-integration` runs cross-package workflows and isolated local contracts, including real Git, real Beads, compiled CLI, and child-process scenarios. It requires `git` and `bd` on `PATH` (some schema scenarios also skip unless `dolt` is available); the target fails early when `bd` is unavailable.
- Integration sources have `//go:build integration` and integration test bodies begin `TestIntegration`; untagged, non-prefixed test bodies are units. The repository convention test rejects an omitted or multiply selected top-level test body. Lane membership comes from the build constraint, not the name filter alone.
- Both lanes use only temporary, isolated filesystem state. They are network-free, credential-free, isolated from operator data, and block real model agents. Disk use by itself does not make a test an integration.
- `make check` is the complete repository validation command. It runs formatting, each test lane once, and linting.

See [testing guidance](docs/testing.md) for scope, prerequisites, and timing baselines.

`orpheus eval review-context` is separate from these validation lanes. It deliberately runs live Pi or Codex review agents and may incur costs; it is never invoked by routine test validation.

## Shell completion

Orpheus generates completion scripts but never changes your shell configuration.
Completion is read-only and best-effort: unavailable repositories or configuration
simply yield fewer suggestions.

Temporary activation:

```sh
# Bash
source <(orpheus completion bash)
# Zsh
source <(orpheus completion zsh)
# Fish
orpheus completion fish | source
```

```powershell
# PowerShell
orpheus completion powershell | Out-String | Invoke-Expression
```

Persistent activation:

```sh
# Bash (bash-completion user directory)
mkdir -p "${BASH_COMPLETION_USER_DIR:-$HOME/.local/share/bash-completion/completions}"
orpheus completion bash > "${BASH_COMPLETION_USER_DIR:-$HOME/.local/share/bash-completion/completions}/orpheus"

# Zsh (add the fpath and compinit lines to ~/.zshrc if not already present)
mkdir -p ~/.zfunc
orpheus completion zsh > ~/.zfunc/_orpheus
printf '%s\n' 'fpath=(~/.zfunc $fpath)' 'autoload -Uz compinit && compinit' >> ~/.zshrc

# Fish
mkdir -p ~/.config/fish/completions
orpheus completion fish > ~/.config/fish/completions/orpheus.fish
```

```powershell
# PowerShell: create the profile directory and file only if missing, then append the startup command.
$profileDirectory = Split-Path -Parent $PROFILE
if (-not (Test-Path -LiteralPath $profileDirectory)) {
  New-Item -ItemType Directory -Path $profileDirectory -Force | Out-Null
}
if (-not (Test-Path -LiteralPath $PROFILE)) {
  New-Item -ItemType File -Path $PROFILE | Out-Null
}
Add-Content -LiteralPath $PROFILE 'orpheus completion powershell | Out-String | Invoke-Expression'
```

## Test timing and regression budgets

`make test-perf` is the repeatable unit-lane performance workflow. It runs five
uncached samples (`go test -count=1`), reports median wall time, and ranks the
slowest packages plus tests/subtests. It writes a JSON report under
`artifacts/test-timing/` by default, which is ignored so local reports can be
retained or uploaded as CI artifacts. Set `TEST_TIMING_OUTPUT=/path/report.json`
to place that artifact elsewhere, `PERF_SAMPLES=7` to take more samples, or
`TEST_TIMING_BASELINE=/path/baseline.json` to use a host-specific baseline.

`make test-perf-integration` measures the real-Beads lane separately and has
the same report and budget behavior. It requires `bd` just like
`make test-integration`.

The tracked `performance/test-timing-baseline.json` records the initial
post-split median measurements, the Go/OS/CPU environment, and suite and
package budgets. Five samples and medians make an isolated timing spike unable
to fail a budget. The budgets allow the larger of a relative or absolute
allowance (50% or 250ms per package; 25% or 500ms for the suite), so small
normal host variance does not assume every CI host matches the reference
machine. A report still identifies slow tests even though only package and
suite budgets are enforced.

To establish a baseline for a new lane, run `make test-perf-baseline` (unit) or
`make test-perf-integration-baseline`. After an optimization, use the matching
`*-baseline-update` command: it only replaces lower medians and lowers their
budgets, never loosens a recorded budget after a regression or a slower host.
Do not edit a baseline merely to make a budget pass. The point-in-time review
at `docs/2026-06-26_review_pipeline_prd.html` is a historical artifact and is
not regenerated by this workflow.

## Documentation

- [Review pipeline workflow](docs/2026-07-05_review_pipeline_workflow.md) explains the operator path from `task run` through `agent done`, `task review`, follow-up work, approval, and publication/finalization retry.
- [Repository publication titles](docs/2026-06-23_repo_publication_titles.md) explains how to configure Jira-style commit and pull-request titles, preserve defaults, and recover from a missing task reference.
- [Publication integration flows](docs/publication_integration_flows.md) explains pull-request and direct-merge defaults, manual selection, and retry behavior.
- [Review pipelines](docs/review_pipelines.md) explains automatic review after `task run`, manual gate resumption, global pipeline configuration, repository defaults, repo-local aliases, clearing behavior, and selection precedence.
- [Task stats and usage capture](docs/task_stats.md) explains Codex-aware profiles, session-correlation limits, report fields, estimates, and troubleshooting unknown telemetry.

- [Task views](docs/2026-08-14_task_views.md) explains the action-oriented `status` queue and the complete active-task inventory from `task list`.

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

Structured Codex and Pi profiles can set `prompt_append` to append literal supplemental instructions after the standard Orpheus bootstrap prompt. One-line and YAML multiline values are supported. Blank values are ignored.

The same effective prompt is passed to the harness launch and exposed as `ORPHEUS_AGENT_PROMPT`. Raw command profiles cannot set `prompt_append`; put any custom text directly in the raw argument that contains `{{prompt}}`, or switch to `harness: codex` or `harness: pi`.

Specialized reviewers can stay on a structured harness profile and keep model metadata plus supported telemetry capture:

```yaml
agents:
  defaults:
    implementer: codex-medium
    reviewer: codex-architecture-review
  profiles:
    codex-medium:
      harness: codex
      model: gpt-5.4
      thinking: high
      interactive: true
    codex-architecture-review:
      harness: codex
      model: gpt-5.4
      interactive: false
      prompt_append: |
        Review from an architecture perspective.
        Focus on module boundaries, dependency direction, data ownership, and long-term maintainability.
```

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

Aggregate stats use focused time views: `orpheus task stats --group day|week|month --view throughput|implementation|review|consumption`. Use `--from YYYY-MM-DD`, `--to YYYY-MM-DD`, and repeated `--repo <id-or-name>` filters to compare bounded periods or repositories. Throughput anchors dates on task resolution and reports workflow time from first implementation launch to resolution. Implementation anchors dates on first implementation launch and reports completed agent work from launch to recorded `agent done`, summing completed follow-up runs while leaving incomplete completion duration unknown. Review anchors dates on first review activity and keeps first-pass approvals, repaired blocked reviews, blocking findings, aborted/paused reviews, and operational failures distinct. Consumption anchors dates on execution launch and reports token/cost totals plus per-task medians. Aggregate duration cohorts report median, p75, sample size, and known-data coverage; token, cost, and repair-cycle cohorts report medians with sample and coverage. Epics are planning containers and are excluded from aggregate views.

Model comparison views use `--view implementation-model`, `--view reviewer-model`, or `--view model-pair` with the same date and repository filters. Task-level outcomes are attributed to a single implementation or reviewer model-selection cohort only when all relevant agent executions used the same model, harness, and thinking level; otherwise they use explicit `mixed`, `unknown`, or `manual-only` cohorts. Cohort labels include known harness and thinking/default qualifiers because those settings can affect token use and cost. Usage and cost stay with the execution model-selection cohort that incurred them, so sparse rows can have token/cost coverage even when their task-outcome count is zero. Model comparison tables keep coverage out of the main columns and list only missing known/sample coverage below the table. These comparisons show correlation only; they do not prove that either model, harness, or thinking level caused the observed outcome.

`orpheus doctor` checks supported harness telemetry for existing task state, including implementation, review-agent, and terminal sync-conflict resolution executions. With `--fix`, it repairs missing Codex or Pi usage only when exactly one safe session correlation exists, or when the closest match is clearly safe. Sync-conflict repair updates the intended finished/failed audit event in place, preferring that event's recorded Work Directory while falling back to the registered repository root. Ambiguous, unmatched, unsupported, or insufficiently identified executions remain unresolved and show candidate details when available; dry runs never mutate state.

`orpheus eval review-context` deliberately runs live review-agent evaluations and may incur Pi or Codex model costs. It is not part of `make test` or routine CI. Use `--harness pi|codex|all`, `--variant legacy|exhaustive|all`, `--scenario general|architecture|all`, and `--repetitions N` to select runs, or `--complete --repetitions 3` for the full Pi/Codex x legacy/exhaustive x general/architecture comparison. Each run uses an isolated temporary repository and Orpheus XDG state, provisions isolated Pi/Codex config homes from the operator's existing auth/config without copying prior session logs, and writes new Pi session logs to an isolated session directory. Unless `--keep-workdirs` is set, completed run directories are removed before the next run to bound disk usage. The report includes seeded findings found or missed, unexpected findings, token usage, cost source, unknown usage or cost, aggregate recall, and total evaluation cost as JSON.

Use raw profiles for custom launch contracts. Use structured `harness: codex` or `harness: pi` profiles when task stats should attempt session and token capture. See [Task stats and usage capture](docs/task_stats.md) for capture reliability, report semantics, cost caveats, and troubleshooting.

### Opt-in follow-up session resumption

Set `ORPHEUS_RESUME_SESSIONS=1` to let review-follow-up implementation runs resume
the latest usable successful, completed session from the same selected profile and
structured Pi or Codex harness. This applies to autonomous repairs and later
`orpheus task run` follow-ups, in both interactive and non-interactive modes. Raw
profiles and all non-follow-up agent purposes remain fresh.

Resumption is best effort: missing, ambiguous, incompatible, deleted, or unsafe
session state falls back to a fresh follow-up and records the reason. A resumed
process that starts and fails is recorded as failed and is not automatically
relaunched fresh. Every launch still receives the standard `orpheus agent context`
bootstrap instruction. Resumed token usage and Pi-reported cost include only the
new execution's increment; unsafe measurements remain unknown. Task inspection and
per-task stats show launch provenance. Orpheus does not assign A/B cohorts or infer
causal resumed-versus-fresh comparisons. See [Review pipelines](docs/review_pipelines.md#resume-implementation-sessions-for-follow-ups).

## License

MIT. See [LICENSE](LICENSE).
