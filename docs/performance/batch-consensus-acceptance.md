# Batch consensus acceptance

Consensus now applies its freshly parsed transaction set through an explicitly
owned path. Other callers retain defensive reconstruction. Reuse requires the
stored raw bytes, current fields and transaction hash to match; stale inputs
fall back to reconstruction. Transaction-local and Batch inner caches may be
mutated only within the owned call.

After close, fee statistics reuse matching outer and inner transactions while
running the existing fee calculators against the final ledger and rules.
Missing or changed inputs fall back to parsing the stored leaf. The temporary
lookup dies before successor preparation; only a fee-level slice crosses into
the successor. TxQ copies the values and the preparation drops its reference.
There is no process-wide parsed-transaction cache. Existing retry retention and
memory admission are unchanged.

Canonical binding encodes the current field projection once and compares its
hash with the already canonicalized bytes. Template validation, field-presence
checks, canonicalization, mutation detection and signature-cache binding remain
in place.

The acceptance timing event now includes `snapshot`, `transaction_capture`,
`txq_fork`, `fee_levels` and `publication`. `stage_results` remains available.
The preparation components overlap `next_open`; they must not be added to it
as disjoint phases. `fee_levels` includes preparation of the reusable vector
and the queue's closed-ledger update.

## Workload and measurement

The deterministic fixture contains 250 signed Batch transactions, each with
eight inner AccountSet transactions, plus one OfferCreate and one Payment:
252 outer transactions and 2,252 stored leaves. A persisted parent with 16,384
opaque state-padding leaves and a 256 KiB Pebble cache exercises storage refresh.
The regression pins every stored transaction's canonical bytes, success result,
Batch parent ID, metadata digest, fee distribution, and final ledger/state/tx
roots against the unchanged baseline. Every measured full acceptance also
checks the pinned ledger hash.

Measurements use Apple M3 Pro, Darwin arm64, Go 1.24.4, CGO enabled, without the
`mptcrypto` tag, with `-cpu=1` and default GC settings (`GOGC` and
`GOMEMLIMIT` unset). Baseline is
`2f6217276f49d66ca1e8965b2c1665349cf6cfc0` plus the same fixture. Baseline phase
benchmarks use the original apply/fee entrypoints; the changed benchmarks use
the owned apply and construct the fee vector inside the timer. No other
build/test jobs from this task run during measured samples; unrelated host
activity is uncontrolled. Large timing outliers occurred across phases, so the
reported tails cannot be attributed solely to this change. An interrupted
initial phase run was discarded and the phase comparison repeated. These
results do not predict the incident's Linux Xeon timings.

Phase figures are medians of five samples of ten iterations, without profiling.
Full acceptance quantiles use 100 ledger-sized bursts for each refresh setting
and version. Setup, signing, service creation and fixture rotation are outside
the timer. Each burst starts from the same parent. With refresh enabled, a real
generation refresh starts before acceptance and must promote state; a 100 µs
promotion delay makes overlap repeatable. This is controlled local contention,
not a production-sized database or network consensus soak.

| Phase | ms/op, before → after | MiB/op, before → after | allocs/op, before → after |
|---|---:|---:|---:|
| Parse 252 outers | 102.9 → 62.2 | 47.9 → 28.2 | 847,602 → 502,566 |
| Application | 343.2 → 242.1 | 189.5 → 148.8 | 2,776,525 → 2,041,857 |
| Closed fee statistics | 150.1 → 23.6 | 67.5 → 12.8 | 1,263,512 → 214,550 |
| Successor readiness | 191.6 → 61.3 | 86.3 → 31.7 | 1,651,663 → 602,717 |

| Refresh | Acceptance p50 ms, before → after | p95 ms, before → after | p99 ms, before → after | MiB/op, before → after | allocs/op, before → after |
|---|---:|---:|---:|---:|---:|
| disabled | 695.0 → 400.3 | 7994.3 → 3487.0 | 13425.8 → 5589.6 | 326.7 → 211.6 | 5,292,247 → 3,163,596 |
| enabled | 701.5 → 381.2 | 3802.5 → 1623.4 | 7721.7 → 3857.0 | 327.9 → 211.8 | 5,305,216 → 3,164,850 |

[All benchmark result rows and phase quantiles](batch-consensus-acceptance.csv).

## Reproduction and profiles

From the repository root, with the documented OpenSSL environment:

```sh
GOTOOLCHAIN=go1.24.4 go test ./internal/ledger/service -run '^TestIssue2090BatchAcceptanceRegression$' -count=1
GOTOOLCHAIN=go1.24.4 go test ./internal/ledger/service -run '^$' -bench '^BenchmarkService_Issue2090(Parse|Application|ClosedFeeLevels|Successor)$' -benchmem -benchtime=10x -count=5 -cpu=1
GOTOOLCHAIN=go1.24.4 go test ./internal/ledger/service -run '^$' -bench '^BenchmarkService_Issue2090FullAcceptance$' -benchmem -benchtime=100x -count=1 -cpu=1
GOTOOLCHAIN=go1.24.4 go test ./internal/ledger/service -run '^$' -bench '^BenchmarkService_Issue2090' -benchmem -benchtime=5x -count=1 -cpu=1 -cpuprofile=cpu.pprof -memprofile=alloc.pprof
go tool pprof -top -cum -tagfocus='phase=closed-fee-levels' cpu.pprof
go tool pprof -top -sample_index=alloc_space alloc.pprof
```

Profiles are collected in separate runs. CPU profiles carry phase labels;
allocation profiles include fixture setup and untimed work, so use benchmark
`B/op` and `allocs/op` for timed allocation volume. These values describe total
allocations per entire ledger-sized pass, not retained heap or RSS. MB/s uses
only the outer input blob bytes, not all expanded leaves. Phase
microbenchmarks overlap the work in full acceptance and must not be summed.

The separate fee-phase CPU profiles contain 690 ms of tagged samples before
and 110 ms after. The baseline includes 400 ms under `tx.ParseFromBinary`;
the changed run has no samples under that function and 30 ms under
`tx.CurrentFieldsMatchRaw`. These small diagnostic samples locate the changed
work; they are not confidence intervals or a substitute for the unprofiled
measurements. CPU and allocation profiles and the unabridged benchmark output
are retained locally; the commands above reproduce them.

## Limits

The local Docker daemon was unavailable. No live validator/network soak was
run, and this work does not establish changes in missed or partial validations,
catch-up frequency, or live retained-memory behavior. The repeated local
acceptances establish deterministic results under controlled refresh only.
The incident's archived parent state was not replayed; this is a generated
AccountSet-heavy workload, not a historical-network conformance claim.
