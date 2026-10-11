# Timing gate handoff

## Goal and authorization

The operator asked to investigate repeated PR quality-check failures, then explore solutions and propose a plan for host-independent timing gates. They suggested publishing main-branch timings for branch actions, possibly through GitHub Actions cache. Coverage quality rules must not change.

The initial investigation authorized reporting only, without implementation, task creation, commits, or publication. The operator subsequently requested this handoff update and approved the exact single-task plan. Task `op-dys` has been created for investigation before deciding on a production design; it has not been started.

The investigation task must work in its assigned task branch and worktree. It may commit and push diagnostic changes on that branch and open a temporary PR to exercise GitHub workflows. It must not commit or push to `main`, merge its PR or branch into `main`, or expect its experimental changes to be merged. Its deliverable is evidence and findings recorded in this handoff so the operator can continue the discussion and reach a final decision. This is not authorization to implement or deploy a production gate.

The planning/task-creation session does not run experiments or publish changes. The operator approved the task's experiment budget below. Production-design recommendations remain subject to review after the investigation.

## Checkout state

- Originally investigated branch: `orpheus/op-zcv`, initially at `eb58df633911dc7d218b3b630516a6e6757aece6`.
- PR: https://github.com/hea3ven/orpheus/pull/142. It is now merged; its final head was `2a0235df0963846621d805bf1ae807b0971c351b`.
- Original work directory: `~/.local/share/orpheus/repos/orpheus/worktrees/op-zcv`. It disappeared during the initial investigation and is absent from the subsequently inspected worktree list. Do not recreate it for this task; use the task's assigned worktree.
- Production checkout inspected during planning: `~/Documents/prog/orpheus`, clean on `main` at `11e9054` (`docs: migrate domain vocabulary to GLOSSARY.md`). Use it for reference, not experimental changes or commits.
- Handoff location: `/home/mati/Documents/prog/orpheus-docs/timing-gate-handoff.md`, in the separate `docs` worktree. This file is untracked; `docs/IDEAS.md` has an unrelated pre-existing modification. Preserve that modification and do not stage or commit the docs worktree wholesale.
- Refresh revision and worktree state when the investigation starts. The original failing PR state is historical, not an active failure.

## Findings and evidence

The inspected workflow run failed seven times on one timing bound. Tests, coverage, lint, and build passed. Gig integration package elapsed time was 1.120 to 1.569 seconds against the tracked 1.038-second ceiling. The ceiling was introduced by the Gig feature commit.

- Run: https://github.com/hea3ven/orpheus/actions/runs/37363908772
- Retained reports from attempts 3 through 7: `/tmp/orpheus-pr142-dnvdud/pull-request-quality-37363908772-{3,4,5,6,7}/report.json` and adjacent `report.md`, `quality.log`, and setup/lint/build logs. These temporary files may disappear.
- Earlier attempts 1 and 2 were checked through `gh run view --attempt`; the same sole timing violation occurred.
- The operator ran `make quality-policy-update`, but it produced no policy change. The inspected local `artifacts/test-coverage/report.json` contained five complete samples and a Gig integration median of 0.786 seconds. It was inside the updater's no-change band. This report was in the now-missing task worktree.

The updater behaving correctly on a faster local host does not fix the slower CI host's absolute threshold. Repeated SQLite opening/closing makes filesystem performance a plausible contributor, but no profiling established the precise runtime cause.

Subsequent read-only review found that PR #142's final quality gate passed:

- Run: https://github.com/hea3ven/orpheus/actions/runs/37532713239
- The job took 1 minute 59 seconds; its single-pass quality-report step took approximately 58 seconds. These are observed command/job costs, not paired-experiment measurements.
- The inspected production `.quality.yml` now has a 2.500-second Gig integration ceiling. The original failure has been resolved, but the policy still contains host-derived absolute ceilings.

## Relevant implementation references

Paths below refer to the active production checkout, not the historical docs worktree.

