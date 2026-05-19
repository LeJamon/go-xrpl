# Issue #470 — residual metadata-bytes divergence (post-session-2026-05-19)

After four fix commits this session (`6f01b20`, `39e7371`, `953e56f`, `0572dc3`)
the network still stalls when a heavily-loaded ledger contains 50+ txs whose
agreed tx_set was reached via dispute resolution. The remaining failure mode
is precisely characterised below so the next investigator can pick up cleanly.

## What we know is OK

- All 5 validators agree on the tx_set hash at build time (confirmed via
  `propose-recv` logs: `diff=false` from every peer at the final position).
- `tx_count` matches across goxrpl and rippled at build time (56 in the iter7
  case).
- Apply produces the **same final state** on both sides
  (`account_hash` matches: `A75BDC044803B510` in iter7 seq 8).
- All 56 txs return `tesSUCCESS` on goxrpl, with plausible AffectedNodes
  shapes (one 2-node modify of an existing trust line, one 6-node first-touch
  with new dir page, rest are standard 5-node TrustSet creates).
- Per-account `PreviousTxnID` threading is correct across the 49-tx run from
  one account (each tx's `PreviousTxnID` matches the prior tx's hash).
- `OwnerCount` delta is correctly `+1` per TrustSet (verified across all 49
  rEbGhvVF AccountRoot modifications in iter7 seq 8).
- The dispute tracker is verified working via `953e56f` instrumentation
  (flipped 33 + 67 votes across two consensus rounds in iter6).

## What is NOT OK

`transaction_hash` differs between goxrpl-built and peer-built ledgers
despite identical `account_hash`. This means the SHAMap **tx+meta tree**
roots differ — i.e., at least one tx's `(tx_blob, meta_blob)` leaf hash is
different.

For iter7 seq 8:
- goxrpl-built: `ledger_hash=2DB765031C279A42` `transaction_hash=A4A778A35865E379`
- peer-built:   `ledger_hash=3E3C6021138A14EC` (peer's `transaction_hash` not
  available because rippled doesn't persist unvalidated ledgers).

A tx-tree leaf hash for `tnTRANSACTION_MD` is
`sha512Half(HashPrefix::txNode, item.slice(), item.key())`.
- `item.key()` = txID = `sha512Half(HashPrefix::transactionID, tx_blob)`.
  Both impls produce the same value (they agree on tx_set hash, which depends
  on these keys).
- `item.slice()` = `[VL-length][tx_blob][VL-length][meta_blob]`.
  The tx_blob is byte-identical (consensus agreement). The meta_blob must
  therefore differ for at least one tx.

So the residual bug is **`SerializeMetadata` produces different bytes than
rippled for at least one of the 56 txs**, despite the in-memory metadata
structure (AffectedNodes / FinalFields / PreviousFields / PreviousTxnID)
appearing rippled-faithful when inspected via JSON.

## Captured data

- `docs/issue-470-iter7-seq8-goxrpl-built.json` — goxrpl-1's locally-built
  version of iter7 seq 8 with full tx_json + metadata for all 56 txs.
- For the binary blob version (with `tx_blob` and `meta_blob` hex strings):
  query `goxrpl-1` RPC with `binary:true` on the iter7 enclave or replay the
  same fuzz seed.

## Next investigation steps

The bug requires byte-level comparison between goxrpl's `meta_blob` and
rippled's `meta_blob` for the same tx, applied to the same initial state.
There is no current tooling for this — the closest is
`docs/issue-470-standalone-replay.py` but it requires:

1. Spinning up a fresh standalone rippled (e.g. `diff-smoke-rippled`).
2. Replaying *all* state-affecting txs from goxrpl's seqs 1-7 (fund txs,
   any initial trust lines, etc.) onto the standalone to reproduce the seq
   7 state exactly. Note: a naïve `submit + ledger_accept` per goxrpl seq
   does NOT produce the same seq-7 state because the standalone's own ledger
   sequence diverges from goxrpl's. The standalone-replay flow needs to
   wait until the standalone's `validated_ledger.seq` matches goxrpl's
   before submitting seq 8 txs, OR use raw account funding to recreate the
   exact pre-seq-8 state.

3. Submit each iter7 seq 8 tx individually via `submit`, capture each
   `meta_blob` from the standalone's `tx` RPC.

4. Diff goxrpl's `meta_blob` against the standalone's `meta_blob` for each
   tx; the first byte-difference identifies the failing field. Likely
   suspects (based on rippled `ApplyStateTable.cpp` reading):
   - Field-order divergence inside FinalFields / PreviousFields for some
     SLE type.
   - sfDestination / sfAccount / similar AccountID field encoding when an
     AccountRoot is freshly created.
   - PreviousFields content for an SLE that mutates multiple times within
     the same ledger (we verified OwnerCount + PreviousTxnID threading;
     other fields may be missing or extra).
   - Differing handling of `DeliveredAmount` (snake-case vs camelCase or
     similar).

## Verified fixes from this session (do NOT revert)

- `6f01b20` — sort `sfIndexes` within owner-dir pages on insert.
- `39e7371` — keep `n.hashes[i]` in sync with `child.Hash()` during SHAMap
  split.
- `953e56f` — instrument `DisputeTracker.UpdateOurVote`.
- `0572dc3` — capture iter7 seq 8 ledger snapshot for diff investigation.
