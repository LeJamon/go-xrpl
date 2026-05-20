# Issue #470 — heavy-load avalanche fork (post-2026-05-20)

After the empty-PreviousFields fix (`4644faa`), iter13 reached vseq=40
(up from vseq=7 on every prior iteration in this session). The
remaining fork mode is a heavy-load avalanche divergence.

## Resolution (2026-05-20, commits 7dacabc + 0d5a5d6)

**Root cause identified and fixed** for the iter14 L10 fork mode (3-vs-2
validator split, same parent + same tx_set → different ledger_hash).

The divergent ledger had exactly one tx (`EF7A1C7D…`) whose meta diverged:
goxrpl emitted a 399B meta with a *ghost* `ModifiedNode` for a
`RippleState` (TrustLine) that was previously touched by an earlier tx
in the same ledger but otherwise unchanged. Rippled v2.6.2 standalone
emits 151B for the same tx — only the sender's `AccountRoot` (fee +
Sequence), no `RippleState` modification.

The bug was in `internal/tx/apply_state_table.go:applyThreading()` for
the `ActionModify` case. When apply code (e.g. `TrustSet`) called
`View.Update(key, bytes)` with bytes identical to the cached SLE, the
Action was promoted Cache→Modify and `applyThreading` then mutated
`entry.Current` with `PreviousTxnID`/`PreviousTxnLgrSeq`. The
`bytes.Equal(Original, Current)` skip in the metadata emit loop then
no longer fired because `Current` had been threaded. Result: a bare
`ModifiedNode` with only `PreviousTxnID`/`PreviousTxnLgrSeq` + bare
`FinalFields`, no `PreviousFields`.

Rippled handles this at the meta emit loop
(`ApplyStateTable.cpp:156-157`):
```cpp
if ((type == &sfModifiedNode) && (*curNode == *origNode))
    continue;
```
The no-op check happens *before* `threadItem` is called. goxrpl runs
`applyThreading` as a separate pre-pass, so the skip must move there
too — commit `7dacabc` adds `if bytes.Equal(Original, Current)
{ continue }` at the top of the `ActionModify` branch.

Regression test: `internal/testing/trustset/repro_noop_modify_test.go`
(commit `0d5a5d6`) reproduces the 399B-vs-151B byte counts directly.

**Status**: Awaiting iter15 soak run with rebuilt `goxrpl:latest`
image to confirm vseq advances past L10.

## Repro pattern

Soak network: 3 rippled v2.6.2 + 2 goxrpl. Fuzz harness submits ~5
tx/s with rotation. After ~30s of soak load, a "heavy" ledger lands
with 80–160+ disputed txs. Avalanche flips the disputes both sides
agree on the tx_set hash. Then:

- goxrpl-built L41: ledger_hash `5F540D5E8327CA72…`  with state_root
  prefix `E6198EC3…`, parent `3540858F…`, tx_set `541B5457…`, 98 txs.
- rippled-built L41: ledger_hash `01921FFEDCC6D1A6…` with the same
  parent `3540858F…` and the same tx_set `541B5457…` (98 txs per
  rippled's "Position change" log).

Same parent, same tx_set, **different ledger hashes** → apply
divergence on the same input.

Worth noting: the L41 stored in goxrpl-1's RPC after acquisition has
`transaction_hash=0000…0000` and `tx_count=0` even though
`account_hash` differs from L40. That's not "no txs" — rippled built
its winning L41 with the same 98-tx set; the displayed all-zero
tx_root either reflects only what goxrpl persisted after the chain
switch, or a separate logging-time issue (goxrpl's round-summary log
also prints `tx_root=0000` with `tx_count=98`, which is structurally
impossible).

## Why this doesn't recover

Validations are emitted before the chain switch:
- 2 goxrpls validate their `5F540D5E…` build.
- 3 rippleds validate `01921FFE…`.
- Quorum needs 4/5 same-hash validations → 3 vs 2 split → stalls.

Once stalled, goxrpls switch to `wrongLedger` mode and keep building
empty ledgers (mode=wrongLedger, tx_set=0000…, tx_count=0), but those
locally-built ledgers don't propagate validations on the canonical
chain.

## Concrete smoking gun in goxrpl's round-summary

For iter13 L41, goxrpl-1's round-summary log shows
`tx_count=98 tx_root=0000000000000000` *together*. Verified by
running `shamap.New(TypeTransaction).Hash()` — the empty tx tree's
root really is all-zeros for the TypeTransaction map. So `tx_root=0`
means the tx tree is empty.

Yet `state_root` differs from L40's `account_hash` (44B0CEBA… →
E6198EC3…) and `total_coins` dropped by 171821 drops (≈ 1750 drops ×
98 fees). So:

