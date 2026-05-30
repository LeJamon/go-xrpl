# OfferCreate Implementation Comparison: Go vs Rippled Spec

This document compares the current Go implementation against the rippled specification.

---

## Summary

| Area | Status | Notes |
|------|--------|-------|
| Preflight Validation | ✅ Mostly Complete | Minor issues with error return |
| Preclaim Validation | ⚠️ Partial | Missing some checks |
| Apply Phase | ⚠️ Significant Issues | Missing two-sandbox pattern for FoK |
| FoK Handling | ❌ Incorrect | Missing sbCancel sandbox |
| IoC Handling | ⚠️ Partial | Logic present but missing `crossed` tracking |
| Reserve Check | ⚠️ Partial | Logic inverted, fee handling unclear |
| Tick Size | ❌ Incomplete | Functions are stubs |
| Quality Calculation | ⚠️ Needs Review | Implementation exists but may have precision issues |
| Remaining Offer Calculation | ⚠️ Complex | Logic present but convoluted |

---

## Detailed Analysis

### 1. Preflight Validation

**Location:** `offer_create.go:162-261`

#### ✅ Correctly Implemented

| Check | Spec | Go Implementation |
|-------|------|-------------------|
| DomainID without amendment | `temDISABLED` | ✅ Lines 165-167 |
| Invalid flags mask | `temINVALID_FLAG` | ✅ Lines 171-173 |
| tfHybrid without amendment | `temINVALID_FLAG` | ✅ Lines 178-180 |
| tfHybrid without DomainID | `temINVALID_FLAG` | ✅ Lines 184-186 |
| IoC + FoK combined | `temINVALID_FLAG` | ✅ Lines 190-194 |
| Expiration == 0 | `temBAD_EXPIRATION` | ✅ Lines 198-200 |
| OfferSequence == 0 | `temBAD_SEQUENCE` | ✅ Lines 204-206 |
| Amount validation | `temBAD_AMOUNT` | ✅ Lines 214-216 |
| XRP for XRP | `temBAD_OFFER` | ✅ Lines 220-222 |
| Amounts <= 0 | `temBAD_OFFER` | ✅ Lines 226-228 |
| Redundant offer | `temREDUNDANT` | ✅ Lines 238-240 |
| Bad currency | `temBAD_CURRENCY` | ✅ Lines 244-249 |
| Issuer consistency | `temBAD_ISSUER` | ✅ Lines 254-259 |

#### ❌ Issues Found

1. **Error Return in Apply()** (Line 138-139):
   ```go
   if err := o.Preflight(ctx.Rules()); err != nil {
       return tx.TemMALFORMED  // Always returns TemMALFORMED
   }
   ```
   **Problem:** All preflight errors return `TemMALFORMED` instead of the specific error code.
   **Spec:** Each preflight check should return its specific error code (temBAD_OFFER, temINVALID_FLAG, etc.)

---

### 2. Preclaim Validation

**Location:** `offer_create.go:266-338`

#### ✅ Correctly Implemented

| Check | Spec | Go Implementation |
|-------|------|-------------------|
| Global freeze check | `tecFROZEN` | ✅ Lines 277-286 |
| OfferSequence >= account.Sequence | `temBAD_SEQUENCE` | ✅ Lines 302-306 |
| Expiration check | `tecEXPIRED` or `tesSUCCESS` | ✅ Lines 310-315 |
| Authorization check | `tecNO_LINE`, `tecNO_AUTH`, `tecFROZEN` | ✅ Lines 319-328 |
| Domain membership | `tecNO_PERMISSION` | ✅ Lines 332-336 |

#### ❌ Issues Found

1. **Funding Check** (Lines 288-298):
   ```go
   funds := tx.AccountFunds(ctx.View, ctx.AccountID, saTakerGets, true)
   diff := sle.SubtractAmount(saTakerGets, funds)
   if diff.Signum() > 0 {
       return tx.TecUNFUNDED_OFFER
   }
   ```
   **Problem:** The check uses `SubtractAmount` which may have issues.
   **Spec:** Should check `accountFunds(TakerGets) <= 0` → `tecUNFUNDED_OFFER`
   **Actual Logic:** Checks if `TakerGets - funds > 0` which is correct (unfunded if offer exceeds balance), but the comment says "seems weird".

2. **Debug Print Statements** (Lines 288, 292-293):
   ```go
   fmt.Println("TakerGet: ", saTakerGets.Value())
   fmt.Println("PASSED FUNDS: ", funds.Value())
   ```
   **Problem:** Debug statements should not be in production code.

3. **Missing Account Existence Check**:
   **Spec:** Should return `terNO_ACCOUNT` if account doesn't exist.
   **Go:** Not explicitly checked (relies on ctx.Account being valid).

