# SHAMap promotion measurements

## Method

The benchmark compares scalar promotion (`batch=0`) with bounded batches of 64 and 256 nodes at one, two, and four workers. The byte budget is 4 MiB; a single oversized record may exceed it so traversal always progresses.

Each fixture has 16,384 synthetic state leaves and a deterministic genesis and close time. Both generations are closed, explicitly flushed offline, and reopened before each timed traversal. For cold cases the 256 KiB shared Pebble block cache is smaller than the persisted SSTables, checked at runtime. Warm cases use an 8 MiB cache and assert cache hits during the timed traversal. NodeStore decoded caches are disabled. `warm=false` starts with a fresh Pebble cache; `warm=true` performs one untimed traversal first. The OS page cache is not evicted, so cold refers to Pebble blocks and decompression, not cold physical storage.

Fixture creation, warming, metric snapshots, rotation, offline flush, reopen, and cleanup are excluded from benchmark timing. GC runs after fixture preparation and before each timed iteration so construction garbage does not carry into the measurement. Timing includes traversal, verification, promotion, and final Sync. Three iterations per result and three independent repetitions are collected in CI; tables use medians. Hardware performance assertions are deliberately absent from tests.

The concurrent benchmark starts real `persistToNodeStore` operations after refresh begins. Each iteration writes 32 successive closed ledgers with 32 changes each through the same Service and writable NodeStore, including ledger headers and Sync. It reports all persistence latencies and, separately, operations whose start overlaps refresh. The latter prevents post-refresh operations from hiding contention. Overlap means wall-clock coexistence: it includes intervals where refresh waits at the persistence-priority gate, not necessarily simultaneous backend I/O. This isolates NodeStore persistence and does not simulate a full consensus network.

Block-cache misses indicate block fetches, not physical disk reads. The pinned Pebble API exposes compaction read bytes but no foreground physical-read-byte counter. The materialized SSTable footprint is sampled after the untimed rotation and flush, when the old source archive has been retired. WAL amplification is WAL bytes divided by logical write bytes (Pebble WAL.BytesIn); it is not total long-term LSM write amplification. Flush and compaction counters cover the timed refresh interval, excluding later fixture preparation. CPU, allocation, block, and mutex profiles are captured separately from the unprofiled measurements. Go profiles include untimed fixture setup and teardown; use them as whole-run diagnostic evidence, not per-refresh attribution.

The current prefetch phase pins the generation pair and holds an archive read lock to exclude deletion. It captures mutation versions, then reads a bounded sorted prefix outside mutation stripes, checking writable before opening the archive for unresolved keys. Its payload budget uses the preferred values, so superseded archive values do not consume it. Commit validates versions under the stripes before writing archive hits. Copy-forward of unchanged pinned archive values does not invalidate concurrent observations; actual mutations still advance the versions. If a stripe changed, promotion releases the locks and retries the invalidated observations. After two retries, it processes keys individually, rereading changed writable values while holding only the current key's stripe. This retains the bounded retry and progress guarantees introduced by #1869; a cold fallback read can still delay a writer on that stripe. Delete batches acquire the archive write lock before stripes. Returned values reuse the prefetched payload; the encoded Pebble write batch owns another copy, and retries discard earlier payloads. The 4 MiB limit is not a claim that the whole operation allocates only 4 MiB. One oversized first value is permitted so traversal can progress.

Correctness CI compares complete reachable and promoted hash sets after retiring the old archive, exercises byte-limited partial batches, cancellation and failures after actual promotion, lazy-value read faults, and a paused-prefetch same-key write followed by deletion. The missing-key test also covers an oversized first value.

Native refresh batches wait, with context cancellation, while that Service has active ledger persistence. Foreground writes never wait on this admission mechanism; already-admitted batches can finish. Sustained foreground work can therefore extend refresh duration. The scalar baseline keeps the original scheduling behavior.

## Results and defaults

