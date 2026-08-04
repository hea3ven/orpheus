# Status snapshot concurrency evidence

Date: 2026-08-04

## Method

This follows the status-Beads investigation methodology in the historical
`status-beads-performance-report.html`: use the operator's normal registered
repositories, warm the filesystem, then measure `orpheus --verbose status`
wall time with Python's `time.perf_counter()`.

A serial **before** binary was built from `git archive HEAD` before this
change. The concurrent **after** binary was built from this worktree. Each
variant received one warm-up invocation. Seven before/after pairs then ran in
an interleaved order, alternating which binary ran first in each pair. Both
binaries exited successfully for every sample.

```sh
# Before binary
mkdir -p /tmp/orpheus-op-vjf-before
git archive HEAD | tar -x -C /tmp/orpheus-op-vjf-before
(cd /tmp/orpheus-op-vjf-before && go build -o /tmp/orpheus-status-before ./cmd/orpheus)

# After binary
go build -o /tmp/orpheus-status-after ./cmd/orpheus

# Each measured invocation
/tmp/orpheus-status-{before,after} --verbose status
```

Environment: Linux 7.1.3-arch1-3, amd64, 4 logical CPUs, Go 1.26.5, and Beads
1.1.2 (20e493e56). The normal registry contained four distinct embedded Beads
workspaces. All fourteen stdout payloads had the same SHA-256:
`fb7028f6df614801c8f7eb3628543a805f90bfc15396dc1dd1eff68e6d4d26ac`.

## Measurements

| Pair | Execution order | Before wall (ms) | After wall (ms) |
| ---: | --- | ---: | ---: |
| 1 | before, after | 1765.752 | 948.061 |
| 2 | after, before | 1696.640 | 863.354 |
| 3 | before, after | 1707.151 | 983.872 |
| 4 | after, before | 1610.527 | 821.761 |
| 5 | before, after | 1659.001 | 936.726 |
| 6 | after, before | 1689.587 | 878.646 |
| 7 | before, after | 1702.157 | 862.183 |
| **Median** |  | **1696.640** | **878.646** |

The median wall-time reduction is **48.2%**
(`(1696.640 - 878.646) / 1696.640`), exceeding the 35% acceptance target.

The measurements use live operator data and are intentionally point-in-time;
they establish that the production implementation overlaps the independent
Beads reads without changing the rendered `status` output for this dataset.
