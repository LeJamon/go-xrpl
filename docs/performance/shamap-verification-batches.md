# Read-only startup verification batches

Strict startup verification uses `KVDatabase.FetchBatchUncached` when available.
Pebble resolves sorted keys with one iterator per active generation per batch,
preferring writable records over archive records. Reads do not promote records or
populate decoded-node caches. Other node stores retain the scalar traversal.

The default limits are 256 nodes and 4 MiB of encoded values per batch. Results
are a prefix of the sorted requests, including duplicate and missing keys. A
first oversized record is returned alone so traversal can make progress; later
records that exceed the remaining byte budget wait for the next batch. Missing
records still fail strict verification. All existing hash, type, depth, and
completeness validation remains in the traversal, and failed or canceled walks
do not publish completeness proofs.

## Measurement environment

Local measurement: September 8, 2026; macOS/Darwin arm64, Go 1.26.1, SSD-backed
APFS workspace. These measurements cannot establish the HDD deployment speedup
reported in issue #1865. No shared operating-system caches were cleared.

The fixture contains 16,384 synthetic leaves, with 17,541 visited nodes and
1,381,802 logical payload bytes per traversal. Its root is
`f223f122c12535a858b025349c1d326314fa893cf093b31a3f47eacae95cfe0b`.
Four workers are used. Records are in the archive generation and are flushed to
SSTables before reopening. The small-cache case reopens a 256 KiB Pebble block
cache for each traversal. The warm case reopens an 8 MiB cache and traverses the
same root before timing. Neither case guarantees a cold operating-system cache.
This compact synthetic tree is not a production ledger snapshot.

Each table entry is the median of three samples, each with three timed
traversals. Fixture creation, reopening, warm-up, and explicit GC are outside
the Go benchmark timer. Every sample returned the same node and logical-byte
counts. The selected run had no concurrent lint/test jobs from this task; other
host activity was uncontrolled.

## Traversal measurements

| Cache | Nodes/batch | ms/traversal | Nodes/s | Allocated MiB/traversal | Allocs/traversal | Block-cache misses/traversal |
|---|---:|---:|---:|---:|---:|---:|
| Small, reopened | scalar | 117.09 | 149,807 | 13.40 | 223,673 | 52,623 |
| Small, reopened | 32 | 57.63 | 304,381 | 13.75 | 182,105 | 14,970 |
| Small, reopened | 128 | 31.58 | 555,524 | 14.05 | 174,481 | 8,410 |
| Small, reopened | 256 | 31.26 | 561,093 | 15.02 | 173,209 | 5,060 |
| Small, reopened | 1024 | 18.99 | 923,670 | 16.01 | 172,543 | 2,634 |
| Warm | scalar | 13.16 | 1,332,679 | 13.41 | 223,668 | 0 |
| Warm | 32 | 11.67 | 1,502,766 | 13.60 | 179,336 | 0 |
| Warm | 128 | 14.58 | 1,203,387 | 14.01 | 173,711 | 0 |
| Warm | 256 | 30.63 | 572,634 | 15.00 | 172,787 | 0 |
| Warm | 1024 | 13.84 | 1,267,506 | 15.99 | 172,283 | 0 |

In this run, the default 256-node batch reduced the small-cache median from
117.09 ms to 31.26 ms. The warm-cache median regressed from 13.16 ms to 30.63 ms;
the three 256-node warm samples ranged from 20.97 to 39.27 ms. Other batch sizes
also varied. These results do not establish a general speedup or an optimal
batch size. The 256-node default retains a conservative existing traversal
bound; the 4 MiB limit bounds returned encoded payloads independently. The
production HDD workload remains unmeasured.

## Process and disk observations

The following counters cover the **entire benchmark process**, including fixture
creation, calibration, cache warm-up, reopening, and cleanup. They are not
verification-only measurements and must not be used to calculate its physical
read amplification. CPU and peak RSS come from `getrusage(RUSAGE_CHILDREN)`.
Process-attributed disk bytes are the last successful 10 ms sample of Darwin
`proc_pid_rusage` with `RUSAGE_INFO_V2`, so they can omit activity after the final
sample. Fixture creation accounts for approximately 24.2 MB of process writes
per case; the read-only traversal does not write node-store records.

| Cache | Nodes/batch | Process elapsed s | CPU user+system s | Peak RSS MiB | Sampled process storage-read bytes |
|---|---:|---:|---:|---:|---:|
| Small, reopened | scalar | 11.75 | 12.93 | 85.50 | 21,250,048 |
| Small, reopened | 32 | 11.00 | 10.09 | 86.11 | 4,206,592 |
| Small, reopened | 128 | 10.31 | 8.91 | 85.30 | 327,680 |
| Small, reopened | 256 | 9.75 | 8.43 | 84.66 | 303,104 |
| Small, reopened | 1024 | 9.55 | 8.16 | 83.72 | 180,224 |
| Warm | scalar | 9.81 | 8.95 | 93.08 | 65,536 |
| Warm | 32 | 15.75 | 9.40 | 94.12 | 184,320 |
| Warm | 128 | 22.53 | 10.92 | 94.12 | 3,584,000 |
| Warm | 256 | 21.05 | 12.15 | 95.86 | 3,723,264 |
| Warm | 1024 | 17.42 | 11.26 | 89.50 | 4,071,424 |

Read-only `iostat -d -w 1` sampling of host `disk0` recorded
5,361 median and 25,075 maximum transfers/second
across 149 one-second samples, excluding its initial since-boot aggregate.
These are host-wide reads and writes, including fixture setup and other
processes. Per-request disk latency was unavailable from this macOS `iostat`
interface and was not measured. The issue's HDD latency/IOPS results therefore
cannot be compared directly with this run.

## Reproduction

From the repository root:

```sh
just test-pkg './internal/ledger/service -run ^$ -bench BenchmarkService_VerifyStoredSHAMapBatch -benchtime=3x -count=3 -benchmem'
```

For process-level measurements, compile once and run each case in its own
process; for example:

```sh
go test -c -o /tmp/verification.test ./internal/ledger/service
/usr/bin/time -l /tmp/verification.test -test.run='^$' \
  -test.bench='^BenchmarkService_VerifyStoredSHAMapBatch$/^warm=false$/^batch=256$' \
  -test.benchtime=3x -test.count=3 -test.benchmem
```

Use `warm=true` for prewarmed cases and `scalar`, `batch=32`, `batch=128`, or
`batch=1024` for the other paths. `/usr/bin/time -l` provides elapsed time, CPU,
and peak RSS on macOS; process disk-byte counters require the Darwin sampling
API described above. On a dedicated Linux test host, collect process I/O and
block-device latency alongside these same benchmark cases. Do not clear shared
host caches or benchmark against a live writable production database.
