# OfferCreate Transaction Specification

*Extracted from rippled C++ reference implementation*

---

## Overview

OfferCreate places an offer on the XRPL's decentralized exchange to exchange one currency for another at a specified rate. The offer can optionally cross existing offers in the order book, partially or completely fulfilling both the new offer and matching existing offers.

**Source Files:**
- `rippled/src/xrpld/app/tx/detail/CreateOffer.cpp` (~950 lines)
- `rippled/src/xrpld/app/tx/detail/CreateOffer.h`
- `rippled/src/xrpld/app/tx/detail/Offer.h`
- `rippled/src/test/app/Offer_test.cpp`

---

## Transaction Fields

### Required Fields

| Field | Type | Description |
|-------|------|-------------|
| **TakerPays** | Amount | Currency/amount the offer creator will pay |
| **TakerGets** | Amount | Currency/amount the offer creator wants to receive |

### Optional Fields

| Field | Type | Description |
|-------|------|-------------|
| **Expiration** | UInt32 | Unix timestamp after which offer is invalid |
| **OfferSequence** | UInt32 | Sequence of existing offer to cancel (must be < tx sequence) |
| **DomainID** | Hash256 | Domain ID for permissioned DEX (requires PermissionedDEX amendment) |

---

## Transaction Flags

| Flag | Hex Value | Description |
|------|-----------|-------------|
| **tfPassive** | 0x00010000 | Only cross offers with strictly better quality |
| **tfImmediateOrCancel** | 0x00020000 | Execute immediately, don't place remainder on books |
| **tfFillOrKill** | 0x00040000 | Entire offer must cross or nothing happens |
| **tfSell** | 0x00080000 | Sell as much as possible (can exceed TakerGets) |
| **tfHybrid** | 0x00100000 | Place in both domain and open order books (PermissionedDEX) |

### Flag Constraints
- `tfImmediateOrCancel` AND `tfFillOrKill` together → `temINVALID_FLAG`
- `tfHybrid` without `DomainID` → `temINVALID_FLAG`
- `tfHybrid` without PermissionedDEX amendment → `temINVALID_FLAG`

---

## Phase 1: Preflight Validation

### 1.1 Amendment Check
```
if DomainID present AND !PermissionedDEX enabled → temDISABLED
```

### 1.2 Flag Validation
```
if invalid flags set → temINVALID_FLAG
if tfImmediateOrCancel AND tfFillOrKill → temINVALID_FLAG
if tfHybrid AND !DomainID → temINVALID_FLAG
```

### 1.3 Expiration Validation
```
if Expiration == 0 → temBAD_EXPIRATION
```

### 1.4 OfferSequence Validation
```
if OfferSequence == 0 → temBAD_SEQUENCE
```

### 1.5 Amount Validation
```
if !isLegalNet(TakerPays) OR !isLegalNet(TakerGets) → temBAD_AMOUNT
if TakerPays <= 0 OR TakerGets <= 0 → temBAD_OFFER
if TakerPays.native AND TakerGets.native → temBAD_OFFER  // XRP for XRP
```

### 1.6 Currency Validation
```
if currency code == XRP (0x0000) for non-native → temBAD_CURRENCY
if native currency has issuer OR non-native lacks issuer → temBAD_ISSUER
```

### 1.7 Redundancy Check
```
if same currency AND same issuer for both sides → temREDUNDANT
```

---

## Phase 2: Preclaim Validation

### 2.1 Account Existence
```
if account doesn't exist → terNO_ACCOUNT
```

### 2.2 Global Freeze Check
```
if TakerPays issuer globally frozen → tecFROZEN
if TakerGets issuer globally frozen → tecFROZEN
```

### 2.3 Funding Check
```
if accountFunds(TakerGets) <= 0 → tecUNFUNDED_OFFER
```

### 2.4 OfferSequence Constraint
```
if OfferSequence >= account.Sequence → temBAD_SEQUENCE
```

### 2.5 Expiration Check
```
if expired:
  - with featureDepositPreauth → tecEXPIRED
  - without amendment → tesSUCCESS (no effect)
```

### 2.6 Authorization Check (for TakerPays currency)
For non-native TakerPays:
1. Issuer must exist → `tecNO_ISSUER`
2. If issuer has `RequireAuth`:
   - Trustline must exist → `terNO_LINE`
   - Must be authorized → `terNO_AUTH`
3. If trustline has deep freeze → `tecFROZEN`

### 2.7 Domain Membership Check (PermissionedDEX)
```
if DomainID present AND account not in domain → tecNO_PERMISSION
```

---

## Phase 3: Apply (doApply/applyGuts)

Uses **two sandboxes**:
- `sb` - Main sandbox for successful execution
- `sbCancel` - Cancel sandbox for FoK failures

### 3.1 Calculate Offer Rate
```go
rate := getRate(TakerGets, TakerPays)
```

