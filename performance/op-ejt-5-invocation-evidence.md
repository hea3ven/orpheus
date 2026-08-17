# Invocation-scoped CLI timing evidence

The final three uncached historical mixed-lane samples were collected with:

```sh
PERF_SAMPLES=3 make test-perf-baseline-update
```

| Sample | Wall time |
| --- | ---: |
| 1 | 17.69s |
| 2 | 14.02s |
| 3 | 14.13s |
| **Median** | **14.13s** |

The measurement ran on the recorded 4-CPU reference environment and is below
the 21.5-second task target. `performance/test-timing-baseline.json` was
ratcheted to the 14.13-second suite median and 17.66-second suite budget.

## Parallel CLI workflow suite

Existing task, repository, status, doctor, diagnostics, completion, and task
creation workflows now construct commands from isolated invocation
Dependencies. The full `internal/cli` package was measured three times
uncached, both with normal parallel scheduling and with `-parallel=1` as its
serial control:

```sh
go test ./internal/cli -count=1
go test ./internal/cli -count=1 -parallel=1
```

| Mode | Samples | Median wall time |
| --- | --- | ---: |
| Serial control (`-parallel=1`) | 15.769s, 15.783s, 16.053s | 15.783s |
| Invocation-scoped parallel | 8.367s, 8.377s, 8.491s | 8.377s |

This is a 46.9% reduction for the CLI workflow suite. The historical mixed-lane timing
report records an `internal/cli` median of 11.781s (previously 14.337s) while
including concurrent packages. The remaining serial CLI tests are documented
in `internal/cli/TESTING.md`. Five shuffled repetitions and race validation of
the package complete without ordering failures or races.
