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

Append findings here as the work progresses.
