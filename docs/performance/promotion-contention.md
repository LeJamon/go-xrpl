# Promotion mutation contention

Issue #1867 concerns a possible foreground-write stall during changed-version promotion rereads. The live-node samples attached to the issue did not capture this fallback, so these development measurements do not establish it as the cause of missed validations.

## Reproduction

```sh
go test ./storage/kvstore/pebble -run '^$' \
  -bench '^BenchmarkPromotionContention$' -benchtime=1000x -count=3 -timeout 5m
```

The fixture contains 4,096 persisted writable records of 4 KiB each, three promotion workers requesting 256-key/4 MiB batches, and either no writer, one writer paced by a 100 µs ticker, or one continuously saturated writer. Ticks can be delayed or dropped when the writer cannot keep up. Foreground keys differ from promotion keys but share mutation stripes. Promotions are read-only writable hits, isolating false invalidation and reread overhead. The fixture warms the cache before timing; 1 MiB remains smaller than its working set, while 64 MiB initially holds it. Sustained writes can subsequently flush and evict cache entries. This measures cache pressure on local storage, not cold operating-system caches or HDD/ZFS latency.

Foreground p95/p99 pool all writes during each benchmark iteration; reported comparisons take the median of three runs. In the saturated case the writer runs continuously, so its sample count and mutation pressure can increase when locks are released sooner. Mismatches count invalidated key observations, including repeated retry observations, rather than distinct keys or live-node event frequency.

## Results

Raw runs: [before](promotion-contention-before.txt), [after](promotion-contention-after.txt).

Measured September 8, 2026. Baseline: storage implementation at `70b6aad9ff5359d31a58b36c0cc956aa9b3ecba7`. Host: Apple M3 Pro, darwin/arm64, Go 1.26.1, GOMAXPROCS 11. The baseline comparison adds only a per-call mismatch counter to the original changed-version branch.

| Cache | Writer | Promotion ms/group, before → after | Put p95 µs, before → after | Put p99 µs, before → after |
| --- | --- | ---: | ---: | ---: |
| 1MiB | none | 0.504 → 0.298 | — | — |
| 1MiB | paced | 0.495 → 0.364 | 382.500 → 36.875 | 543.416 → 105.541 |
| 1MiB | saturated | 0.977 → 2.421 | 2.250 → 2.417 | 58.000 → 16.584 |
| 64MiB | none | 0.449 → 0.162 | — | — |
| 64MiB | paced | 0.363 → 0.262 | 352.500 → 72.166 | 597.333 → 161.958 |
| 64MiB | saturated | 0.314 → 1.313 | 3.583 → 3.458 | 182.333 → 56.375 |

| Cache | Writer | Invalidated observations/key, before → after | Single-stripe rereads/key, after | Cache misses/group, before → after | Foreground writes/run, before → after |
| --- | --- | ---: | ---: | ---: | ---: |
| 1MiB | none | 0.5982 → 0.0000 | 0.0000 | 59.29 → 33.12 | 0 → 0 |
| 1MiB | paced | 0.5454 → 0.0333 | 0.0030 | 59.05 → 46.26 | 3457 → 3354 |
| 1MiB | saturated | 0.8313 → 1.8710 | 0.9177 | 65.50 → 632.20 | 129648 → 1307480 |
| 64MiB | none | 0.5753 → 0.0000 | 0.0000 | 0.00 → 0.00 | 0 → 0 |
| 64MiB | paced | 0.5980 → 0.0168 | 0.0009 | 0.00 → 0.00 | 2499 → 1965 |
| 64MiB | saturated | 0.7259 → 0.7273 | 0.2354 | 0.00 → 0.97 | 56738 → 209603 |

All runs consumed the full 256-key group in one native call. With no foreground writes, read-only promotions produce zero invalidations. With paced writes, both promotion throughput and foreground latency improve in this fixture. Saturation admits substantially more writes and reduces write p99, but promotion throughput declines and cache-pressure rereads increase. This is an explicit throughput/foreground-latency tradeoff under continuous mutation, not a universal performance improvement. Timing varied between runs on this development host.

## Progress and remaining lock costs

The batch performs an initial off-lock writable read and at most two selective retries. A cached record is reused only when its stripe version still matches. If retries exhaust, keys are resolved in order with only one mutation stripe held at a time; still-valid records need no reread. This guarantees finite progress without a retryable-error loop or an empty successful prefix. Byte limits, oversized-first handling, writable precedence, and archive/generation exclusion remain in effect. A failed scalar fallback can have safely copied an earlier prefix; a later retry remains idempotent.

A cold single-key fallback can still delay another key sharing its stripe. Promotion commits still hold their mutation stripes across `batch.Write()`, whose tail is not isolated by this benchmark. The fix eliminates broad-stripe cold rereads; it does not claim to eliminate every persistence stall. `PromotionStats.VersionMismatches`, `Retries`, and `Fallbacks` expose per-call contention diagnostics without adding clocks to the hot path.