---

### 3. Apply Phase (applyGuts)

**Location:** `offer_create.go:351-736`

#### ❌ Critical Issue: Missing Two-Sandbox Pattern

**Spec Requirement:**
```
Uses TWO sandboxes:
- sb: Main sandbox - used for successful execution
- sbCancel: Cancel sandbox - used only if FoK fails
```

**Go Implementation:** Uses only ONE sandbox via `FlowCross()`.

**Impact:** Fill-or-Kill offers cannot properly rollback when they fail to fully cross.

#### ❌ FoK Handling Incorrect (Lines 598-603)

```go
if bFillOrKill {
    if rules.Enabled(amendment.FeatureFix1578) {
        return tx.TecKILLED
    }
    return tx.TesSUCCESS
}
```

**Problems:**
1. This code runs AFTER the offer has already been applied to the sandbox
2. There's no sbCancel to rollback to
3. The cancellation should have been tracked but applied changes remain

**Spec:**
```
if tfFillOrKill:
  if fully crossed (remaining.in == 0 OR remaining.out == 0):
    return tesSUCCESS, apply sb
  else:
    - with fix1578 → tecKILLED, apply sbCancel
    - without → tesSUCCESS, apply sbCancel (no offer placed)
```

#### ⚠️ Offer Cancellation (Lines 372-377)

```go
if o.OfferSequence != nil {
    sleCancel := peekOffer(ctx.View, ctx.AccountID, *o.OfferSequence)
    if sleCancel != nil {
        result = offerDelete(ctx, sleCancel)
    }
}
```

**Spec:** Cancellation should also happen in `sbCancel` for FoK scenarios.
**Go:** Only happens in main context.

#### ⚠️ Balance Update After Crossing (Lines 472-483)

```go
if placeOffer.in.IsNative() {
    paidDrops := uint64(placeOffer.in.Drops())
    if ctx.Account.Balance >= paidDrops {
        ctx.Account.Balance -= paidDrops
    }
}
if placeOffer.out.IsNative() {
    receivedDrops := uint64(placeOffer.out.Drops())
    ctx.Account.Balance += receivedDrops
}
```

**Problem:** Manual balance manipulation after sandbox apply is error-prone.
**Spec:** Balance changes should be handled within the sandbox/view system.

---

### 4. Reserve Check

**Location:** `offer_create.go:614-626`

```go
reserve := ctx.AccountReserve(ctx.Account.OwnerCount + 1)
parsedFee := parseFee(ctx)
priorBalance := ctx.Account.Balance + parseFee(ctx)
if priorBalance < reserve {
    if !crossed {
        return tx.TecINSUF_RESERVE_OFFER
    }
    return tx.TesSUCCESS
}
```

#### ⚠️ Issues

1. **Fee Handling:** The spec says `mPriorBalance` (balance before fee deduction). Adding fee back may be correct, but `parseFee()` just returns `ctx.Config.BaseFee` which may not be the actual transaction fee.

2. **Logic is correct:** If not crossed and insufficient reserve → error. If crossed → success (can go below reserve due to crossing).

---

### 5. Tick Size

**Location:** `offer_create.go:1060-1149`

#### ❌ Functions are Stubs

```go
func roundToTickSize(quality uint64, tickSize uint8) uint64 {
    // TODO: Implement proper tick size rounding
    return quality  // NO-OP!
}

func multiplyByQuality(amount tx.Amount, quality uint64, currency, issuer string) tx.Amount {
    // TODO: Implement proper multiplication
    if amount.IsNative() {
        return tx.NewXRPAmount(amount.Drops())
    }
    return sle.NewIssuedAmountFromDecimalString(amount.Value(), currency, issuer)
}

func divideByQuality(amount tx.Amount, quality uint64, currency, issuer string) tx.Amount {
    // TODO: Implement proper division
    // ... same issue
}
```

**Impact:** Tick size rounding is completely non-functional.

**Spec:**
- Get minimum tick size from both issuers
- Round quality to that precision
- Recalculate one side of the offer based on the flag

---

### 6. Quality/Rate Calculation

**Location:** `offer_create.go:366` and `sle.GetRate()`

```go
uRate := sle.GetRate(saTakerGets, saTakerPays)
```

**Status:** Implementation exists but needs verification that it matches rippled's `getRate()` exactly. The rate is used for book directory ordering.

---

### 7. Remaining Offer Calculation

**Location:** `offer_create.go:530-584`

This section is complex with multiple paths:

