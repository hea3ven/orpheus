# Task stats and usage capture

`orpheus task stats` reports execution timing and, where a supported harness leaves a
local session log, token usage and an estimated cost. It is an operational aid for
comparing work; it is not a billing system.

## Enable Codex usage capture

Configure a **structured** Codex profile in Orpheus' global `config.yaml`. Structured
profiles let Orpheus construct the Codex invocation, preserve the selected model, and
look for the resulting local Codex session log after the execution finishes.

```yaml
agents:
  defaults:
    implementer: codex-implementer
    reviewer: codex-reviewer
    sync_conflict_resolver: codex-sync
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
    codex-sync:
      harness: codex
      model: gpt-5.4-mini
      interactive: false
```

`model` is required for `harness: codex`. Interactive profiles run `codex`; a
non-interactive profile runs `codex exec`. `thinking` becomes Codex's
`model_reasoning_effort` setting. Orpheus captures usage after implementation runs,
review-agent steps, and sync-conflict-resolution executions.

A raw profile remains a generic process launch even if its command happens to be
`codex`:

```yaml
agents:
  defaults:
    implementer: custom-codex
  profiles:
    custom-codex:
      command: codex
      args: ["{{session_name}} - {{prompt}}"]
```

Orpheus runs that command exactly as configured. It does not infer its model, launch
mode, or telemetry support, so its usage is reported as unknown. Use a structured
`harness: codex` profile when Codex usage capture is wanted. A profile cannot mix
structured `harness`/`model` settings with raw `command`/`args`.

