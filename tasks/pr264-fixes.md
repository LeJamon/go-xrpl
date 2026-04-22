# PR #264 Feedback — Fix Plan

Addressing the review on `feature/p2p-todos`. Organized blocking → important → nits, with trivial fixes pulled forward within each tier so they can land independently.

Rule of engagement for each item:
- **File:line pointers** so the change is unambiguous.
- **Rippled anchor** so the fix is validated against the reference, not invented.
- **Verify** column lists the concrete proof the fix works (test, diff, observed behavior).
- **Depends on** flags cross-item ordering.

---

## Phase 1 — Blocking (must all ship together)

These items jointly define "correct consensus behavior" — a partial fix set is worse than the current state because it hides the remaining divergences.

### P1.1 Round-duration wall-clock fix — complete it
**Trivial. Do first.**
- File: `internal/consensus/rcl/engine.go:186`
- Bug: `StartTime: e.adaptor.Now()` captures offset-adjusted time; `shouldCloseLedger` at `engine.go:873` reads via `time.Since(e.state.StartTime)` (wall-clock subtract). Exact same class as commit-1's `roundStartTime` fix.
- Fix: change `StartTime: e.adaptor.Now()` → `StartTime: time.Now()`. Leave `PhaseStart` as adaptor.Now (it's consumed via `adaptor.Now().Sub(...)` at :1037).
- Verify: add a unit test that sets `closeOffset = 10s` on the adaptor, runs one round, and asserts `shouldCloseLedger`'s `openTime` stays within ±100ms of wall clock.

### P1.2 Stop charging peer for engine-divergence on replay-apply failure
**Trivial.**
- File: `internal/consensus/adaptor/router.go:576`
- Bug: `IncPeerBadData(peerID, "replay-delta-apply")` fires when OUR engine's Apply re-derives a different AccountHash than the peer-verified header. GotResponse already verified the peer's bytes; a failure here is an engine bug, not peer misbehavior.
- Rippled anchor: `LedgerDeltaAcquire.cpp:211-223` silently bails on state-map divergence without charging.
- Fix: remove the `IncPeerBadData` call at :576. Escalate the log line from warn → error and include an "ENGINE DIVERGENCE" marker so it's triage-obvious. Keep `IncPeerBadData` at :519 (decode) and :550 (verify) — those ARE peer-attributable.
- Verify: `TestRouter_ReplayDeltaApplyFailure_DoesNotChargePeer` — inject a doctored replay that passes GotResponse but fails Apply; assert `IncPeerBadData` was not called.

### P1.3 Split self-originated from relayed broadcast
**Trivial.**
- Files: `internal/consensus/adaptor/sender.go:22-44`, `internal/peermanagement/overlay.go:881-912`
- Bug: `BroadcastProposal`/`BroadcastValidation` for our own traffic route through `BroadcastFromValidator(ourKey, ...)`, so a peer that squelches our own pubkey silences us to them. `RelayProposal` is aliased to `BroadcastProposal` — no distinction between origin and forward.
- Rippled anchor: `OverlayImpl.cpp:1133-1137, 1158-1163` — self-originated broadcasts skip the squelch filter; only gossip forwards apply it.
- Fix:
  - Rename `Overlay.BroadcastFromValidator` to `Overlay.RelayFromValidator` (unchanged semantics).
  - Use plain `Overlay.Broadcast` for our own `BroadcastProposal`/`BroadcastValidation` in sender.go.
  - Wire `RelayProposal`/`RelayValidation` (new) to `RelayFromValidator` with the ORIGINATING peer's ID excluded. Needs a new `Relay(exceptPeer PeerID, validator []byte, msg []byte)` to exclude one peer.
  - Engine's `OnValidation` (engine.go:333-364) needs a `RelayValidation` call symmetric to the one `OnProposal` already makes.
- Depends on: P1.4 (needs originator peerID plumbed through).
- Verify: Kurtosis test where peer A squelches our pubkey, confirm our proposals still reach B/C; a peer-relayed proposal from A to us does NOT get re-sent to A.

### P1.4 Plumb originator peerID through engine to enable P1.3 exclusion and P2.7 gossip
**Small surface change.**
- Files: `internal/consensus/types.go`, `internal/consensus/rcl/engine.go` (OnProposal/OnValidation signatures), `internal/consensus/adaptor/router.go` (handleProposal/handleValidation)
- Bug: `engine.OnProposal(*Proposal)` doesn't know who sent it; relay can't exclude the origin.
- Fix: extend the signatures to `OnProposal(proposal, originPeerID)` and `OnValidation(validation, originPeerID)`. Default `0` means "self-originated" (no exclusion). Adaptor interface gains a `RelayProposalFromPeer(proposal, exceptPeer)` method.
- Verify: unit test asserting the origin peer is excluded from the outbound slice when we relay.

### P1.5 Drive the reduce-relay slot from inbound consensus traffic
- Files: `internal/consensus/adaptor/router.go:146-181` (handleProposal, handleValidation), `internal/consensus/adaptor/adaptor.go`, `internal/peermanagement/overlay.go`
- Bug: `Relay.OnMessage` is called only from tests (`relay_test.go:89,114,138,164`); production `handleProposal`/`handleValidation` skip it, so the squelch selection logic never fires and `mtSQUELCH` is never emitted in production.
- Rippled anchor: `PeerImp.cpp:1737, 2385, 3013, 3049` — `updateSlotAndSquelch` is called on every inbound `TMProposeSet`/`TMValidation`.
- Fix:
  - Add `Adaptor.UpdateRelaySlot(validatorKey, peerID)` and plumb it through `NetworkSender` → `OverlaySender` → `Overlay.relay.OnMessage`.
  - Call `UpdateRelaySlot(proposal.NodeID[:], originPeerID)` at the end of `handleProposal`; same for `handleValidation`.
  - Remove the `IssueSquelch` shim exposure or keep it test-only under a build tag.
- Depends on: P1.4.
- Verify: two-overlay test where peer A floods proposals for validator V; assert that after `MaxMessageThreshold`, B receives `mtSQUELCH(V)` via the natural path (not via `IssueSquelch`).

### P1.6 Unify ledger-acquisition dedup — eliminate cross-path race
- Files: `internal/consensus/adaptor/router.go:414-506`
- Bug: `startLedgerAcquisition` checks `replayer.Has(hash)`; `startLedgerAcquisitionLegacy` checks `r.inboundLedger != nil`. Two status changes at the same seq with different hashes can arm both paths, last adoption wins.
- Rippled anchor: `InboundLedgers::find` dedupes across both replay-delta and legacy paths.
- Fix:
  - Promote the in-flight registry to a single hash-keyed map owned by the Router: `type acquisition interface { Hash() [32]byte; IsTimedOut() bool; Cancel() }`.
  - Both `ReplayDelta` and `inbound.Ledger` implement `acquisition`. A single `r.active map[[32]byte]acquisition` replaces the current replayer.Has + inboundLedger pair.
  - `startLedgerAcquisition` becomes: "if hash in active, return; else decide replay-delta vs legacy and register in active".
  - `completeInboundLedger`/`adoptVerifiedLedger` both remove from `active` by hash.
- Verify: `TestRouter_ConcurrentStatusChanges_NoRace` — fire two statusChange messages with different hashes at the same seq, assert only one acquisition arms per hash, both complete cleanly.

### P1.7 Restore ModeSwitchedLedger semantics
- Files: `internal/consensus/rcl/engine.go` (handleWrongLedger, OnLedger, closeLedger, sendValidation, acceptLedger), `internal/consensus/types.go` (Mode constants already exist)
- Bug: After `handleWrongLedger`/`OnLedger`, we call `startRoundLocked(..., proposing=true)` for a validator, re-entering as `ModeProposing`. Rippled deliberately uses `ConsensusMode::switchedLedger` for that round — `Consensus.h:1457` suppresses `adaptor_.propose()` when mode != proposing.
- Rippled anchor: `Consensus.h:1107` sets switchedLedger post-recovery; `Consensus.h:1457` gates propose on mode==proposing; `ConsensusTypes.h:64-68` documents the mode.
- Fix:
  - In `handleWrongLedger` (:736) and `OnLedger` (:391), call `startRoundLocked` with a NEW parameter `recovering=true`. Inside `startRoundLocked`, when recovering AND validator+Full, set `mode = ModeSwitchedLedger` instead of `ModeProposing`.
  - `closeLedger()` at :947: keep the existing `if e.mode == ModeProposing` gate — that's already correct; it'll now correctly skip proposing during switchedLedger rounds.
  - `acceptLedger()` at :1236: change `if e.adaptor.IsValidator()` → `if e.mode == ModeProposing` so we suppress our validation on the switchedLedger round.
  - On the NEXT `startRoundLocked` (post-accept), promote back to ModeProposing normally — switchedLedger is a one-round suppression.
- Verify:
  - Unit test: drive engine through `handleWrongLedger`, assert mode==ModeSwitchedLedger, run closeLedger, assert `BroadcastProposal` was NOT called; run acceptLedger, assert `BroadcastValidation` was NOT called.
  - Unit test: the round AFTER a switchedLedger recovery promotes back to ModeProposing for a validator.
  - Kurtosis: force a late-join goXRPL node, grep logs for "switchedLedger" and confirm it doesn't emit validations for the recovery round.

### P1.8 Replace full-validation gate in `checkLedger` with validation-weighted preference
- Files: `internal/consensus/rcl/engine.go:591-629`, `internal/consensus/rcl/validations.go` (add helper)
- Bug: `checkLedger` at :613 refuses to switch unless the target is already fully validated, which can strand a catch-up node on the wrong fork.
- Rippled anchor: `RCLConsensus.cpp:300-303` uses `vals.getPreferred()` — LedgerTrie picks the ledger with the most validation SUPPORT, not necessarily quorum.
- Fix (minimum viable, no full LedgerTrie):
  - Add `ValidationTracker.GetTrustedSupport(ledgerID) int` returning the trusted-validation count for that ledger.
  - In `checkLedger`, replace `!IsFullyValidated(netLgr)` with "switch if trustedSupport(netLgr) > trustedSupport(ourID) OR trustedSupport(netLgr) >= 1 AND peer vote majority". The simplest rule that avoids stranding.
  - Document that this is a support-heuristic, not the full trie — file a follow-up issue to port rippled's LedgerTrie if interop testing reveals edge cases.
- Verify: unit test where 2-of-3 trusted validators validate the peer branch; goXRPL starts on a stale branch and successfully switches without waiting for quorum.

---

## Phase 2 — Important (stage after Phase 1; independent of each other)

### P2.1 `maintenanceTick` must reap stuck legacy acquisitions
- File: `internal/consensus/adaptor/router.go:109-119`
- Fix: after the replayer loop, add `if r.inboundLedger != nil && r.inboundLedger.IsTimedOut() { r.logger.Warn(...); r.inboundLedger = nil }`. Or if P1.6 unified the registry, iterate the single `active` map instead.
- Depends on: P1.6 (simpler if shared registry already exists).
- Verify: unit test with an injected clock; assert stuck legacy acquisition is cleared after its timeout.

### P2.2 Gate ingress replay-delta / proof-path requests on handshake feature
- File: `internal/peermanagement/overlay.go:479-493`
- Rippled anchor: `PeerImp.cpp:1473-1478` charges `feeMalformedRequest` and drops when the peer hasn't negotiated ledger-replay.
- Fix: before calling `dispatchReplayDeltaRequest`/`dispatchProofPathRequest`, check `peer.Capabilities().HasFeature(FeatureLedgerReplay)`; if absent, call `IncPeerBadData(evt.PeerID, "replay-req-unnegotiated")` and drop.
- Verify: unit test where a peer that did NOT advertise `ledgerreplay` sends `mtREPLAY_DELTA_REQ`; assert it's charged and no response is sent.

### P2.3 Fix oversize-response error code — reBAD_REQUEST → reNO_LEDGER
- File: `internal/peermanagement/ledgersync.go:469-481`
- Bug: returning `ReplyErrorBadRequest` makes the requester take a `feeMalformedRequest` (200) hit on rippled side (`PeerImp.cpp:1545-1548`). Our 16MB cap isn't a malformed request.
- Fix: change to `ReplyErrorNoLedger` (gets `feeRequestNoReply`, 10 drops — much lighter). Update the comment block above the const to reflect the semantic choice.
- Verify: update the relevant test (`TestHandleReplayDeltaRequest_OversizedResponse` or similar) to assert the new error code.

### P2.4 Fix `replayDeltaTimeout` comment + adjust value
- File: `internal/ledger/inbound/replay_delta.go:24-28`
- Bug: "Mirrors rippled's PeerSet timeout for inbound ledger requests (~30s)" is false. Rippled's `SUB_TASK_TIMEOUT × SUB_TASK_MAX_TIMEOUTS = 250ms × 10 = 2.5s`.
- Fix: two options — pick one and document it:
  - **(A) Match rippled:** drop to `2500 * time.Millisecond`, drop `inboundReplayDeltaTickInterval` to `500 * time.Millisecond` so stuck acquisitions are detected within a tick.
  - **(B) Keep relaxed:** leave 30s but rewrite comment to say "relaxed from rippled's 2.5s because our recovery is not time-critical — a late reply is still useful".
- Recommend (A) because it keeps fallback responsive; (B) is acceptable if (A) causes flaky Kurtosis.
- Verify: if (A), existing replayer timeout tests need updated timing; if (B), just re-read the comment.

### P2.5 ValidationTracker filters + dynamic trusted set
- Files: `internal/consensus/rcl/validations.go:80-107` (Add), `internal/consensus/rcl/engine.go` (reload wiring)
- Bug: no Full=true filter, no seq sanity check, no negative-UNL filter, trusted set is static after `Start()`.
- Rippled anchor: `LedgerMaster.cpp:886,952` filters by `full()` and negUNL.
- Fix:
  - In `Add()` at validations.go:80: early-return false when `!validation.Full`.
  - In `Add()`: reject validations whose `LedgerSeq < currentLCLSeq - N` (N=256 or config-driven) so far-stale validations don't inflate counts.
  - Add `ValidationTracker.SetNegativeUNL([]NodeID)` and filter those from trusted count in `checkFullValidation`.
  - Expose `Engine.ReloadTrustedValidators()` wired to amendment/UNL changes via an event subscriber. Call it from `acceptLedger` after consensus when the flag ledger rolls over.
- Verify: unit tests for each filter; one assertion per filter branch.

### P2.6 STValidation: add sfValidatedHash, sfAmendments, fee-voting on flag ledgers
- Files: `internal/consensus/adaptor/stvalidation.go:184-258` (serializer), `internal/consensus/adaptor/identity.go` (sign data must match), `internal/consensus/types.go` (Validation struct)
- Rippled anchor: `RCLConsensus.cpp:853-894`.
- Fix:
  - Add `ValidatedHash [32]byte`, `Amendments [][32]byte` to `consensus.Validation`.
  - Populate `ValidatedHash` from the adaptor's current validated-ledger pointer at sign time.
  - On flag ledgers (`seq % 256 == 0`), populate `Amendments` from `amendment.EnabledAmendments(prevLedger)` and `LoadFee` from the fee tracker.
  - Emit sfValidatedHash (field 25, type Hash256) when non-zero; emit sfAmendments (field type Vector256 — check field code against rippled's SField.cpp).
  - Update `buildValidationSigningData` to match byte-for-byte.
  - Update parser to extract the new fields.
- Verify: round-trip test (sign → serialize → parse → verify); feed a rippled-originated flag-ledger validation (captured pcap or test fixture) and assert we parse its amendments correctly.

### P2.7 Make RelayProposal / add RelayValidation actually relay-forward
- Files: `internal/consensus/adaptor/sender.go`, `internal/consensus/rcl/engine.go:333-364`
- Bug: `RelayProposal` is aliased to `BroadcastProposal`; `OnValidation` never re-broadcasts.
- Fix:
  - Split into `BroadcastOwn*` (unfiltered) and `Relay*` (filtered, excludes origin).
  - `OnValidation` should call `RelayValidation(v, originPeerID)` when `trusted == true` (mirrors the existing `OnProposal` pattern).
- Depends on: P1.3, P1.4.
- Verify: three-peer Kurtosis setup; assert a validation emitted at peer A reaches peer C via peer B's relay.

### P2.8 Clean peerLCLs on peer disconnect; dedupe self-vote
- Files: `internal/consensus/adaptor/adaptor.go:159-183`, `internal/consensus/adaptor/router.go` (new disconnect handler), `internal/peermanagement/overlay.go:445-448` (emit disconnect event richer)
- Bugs:
  - `peerLCLs` only grows; a stale disconnected peer keeps voting for its last-reported LCL.
  - A trusted validator connected as a peer contributes both a proposal vote AND a peerLCL synthetic vote → double-counted in `getNetworkLedger`.
- Fix:
  - Add `Adaptor.RemovePeerLCL(peerID)`, and a peer-disconnect callback on the overlay that the router forwards.
  - In `getNetworkLedger` (engine.go:649-705), skip peerLCL entries whose ledger ID is already voted for by a trusted proposer in `votes`. Simplest: build the proposal-vote set first, then only count a peerLCL if its hash isn't already a vote.
- Verify: unit test — add a peerLCL, disconnect the peer, assert it's not returned by `PeerReportedLedgers()`. Second test: a peer whose trusted proposal matches its peerLCL counts once, not twice.

### P2.9 Apply ShouldRetry: run a second pass OR clarify comment
- File: `internal/ledger/inbound/replay_delta.go:486-489`
- Rippled anchor: `BuildLedger.cpp:111-170` runs a retry pass.
- Decision: replay input is canonical, so ShouldRetry shouldn't fire in practice. Two options:
  - **(A) Trust the invariant:** rewrite the error message to say "ShouldRetry during replay means engine divergence — the txs were canonical when first applied".
  - **(B) Match rippled:** add a retry pass: collect ShouldRetry txs, loop once more, fail only if retry also yields ShouldRetry.
- Recommend (A) — it's documentation, no behavior change, and the branch is genuinely unreachable on good input.
- Verify: none needed for (A); (B) needs a test harness creating deliberately sequence-conflicting txs.

### P2.10 Bad-data counter: add time decay + per-reason weights
- File: `internal/peermanagement/overlay.go:20-82`, `internal/peermanagement/peer.go` (where BadDataCount lives)
- Rippled anchor: `libxrpl/resource/Fees.cpp:26-43`.
- Fix (minimum viable):
  - Change `BadDataCount uint32` to a `badDataBalance int64` (signed).
  - Each `IncBadData(reason)` adds a weight from a table: `feeInvalidData=400`, `feeMalformedRequest=200`, `feeRequestNoReply=10`.
  - A 1s decay ticker in `maintenanceLoop` halves the balance per 5s (rippled's rough decay cadence).
  - Eviction threshold: `balance > 1000` (approximates rippled's drop threshold at load).
- Verify: unit test with `EvictionTest_WeightedReasons_DecayOverTime` — a peer charged 10× feeRequestNoReply (100) does not evict; a peer charged 3× feeInvalidData (1200) evicts immediately.

---

## Phase 3 — Nits (low-urgency quality-of-life)

### P3.1 `extractTransactionIndex` — use SerialIter-style skip instead of full decode
- File: `internal/ledger/inbound/replay_delta.go:635-661`
- Fix: use `serdes.NewBinaryParser(metaBytes, nil)`; loop reading field headers with `readFieldHeader` + `skipFieldData` (already implemented in stvalidation.go:293-330 — extract into a shared utility), stop when `(type=UINT32, field=TransactionIndex)` is matched.
- Verify: existing replay-delta tests; add a bench to confirm the O(n²) → O(n) speedup for large meta blobs.

### P3.2 Update `DefaultMaxInFlightReplays` comment
- File: `internal/ledger/inbound/replayer.go:17-20`
- Fix: drop the "Matches rippled's informal ceiling" claim. Rewrite as: "16 accommodates a catchup burst without monopolizing a peer; rippled's MAX_TASKS=10 is for full LedgerReplayTask not sub-acquisitions, so no direct parity".

### P3.3 `DeletePeer`: reset Count=0 and LastMessage=now on the deleted peer
- File: `internal/peermanagement/relay.go:180-213`
- Rippled anchor: `Slot.h:479-480`.
- Fix: at the end of `DeletePeer` (before the final `if erase`), add `peer.Count = 0; peer.LastMessage = now` — these lines in rippled run regardless of whether the peer was Selected.

### P3.4 Populate or remove unused PeerCapabilities fields
- File: `internal/peermanagement/handshake.go:443-478`, `internal/peermanagement/overlay.go:370-378`
- Bug: `ProtocolMajor`, `ProtocolMinor`, `NetworkID`, `ListeningPort`, `SupportsCrawl`, `IsValidator` are declared but never set.
- Fix: parse them from the handshake response headers (`Upgrade`, `Network-ID`, `Server`, `Crawl`) — the values ARE in the HTTP response; we just never read them. Where a header is absent, leave the field at its zero value.
- Verify: handshake tests (existing) extend to assert non-zero population.

### P3.5 Replace best-effort channel sends with counter metrics
- Files: `internal/peermanagement/ledgersync.go:410-415,518-523`, `internal/peermanagement/overlay.go:506-514`
- Fix: expose a `droppedMessages atomic.Uint64` on the overlay; increment on each `default:` drop. Surface via `server_info`/`server_state` (whichever is already wired) so operators can detect back-pressure.

### P3.6 `TestReplayDelta_Apply_OrderedByIndex` — replace string-prefix match with sentinel errors
- File: `internal/ledger/inbound/replay_delta_apply_test.go:84-120`
- Fix: use `errors.Is(err, ErrApplyFailed)` against a typed sentinel rather than `strings.HasPrefix(err.Error(), "tx ...")`. Requires defining a sentinel pair (e.g., `ErrReplayDivergedFromRippled`, `ErrReplayRetryNotAllowed`) and wrapping them where the error is formatted.

### P3.7 pendingValidation LRU drop — log + metric
- File: `internal/ledger/service/service.go:1206-1227`
- Fix: at the LRU-drop line (:1225), add a `slog.Warn("pendingValidation LRU drop — event lost for ledger", "hash", ...)` and increment a counter exposed on `server_info`. Don't change the drop semantics — the cap is there for a reason.

---

## Landing strategy

- **Branch layout:** land Phase 1 as a single stacked PR (items are interdependent); Phase 2 items can each be their own smaller PR; Phase 3 collectively can be one "polish" PR.
- **Commit discipline:** one commit per numbered item, prefix `p1/`, `p2/`, or `p3/` followed by the subsystem (e.g., `p1/engine: restore switchedLedger semantics`). Keeps `git log --oneline` scannable against this plan.
- **Regression fence:** Phase 1 exit criterion is a 3-node Kurtosis run (2 rippled + 1 goXRPL) that:
  - validated_ledger advances in lockstep for ≥ 20 ledgers
  - forced disconnect + reconnect of the goXRPL node recovers without duplicating validations
  - no "replay-delta-apply" IncPeerBadData entries appear in overlay logs
  - `mtSQUELCH` is observed in a pcap after sustained gossip (ran via `tcpdump -i any port 51235`)
- **Out of scope** (will be filed as follow-ups, NOT in this series): full LedgerTrie port, full rippled Resource::Consumer port, WebSocket `path_find` subscription, amendment voting correctness under contested votes.

---

## Open questions (park these — not blockers for this plan)

- **Q1:** For P1.8, is a support-heuristic enough to pass 2-of-3 Byzantine tests, or does interop require the full LedgerTrie? Resolve by running the fix against a network where one peer lies about its LCL; if the heuristic strands, port the trie.
- **Q2:** For P2.4, is 2.5s too aggressive given our 5s Kurtosis RTT baseline? Resolve empirically — start with (A) and flip to (B) only if flakes appear.
- **Q3:** For P2.6, sfAmendments on our validations only matters if we ever vote for/against amendments. Given we track the amendment set but don't expose voting config yet, do we emit the CURRENT set (signaling approval of everything enabled) or emit nothing until voting config exists? Decision: emit the currently-enabled set — matches rippled's default when `[amendments]` is empty in config.
