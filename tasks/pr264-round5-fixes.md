# PR #264 Round-5 Fix Plan — Final Conformance Audit Findings

**Branch:** `feature/p2p-todos` @ `0f14f15`
**Source:** Final A–G conformance audit against rippled (post-round-4)
**Scope:** 1 blocker bug (data-loss, introduced by this PR), 1 pre-existing security gap, 2 validator-readiness items, 7 divergences, 6 follow-up cleanups

**Pre-plan verification:** each finding below was re-read against live code and rippled source. Line numbers are verified. Nothing fabricated.

---

## Phase 1 — Blockers

### R5.1 — `adoptVerifiedLedger` installs an empty tx map (BUG introduced by this PR)

**Files:**
- `internal/ledger/service/service.go:1399-1435` (`AdoptLedgerWithState`)
- `internal/consensus/adaptor/router.go:842-863` (`adoptVerifiedLedger`)

**Verified state:** `AdoptLedgerWithState` calls `genesisLedger.TxMapSnapshot()` at line 1408 — always returns the empty tx map of genesis. The router at `adoptVerifiedLedger` has the verified replay-delta's derived ledger (which carries the real tx map) but only snapshots its state map, discarding the tx map. Result: every replay-delta-adopted ledger in local history has the correct state root but zero transactions. `tx`, `tx_history`, `account_tx`, `transaction_entry` RPCs return empty. Node cannot re-serve replay-deltas for adopted ledgers.

**Rippled anchor:** `src/xrpld/app/ledger/detail/LedgerDeltaAcquire.cpp:209` — installs the peer-provided tx-blob tree into the adopted ledger.

**Fix:**
1. Change `AdoptLedgerWithState` signature:
   ```go
   func (s *Service) AdoptLedgerWithState(
       h *header.LedgerHeader,
       stateMap *shamap.SHAMap,
       txMap *shamap.SHAMap,   // NEW
   ) error
   ```
2. If `txMap == nil` (legacy state-only catchup path at `router.go:1120`), fall back to `genesisLedger.TxMapSnapshot()` as today; otherwise use the provided tx map.
3. `adoptVerifiedLedger` at `router.go:842-863` adds:
   ```go
   txMap, err := l.TxMapSnapshot()
   if err != nil { return fmt.Errorf("snapshot tx map: %w", err) }
   ```
   and passes it to `AdoptLedgerWithState`.
4. Also document that the adopted ledger's `TxHash` must match `txMap.Hash()` — `replay_delta.go` already verifies this before reaching adoption, so it's a precondition check rather than re-verification.

**Verification:**
- New test `TestAdoptLedgerWithState_PreservesTxMap` in `internal/ledger/service/`: build a SHAMap with 2 tx-blobs, call `AdoptLedgerWithState` with it, assert the adopted ledger's `TxMap().Hash()` equals the original.
- Extend `router_replay_delta_test.go` to assert that after a successful replay-delta adoption, the ledger service's `GetLedgerByHash(adopted_hash).TxMap()` has the expected tx count.
- Mutation-test: revert the txMap fix and confirm both new tests fail.

---

### R5.2 — Handshake verification (PARTIAL — signature verification deferred)

**Outcome:** self-connection detection and network-ID verification are now live on the inbound handshake path (`overlay.go:performInboundHandshake`). Full signature verification is blocked on a pre-existing `MakeSharedValue` asymmetry: Go's TLS 1.2 server calls `sendFinished(nil)` at `handshake_server.go:119` and never populates `c.serverFinished`, so inbound and outbound sides compute different shared values and signature verification rejects every honest peer. The deferred piece is tracked as an R5.2-followup.

### R5.2 — Handshake signatures, self-connection, network-ID verification never called in production (PRE-EXISTING SECURITY GAP)

**Files:**
- `internal/peermanagement/handshake.go:240-296` (`VerifyPeerHandshake` defined)
- `internal/peermanagement/overlay.go:444-502` (`performInboundHandshake` — missing call)
- `internal/peermanagement/peer.go:232-287` (outbound handshake — missing call)