```go
if isAmountZeroOrNegative(remainingWithGross) {
    // Fully consumed path
} else if noCrossingHappened {
    // No crossing - use original amounts
} else if bSell {
    // Sell offer with remaining
    remainingGets = subtractAmounts(saTakerGets, placeOffer.in)
    roundUp := !rules.Enabled(amendment.FeatureFixReducedOffersV1)
    remainingPays = multiplyByRatio(remainingGets, o.TakerPays, o.TakerGets, roundUp)
} else {
    // Non-sell offer with remaining
    remainingPays = subtractAmounts(saTakerPays, placeOffer.out)
    remainingGets = multiplyByRatio(remainingPays, o.TakerGets, o.TakerPays, true)
}
```

#### ⚠️ Issues

1. **Sell vs Non-Sell Logic:** The spec says:
   - Sell: `remainingGets = TakerGets - actualAmountIn`, `remainingPays = divRound(remainingGets, rate)`
   - Non-Sell: `remainingPays = TakerPays - actualAmountOut`, `remainingGets = mulRound(remainingPays, rate)`

   The Go implementation uses `multiplyByRatio` for both, which may not match rippled's exact rounding behavior.

2. **Amendment Gating:** `FeatureFixReducedOffersV1` affects rounding direction - this is implemented.

---

### 8. Offer Placement

**Location:** `offer_create.go:628-734`

#### ✅ Mostly Correct

- Owner directory insertion ✅
- Book directory insertion ✅
- Offer SLE creation ✅
- Flag setting (passive, sell) ✅
- Expiration setting ✅
- DomainID setting ✅
- Hybrid offer handling ✅

#### ⚠️ Issues

1. **getOfferSequence()** (Lines 1205-1209):
   ```go
   func getOfferSequence(ctx *tx.ApplyContext) uint32 {
       return ctx.Account.Sequence - 1
   }
   ```
   **Problem:** Assumes sequence was incremented before Apply. Should use transaction sequence or ticket sequence.
   **Spec:** Uses `ctx_.tx.getSeqValue()` which handles both sequence and ticket.

---

### 9. FlowCross Implementation

**Location:** `payment/flow_cross.go`

#### ✅ Good Structure

- Creates sandbox ✅
- Calculates quality threshold ✅
- Handles passive flag (increment threshold) ✅
- Handles transfer rate ✅
- Returns GROSS and NET amounts ✅

#### ⚠️ Issues

1. **Single Sandbox:** No sbCancel for FoK handling.

2. **Quality Calculation** (Line 125):
   ```go
   takerQuality := QualityFromAmounts(sendMax, inAmt)
   ```
   **Spec:** `threshold = Quality(out, sendMax)` - needs verification that the order is correct.

---

## Priority Fixes Required

### Critical (Breaks Core Functionality)

1. **Implement Two-Sandbox Pattern for FoK**
   - Create `sbCancel` sandbox alongside main `sb`
   - Track changes in both
   - Apply correct sandbox based on FoK result

2. **Fix Preflight Error Returns**
   - Return specific error codes instead of always `TemMALFORMED`

3. **Implement Tick Size Functions**
   - `roundToTickSize()` is a no-op
   - `multiplyByQuality()` and `divideByQuality()` need proper implementation

### High Priority

4. **Remove Debug Print Statements**
   - Multiple `fmt.Println` calls in production code

5. **Fix getOfferSequence()**
   - Handle ticket sequences properly
   - Don't assume sequence was pre-incremented

6. **Verify Quality Calculation**
   - Ensure `QualityFromAmounts` parameter order matches rippled

### Medium Priority

7. **Review Remaining Offer Calculation**
   - Verify `multiplyByRatio` matches rippled's `mulRound`/`divRound` exactly

8. **Review Balance Update After Crossing**
   - Consider if manual manipulation is needed or if sandbox handles it

---

## Code Locations Reference

| Component | File | Lines |
|-----------|------|-------|
| OfferCreate struct | `offer_create.go` | 44-61 |
| Preflight | `offer_create.go` | 162-261 |
| Preclaim | `offer_create.go` | 266-338 |
| applyGuts | `offer_create.go` | 351-736 |
| FlowCross | `flow_cross.go` | 51-188 |
| Tick size (stubs) | `offer_create.go` | 1060-1149 |
| Offer delete | `offer_create.go` | 1169-1202 |
| Quality helpers | `offer_create.go` | 782-963 |

---

## Test Coverage

Current tests (`offer_test.go`) have `//go:build ignore` tag - they are not being run.

Tests cover:
- Basic validation ✅
- Flag validation ✅
- Amount type combinations ✅

Missing tests:
- Offer crossing scenarios ❌
- FoK/IoC behavior ❌
- Reserve requirements ❌
- Tick size rounding ❌
- Transfer fee handling ❌
- Partial fills ❌
- Two-sandbox behavior ❌
