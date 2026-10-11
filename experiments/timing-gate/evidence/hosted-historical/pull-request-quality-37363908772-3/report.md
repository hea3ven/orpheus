## Test quality

> [!CAUTION]
> **Timing ceiling exceeded.** A lane or package ran past its accepted timing ceiling.

### Lane results

| Lane | Result | Coverage | Test events | Selected package work | Command wall time |
| --- | --- | ---: | ---: | ---: | ---: |
| unit | **Pass** | 12007/21621 (55.53%) | 1250 | 2.07s | 27.15s |
| integration | **Pass** | 14251/21703 (65.66%) | 616 | 33.15s | 26.88s |

Command wall time measures developer wait and is diagnostic only. Selected package work sums elapsed times for packages that ran tests, including overlapping packages; timing policy governs this work.

### Blocking issues

| Location | Issue |
| --- | --- |
| integration/github.com/hea3ven/orpheus/internal/tasksource/gig | timing exceeds its policy ceiling; run make quality-policy-update if the regression is intentional (ceiling 1.038s, measured 1.226s, proposed ceiling 2.146s) |

### Warnings

| Location | Issue |
| --- | --- |
| unit/suite | timing moved below its refresh floor; run make quality-policy-update (ceiling 7.860s, measured 2.070s, proposed ceiling 3.105s) |
| unit/github.com/hea3ven/orpheus/internal/tasktarget | timing moved below its refresh floor; run make quality-policy-update (ceiling 1.038s, measured 0.111s, proposed ceiling 0.361s) |
| integration/suite | timing moved below its refresh floor; run make quality-policy-update (ceiling 102.856s, measured 33.153s, proposed ceiling 49.730s) |

<details>
<summary>Package coverage and timing</summary>

