# Issue #418 — bootstrap OpMode auto-promote

## The bug

Fresh genesis bootstrap deadlock: a goxrpl node cannot reach `OpModeFull` from a clean network start. The engine gates ALL phase work on Full (engine.go:1042-1045), and `Adaptor.OnConsensusReached` has no auto-promote — rippled's `endConsensus` (NetworkOPs.cpp:2197-2213) does. Networks "leak" forward only via a fragile acquire-from-peer race; under fuzz timing variance this wedges nodes at low seq numbers.

## Two-part fix

### Part A — engine.go: allow observer-mode round advancement

- [x] Removed `engine.go:1042-1045` early-return on non-Full; replaced with rippled-mirror comment
  - Mode-degradation already handled by `startRoundLocked` (line 419) — non-Full rounds enter `ModeObserving`
  - Proposal broadcast already gated on `e.mode == ModeProposing` in `closeLedger` (line 1627)
  - Validation `Full` flag already gated on `e.mode == ModeProposing` in `sendValidation` (line 2715)

### Part B — adaptor.go: auto-promote in `OnConsensusReached`

- [x] Added `maybePromoteAfterConsensus(ledger)` helper mirroring NetworkOPs.cpp:2197-2213
- [x] Wired from `OnConsensusReached` after the existing log + hook
- [x] Test `TestOnConsensusReached_AutoPromote` pins all 5 transition cases

## Verification

- [x] Build clean (`go build ./cmd/xrpld`)
- [x] `./internal/consensus/...` all green
- [x] `./internal/testing/consensus/...` green (openledger_convergence_test included)
- [x] `./internal/ledger/...` + `./internal/txq/...` green
- [ ] Clean-soak 3r+2g via xrpl-confluence (no fuzz) → 50+ ledgers byte-identical across all 5 nodes
- [ ] Soak with fuzz on → 50+ ledgers; divergence only from tx-engine bugs

## Why this is the right fix (rippled mirror)

Rippled bootstrap sequence:
1. DISCONNECTED → CONNECTED (heartbeat sees `numPeers >= minPeerCount`)
2. `timerEntry` advances consensus as **observer** (no proposal/validation emission)
3. First round closes (timeout / empty positions) → `acceptLedger` → `endConsensus` auto-promote → TRACKING/FULL
4. Next round: validators propose normally

goxrpl now mirrors this exactly.

## Review section

TBD — filled after implementation + verification.
