# Bounded state execution: before/after benchmark

**Measured outcome: lower sampled heap, slower execution.** Signed acceptance regressed by 61% for the small Batch and 26% for the larger workload. Disk-backed state workloads were also slower, with a much larger penalty when forced to spill. This is performance evidence, not a performance acceptance pass: numerical production ceilings and the longer soak across online-delete rotations remain unresolved.

## Reference and method

Compare old execution at `546e974024a77d9dcf80bc07cd4e86930467fff1` (the fixed PR base) with bounded execution at `3ee8a740fe3816abbb0a40f1186fd3f6c7491450`. Subsequent benchmark-only changes provide fixtures and instrumentation; they do not alter production execution. Each revision runs in its own worktree/process, on the same host, with the same deterministic workload. The comparison does not include unrelated later main changes.

Measured on 2026-10-12. Host: Apple M3 Pro, 11 CPU cores, 18 GiB RAM, Darwin arm64 25.3.0, Go 1.24.4. Both processes use `GOMAXPROCS=4`, `GOGC=100`, `GOMEMLIMIT=512MiB`. The memory limit is Go's soft runtime target, not a hard RSS cap. This is an interactive laptop with background applications, not a dedicated performance host; small timing differences require caution.

All test binaries are compiled before measurement. Cases run serially, alternating before/after order between samples. The OS page cache is not purged. Process CPU and peak RSS come from `wait4`; they include fixture setup and teardown. Per-workload allocation/GC counters and latency samples use narrower boundaries. Rusage block counters are platform dependent and zero does not prove zero I/O.

Correctness is mandatory: signed acceptance cases verify fixed ledger hashes, state/transaction roots, transaction counts, and `tesSUCCESS` metadata. State-workload results must match before/after correctness digests. Report regressions as well as improvements. These measurements are descriptive; they do not establish mainnet capacity or satisfy an unspecified production latency ceiling.

## Signed transaction acceptance

`BenchmarkService_BatchConsensusAcceptance` runs two workloads: one signed Batch containing two inner payments, and 32 signed Batches containing 256 inner payments. Fixtures and signatures are prepared outside timing; every acceptance starts at the same persisted parent. Each sample contains 200 measured acceptances, with five independent samples per revision. Report medians of per-run results; p99 has only a few tail observations within each run.

The old engine receives a 256 KiB NodeStore block cache. Flat mode receives a 128 KiB NodeStore block cache and a 128 KiB flat cache. The additional flat database has an 8 MiB memtable; that extra storage-engine cost is included in process memory. Flat-hit/root-fallback counters verify which paths execute. A newly closed root may still use authenticated-root reads before asynchronous flat advancement catches up.

The timed operation includes parsing, transaction application, close/hash, successor preparation/publication and persistence queueing. It excludes the asynchronous durable writer and flat advancement. Do not interpret acceptance time as durable-publication latency. Peak process RSS also includes per-round store construction and fixture work, so it is not steady-state node RSS.

## Disk-backed state workload

The common workload source is copied unchanged into the old worktree, with a separate test-only adapter for its API. Both versions seed identical hashed keys and 512-byte values into Pebble, persist them, then replace the seed's decoded tree with a root-backed view. This drops the seed graph; it does **not** clear Pebble caches/memtables or the OS page cache. “Disjoint” means reads sweep the larger keyspace, not cold physical-disk I/O.

| Case | Initial entries | Measured ledgers | Warm-up ledgers | Reads/ledger | Updates or inserts/ledger | Retained closed views |
| --- | ---: | ---: | ---: | ---: | --- | ---: |
| Hot | 4,096 | 512 | 16 | 128 | Update the same 16 keys | 32 |
| Disjoint reads | 65,536 | 1,024 | 0 | 512 | Update the same 16 keys | 64 |
| Growing | 32,768 | 512 | 16 | 128 | Insert 64 new keys | 32 |
| Spill | 4,096 | 256 | 16 | 128 | Update the same 512 keys | 32 |

Each case runs in five independent before/after process pairs. The old NodeStore receives a 16 MiB block cache; the new NodeStore and flat database each receive 8 MiB. The new flat database additionally has a 4 MiB memtable. Bounded execution has a 64 MiB workspace, 1 MiB overlay memory (64 KiB in the spill case), and 64 MiB scratch-disk allowance. Exact settings are in [state.json](../../scripts/benchmarks/state-memory/state.json).

The total timer includes opening a successor, reads, updates/inserts, close/hash, canonical-node persistence and Sync. The new version also verifies root closure, derives the root diff, durably advances flat state, and acknowledges the outbox. The close timer starts after reads/writes and includes those persistence/publication steps. This is a storage-layer comparison: it excludes service publication, ledger-header/relational persistence, networking, and transaction parsing/application. Unlike the acceptance benchmark, it includes flat durable-write cost. The adapter uses a benchmark authorization token after real closure verification; it does not construct the complete service proof token. It acknowledges each outbox record immediately, including its durable commit; production drains the outbox through a separate path. The old engine has neither flat projection maintenance nor a spill allowance, so these new costs are part of the architecture comparison rather than identical storage operations.

