# Issue #470 — Standalone rippled replay findings

## Setup
- Standalone rippled (rippleci/rippled:2.6.2) container `rippled-standalone`.
- Replay script `issue-470-standalone-replay.py` consumes goxrpl ledger blobs
  (via `ledger ... binary:true`) and submits each tx blob in canonical
  TransactionIndex order to standalone, then calls `ledger_accept` per seq.

## Result — goxrpl apply is byte-for-byte rippled-faithful

After replaying seqs 7..12 from a soak run:

| Field at seq 11 | goxrpl | standalone | match |
|---|---|---|---|
| `transaction_hash` | `D5C1DF4A4C9450D8...` | `D5C1DF4A4C9450D8...` | ✓ |
| `total_coins` | `99999999999991682` | `99999999999991682` | ✓ |
| `close_flags` | 0 | 0 | ✓ |
| State SLEs | 17 | 17 | 16/17 ✓ |

At seq 12:

| Field at seq 12 | goxrpl | standalone | match |
|---|---|---|---|
| `transaction_hash` | `11CB59B763F46111...` | `11CB59B763F46111...` | ✓ |
| `total_coins` | `99999999999958850` | `99999999999958850` | ✓ |
| `close_flags` | 0 | 0 | ✓ |

The single differing SLE in both seqs is **LedgerHashes**, and the difference
is purely propagated from the close-time history: standalone's `ledger_accept`
uses wall-clock close times (e.g. seq 11 = `832501944`) instead of the
recorded soak close times (e.g. seq 11 = `832499060`), so historical
`ledger_hash`es differ, and those hashes are what `LedgerHashes` stores.

## What this means

For identical inputs (same parent state, same canonical tx set, same fees),
goxrpl's apply pipeline produces byte-identical state SLEs and a byte-
identical tx+meta SHAMap root to rippled. **The remaining intermittent soak
stall at low-double-digit seqs is therefore NOT a tx-apply bug.**

## Where to look next

The soak diverges with matching close_times (both impls negotiate the same
`832499061` for seq 12). With apply byte-equal, the divergence has to be in
a header field that's NOT part of apply:

1. `close_time_resolution` — defaults to 10s; if one impl computes a
   different bin width per seq the headers differ. Worth a 30-line check
   in `consensus.GetNextLedgerTimeResolution`.
2. `ParentCloseTime` — should equal parent's `close_time`; an off-by-one
   in either impl's header construction would diverge.
3. `total_coins` accounting — fees burned should match (and did in this
   replay) but a Pay-fee path that double-burns or skips a burn under a
   specific tx mix would diverge.
4. The `addRaw` ledger-header serialization itself (field order, type
   tags) — the header isn't routed through the apply state table.

The standalone-rippled + replay infrastructure here is the right oracle
for chasing each of these.
