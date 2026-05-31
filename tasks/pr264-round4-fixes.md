# PR #264 Round-4 Fix Plan — Conformance-Audit Findings

**Branch:** `feature/p2p-todos` @ `0c144b2`
**Source:** Parallel conformance audit (subsystems A–G) vs rippled reference
**Scope:** 3 blockers, 8 accidental divergences, 3 NOT-IMPLEMENTED follow-ups, 4 parked items

The audit confirmed all round-3 fixes landed correctly (MATCH on sfAmendments
field-3, Hash256-before-Amount ordering, negUNL wiring, R3.4 gate + Full flag,
R3.3 mode sequence, isCurrent clock, handshake pubkey identity, charge weights,
replay-delta verification, fork-flip guard). Everything below is net-new vs
round-3 — either previously unreviewed or surfaced by the deep line-by-line pass.

---

## Phase 1 — Blockers (merge-blocking)

### R4.1 — Eviction threshold 1000 vs rippled's 25000

**File:** `internal/peermanagement/overlay.go:20-38`

**Finding (audit C / priority #1):** `EvictBadDataThreshold = 1000` hardcodes
rippled's **minGossipBalance** (not the drop threshold). Rippled drops the
connection at the **25000** threshold — `dropThreshold_` in
`Resource/Charge.h:52`. The current constant evicts honest peers 25× more
aggressively than rippled: one `feeInvalidData` charge (400) plus a handful of
decode misses within a single 10 s decay window crosses 1000. Worse, the header
comment advertises "mirrors rippled's Resource::Consumer model", which is
actively wrong.

**Rippled anchors:**
- `src/xrpld/overlay/detail/ProtocolVersion.cpp` — no direct constant
- `src/xrpld/overlay/Resource/impl/Tuning.h:30-40` — `dropThreshold = 25000`
- `src/xrpld/overlay/Resource/impl/Charge.cpp:26-30` — `feeInvalidData = 400`

**Fix:**
1. Raise `EvictBadDataThreshold` to `25000` (keep `const`, same type).
2. Keep `badDataDecayInterval = 10 * time.Second` — Go's step-halving already
   decays about twice as fast as rippled's continuous 32 s window, which is an
   accepted INTENTIONAL-DIVERGENCE (audit C).
3. Rewrite the doc comment: reference `Tuning.h:dropThreshold` by name, drop
   the false "2.5 × feeInvalidData" example math, replace with rippled's
   canonical examples (62.5 × feeInvalidData, or a sustained feeMalformedRequest
   stream for several decay intervals).

**Verification:**
- Existing `TestPeer_EvictionOnThreshold` (if present) must still pass; update
  its fixture numbers to 25000 and add one case asserting a single 400-weight
  charge does NOT evict.
- Add `TestPeer_NoEviction_AfterSingleInvalidData` — one `IncBadData(peer,
  "replay-delta-verify")` (weight 400) + one decay tick, assert peer stays
  connected.

---

### R4.2 — Reduce-relay natural selection path unproven at integration layer

**Files:**
- `internal/peermanagement/testing/two_overlay_test.go:409-468` (existing)
- `internal/peermanagement/relay.go:*` (no change, just new coverage)

**Finding (audit E / NOT-IMPLEMENTED #1):** `TestTwoOverlay_Squelch_RoundTrip`
drives `IssueSquelch` directly, bypassing the 7-hop selection pipeline
(`router → adaptor → sender → overlay → relay → slot → callback → TMSquelch`).
All seven hops are unit-tested in isolation but no test drives them end-to-end.
Commit cb3d0bb's "two-overlay squelch test coverage" claim is accurate for
wire-frame round-trip only, not for the selection state machine. A regression
where any hop fails to fire would not be caught by CI.

**Rippled anchors:**
- `src/xrpld/overlay/detail/Slot.cpp:updateSlotAndSquelch` — threshold ≥
  `MaxMessageThreshold` triggers squelch emission
- `src/xrpld/overlay/detail/PeerImp.cpp:1737` — feeds `updateSlotAndSquelch` on
  duplicate trusted TMProposeSet only
- `src/xrpld/overlay/detail/ReduceRelayCommon.h:46-54` — constants

**Fix:** Add `TestTwoOverlay_Squelch_NaturalSelection` to
`two_overlay_test.go`:
1. Wire two overlays A and B as peers.
2. Mark a validator `V` trusted on A.
3. Flood A with `MaxMessageThreshold + 1` duplicate trusted proposals from `V`,
   each carrying a distinct peer-id source so the slot sees multiple sources.
4. Assert A emits a TMSquelch to B for `V`, carrying a duration in the
   `[MinSquelchDuration, MaxSquelchDuration]` range.
5. Mutation-test: stub `Relay.OnMessage` to return without calling
   `UpdateRelaySlot`; confirm the new test fails and the existing round-trip
   test still passes (proves the new test exercises the selection machine, not
   the wire frame).

**Verification:**
- Run the new test isolated; run the full
  `go test ./internal/peermanagement/...` to confirm no regression.

---

### R4.3 — Fee-vote / amendment / cookie / server-version never populated

**Files:**
- `internal/consensus/rcl/engine.go:1583-1645` (sendValidation)
- `internal/consensus/adaptor/adaptor.go` (new accessor(s))
- `internal/consensus/adaptor/stvalidation.go:272-361` (no change, already emits)

**Finding (audit A / priority #2):** The STValidation wire format emits all 7
optional fields correctly **when non-zero**:

- `sfBaseFeeDrops`, `sfReserveBaseDrops`, `sfReserveIncrementDrops` (AMOUNT, post-XRPFees)
- `sfBaseFee`, `sfReserveBase`, `sfReserveIncrement` (legacy UINT triple, pre-XRPFees)
- `sfAmendments` (VECTOR256)
- `sfCookie` (UINT64)
- `sfServerVersion` (UINT64)

But **no writer populates any of them** — engine.go constructs `Validation`
with only `LedgerID`, `LedgerSeq`, `NodeID`, `SignTime`, `SeenTime`, `Full`,
`ConsensusHash`, `ValidatedHash`. Round-3's R3.1 asserted "fee voting is no
longer a silent no-op" by fixing the *serializer* — but without an engine writer,
go-xrpl validators contribute zero signal to flag-ledger governance. Rippled
populates these at `RCLConsensus.cpp:820-847` (fee vote) and via `ValidatorList`
for amendment votes.

**Rippled anchors:**
- `src/xrpld/app/consensus/RCLConsensus.cpp:820-847` — fee-vote population
- `src/xrpld/app/consensus/RCLConsensus.cpp:833-847` — amendment list
- `src/xrpld/app/consensus/RCLConsensus.cpp:813-818` — cookie (random per-boot)
- `src/xrpld/app/consensus/RCLConsensus.cpp:803-811` — server version
- `src/xrpld/app/misc/detail/FeeVoteImpl.cpp:120-192` — mutual-exclusion gate on
  `rules.enabled(featureXRPFees)`

**Fix — split into 4 sub-items (ordered by risk):**

**R4.3a — Cookie (lowest risk, wire-only)**
- Add `Cookie uint64` generation at `Adaptor.New()`: read 8 bytes from
  `crypto/rand`, store on the adaptor. Rippled uses
  `std::random_device()()` one-shot per boot (`RCLConsensus.cpp:813-818`).
- Expose `Adaptor.GetCookie() uint64`.
- `engine.go:sendValidation`: `validation.Cookie = e.adaptor.GetCookie()`.

**R4.3b — ServerVersion (wire-only)**
- Add a `BuildVersion uint64` constant to `internal/consensus/adaptor/` that
  encodes the go-xrpl version in rippled's `BuildInfo::encodeSoftwareVersion`
  format (or a clearly-go-xrpl-specific top bit). Rippled uses a 64-bit
  encoding with 0x8000_0000_0000_0000 as the "rippled" high bit; go-xrpl must
  **not** pretend to be rippled (setting that bit misleads peers counting
  version stats on the network). Set a distinct high bit instead and document it.
- `engine.go:sendValidation`: `validation.ServerVersion = adaptor.BuildVersion`.

**R4.3c — Fee vote (moderate risk, governance-visible)**
- Add `Adaptor.GetFeeVote() (baseFee, reserveBase, reserveIncrement uint64, postXRPFees bool)`
  reading the validator's configured fee-vote stance (from config toml
  `[voting]` stanza — mirror rippled's `FeeVoteSetup`).
- In `engine.go:sendValidation`:
  - If `postXRPFees`: set `v.BaseFeeDrops`, `v.ReserveBaseDrops`,
    `v.ReserveIncrementDrops` (AMOUNT triple).
  - Else: set `v.BaseFee`, `v.ReserveBase`, `v.ReserveIncrement` (legacy UINT
    triple).
  - The serializer already gates on non-zero → no double-emission risk.
- The `postXRPFees` bool must come from the parent ledger's `Rules` (ledger
  service accessor) — NOT a static config — so the voting switches the moment
  the amendment activates on the network.

**R4.3d — Amendment vote (highest risk; depends on ValidatorList)**
- Deferred with a short justification below (Phase 4). R4.3d is blocked on
  manifest-chain / ValidatorList work (R3.11); a stubbed amendment vote would
  be worse than none, because empty `sfAmendments` with the field present would
  mis-signal "I don't support any amendments."
- **Current R4.3 scope stops at R4.3c.**

**Verification:**
- Unit test in `rcl/engine_test.go`: `TestSendValidation_PopulatesCookie_FeeVote`
  — configure a fee-vote stance, drive one round, parse the emitted
  `consensus.Validation`, assert Cookie ≠ 0, ServerVersion ≠ 0, and the fee
  triple matches config.
- Byte-level: capture one serialized validation, decode it with the adaptor's
  parser, confirm round-trip.

---

## Phase 2 — Accidental divergences (should land with blockers)

### R4.4 — `UpdateRelaySlot` fires on every trusted inbound instead of duplicates only

**Files:**
- `internal/consensus/adaptor/router.go:220-233, 265-276`

**Finding (audit B8):** `handleProposal` and `handleValidation` call
`UpdateRelaySlot` unconditionally after a successful `engine.OnProposal` /
`engine.OnValidation` when trusted. Rippled `PeerImp.cpp:1737` calls it only
inside the `!added` branch — i.e., when the same proposal was seen from a
different peer (a duplicate), which is the actual "multi-source" signal the
reduce-relay threshold machine is designed for. Firing on every trusted inbound
accelerates selection — go-xrpl hits `MaxMessageThreshold` in ~21 messages, not
~21 duplicates → faster and more aggressive squelches than rippled emits.

**Rippled anchors:**
- `src/xrpld/overlay/detail/PeerImp.cpp:1730-1738` — gated on `!added`
- `src/xrpld/overlay/detail/PeerImp.cpp:2385, 3013, 3049` — validation path,
  same duplicate gate

**Fix:**
1. Change `consensus.Engine.OnProposal` / `OnValidation` return signatures to
   communicate "first-seen vs duplicate" — either a new method
   `OnProposalAdded(proposal) (added bool, err error)` or a sibling
   `IsDuplicateProposal(hash)` check. Prefer extending the return value to
   avoid extra lookups.
2. In the router: `if added || ... then skip UpdateRelaySlot` — invert:
   `if !added && r.adaptor.IsTrusted(...) { r.adaptor.UpdateRelaySlot(...) }`.
3. Update all `Engine` implementers + mock (`engine_test.go`).

**Verification:**
- Extend `TestHandleProposal_ReduceRelayFeed` (or add one) with a sequence:
  [Peer1 sends P; Peer2 sends P] and assert `UpdateRelaySlot` is called **once**
  (on the Peer2 duplicate), not twice.

---

### R4.5 — Inbound TMSquelch gate is stricter than rippled, comment claims parity

**File:** `internal/peermanagement/overlay.go:586-597`

**Finding (audit E3):** Go drops inbound `TMSquelch` and charges 400
(`weightInvalidData`) if the peer didn't negotiate `reduceRelay`. Rippled's
`PeerImp.cpp:2691-2732` has **no** such ingress gate — it processes TMSquelch
regardless of feature negotiation (the feature negotiation governs what WE
send, not what we accept). The comment claims "Mirrors rippled's
PeerImp::onMessage(TMSquelch)"; that mirror claim is false.

**Fix — two valid options:**
1. **Drop the gate** (parity fix). Accept TMSquelch unconditionally; log if
   peer didn't negotiate but still apply it. This is the truly-rippled-parity
   behavior.
2. **Keep the gate, fix the comment + reduce the charge weight.** Document it
   as an INTENTIONAL-DIVERGENCE: non-negotiated TMSquelch is nearly always a
   misconfigured peer, but 400-per-message crosses eviction in 63 messages;
   downgrade to `weightRequestNoReply` (10) so a chatty misconfigured peer isn't
   evicted for it.

**Recommendation:** Option 1 (parity). Squelch messages are harmless if applied
— they just suppress what we send — and rejecting them creates a
not-actually-rippled attack surface where a hostile peer could trick a third
party by advertising one thing to us and another to a rippled neighbor.

**Verification:**
- Add `TestOverlay_InboundSquelch_FromUnnegotiatedPeer` — peer handshakes
  without reducerelay, then sends a TMSquelch. Assert: the squelch is applied
  (Overlay.IsSquelched returns true for the target) and no bad-data charge is
  applied.
- Delete (or invert) any existing test that pins the stricter behavior.

---

### R4.6 — negUNL filter not applied to `GetTrustedSupport` / `IsFullyValidated`

**File:** `internal/consensus/rcl/validations.go:355-388`

**Finding (audit B2):** `checkFullValidation` (line 278) correctly filters
`vt.negUNL[nodeID]` when counting for quorum. But:
- `GetTrustedValidationCount` (line 356) — does NOT filter negUNL
- `GetTrustedSupport` (line 381) — alias of above, inherits the bug
- `IsFullyValidated` (line 386) — uses `GetTrustedValidationCount`, so checks
  quorum against un-filtered counts

Rippled wraps every trusted-count read through `negativeUNLFilter`
(`LedgerMaster.cpp:886, 952, 1120`). Today this is harmless because both sides
of any `checkLedger` comparison are equally unfiltered, but it violates
rippled's invariant: **all trusted reads must be negUNL-filtered**, so any new
caller (server_info, validation_info RPC, LedgerTrie port) gets correct counts.

**Fix:**
1. Extract a private helper `func (vt *ValidationTracker) countTrustedExcludingNegUNL(ledgerVals map[consensus.NodeID]*consensus.Validation) int`.
2. Rewrite `GetTrustedValidationCount` to call it.
3. `IsFullyValidated` now inherits the filter; audit `GetTrustedSupport` call
   sites to confirm no caller depended on unfiltered counts (none in current
   code).

**Verification:**
- Extend `TestValidationTracker_NegativeUNL_ExcludedFromQuorum`: after marking
  a validator negUNL, assert `GetTrustedValidationCount`, `GetTrustedSupport`,
  and `IsFullyValidated` all reflect the exclusion.

---

### R4.7 — TXRR and VPRR collapsed into single `FeatureReduceRelay`

**Files:**
- `internal/peermanagement/handshake.go:335-359, 617-628`
- Wherever `FeatureReduceRelay` is read (currently: `overlay.go:592`)

**Finding (audit C):** Rippled advertises `txrr` (tx reduce-relay) and `vprr`
(validator-proposal reduce-relay) as **independent** feature flags — an
operator may enable one without the other. Go's
`ParseProtocolCtlFeatures` (handshake.go:626) collapses both into a single
`FeatureReduceRelay`. That makes the wire signal coarser and prevents
operator-controlled mixed-feature peers from interoperating correctly.

**Fix:**
1. Split the enum: `FeatureTxReduceRelay` and `FeatureVpReduceRelay`, keep
   `FeatureReduceRelay` as an alias or remove it.
2. Update `ParseProtocolCtlFeatures` to enable them independently.
3. Update `MakeFeaturesRequestHeader` / `MakeFeaturesResponseHeader` (already
   wire-correct — they emit the two features independently).
4. Update `overlay.go:592` to gate TMSquelch on
   `FeatureVpReduceRelay` specifically (validator-proposal squelch is the VPRR
   feature, not TXRR — confirm against `PeerImp.cpp`).
5. Add a `Config` knob or two (EnableTxReduceRelay / EnableVpReduceRelay, both
   already present in `HandshakeConfig`).

**Verification:**
- Extend `TestParseProtocolCtlFeatures` with all four combinations (only txrr,
  only vprr, both, neither); assert the two enum bits are set independently.
- Round-trip test: one peer advertises only txrr, the other advertises only
  vprr → assert `PeerSupports(peerID, FeatureVpReduceRelay)` reflects the
  one-sided negotiation.

Note: this is also a prerequisite for R4.5 option 2 if you pick it (the ingress
gate would need to check vprr specifically).

---

### R4.8 — Replay-delta single-peer 30 s retry vs rippled's 10×250 ms peer-swap

**Files:**
- `internal/ledger/inbound/replay_delta.go:30-36`
- `internal/ledger/inbound/replayer.go` (state machine)

**Finding (audit D):** Go waits 30 s on a single chosen peer before falling
back to legacy catchup. Rippled's `LedgerReplayer.h:49-57` declares:
- `sub_task_retry_time = 250 ms` × 10 retries = 2.5 s peer-swap window
- `fallback_time = 1 s` × 2 = 2 s
- Total ~4.5 s before escalation — **6× faster recovery** than go-xrpl

If the chosen peer silently drops the request (not uncommon on a congested
network), go-xrpl burns 30 s. The round-3 comment argued "recovery is not
time-critical"; the audit notes that's a judgement call, not a mechanical
safety claim. On a flaky network this adds measurable user-facing latency to
catchup.

**Fix:**
1. Add `subTaskRetry = 250 * time.Millisecond`, `subTaskRetryMax = 10` to the
   replay constants.
2. Change `ReplayDelta` state machine to:
   - On first send, arm a 250 ms timer.
   - On timer fire: if not yet received, rotate to a different peer (from
     `overlay.GetPeersSupportingFeature(FeatureLedgerReplay)` minus the peers
     already tried), re-send the request, re-arm timer. Stop after
     `subTaskRetryMax` rotations.
   - On exhaustion, fall back to the legacy catchup path.
3. Extend `replayDeltaTimeout` to cover the full budget (250 ms × 10 + 1 s × 2
   = 4.5 s); actual fallback happens at exhaustion.

**Verification:**
- `TestReplayDelta_PeerSwap_OnSilentPeer` — spawn 3 mock peers, make peer 1
  silent, assert after ~250 ms the request rotates to peer 2 and completes in
  under 500 ms.
- `TestReplayDelta_PeerSwap_ExhaustsThenFallsBack` — all peers silent, assert
  fallback fires after ~4.5 s and not 30 s.

---

### R4.9 — Stale `ledger_provider.go` comment claiming mtGET_LEDGER coverage

**File:** `internal/consensus/adaptor/ledger_provider.go:32-40`

**Finding (audit F):** R3.9 fixed the equivalent stale comment in
`startup.go:79-87`, but the sibling claim in `ledger_provider.go:32-34` was
missed — the compile-time interface assertion's comment still reads "covers
the legacy mtGET_LEDGER path AND the replay/proof-path paths", which is false.

**Fix:** Rewrite the comment at `ledger_provider.go:32-40` to match R3.9's
accurate framing: `LedgerProvider` answers replay/proof-path only; the router's
`handleGetLedger` answers mtGET_LEDGER(LedgerInfoBase) directly from the
ledger service. The adapter exists so peermanagement can reach the ledger
service without violating layering (no direct import of `internal/ledger`).

**Verification:** Docs-only — `go build` and `go vet` pass.

---

### R4.10 — `sfValidatedHash` not gated on `featureHardenedValidations`

**File:** `internal/consensus/rcl/engine.go:1619-1627`

**Finding (audit A):** Rippled emits `sfValidatedHash` **only** when
`featureHardenedValidations` is enabled on the parent ledger's rules
(`RCLConsensus.cpp:853`). Go emits it unconditionally whenever
`GetValidatedLedgerHash()` is non-zero. On mainnet this is invisible
(amendment always on), but a testnet or standalone run against a
HardenedValidations-off network would emit an extra field that peers running
the old rules would reject as malformed.

**Fix:**
1. Add `Adaptor.IsFeatureEnabled(featureName string) bool` if not present.
2. In `engine.go:1625`: gate the copy on
   `e.adaptor.IsFeatureEnabled("HardenedValidations")`. Falls back to "always
   emit" if the adaptor can't read ledger rules (preserves current behavior on
   mainnet).

**Verification:** Unit test: enabled rules → emit; disabled rules → skip.

---

### R4.11 — R3.3 mode-sequence test doesn't cover `OnLedger` branch into SwitchedLedger

**File:** `internal/consensus/rcl/engine_test.go:712-838`

**Finding (audit B4):** R3.3's `TestEngine_WrongLedgerRecovery_ModeSequence`
pins the `handleWrongLedger` → SwitchedLedger path but not the
`OnLedger` branch that also enters SwitchedLedger at `engine.go:474`. A
mutation that breaks one of the two entry points would not fail CI.

**Fix:** Add a second test
`TestEngine_OnLedger_PromotesToSwitchedLedger` that drives an inbound
`OnLedger` call while the engine is in WrongLedger mode, asserts mode
transitions to SwitchedLedger, and asserts the follow-up validation has
`Full=false`.

---

## Phase 3 — Cleanup / polish

### R4.12 — `GetCurrentValidators` uses `time.Now()` instead of the network-adjusted clock

**File:** `internal/consensus/rcl/validations.go:397-411`

**Finding (audit B6 dead-code inconsistency):** R3.6 wired `vt.now` into
`Add` for the isCurrent check, but `GetCurrentValidators` at line 402 still
calls `time.Now()` directly. Currently unused by production code (hence
dead-code), but inconsistent.

**Fix:** `cutoff := vt.now().Add(-vt.freshness)`.

**Verification:** None needed; zero callers in production.

---

## Phase 4 — Parked (follow-up PRs, NOT in round-4 scope)

Each parked item carries a justification for why it's parked, not silence.

### R4.13 — R4.3d: Amendment-vote wiring on validations

**Why parked:** Depends on manifest-chain / ValidatorList port (R3.11), which
is a dedicated milestone. A stubbed `sfAmendments` is actively worse than
omitting the field (makes go-xrpl validators mis-signal "supports nothing"
to amendment-vote accumulators). Track alongside R3.11.

### R4.14 — Router silently drops mtCLUSTER, mtMANIFESTS, mtHAVE_TRANSACTIONS, mtTRANSACTIONS, mtGET_OBJECTS

**Why parked:** Each is a separate feature work item, out of this PR's "p2p
bring-up" scope. mtMANIFESTS is the highest priority of the five and
co-travels with R4.13 (ValidatorList / R3.11). mtCLUSTER is a private-peer
topology feature rarely used outside operator clusters. The rest are
optimizations that peers negotiate — rippled will fall back to the base
protocol on unsupported types.

### R4.15 — mtGET_LEDGER AS_NODE / TX_NODE / fat-node walks

**Why parked:** Low impact because peers negotiating ledgerreplay use the new
replay-delta path exclusively. The fat-node walk matters only for rippled
peers that never negotiate ledgerreplay — a shrinking population. Previously
tracked as R3.12 (deferred); still deferred.

### R4.16 — LedgerReplayTask multi-ledger backward chain walk

**Why parked:** `router.go:562-566` documents as single-ledger only.
SkipListAcquire backward-chain replay is a separate feature — order-of-magnitude
more complex than R3.7's single-ledger replay-delta. Worth its own design doc.

---

## Sequencing

**Recommended order (one commit per item, amended into one PR push):**

1. R4.9 (doc cleanup — zero-risk warm-up)
2. R4.1 (eviction threshold — constant change + comment rewrite)
3. R4.12 (one-line fix)
4. R4.6 (negUNL filter — three method touches)
5. R4.5 (TMSquelch gate — drop or reduce-charge)
6. R4.11 (second mode-sequence test)
7. R4.10 (HardenedValidations gate)
8. R4.7 (TXRR/VPRR split — larger, touches handshake enum)
9. R4.4 (UpdateRelaySlot duplicates — interface change, touches engine + mocks)
10. R4.8 (replay-delta peer-swap — state-machine work)
11. R4.3 (cookie + server-version + fee-vote — three sub-items, largest)
12. R4.2 (integration test — finishes with coverage of everything above)

After each phase: `go test ./internal/consensus/... ./internal/peermanagement/...
./internal/ledger/... ./internal/testing/p2p/...` and
`golangci-lint run ./...`.

---

## Verification checklist (before commit & push)

- [ ] All new tests green
- [ ] `go test ./...` (full suite) green
- [ ] `golangci-lint run ./...` clean
- [ ] Conformance summary unchanged or improved:
      `./scripts/conformance-summary.sh`
- [ ] Manual byte-level check: one captured validation with fee-vote +
      cookie + server-version set is accepted by rippled (use a local
      rippled in standalone mode, or fall back to round-trip through our own
      parser if rippled setup isn't available)
- [ ] Mutation tests for R4.1, R4.2, R4.4, R4.6 (stash the fix, re-run the
      added test, confirm failure — then restore and re-run)
