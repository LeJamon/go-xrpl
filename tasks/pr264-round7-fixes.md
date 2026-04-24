# PR #264 Round-7 Fix Plan — External code-review remediation

**Branch:** `feature/p2p-todos`
**Input:** Independent external code review of the PR against `rippled` source, cross-verified task-by-task against the current worktree.
**Goal:** Address every merge-blocker and clear-scope divergence the review flagged, leaving only the genuinely large architectural items (manifests, per-tx dispute tracking, validation archive, TLS 1.2 session-sig) to dedicated follow-up plans.

---

## Scope

### In scope (this PR)

Five phases, ~19 concrete fixes, no new subsystems. Every fix either changes behavior on an existing code path or adds a localized enforcement check.

| Phase | Subsystem | Items | Severity |
|-------|-----------|-------|----------|
| 1     | F — ledger service adopt path | F1–F6 | HIGH (F1/F2 are the merge-blockers that undermine R5.1) |
| 2     | B — consensus adaptor wire format | B1, B2, B3, B4, B5, B6 | MEDIUM-HIGH (wire/suppression interop) |
| 3     | E — consensus engine | E1, E3, E6 | MEDIUM (E1 causes LedgerHash divergence on bin transitions) |
| 4     | G — reduce-relay hardening | G2, G3, G4, G5 | MEDIUM (G3 is a self-silencing DoS surface) |
| 5     | Cleanups | D5, C3 | LOW (mild + doc) |

### Out of scope (documented, deferred to dedicated plans)

| Item | Reason | Follow-up plan |
|------|--------|----------------|
| B7 / RPC manifest handler | Validator manifest / master-key infrastructure is multi-file, touches consensus + overlay + RPC + SLE + crypto. Needs its own spec. | `pr-manifests-round1.md` |
| E2 per-tx dispute tracking | `DisputeTracker` already exists and is unit-tested — wiring it into `Engine.updateOurPositions` is a substantial port of `Consensus.cpp::updateOurPositions`. Separate plan. | `pr-dispute-tracking.md` |
| E4 on-disk validation archive | DB schema change + `onStale`/`doStaleWrite` port. Separate plan. | `pr-validation-archive.md` |
| E5 full LedgerTrie `getPreferred` | Intentional simplification documented in-code. Port is self-contained and has its own failure modes. | `pr-ledgertrie.md` |
| C2 TLS 1.2 session-signature | Requires either a forked crypto/tls or a post-handshake protocol-level signature exchange. Documented in `handshake.go:101-111` as pre-existing. | `pr-handshake-session-sig.md` |
| C4 remaining handshake headers (Instance-Cookie, Server-Domain, Closed-Ledger, Previous-Ledger, Remote-IP, Local-IP) | Each is individually low-impact; batch them. | `pr-handshake-headers.md` |
| D6 SkipListAcquire + LedgerReplayTask | Catch-up speed only, not correctness. | `pr-multi-ledger-replay.md` |

---

## Phase 1 — Ledger service adopt path (F1–F6) [MERGE BLOCKER]

The two HIGH-severity findings (F1, F2) sit in one function: `Service.AdoptLedgerWithState` at `internal/ledger/service/service.go:1409-1451`. The review established that the PR's advertised R5.1 fix (thread the tx map through peer-adopted ledgers) only reaches in-memory `ledger.Ledger`, never the RelationalDB or the txPositionIndex. Tx / account_tx / tx_history / transaction_entry RPCs silently miss every peer-adopted ledger when a relational DB is configured. R5.1's pinning test (`adopt_with_state_test.go`) only asserts the in-memory TxMap hash, which is how the gap escaped review.

All six F-items touch the same function; fix them as one cohesive change.

**Files:**
- Modify: `internal/ledger/service/service.go:1409-1451` (`AdoptLedgerWithState`)
- Modify: `internal/ledger/service/service.go:1170-1204` (`SetValidatedLedger`)
- Modify: `internal/ledger/service/service.go` — add `tryAdvance` and `fixMismatch` helpers
- Reference: `rippled/src/xrpld/app/ledger/detail/LedgerMaster.cpp:804-864` (`setFullLedger`), `LedgerMaster.cpp:426-427, 694, 1008` (`tryAdvance` call-sites), `LedgerMaster.cpp:849-862` (fixMismatch)
- Test: `internal/ledger/service/adopt_with_state_test.go`

### Task 1.1 (F1 + F2) — Persist peer-adopted ledgers and populate tx-index

**Verified state:** The existing happy-path close at `service.go:580-593` calls `persistLedger` and `collectTransactionResults` (the latter populates `s.txIndex` + `s.txPositionIndex` via `service.go:706-722`). `AdoptLedgerWithState` ends at `return nil` (line 1450) without touching either. Rippled's `setFullLedger` at `LedgerMaster.cpp:831` calls `pendSaveValidated` and the tx-index is written as a consequence.

**Fix:** After `s.ledgerHistory[h.LedgerIndex] = adopted` at line 1434, and BEFORE creating the new open ledger on top:

```go
// Persist the adopted ledger exactly as the local close path does so
// tx/account_tx/tx_history/transaction_entry RPCs can answer queries
// against it. Matches LedgerMaster::setFullLedger -> pendSaveValidated.
if err := s.persistLedger(adopted); err != nil {
    // Degrade gracefully: the in-memory state is still correct and the
    // next consensus close will re-try persistence. Log loudly because
    // a persistent failure breaks tx RPCs silently.
    s.logger.Error("Failed to persist adopted ledger", "seq", h.LedgerIndex, "err", err)
}

// Populate the in-memory tx-index so tx-hash lookups resolve to this
// seq. collectTransactionResults walks the tx map and writes to
// s.txIndex + s.txPositionIndex as a side effect; we don't need the
// returned results here (no subscribers to dispatch to yet — that's
// Task 1.2).
_ = s.collectTransactionResults(adopted, h.LedgerIndex, h.Hash)
```

