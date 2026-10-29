# Reviews

A task review is the approval boundary between implementation and
[publication](publication.md). `orpheus task run <task-id>` advances the workflow:
it runs the implementer, retains safely completed output, enters review, and handles
repairs. A passed review prepares publication but does not authorize it. Publication
remains gated pending the separate consent UX. Later invocations resume pending work.

## Completion and approval

The implementation agent reads `orpheus agent context`, edits the task's work
directory, then records a handoff:

```sh
orpheus agent done --summary 'fix: concise summary' \
  --description 'Concise commit description' \
  --detailed-description-file /tmp/pr-body.md \
  --technical-explanation-file /tmp/technical-explanation.md
```

The handoff requires a summary, commit description, one detailed PR-body source,
and one technical-explanation source. It records completion, not approval or a
commit. The technical explanation goes to reviewers; it does not replace the PR
body. Each implementation or repair attempt needs its own successful `agent done`,
even when it resumes a harness conversation containing an older handoff.
Same-attempt repeats keep the first handoff unchanged. After the child's actual successful
exit, Orpheus captures an ordinary commit with repository hooks, signing, and
configuration intact. A live handoff cannot trigger capture. No-change efforts
may explicitly reuse their actual input while retaining their own handoff.
Review evaluates the retained commit, never a moving HEAD or dirty candidate;
reviewer context includes the full-task baseline and repair-delta endpoints.

Without configuration, Orpheus uses a single manual `local-review` step. The
command stays attached for the operator to approve, record findings, or abort.
Manual commands, when configured, run after confirmation. Review steps are
read-only: changing HEAD or its branch attachment, index entries, tracked or
untracked file contents, symlinks, or file modes is an operational failure.
Orpheus restores and verifies the pre-step checkout and index where possible,
even after reviewer cancellation. An incomplete restore requires manual repair;
it is never reported as successful rollback. Keep temporary review reports
outside the candidate worktree.

`task done` cannot bypass review, a live process, or missing publication consent.
It only retries an existing explicitly authorized publication intent; it does not
commit implementation changes or offer a temporary confirmation override.

## Configure a pipeline

Define named pipelines in `$XDG_CONFIG_HOME/orpheus/config.yaml`, normally
`~/.config/orpheus/config.yaml`:

```yaml
reviews:
  default_pipeline: standard
  max_autonomous_review_attempts: 4
  pipelines:
    standard:
      steps:
        - kind: check
          name: test
          command: make
          args: ["test"]
        - kind: agent_review
          name: ai-review
        - kind: manual
          name: local-review
```

A `check` runs a command; a nonzero exit records a blocker. An `agent_review`
launches only its configured reviewer, which reports findings with
`orpheus agent review add`. A `manual` step collects operator decisions directly.
Configure the reviewer under `agents.defaults.reviewer` or override it with the
step's `agent` field; see
[agent profiles](agents.md). Step names must be nonblank and unique within a
pipeline. Empty pipelines are not allowed.

Selection order:

1. `orpheus task run --pipeline <name-or-alias> <task-id>`
2. Repository `review-pipeline`
3. Global `reviews.default_pipeline`
4. Built-in manual pipeline

```sh
orpheus repo config get my-repo review-pipeline
orpheus repo config set my-repo review-pipeline standard
orpheus repo config set my-repo review-pipeline-alias.quick standard
orpheus task run --pipeline quick op-123
```

Aliases are repository shorthand for global pipelines, not repository-local
step definitions. Orpheus records the resolved pipeline name. Clear a repository
setting or alias by setting its value to `''`. A paused attempt keeps its stored
pipeline; an override cannot replace it.

## Findings and repairs

- Blocking findings require a fix or an explicit disposition before approval.
- Advisory findings record feedback without blocking approval.
- Separate-task findings propose independent follow-up work without blocking
  approval by themselves.

For check and agent-review blockers, Orpheus asks the operator to choose:

