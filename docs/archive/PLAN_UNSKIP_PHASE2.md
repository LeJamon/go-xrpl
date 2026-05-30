# Plan: Unskip Remaining Actionable Tests (Phase 2)

24 tests across 8 categories, excluding `ripple_path_find` RPC (13) and
Domain/hybrid offers (1) which have dedicated issues.

## Dependency Graph

```
Phase 1 (No dependencies — easy wins)
  ├── 1A: Freeze in BookStep (2 tests)
  └── 1B: MultiSignReserve amendment (1 test)

Phase 2 (Core payment engine)
  ├── 2A: Rippling through intermediaries (4 tests)
  └── 2B: Explicit multi-hop paths (7 tests) [depends on 2A]

Phase 3 (Test infrastructure)
  ├── 3A: TxQueue in test env (5 tests)
  └── 3B: Open ledger fee model (4 tests) [depends on 3A]

Phase 4 (Batch advanced)
  └── 4A-C: DelegateSet + batch multi-sign (3 tests)

Phase 5 (Deferred)
  └── AMM in payment paths (2 tests) [depends on 2A+2B]
```

---

## Phase 1A: Freeze Enforcement in BookStep (2 tests)

**Tests**: `TestFreeze_PathsWhenFrozen`, `TestFreeze_OffersWhenFrozen`

**Problem**: `BookStep.getOfferFundedAmount()` doesn't check if the offer
owner's trust line is frozen. In rippled, `accountHolds()` with
`fhZERO_IF_FROZEN` returns zero for frozen accounts, and `isDeepFrozen()`
removes deep-frozen offers from the stream.

**Rippled reference**:
- `OfferStream.cpp` lines 280-292: `isDeepFrozen()` in iteration loop
- `View.cpp` lines 248-268: `isFrozen()` — global + individual freeze
- `View.cpp` lines 348-371: `isDeepFrozen()` — deep freeze flags
- `View.cpp` lines 385-465: `accountHolds()` with `fhZERO_IF_FROZEN`

**Changes**:
1. `internal/tx/payment/step_book.go`:
   - `getOfferFundedAmount()`: add `isFrozen()` check before IOU balance read
   - Iteration loop in `Rev()`/`Fwd()`: add `isDeepFrozen()` check
2. Add helpers: `isOfferFrozen()`, `isOfferDeepFrozen()`
3. Implement test bodies + remove `t.Skip()`

**Effort**: Low | **Risk**: Low

---

## Phase 1B: MultiSignReserve Amendment (1 test)

**Test**: `TestOracle/NoMultiSignReserve_NoExpandedSignerList`

**Problem**: `SignerListSet.Apply()` always charges OwnerCount = 1. When
`MultiSignReserve` is disabled, it should charge `2 + numSigners`.

**Rippled reference**:
- `SetSignerList.cpp` lines 356-366: `addedOwnerCount` amendment check
- Lines 170-193: `signerCountBasedOwnerCountDelta()` = `2 + entryCount`
- Lines 210-223: deletion checks `lsfOneOwnerCount` flag

**Changes**:
1. `internal/tx/signerlist/signer_list_set.go`:
   - Create branch: if `MultiSignReserve` enabled → OwnerCount=1, set `lsfOneOwnerCount`
   - If disabled → OwnerCount = `2 + len(entries)`
   - Delete path: read `lsfOneOwnerCount` to decide decrement amount
2. `internal/ledger/state/signer_list.go`: support `Flags` field in serialization
3. Remove skip in `oracle_test.go`

**Effort**: Low | **Risk**: Low

---

## Phase 2A: Rippling Through Intermediaries (4 tests)

**Tests**: `TestPath_IndirectPath`, `TestPath_IndirectPathsPathFind`,
`TestPath_NoRippleCombinations`, `TestDepositAuth_NoRipple`

**Problem**: Strand builder constructs DirectStepI chains correctly, but
`curIssue.Issuer` isn't updated when moving through intermediate accounts.
For alice→bob→carol with USD/alice, after the first DirectStepI(alice,bob),
`curIssue.Issuer` must become `bob` for the next hop to work.

**Rippled reference**:
- `DirectStep.cpp`: full direct step with rippling semantics
- `StrandFlow.h`: strand execution with issuer tracking

**Changes**:
1. `internal/tx/payment/strand.go` — `ToStrandWithContext()`:
   - After creating DirectStepI(cur, next), update `curIssue.Issuer = next.account`
   - Verify `checkNoRipple()` properly blocks when NoRipple is set
2. `internal/tx/payment/step_direct.go`:
   - Verify trust line lookup uses adjacent accounts (bob,carol), not original issuer
3. Remove `t.Skip()` from 4 tests

**Effort**: Medium | **Risk**: Medium (core payment engine change)

