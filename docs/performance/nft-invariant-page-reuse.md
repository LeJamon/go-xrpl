# NFT invariant page reuse

`checkValidNFTokenPage` retains the initial successful before/after parses for
one entry and reuses them for the lost-link check. It does not cache decoded
pages across entries or transactions. Parse errors, validation order, the
amendment gate and the separate raw URI walks remain in place.

A separate correctness commit repairs explicit-empty URI detection. The field
walker includes the variable-length prefix, so an empty URI is encoded as one
zero byte, not an empty slice. Absent URIs and non-empty URIs (including a single
zero payload byte) remain valid. This restores the URI failure and its existing
precedence over page-size failures, matching rippled's invariant.

## Measurement

Measured on Apple M3 Pro, Darwin arm64, Go 1.24.4. Baseline is commit
`96f2712b045940cdce6c4c6426d0bc84bc9234a8` with the same benchmark fixtures;
the measured reuse-only version changes the invariant's repeated parsing and
predates the separate URI repair. All benchmark pages have absent or non-empty
URIs. Each value below is the
median of five 100 ms samples with `-cpu=1`. Fixture serialization is outside
the timer. No other build/test jobs from this task ran during these samples;
unrelated host activity was uncontrolled.

```sh
GOTOOLCHAIN=go1.24.4 just test-pkg './internal/tx/invariants -run ^$ -bench ^BenchmarkValidNFTokenPage$ -benchmem -benchtime=100ms -count=5 -cpu=1'
```

The matrix covers unchanged, modified, inserted and deleted non-final pages,
1/16/32 tokens, and absent/32/256-byte URIs. Modified pages change URI contents
when present and change `NextPageMin`. Rules explicitly disable
`fixNFTokenPageLinks`; the reporting gate is unchanged and does not affect
parse counts. The unchanged case passes both images directly to the invariant,
although a production apply table may prune an unchanged entry earlier.

| Case | Tokens | URI bytes | ns/op, before → reuse | B/op, before → reuse | allocs/op, before → reuse |
|---|---:|---:|---:|---:|---:|
| unchanged | 1 | 0 | 8,029 → 4,286 | 6,592 → 3,296 | 128 → 64 |
| unchanged | 32 | 256 | 848,589 → 431,725 | 702,725 → 351,362 | 4,224 → 2,112 |
| modified | 1 | 0 | 8,611 → 4,620 | 7,104 → 3,552 | 136 → 68 |
| modified | 32 | 256 | 849,382 → 431,432 | 703,238 → 351,619 | 4,232 → 2,116 |
| inserted | 1 | 0 | 2,118 → 2,106 | 1,648 → 1,648 | 32 → 32 |
| inserted | 32 | 256 | 214,927 → 215,630 | 175,681 → 175,681 | 1,056 → 1,056 |
| deleted | 1 | 0 | 2,111 → 2,275 | 1,648 → 1,648 | 32 → 32 |
| deleted | 32 | 256 | 215,096 → 215,695 | 175,681 → 175,681 | 1,056 → 1,056 |

[All 36 measured cases](nft-invariant-page-reuse.csv) include parse counts and
allocation results. For modified 32-token pages with 256-byte URIs, the median
fell from 849,382 to 431,432 ns/op (49.2%), and allocations from 4,232 to 2,116.
Insertion/deletion retains one parse and unchanged allocation counts; timing
variation on those controls is not an optimization claim.

Parse counts are checked separately with Go statement coverage in
`internal/ledger/state`, one benchmark iteration per case, using the entry block
of `ParseNFTokenPage`. Unchanged and modified non-final pages go from four
parses to two; inserted and deleted pages stay at one. Timing samples do not
include coverage instrumentation. The two raw URI walks for a modified page
remain unchanged.

## Replay and scope

`TestNFTBatchReplayParity` builds a synthetic 33-NFT parent through ledger closes,
checks its recorded header/state/transaction roots, then replays create-offer
and accept-offer batches with four inner transactions each. Tokens are selected
from a non-final page. Both versions match all ten result codes, every canonical
metadata hash, and the resulting ledger/state/transaction roots recorded from
the baseline. This is local replay equivalence, not an independent historical
network oracle. The incident's Testnet parent and Batch ledger data were not
available in the repository.

No typed-codec change from #2082 is included, so no combined speedup is claimed.
The generic `decodeEntry` in `internal/tx/invariants/lending.go` and
`decodeSponsorshipState` in `sponsorship.go` still materialize generated entries
before reading their fields; an NFT page can therefore still decode its token
array in those paths. Those costs were audited but not optimized or measured
here. These microbenchmarks cannot attribute the incident's entire decoder CPU
sample to this invariant or predict end-to-end acceptance time.
