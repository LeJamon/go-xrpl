# PR #264 Round-6b Fix Plan — Audit follow-ups (replay-delta + negUNL + cleanups)

**Branch:** `feature/p2p-todos` @ `dc3a608`
**Scope:** 5 items deferred from the final audit: D9 (negUNL on flag-ledger replay), D8 (replay-delta meta handling), A13 (dynamic quorum), A6 (type codes), cleanups (doc comment, sfLoadFee, sfCloseTime).

Outbound handshake verification (C2) remains in a separate PR per the user's request.

---

## Phase 1 — Blocker: D9 flag-ledger replay calls `updateNegativeUNL`

**File:** `internal/ledger/ledger.go:466-515` (Close method), `internal/ledger/inbound/replay_delta.go:627-635` (Apply)

**Verified state:** rippled's `BuildLedger.cpp:50-53`:
```cpp
if (built->isFlagLedger() && built->rules().enabled(featureNegativeUNL))
{
    built->updateNegativeUNL();
}
```
Fires BEFORE `applyTxs` runs. go-xrpl's `ledger.Ledger.Close` has no equivalent. On networks with `featureNegativeUNL` enabled, every 256th ledger's replay-delta path will produce an incorrect ledger hash (because the negUNL SLE should have been updated as part of flag-ledger processing), fail the final hash check, and fall back to legacy catchup. Not a safety issue but a reliable catchup regression.

**Fix:**
1. Add `UpdateNegativeUNL()` method to `Ledger` that reads `NegativeUNL` SLE, processes the `NegativeUNLEntries` (expire DisabledValidators past the ToDisable seq, promote ToReEnable / ToDisable), and writes the mutated SLE back. Mirror `Ledger::updateNegativeUNL` at `Ledger.cpp:277-354`.
2. In `replay_delta.go::Apply`, before applying txs: check if the child ledger is a flag ledger (seq%256==0) AND has `featureNegativeUNL` in its rules; if so, call `child.UpdateNegativeUNL()`.
3. Same call should exist in the normal consensus close path — if it's already there we just need to mirror; if not, D9 is wider than replay-delta.

**Verification:**
- New test `TestReplay_FlagLedger_NegativeUNLUpdate`: build a parent with a NegativeUNL SLE containing ToDisable / ToReEnable entries, apply replay-delta on seq=256, assert DisabledValidators field reflects the expected transitions.
- Test that non-flag ledger replay does NOT mutate NegativeUNL (no-op).

---

## Phase 2 — Meta handling: D8 install engine-generated meta during replay

**Files:**
- `internal/ledger/inbound/replay_delta.go:590-625` (apply switch)
- `internal/tx/engine.go:260-275` (ApplyResult)
- `internal/tx/serialize.go:130-170` (CreateTxWithMetaBlob already exists)

**Verified state:** `replay_delta.go:599,616,624` installs `dtx.LeafBlob` (peer's tx + peer's meta) into the child tx map. Rippled's `BuildLedger.cpp:58-64` runs:
```cpp
OpenView accum(&*built);
applyTxs(accum, built);
accum.apply(*built);
```
where `applyTxs` just calls `applyTransaction(...)` per tx and DISCARDS its return. The engine-generated metadata accumulates in the OpenView and is installed into the tx map via `accum.apply`. Rippled's design: ENGINE meta ends up in the tx map, peer meta is never installed. Divergence surfaces as a final TxHash mismatch.

**Design decision:** follow rippled's approach. If our engine produces meta byte-identical to rippled's, nothing changes. If our engine produces different meta, the final TxHash won't match and we fall back to legacy — surfacing the divergence loudly instead of masking it.

**Risk:** if our engine's meta is NOT byte-identical to rippled's in practice, every replay-delta will fall through to legacy. Before making D8 the default, add a defensive comparison-only mode first:

**R6b.2a — comparison-only log (low-risk, ship first):**
1. After `engine.Apply(txn)` returns, serialize `result.Metadata` via `SerializeMetadata`.
2. Compare byte-for-byte against `dtx.MetaBytes`.
3. On mismatch: log at WARN with tx hash + first-diff offset. Don't change behavior yet.
4. Install `dtx.LeafBlob` as today.

