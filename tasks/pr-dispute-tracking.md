# Issue #266 — E2: per-tx dispute tracking through consensus convergence

**Branch:** `feature/dispute-tracking-266` off `feature/p2p-todos`
**Scope:** Wire per-peer dispute votes into the consensus engine; replace whole-set popularity voting with per-tx re-voting ramping through 50/65/70/95% avalanche thresholds; implement `unVote` for bow-out.

**Rippled references:**
- `rippled/src/xrpld/consensus/DisputedTx.h` — per-peer Map_t votes_, avalanche state, updateVote
- `rippled/src/xrpld/consensus/ConsensusParms.h` — avalanche cutoffs (init/mid/late/stuck)
- `rippled/src/xrpld/consensus/Consensus.h:createDisputes(1821), updateDisputes(1892)` — wiring into peerProposal/gotTxSet/closeLedger
- `rippled/src/xrpld/consensus/Consensus.h:updateOurPositions(1492), haveConsensus(1682)` — per-tx vote migration + accept gate
- `rippled/src/xrpld/consensus/Consensus.h:804-817` — bow-out → unVote on every dispute

---

## Phase 1 — Data model

### 1.1 Extend `consensus.DisputedTx` (`internal/consensus/types.go:259`)

Add per-peer vote tracking + per-dispute avalanche state:
```go
type AvalancheState int
const (
    AvalancheInit AvalancheState = iota   // 50%
    AvalancheMid                           // 65%
    AvalancheLate                          // 70%
    AvalancheStuck                         // 95%
)

type DisputedTx struct {
    TxID TxID
    Tx   []byte
    OurVote bool
    Yays int
    Nays int
    // Per-peer votes. Yays/Nays are cached counts derived from this map.
    Votes map[NodeID]bool
    // Avalanche state for the per-tx threshold ramp.
    AvalancheState     AvalancheState
    AvalancheCounter   int // rounds at current avalanche state
    CurrentVoteCounter int // rounds since we last changed our vote
}
```

### 1.2 Add `ConsensusParms` type in `consensus/types.go`

Mirror rippled's avalanche cutoff table + avMIN_ROUNDS / avSTALLED_ROUNDS. Export `GetNeededWeight(state, percentTime, rounds, minRounds) -> (pct int, nextState *AvalancheState)` helper matching rippled's `getNeededWeight`.

