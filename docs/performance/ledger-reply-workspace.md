# Ledger reply workspace benchmark

`internal/consensus/adaptor/router_serve_bench_test.go` measures the reply
construction paths changed for issue #2079. It builds a backed SHAMap with
1,024 leaves and stores serialized nodes in a locked in-memory family. The
family implements strict `FetchBounded`: it checks the stored value length
before cloning it, so an oversized node is rejected without a temporary copy.
Backends that only implement unbounded reads fail closed for this bounded
walker.

State and transaction maps use 32-byte and 4 KiB values. Transaction values
encode a unique ordinal and their keys are derived with the transaction-ID
hash prefix, so every transaction leaf has a valid NodeID path and distinct
content. The fixture also adds state leaves at the normal query-depth boundary
to make payload size visible in replies. Every case runs hot with clean
residency retained and cold with that residency cleared before each timed
reply. One successful reply per subbenchmark is validated outside the timer:
the wire data hashes, the requested root hash, and every non-root leaf NodeID
path must agree.

The benchmark reports Go's `B/op` and `allocs/op`, encoded frame and node
sizes, and serialized bytes and reads fetched by the family. `B/op` is the
testing package's cumulative allocation measurement; it is not process RSS,
measured heap, or reserved workspace. The optimized path additionally reports
the real router memory account's managed workspace peak and denied counts.

`BenchmarkRouterLedgerCloseUnderReplyLoad` starts a standalone ledger service
with 256 genesis accounts and times real `AcceptLedger` calls while zero or
four serving workers build, encode, and send replies. The service and reply
fixture use independent maps, so this is foreground ledger processing under
concurrent Go/runtime and reservation pressure rather than a full shared-ledger
integration test. Its `B/op` includes background-serving allocations during
the timed close window.

## Production policy represented by the optimized fixture

Production uses an elastic shared `peer_serve` account capped at 256 MiB. Its
minimum is zero so protected reserves remain available. A reply frame is
capped at 8 MiB. The conservative reservation is:

```
6 * frame ceiling + 512 * hard node cap + encoding workspace + 1 MiB
encoding workspace = 512 + 192 * hard node cap
```

The default hard cap is 12,288 nodes, giving a 60,031,488-byte reservation.
The `smallcap32` policy uses 32 nodes for root requests and 128 for leaf
requests; its root reservation is 51,403,264 bytes. These are implementation
policies, not XRPL protocol limits. Four default workers reached 240,125,952
managed bytes, and four `smallcap32` workers reached 205,613,056 bytes.

The caller retains its reservation through queue admission; a queue clone is
charged separately. The backend checks the first serialized value against the
remaining budget before cloning it. An oversized node is not emitted, so the
caller can retry through another path. Production exposes `peer_serve`
used/peak/denied memory metrics and separate `ledger_serve` admission
denied/truncated counters. These direct benchmarks exercise the reservation
and encoding path but do not exercise queue admission counters.

## Reproducing the measurements

Run these commands from the module root with Go 1.24.4 and the fixed
`GOMAXPROCS` shown below. The redirects use portable log names; keep the logs
outside the repository if they are retained. Timings were collected on an
Apple M3 Pro (darwin/arm64); they are synthetic workload measurements.

The optimized reply run is:

```bash
GOTOOLCHAIN=go1.24.4 GOMAXPROCS=11 CGO_ENABLED=1 \
  just test-pkg './internal/consensus/adaptor -run "^$" -bench "^BenchmarkRouter(LedgerReply|TxSetReply)$" -benchtime=100ms -count=5' \
  > optimized-router-replies-count5.log
```

The optimized foreground-close run is:

```bash
GOTOOLCHAIN=go1.24.4 GOMAXPROCS=11 CGO_ENABLED=1 \
  just test-pkg './internal/consensus/adaptor -run "^$" -bench "^BenchmarkRouterLedgerCloseUnderReplyLoad$" -benchtime=100ms -count=5' \
  > optimized-router-close-count5.log
```

