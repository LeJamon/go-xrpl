# Transaction-only replay tail latency

Issue #2105 reported slow transaction-tree acquisition after a Batch-heavy
acceptance. The production observations establish acquisition delay, but do
not identify which peer, network hop, or local queue caused each delay. The
change targets a reproducible scheduling case: a few missing transaction nodes
remain assigned to a slow peer while an alternate peer is available.

## Request policy

The actionable replay head can make one alternate attempt for each retained
transaction-node reservation after one second. The known outstanding frontier must contain
at most eight nodes. One alternate peer receives the batch, and both original
and alternate requests count toward the existing six-peer window. The original
peer's credit survives until its reply or original expiry. Sending the alternate
request does not create a second logical node reservation or restart the
original lease's deadline.

Candidate selection prefers peers advertising the ledger, then lower smoothed
ping latency. Unknown availability remains usable; missing latency uses a
250 ms estimate. Recently unavailable, disconnected, or explicitly behind peers
are excluded from the alternate attempt. Ordinary acquisition and escalation
remain available when there is no suitable alternate or no spare request slot.

Only the next replay ledger with a ready parent qualifies. Future prefetched
entries and full-state acquisitions retain their existing scheduling. Verified
late replies remain useful. Transaction identity, metadata, roots, trusted
validation checks, and full/partial validation eligibility are unchanged.

## Diagnostics

The `replay head acquisition` INFO record is limited to one every five seconds
across head changes. Its counters describe work observed while that acquisition
is the actionable head; prefetched work before that point is not included in
the send and byte counters. Transaction node counters come from the acquisition
and include its earlier progress.

| Field | Meaning |
| --- | --- |
| `age` | Time since the acquisition was requested |
| `planner_wait` | Last observed planner condition |
| `outstanding_nodes`, `request_leases` | Bounded node reservations, including fulfilled nodes retained solely for outstanding hedged replies |
| `oldest_request_age` | First assignment age of an outstanding node; retained across retries |
| `hedged_nodes` | Hedge count across retained reservations; `hedge_requests` counts attempts cumulatively |
| `leased_requests`, `leased_request_nodes`, `leased_send_failures` | Leased request attempts, including failed admission; legacy fan-out is excluded |
| `hedge_requests` | Cumulative alternate attempts while this ledger is the head; includes failed admission |
| `leased_send_admission_max` | Maximum time spent admitting a leased send to the network API; not wire transmission time |
| `last_peer_ping_rtt` | Smoothed peer ping latency; not measured node-request latency |
| `reply_payload_bytes`, `reply_wire_bytes` | Received decompressed payload and compressed wire payload bytes, respectively; wire headers are excluded |
| `ingress_queue_max` | Peer dispatch to acquisition-worker admission, including overlay/router waiting |
| `worker_queue_max` | Worker admission to first reply processing |
| `reply_process_max` | Longest reply-processing pass |
| `worker_batch_max` | Longest worker batch, including traversal and checkpoint work |
| `result_queue_max` | Worker result publication to coordinator handling |
| `tx_nonuseful` | Received transaction nodes minus useful nodes; can include duplicates and rejected data |
| `ready_blocked`, `ready_wait_max` | Number and longest wait of ready successors behind the head |

## Measurement

Acceptance uses the existing #2090 fixture: 250 Batch transactions with eight
inner AccountSets each, one OfferCreate and one Payment. Its 2,252 closed-ledger
entries differ from the incident's 2,020 entries. Refresh-enabled samples wait
for a real promotion before acceptance, with 16,384 background state entries,
a small block cache and an injected promotion delay. This measures a controlled
competing workload, not Testnet hardware or the incident's exact CPU usage.

The benchmark reports sample count, mean, p50, p95 and p99 for acceptance and
its phases. The refresh interval overlaps acceptance and must not be added to
it. Run from the repository with its native dependencies configured:

```sh
go test ./internal/ledger/service -run '^$' \
  -bench '^BenchmarkService_Issue2090FullAcceptance$' -benchtime=100x -count=1
```

Maintenance scheduling remains tracked in #2066, with read amplification in
#1853 and #1812. This change does not claim those workloads caused the initial
drop, or that a simulated acquisition improvement predicts a specific reduction
in missed or partial validations on a live network.

### Local acceptance results