Structured Pi profiles are also supported; see the [README](../README.md#agent-profiles).
They use Pi session logs and can report Pi's own estimated cost. Other raw profiles
remain valid, but have no harness-specific usage capture.

## How capture is correlated

Codex writes JSONL session logs under `$CODEX_HOME/sessions`, or under
`$HOME/.codex/sessions` when `CODEX_HOME` is not set. After the child process exits,
Orpheus searches those logs for a session whose canonical working directory is the
execution directory and whose start time is within two minutes of the recorded
execution start.

This is deliberately best-effort correlation, not a provider execution ID:

- one matching session is used;
- with several candidates, Orpheus only chooses the nearest start when it is clearly
  safer than the next candidate (within 15 seconds, at least five times closer, and
  separated by at least 30 seconds);
- otherwise it records the usage as **ambiguous**, preserves candidate details, and
  does not guess.

Consequently, simultaneous Codex sessions in the same directory, clock drift, changed
working directories, cleaned session logs, or a harness version that omits session
metadata or `token_count` data can leave a run unknown. A captured session says that
one local log matched these constraints; it does not prove a vendor-side billing
relationship.

Pi correlation follows the same conservative shape. It searches
`PI_CODING_AGENT_SESSION_DIR`, then `PI_CODING_AGENT_DIR/sessions`, then
`~/.pi/agent/sessions`, and matches directory, start time, and (when recorded) the
native Pi session name.

For an opted-in resumed follow-up, Orpheus already has an exact source session.
When readable, it records that session's cumulative token and Pi-cost values
immediately before launch, reads the same exact log after the process exits, and
reports only the non-negative difference. Earlier turns in the reused session are
therefore not counted again. If the pre-launch baseline is unavailable, the log
disappears, counters regress, or a safe difference otherwise cannot be established,
the resumed execution's affected measurement remains explicitly unknown rather
than becoming zero or a cumulative value.

## Run reports

Show one task's recorded executions:

```sh
orpheus task stats op-123
```

The **Executions** table has one row for each implementation run, review-agent step,
or terminal sync-conflict-resolution run:

- `TYPE`, `ATTEMPT`, and `STEP` identify why it ran and where it belongs.
- `PROFILE`, `HARNESS`, `MODEL`, and `COMMAND` are the launch facts recorded by
  Orpheus. The model may be updated from a matched harness session.
- `STARTED`, `FINISHED`, `DURATION`, and `STATUS` describe the attached process.
- `LAUNCH_MODE` is `fresh` or `resumed` for review follow-ups.
  `RESUME_SOURCE` identifies the source run/session for a resume, while
  `RESUME_FALLBACK` explains why an opted-in resume safely fell back to fresh.
  Older and non-follow-up executions show `-` for these fields.
- `SESSION` identifies the matched local session when there is one.
- `USAGE` is either token fields or an `unknown`/`ambiguous` status and reason.
- `ESTIMATED_COST` identifies either the API-equivalent estimate, Pi-reported
  estimate, or why a cost cannot be determined.

The **Totals** table separates implementation, review-agent, and sync-conflict-
resolution work and includes a `combined` row. `UNKNOWN_USAGE` and `UNKNOWN_COST`
are counts of executions, not zero-valued usage or cost. A displayed zero token value
is only meaningful when that execution's usage is known; use those count columns and
the execution row's reason to distinguish zero from unknown.

### Time terms

Use these terms consistently when comparing work:

- **Full task time** is elapsed time from creation in the task source to finalization
  or task-source closure. It includes time before Orpheus dispatch, queues, human
  waits, review, and other elapsed time.
- **Implementation lifecycle time** is elapsed time from the first Orpheus
  implementation dispatch to finalization or task-source closure. It still includes
  waiting and review between agent executions.
- **Active agent working time** is attached-process wall-clock time: the recorded
  elapsed time from process start to finish. It excludes waits between processes and
  lifecycle gates, but can include idle time or human-interaction waits while an
  interactive process remains attached. It is not a measure of agent compute time.
  Per-task totals sum completed execution durations, so retries and follow-up runs
  increase it.

The current `throughput` aggregate calls its duration `WORKFLOW_*`: it measures
implementation lifecycle time from first implementation launch to resolution, not full
task time. Full task time is defined above for operational comparison but is not a
current `task stats` table column. `ACTIVE_AGENT_TIME` in a task report is process
elapsed time. The implementation aggregate's `AGENT_*` values sum the duration from
launch to the recorded `agent done` handoff only when **every** implementation run
for a task has valid launch and completion timestamps. If any run is incomplete or
invalid, that task is excluded from `AGENT_COVERAGE`; no partial `AGENT_*` duration
sample is reported. Use per-task `ACTIVE_AGENT_TIME` when process-only elapsed time
is required.

### Token classes

`USAGE` and task totals normalize the harness-reported fields, but the relationship
between classes is harness-specific:

- `input` is input/context tokens as reported by the harness.
- `cached_input` has different meanings by harness. For **Codex**, it is input served
  from cache and is a subset of `input`; do not add it to Codex `input`. For **Pi**,
  Orpheus combines Pi's separate `cacheRead` and `cacheWrite` values into this field.
  It is not a subset of Pi `input` and can be larger than it. To interpret Pi's token
  classes, `input + cached_input + output` may be added; this represents Pi's separate
  input, cache-read/cache-write, and output classes.
- `output` is generated output tokens.
- `reasoning_output` is reported reasoning tokens. It may overlap output, so do not
  add it to output to derive a charge.
- `total` is the harness-reported total and is authoritative for that harness. Do not
  infer a replacement total from the normalized fields when a harness supplied one:
  class definitions and overlap differ between Codex and Pi. Pi's `totalTokens` can
  equal `input + cacheRead + cacheWrite + output`; Orpheus presents the two cache
  fields together as `cached_input`. If a Codex log omits a total but has input and
  output, Orpheus falls back to their sum. If a Pi assistant message omits or reports
  zero `totalTokens`, Orpheus likewise falls back to `input + output` only; that
  fallback omits `cached_input`, so total can be zero even when cached usage is known.

Use `total` for cross-harness consumption comparisons only when its source is known.
In particular, a Pi fallback total is not directly comparable with a Pi
harness-supplied total because it omits cached input. Compare the individual `input`
and `cached_input` fields only with their harness-specific definitions in mind.

## Aggregate reports

Aggregate non-epic tasks by a period and focused view:

```sh
# Delivery flow: resolved tasks and implementation-lifecycle duration.
orpheus task stats --group week --view throughput

# Active implementation time, tokens, cost, and process failures.
orpheus task stats --group month --view implementation \
  --from 2026-07-01 --to 2026-07-31 --repo backend --repo web

# Review duration, repair cycles, and review outcomes.
orpheus task stats --group week --view review

# Usage and cost by execution launch date.
orpheus task stats --group day --view consumption
```

`--from` and `--to` are inclusive `YYYY-MM-DD` dates. Repeat `--repo` to restrict a
comparison to registered repository IDs or names. The report prints its date anchor:

| View | Date anchor | Key fields |
| --- | --- | --- |
| `throughput` | Task resolution | `RESOLVED`, `WORKFLOW_MEDIAN`, `WORKFLOW_P75`, `WORKFLOW_COVERAGE`. Workflow is first implementation launch to resolution. |
| `implementation` | First implementation launch | `AGENT_MEDIAN`/`AGENT_P75` summed launch-to-`agent done` implementation time, token/cost medians, and `FAILURES`. |
| `review` | First review activity | `REVIEW_MEDIAN`/`REVIEW_P75`, repair-cycle median, first-pass/repaired outcomes, findings, operational failures, aborted, and paused counts. |
| `consumption` | Each execution launch | execution count, known token/cost totals and per-task medians. A task can contribute to more than one period when its executions do. |

Medians and p75 values are per-task values within the period. `*_COVERAGE` is
`known/samples`; it must accompany comparisons when some logs or prices are unknown.
A `-` has no known value. Consumption totals can be partial when coverage is less than
complete, rather than silently treating missing usage as zero.

For configuration comparisons, `--view implementation-model`, `reviewer-model`, or
`model-pair` groups outcomes by recorded model, harness, and thinking/default
selection. A task that used differing selections is labeled `mixed`; missing metadata
is `unknown`; no relevant agent execution is `manual-only`. These are observational
cohorts, not evidence that a model, harness, or reasoning setting caused an outcome.

## Estimated cost limits

For a non-Pi execution with known tokens and a recognized model, Orpheus calculates an
**API-equivalent** USD estimate from public pricing metadata. When it captures the
execution usage, it also stores the amount and the complete pricing snapshot: model,
tier, rates, reasoning-token treatment, pricing source, and source date. That snapshot
is immutable, so later changes to Orpheus' hardcoded pricing table do not reprice a
historical execution. It charges uncached input at the input rate, cached input at the
cached rate, and `max(output, reasoning_output)` at the output rate. This avoids
charging overlapping reasoning/output tokens twice.

This estimate can differ from subscription charges, invoices, negotiated discounts,
service tiers, batch pricing, taxes, or a harness/vendor's own accounting. It is not
billing reconciliation. A model without a supported pricing row still has usable token
statistics, but its cost is unknown.

For Pi, Orpheus stores `usage.cost.total` when Pi reports it and labels it
`pi_reported_estimated`. That is still an estimate, not an invoice. Orpheus does not
replace a missing Pi-reported cost with an API-equivalent calculation.

## Troubleshoot unknown or ambiguous usage

1. Start with `orpheus task stats <task-id>` and read the execution row's `USAGE` or
   `ESTIMATED_COST` reason. Common reasons are an unsupported/raw harness,
   `no_matching_codex_session`, multiple matching sessions, a matching session without
   token data, an unavailable/unreadable session home, missing Pi assistant usage, or
   missing pricing metadata.
2. Verify the profile is structured (`harness: codex` or `harness: pi`), the process
   was launched by Orpheus, and the configured model is present. A raw `codex` command
   cannot be retroactively identified as a structured Codex execution.
3. Confirm the session-log environment (`CODEX_HOME`, `HOME`, or the Pi session
   variables), log retention, and working directory. Avoid concurrent harness sessions
   in the same worktree around the dispatch start time.
4. Run `orpheus doctor` to inspect recoverable telemetry and candidate sessions. Run
   `orpheus doctor --fix` only after reviewing it. The fix writes only a unique or
   clearly safe correlation for implementation, review-agent, and sync-conflict-
   resolution executions; ambiguous or insufficient matches remain unresolved.
5. For known tokens with unknown cost, check whether the model has public pricing
   metadata. For Pi, also check whether that session recorded `usage.cost.total`.

Keep unknown and ambiguous values in comparisons; do not replace them with zero or
manually assign a session. The coverage and unknown-count fields exist to make the
reliability limit visible.