### 3.2 Handle Offer Cancellation
```
if OfferSequence provided:
  if offer exists → delete it
  // No error if offer doesn't exist
```

### 3.3 Check Expiration (again)
```
if expired:
  - with featureDepositPreauth → tecEXPIRED
  - return early (but apply cancellation)
```

### 3.4 Apply Tick Size
If either issuer has TickSize (3-16 significant digits):
```
rate = round(rate, min(tickSize1, tickSize2))
if tfSell:
  TakerPays = TakerGets * rate
else:
  TakerGets = TakerPays / rate

if TakerPays == 0 OR TakerGets == 0 → abandon offer
```

### 3.5 Offer Crossing (flowCross)

**Setup:**
```
takerAmount = {in: TakerGets, out: TakerPays}  // Reversed!
```

**Quality Threshold:**
```
threshold = Quality(out, sendMax)
if tfPassive:
  threshold++  // Only cross strictly better offers
```

**Transfer Fee:**
```
if TakerGets is IOU AND account != issuer:
  gatewayXferRate = transferRate(issuer)
  sendMax = TakerGets * gatewayXferRate
```

**Paths:**
- Default path
- If both currencies are IOUs: also try XRP as bridge

**Call flow():**
```
result = flow(
  deliver: takerAmount.out,
  sendMax: sendMax,
  partialPayment: !tfFillOrKill,
  offerCrossing: true,
  threshold: threshold
)
```

**Calculate Remaining:**
```
if tfSell:
  afterCross.in = takerAmount.in - actualAmountIn
  afterCross.out = divRound(afterCross.in, rate)  // or divRoundStrict with fixReducedOffersV1
else:
  afterCross.out = takerAmount.out - actualAmountOut
  afterCross.in = mulRound(afterCross.out, rate)
```

**Offer Grooming:**
Remove stale offers encountered during crossing:
- Missing ledger entries
- Expired offers
- Unfunded offers (zero balance)

### 3.6 Handle Fill or Kill
```
if tfFillOrKill:
  if fully crossed (remaining.in == 0 OR remaining.out == 0):
    return tesSUCCESS, apply sb
  else:
    - with fix1578 → tecKILLED, apply sbCancel
    - without → tesSUCCESS, apply sbCancel (no offer placed)
```

### 3.7 Handle Immediate or Cancel
```
if tfImmediateOrCancel:
  if nothing crossed AND featureImmediateOfferKilled:
    return tecKILLED, apply sbCancel
  return tesSUCCESS, apply sb (no remaining offer placed)
```

### 3.8 Reserve Check
```
if NOT crossed:
  reserve = base_reserve * (1 + owner_count + 1)
  if balance < reserve → tecINSUF_RESERVE_OFFER
// If crossed, no reserve check needed
```

### 3.9 Place Remaining Offer

**Add to owner directory:**
```
ownerNode = dirInsert(ownerDir(account), offer_index)
if failed → tecDIR_FULL
adjustOwnerCount(+1)
```

**Add to order book:**
```
dir = quality(book(TakerPays.issue, TakerGets.issue, domainID), rate)
bookNode = dirAppend(dir, offer_index)
if failed → tecDIR_FULL
```

**Create offer ledger entry:**
```
sleOffer = {
  Account: account,
  Sequence: offerSequence,
  TakerPays: TakerPays,
  TakerGets: TakerGets,
  BookDirectory: dir.key,
  BookNode: bookNode,
  OwnerNode: ownerNode,
  Flags: (lsfPassive if tfPassive) | (lsfSell if tfSell),
  Expiration: expiration (if set),
  DomainID: domainID (if set)
}
```

**Hybrid offers (PermissionedDEX):**
```
if tfHybrid:
  sleOffer.Flags |= lsfHybrid
  openDir = quality(book(issue, issue, null), rate)  // Open book
  openBookNode = dirAppend(openDir, offer_index)
  sleOffer.AdditionalBooks = [{BookDirectory: openDir.key, BookNode: openBookNode}]
```

---

## Ledger Entry Structure (ltOFFER = 0x006f)

### Fields
| Field | Type | Required | Description |
|-------|------|----------|-------------|
| Account | AccountID | Yes | Creator account |
| Sequence | UInt32 | Yes | Offer identifier |
| TakerPays | Amount | Yes | Amount to pay |
| TakerGets | Amount | Yes | Amount to receive |
| BookDirectory | Hash256 | Yes | Order book directory |
| BookNode | UInt64 | Yes | Index in book directory |
| OwnerNode | UInt64 | Yes | Index in owner directory |
| Expiration | UInt32 | No | Expiration timestamp |
| DomainID | Hash256 | No | Domain ID |
| AdditionalBooks | Array | No | For hybrid offers |

