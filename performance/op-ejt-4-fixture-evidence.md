# CLI fixture timing evidence

The final retained shared-helper and seeded-repository fixture implementation was
measured with three uncached CLI-package runs:

```sh
for sample in 1 2 3; do
  go test -count=1 ./internal/cli
done
```

| Source | CLI package samples (seconds) | Median |
| --- | --- | --- |
| `op-ejt.3` tracked baseline | — | 21.346 |
| Final implementation | 14.335, 14.337, 14.374 | 14.337 |

The final median is 32.8% below the tracked baseline,
exceeding the required 10% reduction (19.211 seconds). The historical mixed-lane CLI package
median and its 50% tolerance budget were ratcheted in
`performance/test-timing-baseline.json` to 14.337 seconds and
21.505 seconds, respectively.