**Verification (new test):**
- `TestAdoptLedgerWithState_PersistsToRelationalDB` — configure an in-memory RelationalDB, adopt a ledger with 2 payment tx, assert `relationaldb.GetTransaction(hash)` returns both records.
- `TestAdoptLedgerWithState_PopulatesTxIndex` — adopt a ledger with 3 tx, assert `s.txIndex[hash]` == seq for each of the 3 hashes.
- Strengthen `adopt_with_state_test.go:62` (the existing `TxMap hash` pin) to also assert `len(s.txIndex) == N` after adoption.

### Task 1.2 (F3) — Fire `eventCallback` + `hooks.OnLedgerClosed` on adopt

**Verified state:** The close path at `service.go:604-655` collects `txResults` and fires both the new-style `hooks.OnLedgerClosed`/`OnTransaction` and the legacy `s.eventCallback` when the ledger transitions to closed+validated. `AdoptLedgerWithState` skips both. WebSocket `ledger` + `transactions` streams and the RPC subscription machinery see nothing for peer-adopted ledgers.

**Fix:** Extract the existing hook-fire block at `service.go:629-670` into a helper `fireLedgerClosedHooksLocked(l *ledger.Ledger, txResults []TransactionResultEvent, closeTime uint32)` and call it from both `CloseLedger` and `AdoptLedgerWithState`. Peer-adopted ledgers do not have a `closeTime` from local consensus — use `adopted.CloseTime()` (already populated from the header).

Key constraint: the legacy `eventCallback` is meant to fire on `validated`, not `closed`. Peer-adopted ledgers advance `closedLedger` but not `validatedLedger` (see the comment at `service.go:1431-1433`). So the adopt path:
- Fire `hooks.OnLedgerClosed` + `hooks.OnTransaction` immediately (matches real-time progress)
- Stash the `LedgerAcceptedEvent` in `pendingValidation` keyed by hash so `SetValidatedLedger` drains it when the ledger crosses quorum — this is the existing pattern at `service.go:1194-1203`

**Verification:**
- `TestAdoptLedgerWithState_FiresOnLedgerClosedHook` — install a mock `Hooks`, adopt, assert hook called exactly once with the expected `(info, txCount, validatedRange)`.
- `TestAdoptLedgerWithState_StashesLegacyEventUntilValidated` — install `eventCallback`, adopt + SetValidatedLedger, assert callback fires exactly once after SetValidatedLedger.

### Task 1.3 (F4) — Buffer late-arriving validations when seq not yet adopted

**Verified state:** `SetValidatedLedger` at `service.go:1180-1190` does `l, ok := s.ledgerHistory[seq]; if !ok { return }`. A trusted validation for seq N that races ahead of the peer-adoption of N is silently dropped with no retry queue. Windows are small in practice but nonzero when the validation tracker leads the adoption loop.

**Fix:** Introduce `s.pendingLedgerValidations map[uint32]pendingValidationEntry` where `pendingValidationEntry` stores `(expectedHash [32]byte, at time.Time)`. On `SetValidatedLedger`'s `!ok` branch, stash the (seq, expectedHash) pair with a TTL of 30s (tunable; rippled's equivalent window is the quorum gossip lag — a few seconds is enough). On every `AdoptLedgerWithState` / `CloseLedger` that inserts into `ledgerHistory`, check if `pendingLedgerValidations[seq]` is set; if so and the hash matches, promote the ledger to validated and fire the stashed event. Cap the map at 16 entries (same as the existing `pendingValidationMaxLen`) and LRU-drop.

```go
// On stash (inside SetValidatedLedger's !ok branch):
s.pendingLedgerValidations[seq] = pendingValidationEntry{
    expectedHash: expectedHash,
    at:           time.Now(),
}
if len(s.pendingLedgerValidations) > pendingValidationMaxLen {
    s.evictOldestPendingLedgerValidationLocked()
}

// On drain (at end of AdoptLedgerWithState and CloseLedger success):
if pending, ok := s.pendingLedgerValidations[seq]; ok {
    delete(s.pendingLedgerValidations, seq)
    if time.Since(pending.at) < pendingValidationTTL &&
        adopted.Hash() == pending.expectedHash {
        _ = adopted.SetValidated()
        s.validatedLedger = adopted
        // Drain any event the stashing path couldn't fire
        // (SetValidatedLedger only stashes in pendingValidation
        // when the ledger is already present — the adopt-first
        // path doesn't need a separate event).
    }
}
```

**Verification:**
- `TestSetValidatedLedger_StashesWhenSeqMissing_FiresOnAdopt` — call SetValidatedLedger(100, h), then AdoptLedgerWithState for seq=100 hash=h, assert validatedLedger == seq 100.
- `TestSetValidatedLedger_StashExpires` — stash with a fake clock, advance past TTL, adopt, assert validatedLedger NOT promoted.
- `TestSetValidatedLedger_StashHashMismatch` — stash hash A, adopt hash B at same seq, assert NOT promoted.

### Task 1.4 (F5) — `fixMismatch` equivalent for adopted-ledger parent-hash drift

**Verified state:** `service.go:1434` does `s.ledgerHistory[h.LedgerIndex] = adopted` unconditionally. If the slot already held a different ledger (e.g., we closed locally and the network adopted a different one), the old entry is blindly overwritten. Rippled's `setFullLedger` at `LedgerMaster.cpp:849-862` compares `prevLedger.hash()` to `ledger.parentHash()` and calls `fixMismatch` to invalidate the tail of the history when they diverge.

