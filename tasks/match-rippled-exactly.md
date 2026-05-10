# Goal: Match rippled exactly — independent same-hash consensus every round

**Date opened:** 2026-05-10
**Scope:** Make goxrpl's consensus engine independently produce the same ledger hash as rippled every round, fast enough that goxrpl can sit in the UNL alongside rippled without ever wedging to `wrongLedger`.

This is the long-form follow-up to the working observer-validator setup (memory: `issue-401-bootstrap-fork-seq6.md` updates #4–#5). The structural deadlock when goxrpl is in the UNL (memory updates #6–#7) is the proof that something is still off — not in tx execution (proven deterministic by `meta-diff.py` + `goxrpl-self-determinism.py`), but in consensus convergence under live peer pressure.

---

## What we already proved (don't re-do)

1. **Tx execution determinism.** `goxrpl-self-determinism.py`: two fresh goxrpl standalones receive the same 5 signed blobs → all hashes match.
2. **Tx execution matches rippled.** `meta-diff.py`: sign on rippled, submit identical blob to both → meta blob byte-for-byte identical when prev state matches.
3. **Cheap round-boundary jump works in observer mode.** Commit `5764f48` makes goxrpl follow rippled's chain at validator pace using rippled's validations as the preferred-LCL beacon.
4. **The all-7 commits in HEAD are rippled-faithful and correct.** No regressions to back out.

## What's still wrong (root list)

**The single observable failure:** with goxrpl in the UNL (5-validator topology, quorum=4), validated_seq stalls because goxrpl wedges to `wrongLedger` every round and emits zero validations. Rippled needs goxrpl's vote to hit quorum=4. Goxrpl needs… what?

Hypotheses to test, ranked by likelihood:

### H1 — Goxrpl's locally-built seq=N hash diverges from peers' very early

If goxrpl-built seq=5 has a different hash than rippled-built seq=5 *in a live network round* (not standalone), then at start of round 6 peers reference a different parent than goxrpl's local LCL, `checkLedger` sees `netLgr != ourID`, `handleWrongLedger` fires, mode=wrongLedger, no validation emitted, deadlock.

This is the FIRST thing to verify. Standalone determinism doesn't prove network determinism — different code paths can run when peer proposals arrive while we're still in `open` phase.

**Action:** instrument both goxrpl and rippled to log per-round inputs (prev_hash, tx_set_hash, close_time, close_resolution, parent_close_time) and the resulting built-ledger hash. Diff round-by-round. The first round where inputs match but output differs is the convergence-determinism bug.

### H2 — `OurPosition` formation in `closeLedger` differs from rippled

Rippled's `RCLConsensus::Adaptor::onClose` builds the initial position by snapshotting the open ledger state + applying held txs. Goxrpl's `closeLedger` (engine.go) does the equivalent — but does it use the same tx selection rule, the same canonical sort, the same close-time rounding?

**Action:** read goxrpl's `closeLedger` and rippled's `Consensus::closeLedger` + `RCLConsensus::Adaptor::onClose` side-by-side. Compare:
- which open-ledger txs go into the initial position
- close-time computation (`effCloseTime`, `roundCloseTime`)
- how `OurPosition.PreviousLedger` is set when we entered the round under switchedLedger vs proposing
- how the TxSet is built and hashed

### H3 — Avalanche dispute-resolution thresholds don't match rippled

Even if initial positions differ, rippled's avalanche mechanism (`updateOurPositions` + `getDisputes`) flips minority positions toward the majority over multiple establish iterations. Goxrpl has the same loop in `updatePosition` — but the threshold tables, percentage formulas, peer-unchanged counter, and convergence-time gates may be off.

**Action:** diff goxrpl's `updatePosition`/`updateOurPositions` against rippled's `Consensus<>::updateOurPositions` (Consensus.h). Verify:
- avalancheCutoff thresholds (50, 65, 70, 95 percent)
- the `PeerUnchangedCounter` increment / reset rules
- the `convergePercent` formula
- timeout-based `ourTime` computation

### H4 — Open-ledger tx-set divergence

Even with identical tx relay, goxrpl's open-ledger may differ from rippled's because:
- goxrpl applies retried (held) txs in a different order
- LoadFee / FeeEscalation rejects a tx on goxrpl that rippled accepts (or vice versa)
- a tx that's `tef` on one side is `tec` on the other (the relay-unless-tem/tef/tel filter could let a tef-locally tx onto the wire)

**Action:** at start of any round where positions diverge, dump both nodes' open-ledger tx-list and diff. If the lists differ, that's the bug — fix in apply / open-ledger / fee escalation. If the lists match, drop H4.

### H5 — Close-time computation differs

Rippled's `effCloseTime` rounds based on `closeResolution`, which itself adapts. If goxrpl rounds differently or uses a different resolution, two nodes with the same prev + same tx-set produce different ledger hashes (because `parent_close_time` and `close_time` are part of the ledger header).

**Action:** read goxrpl's close-time path vs rippled's `roundCloseTime`/`effCloseTime`. Particular attention to `closeTimeResolution` evolution (it doubles on disagreement) and `closeAgree` flag handling.

### H6 — Round-trip serialization of LedgerHeader differs by one byte

Even with identical inputs, if goxrpl's `LedgerHeader.Hash()` serializes differently than rippled's `LedgerInfo::sign`/`calculateLedgerHash`, the hashes diverge.

**Action:** unit test that takes a known fixture (header field by field) and compares goxrpl hash output vs rippled-computed reference hash. If we already have one, run it; if not, write it.

---

## Plan of attack

### Phase 0 — Reproduce and isolate (this session)

- [ ] Re-enable all-5 UNL in `xrpl-confluence/.worktrees/differential-harness/src/topology.star` with the corrected quorum formula `(N*8 + 9) // 10`.
- [ ] Run the soak with extra logging on round inputs/outputs (close_time, tx_set_hash, prev_hash, our_position).
- [ ] Capture the FIRST seq where goxrpl's locally-built hash diverges from rippled's locally-built hash. This pins down which hypothesis (H1–H6) applies.
- [ ] If goxrpl never reaches `closeLedger` (always wedges first), capture WHY checkLedger flips us — what does `getNetworkLedger()` return, and what's the trusted-vote breakdown.

### Phase 1 — Round-input parity (probably 1–2 weeks)

For whichever hypothesis the Phase 0 evidence points to:

- [ ] H1/H6 → header-hash unit tests + diff serialization
- [ ] H2 → side-by-side review and fix of `closeLedger` / position formation
- [ ] H3 → avalanche table + percentage parity work
- [ ] H4 → open-ledger / apply / held-tx-replay parity
- [ ] H5 → close-time rounding parity

### Phase 2 — Soak verification (1 week)

- [ ] All-5 UNL with new code, sustained 1000+ validated_seq lockstep
- [ ] No `mode=wrongLedger` events in steady state
- [ ] All 5 validators emit a validation per round at the network rate
- [ ] Cheap-jump path is rarely needed (≤ 1% of rounds — only on bona fide catch-up)

### Phase 3 — Tighten and document

- [ ] Remove (or repurpose) the cheap-jump as a catch-up-only path
- [ ] Memory note: how UNL mode now works, any remaining caveats
- [ ] Promote the differential round-input dumper into a permanent debug tool

---

## Working notes log

### 2026-05-10 — Phase 0 instrumentation landed (commit 1d9f20f)

Added two structured per-round log lines:
- `event=our-position` in `engine.closeLedger`: prev / tx_set_id / tx_count / close_time / mode
- `event=round-summary` in `service.AcceptConsensusResult`: seq / hash / parent_hash / close_time / close_time_correct / close_flags / state_root / tx_root / total_drops / canonical tx_hashes

These give us the raw evidence needed to compare goxrpl-vs-rippled per-round inputs/outputs once the next soak runs.

### 2026-05-10 — Strong H4 candidate found (open-ledger filter)

Side-by-side reading of rippled's `RCLConsensus::Adaptor::onClose` (RCLConsensus.cpp:317-405) vs goxrpl's `Engine.closeLedger` (engine.go:1582):

**Rippled** (in this order):
1. `ledgerMaster_.applyHeldTransactions()` — re-applies ter/retry txs
2. `setBuildingLedger(prev_seq + 1)` — prevents acquiring the ledger we're building
3. `auto initialLedger = app_.openLedger().current()` — snapshot of OPEN LEDGER (txs successfully applied to a working copy of LCL)
4. Iterate `initialLedger->txs` to build initial SHAMap — only txs in the **applied open ledger** are proposed
5. Inject pseudo-txs (flag/voting)
6. Snapshot, propose, censorship detector

**Goxrpl** (in this order):
1. `txs := e.adaptor.GetPendingTxs()` — raw relay pool, no open-ledger filter
2. Inject pseudo-txs (flag/voting)
3. `BuildTxSet(txs)`
4. Propose

**Why this matters:** rippled's `openLedger` is the result of *incrementally applying* valid txs to a working LCL copy. A tx that fails to apply (conflicts, fee escalation, tef/tem) is NOT in `openLedger->txs`. Goxrpl's `pendingTxs` is just `map[txID][]byte` — every tx the relay accepted, with no apply-time filtering.

Concrete consequences when goxrpl is in a UNL alongside rippled:
- Two txs from the same account at the same Sequence: rippled keeps the first one applied to open, drops the second; goxrpl proposes both.
- Fee escalation under load: rippled excludes low-fee txs from open; goxrpl includes them.
- ter/retry txs: rippled's `applyHeldTransactions` re-applies them in priority order before snapshot; goxrpl has no equivalent.

Result: goxrpl's initial position is a SUPERSET of rippled's. The dispute mechanism *should* converge them via avalanche during establish, but only if there's enough establish time. Under 4-5s rounds with 5+ peer positions, even small position deltas can prevent timely convergence — and goxrpl ends up running BuildLedger with whatever set wins, which may be different than rippled's, producing a different `transaction_hash` / `account_hash`.

**Action:** the proper fix is to introduce an "open ledger" abstraction in goxrpl's adaptor, parallel to rippled's `OpenLedger`. It maintains an applied-tx working copy of the LCL; pending txs are streamed through it via `apply` and only those that succeed (success or tec under retry) end up in `openLedger->txs`. `closeLedger` then iterates *that* set, not the raw relay pool.

This is a substantial change (likely 1–2 weeks):
- New type/file: `internal/consensus/adaptor/openledger.go`
- Lifecycle: rebuild on every accepted ledger (LCL change), apply pending txs in arrival/canonical order
- Wire `Engine.closeLedger` to source from open-ledger snapshot, not pending pool
- `AddPendingTx` becomes "try to apply to open-ledger; on fail, hold or drop"
- `applyHeldTransactions()` equivalent on round start

This is now the leading candidate for the next code change. Defer until soak diagnostics confirm position divergence is the real failure mode (vs build-path determinism — already proved correct in standalone).

### 2026-05-10 — H4 fix landed (commit f61199f)

Implemented the close-time open-ledger filter. The fuller incremental
OpenLedger abstraction can come later if needed; this version pays for
the filter once per round at propose time, against a fresh open view
of `parent`.

Wiring:
- `Service.FilterApplicableTxs(parent, txBlobs)` — side-effect-free
  multi-pass apply (3 passes, 1 retry pass), returns the blob subset
  that ended in `txSucceeded`.
- `Adaptor.GetProposableTxs(parent Ledger)` — wraps the service call
  via the LedgerWrapper unwrap.
- `consensus.Adaptor.GetProposableTxs(parent Ledger)` — new interface
  method. Mock returns raw GetPendingTxs.
- `Engine.closeLedger` swapped from `GetPendingTxs()` to
  `GetProposableTxs(e.prevLedger)`. The open-phase `anyTransactions`
  raw-count check stays unchanged — empty filtered set just produces
  an empty position, which is fine.

Verification path:
1. Rebuild goxrpl image, redeploy soak with all-5 UNL.
2. Grep `t=consensus-build event=our-position` on goxrpl-0 vs
   rippled-0 logs at the same `round_seq`. `tx_count` should now
   match. If not, position divergence is somewhere else (H2/H5).
3. Grep `event=round-summary` to confirm goxrpl's locally-built hash
   matches rippled's at each seq.

Cost: roughly one full apply (~3 passes through pending) per round at
close time. With ~50 txs/round in the test setup this is microseconds;
under heavy load this could become hot. The cleanup is a true
incremental OpenLedger that applies on tx arrival and only rebuilds
on LCL change — deferred until profiling shows it matters.