This gives us telemetry without breaking catchup. If logs show engine meta matches peer meta consistently, we can flip to R6b.2b below.

**R6b.2b — install engine meta (future round, gated on R6b.2a telemetry):**
1. Build a new leaf via `tx.CreateTxWithMetaBlob(dtx.TxBytes, result.Metadata)`.
2. Install that leaf instead of `dtx.LeafBlob`.
3. If the final `child.TxMapHash()` doesn't match `hdr.TxHash`, abandon the replay and fall back to legacy.

**Ship R6b.2a in this round. Defer R6b.2b until we have meta-parity telemetry.**

**Verification for R6b.2a:**
- New test: inject a replay where peer meta differs from what our engine would produce for the same tx; assert Apply succeeds (existing behavior preserved) AND the WARN log fired. Requires some way to capture slog output in tests — see existing tests for a pattern.
- Existing integration tests must keep passing (no behavior change).

---

## Phase 3 — A13 dynamic quorum recomputation on negUNL changes

**File:** `internal/consensus/adaptor/adaptor.go:215-220`

**Verified state:** `quorum := (n*4 + 4) / 5` is computed once at `New()` from `cfg.Validators`. Never recomputed. Rippled `ValidatorList.cpp:2061-2087` recomputes quorum on every UNL/negUNL change: `quorum = ceil(0.8 * (trusted - disabled))`.

**Fix:**
1. Move quorum from a `int` field on Adaptor to a method `GetQuorum()` that computes it live:
   ```go
   func (a *Adaptor) GetQuorum() int {
       n := len(a.trustedValidators)
       disabled := len(a.GetNegativeUNL())
       effective := n - disabled
       if effective < 1 && n > 0 {
           effective = 1 // minimum-1 floor to stay live with mostly-disabled UNL
       }
       q := (effective*4 + 4) / 5
       if q < 1 && n > 0 {
           q = 1
       }
       return q
   }
   ```
2. Update `ValidationTracker.SetNegativeUNL` to also refresh the quorum via a new field or recompute lazily from the adaptor. Or: the tracker's quorum can just be read from `adaptor.GetQuorum()` on every `checkFullValidation` call.
3. The engine already calls `validationTracker.SetNegativeUNL(adaptor.GetNegativeUNL())` on every `acceptLedger` (rcl/engine.go:1374-1388 post-R3.2); add a sibling `SetQuorum(adaptor.GetQuorum())` call in the same spot.

**Edge case:** quorum floor. If all validators are on negUNL, effective=0 → quorum=0, meaning any single validation triggers fully-validated. That's wrong (a completely disabled UNL has no validators, so it shouldn't validate anything). Guard: if effective == 0, set quorum to something impossibly high (e.g., math.MaxInt) so checkFullValidation never fires.

**Verification:**
- New test: 5-validator UNL, no negUNL → quorum=4. Mark 2 negUNL → quorum should recompute to ceil(0.8*3)=3.
- Edge: all 5 on negUNL → quorum never reachable (no validations count).

---

## Phase 4 — A6 type codes for UINT96/192/384/512

**File:** `internal/consensus/adaptor/stvalidation.go:27-28`

**Verified state:** Go has:
```go
typeUINT384 = 20
typeUINT512 = 21
```
Rippled `SField.h:84-87`:
```cpp
STYPE(STI_UINT96, 20)
STYPE(STI_UINT192, 21)
STYPE(STI_UINT384, 22)
STYPE(STI_UINT512, 23)
```
Off-by-2. Also missing UINT96/192 entirely.

**Impact today:** Latent — no validation field uses these types. But these constants are part of the SField canonicalOrder key `(type<<16)|field` so a field added later (e.g., a future UINT384 sfield) would sort in the wrong position.

**Fix:**
```go
typeUINT96    = 20
typeUINT192   = 21
typeUINT384   = 22
typeUINT512   = 23
```
Plus add a regression test `TestSFieldTypeCodes_MatchRippled` that pins all known STI_ constants against rippled's values.

