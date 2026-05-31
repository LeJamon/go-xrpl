# Situation Report: 6 Remaining Offer Test Failures

## Status: 6 tests failing, root cause partially identified

We started with 29 failing offer tests. We're now down to **6 failing tests**. All 6 share a common theme: **IOU arithmetic precision doesn't match rippled**.

---

## The 6 Failing Tests

| Test | Symptom |
|------|---------|
| `TestOffer_CreateThenCross` | Bob's balance = `-0.9665` instead of `-0.966500000033334` (switchover=false) or `-0.9665000000333333` (switchover=true) |
| `TestOffer_InScalingWithXferRate` | Mantissa off by ~23 units |
| `TestOffer_SelfPayUnlimitedFunds` | 2 offers remain instead of 1, BTC balance wrong |
| `TestReducedOffer_UnderFundedXrpIouQChange` | Underfunded offer not removed |
| `TestReducedOffer_UnderFundedIouIouQChange` | `tecPATH_DRY` + offer not removed |
| `TestReducedOffer_SellPartialCrossOldXrpIouQChange` | Bad rate offers with fixV2 |

---

## What We Know For Sure

### 1. Confirmed Bug: `Div()` incorrectly uses XRPLNumber when switchover is on

**File:** `internal/core/tx/sle/amount.go` line 1055

```go
// BUG: rippled's divide() NEVER uses Number, even with fixUniversalNumber
if GetNumberSwitchover() && !a.IsNative() {
    na := NewXRPLNumber(m1, e1)
    nb := NewXRPLNumber(m2, e2)
    result := na.Div(nb)  // <-- WRONG: should use muldiv+5 path always
    ...
}
```

In rippled, `divide()` in STAmount.cpp **always** uses the old `muldiv + 5` path regardless of `getSTNumberSwitchover()`. Our Go code incorrectly routes through `XRPLNumber.Div()` when the switchover flag is on.

**Impact:** Affects quality computation via `QualityFromAmounts()` (which calls `Div(roundUp=false)`), and any `Div(roundUp=true)` calls (divRound). Only manifests when `fixUniversalNumber` is enabled.

### 2. XRPLNumber type already exists and works

We already have a complete `XRPLNumber` type in `internal/core/tx/sle/xrpl_number.go` with:
- Guard class (16 BCD nibbles + sticky bit) for extended precision
- All 4 rounding modes (nearest, towards-zero, downward, upward)
- `NumberRoundModeGuard` for RAII-like mode switching
- Switchover flag controlled by `SetNumberSwitchover()` / `GetNumberSwitchover()`
- Called from `normalize()`, `addIOUValues()`, `Mul(roundUp=false)` — all correct

### 3. Where the switchover IS and ISN'T used in rippled

| Function | Uses Number with switchover? | Our Go code |
|----------|-----|------|
| `multiply()` (Mul, roundUp=false) | YES | Correctly delegates to XRPLNumber |
| `divide()` (Div) | **NEVER** | BUG: delegates to XRPLNumber |
| `mulRatio()` (MulRatio) | **NEVER** (128-bit int arithmetic) | Correct: never uses XRPLNumber |
| `mulRound()` / `mulRoundStrict()` | Old muldiv_round + canonicalize; Number only affects normalize via STAmount constructor | Correct: separate functions `MulRound`/`MulRoundStrict` |
| `divRound()` / `divRoundStrict()` | Old muldiv_round + canonicalize; Number only affects normalize | Correct: separate functions `DivRound`/`DivRoundStrict` |
| `IOUAmount::normalize()` | YES | Correct |
| `IOUAmount::operator+=` | YES | Correct (addIOUValues) |

### 4. Amount type architecture is correct

The Go `Amount` type in `sle/amount.go` correctly mirrors rippled's `STAmount`:
- `MulRatio()` uses big.Int with roomToGrow/mustShrink matching rippled's `IOUAmount::mulRatio()`
- `MulRoundStrict()` / `MulRound()` use muldiv_round + canonicalizeRound(Strict) matching rippled
- `DivRound()` / `DivRoundStrict()` match rippled
- `normalize()` delegates to XRPLNumber when switchover is on
- `addIOUValues()` delegates to XRPLNumber.Add() when switchover is on

### 5. BookStep architecture is correct

The BookStep `Rev()` and `Fwd()` functions correctly use:
- `QualityFromAmounts` → `Div(roundUp=false)` for quality computation
- `CeilOutStrict` → `MulRoundStrict` for strict rounding with fixReducedOffersV1
- `CeilOut` → `MulRound` for legacy rounding
- `MulRatio` for transfer rate application
- `consumeOffer` → `transferFundsWithFee` + `transferFunds` for trust line updates