| Lane | Package | Coverage | Selected package work |
| --- | --- | ---: | ---: |
| unit | github.com/hea3ven/orpheus/cmd/orpheus | 0/4 (0.00%) | Not measured |
| unit | github.com/hea3ven/orpheus/cmd/testcoverage | 597/920 (64.89%) | 0.11s |
| unit | github.com/hea3ven/orpheus/internal/agent | 1651/1987 (83.09%) | 0.40s |
| unit | github.com/hea3ven/orpheus/internal/agentexec | 2/114 (1.75%) | 0.10s |
| unit | github.com/hea3ven/orpheus/internal/cli | 2041/5148 (39.65%) | 0.10s |
| unit | github.com/hea3ven/orpheus/internal/doctor | 253/467 (54.18%) | 0.08s |
| unit | github.com/hea3ven/orpheus/internal/git | 491/1583 (31.02%) | 0.13s |
| unit | github.com/hea3ven/orpheus/internal/logging | 19/36 (52.78%) | Not measured |
| unit | github.com/hea3ven/orpheus/internal/pathutil | 17/19 (89.47%) | Not measured |
| unit | github.com/hea3ven/orpheus/internal/publication | 76/82 (92.68%) | 0.04s |
| unit | github.com/hea3ven/orpheus/internal/pullrequest | 9/147 (6.12%) | 0.03s |
| unit | github.com/hea3ven/orpheus/internal/readiness | 34/37 (91.89%) | 0.04s |
| unit | github.com/hea3ven/orpheus/internal/registry | 262/295 (88.81%) | 0.04s |
| unit | github.com/hea3ven/orpheus/internal/review | 371/1229 (30.19%) | 0.04s |
| unit | github.com/hea3ven/orpheus/internal/revieweval | 313/548 (57.12%) | 0.08s |
| unit | github.com/hea3ven/orpheus/internal/state | 172/223 (77.13%) | 0.04s |
| unit | github.com/hea3ven/orpheus/internal/status | 312/336 (92.86%) | 0.03s |
| unit | github.com/hea3ven/orpheus/internal/task | 721/837 (86.14%) | 0.04s |
| unit | github.com/hea3ven/orpheus/internal/taskbranch | 94/100 (94.00%) | 0.03s |
| unit | github.com/hea3ven/orpheus/internal/tasksource/beads | 481/778 (61.83%) | 0.04s |
| unit | github.com/hea3ven/orpheus/internal/tasksource/gig | 51/383 (13.32%) | 0.03s |
| unit | github.com/hea3ven/orpheus/internal/taskstate | 1571/2250 (69.82%) | 0.23s |
| unit | github.com/hea3ven/orpheus/internal/taskstats | 520/599 (86.81%) | 0.03s |
| unit | github.com/hea3ven/orpheus/internal/tasktarget | 95/113 (84.07%) | 0.11s |
| unit | github.com/hea3ven/orpheus/internal/testguard | 31/53 (58.49%) | 0.03s |
| unit | github.com/hea3ven/orpheus/internal/testlane | 103/117 (88.03%) | 0.11s |
| unit | github.com/hea3ven/orpheus/internal/testutil | 25/28 (89.29%) | 0.03s |
| unit | github.com/hea3ven/orpheus/internal/workflow | 1695/3188 (53.17%) | 0.13s |
| integration | github.com/hea3ven/orpheus/cmd/orpheus | 0/4 (0.00%) | Not measured |
| integration | github.com/hea3ven/orpheus/cmd/testcoverage | 0/920 (0.00%) | 0.45s |
| integration | github.com/hea3ven/orpheus/internal/agent | 1047/1987 (52.69%) | Not measured |
| integration | github.com/hea3ven/orpheus/internal/agentexec | 94/114 (82.46%) | 0.94s |
| integration | github.com/hea3ven/orpheus/internal/cli | 3696/5148 (71.79%) | 6.49s |
| integration | github.com/hea3ven/orpheus/internal/doctor | 356/467 (76.23%) | Not measured |
| integration | github.com/hea3ven/orpheus/internal/git | 1084/1583 (68.48%) | 6.31s |
| integration | github.com/hea3ven/orpheus/internal/logging | 19/36 (52.78%) | Not measured |
| integration | github.com/hea3ven/orpheus/internal/pathutil | 17/19 (89.47%) | Not measured |
| integration | github.com/hea3ven/orpheus/internal/publication | 74/82 (90.24%) | Not measured |
| integration | github.com/hea3ven/orpheus/internal/pullrequest | 127/147 (86.39%) | 0.21s |
| integration | github.com/hea3ven/orpheus/internal/readiness | 25/37 (67.57%) | Not measured |
| integration | github.com/hea3ven/orpheus/internal/registry | 228/295 (77.29%) | Not measured |
| integration | github.com/hea3ven/orpheus/internal/review | 955/1229 (77.71%) | 0.16s |
| integration | github.com/hea3ven/orpheus/internal/revieweval | 292/548 (53.28%) | Not measured |
| integration | github.com/hea3ven/orpheus/internal/state | 227/305 (74.43%) | Not measured |
| integration | github.com/hea3ven/orpheus/internal/status | 227/336 (67.56%) | Not measured |
| integration | github.com/hea3ven/orpheus/internal/task | 555/837 (66.31%) | Not measured |
| integration | github.com/hea3ven/orpheus/internal/taskbranch | 80/100 (80.00%) | 0.04s |
| integration | github.com/hea3ven/orpheus/internal/tasksource/beads | 452/778 (58.10%) | 17.06s |
| integration | github.com/hea3ven/orpheus/internal/tasksource/gig | 325/383 (84.86%) | 1.23s |
| integration | github.com/hea3ven/orpheus/internal/taskstate | 1640/2250 (72.89%) | Not measured |
| integration | github.com/hea3ven/orpheus/internal/taskstats | 282/599 (47.08%) | Not measured |
| integration | github.com/hea3ven/orpheus/internal/tasktarget | 90/113 (79.65%) | Not measured |
| integration | github.com/hea3ven/orpheus/internal/testguard | 41/53 (77.36%) | 0.27s |
| integration | github.com/hea3ven/orpheus/internal/testlane | 0/117 (0.00%) | Not measured |
| integration | github.com/hea3ven/orpheus/internal/testutil | 22/28 (78.57%) | Not measured |
| integration | github.com/hea3ven/orpheus/internal/workflow | 2296/3188 (72.02%) | Not measured |

</details>

Machine-readable report: `artifacts/test-coverage/report.json`