- `AGENTS.md`, `docs/README.md`, `docs/developer/testing.md`: current work and validation rules.
- `.quality.yml`: tracked coverage floors, timing headroom settings, and host-derived timing ceilings.
- `.github/workflows/quality-gate.yml`: PR-only workflow, synthetic merge checkout, pinned tools, single-pass quality report, separate lint/build, final enforcement, diagnostic artifacts.
- `cmd/testcoverage/main.go`: collection, normal assessment, five-sample policy update, report output.
- `cmd/testcoverage/quality_policy.go`: coverage and timing currently assessed together; median aggregation and update rules.
- `cmd/testcoverage/coverage.go`: coverage-instrumented commands, environment sanitization, JSON event decoding, package elapsed times.
- `cmd/testcoverage/report_types.go`, `summary.go`, `quality_policy_test.go`, `scheduling_test.go`: report contract and boundary tests.
- `internal/tasksource/gig/store.go`: each adapter operation opens and closes the SDK store.

Reuse the existing selected-package-work metric. Command wall time is diagnostic only. Keep default package concurrency and `-parallel=1`. Capture effective scheduling environment such as `GOFLAGS`, not just command arguments. Preserve `ORPHEUS_COVERAGE_RUN=1`.

## Options explored

1. **Main publishes a timing cache.** PRs can restore default-branch caches, but this yields an environment-specific baseline, not the same physical host. Cache entries are immutable, evictable, and searched in current-branch scope before default-branch scope. An exact hit does not independently prove trusted publisher provenance.
2. **Main publishes timing artifacts.** Easier to select a trusted producer run for an exact commit. Requires cross-run lookup/download and Actions read permission. Retention and host variation remain.
3. **Measure base and PR on the same runner.** Most direct treatment of host differences. No baseline publication race. Approximately doubles initial test work and needs sampling to handle transient load and ordering effects.

The assistant recommends option 3 initially, deferring published baselines until test cost warrants the complexity. The operator instead selected investigation before implementation; no baseline strategy is approved. Evaluate the alternatives with evidence rather than treating same-runner comparison as a settled decision.

For a later production design using published baselines, consider a main-only publisher, five complete samples, PR restore-only consumption, an immutable key containing schema/environment/exact base SHA, metadata and provenance validation, and an explicit missing-baseline fallback. Do not install a publisher on `main` during this investigation. Never use a broad "latest main" fallback or candidate-derived baseline to silently pass a PR. Artifacts were preferred over cache as authoritative evidence.

## Settled operator decisions

- One investigation task, `op-dys`, is approved and created, not an implementation epic. Include isolated local measurements and fresh GitHub-hosted experiments.
- A future production design keeps routine local `make check` single-pass for tests and coverage, with authoritative timing comparison in an explicit command. Documentation and agent instructions must change with that implementation, not ahead of it.
- Confirmed timing regressions must fail CI and clearly show the regression. Do not implement allowances, bypass labels, or an override mechanism. An admin operator may decide to merge despite a failed check using repository permissions; the code need not support that decision.
- Experimental changes stay on the assigned task branch. Pushing that branch and opening a temporary PR are allowed for workflow experiments; merging or committing experimental changes to `main` is not.
- Record all findings and the recommendation in this handoff. Completion means a useful continuation record, not merge-ready code or a deployed gate. Leave the final production-design decision to a later operator discussion.

## Review findings

The handoff was checked against the current collector, policy, workflow, and related tests. The Gig runtime implementation was not profiled, and the original failing reports were not remeasured.