Integrate with the existing `Thresholds` (don't duplicate `MinConsensusPct = 80`).

---

## Phase 2 — DisputeTracker rework (`internal/consensus/rcl/proposals.go`)

Replace the naïve Yays/Nays-only tracker with the rippled-style one.

### 2.1 `CreateDispute(txID, tx, ourVote, numPeers)` — preserve backward signature (drop `numPeers` arg unused today, or add it as an optional hint).

### 2.2 `SetVote(txID, peerID, yes bool) bool`
- Insert or update peer's vote.
- On insert, increment Yays or Nays.
- On change, swap one count.
- Returns true iff a vote was inserted or changed (caller uses this to reset `peerUnchangedCounter`).

### 2.3 `UnVote(peerID NodeID)`
- Iterate every dispute.
- If peer had a vote there, decrement the count and remove the entry.
- No-op on disputes where peer has no vote.

### 2.4 `UpdateDisputes(peerID, peerTxSet TxSet) (anyChanged bool)`
- For every existing dispute, call `SetVote(txID, peerID, peerTxSet.Contains(txID))`.
- Tracks whether any vote changed.

### 2.5 `UpdateOurVote(percentTime int, proposing bool, parms ConsensusParms) (changedTxs []TxID)`
- For every dispute, run `updateVote` logic matching `DisputedTx.h:updateVote`:
  - Short-circuit if we already align with the peer tally (yours=yes && nays==0 etc.).
  - Call `GetNeededWeight` to advance avalanche state.
  - If proposing: `weight = (yays*100 + (ourVote?100:0)) / (yays + nays + 1)`; newPosition = weight > requiredPct.
  - Else: newPosition = yays > nays (read-only observer).
  - If newPosition differs from ourVote: flip ourVote, reset CurrentVoteCounter, swap one count, append to changedTxs.
  - Else: bump CurrentVoteCounter.

### 2.6 Deprecate `AddVote` and the old `Resolve`
Keep them for now as convenience wrappers + update proposals_test.go to use new API (per-peer). Do NOT leave dead code in final PR; delete old Resolve.

---

## Phase 3 — Engine wiring (`internal/consensus/rcl/engine.go`)

### 3.1 Engine state changes
- Replace `disputes map[TxID]*DisputedTx` field with `disputeTracker *DisputeTracker`.
- Add `acquiredTxSets map[TxSetID]TxSet` — matches rippled's `acquired_`. Populated from both our own `BuildTxSet` and successful `OnTxSet`/`GetTxSet` lookups.
- Add `peerUnchangedCounter int` — rounds since any peer flipped a vote (for stall detection).
- Add `establishCounter int` — rounds spent in establish phase (for avalanche minMaxRounds check).

### 3.2 `startRoundLocked` — reset
Reset disputeTracker, acquiredTxSets, peerUnchangedCounter, establishCounter on round start.

### 3.3 `closeLedger` — seed disputes
After `ourTxSet` is built and proposal broadcast, iterate current peer positions:
- For each peer whose proposal has an acquired tx set different from ours:
  - `createDisputes(peerTxSet)` → builds DisputedTx for every tx in symmetric difference; seeds ourVote = `ourTxSet.Contains(txID)`; calls SetVote for every peer whose acquired set is known.

Helper `createDisputesLocked(peerTxSet TxSet)` mirrors rippled's `createDisputes(o)` — the `compares_` set to avoid re-diffing the same tx set.

Add `comparesTxSets map[TxSetID]struct{}` to engine state to dedupe.

### 3.4 `OnProposal` — update disputes when position is known
After storing the updated position:
- If `peerTxSet := e.acquiredTxSets[proposal.TxSet]; peerTxSet != nil`:
  - `createDisputesLocked(peerTxSet)`
  - `disputeTracker.UpdateDisputes(proposal.NodeID, peerTxSet)`

### 3.5 `OnTxSet` — back-fill disputes when tx set arrives late
When a tx set is acquired:
- Store in `acquiredTxSets`.
- For every peer whose stored proposal position == this tx set ID: `createDisputesLocked + UpdateDisputes`.

### 3.6 Bow-out arm — replace TODO(E2) with `disputeTracker.UnVote(peerID)` (`engine.go:372-377`).

---

## Phase 4 — Convergence rewrite

### 4.1 Add `updateOurPositions` method
Matches rippled's `updateOurPositions(1492-1678)` for the TX side (close-time logic stays in `updateCloseTimePosition`):

1. `convergePercent_` — ratio of current-round duration to previous round, clamped by `avMIN_CONSENSUS_TIME=5s`. Already computed by `convergePercent()`.
2. Stale proposal pruning: iterate `e.proposals`; drop stale ones; `disputeTracker.UnVote(peerID)` for each dropped peer.
3. `changedTxs := disputeTracker.UpdateOurVote(convergePercent, proposing, parms)`.
4. If non-empty, rebuild `ourTxSet` from old set ± changedTxs (mutate through adaptor `BuildTxSet` with the adjusted blob list), bump position counter, re-sign, broadcast.
5. If we updated our position and are proposing: `disputeTracker.UpdateDisputes(selfNodeID, ourTxSet)` and also call `UpdateDisputes` for every peer whose acquired set matches the new position.

### 4.2 Rewrite `checkConvergence` (`engine.go:1132-1184`)
Remove the whole-set popularity block. New logic:

```
if now - phaseStart <= LedgerMinConsensus: return
updateOurPositions(parms)   // may change our position
updateCloseTimePosition()
if !haveConsensus(): return
if !haveCloseTimeConsensus: return
acceptLedger(ResultSuccess)
```

### 4.3 Add `haveConsensus` helper
Matches rippled's `haveConsensus(1682)`:
- `agree := count of peers whose proposal.TxSet == ourPosition.TxSet` (+ self if proposing)
- `disagree := count of peers whose proposal.TxSet != ourPosition.TxSet`
- accept iff `agree * 100 >= (agree + disagree) * MinConsensusPct`

Also handle stall detection (all disputes `stalled()`) for future expiration — but E3 already added the hard-timeout so we can defer stall fallback. Document as out-of-scope for #266.

### 4.4 Increment `establishCounter` / `peerUnchangedCounter` on each `phaseEstablish` tick.

---

## Phase 5 — Tests (internal/consensus/rcl/ — new test file `disputes_test.go`)

### 5.1 `TestConsensus_OverlappingDisjointProposals_Converges`
- 5 trusted validators (our node + 4 peers).
- Configure 2 peers to propose {A,B,C}, 2 peers to propose {A,B,D}; our initial set = {A,B}.
- Drive rounds: each `phaseEstablish` tick, advance `adaptor.now` past LedgerMinConsensus; iterate.
- Assert: eventually ourTxSet = {A,B,C,D} (both in after threshold ramps), engine reaches `PhaseAccepted`, `disputeTracker` created disputes for C and D.

### 5.2 `TestConsensus_BowOut_UnVotesDisputes`
- Set up 3 peers; create dispute on tx C; peer X votes yes (via an acquired peer tx set containing C).
- Assert Yays == 1 (plus maybe ourVote yes).
- Send a bow-out proposal (Position = seqLeave) from X.
- Assert: X no longer in `e.proposals`, X in `e.deadNodes`, dispute C's Yay count decremented, X not in `dispute.Votes`.

### 5.3 `TestConsensus_AvalancheThresholdRamp`
- Create a single dispute with a known split vote (e.g. 2 yay / 2 nay out of 5 participants).
- Call `UpdateOurVote` repeatedly with increasing `percentTime`:
  - percentTime=0, 2 rounds → still init (50%)
  - percentTime=60 → advances to mid (65%)
  - percentTime=90 → advances to late (70%)
  - percentTime=210 → advances to stuck (95%)
- Assert dispute's avalanche state field and the threshold the next updateVote call requires.

### 5.4 Update `proposals_test.go` for the new API
- Replace `AddVote(id, bool)` calls with `SetVote(id, peerID, bool)`.
- Update `Resolve` test OR delete Resolve entirely (rippled has no equivalent API; the engine now drives inclusion via `UpdateOurVote`).

---

## Phase 6 — Acceptance verification
- `go build ./...`
- `go test ./internal/consensus/...` — all existing tests plus the three new ones pass
- Confirm `TestOnProposal_BowOutEvictsNode` still passes with the B4→E2 handoff in place

## Out of scope (explicitly defer)
- Stall-based ResultExpired return (already covered by E3 ledgerABANDON_CONSENSUS).
- Full `adaptor_.share(tx)` rebroadcast on new disputes — goXRPL's gossip layer doesn't have per-tx share hooks yet; the tx is already gossiped via the normal mempool.
- Peer UnchangedCounter decay across rounds — rippled resets it per-round; we do too.

---

## Review — completed 2026-04-24

### Files touched

- `internal/consensus/types.go` — `DisputedTx` extended with `Votes map[NodeID]bool` + per-dispute avalanche state fields; new `AvalancheState` enum; new `ConsensusParms` + `DefaultConsensusParms()` + `NeededWeight()` helper mirroring rippled's `ConsensusParms::getNeededWeight`.
- `internal/consensus/engine.go` — `TxSet` interface gained `TxIDs() []TxID` (documented to zip with `Txs()`).
- `internal/consensus/adaptor/txset.go` — `TxSetImpl.TxIDs()` returns IDs in the same order as `Txs()`.
- `internal/consensus/rcl/proposals.go` — `DisputeTracker` reworked: `CreateDispute` now seeds an empty peer-vote map; new `SetVote(txID, peerID, yes) bool`, `UnVote(peerID)`, `UpdateDisputes(peerID, peerTxSet)`, `UpdateOurVote(percentTime, proposing, parms) []TxID`. Old `AddVote`, `Resolve`, and legacy `UpdateOurVote(txID, include)` were removed — they assumed Yays/Nays counted ourVote, which rippled does not do.
- `internal/consensus/rcl/engine.go` — Engine now owns a `disputeTracker`, `acquiredTxSets`, `comparesTxSets`, `parms`, `peerUnchangedCounter`, `establishCounter`. New helpers: `createDisputesAgainst`, `seedDisputeVotes`, `countAgreement`. `updatePosition` rewritten from whole-set popularity to per-tx dispute re-vote + rebuild. `checkConvergence` rewritten to use `countAgreement` (rippled's `haveConsensus`). `OnProposal` / `OnTxSet` / `closeLedger` wire dispute create + update. Bow-out arm now calls `disputeTracker.UnVote(peerID)` — the issue's TODO(E2) pointer is resolved. `handleWrongLedger` resets the new tracker fields alongside the old ones.
- `internal/consensus/rcl/proposals_test.go` — Replaced `TestDisputeTracker_CreateAndVote`, `TestDisputeTracker_Resolve`, `TestDisputeTracker_UpdateOurVote` with four tests covering the new API: `TestDisputeTracker_CreateAndVote`, `TestDisputeTracker_UnVote`, `TestDisputeTracker_UpdateDisputes`, `TestDisputeTracker_UpdateOurVote_AvalancheRamp`.
- `internal/consensus/rcl/engine_test.go` — `mockTxSet` gained `txIDs` slice for insertion-order `TxIDs()`; `mockAdaptor.BuildTxSet` now derives per-tx IDs from blob prefixes (32-byte) so round-trip tests see correct `Contains` behavior.
- `internal/consensus/rcl/disputes_test.go` **(new)** — Three acceptance tests:
  - `TestConsensus_OverlappingDisjointProposals_Converges`
  - `TestConsensus_BowOut_UnVotesDisputes`
  - `TestConsensus_AvalancheThresholdRamp`

### Acceptance criteria — all met

- **TestConsensus_OverlappingDisjointProposals_Converges** ✅ — peers split between {A,B,C} and {A,B,D} with enough ABCD-proposing peers for weight > 50%. Engine creates disputes C and D, `updatePosition` flips both to yes, final tx set is {A,B,C,D}.
- **TestConsensus_BowOut_UnVotesDisputes** ✅ — bowingNode votes YES on C, bows out via seqLeave; C's Yay count drops from 1 to 0 and bowingNode is gone from `Votes[bowingNode]`.
- **TestConsensus_AvalancheThresholdRamp** ✅ — `NeededWeight` returns 50 → 65 → 70 → 95 as percentTime advances past the cutoffs; the dispute's state advances accordingly with MinRounds enforced.

Bonus: `TestOnProposal_BowOutEvictsNode` and siblings (the B4 work) all still pass with the TODO(E2) resolved.

### Test status

- `go test ./internal/consensus/...` — all green (including the 3 new integration + 4 new unit + all prior tests).
- `go build ./...` — clean.
- Pre-existing failures on `feature/p2p-todos` (confirmed unchanged): `codec/binarycodec/types`, `internal/rpc`, `internal/tx/payment`, `internal/tx/vault`, `internal/testing/consensus::TestClusterNodesHaveSameGenesis`. None of these touch consensus or the TxSet interface.
