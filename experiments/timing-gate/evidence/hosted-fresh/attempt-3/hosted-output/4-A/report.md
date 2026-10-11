## Test quality

> [!TIP]
> **Quality checks passed.** No blocking test, coverage, or timing issues were found.

### Lane results

| Lane | Result | Coverage | Test events | Selected package work | Command wall time |
| --- | --- | ---: | ---: | ---: | ---: |
| unit | **Pass** | 12073/21637 (55.80%) | 1259 | 2.11s | 2.39s |
| integration | **Pass** | 14288/21719 (65.79%) | 638 | 27.27s | 16.47s |

Command wall time measures developer wait and is diagnostic only. Selected package work sums elapsed times for packages that ran tests, including overlapping packages; timing policy governs this work.

### Warnings

| Location | Issue |
| --- | --- |
| unit/suite | timing moved below its refresh floor; run make quality-policy-update (ceiling 7.860s, measured 2.107s, proposed ceiling 3.161s) |
| integration/suite | timing moved below its refresh floor; run make quality-policy-update (ceiling 102.856s, measured 27.266s, proposed ceiling 40.899s) |

<details>
<summary>Package coverage and timing</summary>

| Lane | Package | Coverage | Selected package work |
| --- | --- | ---: | ---: |
| unit | github.com/hea3ven/orpheus/cmd/orpheus | 0/4 (0.00%) | Not measured |
| unit | github.com/hea3ven/orpheus/cmd/testcoverage | 597/920 (64.89%) | 0.07s |
| unit | github.com/hea3ven/orpheus/internal/agent | 1651/1987 (83.09%) | 0.24s |
| unit | github.com/hea3ven/orpheus/internal/agentexec | 2/114 (1.75%) | 0.03s |
| unit | github.com/hea3ven/orpheus/internal/cli | 2041/5148 (39.65%) | 0.12s |
| unit | github.com/hea3ven/orpheus/internal/doctor | 253/467 (54.18%) | 0.08s |
| unit | github.com/hea3ven/orpheus/internal/git | 491/1583 (31.02%) | 0.16s |
| unit | github.com/hea3ven/orpheus/internal/logging | 19/36 (52.78%) | Not measured |
| unit | github.com/hea3ven/orpheus/internal/pathutil | 17/19 (89.47%) | Not measured |
| unit | github.com/hea3ven/orpheus/internal/publication | 76/82 (92.68%) | 0.05s |
| unit | github.com/hea3ven/orpheus/internal/pullrequest | 9/147 (6.12%) | 0.04s |
| unit | github.com/hea3ven/orpheus/internal/readiness | 34/37 (91.89%) | 0.04s |
| unit | github.com/hea3ven/orpheus/internal/registry | 262/295 (88.81%) | 0.06s |
| unit | github.com/hea3ven/orpheus/internal/review | 371/1229 (30.19%) | 0.06s |
| unit | github.com/hea3ven/orpheus/internal/revieweval | 313/548 (57.12%) | 0.09s |
| unit | github.com/hea3ven/orpheus/internal/state | 172/223 (77.13%) | 0.05s |
| unit | github.com/hea3ven/orpheus/internal/status | 312/336 (92.86%) | 0.04s |
| unit | github.com/hea3ven/orpheus/internal/task | 721/837 (86.14%) | 0.05s |
| unit | github.com/hea3ven/orpheus/internal/taskbranch | 94/100 (94.00%) | 0.05s |
| unit | github.com/hea3ven/orpheus/internal/tasksource/beads | 481/778 (61.83%) | 0.06s |
| unit | github.com/hea3ven/orpheus/internal/tasksource/gig | 51/383 (13.32%) | 0.04s |
| unit | github.com/hea3ven/orpheus/internal/taskstate | 1571/2250 (69.82%) | 0.25s |
| unit | github.com/hea3ven/orpheus/internal/taskstats | 520/599 (86.81%) | 0.04s |
| unit | github.com/hea3ven/orpheus/internal/tasktarget | 95/113 (84.07%) | 0.16s |
| unit | github.com/hea3ven/orpheus/internal/testguard | 31/53 (58.49%) | 0.04s |
| unit | github.com/hea3ven/orpheus/internal/testlane | 103/117 (88.03%) | 0.15s |
| unit | github.com/hea3ven/orpheus/internal/testutil | 25/28 (89.29%) | 0.03s |
| unit | github.com/hea3ven/orpheus/internal/workflow | 1761/3204 (54.96%) | 0.11s |
| integration | github.com/hea3ven/orpheus/cmd/orpheus | 0/4 (0.00%) | Not measured |
| integration | github.com/hea3ven/orpheus/cmd/testcoverage | 0/920 (0.00%) | 0.41s |
| integration | github.com/hea3ven/orpheus/internal/agent | 1047/1987 (52.69%) | Not measured |
| integration | github.com/hea3ven/orpheus/internal/agentexec | 94/114 (82.46%) | 0.68s |
| integration | github.com/hea3ven/orpheus/internal/cli | 3696/5148 (71.79%) | 4.98s |
| integration | github.com/hea3ven/orpheus/internal/doctor | 356/467 (76.23%) | Not measured |
| integration | github.com/hea3ven/orpheus/internal/git | 1090/1583 (68.86%) | 6.27s |
| integration | github.com/hea3ven/orpheus/internal/logging | 19/36 (52.78%) | Not measured |
| integration | github.com/hea3ven/orpheus/internal/pathutil | 17/19 (89.47%) | Not measured |
| integration | github.com/hea3ven/orpheus/internal/publication | 74/82 (90.24%) | Not measured |
| integration | github.com/hea3ven/orpheus/internal/pullrequest | 127/147 (86.39%) | 0.23s |
| integration | github.com/hea3ven/orpheus/internal/readiness | 25/37 (67.57%) | Not measured |
| integration | github.com/hea3ven/orpheus/internal/registry | 228/295 (77.29%) | Not measured |
| integration | github.com/hea3ven/orpheus/internal/review | 955/1229 (77.71%) | 0.19s |
| integration | github.com/hea3ven/orpheus/internal/revieweval | 292/548 (53.28%) | Not measured |
| integration | github.com/hea3ven/orpheus/internal/state | 227/305 (74.43%) | Not measured |
| integration | github.com/hea3ven/orpheus/internal/status | 227/336 (67.56%) | Not measured |
| integration | github.com/hea3ven/orpheus/internal/task | 555/837 (66.31%) | Not measured |
| integration | github.com/hea3ven/orpheus/internal/taskbranch | 80/100 (80.00%) | 0.06s |
| integration | github.com/hea3ven/orpheus/internal/tasksource/beads | 452/778 (58.10%) | 13.57s |
| integration | github.com/hea3ven/orpheus/internal/tasksource/gig | 325/383 (84.86%) | 0.63s |
| integration | github.com/hea3ven/orpheus/internal/taskstate | 1640/2250 (72.89%) | Not measured |
| integration | github.com/hea3ven/orpheus/internal/taskstats | 282/599 (47.08%) | Not measured |
| integration | github.com/hea3ven/orpheus/internal/tasktarget | 90/113 (79.65%) | Not measured |
| integration | github.com/hea3ven/orpheus/internal/testguard | 41/53 (77.36%) | 0.24s |
| integration | github.com/hea3ven/orpheus/internal/testlane | 0/117 (0.00%) | Not measured |
| integration | github.com/hea3ven/orpheus/internal/testutil | 22/28 (78.57%) | Not measured |
| integration | github.com/hea3ven/orpheus/internal/workflow | 2327/3204 (72.63%) | Not measured |

</details>

Machine-readable report: `/home/runner/work/orpheus/orpheus/experiments/timing-gate/hosted-output/4-A/report.json`