### Ledger Entry Flags
| Flag | Value | Description |
|------|-------|-------------|
| lsfPassive | 0x00010000 | Created with tfPassive |
| lsfSell | 0x00020000 | Created with tfSell |
| lsfHybrid | 0x00040000 | Hybrid offer |

---

## Error Codes

### Preflight (tem)
| Code | Condition |
|------|-----------|
| temBAD_AMOUNT | Invalid amount format |
| temBAD_CURRENCY | Non-native currency code is XRP (0x0000) |
| temBAD_EXPIRATION | Expiration is 0 |
| temBAD_ISSUER | Missing/extra issuer field |
| temBAD_OFFER | Amounts ≤ 0, or XRP for XRP |
| temBAD_SEQUENCE | OfferSequence is 0 or ≥ tx sequence |
| temINVALID_FLAG | Invalid flag combination |
| temREDUNDANT | Same currency and issuer on both sides |
| temDISABLED | DomainID without PermissionedDEX |

### Preclaim (ter)
| Code | Condition |
|------|-----------|
| terNO_ACCOUNT | Account doesn't exist |
| terNO_LINE | No trustline when RequireAuth |
| terNO_AUTH | Not authorized on trustline |

### Claim (tec)
| Code | Condition |
|------|-----------|
| tecUNFUNDED_OFFER | Can't fund the offer |
| tecFROZEN | Currency is frozen |
| tecNO_ISSUER | Issuer doesn't exist |
| tecNO_PERMISSION | Not in domain (PermissionedDEX) |
| tecINSUF_RESERVE_OFFER | Insufficient reserve |
| tecDIR_FULL | Directory full |
| tecKILLED | FoK/IoC killed |
| tecEXPIRED | Offer expired |

---

## Key Amendments

| Amendment | Impact |
|-----------|--------|
| **featureDepositPreauth** | Expired → tecEXPIRED instead of tesSUCCESS |
| **fix1578** | FoK failures → tecKILLED instead of tesSUCCESS |
| **featureImmediateOfferKilled** | IoC with no crossing → tecKILLED |
| **fixReducedOffersV1** | Strict rounding for sell offers |
| **featurePermissionedDEX** | Enables tfHybrid and DomainID |

---

## Critical Implementation Notes

1. **Quality is computed once at creation** - never changes for partial fills
2. **Two sandboxes for FoK** - apply sbCancel if FoK fails
3. **Taker amounts are reversed for crossing** - `{in: TakerGets, out: TakerPays}`
4. **Passive threshold incremented** - `++threshold` to only cross strictly better
5. **Reserve only checked if no crossing** - partial crosses bypass reserve check
6. **Transfer fee affects sendMax** - multiply TakerGets by gateway rate
7. **Tick size uses minimum** - min(pays_issuer.tickSize, gets_issuer.tickSize)

---

## Quality Calculation

Quality represents the exchange rate and is used for ordering offers in the book:

```
quality = getRate(TakerGets, TakerPays)
```

Higher quality means better rate for the taker (person consuming the offer).

### Quality Comparison for Crossing

When crossing offers:
- An incoming offer crosses existing offers with **equal or better** quality
- With `tfPassive`, only crosses offers with **strictly better** quality

---

## Offer Crossing Flow Diagram

```
┌─────────────────────────────────────────────────────────────┐
│                     OfferCreate Apply                        │
├─────────────────────────────────────────────────────────────┤
│  1. Cancel existing offer (if OfferSequence set)            │
│  2. Check expiration                                         │
│  3. Apply tick size rounding                                 │
│  4. flowCross() - attempt to cross existing offers          │
│     ├── Build paths (direct + XRP bridge if IOU/IOU)        │
│     ├── Call flow() with quality threshold                  │
│     ├── Remove stale offers encountered                     │
│     └── Calculate remaining amounts                         │
│  5. Handle FoK/IoC flags                                    │
│  6. Check reserve (if not crossed)                          │
│  7. Place remaining offer on books                          │
│     ├── Add to owner directory                              │
│     ├── Add to order book directory                         │
│     ├── Create offer ledger entry                           │
│     └── Handle hybrid (add to open book too)                │
└─────────────────────────────────────────────────────────────┘
```

---

## Implementation Checklist

- [ ] Preflight validates all 7 checks in order
- [ ] Preclaim validates funding, freeze, auth, domain
- [ ] Apply uses two sandboxes for FoK handling
- [ ] Quality calculated correctly (TakerGets/TakerPays)
- [ ] Taker amounts reversed for crossing (in=Gets, out=Pays)
- [ ] Passive flag increments threshold
- [ ] Transfer fee applied to sendMax
- [ ] Tick size rounding uses minimum of both issuers
- [ ] Reserve only checked when no crossing occurred
- [ ] Offer grooming removes stale offers during crossing
- [ ] Amendment-gated behavior implemented correctly
- [ ] All error codes returned at correct phases
