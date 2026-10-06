# Tasks

Tasks and epics belong to registered repositories. Each task source owns their lifecycle;
Orpheus records execution, review, and publication history. Operators choose
which work to run. Epics are planning containers, not executable tasks.

## Register and inspect work

```sh
orpheus repo add /path/to/repository
orpheus repo list
orpheus status
orpheus task list --repo my-repo
orpheus task show op-123
```

Registration defaults to gig, even when the repository contains local Beads state.
Use `orpheus repo add /path/to/repository --task-source beads` to select Beads,
or `--task-source gig` to select gig explicitly. `repo list` shows each source,
storage mode, and task prefix. Source selection is fixed at registration.

Gig supports managed storage only. Orpheus creates and owns
`$XDG_DATA_HOME/orpheus/repos/<repo-id>/gig/tasks.db`, with the usual
`~/.local/share` fallback when `XDG_DATA_HOME` is unset. The database is local
SQLite state, not Git-synchronized task storage. No gig executable, daemon,
or manual database setup is needed. Repository-local gig stores are never
adopted. Back up the Orpheus data root, including gig databases, alongside your
repository backups. Do not remove a managed database to repair a read failure.
Missing or empty databases and physical storage failures produce errors rather
than empty inventories. Gig may tolerate malformed optional fields as empty
values. Its SDK applies schema upgrades when opening storage, including reads.
Managed paths cannot contain `%`, `?`, or `#`.

Existing registrations without a source field remain Beads registrations.
Explicit Beads registration uses an existing local Beads store or initializes
managed Beads storage. `orpheus repo beads-dir <repo>` prints that location and
reports an error for gig repositories. No task data is rewritten or imported.
There is no source-switching, migration, or import/export command.

Gig and Beads repositories can share one workspace. Task IDs resolve through
registered repository prefixes. `task show` includes
relationships and execution history; showing an epic includes its direct children.

`status` is the action queue. It emphasizes needs-attention, reviewing, working,
idle, and ready tasks. Add `--full` to include blocked and closed items.
`task list` is the inventory of all non-closed tasks and epics, including blocked
items. Both use the same projected status and presentation; that projection is
not the task source's lifecycle status.

Useful inventory filters:

```sh
orpheus task list --query parser --type task --status reviewing
orpheus task list --created-after 2026-07-01 --updated-before 2026-08-01
```

`--type task|epic` and
`--status needs-attention|reviewing|working|idle|blocked|closed` are repeatable.
Values within one filter are alternatives; different filters intersect.
`--query` matches IDs and titles without case sensitivity. Date filters accept
`YYYY-MM-DD`; created/updated each support `--*-after` and `--*-before`.
`ready` is an internal projection, not a `task list --status` filter.

For scripts, use JSON rather than parsing tables:

```sh
orpheus status --full --json
orpheus task list --repo my-repo --type task --json
orpheus task show op-123 --json
```

List and status return arrays; show returns one task with its content and
relationships, without execution or review history. All use projected status.

On read failures, commands exit nonzero and print diagnostics to stderr. List
and status retain successful results; show writes no JSON. Blocked and
needs-attention tasks remain valid results.

Agents and tooling can use the [task JSON reference](../developer/task-json.md).

## Create and edit plans

```sh
orpheus task create --repo my-repo --title 'Add parser validation' \
  --description-file /tmp/task.md --acceptance-file /tmp/acceptance.md
orpheus task edit op-123 --external-ref TREX-1234
```

Creation defaults to `--type task`; use `--type epic` for a planning container,
`--parent <epic-id>` for a child, and repeat `--blocked-by <id>` for prerequisites.
Without `--repo`, selection uses active Orpheus repository context, then an
unambiguous registered repository containing the current directory. Use `--help`
for editing fields and dependency flags.

`orpheus task start <epic-id>` starts an eligible epic;
`orpheus task close <epic-id>` closes an epic whose lifecycle checks pass.
Task completion instead goes through review and publication. When working on the
Orpheus repository itself, agents must also follow the task-management restrictions
in [AGENTS.md](../../AGENTS.md).

