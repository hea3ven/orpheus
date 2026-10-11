## Test quality

> [!CAUTION]
> **Timing ceiling exceeded.** A lane or package ran past its accepted timing ceiling.

### Lane results

| Lane | Result | Coverage | Test events | Selected package work | Command wall time |
| --- | --- | ---: | ---: | ---: | ---: |
| unit | **Pass** | 12074/21637 (55.80%) | 1259 | 2.94s | 3.82s |
| integration | **Pass** | 14288/21719 (65.79%) | 638 | 47.84s | 26.34s |

Command wall time measures developer wait and is diagnostic only. Selected package work sums elapsed times for packages that ran tests, including overlapping packages; timing policy governs this work.

### Blocking issues

| Location | Issue |
| --- | --- |
| integration/github.com/hea3ven/orpheus/internal/tasksource/gig | timing exceeds its policy ceiling; run make quality-policy-update if the regression is intentional (ceiling 2.500s, measured 3.658s, proposed ceiling 6.402s) |

<details>
<summary>Package coverage and timing</summary>

| Lane | Package | Coverage | Selected package work |
| --- | --- | ---: | ---: |
| unit | github.com/hea3ven/orpheus/cmd/orpheus | 0/4 (0.00%) | Not measured |
| unit | github.com/hea3ven/orpheus/cmd/testcoverage | 597/920 (64.89%) | 0.08s |
| unit | github.com/hea3ven/orpheus/internal/agent | 1652/1987 (83.14%) | 0.28s |
| unit | github.com/hea3ven/orpheus/internal/agentexec | 2/114 (1.75%) | 0.06s |
| unit | github.com/hea3ven/orpheus/internal/cli | 2041/5148 (39.65%) | 0.18s |
| unit | github.com/hea3ven/orpheus/internal/doctor | 253/467 (54.18%) | 0.10s |
| unit | github.com/hea3ven/orpheus/internal/git | 491/1583 (31.02%) | 0.25s |
| unit | github.com/hea3ven/orpheus/internal/logging | 19/36 (52.78%) | Not measured |
| unit | github.com/hea3ven/orpheus/internal/pathutil | 17/19 (89.47%) | Not measured |
| unit | github.com/hea3ven/orpheus/internal/publication | 76/82 (92.68%) | 0.08s |
| unit | github.com/hea3ven/orpheus/internal/pullrequest | 9/147 (6.12%) | 0.06s |
| unit | github.com/hea3ven/orpheus/internal/readiness | 34/37 (91.89%) | 0.06s |
| unit | github.com/hea3ven/orpheus/internal/registry | 262/295 (88.81%) | 0.09s |
| unit | github.com/hea3ven/orpheus/internal/review | 371/1229 (30.19%) | 0.10s |
| unit | github.com/hea3ven/orpheus/internal/revieweval | 313/548 (57.12%) | 0.11s |
| unit | github.com/hea3ven/orpheus/internal/state | 172/223 (77.13%) | 0.07s |
| unit | github.com/hea3ven/orpheus/internal/status | 312/336 (92.86%) | 0.09s |
| unit | github.com/hea3ven/orpheus/internal/task | 721/837 (86.14%) | 0.08s |
| unit | github.com/hea3ven/orpheus/internal/taskbranch | 94/100 (94.00%) | 0.06s |
| unit | github.com/hea3ven/orpheus/internal/tasksource/beads | 481/778 (61.83%) | 0.08s |
| unit | github.com/hea3ven/orpheus/internal/tasksource/gig | 51/383 (13.32%) | 0.07s |
| unit | github.com/hea3ven/orpheus/internal/taskstate | 1571/2250 (69.82%) | 0.19s |
| unit | github.com/hea3ven/orpheus/internal/taskstats | 520/599 (86.81%) | 0.06s |
| unit | github.com/hea3ven/orpheus/internal/tasktarget | 95/113 (84.07%) | 0.26s |
| unit | github.com/hea3ven/orpheus/internal/testguard | 31/53 (58.49%) | 0.06s |
| unit | github.com/hea3ven/orpheus/internal/testlane | 103/117 (88.03%) | 0.25s |
| unit | github.com/hea3ven/orpheus/internal/testutil | 25/28 (89.29%) | 0.08s |
| unit | github.com/hea3ven/orpheus/internal/workflow | 1761/3204 (54.96%) | 0.16s |
| integration | github.com/hea3ven/orpheus/cmd/orpheus | 0/4 (0.00%) | Not measured |
| integration | github.com/hea3ven/orpheus/cmd/testcoverage | 0/920 (0.00%) | 0.61s |
| integration | github.com/hea3ven/orpheus/internal/agent | 1047/1987 (52.69%) | Not measured |
| integration | github.com/hea3ven/orpheus/internal/agentexec | 94/114 (82.46%) | 1.08s |
| integration | github.com/hea3ven/orpheus/internal/cli | 3696/5148 (71.79%) | 8.42s |
| integration | github.com/hea3ven/orpheus/internal/doctor | 356/467 (76.23%) | Not measured |
| integration | github.com/hea3ven/orpheus/internal/git | 1090/1583 (68.86%) | 11.48s |
| integration | github.com/hea3ven/orpheus/internal/logging | 19/36 (52.78%) | Not measured |
| integration | github.com/hea3ven/orpheus/internal/pathutil | 17/19 (89.47%) | Not measured |
| integration | github.com/hea3ven/orpheus/internal/publication | 74/82 (90.24%) | Not measured |
| integration | github.com/hea3ven/orpheus/internal/pullrequest | 127/147 (86.39%) | 0.40s |
| integration | github.com/hea3ven/orpheus/internal/readiness | 25/37 (67.57%) | Not measured |
| integration | github.com/hea3ven/orpheus/internal/registry | 228/295 (77.29%) | Not measured |
| integration | github.com/hea3ven/orpheus/internal/review | 955/1229 (77.71%) | 0.40s |
| integration | github.com/hea3ven/orpheus/internal/revieweval | 292/548 (53.28%) | Not measured |
| integration | github.com/hea3ven/orpheus/internal/state | 227/305 (74.43%) | Not measured |
| integration | github.com/hea3ven/orpheus/internal/status | 227/336 (67.56%) | Not measured |
| integration | github.com/hea3ven/orpheus/internal/task | 555/837 (66.31%) | Not measured |
| integration | github.com/hea3ven/orpheus/internal/taskbranch | 80/100 (80.00%) | 0.10s |
| integration | github.com/hea3ven/orpheus/internal/tasksource/beads | 452/778 (58.10%) | 21.32s |
| integration | github.com/hea3ven/orpheus/internal/tasksource/gig | 325/383 (84.86%) | 3.66s |
| integration | github.com/hea3ven/orpheus/internal/taskstate | 1640/2250 (72.89%) | Not measured |
| integration | github.com/hea3ven/orpheus/internal/taskstats | 282/599 (47.08%) | Not measured |
| integration | github.com/hea3ven/orpheus/internal/tasktarget | 90/113 (79.65%) | Not measured |
| integration | github.com/hea3ven/orpheus/internal/testguard | 41/53 (77.36%) | 0.39s |
| integration | github.com/hea3ven/orpheus/internal/testlane | 0/117 (0.00%) | Not measured |
| integration | github.com/hea3ven/orpheus/internal/testutil | 22/28 (78.57%) | Not measured |
| integration | github.com/hea3ven/orpheus/internal/workflow | 2327/3204 (72.63%) | Not measured |

</details>

Machine-readable report: `/home/mati/.local/share/orpheus/repos/orpheus/worktrees/op-dys/experiments/timing-gate/evidence/local/3-S/report.json`