**Verified state:** `grep -r VerifyPeerHandshake internal/` returns only test files. `performInboundHandshake` parses features from `req.Header` but never validates:
- `Public-Key` header — peer identity is asserted, never verified against `Session-Signature`
- Self-connection check (`pubKeyStr == localPubKey`)
- Network-ID mismatch
- Peer pubkey format (0xED ed25519 rejection)

Any TCP/TLS peer can spoof an arbitrary node identity. Mainnet↔testnet cross-connects succeed silently.

**Rippled anchor:** `src/xrpld/overlay/detail/Handshake.cpp:286-323` — `verifyHandshake` is called from both inbound (`PeerImp.cpp`) and outbound handshake flows.

**Fix:**
1. In `performInboundHandshake` (`overlay.go:445-502`), after `ParseProtocolCtlFeatures`, add:
   ```go
   localPubKey := o.identity.PublicKey().Encode()
   peerPubKey, err := VerifyPeerHandshake(req.Header, sharedValue, localPubKey, HandshakeConfig{
       NetworkID: o.cfg.NetworkID,
   })
   if err != nil {
       return NewHandshakeError(peer.Endpoint(), "verify", err)
   }
   // Store on the peer so IncPeerBadData + PeerCapabilities.PubKey lookups work.
   peer.mu.Lock()
   peer.remotePubKey = peerPubKey
   peer.mu.Unlock()
   ```