- **Policy-independent collection is needed.** `cmd/testcoverage/main.go` loads policy before collecting. Running each checkout's `make quality` would use different collector versions and could reject the base for stale policy. A comparison design needs one collector measuring both revisions without modifying them or enforcing the base's historical bounds.
- **Missing evidence needs a distinct result.** A failed, missing, or incompatible base measurement must not produce timing success or be mislabeled as a measured regression. Recommend an explicit unevaluated result that blocks CI.
- **Same-runner sampling is not yet specified.** Warm-up, ordering, effective environment, and whether the first pair counts toward confirmation need experimental support. Current `comparableLane` checks command arguments but not inherited scheduling settings.
- **Policy separation should not ship alone.** If implementation is later selected, fold removal of absolute ceilings into the first usable replacement comparison slice rather than leaving a temporary enforcement gap.
- **The testing guide disagrees with configured tolerances.** The current guide describes 25% suite and 50% package timing headroom; `.quality.yml` actually configures 50% and 75%. Preserve the configured values in experiments.

Related title searches through `orpheus task list` for timing, quality, coverage, and scheduling found no matching tasks before creation. After explicit approval, task `op-dys` was created and its stored description, acceptance criteria, and relationships were verified with `orpheus task show op-dys --json`. No separate spec or ADR has been written.

## Investigation task

- **ID:** `op-dys`
- **Title:** Evaluate host-independent timing-gate designs
- **Type:** Task
- **Repository:** `orpheus`
- **Parent/dependencies:** None
- **Execution:** Not started

### Description

Investigate replacements for machine-derived timing ceilings using controlled local and fresh GitHub-hosted measurements. Compare same-runner base/candidate measurements with exact-commit published baselines, including authoritative artifacts versus cache. Deliver durable evidence and an implementation-ready recommendation for operator review, without selecting or deploying the production gate.

### Scope and execution boundary

- Preserve coverage floors, significance thresholds, package checks, explicit five-sample coverage updates, instrumentation, and existing scheduling.
- Use the assigned task branch and worktree for diagnostic prototypes and workflow changes. Diagnostic commits and pushes on that branch and a temporary PR are authorized for these experiments. Do not change, commit, or push `main`; do not merge the temporary PR or task branch into `main`.
- Do not treat this task as normal publication/finalization work or invoke a completion path that expects a merge. Experimental commits need not become production changes.
- Update `/home/mati/Documents/prog/orpheus-docs/timing-gate-handoff.md` with findings, evidence references, limitations, and the recommendation. This explicitly authorized documentation output is separate from the assigned experimental worktree. Keep the handoff update uncommitted unless separately asked to commit it.
- Approved experiment bound: at most three hosted jobs, each with a twenty-minute timeout, plus bounded local samples. Expansion requires operator approval.

### Acceptance criteria

1. This handoff contains a reproducible experiment report with revision identities, collector/tool versions, effective scheduling environment, measurement order, and durable raw-sample references. Do not rely solely on temporary directories or expiring Actions artifacts for continuation evidence.
2. Local and hosted evidence evaluates unchanged-revision noise, cross-run host variation, and detection of a controlled slowdown. Identify evidence that remains unavailable or inconclusive rather than claiming an unproven result.
3. Results quantify normal-path and confirmation-path cost, separately reporting command wall time and selected-package work.
4. The recommendation specifies warm-up/order controls, sample counts, confirmation rules, baseline provenance, and environment compatibility.
5. The design preserves candidate coverage assessment independently of timing; stale base policy cannot invalidate collection or require an automatic policy refresh.
6. The comparison contract permits legitimate cross-revision test/package changes, compares common packages and whole-lane work, and reports additions/removals. Missing, failed, or incompatible evidence is explicitly unevaluated and blocking in CI, not a pass or measured regression.
7. All findings, temporary PR/run links, experimental branch state, validation results, and remaining decisions are recorded in this handoff. No task changes are committed or merged to `main`. Any temporary PR remains clearly experimental and is never merged by the task.
8. The report proposes vertical CI/local implementation slices for later discussion and approval. It does not create implementation tasks, assume a final design, or require experimental changes to be merged.

### Non-goals

Production gate changes on `main`, coverage-policy refreshes, SQLite optimization, baseline infrastructure deployment, regression exceptions, task lifecycle automation, and automatic cleanup/publication of the docs worktree.

## Candidate design to test, not an implementation mandate

