# Quality package scheduling evidence

## Execution contract

The quality runner no longer supplies `-p=1`. Go schedules packages using its
normal limit. `-parallel=1` remains in both lanes and scenario commands. Unit
and integration lanes still run in sequence, once each during a routine check.
No test-kind reporting or policy was added.

Console and Markdown reports name selected package work separately from command
wall time. The JSON names remain `selected_test_seconds` and `wall_seconds`.
Work is the sum of elapsed times for packages with selected tests, not CPU time
and not the sum of nested test durations. Concurrent packages contribute their
full elapsed times. Compilation and scheduling affect command wall time, which
never supplies a policy bound.

Policy refreshes still use five complete samples. They now also reject changed
recorded commands, including scheduling flags, while ignoring temporary profile
paths. Wall time uses its own median in the refresh report instead of retaining
the first sample's wait. Coverage and timing policy ownership is unchanged.

## The historical 21-statement difference

A controlled CLI integration comparison reproduces exactly the difference
reported in the September architecture review. Both commands used
`-count=1 -parallel=1 -tags=integration -run '^TestIntegration' -covermode=set
-coverpkg=./...`, targeting `./internal/cli`. The only environment difference was
whether `ORPHEUS_COVERAGE_RUN=1` was present.

| Environment | CLI covered statements | CLI statements |
| --- | ---: | ---: |
| Quality environment, ancestry guard enabled | 3,571 | 5,073 |
| Same environment, ancestry guard omitted | 3,592 | 5,073 |

All changed blocks are in `internal/cli/watch_linux.go`. Without the guard,
`runningUnderWatch` calls `processAncestryContains` and `linuxProcessInfo`, adding
22 covered statements in launcher-ancestry traversal and `/proc` parsing. The
one-statement guarded return becomes uncovered. Net change: **21 statements**.
This is incidental launcher coverage, not extra tests or scheduling behavior.

The guard and its focused unit regression already existed. The quality runner
continues to set it through `coverageEnvironment` for both schedules. No CLI
production change or coverage-floor relaxation is needed to resolve this
difference. Direct `go test` comparisons must use the same environment; removing
only `-p=1` from a manually reconstructed command is not enough. The historical
raw profiles were not available in this checkout, so the diagnosis is backed by
this controlled reproduction rather than a claimed comparison of those files.

The pre-change full-repository measurements selected identical tests in all
three serial/concurrent pairs. All six integration profiles matched exactly.
Unit coverage varied by one statement at `internal/agent/completion.go:116`,
where the existing child-PID/completion concurrency test sometimes entered the
lock retry loop. A new unit test holds the mutation lock and calls completion
with a canceled context. It asserts `context.Canceled` and deterministically
covers the retry select and cancellation return without sleeping or starting a
child executable. The original concurrency test remains unchanged.

## Measurement method

Measurements run on Linux amd64, four available CPUs, with
`go1.27.1-X:nodwarf5`. Both schedules use the same checkout, build cache, test
selectors, coverage instrumentation, and sanitized quality environment.
`-count=1` prevents result caching. No other validation runs overlap the timing
samples. Each of three pairs alternates schedule order. Each schedule runs the
complete unit lane followed by the complete integration lane.

The serial command adds `-p=1` to the concurrent command:

```text
go test -json -count=1 -parallel=1 -covermode=set -coverpkg=./... -coverprofile=<profile> ./...
go test -json -count=1 -parallel=1 -covermode=set -coverpkg=./... -coverprofile=<profile> -tags=integration -run ^TestIntegration ./...
```

The environment matches `coverageEnvironment`: remove `NO_COLOR`,
`ORPHEUS_AGENT_PURPOSE`, `ORPHEUS_ALTERNATE_REVIEWER_PROFILE`,
`ORPHEUS_EXHAUSTIVE_REVIEW_CONTEXT`, `ORPHEUS_RESUME_SESSIONS`,
`ORPHEUS_REVIEWER_ROLE`, `CODEX_HOME`, `PI_CODING_AGENT_DIR`, and
`PI_CODING_AGENT_SESSION_DIR`; set `ORPHEUS_COVERAGE_RUN=1`.

Comparison uses sorted package/test identities from `run` events and sorted
terminal test outcomes, not just counts. Coverage unions duplicate block records
by source position and statement count. Zero-statement blocks are omitted from
the equivalence hash because their counters carry no statement coverage. Wall
time is measured around each command with a monotonic clock. Selected package
work uses package terminal event durations, excluding packages without selected
tests.

Raw profiles, event streams, reproduction script, and initial measurements are
retained locally under `artifacts/test-coverage/scheduling/`. The checked-in
[measurement JSON](op-sc7-2-scheduling-measurements.json) records all final
samples and the ancestry block difference.

## Results

All twelve final lane commands passed. Each lane has identical selected test
identities, terminal outcomes, and normalized positive-statement coverage across
its six executions. Unit runs pass 1,106 test events and cover 11,419 of 20,917
statements. Integration runs pass 516 test events and cover 13,953 of 20,981
statements. The existing integration build adds integration-only support code,
so the two lanes' denominators differ; each is stable across scheduling modes.