**Verification:** unit test compares each constant to rippled's SField.h values.

---

## Phase 5 — Cleanups

### R6b.5a — Fix replayer.go doc comment

**File:** `internal/ledger/inbound/replayer.go:22-30`

Current comment cites `MAX_PEERS_PER_LEDGER` (doesn't exist in rippled). The actual rippled constant is `MAX_NO_FEATURE_PEER_COUNT` (`LedgerReplayer.h:55`), which counts legacy-only peers per replay task — a different semantic than go-xrpl's per-peer concurrent-acquisition cap. Rewrite the comment to say go-xrpl's cap is its own tuning knob inspired by rippled's similar-purpose limit, without claiming direct parity.

### R6b.5b — Populate `sfLoadFee` on outbound validations

**Files:** `internal/consensus/rcl/engine.go:sendValidation`, `internal/consensus/engine.go` (Adaptor interface)

**Verified state:** the struct field `validation.LoadFee` exists (types.go:188), the serializer emits it when non-zero (stvalidation.go:280+), but no code path ever sets it. Rippled emits the local load_fee on every validation under HardenedValidations (`RCLConsensus.cpp:851`).

**Fix:**
1. Add `GetLoadFee() uint32` to the `consensus.Adaptor` interface.
2. Production implementation returns the current load_fee (today: always 0 since there's no load feedback loop yet — safe default).
3. `sendValidation` populates `validation.LoadFee = e.adaptor.GetLoadFee()`.
4. Test: mock adaptor returns 42, assert emitted validation has LoadFee=42.

Low impact: a validator with no load feedback emits 0, which the serializer already omits.

### R6b.5c — `sfCloseTime` parsing

**File:** `internal/consensus/adaptor/stvalidation.go` (parseSTValidation)

**Verified state:** inbound parser silently discards sfCloseTime. Rippled lists it as soeOPTIONAL at `STValidation.cpp:63`. SigningData still flows (CloseTime is captured into SigningData bytes for signature verification) — we just don't surface it to the engine.

**Fix:** add a `CloseTime time.Time` field on `consensus.Validation`, populate from the parsed field. Low-priority but closes the parser gap.

---

## Sequencing

1. **R6b.5a** — one-line doc comment fix (warm-up, zero-risk)
2. **R6b.4** — A6 type codes + test (tiny diff, high-value)
3. **R6b.5b** — sfLoadFee wiring (small, interface extension)
4. **R6b.5c** — sfCloseTime parsing (small)
5. **R6b.3** — A13 dynamic quorum (medium; touches adaptor + tracker + engine wiring)
6. **R6b.1** — D9 updateNegativeUNL on flag-ledger replay (medium; new Ledger method)
7. **R6b.2a** — D8 comparison-only meta log (medium; low behavior change)

R6b.2b (switch to engine-generated meta) deferred until telemetry from R6b.2a informs the design.

---

## Verification checklist

- [ ] All new tests green
- [ ] `go test ./...` passes (excluding pre-existing vault failures)
- [ ] `golangci-lint run ./...` clean
- [ ] Mutation tests for R6b.1, R6b.3, R6b.4
- [ ] R6b.2a: run existing replay-delta integration tests; confirm no behavior change
- [ ] Conformance summary unchanged or improved

---

## Risk matrix

| Item | Risk | Mitigation |
|------|------|------------|
| R6b.1 D9 | Medium — touches Ledger close semantics | Isolate to replay-delta path; don't touch consensus close yet |
| R6b.2a D8 | Low — log-only, no behavior change | Safe by construction |
| R6b.2b D8 | **Deferred** — risks breaking catchup on engine meta drift | Ship R6b.2a first, gather telemetry |
| R6b.3 A13 | Medium — quorum math changes affect finality | Test matrix covering no-negUNL, partial-negUNL, all-negUNL |
| R6b.4 A6 | Low — latent, no current field uses these types | Regression test pinning constants |
| R6b.5a | Trivial | — |
| R6b.5b sfLoadFee | Low — adds field emission, 0 for non-validators | Mock adaptor returns 0 |
| R6b.5c sfCloseTime | Low — parse-only, no emission change | — |