- State WAS modified (98 fees burned).
- Tx tree was NOT updated (zero leaves).

That's a structural bug: somewhere in the apply path, fee/sequence
commit and `AddTransactionWithMeta` are decoupled. The most likely
suspects:

1. `applyAndClassify` (`internal/ledger/openledger/apply.go:104-125`)
   silently discards the return value of
   `view.AddTransactionWithMeta(...)`. If that call returns
   `ErrLedgerImmutable` or any other error during build, the tx is
   dropped from the tree even though `engine.Apply` already committed
   fee/state via the apply-state table.
2. `commitPreclaimTec` path (`internal/tx/apply.go:265`): for
   preclaim-tec on the final pass (TapRETRY=false), the recovery
   apply-state table commits fee/sequence directly to `e.view`
   *before* `AddTransactionWithMeta` is called by the caller. If
   anything between the two calls flips the view's state, the tree
   misses the entry. Worth confirming by logging the return of
   `AddTransactionWithMeta` for one stall ledger.
3. `TxMapHash()` is called after `Close()` — if `Close()` is the
   thing that returns from the apply-loop-aware state but doesn't
   surface the tree from a sandbox, the tree could legitimately be
   empty in the closed ledger even though apply seemed to run.

The fastest concrete next step is to instrument
`applyAndClassify` to log every `AddTransactionWithMeta` return
(error or not) and rerun the soak. The first non-nil error in a
stall ledger is the bug.

## Likely bug locations

1. **Per-tx apply ordering or canonical sort.** Both sides agree on
   the tx_set's SHAMap root (`541B5457`), but the canonical-sort step
   that determines apply order might differ when there are
   sequence-chained txs from the same account.
2. **Retry-pass txCount seeding (Issue #470 historical fix referenced
   in `internal/ledger/openledger/apply.go:193-199`).** The fix
   addressed duplicate TransactionIndex on retry passes. If the same
   pattern leaks into a 3-pass `BuildLedgerMode` with high tx volume,
   per-tx metadata may diverge.
3. **`tef`/`ter` classification.** If goxrpl maps a result to "drop
   tx, do not add to tx tree" while rippled maps it to "include with
   tec result", the SHAMap tx tree diverges. The fuzz harness FATAL
   on `tefPAST_SEQ` at the exact L41-divergence timestamp suggests
   sequence-driven failures are in scope.

## How to find the divergent tx

The 98-tx tx_set `541B5457287B8C5BA441BF6E28C9177B37498F866059CC850A3D9B1F99DC5B5B`
is the AGREED set. Both sides should have all 98 tx_blobs in their
mempool. To reproduce:

1. Restart soak; capture the agreed tx_set's tx_blobs from goxrpl's
   tx-acquire log or via a peer-level capture as soon as the heavy
   ledger lands.
2. Submit those tx_blobs in canonical order to a fresh
   diff-smoke-rippled standalone (v2.6.2) and to a fresh goxrpl
   standalone, against the SAME L40 parent state.
3. Compare per-tx `meta_blob` and post-tx `state_root` to find the
   first divergent tx.

The previous "byte-diff" infrastructure in
`internal/testing/trustset/repro_byte_diff_test.go` is the template.
Extend it to a multi-tx scenario and feed the tx_blobs captured from
the soak.

## Confirmed fixes from this session (do not revert)

- `8fc4a19` — gate PreviousTxnID emission on isThreadedType
  (prevented spurious DirectoryNode threading without
  fixPreviousTxnID amendment).
- `4644faa` — emit empty `sfPreviousFields` only when a
  sMD_ChangeOrig-eligible field went absent→present in this modify
  (mirrors rippled v2.6.2 prevs.empty() behavior — `prevs`
  contains STI_NOTPRESENT template entries that serialize to zero
  bytes but make the `if (!prevs.empty())` gate fire).

Both fixes verified byte-for-byte against rippled v2.6.2 standalone
(`diff-smoke-rippled` container, 743-byte pagination meta match).