**Fix:** Add `func (s *Service) fixMismatchLocked(adopted *ledger.Ledger)` that:
1. If `prev := s.ledgerHistory[adopted.Sequence()-1]` exists and `prev.Hash() != adopted.ParentHash()`, walk backward from `adopted.Sequence()-1` deleting entries until we find a prefix whose forward chain is consistent with `adopted`.
2. Before the walk, log a WARN with all deleted seqs + hashes — a fixMismatch hit is rare and operationally significant.

Call `fixMismatchLocked(adopted)` from `AdoptLedgerWithState` BEFORE the `s.ledgerHistory[h.LedgerIndex] = adopted` assignment.

**Verification:**
- `TestAdoptLedgerWithState_FixMismatchInvalidatesDivergedTail` — seed ledgerHistory with a 3-ledger chain {A1,B2,C3}, adopt a ledger D3 whose parentHash is a hash not equal to B2.Hash(), assert B2 and C3 are purged from history and D3 is installed.
- `TestAdoptLedgerWithState_NoMismatchNoOp` — adopt a ledger whose parent exists with matching hash, assert history unchanged apart from the new seq.

### Task 1.5 (F6) — `tryAdvance` cascade after adoption

**Verified state:** `tasks/pr264-round5-fixes.md:426` already carries an internal TODO acknowledging this gap. Out-of-order replay-delta completions (seq N+2 arriving before seq N+1) cannot trigger follow-on adoption today — each ledger must be explicitly delivered by the inbound replay loop in order.

**Fix:** Add an orphan map `s.heldAdoptions map[uint32]*pendingAdopt` keyed by the awaited **parent** seq (i.e., `seq-1`). When `AdoptLedgerWithState` succeeds for seq N, look up `s.heldAdoptions[N]` and recurse (bounded to 256 levels to cap fork storms). Entries older than 60s are evicted on every adopt call. Entries whose parent hash doesn't match the just-adopted ledger's hash are dropped.

The orphan queue is deliberately flat (no multi-hop): replay-delta is single-ledger-per-request; multi-ledger backward walks are out of scope (D6).

**Verification:**
- `TestAdoptLedgerWithState_CascadesHeldOrphan` — submit seq=102 parent-of-101 as held, then adopt seq=101 whose hash matches the held parent reference, assert 102 is adopted in the same call.
- `TestAdoptLedgerWithState_OrphanMismatchDropped` — submit seq=102 parent=X, adopt seq=101 hash=Y (≠X), assert 102 is dropped and not re-adopted.

---

## Phase 2 — Consensus adaptor wire format (B1–B6)

### Task 2.1 (B1) — Gate sfCookie / sfServerVersion on `featureHardenedValidations`

**Files:**
- Modify: `internal/consensus/rcl/engine.go:1602-1698` (`sendValidation`)
- Modify: `internal/consensus/adaptor/stvalidation.go:308-319` (serializer gates)
- Reference: `rippled/src/xrpld/app/consensus/RCLConsensus.cpp:853-867`

**Verified state:** `rcl/engine.go:1610-1611, 1633-1634` always populates `Cookie` and `ServerVersion`. The serializer at `stvalidation.go:309, 317` emits whenever non-zero. Rippled only emits both under HardenedValidations, and ServerVersion additionally only on voting-ledgers. On any pre-HardenedValidations ruleset a goXRPL validation's signing preimage differs byte-for-byte from an equivalent rippled validation.

**Fix:** In `sendValidation`, replace the unconditional population at lines 1633-1634 with:

```go
if e.adaptor.IsFeatureEnabled("HardenedValidations") {
    validation.Cookie = cookie
    if isVotingLedger(ledger.Seq()) {
        validation.ServerVersion = serverVersion
    }
}
```

Remove the `if cookie == 0` / `if serverVersion == 0` warn-on-violation blocks at lines 1619-1624 — the R5.10 invariant no longer applies; zero is legitimate when HardenedValidations is off. Adjust the warning text + move it to the HardenedValidations branch so we warn when HV is on but values are still zero.

The serializer already short-circuits on zero, so no change there is strictly needed. Keep both gates (caller + serializer) — defense in depth.

**Verification:**
- `TestSendValidation_PreHardenedValidations_OmitsCookieAndServerVersion` — feature disabled, assert `validation.Cookie == 0` and `validation.ServerVersion == 0` on the emitted struct (serializer will omit).
- `TestSendValidation_HardenedValidations_NonVotingLedger_OmitsServerVersion` — feature enabled, seq=100 (non-voting), assert Cookie set, ServerVersion zero.
- `TestSendValidation_HardenedValidations_VotingLedger_EmitsBoth` — feature enabled, seq=255 (voting), assert both populated.

### Task 2.2 (B2) — Change suppression-hash domain to match rippled's `proposalUniqueId` / `sha512Half(val->serialized)`

**Files:**
- Modify: `internal/consensus/adaptor/router_dedup.go:17` (`hashPayload`)
- Modify: `internal/consensus/adaptor/router.go` — call sites that feed hashPayload (grep for `hashPayload(` — proposal + validation handlers)
- Reference: `rippled/src/xrpld/overlay/detail/PeerImp.cpp:1722-1728` (proposal), `PeerImp.cpp:2374` (validation)