## Gig behavior and recovery

Gig uses its native SDK behavior. Starting a child can advance its immediate open
parent, but current Orpheus dispatch and epic-start checks already require that
parent to be in progress. Closing a prerequisite can reopen native blocked
dependents. Orpheus normally projects blocking from dependencies rather than
writing that native status. Gig considers cancelled children terminal, while
Orpheus requires closed children and prerequisites. Current commands do not create
cancelled states; manually setting native statuses does not extend Orpheus policy.
Beads behavior is unchanged.

Managed storage is for Orpheus to mutate. Direct external writes bypass its
protection and are unsupported unless you accept responsibility. The mutation lock
covers participating writers, not every entry point yet. Avoid simultaneous
uncoordinated edits. A lock also cannot undo completed SDK calls after an error or
crash. Lists and task details assembled across calls are not a single snapshot.

If creation reports a created ID with an error, inspect that task and repair its
dependencies with `task edit`. Do not repeat `task create`. Failed edits may have
saved content or some relationships. A failed dispatch can leave branch/worktree
facts and a start-attempt timestamp on an open task; retry the same dispatch.
Closure can succeed even if subsequent unblocking fails. Inspect the task and its
dependents, since retrying an already closed task does not replay unblocking.

Follow-up creation and review-history recording are separate writes. After a crash
or recording failure, inspect existing source tasks before repeating creation to
avoid duplicates. SDK calls cannot be interrupted directly while in flight.
See [task sources](../developer/task-sources.md) for mapping and storage limits.

## Run a task

```sh
orpheus task run op-123
orpheus task dir op-123
```

Orpheus creates or reuses a deterministic task worktree and branch, then launches
the selected [agent profile](agents.md). Use `--agent <profile>` to override the
implementer. Use `--repo-root` on first dispatch to work in the registered
repository checkout instead. That starts on a clean default branch and
materializes the task branch at publication.

The work directory stays fixed across retries and repairs. Rerunning `task run`
advances the existing workflow rather than blindly launching another implementer.
It can resume [review](reviews.md), dispatch targeted repairs, or retry
[publication](publication.md). Once a PR exists, use sync for reconciliation.

## Task branch names

Configure the global template in Orpheus `config.yaml`:

```yaml
tasks:
  branch_template: "feature/{{external_ref}}/{{task_title}}"
```

```sh
orpheus repo config get my-repo branch-template
orpheus repo config set my-repo branch-template 'work/{{task_id}}-{{task_title}}'
orpheus repo config set my-repo branch-template ''
```

Selection is repository override, global template, then `orpheus/{{task_id}}`.
Clearing a repository override inherits the global value. Placeholders are
`{{task_id}}`, `{{external_ref}}`, and `{{task_title}}`. Missing required values
fail before creating a branch/worktree or writing task metadata.

Dynamic values retain letters, digits, `_`, and `-`. Other character runs become
one `-`, with leading/trailing replacement dashes removed. Dynamic values cannot
introduce `/`; literal template slashes create branch namespaces. The rendered
name must be a valid Git branch, differ from the registered default branch, and
not already be recorded for another task.

Once recorded, the branch remains authoritative despite later configuration
changes. Repository-root work records it when publication materializes the branch.

## Stats and usage

```sh
orpheus task stats op-123
orpheus task stats --group week --view throughput
orpheus task stats --group month --view consumption \
  --from 2026-07-01 --to 2026-07-31 --repo my-repo
```

Per-task reports include implementation, review-agent, and terminal sync-conflict
executions. Rows show launch facts, process status/duration, session provenance,
usage, and estimated cost. Totals separate execution purposes and count unknown
usage/cost instead of silently treating missing data as zero.

Aggregate views exclude epics. `--group` accepts `day`, `week`, or `month`;
`--from` and `--to` are inclusive, and `--repo` is repeatable.