---

## What We DON'T Know (The Mystery)

### The 3.3e-11 precision difference in CreateThenCross

For `TestOffer_CreateThenCross` with `NumberSwitchOver=false`:

- **Expected:** Bob's balance = `-0.966500000033334` (Bob spent 0.033499999966666 USD)
- **Actual:** Bob's balance = `-0.9665` (Bob spent 0.0335 USD exactly)
- **Difference:** 3.3e-11 USD

**Manual arithmetic tracing** through the entire computation chain consistently produces 0.0335:

1. Quality = `divide(50 USD, 150000 XRP)` → mantissa=3333333333333333, exp=-25
2. `CeilOutStrict(100 XRP, quality)` → `MulRoundStrict` → mantissa=3333333333333333, exp=-17 (= 0.03333...)
3. `MulRatio(0.03333..., 1005000000, 1000000000, true)` → after roomToGrow, normalize, +1 rounding → mantissa=3350000000000000, exp=-17 (= 0.0335 exactly)
4. `addIOUValues(-1.0, +0.0335)` → -0.9665

The expected value (0.033499999966666) would require MulRatio to produce mantissa=3349999996666600 instead of 3350000000000000. These are vastly different (not a 1-ULP rounding issue).

**This means either:**
1. Our manual trace has an error somewhere in the long arithmetic chain
2. There's an additional step or modification we're not accounting for (e.g., the DirectStepI qualities, a second trust line modification, or the consumeOffer doing something unexpected)
3. The flow engine's forward/reverse pass interaction creates a different amount

**We have not been able to identify the root cause through manual tracing.** The arithmetic chains are too long and error-prone for manual verification.

---

## Recommended Next Steps

### Step 1: Fix the confirmed Div() bug
Remove the XRPLNumber path from `Div()`. This is a clear-cut bug regardless of the precision issue.

### Step 2: Add targeted debug logging
Add logging at these critical points to compare intermediate values with rippled:

1. **BookStep.Rev() partial take:** Log `ofrAdjIn`, `ofrAdjOut`, `stpAdjIn` (mantissa + exponent)
2. **MulRatio:** Log input mantissa, product, low, rem, roomToGrow, addRem, final result (mantissa + exponent)
3. **DirectStepI.Rev():** Log `out`, `srcToDst`, `in`, `dstQIn`, `srcQOut` values
4. **rippleCredit:** Log the exact amount being applied and the before/after trust line balance
5. **addIOUValues:** Log both operands (mantissa + exponent) and the result

Run the test with logging, then compare every intermediate value against rippled's expected behavior. This will pinpoint exactly where the divergence occurs.

### Step 3: Fix identified precision bugs
Based on the debug output, fix whatever is producing the wrong intermediate value.

### Step 4: Clean up
- Remove dead code (`computeOutputFromInput*` functions in step_book.go)
- Remove all `fmt.Printf` debug statements from: step_book.go, flow.go, flow_cross.go, step_direct.go, strand_flow.go

### Step 5: Verify
Run all 6 tests. Target: 0 failures.

---

## Key Files

| File | Role |
|------|------|
| `internal/core/tx/sle/amount.go` | All STAmount arithmetic (Mul, Div, MulRatio, MulRound, etc.) |
| `internal/core/tx/sle/xrpl_number.go` | XRPLNumber type (Guard class, rounding modes) |
| `internal/core/tx/payment/step_book.go` | BookStep Rev/Fwd + consumeOffer |
| `internal/core/tx/payment/step_direct.go` | DirectStepI Rev/Fwd + rippleCredit |
| `internal/core/tx/payment/step.go` | Quality, CeilOut/CeilOutStrict, CeilIn/CeilInStrict |
| `internal/core/tx/payment/strand_flow.go` | ExecuteStrand (reverse + forward pass) |
| `internal/core/tx/payment/flow_cross.go` | FlowCross entry point |
| `internal/core/tx/payment/flow.go` | Flow engine (strand iteration) |

## Rippled References

| File | What to look at |
|------|-----------------|
| `rippled/src/libxrpl/protocol/STAmount.cpp` | multiply, divide, mulRound, divRound, canonicalize |
| `rippled/src/libxrpl/protocol/IOUAmount.cpp` | mulRatio, normalize, operator+= |
| `rippled/src/libxrpl/basics/Number.cpp` | Number type with Guard class |
| `rippled/src/xrpld/app/paths/detail/BookStep.cpp` | BookStep consumeOffer, forEachOffer |
| `rippled/src/test/app/Offer_test.cpp` | Test reference (lines 2098-2151 for CreateThenCross) |