**Verified state:** `hashPayload` takes the raw decompressed protobuf payload and sha512Half's it. Rippled:
- Proposal: `sha512Half(proposalUniqueId(proposeHash, prevLedger, proposeSeq, closeTime, pubkey, sig))` — a tuple of the *decoded* semantic fields, not the wire bytes.
- Validation: `sha512Half(val->serialized)` — the canonical STValidation blob *inside* the TMValidation, NOT the TMValidation protobuf envelope.

In practice: any semantically-identical message whose protobuf serializer reorders optional fields or includes/omits a default-valued field produces a different `hashPayload` in Go but the same rippled suppression key. Across mixed Go/rippled peers the dedup desynchronizes.

**Fix:** Replace `hashPayload` with two specialized hash functions:

```go
// hashProposalSuppression returns the suppression key for a TMProposeSet.
// Matches rippled proposalUniqueId: sha512Half(proposeHash || prevLedger
// || proposeSeq || closeTime || pubkey || sig).
func hashProposalSuppression(p *consensus.Proposal) [32]byte {
    h := common.NewSha512Half()
    h.Write(p.Position[:])
    h.Write(p.PreviousLedger[:])
    binary.Write(h, binary.BigEndian, p.ProposeSeq)
    binary.Write(h, binary.BigEndian, p.CloseTime.Unix())
    h.Write(p.NodePubKey)
    h.Write(p.Signature)
    return h.Sum()
}

// hashValidationSuppression returns the suppression key for a
// TMValidation. Matches rippled sha512Half(val->serialized) — the
// hash of the canonical STValidation blob, NOT the TMValidation
// protobuf envelope.
func hashValidationSuppression(serializedSTValidation []byte) [32]byte {
    return common.Sha512Half(serializedSTValidation)
}
```

Then update `handleProposal` / `handleValidation` in `router.go` to call the appropriate function with the *decoded* fields (proposal) or the inner serialized blob (validation) rather than `msg.Payload`.

**Verification:**
- `TestSuppression_ProposalSemanticIdentity_SameHash` — construct two protobuf payloads that round-trip to the same Proposal struct but differ byte-for-byte (e.g., optional `load_fee` omitted vs present-and-default); assert hashProposalSuppression returns the same value.
- `TestSuppression_ValidationInnerBlobDomain_SameHash` — same logic for validations.
- Cross-check against a captured rippled proposal/validation pair (fixture): assert hash matches rippled's expected value.

### Task 2.3 (B3) — Feed reduce-relay slot with full relay target set

**Files:**
- Modify: `internal/consensus/adaptor/router.go:313-315, 360-362` (`UpdateRelaySlot` call sites)
- Modify: `internal/peermanagement/relay.go` — extend `UpdateRelaySlot` signature to accept a set of peer IDs
- Reference: `rippled/src/xrpld/overlay/detail/PeerImp.cpp:3010-3054`

**Verified state:** `UpdateRelaySlot(nodeID, originPeer)` feeds one peer per received message. Rippled computes `haveMessage := overlay().relay(msg, suppressionKey)` and passes the whole set to `updateSlotAndSquelch` so peers that already claimed to have the message also count as "evidence of multi-path delivery" for selection purposes. Reduce-relay selection on goXRPL converges more slowly as a result.

**Fix:**
1. Change `UpdateRelaySlot` from `(validator NodeID, peer PeerID)` to `(validator NodeID, originPeer PeerID, seenPeers []PeerID)`.
2. In `router.go`, after observing a duplicate (the `firstSeen == false` branch of `messageSuppression.observe`), also enumerate the `peermanagement.Overlay.PeersThatHave(suppressionKey)` call — this is new; implement it as a reverse index from suppressionKey → peerIDs in `overlay.go`, populated during the relay-forward pass (where we already know which peers the message went to).
3. Pass that slice into `UpdateRelaySlot`. Slot.Update iterates the set and calls the counter increment per peer.

