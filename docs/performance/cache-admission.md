# Foreground and peer cache admission

Issue #2075 protects useful foreground records from peer scans while retaining a
bounded cache for repeated peer requests. The allocation and admission policies
are described in [Operating the node](../operating.md#resources-and-acquisition).

## Method

Measured on 2026-10-16, darwin/arm64, Apple M3 Pro, Go 1.24.4. The baseline is
`65869d1c60bd3fdc7b9f5d82faf62cecdc0134ab`; the candidate is the implementation in
this PR. The identical benchmark was copied onto the baseline. Both revisions
ran sequentially after other test jobs finished, three samples of 3,000 mixed
operations each. These are synthetic CPU/allocation measurements using a counted
in-memory backend, not live-node validation latency or physical-disk results.

```sh
GOTOOLCHAIN=go1.24.4 go test ./shamap/backend -run '^$' \
  -bench '^BenchmarkCleanResidencyMixedWorkload$' -benchtime=3000x -count=3 -benchmem
```

The real backed SHAMap contains 4,096 leaves with 96-byte values, larger than the
256 KiB shared allowance. A 32 KiB progress reserve leaves `R = 224 KiB`.
NodeStore's positive cache permits 2,000 entries, subject to its byte account.
The fixture's writes initially fill that cache, exercising competition with
subsequent upper-cache admissions. Sixteen foreground keys share branch zero;
scans cycle through 239 disjoint peer subtrees outside that branch. Each mixed
scan operation reads one foreground key and serves four bounded requests
(wanted depth 2, query depth 2, leaves included). Reuse operations instead request
the same subtree once. Both access classes warm in either order before sampling.
Latency samples occupy a fixed-size ring. Recovery reads the foreground set once
after scans stop, outside the timed interval.

`legacy` gives both caches zero minima and whole-pool maxima. `balanced` uses the
production record-cache shares: NodeStore 28–56 KiB and SHAMap 56–112 KiB.
The pressure case fills the progress account's available capacity after warming,
then releases it before recovery. Counters and retained-byte snapshots are taken
before recovery. Each run checks the total byte bound and zero charges after
shutdown. Raw samples, including isolated policy comparisons and both warm-up
orders, are in [cache-admission-results.csv](cache-admission-results.csv).

## Results

Medians of three samples. Latencies are microseconds; storage counts are backend
`Get` calls during the 3,000-operation interval. Serving rate includes foreground
work between peer requests.

| Scenario / revision | Foreground p50 / p95 / p99 (µs) | Foreground Gets | Total Gets | Peer requests/s | Recovery Gets |
|---|---:|---:|---:|---:|---:|
| Scan, local first — baseline | 5.54 / 8.58 / 33.08 | 9,415 | 233,335 | 33,989 | 53 |
| Scan, local first — candidate | 3.46 / 4.25 / 7.79 | 0 | 214,703 | 38,806 | 0 |
| Scan, peer first — baseline | 5.71 / 8.71 / 20.75 | 10,345 | 233,730 | 34,676 | 52 |
| Scan, peer first — candidate | 3.62 / 5.21 / 15.21 | 0 | 214,703 | 33,247 | 0 |
| Reuse, local first — baseline | 4.50 / 6.12 / 17.58 | 3,934 | 30,934 | 33,776 | 21 |
| Reuse, local first — candidate | 3.33 / 4.17 / 7.29 | 0 | 0 | 55,163 | 0 |
| Pressure + scan — baseline with shares | 5.58 / 8.75 / 28.92 | 8,997 | 225,750 | 35,147 | 42 |
| Pressure + scan — candidate | 3.46 / 4.58 / 13.79 | 0 | 214,703 | 38,973 | 0 |

Retained bytes include payloads and conservative structural charges. Allocations
are per mixed operation, including all four scan replies or one reuse reply.

| Scenario / revision | SHAMap / NodeStore retained bytes | Bytes/op | Allocs/op | Upper hits / misses |
|---|---:|---:|---:|---:|
| Scan, local first — baseline | 0 / 229,376 | 132,780 | 1,414 | 253 / 239,747 |
| Scan, local first — candidate | 45,888 / 57,128 | 109,900 | 1,220 | 23,247 / 216,753 |
| Scan, peer first — baseline | 1,104 / 228,056 | 131,664 | 1,405 | 255 / 239,745 |
| Scan, peer first — candidate | 45,888 / 57,128 | 109,901 | 1,220 | 23,247 / 216,753 |
| Reuse, local first — baseline | 368 / 228,940 | 34,768 | 342 | 0 / 69,000 |
| Reuse, local first — candidate | 40,000 / 57,128 | 17,726 | 170 | 69,000 / 0 |
| Pressure + scan — baseline with shares | 85,152 / 29,428 | 121,611 | 1,359 | 14,250 / 225,750 |
| Pressure + scan — candidate | 45,888 / 57,128 | 109,899 | 1,220 | 23,247 / 216,753 |

The combined policy eliminates foreground backend reads in every balanced scan
sample, including pressure and both warm-up orders, and keeps repeated peer
requests useful. Serving throughput is not uniformly higher: the peer-first
scan median is about 4% below baseline, while local-first is about 14% higher.
These short in-memory samples do not establish live serving performance.

Allocation shares alone are insufficient: baseline code with balanced shares
still performs 8,665 foreground backend Gets in the local-first scan median.
Admission changes alone are also insufficient: candidate code with legacy
zero-minimum accounts can leave the upper cache starved by records admitted on
writes, increasing foreground Gets. The two changes must ship together. Under
balanced shares without peers, both revisions perform zero backend Gets and
12 allocations per foreground read; small latency differences remain noisy.

The chosen shares bound peer occupancy while reserving at least `3R / 16` for
foreground SHAMap records after a full peer segment, and leave elastic capacity
for other consumers. They are a conservative fixed policy supported by this
workload, not a claim of globally optimal cache sizing. Peer working sets larger
than their segment continue to miss; real storage, larger ledger working sets
and concurrent request latency need operational measurement via the per-class
counters. Materialization remains under the cache lock to preserve payload
budget ownership; this change makes no lock-contention claim.
