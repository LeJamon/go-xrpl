# PR #264 Round-3 Review — Fix Plan

Addresses the coworker review on `feature/p2p-todos` head `1bf57e3`. Split by
severity; Phase 1 is merge-blocking, Phase 2 is should-land-now, Phase 3 is
doc/scope cleanup, Phase 4 is deferred-with-justification.

Each item carries: file:line, rippled anchor, concrete fix, verification
step, ordering dependencies.

---

## Phase 1 — Blockers (must land before merge)

### R3.1 STValidation canonical field ordering + mutually-exclusive fee-vote triples
**Network-splitting bug on featureXRPFees flag ledgers. Highest priority.**
- Files: `internal/consensus/adaptor/stvalidation.go:306-350`,
  `internal/consensus/adaptor/identity.go:251-281`
- Bug: `serializeSTValidation` emits Amount (type 6) block BEFORE Hash256
  (type 5) block. Canonical XRPL order is ascending type<<16|field, so type 5
  must come before type 6. `buildValidationSigningData` mirrors the same
  wrong order.
- Rippled anchor: `rippled/include/xrpl/protocol/SField.h:69-70` — STI_UINT256 = 5,
  STI_AMOUNT = 6. Canonicalization is enforced by `STObject::getSigningHash`.
- Impact: a goXRPL validator on a `featureXRPFees` flag ledger with any fee
  vote set (`BaseFeeDrops`, `ReserveBaseDrops`, or `ReserveIncrementDrops`
  non-zero) will sign a preimage that rippled peers cannot reproduce →
  signature verification fails network-wide.
- Fix:
  1. In `serializeSTValidation`, move the Hash256 block (currently :327-350)
     to appear BEFORE the Amount block (currently :306-325).
  2. In `buildValidationSigningData` (`identity.go`), apply the identical
     swap so the signing preimage matches the wire bytes.
  3. Add a regression test: `TestSerializeSTValidation_CanonicalOrder` that
     populates both AMOUNT and HASH256 fields, serializes, and asserts the
     field-header bytes appear in type-ascending order.
- Verify: round-trip sign/verify test with `BaseFeeDrops=10`, `ReserveBaseDrops=20`,
  `ReserveIncrementDrops=5`, `ConsensusHash` set — existing verify path must
  succeed. Capture a rippled flag-ledger validation with AMOUNT fields if
  available (CI fixture) and assert byte-equal after parse+re-serialize.

### R3.2 NegativeUNL wiring — make SetNegativeUNL reachable
- Files: `internal/consensus/rcl/validations.go:83-99`,
  `internal/consensus/rcl/engine.go:1363-1380` (acceptLedger refresh block),
  `internal/consensus/engine.go` (Adaptor interface),
  `internal/consensus/adaptor/adaptor.go`
- Bug: `ValidationTracker.SetNegativeUNL` exists and `Add()` / `checkFullValidation`
  consult `vt.negUNL`, but no caller populates it. On mainnet, any validator
  on the negative-UNL would still count toward quorum here — observable
  divergence from rippled.
- Rippled anchor: `rippled/src/xrpld/app/misc/NegativeUNLVote.cpp`; negative-UNL
  SLE keyed by `ltNEGATIVE_UNL` (fixed ledger entry).
- Fix:
  1. Add `Adaptor.GetNegativeUNL() []NodeID` to the interface
     (`consensus/engine.go`).
  2. Implement it in `adaptor.go` by loading the `ltNEGATIVE_UNL` SLE from
     the current validated ledger via `ledgerService` and extracting the
     `sfDisabledValidators` array (each with `sfPublicKey` — 33 bytes
     compressed secp256k1).
  3. In `engine.acceptLedger` at the trusted-set refresh block (currently
     :1363-1365), call
     `e.validationTracker.SetNegativeUNL(e.adaptor.GetNegativeUNL())`
     alongside `SetTrusted` and `SetQuorum`.
  4. Mock implementations in `engine_test.go` mockAdaptor and
     `adaptor_test.go` router mocks.
- Verify:
  - Unit test: seed trusted = {A, B, C, D}, set quorum=3, mark `D` on negUNL,
    add 3 validations from {A, B, D} for ledger L. Assert
    `IsFullyValidated(L)` is `false` (D's vote doesn't count toward quorum).
  - Remove D from negUNL, assert L is now fully validated without adding
    any new validation.

