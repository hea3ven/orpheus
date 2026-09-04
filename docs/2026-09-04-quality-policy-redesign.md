# Quality policy redesign

Date: 2026-09-04

Status: Approved planning direction

## Summary

Orpheus will replace its generated coverage and timing baselines with a reviewed
repository policy in `.quality.yml`. The file will contain stable configuration
and accepted quality bounds:

- coverage floors for unit and integration lane totals;
- coverage floors for every package in each lane;
- timing ceilings for unit and integration suite totals;
- timing ceilings for every package that runs selected tests;
- significance, headroom, and timing-sample settings used to decide when a
  bound needs updating.

Routine quality checks will read this policy without changing it. A separate
`make quality-policy-update` target will measure the repository and update only
materially stale bounds, including package additions and removals. Updates may
tighten or relax the policy. The developer reviews the resulting diff before
committing it, so normal code review is the approval mechanism for both
improvements and regressions.

This design does not need a stored history of quality results, a base-branch
comparison, or CI-published baseline artifacts.

## Context

The current quality gate stores policy and generated observations together in
`coverage/test-coverage-baseline.json`. The generated portion includes exact
test counts, statement totals, covered-statement counts, package inventories,
baseline timings, and timing budgets. Significant coverage movement or
structural drift requires every affected pull request to regenerate that file.

The pull-request workflow separately reads the baseline from the exact base
commit and verifies that a pull request either leaves its own copy unchanged or
contains the expected generated replacement. This prevents an edited baseline
from concealing a regression, but it also makes the generated file shared
mutable state across feature branches.

In practice, unrelated pull requests frequently edit the same file. Merging
`main` into a branch then produces conflicts or requires another complete
baseline refresh. The current design reduces exact coverage churn compared with
the older source-block inventory, but it does not remove ownership of generated
state from feature branches.

The repository also has a separate multi-sample timing implementation and
tracked baseline under `cmd/testtiming` and
`performance/test-timing-baseline.json`. Maintaining both timing systems creates
a second policy lifecycle and two sets of commands.

The quality policy redesign keeps the useful parts of the current gate:

- one coverage-instrumented execution of each test lane during routine checks;
- independent unit and integration coverage;
- lane and package diagnostics;
- noise-tolerant timing bounds;
- complete and partial machine-readable reports;
- the existing lint and build checks;
- short-lived CI artifacts for diagnosis.

It removes generated observations that are only needed to compare one revision
with another.

## Goals

1. Make accepted quality bounds visible and reviewable in one conventional
   repository dotfile.
2. Stop routine checks and CI from modifying repository state.
3. Remove base-branch baseline lookup and verification.
4. Preserve meaningful coverage and timing regression detection at lane and
   package scope.
5. Require policy review when results move materially in either direction.
6. Keep ordinary pull-request validation to one execution of each test lane.
7. Consolidate deliberate timing updates into the same policy workflow.
8. Reduce merge conflicts by changing only bounds that crossed their configured
   update threshold.

## Non-goals

- Publishing historical quality results for later pull requests.
- Automatically committing policy changes.
- Comparing a pull request with a stored report for its base commit.
- Protecting policy bounds from ordinary reviewed pull-request edits.
- Preserving exact test counts, statement denominators, source coordinates, or
  hit inventories as persistent state.
- Keeping a second detailed timing baseline after the new policy is adopted.

## Policy ownership and review

`.quality.yml` is normal reviewed source. CI evaluates the version present in
the synthetic pull-request merge.

A pull request may raise or lower a coverage floor and may raise or lower a
timing ceiling. Such a change is visible in the policy diff and is approved or
rejected through the repository's normal review process. CI does not load a
second trusted policy from the base branch, inspect approvals, require a force
mode, or distinguish an authorized relaxation from any other policy edit.

Routine commands never write the policy. Only the explicit update command does
so, and it leaves the resulting changes in the working tree for human review.

## Policy scope

The policy covers both classified test lanes:

- `unit`;
- `integration`.

Each lane has an aggregate coverage floor and suite timing ceiling. Coverage
also has a floor for every package included in the complete production coverage
scope. Timing has a ceiling for every package that runs selected tests in that
lane. Packages with no selected tests remain excluded from package timing, as
their process startup measurements are not useful performance signals.

A new applicable package makes the policy stale until the explicit update
command adds it. A removed package makes the policy stale until the command
removes it.

Keeping every package means `.quality.yml` will not be tiny. It should still
change less often than the current baseline because it stores stable bounds,
not exact observations, and the updater leaves in-band entries untouched.

## Policy shape

The final schema may adjust names, but it should keep settings separate from
accepted bounds. A representative shape is:

```yaml
version: 1

coverage:
  repository:
    significance_percentage_points: 0.5
    headroom_percentage_points: 0.5
  package:
    significance_percentage_points: 2
    headroom_percentage_points: 2

timing:
  update_samples: 5
  suite:
    relative_headroom: 0.25
    absolute_headroom_seconds: 0.5
  package:
    relative_headroom: 0.5
    absolute_headroom_seconds: 0.25

lanes:
  unit:
    coverage:
      floor_percent: <accepted floor>
      packages:
        <package name>: <accepted floor>
    timing:
      ceiling_seconds: <accepted ceiling>
      packages:
        <package name>: <accepted ceiling>
  integration:
    coverage:
      floor_percent: <accepted floor>
      packages:
        <package name>: <accepted floor>
    timing:
      ceiling_seconds: <accepted ceiling>
      packages:
        <package name>: <accepted ceiling>
```

The initial policy should preserve the current acceptance bands rather than
silently tightening or loosening the gate during migration:

- repository coverage significance remains 0.5 percentage points per lane;
- package coverage significance remains 2 percentage points per lane;
- suite timing headroom remains the greater of 25 percent or 0.5 seconds;
- package timing headroom remains the greater of 50 percent or 0.25 seconds;
- deliberate timing updates use five samples and their median.

Serialization must be stable and package entries must have a deterministic
order so policy diffs contain only meaningful changes.

## Routine quality behavior

`make quality` remains the local and CI quality command. It runs each classified
lane once and derives test outcomes, coverage, and selected-test timings from
those executions.

The command is read-only with respect to `.quality.yml`. It reports:

- test failure when a lane does not complete successfully;
- a coverage violation when a lane or package falls below its floor;
- a timing violation when a suite or package exceeds its ceiling;
- policy update required when a result has moved far enough within the good
  direction that its accepted bound is materially stale;
- policy update required for applicable package additions or removals;
- pass when tests succeed and all results remain within accepted bounds and
  refresh bands.

A policy update requirement is blocking. The developer runs the explicit update
target, reviews the policy diff, and commits it with the associated work.

Reports remain disposable outputs under `artifacts/`. They continue to include
current lane and package values, failures, warnings, and decoded test evidence,
but they are not inputs to later runs.

## Explicit policy updates

`make quality-policy-update` is the only command that automatically changes
`.quality.yml`.

The command collects complete coverage and timing evidence, calculates proposed
bounds, and updates only entries that need refresh. It must:

- move coverage floors up after significant improvement;
- move coverage floors down after significant regression;
- move timing ceilings down after significant improvement;
- move timing ceilings up after significant regression;
- add newly applicable package bounds;
- remove obsolete package bounds;
- leave in-band bounds unchanged;
- refuse to write from failed or incomplete test measurements;
- print a summary of every changed bound.

A coverage floor is derived from measured coverage while retaining the
configured headroom. A proposed floor replaces the committed value only when
the difference crosses the applicable repository or package significance
threshold, or when the current result violates the committed floor.

A timing ceiling is derived from the measured median plus the greater of its
configured relative or absolute headroom. A proposed ceiling replaces the
committed value only when the existing ceiling has been crossed or when the
newly calculated ceiling differs materially under the same noise-tolerance
policy. This prevents every timing fluctuation from rewriting package entries.

Coverage comes from a complete coverage-instrumented lane execution. Timing
updates use five comparable coverage-instrumented samples so their values match
the execution mode used by the routine quality gate. The median suite and
package selected-test timings supply the proposed ceilings. If any required
sample fails or the package/test structure is inconsistent across samples, the
command does not update policy.

The update command does not decide whether a regression is acceptable. It
prepares the policy change. The reviewer makes that decision from the diff and
quality report.

## Pull-request gate

The pull-request workflow continues to test GitHub's synthetic merge commit. It
loads `.quality.yml` from that checkout and runs the normal read-only quality
command once, followed by lint and build without rerunning tests.

The workflow no longer:

- reads `coverage/test-coverage-baseline.json` from the base commit;
- supplies a trusted comparison baseline;
- validates a generated replacement baseline;
- has a maintainer force-baseline lifecycle;
- publishes quality state for future pull requests.

It continues to publish the current run's Markdown summary, JSON report, setup
diagnostics, and failure logs as short-lived workflow artifacts.

## Timing workflow consolidation

The separate detailed timing workflow will be retired. The new policy updater
keeps its important stability property by using repeated medians when changing
timing ceilings, while routine quality checks retain the cheaper single-pass
execution.

The final design removes:

- `performance/test-timing-baseline.json`;
- the separate timing measurement command;
- Make targets that initialize, replace, or ratchet that baseline;
- active documentation describing a second timing lifecycle.

Historical performance evidence remains unchanged as a record of prior work.

## Migration and compatibility

The migration should preserve current accepted coverage and timing bounds in
the initial `.quality.yml`. It should not combine the storage redesign with an
unreviewed quality-policy change.

A staged implementation may temporarily retain legacy files while the local
policy contract is introduced. The hosted cutover must then remove both legacy
baseline systems and their active commands so the repository finishes with one
quality policy and one update workflow.

Active agent guidance must stop instructing contributors to regenerate
`coverage/test-coverage-baseline.json`. Merge guidance should instead say to
keep the reviewed `.quality.yml` resolution and rerun the explicit update target
when current results require another policy refresh.

## Trade-offs

The design deliberately moves trust from base-relative verification to code
review. A pull request can relax a bound and make its current result pass. That
is expected and visible.

Coverage or timing can regress within retained headroom without a policy change.
This is the purpose of the noise and significance bands. Cumulative movement
eventually crosses a bound or refresh threshold because every quality run
compares the current repository state with the committed policy.

Concurrent pull requests can still conflict when they update the same lane or
package bound. They should conflict much less often because unrelated exact
measurements and denominator changes no longer rewrite the whole policy.

Removing stored observations also removes exact test-count and denominator-drift
checks. Test-lane structure remains enforced by the repository's structural
validator, and current reports continue to expose test and coverage totals for
diagnosis.

Repeated timing samples make deliberate updates slower, especially for the
integration lane. That cost occurs only when preparing a policy update, not on
every pull request.

## Proposed delivery sequence

1. Introduce `.quality.yml`, read-only evaluation, and the explicit policy
   update workflow while retaining only the temporary compatibility needed for
   the existing hosted gate.
2. Switch the pull-request workflow to the checked-in policy and retire the
   aggregate coverage baseline, trusted-base comparison, force lifecycle, and
   separate timing baseline and commands.