| View | Date anchor and meaning |
| --- | --- |
| `throughput` | Resolution date; workflow duration from first implementation launch to resolution. |
| `implementation` | First implementation launch; completed launch-to-`agent done` work, usage, and failures. |
| `review` | First review activity; duration, repair cycles, findings, and distinct review outcomes. |
| `consumption` | Execution launch; usage and cost attributed to when each execution ran. |

Durations report median, p75, sample size, and known-data coverage. Token, cost,
and repair-cycle comparisons include medians and coverage. Model comparisons use
`--view implementation-model`, `reviewer-model`, or `model-pair`, grouped by
model, harness, and thinking selection. Mixed, unknown, and manual-only cohorts
remain explicit. These are correlations, not proof that a model caused an outcome.

### Time and token interpretation

- Full task time starts at task creation. It is not a current stats table column.
- Workflow time starts at first implementation launch and includes review and waits.
- Per-task active agent time is process elapsed time, which can include interactive
  waits. Implementation aggregates instead sum launch-to-handoff time only when
  every implementation run has valid completion timestamps.
- Codex `cached_input` is a subset of `input`; do not add it again. Pi combines
  separate cache-read/write counts into `cached_input`, outside its `input` count.
- Reasoning output can overlap output. Prefer harness-reported `total`; do not
  derive cross-harness totals by adding normalized fields blindly. If a Pi message
  omits or reports zero total tokens, the fallback adds input and output only,
  excluding cached input.

### Capture reliability and cost

Usage capture requires a structured Pi or Codex profile. Raw profiles remain
unknown even when their command launches one of those tools. Orpheus correlates
local JSONL logs by working directory, execution start, and session name where
available. Multiple candidates require a safely disambiguated match; otherwise
usage remains ambiguous. A match is not proof of vendor billing identity.

Codex logs are under `$CODEX_HOME/sessions` or `~/.codex/sessions`. Pi lookup uses
`PI_CODING_AGENT_SESSION_DIR`, then `PI_CODING_AGENT_DIR/sessions`, then
`~/.pi/agent/sessions`. Concurrent sessions, missing logs, clock drift, or missing
usage fields can prevent capture. Resumed repairs count only a safely measured,
non-negative increment over the pre-launch session counters. Unsafe increments
remain unknown.

Known non-Pi usage for a recognized model gets an API-equivalent estimate using
pricing effective at execution start. Orpheus stores the amount and pricing
snapshot; later catalog changes do not reprice it. Missing dates, unsupported
prices, and dates before the earliest known rate leave estimates unknown. Usage
spanning a price change is not split across rates.

Pi cost uses its reported `usage.cost.total`, labeled `pi_reported_estimated`.
Missing Pi cost stays unknown rather than falling back to API pricing. Neither
estimate is an invoice or subscription reconciliation. Estimates exclude pricing
variations such as long-context premiums, Batch/Flex/Fast rates, regions, and
cache-write pricing.

For missing data, inspect the row's reason, structured profile, session-log path,
and log retention. Avoid assigning sessions by hand or replacing unknowns with
zero. Run `orpheus doctor` to inspect recoverable telemetry; `--fix` writes only
safe matches.

## Recovery and cleanup

```sh
orpheus doctor
orpheus doctor --fix
```

Plain doctor diagnoses local state without changing it. `--fix` attempts safe
repairs, including interrupted executions, recoverable usage, and closed-task
worktree cleanup. Use `--verbose` for diagnostics on stderr.

Automatic cleanup after PR or direct-merge closure and `doctor --fix` use the
same worktree ownership checks. After confirming task closure, metadata, local
state, deterministic path, branch, and lock state, Orpheus attempts native
`git worktree remove` without force. Plain doctor reports `cleanup_pending`
without testing cleanliness or removing files.

Git permits ignored build outputs but refuses tracked or non-ignored untracked
changes. A failed cleanup leaves the task closed and reports the retained path.
Cleanup never forces removal or runs clean, stash, reset, or commit. It preserves
task branches, local history, logs, and backend records. Already absent worktrees
are harmless; incomplete Git registrations require manual repair.