Per-ledger sampling and eviction of expired retained views are outside the latency timers but inside allocation/GC deltas. There is no forced GC. Heap samples therefore include uncollected garbage and are not post-GC live-memory measurements. Account peaks and final database sizes include setup; family fetch/store counters cover measured ledgers and represent logical SHAMap work. File sizes include the initial state and transient WAL/compaction files. Neither file sizes nor Darwin's rusage block counters measure physical write amplification. These cases do not cover online deletion, RPC scans, network acquisition, or a long-running mainnet replay.

## Acceptance results

Median of five independent 200-acceptance samples, milliseconds unless stated otherwise:

| Workload | Metric | Old | Bounded + flat | Change |
| --- | --- | ---: | ---: | ---: |
| Batch with 2 inner payments | Mean acceptance | 1.220 | 1.964 | +61.0% |
| | p50 acceptance | 1.112 | 1.740 | +56.5% |
| | p95 acceptance | 1.689 | 2.457 | +45.5% |
| | p99 acceptance | 1.859 | 2.906 | +56.3% |
| | Allocated bytes/acceptance | 751,710 | 865,905 | +15.2% |
| 32 Batches / 256 inner payments | Mean acceptance | 94.858 | 119.983 | +26.5% |
| | p50 acceptance | 94.188 | 119.128 | +26.5% |
| | p95 acceptance | 100.983 | 126.393 | +25.2% |
| | p99 acceptance | 106.784 | 132.614 | +24.2% |
| | Allocated bytes/acceptance | 64,184,692 | 73,590,682 | +14.7% |

The five per-run means ranged from 1.192–1.270 ms before versus 1.938–1.985 ms after for the small case, and 94.498–95.428 ms versus 119.240–120.288 ms for the large case. These are observed ranges, not confidence intervals. All roots and transaction-effect assertions passed. Both cases demonstrate a repeatable acceptance regression on this host. Inverse mean acceptance time falls from 820 to 509 acceptances/s for the small fixture and 10.54 to 8.33 acceptances/s for the large fixture; these are benchmark rates, not production TPS.

Flat mode reported 21 flat hits per acceptance, plus 18 root fallbacks in the small case and 904 in the large case. The fallback count reflects successor/new-root access before flat advancement; this is not an all-flat-read benchmark. Median process peak RSS was 42.6 → 45.9 MiB for the small case and 93.5 → 97.4 MiB for the large case, including setup and teardown. These short fixtures do not demonstrate retained-memory reduction.

Raw per-run results and process counters are in [service-runs.json](state-memory-results/service-runs.json), the [CSV](state-memory-results/service-process-and-service.csv), and [metadata](state-memory-results/service-metadata.json). Whole-process CPU times include fixture construction; use the timed operation measurements above for acceptance comparisons.

## State workload results

All four cases completed five before/after process pairs. Every per-ledger state root, ledger hash, sequence and rolling read digest matched across revisions and repetitions. All new-engine runs used flat reads with zero root fallbacks, advanced flat state once per measured ledger, and ended with no pending outbox record or recorded flat failure. All measured account peaks stayed within their allowances; every spill run used scratch disk.

Median of five per-run values; arrows are **old → bounded + flat**. Total latency includes reads, writes and the durable storage boundary described above.

| Workload | Mean total (ms) | p50 (ms) | p95 (ms) | p99 (ms) | Workloads/s | Mean latency ratio |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| Hot | 3.95 → 18.44 | 3.90 → 18.58 | 5.85 → 22.95 | 6.10 → 24.93 | 252.9 → 54.2 | 4.66× |
| Disjoint reads | 5.89 → 25.66 | 5.91 → 25.85 | 8.77 → 32.06 | 12.03 → 34.01 | 169.7 → 39.0 | 4.36× |
| Growing | 6.30 → 26.15 | 6.63 → 25.98 | 8.55 → 31.12 | 10.85 → 35.00 | 158.8 → 38.2 | 4.15× |
| Spill (64 KiB overlay) | 9.52 → 152.14 | 9.07 → 151.95 | 12.15 → 162.09 | 14.99 → 167.10 | 105.0 → 6.6 | 15.97× |

The new implementation is slower in every tested case. The forced-spill case compares a 64 KiB overlay with actual scratch writes against the old unconstrained mutation path; it quantifies the cost of that tight allowance, not a production-default claim. These synthetic ledger-workload rates are not transaction throughput.

| Workload | Last-quarter sampled heap (MiB) | Whole-process peak RSS (MiB) | Allocated KiB/ledger | GC CPU (s/run) | GC cycles/run |
| --- | ---: | ---: | ---: | ---: | ---: |
| Hot | 11.6 → 3.2 | 72.3 → 71.6 | 478 → 1116 | 0.20 → 0.46 | 33 → 284 |
| Disjoint reads | 105.5 → 6.6 | 517.7 → 317.7 | 921 → 2312 | 0.39 → 0.56 | 13 → 527 |
| Growing | 98.0 → 6.5 | 325.3 → 224.5 | 1407 → 3014 | 0.23 → 0.40 | 15 → 368 |
| Spill (64 KiB overlay) | 51.1 → 4.0 | 274.4 → 224.7 | 7046 → 25630 | 0.47 → 2.23 | 69 → 2731 |

