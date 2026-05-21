# Issue #470 — consensus-engine deliberate divergences from rippled

The issue #470 fix landed two consensus-engine gates whose behaviour does
not have a structural counterpart in rippled. Both are documented at
their call sites and are functionally equivalent to rippled for every
soak scenario observed in iter27/iter28, but the divergences are real
and worth pinning as separate follow-up work.

## 1. Peer-LCL trusted-validation gate (`internal/consensus/rcl/engine.go:511-513`)

**Goxrpl behaviour.** Drops every peer-LCL vote whose hash has zero
trusted validations. The fallback to peer-counted LCLs is silently
disabled until at least one trusted validator backs the hash.

**Rippled behaviour.**
- `rippled/src/xrpld/app/misc/NetworkOPs.cpp:1909-1921` counts peer LCLs
  ungated.
- `rippled/src/xrpld/app/consensus/RCLConsensus.cpp:295-314`
  (`getPrevLedger`) uses trusted-only `getPreferred` at the
  chain-selection boundary.
- The unfiltered peer count is consumed by
  `Validations::getPreferredLCL`, which is documented at NetworkOPs.cpp:
  1909 as "Will rely on peer LCL if no trusted validations exist".

**Failure mode this could mask.** In a partition where every trusted
validator goxrpl knows about becomes unreachable but enough peers are
gossiping a peer-LCL the local node could acquire, rippled would
converge on the peer-counted LCL. With the gate, goxrpl stalls.

**Proposed convergence work.**
1. Refactor goxrpl so peer-LCL counting lives outside the consensus
   engine (mirror rippled's NetworkOPs / `getPreferred` layering).
2. Replace the in-engine gate with the equivalent trusted-only
   `getPreferred`-style selection at the chain-selection boundary.
3. Add a regression case to `internal/consensus/rcl/engine_test.go`
   that drives the isolated-partition scenario described above and
   asserts convergence on the peer-counted LCL.

## 2. `checkConvergence` `ModeWrongLedger` early-return (`internal/consensus/rcl/engine.go:2439-2441`)

**Goxrpl behaviour.** Explicit `if e.mode == consensus.ModeWrongLedger
{ return }` guard at the top of `checkConvergence`.

**Rippled behaviour.** Enforces the same unreachability via the
`result_ == std::nullopt` invariant:

- `rippled/src/xrpld/consensus/Consensus.h:1371` —
  `XRPL_ASSERT(result_, "phaseEstablish : result is set")`.
- `rippled/src/xrpld/consensus/Consensus.h:1686` —
  `XRPL_ASSERT(result_, "haveConsensus : has result")`.
- `rippled/src/xrpld/consensus/Consensus.h:1437` —
  `XRPL_ASSERT(!result_, "closeLedger : result is not set")`.
- `rippled/src/xrpld/consensus/Consensus.h:704,713` —
  `startRoundInternal` resets `phase_` and calls `result_.reset()`.

**Proposed convergence work.**
1. Introduce a `result_`-equivalent optional state on goxrpl's engine
   (or refactor the engine so phase transitions carry an
   `std::optional`-shaped `Result`).
2. Add the same XRPL_ASSERT-style invariant checks goxrpl uses for
   other state machines.
3. Once the invariant holds, drop the explicit `ModeWrongLedger`
   guard at `checkConvergence`.

## Why these stay deliberate divergences for now

Both gates were added under the issue #470 soak deadline to break
specific stalls (iter27 L34, iter28 L38). The structural refactor is
non-trivial — it touches the engine's phase-transition state machine
and the validation tracker — and out of scope for the LedgerHashes-
focused fix.

If a future PR observes a new failure mode that one of these gates
masks (e.g. the isolated-partition stall described above), it should
take the convergence work as part of the fix rather than layering
another gate on top.