### R3.3 Test pinning P1.7 ModeSwitchedLedger recovery sequence
- File: `internal/consensus/rcl/engine_test.go` (new test)
- Bug: P1.7's claim — "after handleWrongLedger/OnLedger, enter
  ModeSwitchedLedger for one round, then next round promotes to
  ModeProposing" — is only enforced by code, not by any test. A future
  refactor could silently regress.
- Fix: add `TestEngine_WrongLedgerRecovery_ModeSequence` covering:
  1. Engine in ModeProposing → trigger handleWrongLedger via a divergent
     `getNetworkLedger` vote → assert mode transitions to ModeWrongLedger.
  2. Deliver the correct ledger via `OnLedger` → assert mode becomes
     ModeSwitchedLedger (not ModeProposing).
  3. Run `closeLedger` and `acceptLedger` on the switched-ledger round →
     assert `BroadcastProposal` and `BroadcastValidation` were NOT called.
  4. Trigger the next `startRoundLocked(..., recovering=false)` → assert
     mode is ModeProposing.
  5. Run closeLedger+acceptLedger on this next round → assert
     `BroadcastProposal` and `BroadcastValidation` WERE called.
- Depends on: nothing — pure test addition.
- Verify: the test itself is the verification. Mutation-testing: revert P1.7
  (set `recovering=false` in handleWrongLedger), run the test, it must fail.

---

## Phase 2 — Semantic correctness (should land with blockers)

### R3.4 acceptLedger validation gate — allow switchedLedger to emit partial validation
- File: `internal/consensus/rcl/engine.go:1273-1283` (the `if e.mode ==
  consensus.ModeProposing` gate around `sendValidation`)
- Bug: my P1.7 fix narrows validation emission to ModeProposing only.
  Rippled's `RCLConsensus.cpp:587-595` emits a validation whenever
  `validating_ && !consensusFail && canValidateSeq`, regardless of mode —
  passing `proposing` as a parameter that controls the vfFullValidation
  flag INSIDE the validation, not whether to send at all. switchedLedger
  emits a PARTIAL validation (Full=false).
- Rippled anchor: `RCLConsensus.cpp:594` — `validate(built, result.txns, proposing)`;
  the function emits regardless of mode but flips `vfFullValidation` off in
  non-proposing rounds.
- Fix:
  1. Change the gate to `if e.adaptor.IsValidator() && e.mode !=
     consensus.ModeWrongLedger` so we emit in Proposing AND SwitchedLedger AND
     Observing modes but not when we know we're on the wrong ledger.
  2. Inside `sendValidation`, set `validation.Full = (e.mode == ModeProposing)`
     so the flag bit accurately reflects whether we were a full participant
     this round. This is the partial-validation semantic rippled uses.
  3. Update the comment block to document the new gate and cite
     RCLConsensus.cpp:594.
- Verify: extend R3.3's test — assert that in the switchedLedger round
  `BroadcastValidation` IS called (not, as currently, suppressed), but the
  emitted validation has `Full == false`.
- Risk: a clock-skewed or disconnected node might emit stale partial
  validations. Mitigation: the isCurrent gate (R3.6 below) drops these at
  the receiving peer.

### R3.5 peerLCLs double-count gap — DEFERRED (see Q2 resolution below)
- Moved to Phase 4 after investigation found the reverse-map approach
  doesn't work in the presence of rotated validator signing keys. See
  Q2 analysis at the end of this document. The double-count is bounded
  and rare; the fix needs a broader decision about deployment identity
  coupling that's out-of-scope for this review round.

### R3.6 isCurrent uses adaptor.Now() not wall time.Now()
- File: `internal/consensus/rcl/validations.go` (the `isCurrent` callsite
  inside `Add`)
- Bug: `Add()` calls `isCurrent(time.Now(), …)`. Rippled uses the consensus
  adaptor's adjusted time (which tracks network close-time offset). On a
  clock-skewed node, wall-clock comparison can reject our OWN just-signed
  validation because the SignTime we stamp uses `adaptor.Now()` but the
  freshness check uses `time.Now()` — the delta is exactly the accumulated
  closeOffset, which can be meaningful after a long session.
- Rippled anchor: `Validations.h` `isCurrent` called with
  `app_.timeKeeper().closeTime()`.