Measured revision: `518a187b5d95018a343be3fe5a1b3e62b82be76a`, [CI run 34091933927, attempt 2](https://github.com/LeJamon/go-xrpl/actions/runs/34091933927/attempts/2), September 7, 2026. Environment: Go 1.24.4, Linux amd64, four virtual CPUs on AMD EPYC 7763. Each cell below is the median of three benchmark results, each with three timed iterations. Persistence quantiles pool the writes within those iterations, then the table takes the median reported quantile across the three results.

| Workers | Batch nodes | Cold refresh (ms) | Warm refresh (ms) | Refresh with persistence (ms) | Overlap persist p50 (ms) | Overlap persist p95 (ms) |
| --- | --- | --- | --- | --- | --- | --- |
| 1 | 0 | 522.956 | 109.290 | 582.453 | 1.184 | 2.203 |
| 1 | 64 | 273.046 | 77.222 | 355.733 | 1.039 | 1.916 |
| 1 | 256 | 144.983 | 65.631 | 226.322 | 1.039 | 2.022 |
| 2 | 0 | 302.800 | 83.948 | 332.697 | 1.357 | 2.688 |
| 2 | 64 | 159.406 | 83.853 | 217.828 | 1.064 | 1.962 |
| 2 | 256 | 95.733 | 53.412 | 148.322 | 1.013 | 1.862 |
| 4 | 0 | 272.732 | 67.554 | 288.546 | 1.770 | 3.731 |
| 4 | 64 | 116.297 | 68.323 | 165.352 | 1.045 | 1.921 |
| 4 | 256 | 69.044 | 45.168 | 119.243 | 1.045 | 1.855 |

At four workers, 256-node batches reduced cold refresh time by 75% and overlapping persistence p95 by 50% against the scalar path in this run. The 256-node choice also outperformed 64 nodes for cold refresh at each worker count, while persistence p95 stayed below scalar at every worker count. Keep the existing worker-count default. The 4 MiB payload cap is a safety limit, not a tuned throughput optimum: the largest observed batch payload in this fixture was 133,376 bytes. Oversized-record and partial-batch correctness tests exercise the byte-limit behavior separately.

Four-worker cold storage and allocation measurements:

| Metric per refresh unless stated | Scalar | 64 nodes | 256 nodes |
| --- | ---: | ---: | ---: |
| Block-cache misses per node | 3 | 0.6855 | 0.2886 |
| WAL bytes | 2,488,865 | 2,093,904 | 2,089,285 |
| Materialized SSTable bytes | 1,497,873 | 1,496,605 | 1,493,818 |
| Allocated bytes | 13,011,597 | 16,430,688 | 18,506,693 |
| Allocation count | 206,323 | 195,260 | 191,041 |

The batch path lowers block fetch/decompression demand, WAL overhead, and allocation count, but allocates more total bytes. WAL amplification falls from 1.084 to 1.001; the materialized SSTable footprint does not increase. No flush or compaction bytes were observed inside these short timed refresh intervals, so this does not establish long-running compaction amplification. Whole-run CPU profiles include fixture construction; they are diagnostic support rather than isolated refresh CPU measurements.

### Run variability

[Attempt 1 on the identical revision](https://github.com/LeJamon/go-xrpl/actions/runs/34091933927/attempts/1) used an Intel Xeon 6973P-C runner and produced unstable wall-clock comparisons. Four-worker scalar/64/256 cold times were 367.369/157.854/325.175 ms; warm times were 162.523/340.155/175.544 ms. Overlapping persistence p95 was 162.745/62.972/121.928 ms at four workers, but 91.168/91.122/175.917 ms at one worker. These results do not support a universal latency guarantee. Stable cache, batch, WAL, and SSTable counts and the unchanged-head repeat support the storage-work reduction; elapsed-time results remain host and workload dependent. The repeat was run without tuning code between attempts.

## Reproduction

The `Promotion performance` workflow records the revision and CPU environment and uploads the full matrix and comparable CPU, allocation, block, and mutex profiles. Its unprofiled command is:

```sh
go test ./internal/ledger/service -run '^$' \
  -bench '^BenchmarkService_Refresh' -benchmem -benchtime=3x -count=3 -timeout 20m
```

Run it in CI for the finalization workflow. A green performance job means the benchmark and its correctness checks completed; timing acceptance requires inspecting the artifact.

## Writable-first promotion (#1866)

Baseline: deployed `6ae1f5f4`, including the cold-writable prefetch fix from `70b6aad9`. The updated implementation combines this PR's writable-first reads with the bounded retry and fallback behavior merged in #1869. Measurements ran locally on September 8, 2026 using Go 1.26.1, darwin/arm64, an Apple SSD AP0512Z, and `GOMAXPROCS=3`.

Each case uses 32 fresh, flushed and reopened two-generation fixtures, 256 deterministic 16 KiB values (4 MiB per group), and a fresh 16 MiB shared Pebble block cache. The delayed case adds 250 µs to each SST file read; the other case adds no delay. The writable-hit keys form a sorted prefix; the 50% case does not model interleaved hits, whose SST locality may differ. The OS cache remains populated. Foreground samples issue one unrelated `Put` after the first observed SST read, verify its start falls within the promotion interval, and exclude non-overlapping samples from overlap quantiles; promotion follows any returned prefixes until the entire group completes. All 192 overlapping samples completed on each revision. The updated uncontended samples recorded zero internal retries or fallbacks; the saturated-writer benchmark exercises contention separately.

The historical TSVs did not capture the updated revision SHA, and the overlap helper did not force GC before its timed run. Those overlap quantiles may include collection of fixture garbage. The current fixture forces GC before both sample types and records the checkout SHA and working-tree status; the historical tables have not been rerun after these corrections.

These are synthetic local storage measurements, not a testnet/HDD/ZFS baseline. VFS reads count file API calls, including metadata and any concurrent Pebble background work; they are not physical device reads. Cache misses do not count decompressions. Allocation counters cover the process during promotion and can include Pebble background work. At 32 samples, p99 is the maximum observed sample. Full tables retain all quantiles, per-generation read bytes, cache activity, allocations, completed samples, and logical promotion counters: [baseline](promotion-before.tsv), [updated](promotion-after.tsv).

| Fixture | Writable hits | Archive SST reads, before → after | Total SST reads, before → after | Allocated MiB/group, before → after |
| --- | ---: | ---: | ---: | ---: |
| SSD | 0% | 268.00 → 268.00 | 268.00 → 268.00 | 25.04 → 24.10 |
| SSD | 50% | 268.00 → 134.00 | 399.00 → 265.00 | 16.61 → 14.17 |
| SSD | 100% | 268.00 → 0.00 | 530.00 → 262.00 | 8.10 → 4.08 |
| Delayed I/O | 0% | 268.00 → 268.00 | 268.00 → 268.00 | 24.51 → 24.02 |
| Delayed I/O | 50% | 268.00 → 134.00 | 399.16 → 265.00 | 16.64 → 14.21 |
| Delayed I/O | 100% | 268.00 → 0.00 | 532.22 → 263.62 | 8.11 → 4.08 |

| Fixture | Writable hits | Promotion p50/p95/p99 before (ms) | Promotion p50/p95/p99 after (ms) |
| --- | ---: | ---: | ---: |
| SSD | 0% | 79.85 / 93.43 / 93.88 | 45.24 / 57.35 / 57.38 |
| SSD | 50% | 40.67 / 68.64 / 69.25 | 35.07 / 45.78 / 48.03 |
| SSD | 100% | 5.16 / 9.64 / 10.05 | 4.70 / 8.39 / 10.78 |
| Delayed I/O | 0% | 133.29 / 166.60 / 177.85 | 143.82 / 162.14 / 163.56 |
| Delayed I/O | 50% | 169.14 / 196.58 / 204.33 | 135.57 / 160.77 / 378.64 |
| Delayed I/O | 100% | 166.03 / 294.71 / 318.46 | 96.44 / 108.54 / 201.23 |

Stable writable hits now make zero archive lookups. The updated logical lookup counts are 256/128/0 at 0%/50%/100% writable hits, with 0/128/256 lookups avoided and 4 MiB of prefetched payload in each case. Archive-only SST work is unchanged. Elapsed time and foreground latency vary across runs. An earlier unchanged-baseline delayed archive-only median ranged from 136.73 ms to 185.20 ms on repetition, despite identical SST-read counts. The final archive-only results above also have unchanged SST work; their timing differences reflect run variability. These results establish reduced archive work, not a universal foreground-latency improvement or a measured effect on validator participation.

Progress and completion logs expose `promotion_requested`, `promotion_consumed`, `promotion_returned`, writable/archive hits and misses, `promotion_archive_lookups`, `promotion_archive_lookups_avoided`, `promotion_prefetch_bytes`, committed promotion counts/bytes, `promotion_partial_prefix_retries`, and internal `promotion_version_mismatches`, `promotion_retries`, and `promotion_fallbacks`. Lookup and prefetch counters include internal retries. These counters belong to the refresh's batch calls; the existing NodeStore/cache totals remain shared across callers.

Run the same benchmark file on both revisions; copy it into a baseline worktree when that revision predates the fixture. In the current fixture, creation, flush, reopen, forced GC, and cleanup are excluded from promotion timing. Automatic GC during the operation can still contribute to elapsed time. The default report is written to the OS temporary directory; set `GOXRPL_PROMOTION_BENCH_REPORT` to choose its path.

```sh
GOMAXPROCS=3 GOXRPL_PROMOTION_BENCH=1 \
  go test -count=1 -run '^TestPromotionBatchOfflineReport$' -v ./storage/kvstore/pebble

# One profile/hit distribution, with the same 32-sample default:
GOMAXPROCS=3 GOXRPL_PROMOTION_BENCH=1 \
  go test -count=1 -run '^TestPromotionBatchOfflineReport$/delayed-vfs/hits=0$' -v ./storage/kvstore/pebble

# Exercise the fixture's synchronization under the race detector:
GOXRPL_PROMOTION_BENCH=1 GOXRPL_PROMOTION_BENCH_ITERATIONS=1 \
  go test -race -count=1 -run '^TestPromotionBatchOfflineReport$' ./storage/kvstore/pebble
```

### Ledger persistence during refresh

The existing Service benchmark also completed with one and four configured refresh workers, `GOMAXPROCS=3`, and 256-node batches. It runs three refresh iterations and 32 actual `persistToNodeStore` calls per iteration, including Sync, with 16,384 state leaves and a 256 KiB shared Pebble cache. This is a storage-persistence measurement, not full consensus acceptance latency. The intermediate main baseline includes #1869. Copy-forward avoids artificial version conflicts between refresh workers; a paused-read regression test verifies that another promotion on the same stripe does not trigger retries.

| Revision | Workers | Mean refresh (ms) | Overlapping persistence p50/p95/p99 (ms) | Overlapping persists/refresh |
| --- | ---: | ---: | ---: | ---: |
| before | 1 | 669.69 | 13.70 / 25.28 / 44.35 | 32 |
| before | 4 | 566.50 | 13.78 / 25.20 / 36.89 | 32 |
| main `469f13c2` | 1 | 473.25 | 10.01 / 18.70 / 21.85 | 32 |
| main `469f13c2` | 4 | 388.60 | 9.03 / 15.98 / 22.05 | 32 |
| after | 1 | 434.73 | 8.12 / 16.02 / 21.05 | 32 |
| after | 4 | 360.64 | 8.10 / 14.98 / 19.37 | 32 |

```sh
GOMAXPROCS=3 go test ./internal/ledger/service -run '^$' \
  -bench '^BenchmarkService_RefreshWithPersistence$/workers=(1|4)$/batch=256$' \
  -benchmem -benchtime=3x -count=1
```

### Progress under sustained writes

The existing `BenchmarkPromotionContention` completed 100 groups of 256 keys in each of six cases (1/64 MiB cache and no/paced/saturated writer), using three promotion workers and `GOMAXPROCS=3`. All groups completed in one call. Saturated writers exercised 1.70/1.26 retries per call and 0.59/0.24 single-stripe fallbacks per key at 1/64 MiB. This checks progress under contention; the single-stripe fallback can still delay a colliding writer.

```sh
GOMAXPROCS=3 go test ./storage/kvstore/pebble -run '^$' \
  -bench '^BenchmarkPromotionContention$' -benchtime=100x -count=1
```

The separate #1853 fixture and its measurements are retained in [Writable-first SHAMap promotion](shamap-writable-first.md).