The bounded engine retains much less sampled Go heap near the end of these workloads, while allocating more bytes and spending more CPU on GC. Heap includes uncollected garbage; peak RSS includes initialization and storage-engine/native memory. The measurements do not establish a production RSS plateau, post-GC live-heap size, or behavior across online-delete rotations.

| Workload | Logical node fetches/ledger | Canonical node KiB stored/ledger (same both sides) | Final node + flat files (MiB) | New peak scratch (KiB) |
| --- | ---: | ---: | ---: | ---: |
| Hot | 4.4 → 165.0 | 34.0 | 21.7 → 34.3 | 0.0 |
| Disjoint reads | 87.2 → 213.0 | 42.8 | 135.6 → 151.1 | 0.0 |
| Growing | 73.1 → 615.7 | 129.3 | 137.3 → 150.2 | 0.0 |
| Spill (64 KiB overlay) | 6.8 → 3246.0 | 565.3 | 195.3 → 220.9 | 249.0 |

Canonical node bytes are identical, but bounded execution fetches more canonical nodes and adds flat-state writes. Final file sizes vary with compaction timing and include the seeded database; they must not be used as physical write-amplification measurements. Fetch counters exclude flat database lookups.

Full raw samples are in [state-runs.json.gz](state-memory-results/state-runs.json.gz); see [per-run metrics](state-memory-results/state-per-run.csv), [comparison medians](state-memory-results/state-summary.csv), [metadata](state-memory-results/state-metadata.json), and the [aggregation definitions](state-memory-results/README.md). Original output for both suites is preserved in [raw-logs.tar.gz](state-memory-results/raw-logs.tar.gz).

## Validation

The integrated state smoke and shared-parent service regression passed three repetitions, plus race detection. The service fixture also passed 20 ordinary and 10 race repetitions in its isolated worktree. Strict and advisory ledger-package lint reported zero issues. A fresh before/after smoke run passed using the final runner. Production source is unchanged from the measured implementation.

## Code paths behind the trade-off

The bounded [execution read path](../../internal/ledger/execution.go) opens and closes a flat view for each base lookup. [Opening that view](../../internal/ledger/flatstate/store.go) creates a Pebble snapshot, validates current/advancing metadata, and registers the reader. The old SHAMap instead keeps decoded nodes attached after lazy reads. Repeated view setup is therefore a plausible contributor to slower reads, while dropping decoded-tree retention explains the different memory behavior. These measurements do not attribute an exact percentage to view setup; that would require profiling.

The new close path constructs the canonical commitment, then traverses changed old/new paths again to derive flat changes. The production [flat-state publisher](../../internal/ledger/service/flat_state.go) also uses this root-diff approach. The state benchmark additionally includes flat advancement and immediate durable outbox acknowledgement. Spill adds overlay file operations to enforce its small allowance. These are concrete extra operations; the tables measure their combined effect, not isolated function costs.

## Reproduction

From this branch's repository root, use Go 1.24.4 and Python 3.9+ on macOS or Linux (`os.wait4` is required). Create a fresh old-code worktree, then install only the shared test workload and old-API adapter:

```sh
git worktree add --detach /tmp/go-xrpl-state-before 546e974024a77d9dcf80bc07cd4e86930467fff1
cp internal/ledger/state_memory_benchmark_common_test.go /tmp/go-xrpl-state-before/internal/ledger/
cp scripts/benchmarks/state-memory/before_adapter_test.go.txt /tmp/go-xrpl-state-before/internal/ledger/state_memory_benchmark_before_test.go
python3 scripts/benchmark-state-memory.py --before-tree /tmp/go-xrpl-state-before --after-tree . --manifest scripts/benchmarks/state-memory/service.json --output /tmp/go-xrpl-service-results --samples 5
python3 scripts/benchmark-state-memory.py --before-tree /tmp/go-xrpl-state-before --after-tree . --manifest scripts/benchmarks/state-memory/state.json --output /tmp/go-xrpl-state-results --samples 5
```

Use fresh output paths on a repeat; the runner refuses to overwrite an existing `runs.json`. It compiles before timing, alternates process order, stops on failed runs or differing state digests, and records commits, worktree status, Go version, manifest, binary SHA-256 hashes, commands, logs, per-run JSON and process/acceptance CSV. Keep other local builds/tests stopped during measurement. Benchmark-only baseline adapters live under `scripts/benchmarks`; the production code has no old-engine fallback.

Acceptance measurements used fixture commit `627ca8313` plus the metric-label change from `flat-memtable-bytes/op` to `flat-memtable-limit-bytes`. State measurements used fixture commit `8e07404b9`. Both have identical production code to `3ee8a740f`. The published metadata records the original measured worktree status; subsequent documentation/results commits do not change those measurements.

The original measurement runner was uncommitted and its source hash was not captured; the service build logs were not retained. Binary hashes and all process logs are preserved. The committed runner provides workload reproduction, with stricter per-ledger identity checks and mandatory fresh builds added after measurement; published data was independently checked against those stricter conditions.
