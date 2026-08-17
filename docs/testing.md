# Testing

Orpheus separates tests by behavioral scope, not by speed or whether a test writes to disk.

## Commands

- `make test-unit` runs the explicit unit lane.
- `make test` is a compatibility alias for `make test-unit`.
- `make test-integration` runs the integration lane only.
- `make check` formats code, runs each lane once, then lints.

## Unit lane

A unit test exercises package-owned logic with injected collaborators or fakes. It requires only Go and may use isolated temporary files. It must not require or start Git, Beads, gh, Codex, Pi, or any other child executable.

## Integration lane

An integration test verifies a cross-package workflow or an isolated local process contract. This includes real Git and Beads behavior, compiled CLI behavior, and child-process contracts. It may use temporary directories, local bare Git remotes, and fake executables, but it remains network-free and credential-free.

`make test-integration` requires `git` and `bd` on `PATH`. Tests that need `dolt` skip when it is unavailable. No lane may read operator data, use live networks or credentials, or run a real model agent.

## Structural membership

Integration source files use `//go:build integration`, and their top-level test bodies begin with `TestIntegration`. Untagged test bodies whose names do not have that prefix are unit tests. `internal/testlane` validates this convention so every top-level body is selected by exactly one lane. The build constraint is the membership mechanism; the integration name filter only limits execution to structurally tagged integration bodies.

## Coverage baseline

`make coverage` runs both classified lanes serially with `-coverpkg=./...`,
normalizes the multi-package profiles, and checks them against the tracked
`coverage/test-coverage-baseline.json`. Each lane is profiled twice and its hit
states are unioned without inflating the recorded per-profile test-event count.
The coverage launcher also makes process-ancestry probes deterministic, so its
own launch depth cannot change the tracked profile. Both primary lane percentages
use the same complete
production-statement denominator. The command also leaves its full local
report in `artifacts/test-coverage/report.json`; raw profiles are created in a
temporary directory and are not retained.

Use `make coverage-baseline` after intentional test or production changes to
regenerate the normalized baseline. It records each lane's identity, test-event
count, Go version, command, package/file totals, and normalized source blocks
with source-content fingerprints. Comparisons align repeated fingerprints by
their shared source-line offset. Equal alignments are retained and surfaced as
an ambiguous source-match review signal, rather than silently favoring either
an insertion or a deletion. A missing or stale baseline (its lane test-event count,
command metadata, production-statement inventory, package/file aggregates, or
normalized hit states changed) fails `make coverage` and the pull-request
workflow. Go version and temporary-profile paths are audit
metadata, not freshness inputs. This validation runs on the deterministic CI
runner too, so pull-request deltas always compare with the actual baseline on
main.

The pull-request workflow compares the checked-in result against the base
branch baseline and writes unit, integration, combined, and
integration-marginal values to the GitHub Actions job summary. It also reports
package/file changes plus file, range, statement count, and lane information
for newly covered and newly uncovered unchanged statements. It uses only
`contents: read` and the job summary, so it does not need PR-comment write
permission or a paid GitHub feature.

`make coverage-audit` is deliberately on-demand. It profiles each integration
top-level scenario separately and reports runtime, containment in the full
integration profile, Jaccard similarity with the other scenarios, and exclusive
statements. It is too expensive for routine pull requests.

Workflow activation is coordinated with cross-repository task `server-8ls`,
which establishes the Terraform-managed Actions budget. The checked-in
workflow is intentionally dispatch-only for the initial baseline rollout.
After the baseline has landed on `main` and `server-8ls` is complete, enable its
documented `pull_request` trigger. This repository adds no GitHub account or
budget configuration.

## Timing baseline

`make test-perf` and `make test-perf-integration` collect five uncached samples by default and compare median timings against `performance/test-timing-baseline.json`. A report includes lane-specific environment metadata, test-event counts, package timings, and the full JSON output plus assertion output for a failed sample. Failed or incomplete reports are retained but never checked against or used to update a baseline. A changed test-event count fails the comparison so an obsolete baseline cannot look comparable.

Use `make test-perf-baseline` or `make test-perf-integration-baseline` only after a complete, stable set of samples to regenerate that lane's baseline. The corresponding `*-update` target only ratchets budgets down after an optimization; a changed test-event count requires regeneration instead.