| Decision | Effect |
| --- | --- |
| Keep | Preserve the blocker and dispatch a targeted implementation repair. |
| Downgrade | Make it advisory, with a required reason. |
| Waive/cancel | Waive it, with a required reason. |
| Restart | Append another attempt for that slot; retain the previous execution/findings as superseded audit history. |
| Pause | Preserve the pending decision and exit without a repair or publication. |

A manual `finish/block` decision already keeps the blockers recorded at that
gate; it does not require another confirmation. One repair run targets the
eligible blockers from the attempt. Once the repair records completion, a fresh
review starts at step one, including any earlier manual gate. An incomplete
repair does not start another review or publish.

`reviews.max_autonomous_review_attempts` defaults to `4`, including the initial
review. That allows at most three repair runs before the fourth blocked review
stops. A new `task run` grants a fresh budget. Restarting a step does not consume
that budget or rerun earlier passed steps. Repairs and explicit step restarts
use the same configured reviewer selection.

Resume paused decisions, waiting manual steps, or aborted reviews with:

```sh
orpheus task run op-123
```

A waiting manual step resumes within the same attempt without rerunning completed
steps. Interrupted blocker input launches no repair and grants no approval. On
continuation, Orpheus resumes the exact pending automated decision; when a fresh
review is required it first asks for dispositions of unresolved prior blockers. Addressed-manually and waiver decisions require reasons.

Operational failures, such as a missing executable, failed reviewer process, or
read-only violation, are not product findings. Correct the environment or review
process, then run `task run` again.

When a passing attempt contains separate-task proposals, the operator selects
numbers, `a=all`, or `n=none`. Selected proposals become Beads with review
provenance before publication. If creation fails, the operator can stop and
retry later or explicitly continue without creating that task.

## Inspect review history

```sh
orpheus task show review op-123
orpheus task show review op-123 2
orpheus task show review op-123 2 1
```

These show authoritative finding history, one attempt, or one finding.
`<attempt>/<finding>` references identify findings across reviews. Inspection is
read-only and includes dispositions, repair runs, and created follow-up tasks.

Fresh reviewers receive the technical explanation and a compact history of prior
authoritative findings. Earlier waivers supply context, not permission to ignore
a newly applicable or materially changed defect. The latest attempt governs the
workflow; older attempts remain audit history.

## Follow-up session resumption

Repairs reuse the latest usable, successful, completed implementation session
from the same selected structured Pi or Codex profile, in attached and
non-interactive launches. Initial implementation runs, raw-command profiles,
reviewers, and sync-conflict agents always start fresh.

Missing, ambiguous, incompatible, or unsafe session data falls back to a fresh
repair with a recorded reason. If a resumed process starts and fails, Orpheus
does not automatically launch a fresh replacement because files may already have
changed. Every repair still reads current `agent context` and records a new
`agent done`. Task inspection and stats show launch provenance; resumed usage
counts only the new execution's safely measured increment.

## Safe reporting text

Generated prose is data, not shell source. Use file flags for Markdown, including
`--detailed-description-file`, `--technical-explanation-file`, and the review
reporting flags `--description-file`, `--task-description-file`, and
`--task-acceptance-criteria-file`.

Do not interpolate generated prose into double-quoted shell arguments or a
fixed-delimiter heredoc. Double quotes still expand backticks, `$()`, and variables;
a generated heredoc delimiter can end the data and execute subsequent lines.
One safe file-writing method is to base64-encode the content and decode a
single-quoted payload with `printf '%s' '<base64-data>' | base64 --decode >"$file"`.
For inline text, use single-quoted literals and escape apostrophes as in
`'O'\''Brien'`. Verify reporting commands succeeded before retrying.

## Live review evaluation

`orpheus eval review-context` deliberately runs live Pi or Codex agents and may
incur model costs. It is never routine validation. It uses isolated repositories
and state, provisions isolated harness config from existing operator auth/config,
and reports findings, recall, usage, and cost as JSON. It removes run directories
unless `--keep-workdirs` is set. Use `--help` for selectors, or
`--complete --repetitions 3` for the full comparison only when intentionally
running a live evaluation.