---

## Phase 2B: Explicit Multi-hop Paths (7 tests)

**Tests**: `TestPath_IssuesRippleClientIssue23Smaller/Larger`,
`TestPath_AlternativePathsConsumeBestFirst`, `TestFlow_SelfPayment2`,
`TestFlow_SelfFundedXRPEndpoint`, `TestFlow_CircularXRP`,
`TestSetTrust_PaymentsWithPathsAndFees`

**Problem**: Multi-issuer path steps and self-payment scenarios (src==dst
through external offers) fail with `tecPATH_DRY`.

**Depends on**: Phase 2A (rippling fix)

**Changes**:
1. `internal/tx/payment/strand.go`:
   - Handle self-payment edge case (src==dst with explicit path)
   - Fix multi-issuer path step handling
2. `internal/tx/payment/payment.go`:
   - Don't short-circuit self-payments when explicit paths are provided
3. Remove `t.Skip()` from 7 tests

**Effort**: Medium | **Risk**: Medium

---

## Phase 3A: Transaction Queue in Test Env (5 tests)

**Tests**: `TestOrdering_IncorrectOrder/MultipleIntermediaries`,
`TestRegression_FeeEscalation/ExtremeConfig`, `TestBatchTxQueue`

**Problem**: Test env has no TxQueue. Tests that submit out-of-order
sequences expect `terPRE_SEQ` buffering and replay on `Close()`.

**Rippled reference**: `TxQ.cpp` — full queue implementation

**Note**: TxQ code already exists in `internal/txq/` with `Apply()`,
`Accept()`, and `ProcessClosedLedger()`.

**Changes**:
1. `internal/testing/env.go`:
   - Add `txQueue *txq.TxQ` field, init in `NewTestEnv()`
   - `Submit()`: route through TxQ when sequence > current
   - `Close()`: call `txQueue.Accept()` to drain queue
2. Create `ApplyContext` adapter for test env
3. Remove `t.Skip()` from 5 tests

**Effort**: Medium-High | **Risk**: Medium

---

## Phase 3B: Open Ledger Fee Model (4 tests)

**Tests**: `TestSequenceOpenLedger`, `TestObjectsOpenLedger`,
`TestOpenLedger`, `TestTicketsOpenLedger`

**Depends on**: Phase 3A (TxQueue)

**Changes**:
1. `internal/testing/env.go`:
   - Track `txInLedger` count per open ledger period
   - Implement fee escalation check via `ScaleFeeLevel()`
   - Add `openLedgerFeeLevel()` method
2. `internal/txq/fee.go`: verify `ScaleFeeLevel` matches rippled
3. Remove `t.Skip()` from 4 tests

**Effort**: High | **Risk**: High (complex fee model integration)

---

## Phase 4: Batch Infrastructure (3 tests)

### 4A: DelegateSet Full Apply (TestBatchDelegate)

**Rippled reference**: `DelegateSet.cpp` — `doApply()` + `deleteDelegate()`

**Changes**:
1. `internal/tx/delegate/delegate_set.go`: rewrite `Apply()`:
   - Create/update/delete Delegate SLE with proper fields
   - Reserve checks, owner directory, owner count
2. Add Delegate SLE serialization in `internal/ledger/state/`

### 4B+4C: Batch Multi-signing (TestPreclaim, TestObjectCreate3rdParty)

**Changes**:
1. `internal/tx/batch/batch.go`: add `Preclaim()`:
   - `checkSign()` for single signing
   - `checkBatchSign()` for multi-sign (weight accumulation vs quorum)
2. `internal/tx/engine.go`: ensure batch preclaim is called

**Effort**: High | **Risk**: High (complex signing verification)

---

## Phase 5: AMM in Payment Paths (2 tests) — DEFERRED

**Tests**: `TestAMMBookStep_GatewayCrossCurrency`, `TestFreeze_AMMWhenFrozen`

Requires auto-path discovery (`build_path`) with AMM pool integration.
Deferred until rippling (Phase 2) is stable and path-finding work progresses.

---

## Implementation Order

| Step | Phase | Tests | Cumulative |
|------|-------|-------|------------|
| 1 | 1A: Freeze | 2 | 2 |
| 2 | 1B: MultiSignReserve | 1 | 3 |
| 3 | 2A: Rippling | 4 | 7 |
| 4 | 2B: Multi-hop paths | 7 | 14 |
| 5 | 4A: DelegateSet | 1 | 15 |
| 6 | 3A: TxQueue | 5 | 20 |
| 7 | 3B: Open ledger fees | 4 | 24 |
| 8 | 4B+4C: Batch signing | 2 | 26 |

Total: **24 tests** unskipped (26 counting 2 deferred AMM tests separately).
