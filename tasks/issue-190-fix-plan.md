# Issue #190 — Ledger hash divergence vs rippled:2.6.2

## Status

Investigation complete. No production code change in this branch — the
remaining work splits into three concrete fixes that warrant separate
review (one is large enough to be its own design discussion).

`internal/ledger/genesis/divergence_diag_test.go` is added in this
branch as a diagnostic harness (passing, no production assertions).

## Reported symptom recap

Mixed kurtosis network (4× rippled:2.6.2 + 1× goXRPL). At empty seq 5:

|             | rippled                      | goXRPL                       |
|-------------|------------------------------|------------------------------|
| close_time  | 827228421                    | 827228430 (Δ 9s)             |
| account_hash| `72CF95AC7F0EB1B88EF2BA…`    | `3791BF543E5B77A17BC454…`    |
| ledger_hash | `FC22EB5D…`                  | `C04D4B2F…`                  |
| op mode     | proposing                    | full (never proposing)       |
| tx prop     | working                      | goXRPL→rippled silent        |

## Findings

### F1 — Mode-manager wiring is dead code (real bug, highest priority)

`ModeManager.OnLCLAcquired` (`internal/consensus/adaptor/modemanager.go:93`)
and `OnValidationsReceived` (`:104`) are defined but **never called from
production code** — `grep -rn` shows hits only in `*_test.go`. The state
machine is therefore stuck at `OpModeConnected` / `OpModeSyncing` after
startup.

The router bypasses ModeManager and force-sets
`SetOperatingMode(OpModeFull)` directly at
`internal/consensus/adaptor/router.go:1071`, but only from
`OpModeTracking` — which is itself only reached via
`router.go:1025,1309`. Net result: the only way to reach `OpModeFull`
in production is the router's force-set path, and the path to it is
shaky.

`Engine.startRoundLocked`
(`internal/consensus/rcl/engine.go:374-386`) requires
`IsValidator() && GetOperatingMode() == OpModeFull` to enter
`ModeProposing`. Validator wiring works:
`appCfg.ValidationSeed != ""` (set by kurtosis at
`xrpl-confluence/src/topology.star:214`) →
`IsValidator() == true` (`adaptor.go:616-618`). So the missing piece
**is** the operating-mode never legitimately reaching Full.

Additionally, `ourLCLMatchesPeers()` (`router.go:1132`) returns `true`
when no peer reports our seq — meaning once the router *does* promote
to Full, it will silently fork if the network has moved past us.

**Why this drives the bug:** without proposing mode, goXRPL
- never originates proposals → its tx submissions are not relayed to
  rippled (explains `goXRPL → rippled` propagation symptom);
- runs consensus rounds in Observing/non-validating mode where its
  own `acceptLedger` computes a local `effCloseTime` from its own
  clock and its own (mostly empty) proposal pool, instead of inheriting
  the close_time from the validated ledger rippled produced — explains
  the 9-second drift, exactly within one 10s resolution bucket.

**Scope:** ~50 LOC across `adaptor/router.go`, the validation
tracker, and the inbound-ledger acquisition path. Behavioural change;
needs a careful test plan covering chain-switch and slow-start.

### F2 — Genesis amendment-set drift between goXRPL and rippled (real, config-shaped)

The reported `account_hash 3791BF54…` exactly matches the goXRPL
unit test `TestGenesisHashConformance/StandardDefaults_WithAmendments`
(`internal/ledger/genesis/genesis_test.go:307`). That means the
deployed goXRPL was using `DefaultYesFeatures()` — 28 amendments
listed in the diagnostic test output of `TestDivergence_PrintAmendmentList`.

Meanwhile, `xrpl-confluence/src/topology.star:205` sets
`genesis_amendments_disabled=true` for goXRPL nodes specifically, but
the rippled sidecar at `topology.star:130-160` does **not** set the
equivalent rippled flag — it runs with `START_UP=FRESH` (default) and
seeds genesis with all `Supported && DefaultYes` amendments via
`getDesired()`. The two genesis ledgers are therefore byte-different
**by configuration**, not by code.

Two ways to close this:

- Align kurtosis topology so both sides enable the same amendment set
  (and verify `DefaultYesFeatures()` exactly matches rippled 2.6.2's
  `getDesired()` output ID-for-ID), OR
- Have goXRPL refuse to synthesize genesis when joining an existing
  network (see F3) — moot, because then it would adopt rippled's.

Diagnostic harness (already in this branch):
`go test -run TestDivergence -v ./internal/ledger/genesis/` prints the
goXRPL `account_hash`, `ledger_hash`, FeeSettings/AccountRoot SLE
bytes, and the 28-entry default amendment list for direct comparison
against any rippled snapshot.

### F3 — Genesis is always synthesized, never acquired (architectural gap)

`internal/ledger/service/service.go:248` calls `genesis.Create(...)`
unconditionally on `Service.Start()`. Rippled only does this when
`START_UP == FRESH` (`rippled/src/xrpld/app/main/Application.cpp:1707-1712`);
joining nodes acquire the chain head from peers via the
inbound-ledger / replay path.

Even when genesis bytes happen to match (F2 resolved), this is a latent
correctness hazard: any non-validator joining via the network must
adopt the network's chain, not synthesize its own and assert it as the
LCL.

**Scope:** ~150 LOC touching service.go startup, plus the inbound
ledger acquisition path. Likely a `Config.SkipGenesisCreation` flag
gated on bootstrap-peers + persistent-store presence, mirroring
rippled's `START_UP=NETWORK` semantics.

### F4 — Standalone-mode close_time is unrounded (small, peripheral)

`internal/ledger/service/service.go:424` (`AcceptLedger`, standalone)
sets `closeTime := time.Now()` and passes it through `Ledger.NewOpen`
+ `Ledger.Close` unrounded. Rippled's standalone path (`Ledger.cpp:296-304`)
applies a genesis-aware rounding (round when `prev.closeTime == 0`,
else `prev + resolution`).

This does **not** explain the kurtosis bug — that scenario hits the
consensus path, where `Engine.acceptLedger`
(`internal/consensus/rcl/engine.go:1832`) **does** apply
`effCloseTime(rawCloseTime, resolution, priorClose)` before calling
`adaptor.BuildLedger`, which then forwards to
`service.AcceptConsensusResult`. The agent's initial reading missed
this — the consensus path is rounded.

Real, but a unit-test-quality bug rather than a #190 driver.
**Scope:** ~10 LOC in `service.AcceptLedger` + a regression test.

## Recommended order

1. **F1 — mode-manager wiring** (the root cause of "stuck in full" and
   tx propagation breakage; very likely also explains the close_time
   drift via local-vs-network consensus). Requires a separate PR with
   a test plan covering the LCL-acquired transition and the
   `ourLCLMatchesPeers` quorum tightening.
2. **F2 — amendment-set parity** (configuration audit + a vendored
   "rippled 2.6.2 expected genesis" fixture/test).
3. **F4 — standalone close_time rounding** (small, can ride a sweep
   PR).
4. **F3 — genesis acquisition for non-FRESH startups** (largest;
   architectural; do last and behind a feature flag).

## What this branch contains

- `internal/ledger/genesis/divergence_diag_test.go` — passing
  diagnostic harness, no production code changed.
- `tasks/issue-190-fix-plan.md` — this document.

## Files of interest

- `internal/ledger/service/service.go:248,424,1130`
- `internal/ledger/ledger.go:125,485-534`
- `internal/consensus/adaptor/adaptor.go:282,499-518,616`
- `internal/consensus/adaptor/modemanager.go:36,91-110`
- `internal/consensus/adaptor/router.go:1025-1137,1309`
- `internal/consensus/rcl/engine.go:374-386,1822-1948,2319-2351`
- `internal/cli/server.go:174-180`
- `internal/ledger/genesis/genesis.go:108-117,189-308`
- rippled `src/xrpld/app/ledger/Ledger.cpp:168-229,277-305`
- rippled `src/xrpld/app/main/Application.cpp:1704-1724`
- rippled `src/xrpld/app/consensus/RCLConsensus.cpp:481-495`
- rippled `src/xrpld/consensus/LedgerTiming.h:131-169`
- xrpl-confluence `src/topology.star:141,205,214`