2. Mirror in the outbound handshake path (`peer.go:232-287`) after the response is read.
3. Map verification errors to disconnect + bad-data charge:
   - `ErrSelfConnection`, `ErrNetworkMismatch` → disconnect, no charge (not the peer's fault per se, but close).
   - Signature failures → `feeInvalidSignature` (2000, see R5.7) charge + disconnect.
   - Missing headers → `feeMalformedRequest` (200) charge + disconnect.
4. Add a test `TestHandshake_RejectsForgedSignature` and `TestHandshake_RejectsSelfConnection` that drive a real inbound path with a crafted handshake and assert the connection is torn down.

**Risk:** This change will reject peers that previously "connected" but had broken handshake headers. Pre-verification that this doesn't break existing integration tests:
- `TestTwoOverlay_*` tests use real valid handshakes end-to-end; should pass.
- Any test that `Dial`s with a handmade HTTP request and skipped signing will break — audit for those.

**Verification:**
- Run full `internal/testing/p2p/...` suite; all existing two-overlay tests must pass.
- New negative tests with forged Session-Signature and mismatched Public-Key.
- Self-connection test: start two overlays with the same identity seed, attempt connection, assert rejected with `ErrSelfConnection`.

---

## Phase 2 — Validator-readiness (if ever on a UNL)

### R5.3 — `sfAmendments` never populated on outbound validations

**Files:**
- `internal/consensus/rcl/engine.go:1592-1663` (sendValidation)
- `internal/consensus/adaptor/adaptor.go` (new accessor)
- `internal/consensus/engine.go` (interface addition)

**Verified state:** `grep "validation.Amendments" internal/consensus/` returns only the serializer and tests. `sendValidation` never assigns `validation.Amendments`. Round-3's 19→3 field-code fix is cosmetic without a writer.

**Rippled anchor:** `src/xrpld/app/consensus/RCLConsensus.cpp:888-893` — `app_.getAmendmentTable().doValidation(...)` returns the amendments this validator wishes to vote FOR on the next flag ledger.

**Fix:**
1. Add `GetAmendmentVote() [][32]byte` to the `consensus.Adaptor` interface:
   ```go
   // GetAmendmentVote returns the list of amendment IDs this validator
   // wishes to vote FOR on the upcoming flag ledger. Only populated on
   // flag ledgers (seq+1)%256==0 — callers gate emission separately (see
   // isVotingLedger in R5.4). Returns nil on non-validators or when the
   // amendment table has no pending votes. Matches rippled
   // RCLConsensus.cpp:888-893.
   GetAmendmentVote() [][32]byte
   ```
2. Production `Adaptor.GetAmendmentVote()`:
   - Read the validator's amendment-vote stance from `Config` (see R5.3a below).
   - Cross-check against the current ledger's `Amendments` SLE: only vote FOR amendments NOT already enabled and supported by our codebase (via `amendment.AllFeatures()`).
   - Return a canonically-sorted slice of amendment IDs.
3. Add `AmendmentVote []string` to `Config` (accepts feature names, converted to IDs at adaptor construction).
4. `sendValidation` populates: `validation.Amendments = e.adaptor.GetAmendmentVote()` — **gated on R5.4**.

**R5.3a — Config wiring:** the [voting] stanza in rippled's config has `[amendments]` and `[veto_amendments]`. Mirror: `ConfigAmendments []string` (want enabled) and `ConfigVetoAmendments []string` (want disabled). For this PR scope, support only the "amendments" list; veto support can land as follow-up.

**Verification:**
- Unit test `TestAdaptor_GetAmendmentVote_FiltersAlreadyEnabled`: configure stance with 3 amendments, mock a ledger with 1 already enabled, assert only 2 returned.
- Unit test at engine level: configure stance, drive a flag-ledger validation, assert `validation.Amendments` is non-empty and canonically sorted.

---

### R5.4 — No `isVotingLedger` gate on fee-vote / amendments emission

**File:** `internal/consensus/rcl/engine.go:1610-1635` (sendValidation fee-vote block)

**Verified state:** current code emits the fee-vote triple whenever any of `baseFee/reserveBase/reserveIncrement` is non-zero, on **every** ledger. Rippled emits only on flag ledgers.

**Rippled anchor:** `src/xrpld/app/consensus/RCLConsensus.cpp:879` — `if (((ledger.seq() + 1) % 256) == 0)`. `Ledger.cpp:951-953` defines `isVotingLedger`.

**Fix:**
1. Add a small helper in the engine:
   ```go
   // isVotingLedger reports whether a validation for this ledger should
   // carry fee-vote and amendment-vote fields. Rippled emits these only
   // on flag ledgers (the validation with seq N covers the transition
   // from seq N to seq N+1; the vote is for the next flag boundary).
   // Matches Ledger.cpp:951-953.
   func isVotingLedger(ledgerSeq uint32) bool {
       return (ledgerSeq+1)%256 == 0
   }
   ```
2. Wrap both the fee-vote block and the (new R5.3) amendments block:
   ```go
   if isVotingLedger(ledger.Seq()) {
       // fee-vote emission (existing)
       // amendment-vote emission (from R5.3)
   }
   ```

**Verification:**
- Unit test `TestSendValidation_FeeVoteOnlyOnFlagLedger`: seed a fee-vote stance, drive two validations — one at seq=255 (flag: 255+1=256), one at seq=100 (non-flag). Assert fee-vote triple present only on the flag-ledger validation.
- Same pattern for `TestSendValidation_AmendmentsOnlyOnFlagLedger`.

---

## Phase 3 — Accidental divergences

### R5.5 — `isCurrent` windows mis-applied (3 bugs)

**File:** `internal/consensus/rcl/validations.go:172-190`

**Verified state** (compared against `rippled/src/xrpld/consensus/Validations.h:148-166`):

| Check | Rippled | goXRPL now |
|-------|---------|------------|
| signTime past bound | `signTime > (now - EARLY=3m)` | `signTime.Before(now - WALL=5m)` rejects — **wrong: uses WALL** |
| signTime future bound | `signTime < (now + WALL=5m)` | `signTime.After(now + EARLY=3m)` rejects — **wrong: uses EARLY** |
| seenTime bound | `seenTime == 0 \|\| seenTime < (now + LOCAL=3m)` — FUTURE | `seenTime.Before(now - LOCAL=3m)` rejects — **wrong: checks PAST** |

**Fix:**
```go
func isCurrent(now, signTime, seenTime time.Time) bool {
    // Rippled Validations.h:162-165. Past bound on signTime uses EARLY;
    // future bound uses WALL; seenTime FUTURE-bound uses LOCAL.
    if !signTime.After(now.Add(-validationCurrentEarly)) {
        return false
    }
    if !signTime.Before(now.Add(validationCurrentWall)) {
        return false
    }
    if !seenTime.IsZero() && !seenTime.Before(now.Add(validationCurrentLocal)) {
        return false
    }
    return true
}
```

**Verification:**
- Unit test exercising all 6 boundary cases (each side of each of the 3 bounds).
- Mutation-test: revert any single direction swap, confirm test fails.

---

### R5.6 — Bad-data weight mis-mapping for signatures vs hashes

**File:** `internal/peermanagement/overlay.go:46-102`

**Verified state:** bad sig/pubkey and bad-hash reasons are all mapped to `weightInvalidData=400`. Rippled charges:
- `feeInvalidSignature = 2000` for bad sig/pubkey (`Fees.cpp:28`)
- `feeMalformedRequest = 200` for bad hashes (`Fees.cpp:26`)

**Rippled anchor:** `PeerImp.cpp:1683-1686` (sig) and `PeerImp.cpp:1693` (hashes).

**Fix:**
1. Add new constant:
   ```go
   weightInvalidSignature = 2000
   ```
2. Reclassify in `BadDataWeight`:
   - `proposal-malformed-sig-size`, `proposal-malformed-pubkey-size`, `validation-malformed-sig-size` → `weightInvalidSignature`
   - `proposal-malformed-prev-ledger-size`, `proposal-malformed-txset-size`, `validation-malformed-ledger-hash-zero`, `validation-malformed-node-id-zero` → `weightMalformedReq` (200)
   - Keep `replay-delta-verify` as `weightInvalidData` (400) — it's a data-integrity failure, not a sig failure.
3. Rewrite the header comment to list all four weights with their rippled constant names.

**Verification:**
- Extend `bad_data_test.go` with per-reason weight assertions for sig, hash, and invalid-data categories.
- Guard test: one `feeInvalidSignature` charge (2000) + one decay tick, peer still below 25000 threshold (no regression with the new weight).

---

### R5.7 — `UpdateRelaySlot` trust-gates too early AND missing IDLED window

**Files:**
- `internal/consensus/adaptor/router.go:288-302, 338-350`

**Verified state** (vs `PeerImp.cpp:1730-1748, 2374-2395`): rippled calls `updateSlotAndSquelch` for BOTH trusted and untrusted duplicates — `isTrusted` branching happens AFTER. Rippled also gates on `(stopwatch().now() - *relayed) < IDLED` to avoid feeding the slot with arrivals older than the IDLED window (rippled's `reduce_relay::IDLED`).

goXRPL today gates on `r.adaptor.IsTrusted(...)` BEFORE `UpdateRelaySlot`, and has no IDLED check at all.

**Fix:**
1. Remove the `IsTrusted` gate in both `handleProposal` and `handleValidation`. Feed the slot on every duplicate regardless of trust:
   ```go
   if isDuplicate {
       r.adaptor.UpdateRelaySlot(proposal.NodeID[:], originPeer)
   }
   ```
2. Add IDLED semantics. `IDLED = 8 * time.Second` per `rippled/src/xrpld/overlay/reduce_relay/ReduceRelayCommon.h`. The dedup tracker already stores `seenAt`; extend `messageSuppression.observe` to return both `(firstSeen bool, lastSeen time.Time)`:
   ```go
   func (s *messageSuppression) observe(hash [32]byte) (firstSeen bool, lastSeen time.Time)
   ```
   Router checks:
   ```go
   isDuplicate, lastSeen := r.messageSeen.observe(hashPayload(msg.Payload))
   isIdled := !isDuplicate && time.Since(lastSeen) < reduceRelayIDLED
   if !isDuplicate && isIdled {  // only idled duplicates feed the slot
       r.adaptor.UpdateRelaySlot(proposal.NodeID[:], originPeer)
   }
   ```
   Wait — re-read rippled: it fires when `!added && relayed && (now - relayed) < IDLED`. So: fire the slot ONLY when the previous sighting is recent (`< IDLED`). Keep duplicates-only semantics, add `< IDLED` check.
3. Add constant `reduceRelayIDLED = 8 * time.Second` next to other reduce-relay constants in `internal/peermanagement/reduce_relay_common.go`.

**Verification:**
- Extend `TestRouter_UpdateRelaySlot_DuplicatesOnly` with an untrusted-validator case: proposal from a non-UNL key delivered by two peers — assert `UpdateRelaySlot` still fires (R5.7 fix: drops trust gate).
- New test `TestRouter_UpdateRelaySlot_IdledWindow`: first arrival at t=0, duplicate at t=10s → slot NOT fed (`IDLED=8s`); duplicate at t=2s → slot fed.

---

### R5.8 — Malformed TMSquelch silently dropped without charge

**File:** `internal/peermanagement/overlay.go:779-782` (inside `handleSquelchMessage`)

**Verified state:** `handleSquelchMessage` returns silently on `len(sq.ValidatorPubKey) == 0` or other shape issues. Rippled charges `feeInvalidData` at `PeerImp.cpp:2701-2712` for empty or wrong-length pubkeys.

**Fix:**
1. Validate pubkey length (33 bytes, compressed secp256k1) before dispatching:
   ```go
   if len(sq.ValidatorPubKey) != 33 {
       slog.Debug("TMSquelch: malformed pubkey", "t", "Overlay", "peer", evt.PeerID, "len", len(sq.ValidatorPubKey))
       o.IncPeerBadData(evt.PeerID, "squelch-malformed-pubkey")
       return
   }
   ```
2. Route `squelch-malformed-pubkey` to `weightInvalidData` in `BadDataWeight`.
3. Add equivalent check for squelch-duration overflow (caught elsewhere already, but tightly couple to a charge).

**Verification:**
- New test `TestOverlay_InboundSquelch_MalformedPubkey_Charges`: send a TMSquelch with 32-byte pubkey, assert the peer's bad-data count rose by `weightInvalidData` (400).

---

### R5.9 — `shouldCloseLedger` reads round-scoped validations instead of persistent tracker

**File:** `internal/consensus/rcl/engine.go:975-985`

**Verified state:** `proposersValidated` iterates `e.validations` (the per-round map that's reset at round start). Rippled reads the persistent `Validations` store via `adaptor_.proposersValidated(prevLedgerID_)` (`RCLConsensus.cpp:281`). On round open, goXRPL sees zero validated proposers until the current round's validations arrive.

**Fix:**
1. Add `ProposersValidated(ledgerID LedgerID) int` to `consensus.Adaptor`:
   ```go
   // ProposersValidated returns the count of trusted validators whose
   // most-recent full validation points at ledgerID. Reads the persistent
   // ValidationTracker, NOT the round-scoped map — matches rippled's
   // adaptor_.proposersValidated(prevLedgerID_) at RCLConsensus.cpp:281.
   ProposersValidated(ledgerID LedgerID) int
   ```
2. Production implementation reads from the engine's `validationTracker` via a `ProposersValidated(ledgerID)` method on the tracker (iterate `byNode`, count trusted + latest-validation == ledgerID + not-negUNL + Full).
3. `shouldCloseLedger` calls the adaptor method instead of scanning `e.validations`.

**Concern:** the engine currently owns the `validationTracker`, but the adaptor needs to call into it. Cleanest wiring: expose the tracker via an engine method `GetProposersValidated(ledgerID)` and have the adaptor thread-through — OR give the adaptor direct access to the tracker. Prefer the latter: the tracker is stateful protocol data, not round-scoped.

**Verification:**
- Test `TestShouldCloseLedger_PeerPressureFromTracker`: seed the validation tracker with 6 trusted validators' validations for our prev ledger, start a new round, call `shouldCloseLedger` before any round-scoped validation has arrived, assert it returns true (peer-pressure fired).

---

### R5.10 — `sfCookie`/`sfServerVersion`/`sfConsensusHash` non-zero-gate emission (LATENT)

**File:** `internal/consensus/adaptor/stvalidation.go:293-324` (and engine's sendValidation)

**Verified state:** serializer emits these only when non-zero. Rippled unconditionally emits when HardenedValidations is active (`RCLConsensus.cpp:846, 861, 864-866`). Today defended by:
- Cookie bumped to 1 on all-zero CSPRNG read.
- ourTxSet is always set when sendValidation runs (observed invariant, not enforced).

A future refactor could silently drop the field if the defending invariants slip.

**Fix:**
- This is a code-robustness fix. Option A: make the serializer emit unconditionally under HardenedValidations (passing a bool flag from engine). Option B: document the invariants and add runtime guards.
- **Recommend Option B** for scope control — add a panic-on-nil guard in `sendValidation`:
  ```go
  if validation.Cookie == 0 {
      panic("invariant: cookie must be non-zero before signing; check adaptor.GetCookie()")
  }
  ```
  (For ServerVersion + ConsensusHash, similar assertions.)

Keep Option A as a follow-up since it requires threading the HardenedValidations bool into the serializer signature — moderate API churn for a latent issue.

**Verification:**
- New test `TestSendValidation_PanicsOnZeroCookie`: force adaptor to return cookie=0, attempt sendValidation, assert panic.

---

### R5.11 — `ShouldRetry` fails hard during replay

**File:** `internal/ledger/inbound/replay_delta.go:579-592`

**Verified state:** if `ApplyResult` says `ShouldRetry`, Go hard-fails and falls back to legacy catchup. Rippled's `BuildLedger.cpp:246` discards the `ApplyResult` — `ShouldRetry` during replay is silently ignored (the tx stays in the block with whatever state applyTransaction produced).

**Fix:** relax the check. Replace the hard-fail with a logged warning:
```go
case result.Result.ShouldRetry():
    r.logger.Warn("tx returned ShouldRetry during replay; continuing (matches rippled BuildLedger.cpp:246)",
        "tx", fmt.Sprintf("%x", txHash),
        "ter", result.Result.String(),
    )
    // Fall through — do NOT return error. Engine divergence will surface
    // via state-hash mismatch at the end of Apply, which is the correct
    // arbitration signal.
```

**Concern:** This was intentionally stricter in prior rounds. Document that the state-hash check at the end of `Apply` is sufficient — if the retry-skipped tx produces a divergent state, we'll catch it there and fall back to legacy via the hash mismatch path, which is the correct signal.

**Verification:**
- Test with a crafted tx that returns `terRETRY` on replay + a correct parent state → assert replay succeeds without hard-fail, state hash arbitrates.
- Mutation-test: break the fix (re-add hard-fail), confirm test fails.

---

### R5.12 — VPRR/TXRR config collapsed to one flag

**Files:**
- `internal/peermanagement/overlay.go:490-493, 1042-1043`
- `internal/peermanagement/config.go:63`

**Verified state:** `Config.EnableReduceRelay` is a single bool. Both VPRR and TXRR handshake features are set to the same value. Parser at `handshake.go:636-658` tracks them separately (R4.7 fix) — but the producer side collapses them.

**Fix:**
1. Add two config bools: `EnableVPReduceRelay` and `EnableTxReduceRelay`. Keep `EnableReduceRelay` as a legacy alias — when set, propagate to both.
2. Update `overlay.go:490-493, 1042-1043` to read the two new flags directly.
3. Update `config.go` option helpers (`WithReduceRelay`, add `WithVPReduceRelay`, `WithTxReduceRelay`).

**Verification:**
- Unit test: set only `EnableVPReduceRelay=true`, verify handshake emits `vprr=1` without `txrr=1`.

---

## Phase 4 — Follow-up cleanups (should land, not blocking)

### R5.13 — No ed25519 handshake-rejection test (regression risk)

**File:** `internal/peermanagement/handshake_test.go`

**Fix:** add `TestVerifyPeerHandshake_RejectsEd25519Pubkey` that passes a 33-byte pubkey with 0xED prefix and asserts `ErrInvalidHandshake` or similar.

### R5.14 — Per-peer replayer concurrency cap

**File:** `internal/ledger/inbound/replayer.go`

**Fix:** add a per-peer counter in `Replayer`, cap at 2 concurrent acquisitions per peer (rippled's `LedgerReplayer.h:55` `MAX_PEERS_PER_LEDGER=2`). When the cap is reached, the caller picks a different peer or waits.

### R5.15 — Replayer `Stop()` drain method

**File:** `internal/ledger/inbound/replayer.go`

**Fix:** add `Stop()` that drains the in-flight map, fires the onFailed callback for each, and blocks return until all are cleared. Called from `Components.Stop()`.

### R5.16 — Typed sentinel errors for replay-delta apply-path

**File:** `internal/ledger/inbound/replay_delta.go`

**Fix:** replace string-error construction at lines ~579-599 with package-level errors (`ErrReplayApplyShouldRetry`, `ErrReplayApplyTef`, `ErrReplayApplyTem`, etc). Update tests to use `errors.Is` instead of `assert.Contains` on strings.

### R5.17 — `tryAdvance`-equivalent cascade on replay-delta adoption

**File:** `internal/ledger/service/service.go:1417-1419`

**Fix:** after `AdoptLedgerWithState` installs a new closed ledger, scan `pendingLedgers` (or equivalent held-ledgers map) for seq+1 arrivals and cascade-apply. Out-of-order replay-delta completions don't currently trigger follow-on adoption.

### R5.18 — `LedgerReplayTask` backward-chain walk (DEFER)

**File:** `internal/consensus/adaptor/router.go:878-884`

**Why defer:** full port of rippled's `LedgerReplayer` chain-walker is order-of-magnitude larger than the forward-only sub-acquisitions. Separate milestone. Document in `tasks/` as a tracked item for a future PR.

---

## Phase 5 — Parked (explicit no-action this round)

- Documentation-only: commit-message claim that engine calls `UpdateRelaySlot` (it's actually the router). Text-only correction, no code impact.
- `IssueSquelch` shim exposed without build tag. No observed misuse from non-test call sites; add a `// Deprecated: test-only` marker in a follow-up doc pass.

---

## Sequencing

**Recommended commit order** (one commit per item, amend all into one PR push):

1. R5.1 tx-map adoption fix (most urgent — data-loss bug introduced by this PR)
2. R5.5 isCurrent windows (tiny, high-value)
3. R5.6 bad-data weights reclassification
4. R5.8 malformed TMSquelch charge
5. R5.12 VPRR/TXRR config split
6. R5.4 isVotingLedger gate
7. R5.3 amendments population + R5.3a config
8. R5.11 ShouldRetry relax during replay
9. R5.9 shouldCloseLedger via tracker
10. R5.7 UpdateRelaySlot IDLED + trust-gate-fix
11. R5.2 handshake verification wiring (largest security fix; land last so earlier changes don't hide any regressions the handshake changes expose)
12. R5.10 cookie/server-version/consensushash guard
13. R5.13 ed25519 test
14. R5.14 per-peer replayer cap
15. R5.15 Replayer.Stop()
16. R5.16 sentinel errors
17. R5.17 tryAdvance cascade

After each phase: `go test ./internal/consensus/... ./internal/peermanagement/... ./internal/ledger/... ./internal/testing/p2p/...` and `golangci-lint run ./...`.

---

## Verification checklist (before commit & push)

- [ ] All new tests green
- [ ] `go test ./...` passes (excluding pre-existing vault test failures documented in R4)
- [ ] `golangci-lint run ./...` clean
- [ ] Mutation tests for R5.1, R5.5, R5.7, R5.9, R5.11 (stash the fix, re-run new test, confirm failure)
- [ ] For R5.2: drive a full two-overlay handshake with valid + invalid signatures; confirm invalid side drops
- [ ] For R5.1: after an adopted ledger, `LedgerService.GetLedgerByHash(hash).TxMap().Hash()` matches the verified tx root
- [ ] `./scripts/conformance-summary.sh` shows no new regressions

---

## Risk assessment

**Highest-risk change:** R5.2 (handshake verification wiring). Every inbound/outbound connection flows through this path. Mitigation: land last, run full two-overlay suite, sanity-check against a live rippled peer if feasible.

**Lowest-risk changes:** R5.5, R5.6, R5.8, R5.13 — tiny diffs, high signal-to-change ratio.

**Unknown risk:** R5.11 (ShouldRetry relax). The prior-round comment argues hard-fail is safer; relaxing it trusts the state-hash arbitration to catch real divergence. Mutation test required to prove the relaxed path doesn't mask engine bugs.
