# Ledger serving scheduler benchmark

The benchmark compares the former shared FIFO with the two fair serving pools.
Both receive the same 512 synthetic jobs: 448 ordinary ledger requests and 64
transaction-set requests, using four workers and 256 total pending slots.
`bulk_state_first` offers ordinary work first; `bulk_state_reverse` offers the
64 transaction-set requests first. Admission finishes before workers start,
then a barrier holds the first four active jobs until a foreground probe starts.
This isolates full-queue admission policy; the router tests separately exercise
already-running handlers blocked in real SHAMap reads.

These measurements used Go 1.24.4 on an Apple M3 Pro (`darwin/arm64`,
GOMAXPROCS=11), with two repetitions of 100 iterations. The machine was shared
with other verification work, so timings include scheduling noise.

```sh
GOTOOLCHAIN=go1.24.4 just test-pkg './internal/consensus/adaptor -run "^$" -bench "^BenchmarkLedgerServingScheduler$" -benchtime=100x -count=2 -cpuprofile=/tmp/ledger-serving.cpu'
go tool pprof -top /tmp/ledger-serving.cpu
```

Each pair below is ordinary / txset. Semicolons separate the two runs.
Queue percentiles cover admitted jobs only. Zero for a class with no completed
jobs means no samples, not zero latency.

| Scenario / mode | ns/op | Completed | Dropped | Queue p95 (us) | Queue p99 (us) | Bytes/op | Allocs/op |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| bulk_state_first/fifo | 191205; 180130 | 256.0 / 0 | 192.0 / 64.00 | 160.9 / 0; 171.8 / 0 | 516.2 / 0; 267.7 / 0 | 28901; 28597 | 37; 36 |
| bulk_state_first/fair | 789143; 1043319 | 192.0 / 64.00 | 256.0 / 0 | 1043 / 247.9; 1471 / 711.4 | 2326 / 1251; 2028 / 1265 | 314544; 314073 | 2817; 2816 |
| bulk_state_reverse/fifo | 340276; 411413 | 192.0 / 64.00 | 256.0 / 0 | 476.3 / 325.5; 593.3 / 477.6 | 1153 / 595.9; 3194 / 1506 | 29070; 29120 | 43; 43 |
| bulk_state_reverse/fair | 1587267; 1005201 | 192.0 / 64.00 | 256.0 / 0 | 2371 / 1987; 1333 / 1377 | 4703 / 3714; 2373 / 2126 | 314031; 313906 | 2816; 2815 |

FIFO admits 256 ordinary jobs and drops all 64 transaction-set requests when
ordinary work arrives first. The new scheduler admits 192 ordinary and 64
transaction-set jobs in either order. This protection costs additional
allocation and scheduling overhead, clearly visible with these tiny job bodies;
it is not a throughput improvement. Reserved workers and queue slots remain
unavailable to the other class even when idle.

The foreground probe is a fixed arithmetic loop, measuring about 0.46–3.47 us
in these runs. It is not a ledger read or ledger-application latency measurement.
A combined CPU profile collected 720 ms of samples over 803 ms, mostly in
runtime scheduling/synchronization; only 30 ms was attributed directly to the
synthetic job loop. This short combined profile does not establish per-class CPU
cost or production CPU savings.

The benchmark does not read a production ledger, walk a production SHAMap,
encode a response, or send through a peer. Queue waits and ns/op are wall-clock
measurements. The results establish admission accounting and expose scheduler
overhead; they do not establish consensus latency or agreement improvements.