The baseline adaptation is kept in
`docs/performance/ledger-reply-baseline.patch`. It removes workspace
reservation and uses the base branch's unbounded walker and frame encoder,
while retaining the corrected transaction fixture and validation. The
baseline intentionally contains only `smallcap32`: the base benchmark did not
have the production-default policy, so there is no valid default-policy
before/after comparison.

The optimized timing samples were collected at `196551f68`, before merging
the cancellation changes from issue #2080. Post-integration checks exercise
the benchmark and cancellation paths without repeating the timing study;
the tables below describe the measured pre-integration revision.

Starting from a clean checkout at `2f6217276`, copy the final benchmark and
patch into that checkout, then apply the patch:

```bash
git worktree add --detach ../ledger-reply-baseline 2f6217276
cp internal/consensus/adaptor/router_serve_bench_test.go \
  ../ledger-reply-baseline/internal/consensus/adaptor/router_serve_bench_test.go
cp docs/performance/ledger-reply-baseline.patch ../ledger-reply-baseline/
cd ../ledger-reply-baseline
patch -p0 < ledger-reply-baseline.patch
GOTOOLCHAIN=go1.24.4 GOMAXPROCS=11 CGO_ENABLED=1 \
  just test-pkg './internal/consensus/adaptor -run "^$" -bench "^BenchmarkRouter(LedgerReply|TxSetReply|LedgerCloseUnderReplyLoad)$" -benchtime=100ms -count=5' \
  > baseline-router-serve-count5.log
```

The small-cap rows below are medians of the five benchmark samples from the
matching baseline and optimized reply runs. The table shows root requests,
which are the representative subtree-overbuild workload; leaf rows are also
run and validated in the raw benchmark logs. `family bytes/op` is the
serialized data cloned by the family, so it exposes cold-path fetch work.

## Before and after: `smallcap32`

| Map / payload | Mode | Baseline ns/op | Optimized ns/op | Baseline B/op | Optimized B/op | Baseline allocs/op | Optimized allocs/op | Baseline / optimized family bytes/op |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| state / 32 B | root hot | 1,288,667 | 50,527 | 992,110 | 39,565 | 10,748 | 234 | 0 / 0 |
| state / 32 B | root cold | 1,597,988 | 87,967 | 1,931,411 | 66,015 | 17,702 | 428 | 243,404 / 7,036 |
| state / 4 KiB | root hot | 5,228,059 | 156,333 | 8,109,693 | 301,859 | 10,751 | 234 | 0 / 0 |
| state / 4 KiB | root cold | 8,802,672 | 188,324 | 16,013,470 | 505,331 | 17,709 | 428 | 3,441,772 / 88,316 |
| transaction / 32 B | root hot | 1,245,200 | 54,442 | 981,121 | 39,122 | 11,543 | 259 | 0 / 0 |
| transaction / 32 B | root cold | 2,064,930 | 73,813 | 1,879,899 | 63,917 | 18,515 | 453 | 210,648 / 5,916 |
| transaction / 4 KiB | root hot | 7,765,965 | 244,745 | 8,082,648 | 327,208 | 11,599 | 264 | 0 / 0 |
| transaction / 4 KiB | root cold | 9,433,058 | 303,581 | 15,931,340 | 546,426 | 18,556 | 458 | 3,391,500 / 94,844 |
| txset / 32 B | root hot | 1,249,385 | 117,504 | 849,992 | 73,274 | 9,927 | 518 | 0 / 0 |
| txset / 32 B | root cold | 1,650,816 | 181,891 | 1,748,369 | 143,200 | 16,898 | 1,080 | 210,648 / 18,192 |

For example, the corrected 32-byte state fixture goes from 1.29 ms to
50.5 µs per hot root reply, while its cold family copy falls from 243,404 to
7,036 bytes. The corrected transaction fixture goes from 1.25 ms to 54.4 µs
for the corresponding hot root reply. All optimized rows had zero managed or
local workspace denials.

## Optimized production-default workload