**Verification:**
- `TestRelay_DuplicateArrivalFeedsAllKnownRelayers` — seed the overlay with a "seen" entry mapping suppressionKey K → peers {A,B}, receive the same message from peer C; assert validator's slot has incremented counters for A, B, and C.
- `TestRelay_FirstSeenMessageDoesNotFeedSlot` — receive a message with no prior suppression entry; assert slot is not fed (matches rippled PeerImp.cpp:1730's `!added` branch).

### Task 2.4 (B4) — Detect `isBowOut` (seqLeave == 0xFFFFFFFF) and evict bowed-out validators

**Files:**
- Modify: `internal/consensus/rcl/engine.go:295-367` (`OnProposal`)
- Modify: `internal/consensus/rcl/engine.go` — add `deadNodes` field + eviction hook
- Reference: `rippled/src/xrpld/consensus/Consensus.h:804-817`, `rippled/src/xrpld/consensus/ConsensusProposal.h:68,154-156`

**Verified state:** No bowOut/seqLeave/deadNodes anywhere in goXRPL. A validator that bows out by emitting a position with `seqJoin == 0xFFFFFFFF` (rippled's `seqLeave` constant) has its final position persisted in the proposal map forever — it keeps "voting" on every subsequent round until the node restarts.

**Fix:** In `OnProposal`, after bounds checks and before storing:

```go
// isBowOut: rippled ConsensusProposal.h:154-156. A validator bowing
// out sets ProposeSeq to seqLeave (0xFFFFFFFF) on its final position
// so peers know to stop counting them.
const seqLeave = uint32(0xFFFFFFFF)
if proposal.ProposeSeq == seqLeave {
    e.peerProposals.Delete(proposal.NodeID)
    e.deadNodes[proposal.NodeID] = struct{}{}
    // Un-vote this node's contributions from any active disputes —
    // placeholder for when E2 (per-tx dispute tracking) lands.
    return
}
if _, dead := e.deadNodes[proposal.NodeID]; dead {
    // Defer any further proposals from this node until the next
    // consensus round clears deadNodes.
    return
}
```

Clear `e.deadNodes` in `Engine.startRound` alongside the existing position resets.

**Verification:**
- `TestOnProposal_BowOutEvictsNode` — feed a valid proposal from node X, then feed a seqLeave proposal from X; assert `peerProposals.Get(X)` is nil.
- `TestOnProposal_DeadNodeLaterProposalIgnored` — after bow-out, feed a normal proposal from X; assert it's not stored.
- `TestStartRound_ClearsDeadNodes` — after bow-out, call StartRound, feed a proposal; assert it IS accepted.

### Task 2.5 (B5) — Enforce monotonic signTime

**Files:**
- Modify: `internal/consensus/rcl/engine.go:1602-1640` (`sendValidation`)
- Reference: `rippled/src/xrpld/app/consensus/RCLConsensus.cpp:825-828`

**Verified state:** `SignTime = e.adaptor.Now()` is set directly. If the adaptor clock regresses (NTP step, leap-second correction, VM pause/resume) the emitted signTime can be older than the previous validation from the same node, causing peers to treat it as stale (`rcl/validations.go:184`).

**Fix:** Add `lastSignTime` field to `Engine`:

```go
type Engine struct {
    // ... existing fields ...
    lastSignTime time.Time // monotonic floor for sendValidation
}

// Inside sendValidation, after computing signTime:
signTime := e.adaptor.Now()
if !e.lastSignTime.IsZero() && !signTime.After(e.lastSignTime) {
    // Clock regressed — step forward 1s from the prior emission to
    // preserve monotonicity. Matches RCLConsensus.cpp:826.
    signTime = e.lastSignTime.Add(1 * time.Second)
}
e.lastSignTime = signTime
validation.SignTime = signTime
validation.SeenTime = signTime // (was also Now()); keep them equal as today
```

**Verification:**
- `TestSendValidation_ClockRegressionPreservesMonotonic` — use a fake clock, call sendValidation, step clock backward 5s, call again; assert the second SignTime == first + 1s.
- `TestSendValidation_ClockMonotonic_NormalCase` — fake clock forward 3s between two calls, assert SignTime difference is 3s (no artificial step).

### Task 2.6 (B6) — Explicitly reject non-secp256k1 (ed25519 / 0xED) pubkey on TMProposeSet

**Files:**
- Modify: `internal/consensus/adaptor/router.go:370-387` (`validateProposeBounds`)
- Reference: `rippled/src/xrpld/overlay/detail/PeerImp.cpp:1679-1680`

**Verified state:** The length check at line 383 passes both 33-byte secp256k1 compressed points AND 33-byte ed25519 points (0xED prefix + 32-byte key). VerifyProposal may still reject, but the malformed-bounds fee-charge path currently lets the peer slip through without attribution.

**Fix:** Replace the length-only check with a KeyType check:

```go
// Proposal pubkeys must be compressed secp256k1 (0x02/0x03 prefix).
// ed25519 validators (0xED prefix) are not allowed in propose-set
// per rippled PeerImp.cpp:1679 (publicKeyType != KeyType::secp256k1).
if len(p.NodePubKey) != 33 {
    return "pubkey-size", false
}
if p.NodePubKey[0] != 0x02 && p.NodePubKey[0] != 0x03 {
    return "pubkey-type", false
}
```

**Verification:**
- `TestValidateProposeBounds_RejectsEd25519Prefix` — construct a Proposal with NodePubKey = 0xED || 32 bytes; assert `("pubkey-type", false)`.
- `TestValidateProposeBounds_AcceptsSecp256k1Prefix` — 0x02/0x03 prefix + 32 bytes; assert `("", true)`.
- `TestHandleProposal_Ed25519PubKeyChargesPeer` — end-to-end: feed a TMProposeSet with ed25519 pubkey, assert `IncPeerBadData` was called with `"propose-pubkey-type"`.

---

## Phase 3 — Consensus engine (E1, E3, E6)

### Task 3.1 (E1) — Dynamic `getNextLedgerTimeResolution`

**Files:**
- Modify: `internal/ledger/ledger.go:147` (static `CloseTimeResolution: parent.header.CloseTimeResolution`)
- Add: `internal/consensus/ledger_timing.go` — new file hosting `getNextLedgerTimeResolution`
- Reference: `rippled/src/xrpld/consensus/LedgerTiming.h:80-122`

**Verified state:** goXRPL inherits the parent's resolution unchanged on every close. Rippled steps the resolution up/down based on `ledgerSeq % decreaseLedgerTimeResolutionEvery` and whether `previousAgree` was true. At bin transitions (every N ledgers) the resolution changes; goXRPL peers compute a different CloseTime (truncated to a different resolution) and the resulting LedgerHash diverges from mainnet/testnet rippled peers.

**Fix:** Port `getNextLedgerTimeResolution`:

```go
// ledger_timing.go
package consensus

import "time"

// Possible resolutions in seconds, ordered coarsest first. Matches
// rippled LedgerTiming.h:35 ledgerPossibleTimeResolutions.
var possibleTimeResolutions = []uint32{10, 20, 30, 60, 90, 120}

const (
    // Every N seqs, try to step the resolution in the direction
    // chosen by whether the previous round agreed.
    increaseLedgerTimeResolutionEvery = 8
    decreaseLedgerTimeResolutionEvery = 1
)

// GetNextLedgerTimeResolution returns the close-time resolution to
// use for (parent.LedgerSeq+1) given whether the previous round
// agreed. Matches rippled LedgerTiming.h:80-122.
func GetNextLedgerTimeResolution(parentRes uint32, previousAgree bool, newLedgerSeq uint32) uint32 {
    idx := 0
    for i, r := range possibleTimeResolutions {
        if r == parentRes {
            idx = i
            break
        }
    }
    if !previousAgree && newLedgerSeq%decreaseLedgerTimeResolutionEvery == 0 {
        if idx+1 < len(possibleTimeResolutions) {
            idx++ // coarser (slower convergence tolerated)
        }
    }
    if previousAgree && newLedgerSeq%increaseLedgerTimeResolutionEvery == 0 {
        if idx > 0 {
            idx-- // finer (tighter convergence required)
        }
    }
    return possibleTimeResolutions[idx]
}
```

Then at `ledger.go:147`, pass `previousAgree` through from the consensus adaptor (it's tracked in the engine) and call `GetNextLedgerTimeResolution(parent.CloseTimeResolution, previousAgree, newLedgerSeq)`.

**Verification (cross-reference tests):**
- `TestGetNextLedgerTimeResolution_ParityTable` — parameterize against the exact same (parentRes, previousAgree, newSeq) tuples rippled unit tests use; assert identical output.
- `TestLedger_Close_UsesDynamicResolution` — build a parent at res=30 with previousAgree=true and newSeq=8; assert child's CloseTimeResolution is 20.

### Task 3.2 (E3) — Add `LedgerABANDON_CONSENSUS` hard timeout

**Files:**
- Modify: `internal/consensus/types.go:351` (constants)
- Modify: `internal/consensus/rcl/engine.go:1095-1103` (consensus phase timeout check)
- Reference: `rippled/src/xrpld/consensus/ConsensusParms.h:95,105,113`, `rippled/src/xrpld/consensus/Consensus.cpp:254-258`

**Verified state:** Only hard timeout is `LedgerMaxClose = 10s`. Rippled has BOTH `ledgerMAX_CONSENSUS=15s` (soft) and `ledgerABANDON_CONSENSUS=120s` (hard, with `ledgerABANDON_CONSENSUS_FACTOR=10` multiplier). goXRPL drops out of establish 5s earlier than rippled's 15s-soft and doesn't have a 120s abandon safety net at all.

**Fix:**
1. In `types.go:351`, add:
   ```go
   LedgerMaxConsensus       = 15 * time.Second  // was LedgerMaxClose=10s; keep LedgerMaxClose as the old alias for now
   LedgerAbandonConsensus   = 120 * time.Second
   LedgerAbandonConsensusFactor = 10
   ```
   Keep `LedgerMaxClose` as an alias but migrate its call-sites to `LedgerMaxConsensus`.
2. In the consensus phase timer in `rcl/engine.go`, add a second branch: if `now - phaseStart > LedgerAbandonConsensus` or `> LedgerMaxConsensus * LedgerAbandonConsensusFactor`, mark the consensus as abandoned — the engine transitions to a new round with an empty set instead of attempting to close on stale state.

**Verification:**
- `TestConsensus_MaxConsensusSoftTimeoutTransitions` — simulate time past LedgerMaxConsensus (15s), assert phase transitions (no hard abort).
- `TestConsensus_AbandonHardTimeout` — simulate 121s, assert abandonment state entered.

### Task 3.3 (E6) — Rename `MinConsensusPct` / `MaxConsensusPct` to match rippled terminology

**Files:**
- Modify: `internal/consensus/types.go:375-377`
- Modify: call-sites in `internal/consensus/rcl/engine.go:1153, 1170`
- Reference: `rippled/src/xrpld/consensus/ConsensusParms.h:79`

**Verified state:** Rippled has a single `minCONSENSUS_PCT = 80`. goXRPL has two constants:
- `MinConsensusPct = 50` (early convergence gate)
- `MaxConsensusPct = 80` (accept gate)

The review calls this "inverted" — strictly, it's "renamed plus an extra lower threshold". The 80% accept gate is arithmetically identical to rippled.

**Fix:** Rename to clarify the two-threshold nature without implying an inversion relative to rippled:
- `MinConsensusPct` → `EarlyConvergencePct` (the 50% threshold)
- `MaxConsensusPct` → `MinConsensusPct` (the 80% threshold, matching rippled's `minCONSENSUS_PCT`)

Update both call sites. This is a mechanical rename; the arithmetic is unchanged.

**Verification:** Existing consensus tests must continue to pass. Add a doc comment at the constant declarations citing rippled's `minCONSENSUS_PCT = 80` so the relationship is explicit.

---

## Phase 4 — Reduce-relay hardening (G2–G5)

### Task 4.1 (G2) — Periodic `deleteIdlePeers` sweep

**Files:**
- Modify: `internal/peermanagement/relay.go` — add ticker + sweep method
- Modify: `internal/peermanagement/overlay.go` — start the ticker in `Overlay.Start`
- Reference: `rippled/src/xrpld/overlay/Slot.h:264-283`, `rippled/src/xrpld/overlay/detail/OverlayImpl.cpp:1472-1479`

**Verified state:** `r.slots` only shrinks on explicit `RemovePeer` (disconnect). Selected peers that stop relaying are never demoted back to Counting; slots accumulate entries for validators we no longer see.

**Fix:** Add `func (r *Relay) deleteIdlePeers(now time.Time)` that iterates `r.slots`, and inside each slot:
- Walks peers, removes any with `now - lastMessage > Idled` (8s, existing constant `reduce_relay_common.go`).
- If the slot transitions to fewer peers than `MaxSelectedPeers` and was in Selected state, demote to Counting.
- After the walk, if the slot has zero peers, delete it from `r.slots`.

Wire a `time.Ticker` at `Idled/2` cadence (4s) in `Overlay.Start` that calls `r.deleteIdlePeers(time.Now())`. Stop the ticker in `Overlay.Stop`.

**Verification:**
- `TestRelay_DeleteIdlePeers_EvictsStaleEntries` — add a peer, advance fake clock past Idled, call deleteIdlePeers, assert peer removed.
- `TestRelay_DeleteIdlePeers_DemotesSelectedBelowQuorum` — add 5 selected peers, idle 3 of them out, call deleteIdlePeers, assert slot state transitioned from Selected to Counting.

### Task 4.2 (G3) — Filter inbound TMSquelch targeting our own validator pubkey

**Files:**
- Modify: `internal/peermanagement/overlay.go:820-857` (`handleSquelchMessage`)
- Reference: `rippled/src/xrpld/overlay/detail/PeerImp.cpp:2715-2721`

**Verified state:** No comparison between `sq.ValidatorPubKey` and the local validator pubkey. A peer can send us a TMSquelch targeting our own validator pubkey, and we'll obediently stop relaying our own validator's proposals/validations — a self-silencing DoS surface.

**Fix:** Right after the 33-byte length check at line 835, add:

```go
// Rippled PeerImp.cpp:2715-2721 drops any inbound squelch whose
// target pubkey is our own validator — otherwise a peer could
// silence our own traffic on our RelayFromValidator path.
if ownPubKey := o.localValidatorPubKey(); len(ownPubKey) == 33 && bytes.Equal(sq.ValidatorPubKey, ownPubKey) {
    slog.Debug("Squelch dropped: targets local validator",
        "t", "Overlay", "peer", evt.PeerID)
    o.IncPeerBadData(evt.PeerID, "squelch-targets-self")
    return
}
```

Add `localValidatorPubKey()` as a passthrough to the existing validator identity (already threaded through the adaptor for signing).

**Verification:**
- `TestHandleSquelchMessage_DropsSelfTargetingSquelch` — configure local validator pubkey P, feed a TMSquelch with ValidatorPubKey=P; assert peer charged with `"squelch-targets-self"` and AddSquelch NOT called.
- `TestHandleSquelchMessage_AllowsOtherValidatorSquelch` — same test but ValidatorPubKey=Q (≠P); assert AddSquelch called.

### Task 4.3 (G4) — Charge peers that keep relaying after being squelched

**Files:**
- Modify: `internal/peermanagement/relay.go:83-130` (`ValidatorSlot.Update` — extend signature to accept a charge callback)
- Modify: `internal/peermanagement/overlay.go` — wire the callback to `IncPeerBadData(peer, "squelch-ignored")`
- Reference: `rippled/src/xrpld/overlay/Slot.h:113,158,291,329-331`

**Verified state:** `ValidatorSlot.Update` returns silently at `relay.go:110` when the peer's state is `RelayPeerSquelched`. Rippled invokes an `ignored_squelch_callback` at `Slot.h:329-331` that wires into `feeUselessData`-style reputation charges — a peer that ignores our squelch bleeds reputation until it's disconnected.

**Fix:**
1. Extend `ValidatorSlot.Update` to accept `ignoredCallback func(peerID PeerID)`.
2. At the `peer.State == RelayPeerSquelched` branch (relay.go:110), call `ignoredCallback(peerID)` before returning.
3. In `Relay.OnMessage` / its callers in `overlay.go`, pass a closure that calls `o.IncPeerBadData(peer, "squelch-ignored")`.

**Verification:**
- `TestRelay_SquelchedPeerRelayChargesPeer` — squelch peer P, feed a validator message from P; assert `IncPeerBadData(P, "squelch-ignored")` called exactly once.
- `TestRelay_UnsquelchedPeerRelayDoesNotCharge` — baseline: unsquelched peer relaying does not trigger the charge.

### Task 4.4 (G5) — Change `EnableReduceRelay` default to `false`

**Files:**
- Modify: `internal/peermanagement/config.go:99,273-275` (default + cascade)
- Reference: `rippled/src/xrpld/core/Config.h:248`, `rippled/src/xrpld/core/detail/Config.cpp:758-762`

**Verified state:** `config.go:99` sets `EnableReduceRelay: true` in DefaultConfig, cascading to `EnableVPReduceRelay=true` + `EnableTxReduceRelay=true`. Rippled defaults all three to `false`. Stock goXRPL on a stock rippled network will advertise vprr+txrr and engage slot squelching aggressively, diverging from default peer behavior.

**Fix:**
1. Flip the three defaults in `DefaultConfig` to `false`.
2. Update any integration / E2E test that depended on the old default — search with `grep -r "EnableReduceRelay" internal/` and explicitly set `true` where the test is meant to exercise reduce-relay.
3. Update the CLI / config-file documentation to note that reduce-relay is opt-in.

**Verification:**
- `TestDefaultConfig_ReduceRelayOptIn` — construct DefaultConfig(), assert all three fields are false.
- Grep for existing tests that silently relied on the default and confirm they either explicitly enable or don't care about reduce-relay behavior.

---

## Phase 5 — Cleanups (D5, C3)

### Task 5.1 (D5) — Install peer-supplied leaf only on `applied==true`

**Files:**
- Modify: `internal/ledger/inbound/replay_delta.go:627-676` (apply switch)
- Reference: `rippled/src/xrpld/app/tx/detail/Transactor.cpp:1108,1215-1267`, `rippled/src/xrpld/app/ledger/detail/BuildLedger.cpp:246`

**Verified state:** The review's mild-accidental finding: the switch at lines 627-676 installs the peer-supplied tx+meta leaf on tes / tec / terRETRY / tef / tem / tel alike. Rippled only `rawTxInsert`s when `applied == true` (i.e., tes or tec). On tef/tem/tel/ter, rippled silently drops the tx from the view's txs_; goXRPL preserves it to keep TxHash == header.TxHash. The AccountHash invariant check at line 708 is a safety net.

This is a genuine divergence but catches on the AccountHash check in practice. Tightening it removes the safety-net dependency.

**Fix:** Restructure the switch so only tes/tec install the peer leaf from the engine path. For tef/tem/tel/ter, compare the peer-supplied leaf hash to what the engine would have produced (empty); if they differ, log a WARN and fail the whole replay so the AccountHash check's safety-net role shrinks to AccountHash-only.

The honest option: if the engine disagrees with the peer on whether a tx applies at all, the AccountHash will diverge anyway — we gain nothing by preserving the tx leaf. Drop it.

**Verification:**
- `TestReplay_TefTxDoesNotInstallPeerLeaf` — construct a replay scenario where the peer's tx map has a tx the engine rejects with tef; assert replay fails loudly (hash mismatch logged) rather than silently succeeding via the peer-leaf path.
- Confirm existing replay success tests still pass (tes / tec unchanged).

### Task 5.2 (C3) — Fix round-2 commit message / TMSquelch gating comment

**Files:**
- Modify: `internal/peermanagement/overlay.go:636-644` (comment block)
- Optional: Amend commit `1bf57e3` message via a new revert-and-re-commit OR add a clarifying commit on top

**Verified state:** Commit `1bf57e3` message says "Inbound TMSquelch gated on vpReduceRelay; unnegotiated peers charged and dropped". The code at `overlay.go:645-654` accepts unconditionally; the comment above even acknowledges the contradiction with the commit.

**Fix:** Reconcile code-with-intent. The right answer is the unconditional-accept one (rippled accepts unconditionally too), so the commit message is what's wrong:
1. Rewrite the comment block at lines 636-644 to state plainly: "Inbound TMSquelch is accepted unconditionally. Rippled PeerImp.cpp:2684-2721 does the same — there is no per-peer gating on vpReduceRelay for incoming squelches. The round-2 commit message was inaccurate and this comment documents the correct behavior."
2. In the PR description, add a clarification noting the commit message inaccuracy.

No tests needed; this is a comment-only change.

---

## Self-review

**Spec coverage:**
- F1–F6 → Phase 1 (five tasks)
- B1, B2, B3, B4, B5, B6 → Phase 2 (six tasks)
- E1, E3, E6 → Phase 3 (three tasks)
- G2, G3, G4, G5 → Phase 4 (four tasks)
- D5, C3 → Phase 5 (two tasks)
- B7, E2, E4, E5, C2, C4 (partial), D6 → explicitly deferred, listed in Scope with follow-up plan references

**Placeholders:** None. Every fix includes concrete code or concrete enumeration of what changes where.

**Type consistency:** Renames in 3.3 (MinConsensusPct → EarlyConvergencePct, MaxConsensusPct → MinConsensusPct) must be applied simultaneously in all three locations listed. The `pendingValidationEntry` introduced in 1.3 is only used in 1.3. `deadNodes` introduced in 2.4 is only used in 2.4. `hashProposalSuppression` / `hashValidationSuppression` in 2.2 replace a single `hashPayload` callsite.

**Execution order:** Phase 1 is the merge-blocker and should land first. Phases 2–4 are independent of each other and can interleave. Phase 5 can land alongside any of them. Within Phase 1, Task 1.1 (persist + tx-index) is the critical one; 1.2–1.5 can batch into a second commit.

---

## Commit strategy

Per `goXRPL/CLAUDE.md`: "Never mention yourself when committing."

Proposed commit series (one per task, to match the existing PR-audit cadence):

1. `feat(ledger/service): persist peer-adopted ledgers and populate tx index (F1,F2)`
2. `feat(ledger/service): fire OnLedgerClosed hooks on peer adoption (F3)`
3. `feat(ledger/service): buffer late validations until adopt (F4)`
4. `feat(ledger/service): invalidate history tail on adopt parent-hash mismatch (F5)`
5. `feat(ledger/service): cascade held adoptions after parent arrives (F6)`
6. `fix(consensus/adaptor): gate sfCookie/sfServerVersion on HardenedValidations (B1)`
7. `fix(consensus/adaptor): hash suppression keys in rippled's domain (B2)`
8. `feat(consensus/adaptor): feed relay slot with full seen-peer set (B3)`
9. `feat(consensus/rcl): evict bowed-out validators (B4)`
10. `fix(consensus/rcl): enforce monotonic signTime (B5)`
11. `fix(consensus/adaptor): reject ed25519 pubkey on proposals (B6)`
12. `feat(consensus): dynamic getNextLedgerTimeResolution (E1)`
13. `feat(consensus): add ledgerABANDON_CONSENSUS hard timeout (E3)`
14. `refactor(consensus): rename consensus-pct constants to match rippled (E6)`
15. `feat(peermanagement/relay): periodic deleteIdlePeers sweep (G2)`
16. `fix(peermanagement/overlay): drop squelches targeting our own validator (G3)`
17. `feat(peermanagement/relay): charge peers that ignore our squelch (G4)`
18. `fix(peermanagement/config): default reduce-relay off to match rippled (G5)`
19. `fix(ledger/inbound): install peer leaf only on applied==true (D5)`
20. `docs(peermanagement): reconcile TMSquelch gating comment with code (C3)`
