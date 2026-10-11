# Timing-gate investigation (experimental, do not merge as a gate)

This directory retains diagnostics for `op-dys`. It does not change the production
collector, policy, or scheduling. On this branch only, the PR workflow is replaced
with a bounded diagnostic workflow; **never merge this experimental branch**. The operator-facing report is
`/home/mati/Documents/prog/orpheus-docs/timing-gate-handoff.md`.

## Reproduce local collection

From the repository root, with the revision and tools from
[`evidence/local/manifest.json`](evidence/local/manifest.json) and dependencies
already installed:

```sh
python3 experiments/timing-gate/collect.py experiments/timing-gate/evidence/new-local
```

The output directory must be new and inside this worktree. Do not run other builds
or test lanes concurrently. The driver compiles the **unchanged** Go collector
once, excludes one full warm-up of each source variant, then runs 15 full samples
in order `ABS, SBA, BAS, SAB, ASB`. Each full sample is unit then integration;
Go's default package concurrency, `-parallel=1`, coverage instrumentation and
`ORPHEUS_COVERAGE_RUN=1` are preserved. `A` and `B` are byte-identical sources;
`S` adds `time.Sleep(3 * time.Second)` to one existing Gig integration test via a
Go overlay. The repository sources are never patched. The same physical checkout
and shared build cache are used, **not** a true isolated-checkout PR comparison.
The empty base overlay and slow overlay are explicit in each sample's `GOFLAGS`.

The run bounds are 17 full samples, 180 seconds per collector invocation, and
1,200 seconds for the measurement loop. Setup/build time is separately recorded;
setup is not included in that loop budget. The driver stops at the first failed
or incomplete sample. A complete report with a nonzero *policy* result remains
evidence, not a successful gate. It never invokes a policy update. The current
collector still requires a valid policy before collection; production separation
of collection and assessment is a **recommendation**, not implemented here.

The manifest records revision/tree, tools, collector source and binary hashes,
driver/source-overlay hashes, CPU/Go scheduling information, and policy hash.
`order.json` and `samples.json` retain execution order, wall cost, command status,
and load diagnostics. Each sample retains the collector's original `report.json`,
`report.md`, and command output. These JSON reports are unaggregated raw samples;
the collector does not retain successful `go test` event streams or coverprofiles.

`collect.py` is Linux-specific (process groups, affinity, `/proc`); it is not a
supported local timing command. `.scratch/` holds disposable binaries and a runtime
probe, is ignored by Git and Go's `./...` traversal, and is not evidence.

## Recompute this report

```sh
python3 -m unittest discover -s experiments/timing-gate -p 'test_*.py'
python3 experiments/timing-gate/analyze.py experiments/timing-gate/evidence
(cd experiments/timing-gate/evidence && sha256sum -c SHA256SUMS)
```

The analysis expects the retained `local/` and `hosted-historical/` layout. It
checks report completeness, lane success, within-revision count/package/command
consistency, and timing arithmetic. Across revisions it permits count and package
changes, compares common packages plus whole-lane work, and lists additions and
removals. It does **not** authenticate provenance or enforce environment
compatibility and must not be used as a CI gate. Missing/invalid inputs fail the
analysis rather than produce a timing pass. The checksum inventory covers the
retained evidence, not itself or this documentation.

## Hosted evidence and limits

The operator explicitly authorized diagnostic commits and a temporary PR during
review repair. Draft PR #145 is experimental/do-not-merge. The original workflow
at `9838019` ran on PR opening; one rerun consumed job 2. That rerun made the first
artifact unavailable (saved artifact ID returned 404), so job 2 stopped before
measurements. Its failure evidence is retained, not silently retried.

The revised branch-only workflow consumes the final budgeted job on a workflow
file push, collecting the slowdown control even when published input is missing;
the missing input still explicitly fails the job. **Do not rerun or modify/push
the workflow again without operator approval for more jobs.** Evidence-only
pushes do not trigger it. Each job has a 20-minute timeout. The driver modes
1/2 select unchanged A/B and mode 3 overlays the slowdown on B; mode numbers are
not necessarily GitHub attempt numbers. Each mode uses two excluded warm-ups and
five strictly alternating pairs (AB first in modes 1/3, BA first in mode 2).
Use `collect.py NEW_OUTPUT MODE` to reproduce that order.

Each fresh runner provisions fixed Go/Beads/Dolt versions without restored build
or tool caches. Both variants share a checkout/cache within the job; this remains
a diagnostic overlay experiment, not isolated-checkout production enforcement.
The workflow attempts to download the exact original producer artifact through
the GitHub API and verify its ZIP digest. Missing input is not a timing pass.
The original artifact was downloaded and verified locally before the rerun;
`analyze_hosted.py evidence/hosted-fresh` compares the retained reports offline.
Incomplete environment fingerprints remain unevaluated even when numerical
arithmetic is available. No cache authority or production publisher is installed.

The original local driver is retained at `evidence/local/original-collect.py`;
its SHA-256 matches the original manifest despite the later hosted extension.
Consult the external handoff for actual outcomes, run links, and limitations.

`hosted-historical/` archives run
[37363908772](https://github.com/hea3ven/orpheus/actions/runs/37363908772), attempts
3–7: original artifact ZIPs, extracted reports/logs, GitHub artifact/run/job/commit
metadata and full attempt logs. All five ZIP SHA-256 values match the digests from
GitHub's API (`artifact-digests.json`). These files were downloaded read-only; no
historical run was rerun. They show numerical cross-job variation at one tested
synthetic merge SHA, but lack full effective scheduling fingerprints. Images vary
between attempts. They are **not** five samples from one publisher job, a fresh
controlled experiment, or proof of same-runner behavior on hosted runners.

The evidence is retained in this non-temporary worktree rather than solely in
expiring Actions artifacts. Evidence is archived on the experimental task branch
and downloaded into `evidence/hosted-fresh/` after each run. Preserve this directory
before removing the worktree. Do not merge the experiment merely to preserve its
evidence. The external handoff remains uncommitted.