Measured on 2026-10-22, Apple M3 Pro, darwin/arm64, Go 1.25.0,
`GOMAXPROCS=3`. Baseline and changed binaries ran sequentially, with 100
samples per refresh setting and no concurrent builds or Go tests from this
task. The baseline was `89b69673b` plus the same benchmark instrumentation.
The acceptance implementation is unchanged; these runs do not exercise
acquisition. They show run-to-run variation, not an acceptance speedup.

All times below are milliseconds. The refresh interval overlaps acceptance.

| Build | Refresh | Phase | Mean | p50 | p95 | p99 |
| --- | --- | --- | ---: | ---: | ---: | ---: |
| before | off | acceptance | 199.36 | 198.04 | 207.69 | 217.75 |
| before | off | parse | 36.14 | 35.83 | 38.24 | 39.88 |
| before | off | transaction-apply | 131.85 | 130.94 | 137.48 | 143.17 |
| before | off | next-open | 30.74 | 30.30 | 32.81 | 33.87 |
| before | on | acceptance | 220.38 | 218.49 | 230.24 | 258.60 |
| before | on | parse | 42.22 | 41.46 | 45.73 | 61.92 |
| before | on | transaction-apply | 146.98 | 146.32 | 154.38 | 164.32 |
| before | on | next-open | 30.58 | 30.20 | 33.73 | 37.91 |
| after | off | acceptance | 200.93 | 199.20 | 210.96 | 223.85 |
| after | off | parse | 36.37 | 35.86 | 38.24 | 40.86 |
| after | off | transaction-apply | 132.71 | 131.76 | 138.55 | 149.89 |
| after | off | next-open | 31.21 | 30.80 | 33.91 | 37.00 |
| after | on | acceptance | 229.71 | 226.68 | 256.41 | 279.45 |
| after | on | parse | 44.19 | 43.44 | 51.49 | 57.83 |
| after | on | transaction-apply | 153.06 | 151.43 | 169.82 | 180.32 |
| after | on | next-open | 31.73 | 30.91 | 34.78 | 46.08 |

The refresh-on acceptance mean was 220.38 ms before and 229.71 ms after;
the refresh-off mean was 199.36 ms before and 200.93 ms after. These laboratory
values do not reproduce the incident’s 2.12-second acceptance or establish
its CPU cause.

### Controlled acquisition results

`TestTransactionOnlyTailRecoverySimulation` compares the normal three-second
retry timer with the one-second hedge on a verified transaction tree missing
eight leaves. Seven distinct original-peer delays (0.4, 0.9, 1.1, 2, 2.5, 4
and 8 seconds) share a 100 ms response delay on both paths. Replies are applied
through production acquisition code and the tree must finish verification.
The clock advances in 100 ms steps; these are simulated event times, excluding
local CPU and transport overhead. With seven cases, p95 and p99 are both the
maximum observed case, not estimates of a live-network tail distribution.

| Policy | Recovery p50 | p95 | p99 | Requests across 7 cases | Request NodeID bytes |
| --- | ---: | ---: | ---: | ---: | ---: |
| Normal retry | 2.1 s | 3.1 s | 3.1 s | 9 | 2,376 |
| Head hedge | 1.1 s | 1.1 s | 1.1 s | 12 | 3,168 |

This case trades 33% more request attempts and NodeID bytes for earlier
completion. NodeID bytes exclude message envelopes, headers and response bytes;
they are not full wire-byte amplification. Each retained node reservation gets at most one hedge,
and outstanding original requests retain their slots even when an alternate
reply or cache placement exposes more children.

A separate router test builds two canonical successors, each containing two
signed Payments. The later successor becomes ready while the head is held;
a one-second virtual hedge releases the head and the ordered replay drain.
The final header, ledger hash and transaction/state roots match the locally
built canonical ledgers. Silent and partial replies, late replies, failed sends,
expiry, a full peer window, cancellation and retirement are also covered.
Live recovery time, ready-wait percentiles and missed/partial-validation counts
have not been measured for this change.

### Future incident capture

The [slow-acceptance recorder](../../scripts/profile-slow-acceptance.md)
retains bounded CPU windows overlapping a slow acceptance log event, then
captures an execution trace and goroutine snapshot. It is an external operator
tool and requires the existing local pprof endpoint. No future production
incident has been captured as part of this change; exact CPU attribution and
live missed/partial-validation improvements remain unmeasured.