- Fix:
  1. `isCurrent` already takes `now` as a parameter (good).
  2. At the callsite in `Add`, the tracker needs access to a Now function.
     Easiest: store a `now func() time.Time` on `ValidationTracker`,
     default to `time.Now`, overridable for tests AND for wiring to
     `adaptor.Now()`. Add `SetNow(fn func() time.Time)`.
  3. In `engine.Start`, after creating the tracker, call
     `e.validationTracker.SetNow(e.adaptor.Now)`.
- Verify: unit test — set tracker's Now to return a fixed time T, push a
  validation with SignTime=T-10min. Assert dropped as stale. Push SignTime=T-1min,
  assert accepted. Integration: set adaptor.closeOffset to +30s, sign a
  validation, pass through Add — must accept (it wouldn't with raw time.Now).

### R3.7 Response-path feature gating — charge on unnegotiated TMREPLAY_DELTA_RESPONSE / TMPROOF_PATH_RESPONSE
- File: `internal/peermanagement/overlay.go` (onMessageReceived switch)
- Bug: R3 (this PR's P2.2) gates the REQUEST-side messages but not the
  RESPONSE-side. A peer that didn't negotiate ledgerreplay can still send
  us TMReplayDeltaResponse / TMProofPathResponse unsolicited, and we
  process them.
- Rippled anchor: `PeerImp.cpp:1511-1515` — charges `feeMalformedRequest`
  when a response arrives from a peer without the feature.
- Fix: add the same `peerNegotiatedLedgerReplay(peerID)` gate to the
  inbound TMReplayDeltaResponse / TMProofPathResponse arms. On fail,
  `IncPeerBadData(peerID, "replay-delta-resp-unnegotiated")` and drop.
- Verify: unit test — inject a response from a peer whose handshake did
  NOT include ledgerreplay; assert it's dropped and counter increments.

---

## Phase 3 — Doc / comment hygiene

### R3.8 Fix stale sfAmendments comments
- Files: `internal/consensus/adaptor/stvalidation.go:56, 365, 367, 371`,
  `internal/consensus/adaptor/identity.go:287`
- Bug: the constant was corrected 19 → 3 in round 2, but 4 comments still
  reference "field 19" or "type 19 / field 19".
- Fix: grep for `field 19` under `internal/consensus/adaptor/` and rewrite
  each to "field 3 (type 19 Vector256)" per rippled's naming.

### R3.9 Fix misleading startup.go comment about mtGET_LEDGER coverage
- File: `internal/consensus/adaptor/startup.go:79-83`
- Bug: comment claims `NewLedgerProvider` "covers the legacy mtGET_LEDGER
  path" — it does not. mtGET_LEDGER goes through `router.go:handleGetLedger`
  which answers `LedgerInfoBase` only. The LedgerProvider is reached via
  `LedgerSyncHandler` for replay-delta and proof-path requests.
- Fix: rewrite comment to say "covers mtREPLAY_DELTA_REQ and
  mtPROOF_PATH_REQ; the legacy mtGET_LEDGER path is served directly from
  the consensus router".

### R3.10 Fix router.go InboundLedgers::find comment
- File: `internal/consensus/adaptor/router.go:573-574` (in `startLedgerAcquisition`)
- Bug: comment cites "Mirror rippled's InboundLedgers::find which dedupes
  across both inbound replay-delta and inbound-ledger state machines".
  Rippled's maps are actually separate; the unified check here is stricter
  than rippled, not a mirror.
- Fix: rewrite comment to acknowledge we're stricter than rippled — "Go
  beyond rippled's per-map dedup (which is separate per state machine) by
  checking BOTH maps — a single point of truth eliminates the cross-path
  race that rippled allows."

---

## Phase 4 — Deferred with justification

### R3.11 Manifest / master-key ↔ session-key chain
- Deferred. Dedicated validator-security milestone. Out-of-scope for this
  PR per reviewer's own framing ("non-blocking for Kurtosis/test-net").
- Action: add a single note to the PR description acknowledging the gap so
  the PR body explicitly lists it as known-out-of-scope rather than silent.

### R3.12 LedgerProvider getNodeFat / partial-path walk
- Partially overstated by reviewer — legacy mtGET_LEDGER base requests
  already work via `router.go:handleGetLedger`. The actual gap is in
  LedgerSyncHandler's handleGetLedger (dead code path) and in any peer
  that asks for non-leaf AS_NODE / TX_NODE via the replay-delta-adjacent
  path.
- Action: after R3.9 fixes the misleading comment, this is scoped to "we
  don't support fat-node walks". Dedicate follow-up only if CI shows peers
  depending on fat-node responses from us (which they won't, because they
  negotiate replay-delta instead).

### R3.13 Affected-account extraction reads top-level tx fields only
- Out-of-scope for this PR (consensus + P2P). AccountTx accuracy is a
  ledger-service concern. Dedicated follow-up.

### R3.14 TestReplayDelta_Apply_OrderedByIndex brittleness (P3.6)
- Skipped explicitly in my plan. Cost/benefit still holds — the test
  passes; rewriting to typed sentinels is polish, not correctness.
- Action: no change.

### R3.15 PeerCapabilities narrowing (P3.4)
- Reviewer concedes "defensible scope choice". Keep as-is.
- Action: no change.

### R3.16 Replay-delta 30s timeout
- Already honestly documented in the comment as intentional non-parity.
- Action: no change; port rippled's sub-task retry in a dedicated PR.

### R3.17 pendingValidation LRU counter (P3.7 half-landed)
- Already has warn log. Counter addition still useful.
- Action: fold into the telemetry pass alongside the existing
  DroppedMessages / DroppedLedgerResponses counters — out of scope here.

---

## Landing order

1. **R3.1** (STValidation ordering + fee-vote mutex) — zero dependencies.
2. **R3.2** (NegativeUNL wiring) — needs `Adaptor.GetNegativeUNL` first,
   then the acceptLedger wiring; small but touches the interface.
3. **R3.4** (validation gate) — lands BEFORE R3.3 so R3.3's assertions
   are against the final behavior.
4. **R3.3** (ModeSwitchedLedger test) — pins R3.4's choice.
5. **R3.6** (isCurrent clock) — one-line API addition + callsite.
6. **R3.7** (response-path gating) — small, independent.
7. **R3.8–R3.10** (comments) — squash into a single "docs:" commit.

R3.5 deferred per Q2 resolution below.

Recommended commit shape:
- `fix(consensus): canonical STValidation ordering + mutually-exclusive fee-vote triples` — R3.1
- `fix(consensus): wire negative-UNL into ValidationTracker` — R3.2
- `fix(consensus): emit partial validation in switchedLedger mode` — R3.4
- `test(consensus): pin wrong-ledger recovery mode sequence` — R3.3
- `fix(consensus): use adaptor clock for validation freshness check` — R3.6
- `fix(p2p): charge peers for unnegotiated replay-delta / proof-path responses` — R3.7
- `docs: correct stale comments in STValidation / startup / router` — R3.8–R3.10

---

## Exit criteria

- Regression test from R3.3 passes.
- A new `TestSerializeSTValidation_CanonicalOrder` passes and would fail on
  the pre-fix code (mutation-check).
- `golangci-lint run ./...` clean.
- `go test ./internal/consensus/... ./internal/peermanagement/...
  ./internal/ledger/... ./internal/testing/p2p/...` green.
- Kurtosis interop (2× rippled + 1× goXRPL) still reaches quorum on a
  flag-ledger boundary with fee-voting enabled. The flag-ledger test is
  the ACCEPTANCE gate for R3.1 — without it, R3.1's fix is unproven.

## Open questions — RESOLVED

### Q1: sfBaseFee vs sfBaseFeeDrops — **STRICTLY MUTUALLY EXCLUSIVE**

`rippled/src/xrpld/app/misc/FeeVoteImpl.cpp:120-192` is a hard if/else on
`rules.enabled(featureXRPFees)`:
  - **XRPFees ON**: emits ONLY `sfBaseFeeDrops`, `sfReserveBaseDrops`,
    `sfReserveIncrementDrops` (AMOUNT-typed).
  - **XRPFees OFF**: emits ONLY `sfBaseFee`, `sfReserveBase`,
    `sfReserveIncrement` (legacy UINT-typed).
  - Never both. The branches are full function bodies that don't fall
    through.

**Implication for R3.1 and fee-vote population:**

My current serializer emits each field independently when non-zero — which
CAN double-emit if both legacy and drops variants are set. The adaptor
must therefore choose ONE set based on the parent ledger's rules and
zero out the other.

**Updated fix for R3.1** (append to the existing R3.1 entry above):
  - Add to the adaptor's validation-populating path (in `sendValidation`
    or a new `populateFeeVote` helper): check
    `rules.IsEnabled(amendment.XRPFees)` on the parent ledger; populate
    ONLY the matching triple; ensure the other triple stays at zero.
  - The "non-zero" gate in the serializer stays as a defensive measure —
    it means a bug in the population logic produces a MISSING field
    rather than a DOUBLE field.

### Q2: HeaderPublicKey ≠ validator pubkey — **THEY ARE DISTINCT**

Three potentially-diverging keys per rippled node:
  1. **Node pubkey** — in `Public-Key` handshake header. Source:
     `[node_seed]` config (`ConfigSections.h:63`) or wallet DB.
     `Handshake.cpp:199-200` writes `app.nodeIdentity().first`.
  2. **Validator master pubkey** — the UNL entry / trusted-set match key.
     Source: `[validation_seed]` (`ConfigSections.h:89`) or validator
     token. Accessed via `validatorKeys_.keys->masterPublicKey`
     (`RCLConsensus.cpp:111`).
  3. **Validator signing pubkey** — signs proposals/validations. Rotated
     under the validator-token manifest scheme. Accessed via
     `validatorKeys_.keys->publicKey` (`RCLConsensus.cpp:114`).

All three can differ. On mainnet with token-rotated validators, they
commonly do.

**Implication for R3.5:** the proposed reverse map
`peerID → NodeID` built from the handshake `Public-Key` header does NOT
dedupe correctly, because:
  - handshake-pubkey = node pubkey (key #1)
  - proposal NodeID = validator signing pubkey (key #3)
  - `IsTrusted(NodeID)` checks master pubkey (key #2) via UNL
  - key #1 ≠ key #2 ≠ key #3 in the common case

The reverse map would fail silently on trusted validators who rotate
signing keys — exactly the deployment the fix most needs to handle.

**Updated fix for R3.5** (replaces the current R3.5 approach):

Instead of keying dedup on "same validator", key it on "same peer":
  1. Extend `Adaptor.UpdatePeerLCL` to atomically record that the peerID
     has REPORTED an LCL. Separately, on every inbound proposal, record
     that the peerID has CONTRIBUTED a proposal this round.
  2. In `getNetworkLedger`, when folding peerLCLs, skip any peerID that
     has ALREADY contributed a proposal vote this round — regardless of
     whether the hashes match. The assumption: one peer, one vote; if a
     peer has a proposal on record, its status-change LCL is redundant
     signal and should not count a second time.
  3. The contributed-proposal tracking lives on the engine's
     `recentProposals` buffer, not the adaptor — we already index
     proposals by NodeID there. Add a peerID-keyed counterpart populated
     in `OnProposal` when `originPeer != 0`.
  4. Reset the per-round tracking on `startRoundLocked`.

This avoids the handshake-pubkey coupling entirely and works for
token-rotated validators.

**Risk of the new approach:** a non-validator peer that merely forwards
proposals (gossip) is not the origin but still has `originPeer !=
proposer_nodeID`. We'd dedup its LCL against the forwarded proposal,
which is overly aggressive for honest topology. Mitigation: only
record peerID-contributed-proposal when the proposal is ORIGINATED (not
relayed). Signal: proposal arrived with `originPeer` matching a peer
whose handshake pubkey matches the proposal's NodeID — which IS the
"this peer runs this validator" case we want. For everyone else, treat
the LCL vote independently.

But that still needs the handshake-pubkey-to-proposal-NodeID comparison
to distinguish origin from relay. Which is only meaningful for
validators who run their node identity and validator master identity
from the same seed (not required, but possible).

**Simpler final approach:** just accept the small over-count. Document
that peerLCL + proposal from the same peer both count in the rare case
a peer reports an LCL that diverges from the proposal's prevLedger
(which indicates the peer is EITHER on a different branch from its
proposal OR concurrently moved during the round — both worth
double-counting as a signal). Mark R3.5 as DEFERRED with the analysis
above, pending a decision on whether to require same-seed validator
deployments in goXRPL.

### Plan updates

- **R3.1** gains a sub-item: populate fee-vote fields mutually
  exclusively based on parent-ledger rules.
- **R3.5** downgraded from Phase 2 to Phase 4 (deferred) with the
  analysis above. The double-count is bounded (one peer, one extra
  vote in a rare configuration) and doesn't block merge.