| Lane | Serial median wall | Concurrent median wall | Serial median work | Concurrent median work |
| --- | ---: | ---: | ---: | ---: |
| Unit | 7.854s | 3.856s | 1.896s | 3.240s |
| Integration | 45.007s | 30.848s | 38.015s | 61.430s |

The sum of lane wall medians falls from 52.861s to 34.704s, a **34.3% reduction**.
Elapsed package work increases because packages contend for the same host
resources. This is why the report must not call reduced waiting reduced work,
and why serialized timing bounds cannot simply be reused without measurement.

## Initial policy review and validation

The initial concurrent `make quality` passed all test bodies and coverage floors
but exceeded timing ceilings. `make quality-policy-update` then collected five
complete comparable concurrent samples. The reviewed generated diff changes
only nine timing bounds:

| Bound | Prior ceiling | Measured median | New ceiling |
| --- | ---: | ---: | ---: |
| Unit suite work | 2.411s | 3.295s | 4.119s |
| Integration `cmd/testcoverage` | 0.589s | 0.734s | 1.101s |
| Integration `internal/agentexec` | 0.644s | 0.883s | 1.325s |
| Integration `internal/cli` | 11.054s | 13.380s | 20.070s |
| Integration `internal/doctor` | 0.658s | 0.737s | 1.106s |
| Integration `internal/git` | 5.178s | 9.214s | 13.821s |
| Integration `internal/review` | 2.888s | 3.874s | 5.811s |
| Integration `internal/testguard` | 0.489s | 0.597s | 0.896s |
| Integration `internal/workflow` | 1.053s | 1.190s | 1.785s |

No coverage floor, policy headroom, bound-calculation rule, or integration suite
ceiling changed. No wall-time bound was introduced. The five-sample integration work
median was 60.900s, within its existing 62.151s ceiling.

Validation passed:

- `make check`, including formatting, both quality lanes, lint, and CLI build.
- A separate final `make quality`.
- A serialized runner control with `GOFLAGS=-p=1 go run ./cmd/testcoverage`.
  It returns the same passing test/coverage decision and package coverage as the
  final concurrent report, with expected non-blocking faster-work warnings.
  This control was not used to update concurrent policy.
- `go vet ./...` and `go vet -tags=integration ./...`.
- Five race-instrumented repetitions of `./cmd/testcoverage` and
  `./internal/agent`.
- `git diff --check`.

One intervening post-check quality run exceeded the unchanged review-evaluation
package ceiling by 0.003s, measuring 0.482s against 0.479s. The next standalone
run passed without code or policy changes. This observation is retained in the
measurement JSON; no bound was loosened from that single noisy sample. Timing
ceilings remain sensitive to host contention even though wall time is not
policy.

## Review follow-up: restore concurrent timing headroom

Review reproduced two integration suite overruns at 62.887s and 62.616s, plus
another `internal/revieweval` overrun at 0.497s. The initial 60.900s suite median
left only 2.0 percent headroom under the unchanged 62.151s ceiling. The updater
had retained that bound because the median had not crossed its refresh threshold.
Passing a subsequent run did not resolve the repeated failures.

The repair ran `make quality-policy-update` again on the same four-CPU host and
Go version. It collected five complete concurrent samples, with sequential lanes
and `-parallel=1`, and validated matching commands, test counts, coverage, and
package structure. No serial controls or overlapping validation commands entered
this sample set. The generated aggregate report is retained as
`review_followup.policy_update` in the measurement JSON. The runner retains
medians rather than individual sample reports.

| Bound | Prior ceiling | Fresh five-sample median | Repaired ceiling |
| --- | ---: | ---: | ---: |
| Integration suite work | 62.151s | 67.613s | 84.516s |
| Integration `internal/revieweval` | 0.479s | 0.426s | 0.676s |

The updater generated the suite change using 25 percent headroom. Its threshold
left `internal/revieweval` unchanged because the median was below 0.479s. The
review-requested package refresh therefore applies the existing formula directly
to that same validated median: `0.426 + max(0.426 * 0.5, 0.250) = 0.676`.
This is an explicit policy edit for the repeat offender, not a change to updater
thresholds. No other policy values, coverage floors, scheduling, or runner code
changed during this repair. Wall time remains diagnostic only.

The pre-update quality run failed timing bounds, with 8.127s unit work and
83.987s integration work. All test bodies and coverage passed. Host load was
higher during that run; its single-run timings did not determine the new bounds.
The complete five-sample update measured 3.796s median unit work and left unit
policy unchanged.

After the repair, three consecutive standalone `make quality` runs and
`make check` passed without intervening policy changes:

| Validation | Unit work | Integration work | Integration wall | Integration `revieweval` |
| --- | ---: | ---: | ---: | ---: |
| Standalone 1 | 3.420s | 61.056s | 30.673s | 0.492s |
| Standalone 2 | 3.459s | 61.666s | 31.090s | 0.432s |
| Standalone 3 | 3.333s | 60.708s | 30.590s | 0.427s |
| `make check` | 3.528s | 61.114s | 30.647s | 0.392s |

The first standalone run would still have failed the old `revieweval` ceiling.
All four reports retain 1,106 unit test events and 516 integration test events,
with the same per-package coverage as the update report. `make check` also
passed formatting, lint with zero issues, and the CLI build. Repair logs and
full reports remain locally under `artifacts/test-coverage/policy-repair/`.