Separate timing evaluation from coverage policy while retaining checked-in coverage floors, significance thresholds, package-structure checks, instrumentation, and explicit coverage updates. Replace host-derived timing ceilings with baseline inputs and independently reported coverage/timing results. `quality-policy-update` would update coverage without rewriting machine-specific timing bounds.

For same-runner comparison, measure the exact base commit used to construct the tested synthetic merge and the merge itself in isolated checkouts, sequentially, with the same collector, tools, flags, and environment. Assess coverage only for the tested merge against its own checked-in policy. Base collection must not fail solely because its old policy needs an update.

Preserve actual configured tolerances:

- Suite ceiling = base median + max(base median × 50%, 0.5 seconds).
- Package ceiling = base median + max(base median × 75%, 0.25 seconds).

The initial hypothesis is one full measurement per revision, confirming suspected timing violations with five complete samples per revision and balanced run order. Investigate warm-up controls and whether the initial pair can count toward those samples. Do not replace full-lane measurements with isolated-package runs for enforcement.

Within samples of one revision, require consistent test counts and package structure. Across revisions, allow legitimate changes; compare common packages and whole-lane work, and report additions/removals. The existing `comparableLane` cannot be reused unchanged across revisions. New packages have no individual base measurement; their work still contributes to the suite comparison.

A future explicit local command could reuse host-local base measurements keyed by repository, base SHA, and measurement environment. Cache reuse is not settled. Missing local baselines must report timing as not evaluated, not passed.

## Decisions still needed

- Select concrete controls/revisions within the approved experiment budget when task `op-dys` starts.
- After evidence is reviewed, choose same-runner comparison or published environment baselines. Do not equate environment baselines with strict host independence.
- Specify warm-up/order controls, sampling rules, collector ownership, baseline fingerprints, and any local reuse policy.
- Finalize report/exit semantics for unavailable or failed measurements, keeping execution failure separate from regression and never silently reporting timing success.
- Discuss and approve implementation slices only after the investigation updates this handoff. No merge of the investigation branch is expected.

## Research sources

- Cache scope, immutability, matching, and eviction: https://docs.github.com/en/actions/reference/workflows-and-actions/dependency-caching
- Restore-only action and matched-key output: https://github.com/actions/cache/blob/main/restore/README.md
- Fresh VM per standard hosted job: https://docs.github.com/en/actions/how-tos/write-workflows/choose-where-workflows-run/choose-the-runner-for-a-job
- Cross-run artifact download: https://github.com/actions/download-artifact#download-artifacts-from-other-workflow-runs-or-repositories

## Suggested skills

Read the relevant available `SKILL.md` files:

- `task-planning`: continue refining the proposal; do not create tasks without explicit approval.
- `architecture-review`: assess timing-baseline ownership, trust, policy separation, and workflow boundaries.
- For diagnostic code/test changes: `automatic-tests` for test design, `go-code-review` for Go changes, and `code-simplifier` after implementation.

## Validation and safety

The initial investigation and subsequent read-only review ran no tests, formatting, builds, or quality updates. The operator's policy-update run was inspected, not repeated by the agent. Continuation updates change only the handoff and use document whitespace validation. Following explicit approval, task `op-dys` was created and read back for verification. No other task lifecycle operations, experiments, commits, pushes, or PR creation have occurred in these sessions.

Follow current production `AGENTS.md`, not historical instructions in the docs worktree. Run `make check` in the assigned experimental worktree after diagnostic production/test/build changes; retain and explain any validation failures without automatically refreshing policy to hide them. Do not run executable validation for documentation-only work. Workflow setup may access GitHub and download tools, but test lanes remain network-free, credential-free, and isolated from operator data. Do not invoke live evaluations or real model agents. Do not add compatibility or migration machinery for this unreleased MVP unless requested.

Before ending the investigation, update this handoff even if experiments are inconclusive. Record the temporary PR state and any cleanup still needed; do not merge the PR or automatically delete evidence. Leave final design selection and any main-branch implementation to a later operator decision.