The `default` policy is the production 8 MiB frame and 12,288-node workload.
The baseline has no corresponding policy, so these rows characterize the
optimized implementation and make no default-policy before/after claim. The
optimized transcript uses the current bounded walker, including skipped-leaf
accounting for the `fatLeaves=false` TxSet cases.

| Map / payload | Mode | Optimized ns/op | B/op | allocs/op | family bytes/op | Managed peak bytes |
| --- | --- | ---: | ---: | ---: | ---: | ---: |
| state / 32 B | root hot | 1,724,932 | 1,427,178 | 7,810 | 0 | 60,031,488 |
| state / 32 B | root cold | 2,171,256 | 2,366,508 | 14,764 | 243,404 | 60,031,488 |
| state / 4 KiB | root hot | 6,671,287 | 11,594,973 | 7,814 | 0 | 60,031,488 |
| state / 4 KiB | root cold | 8,031,983 | 19,497,717 | 14,768 | 3,441,772 | 60,031,488 |
| transaction / 32 B | root hot | 1,881,519 | 1,397,546 | 8,704 | 0 | 60,031,488 |
| transaction / 32 B | root cold | 2,580,867 | 2,296,343 | 15,676 | 210,648 | 60,031,488 |
| transaction / 4 KiB | root hot | 8,535,917 | 11,507,355 | 8,566 | 0 | 60,031,488 |
| transaction / 4 KiB | root cold | 10,227,812 | 19,355,563 | 15,522 | 3,391,500 | 60,031,488 |
| txset / 32 B | root hot | 1,224,072 | 903,251 | 6,279 | 0 | 60,031,488 |
| txset / 32 B | root cold | 1,737,707 | 1,801,593 | 13,250 | 210,648 | 60,031,488 |
| txset / 4 KiB | root hot | 7,252,510 | 4,090,431 | 6,215 | 0 | 60,031,488 |
| txset / 4 KiB | root cold | 8,893,308 | 11,938,480 | 13,171 | 3,391,500 | 60,031,488 |

No default-policy reply reservation was denied. Managed peak is the workspace
account's reservation ceiling observed during the run; it is not a heap or
RSS measurement.

## Foreground ledger-close latency

The baseline close run used `smallcap32` only and did not record a maximum, so
its maximum is `n/a`. The optimized values are medians of the five process
iterations and include the maximum observed latency in each 100 ms sample set.
The default baseline cells are `n/a` because the baseline had no default
policy.

| Policy / serving workers | Baseline p50 / p95 / p99 / max ns | Optimized p50 / p95 / p99 / max ns | Optimized B/op | Managed peak bytes | Replies / family bytes |
| --- | ---: | ---: | ---: | ---: | ---: |
| default / 0 | n/a | 138,917 / 218,458 / 312,167 / 584,583 | 191,082 | 0 | 0 / 0 |
| default / 4 | n/a | 145,583 / 405,375 / 569,291 / 951,000 | 352,237 | 240,125,952 | 124 / 468,024 |
| smallcap32 / 0 | 250,083 / 938,000 / 5,371,292 / n/a | 145,375 / 261,292 / 464,375 / 693,125 | 189,686 | 0 | 0 / 0 |
| smallcap32 / 4 | 247,708 / 1,640,000 / 42,358,125 / n/a | 148,416 / 411,167 / 563,667 / 915,500 | 327,777 | 205,613,056 | 3,504 / 13,132 |

The four-worker run uses an independent serving fixture and a simulated
successful send. Host scheduling and unrelated user threads can affect these
samples even with `GOMAXPROCS=11`; repeat on an otherwise idle machine before
drawing capacity conclusions. The benchmark does not claim RSS, retained
heap, or reservation metrics as one another.

For a short race sanity run, use the same optimized checkout:

```bash
GOTOOLCHAIN=go1.24.4 GOMAXPROCS=4 CGO_ENABLED=1 \
  just test-pkg './internal/consensus/adaptor -race -run "^$" -bench "^BenchmarkRouterLedgerCloseUnderReplyLoad/policy=smallcap32/serve-workers=4$" -benchtime=1ms -count=1' \
  > optimized-router-race.log
```
