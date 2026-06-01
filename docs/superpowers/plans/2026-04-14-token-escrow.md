# TokenEscrow Amendment Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement the `featureTokenEscrow` amendment (XRPL v2.6.2) so EscrowCreate/Finish/Cancel support IOU and MPT amounts alongside XRP.

**Architecture:** Extend the existing escrow transaction handlers (`internal/tx/escrow/`) with IOU and MPT preclaim validation, lock/unlock helpers, transfer-rate tracking, and EscrowData parsing for non-XRP amounts. Add the `lsfAllowTrustLineLocking` account flag. Implement `rippleLockEscrowMPT` / `rippleUnlockEscrowMPT` as new helpers in the escrow package. Relocate shared helpers (requireAuth, isFrozen, transferRate, rippleCredit) from their current packages into the escrow package as thin wrappers or direct calls. All new logic is gated behind `amendment.FeatureTokenEscrow`.

**Tech Stack:** Go 1.24, XRPL binary codec, existing test framework (`internal/testing/`)

**Reference:** `rippled/src/xrpld/app/tx/detail/Escrow.cpp` (1410 lines), `rippled/src/xrpld/ledger/detail/View.cpp` (rippleLockEscrowMPT/rippleUnlockEscrowMPT), `rippled/src/test/app/EscrowToken_test.cpp` (3887 lines)

---

## File Map

### New files
| File | Responsibility |
|---|---|
| `internal/tx/escrow/token_helpers.go` | IOU/MPT preclaim validators, lock/unlock helpers, transfer-rate helpers |
| `internal/testing/escrow/token_escrow_test.go` | IOU escrow integration tests |
| `internal/testing/escrow/token_escrow_mpt_test.go` | MPT escrow integration tests |

### Modified files
| File | What changes |
|---|---|
| `internal/ledger/state/account_root.go` | Add `LsfAllowTrustLineLocking` constant (0x40000000) |
| `internal/tx/account/account_set.go` | Add `AccountSetFlagAllowTrustLineLocking = 17`, wire set/clear logic |
| `internal/ledger/state/escrow_entry.go` | Extend `EscrowData` with IOU amount, MPT amount, `TransferRate`, `IssuerNode`; update `ParseEscrow` |
| `internal/tx/escrow/escrow_create.go` | Add IOU/MPT preclaim, transfer-rate capture, `sfIssuerNode` serialization, MPT lock path |
| `internal/tx/escrow/escrow_finish.go` | Add IOU/MPT preclaim, transfer-rate fee logic, IOU/MPT unlock with trust-line/mptoken creation, issuer dir removal |
| `internal/tx/escrow/escrow_cancel.go` | Add IOU/MPT preclaim, IOU/MPT unlock back to sender, issuer dir removal |
| `internal/testing/escrow/builder.go` | Add IOU/MPT amount support to EscrowCreateBuilder |

---

## Task 1: Add `lsfAllowTrustLineLocking` Flag & AccountSet Support

This flag must exist before any IOU escrow validation can work. Rippled value: `0x40000000`, AccountSet index: `17`.

**Files:**
- Modify: `goXRPL/internal/ledger/state/account_root.go:128-131`
- Modify: `goXRPL/internal/tx/account/account_set.go:144-146` (constants) and Apply logic

- [ ] **Step 1: Add the ledger flag constant**

In `goXRPL/internal/ledger/state/account_root.go`, add before `LsfAllowTrustLineClawback`:

```go
// LsfAllowTrustLineLocking allows trust-line locking for token escrow
LsfAllowTrustLineLocking uint32 = 0x40000000
```

- [ ] **Step 2: Add the AccountSet flag constant**

In `goXRPL/internal/tx/account/account_set.go`, add after `AccountSetFlagAllowTrustLineClawback`:

```go
// AccountSetFlagAllowTrustLineLocking enables trust-line locking for token escrow
AccountSetFlagAllowTrustLineLocking uint32 = 17
```

- [ ] **Step 3: Wire set/clear logic in AccountSet.Apply()**

In the Apply method's flag-handling switch/if chain (around line 368+), add:

```go
// asfAllowTrustLineLocking — set/clear lsfAllowTrustLineLocking
// Reference: rippled AccountSet.cpp — featureTokenEscrow gated
if rules.Enabled(amendment.FeatureTokenEscrow) {
    if uSetFlag == AccountSetFlagAllowTrustLineLocking && (uFlagsIn&state.LsfAllowTrustLineLocking) == 0 {
        uFlagsIn |= state.LsfAllowTrustLineLocking
    }
    if uClearFlag == AccountSetFlagAllowTrustLineLocking && (uFlagsIn&state.LsfAllowTrustLineLocking) != 0 {
        uFlagsIn &^= state.LsfAllowTrustLineLocking
    }
}
```

- [ ] **Step 4: Run existing tests to verify no regressions**

Run: `go test ./goXRPL/internal/tx/account/...`
Expected: All existing tests pass.

- [ ] **Step 5: Commit**

```bash
git add goXRPL/internal/ledger/state/account_root.go goXRPL/internal/tx/account/account_set.go
git commit -m "feat(escrow): add lsfAllowTrustLineLocking flag and AccountSet support"
```

---

## Task 2: Extend EscrowData & ParseEscrow for IOU/MPT Amounts

The current `EscrowData` only stores `Amount uint64` for XRP drops. We need it to store IOU amounts (as `state.Amount`) and MPT amounts, plus the new `TransferRate` and `IssuerNode` fields.

**Files:**
- Modify: `goXRPL/internal/ledger/state/escrow_entry.go`

- [ ] **Step 1: Extend the EscrowData struct**

Replace the current struct with:

```go
type EscrowData struct {
    Account         [20]byte
    DestinationID   [20]byte
    Amount          uint64  // XRP drops (only valid when IsXRP is true)
    IsXRP           bool    // true if the escrow Amount is XRP
    IOUAmount       *Amount // non-nil for IOU escrows
    MPTAmount       *int64  // non-nil for MPT escrows (raw int64 value)
    MPTIssuanceID   string  // hex-encoded 48-char MPT issuance ID (set when MPT)
    Condition       string
    CancelAfter     uint32
    FinishAfter     uint32
    SourceTag       uint32
    HasSourceTag    bool
    DestinationTag  uint32
    HasDestTag      bool
    OwnerNode       uint64
    DestinationNode uint64
    HasDestNode     bool
    IssuerNode      uint64
    HasIssuerNode   bool
    TransferRate    uint32
    HasTransferRate bool
    Flags           uint32
}
```

- [ ] **Step 2: Update ParseEscrow to handle IOU amounts**

In the `FieldTypeAmount` case (around line 101), replace the IOU skip logic:

```go
case FieldTypeAmount:
    if offset+8 > len(data) {
        return escrow, nil
    }
    rawAmount := binary.BigEndian.Uint64(data[offset : offset+8])
    isIOU := rawAmount&0x8000000000000000 != 0
    if fieldCode == 1 { // sfAmount (field code 1 for Amount type)
        if isIOU {
            // IOU amount: 48 bytes total (8 value + 20 currency + 20 issuer)
            if offset+48 > len(data) {
                return escrow, nil
            }
            iouAmount, err := ParseAmountFromBinary(data[offset : offset+48])
            if err == nil {
                escrow.IOUAmount = &iouAmount
            }
            offset += 48
        } else {
            escrow.Amount = rawAmount & 0x3FFFFFFFFFFFFFFF
            escrow.IsXRP = true
            offset += 8
        }
    } else {
        // Other amount fields — skip
        if isIOU {
            if offset+48 > len(data) {
                return escrow, nil
            }
            offset += 48
        } else {
            offset += 8
        }
    }
```

Note: `ParseAmountFromBinary` should already exist or be added as a helper that creates an `Amount` from the 48-byte binary encoding.

- [ ] **Step 3: Handle MPT amounts in ParseEscrow**

MPT amounts in binary format are 57 bytes: 8 (value) + 24 (issuance ID) + 20 (issuer) + 3 (currency code = 0x00) + 2 extra. The binary codec detects MPT via the issuance ID presence in the serialized data. Since the binary codec already handles deserialization, the escrow SLE is serialized via the codec. We need to handle the case where Amount is MPT by checking if the `IOUAmount` returned has `IsMPT()` true, then extract the raw value:

After parsing the IOU amount in step 2, add:

```go
if iouAmount.IsMPT() {
    raw, ok := iouAmount.MPTRaw()
    if ok {
        escrow.MPTAmount = &raw
        escrow.MPTIssuanceID = iouAmount.MPTIssuanceID()
    }
    escrow.IOUAmount = &iouAmount // still keep for issuer info
}
```

- [ ] **Step 4: Parse TransferRate (UInt32 field code 11)**

In the `FieldTypeUInt32` switch, add:

```go
case 11: // TransferRate
    escrow.TransferRate = value
    escrow.HasTransferRate = true
```

- [ ] **Step 5: Parse IssuerNode (UInt64 field code 27)**

In the `FieldTypeUInt64` switch, add:

```go
case 27: // IssuerNode
    escrow.IssuerNode = value
    escrow.HasIssuerNode = true
```

- [ ] **Step 6: Run tests**

Run: `go test ./goXRPL/internal/ledger/state/... ./goXRPL/internal/testing/escrow/...`
Expected: All existing escrow tests still pass (XRP-only escrows unchanged).

- [ ] **Step 7: Commit**

```bash
git add goXRPL/internal/ledger/state/escrow_entry.go
git commit -m "feat(escrow): extend EscrowData and ParseEscrow for IOU/MPT amounts"
```

---

## Task 3: Update serializeEscrow for IOU/MPT + New Fields

The serializer must handle MPT amounts, `sfTransferRate`, and `sfIssuerNode`.

**Files:**
- Modify: `goXRPL/internal/tx/escrow/escrow_create.go` (serializeEscrow function, lines 329-391)

- [ ] **Step 1: Update the Amount serialization for MPT**

In `serializeEscrow`, update the amount serialization block:

```go
var amountVal any
if txn.Amount.IsNative() {
    amountVal = fmt.Sprintf("%d", txn.Amount.Drops())
} else if txn.Amount.IsMPT() {
    amountVal = map[string]any{
        "value":            txn.Amount.Value(),
        "mpt_issuance_id": txn.Amount.MPTIssuanceID(),
    }
} else {
    amountVal = map[string]any{
        "value":    txn.Amount.Value(),
        "currency": txn.Amount.Currency,
        "issuer":   txn.Amount.Issuer,
    }
}
```

- [ ] **Step 2: Add TransferRate and IssuerNode to the JSON map**

Add a `transferRate` and `issuerNode` parameter to `serializeEscrow` (or pass via a struct). After building `jsonObj`:

```go
if transferRate > 0 {
    jsonObj["TransferRate"] = transferRate
}
// IssuerNode is set after directory insertion in Apply, so it needs to be
// updated on the SLE after insertion. We handle this via a separate update.
```

Actually, `IssuerNode` is set during Apply after `DirInsert` returns the page number, similar to `OwnerNode`. The serializer creates the initial SLE; `IssuerNode` is written by updating the SLE after directory insertion.

- [ ] **Step 3: Run tests**

Run: `go test ./goXRPL/internal/tx/escrow/...`
Expected: All existing escrow unit tests pass.

- [ ] **Step 4: Commit**

```bash
git add goXRPL/internal/tx/escrow/escrow_create.go
git commit -m "feat(escrow): update serializeEscrow for IOU/MPT amounts and TransferRate"
```

---

## Task 4: Create Token Helpers (`token_helpers.go`)

This file houses all preclaim validators, lock/unlock helpers, and transfer-rate utilities needed by all three escrow transactions. Keep them in the escrow package to avoid circular imports.

**Files:**
- Create: `goXRPL/internal/tx/escrow/token_helpers.go`

- [ ] **Step 1: Write the IOU preclaim helper**

```go
package escrow

import (
    "github.com/LeJamon/go-xrpl/internal/ledger/state"
    "github.com/LeJamon/go-xrpl/internal/tx"
    "github.com/LeJamon/go-xrpl/keylet"
    "github.com/LeJamon/go-xrpl/ledger/entry"
)

// escrowCreatePreclaimIOU validates IOU-specific constraints for EscrowCreate.
// Reference: rippled Escrow.cpp escrowCreatePreclaimHelper<Issue> lines 204-279
func escrowCreatePreclaimIOU(view tx.LedgerView, accountID, destID [20]byte, amount tx.Amount) tx.Result {
    issuerID, err := state.DecodeAccountID(amount.Issuer)
    if err != nil {
        return tx.TefINTERNAL
    }

    // Issuer cannot create escrow of own tokens
    if issuerID == accountID {
        return tx.TecNO_PERMISSION
    }

    // Issuer must exist and have lsfAllowTrustLineLocking
    issuerKey := keylet.Account(issuerID)
    issuerData, err := view.Read(issuerKey)
    if err != nil || issuerData == nil {
        return tx.TecNO_ISSUER
    }
    issuerAccount, err := state.ParseAccountRoot(issuerData)
    if err != nil {
        return tx.TefINTERNAL
    }
    if (issuerAccount.Flags & state.LsfAllowTrustLineLocking) == 0 {
        return tx.TecNO_PERMISSION
    }

    // Trust line must exist
    trustLineKey := keylet.Line(accountID, issuerID, amount.Currency)
    trustLineData, err := view.Read(trustLineKey)
    if err != nil || trustLineData == nil {
        return tx.TecNO_LINE
    }
    rs, err := state.ParseRippleState(trustLineData)
    if err != nil {
        return tx.TefINTERNAL
    }

    // Balance direction validation
    balancePositive := rs.Balance.IsPositive()
    balanceNegative := rs.Balance.IsNegative()
    senderIsLow := state.CompareAccountIDsForLine(accountID, issuerID) < 0
    // If sender is low: balance should be negative (low owes high = sender owes issuer, i.e. sender holds IOU)
    // If sender is high: balance should be positive
    // rippled checks: balance > 0 && issuer < account → tecNO_PERMISSION
    //                 balance < 0 && issuer > account → tecNO_PERMISSION
    if balancePositive && !senderIsLow {
        // issuer < account, balance > 0
        return tx.TecNO_PERMISSION
    }
    if balanceNegative && senderIsLow {
        // issuer > account, balance < 0
        return tx.TecNO_PERMISSION
    }

    // requireAuth: sender authorized?
    if result := requireAuthIOU(view, issuerID, accountID, amount.Currency); result != tx.TesSUCCESS {
        return result
    }
    // requireAuth: destination authorized?
    if result := requireAuthIOU(view, issuerID, destID, amount.Currency); result != tx.TesSUCCESS {
        return result
    }

    // Frozen checks
    if isTrustlineFrozen(view, accountID, issuerID, amount.Currency) {
        return tx.TecFROZEN
    }
    if isTrustlineFrozen(view, destID, issuerID, amount.Currency) {
        return tx.TecFROZEN
    }

    // Spendable amount check (ignoring freeze — preclaim already checked)
    spendable := accountHoldsIOU(view, accountID, issuerID, amount.Currency)
    if spendable.IsZero() || spendable.IsNegative() {
        return tx.TecINSUFFICIENT_FUNDS
    }
    cmp, _ := spendable.Compare(amount)
    if cmp < 0 {
        return tx.TecINSUFFICIENT_FUNDS
    }

    // Precision loss check
    if !canAddIOUAmounts(spendable, amount) {
        return tx.TecPRECISION_LOSS
    }

    return tx.TesSUCCESS
}
```

- [ ] **Step 2: Write the MPT preclaim helper**

```go
// escrowCreatePreclaimMPT validates MPT-specific constraints for EscrowCreate.
// Reference: rippled Escrow.cpp escrowCreatePreclaimHelper<MPTIssue> lines 283-359
func escrowCreatePreclaimMPT(ctx *tx.ApplyContext, accountID, destID [20]byte, amount tx.Amount) tx.Result {
    rules := ctx.Rules()
    if !rules.Enabled(amendment.FeatureMPTokensV1) {
        return tx.TemDISABLED
    }

    issuerID, err := state.DecodeAccountID(amount.Issuer)
    if err != nil {
        return tx.TefINTERNAL
    }

    if issuerID == accountID {
        return tx.TecNO_PERMISSION
    }

    // MPTIssuance must exist and have lsfMPTCanEscrow
    mptID := amount.MPTIssuanceID()
    issuanceKey := keylet.MPTIssuanceByHexID(mptID)
    issuanceData, err := ctx.View.Read(issuanceKey)
    if err != nil || issuanceData == nil {
        return tx.TecOBJECT_NOT_FOUND
    }
    issuance, err := state.ParseMPTokenIssuance(issuanceData)
    if err != nil {
        return tx.TefINTERNAL
    }
    if (issuance.Flags & entry.LsfMPTCanEscrow) == 0 {
        return tx.TecNO_PERMISSION
    }
    // Issuer of issuance must match amount issuer
    if issuance.Issuer != issuerID {
        return tx.TecNO_PERMISSION
    }

    // Sender must hold MPToken
    senderTokenKey := keylet.MPToken(issuanceKey.Key, accountID)
    if exists, _ := ctx.View.Exists(senderTokenKey); !exists {
        return tx.TecOBJECT_NOT_FOUND
    }

    // requireAuth for sender and destination
    if result := requireMPTAuthForEscrow(ctx, issuance, issuanceKey, accountID, issuerID); result != tx.TesSUCCESS {
        return result
    }
    if result := requireMPTAuthForEscrow(ctx, issuance, issuanceKey, destID, issuerID); result != tx.TesSUCCESS {
        return result
    }

    // Frozen checks — MPT uses tecLOCKED, not tecFROZEN
    if isMPTFrozen(ctx.View, issuance, issuanceKey, accountID, issuerID) {
        return tx.TecLOCKED
    }
    if isMPTFrozen(ctx.View, issuance, issuanceKey, destID, issuerID) {
        return tx.TecLOCKED
    }

    // canTransfer: holder-to-holder requires CanTransfer flag
    if accountID != issuerID && destID != issuerID {
        if issuance.Flags&entry.LsfMPTCanTransfer == 0 {
            return tx.TecNO_AUTH
        }
    }

    // Sufficient balance check
    mptRaw, ok := amount.MPTRaw()
    if !ok || mptRaw <= 0 {
        return tx.TemBAD_AMOUNT
    }
    senderTokenData, _ := ctx.View.Read(senderTokenKey)
    senderToken, _ := state.ParseMPToken(senderTokenData)
    if senderToken == nil {
        return tx.TecINSUFFICIENT_FUNDS
    }
    available := int64(senderToken.MPTAmount)
    if senderToken.LockedAmount != nil {
        available -= int64(*senderToken.LockedAmount)
    }
    if available <= 0 || available < mptRaw {
        return tx.TecINSUFFICIENT_FUNDS
    }

    return tx.TesSUCCESS
}
```

- [ ] **Step 3: Write EscrowFinish preclaim helpers**

```go
// escrowFinishPreclaimIOU validates IOU-specific constraints for EscrowFinish.
// Reference: rippled Escrow.cpp escrowFinishPreclaimHelper<Issue> lines 702-724
func escrowFinishPreclaimIOU(view tx.LedgerView, destID [20]byte, amount tx.Amount) tx.Result {
    issuerID, err := state.DecodeAccountID(amount.Issuer)
    if err != nil {
        return tx.TefINTERNAL
    }
    // If destination is the issuer, no checks needed
    if issuerID == destID {
        return tx.TesSUCCESS
    }
    // requireAuth on destination
    if result := requireAuthIOU(view, issuerID, destID, amount.Currency); result != tx.TesSUCCESS {
        return result
    }
    // Deep freeze check on destination
    if isDeepFrozen(view, destID, issuerID, amount.Currency) {
        return tx.TecFROZEN
    }
    return tx.TesSUCCESS
}

// escrowFinishPreclaimMPT validates MPT-specific constraints for EscrowFinish.
// Reference: rippled Escrow.cpp escrowFinishPreclaimHelper<MPTIssue> lines 726-758
func escrowFinishPreclaimMPT(ctx *tx.ApplyContext, destID [20]byte, amount tx.Amount) tx.Result {
    issuerID, err := state.DecodeAccountID(amount.Issuer)
    if err != nil {
        return tx.TefINTERNAL
    }
    if issuerID == destID {
        return tx.TesSUCCESS
    }
    mptID := amount.MPTIssuanceID()
    issuanceKey := keylet.MPTIssuanceByHexID(mptID)
    issuanceData, err := ctx.View.Read(issuanceKey)
    if err != nil || issuanceData == nil {
        return tx.TecOBJECT_NOT_FOUND
    }
    issuance, err := state.ParseMPTokenIssuance(issuanceData)
    if err != nil {
        return tx.TefINTERNAL
    }
    if result := requireMPTAuthForEscrow(ctx, issuance, issuanceKey, destID, issuerID); result != tx.TesSUCCESS {
        return result
    }
    if isMPTFrozen(ctx.View, issuance, issuanceKey, destID, issuerID) {
        return tx.TecLOCKED
    }
    return tx.TesSUCCESS
}
```

- [ ] **Step 4: Write EscrowCancel preclaim helpers**

```go
// escrowCancelPreclaimIOU validates IOU-specific constraints for EscrowCancel.
// Reference: rippled Escrow.cpp escrowCancelPreclaimHelper<Issue> lines 1219-1237
func escrowCancelPreclaimIOU(view tx.LedgerView, accountID [20]byte, amount tx.Amount) tx.Result {
    issuerID, err := state.DecodeAccountID(amount.Issuer)
    if err != nil {
        return tx.TefINTERNAL
    }
    if issuerID == accountID {
        return tx.TecINTERNAL
    }
    if result := requireAuthIOU(view, issuerID, accountID, amount.Currency); result != tx.TesSUCCESS {
        return result
    }
    return tx.TesSUCCESS
}

// escrowCancelPreclaimMPT validates MPT-specific constraints for EscrowCancel.
// Reference: rippled Escrow.cpp escrowCancelPreclaimHelper<MPTIssue> lines 1239-1267
func escrowCancelPreclaimMPT(ctx *tx.ApplyContext, accountID [20]byte, amount tx.Amount) tx.Result {
    issuerID, err := state.DecodeAccountID(amount.Issuer)
    if err != nil {
        return tx.TefINTERNAL
    }
    if issuerID == accountID {
        return tx.TecINTERNAL
    }
    mptID := amount.MPTIssuanceID()
    issuanceKey := keylet.MPTIssuanceByHexID(mptID)
    issuanceData, err := ctx.View.Read(issuanceKey)
    if err != nil || issuanceData == nil {
        return tx.TecOBJECT_NOT_FOUND
    }
    issuance, err := state.ParseMPTokenIssuance(issuanceData)
    if err != nil {
        return tx.TefINTERNAL
    }
    if result := requireMPTAuthForEscrow(ctx, issuance, issuanceKey, accountID, issuerID); result != tx.TesSUCCESS {
        return result
    }
    return tx.TesSUCCESS
}
```

- [ ] **Step 5: Write shared utility helpers**

These thin wrappers adapt existing helpers or provide new logic:

```go
// requireAuthIOU checks if accountID is authorized for the IOU from issuerID.
// Mirrors rippled View.cpp requireAuth() for Issue.
func requireAuthIOU(view tx.LedgerView, issuerID, accountID [20]byte, currency string) tx.Result {
    if accountID == issuerID {
        return tx.TesSUCCESS
    }
    issuerKey := keylet.Account(issuerID)
    issuerData, err := view.Read(issuerKey)
    if err != nil || issuerData == nil {
        return tx.TesSUCCESS
    }
    issuerAccount, err := state.ParseAccountRoot(issuerData)
    if err != nil {
        return tx.TesSUCCESS
    }
    if (issuerAccount.Flags & state.LsfRequireAuth) == 0 {
        return tx.TesSUCCESS
    }
    trustLineKey := keylet.Line(accountID, issuerID, currency)
    trustLineData, err := view.Read(trustLineKey)
    if err != nil || trustLineData == nil {
        return tx.TecNO_LINE
    }
    rs, err := state.ParseRippleState(trustLineData)
    if err != nil {
        return tx.TecNO_AUTH
    }
    accountIsLow := state.CompareAccountIDsForLine(accountID, issuerID) < 0
    if accountIsLow {
        if rs.Flags&state.LsfLowAuth == 0 {
            return tx.TecNO_AUTH
        }
    } else {
        if rs.Flags&state.LsfHighAuth == 0 {
            return tx.TecNO_AUTH
        }
    }
    return tx.TesSUCCESS
}

// requireMPTAuthForEscrow wraps MPT auth check for escrow context.
// Uses WeakAuth semantics matching rippled's requireAuth(view, mptIssue, account, AuthType::WeakAuth).
func requireMPTAuthForEscrow(ctx *tx.ApplyContext, issuance *state.MPTokenIssuanceData,
    issuanceKey keylet.Keylet, accountID, issuerID [20]byte) tx.Result {
    if accountID == issuerID {
        return tx.TesSUCCESS
    }
    if (issuance.Flags & entry.LsfMPTRequireAuth) == 0 {
        return tx.TesSUCCESS
    }
    tokenKey := keylet.MPToken(issuanceKey.Key, accountID)
    tokenData, _ := ctx.View.Read(tokenKey)
    if tokenData == nil {
        return tx.TecNO_AUTH
    }
    token, err := state.ParseMPToken(tokenData)
    if err != nil || token == nil {
        return tx.TecNO_AUTH
    }
    if token.Flags&entry.LsfMPTAuthorized == 0 {
        return tx.TecNO_AUTH
    }
    return tx.TesSUCCESS
}

// isTrustlineFrozen checks if the trust line between accountID and issuerID is frozen.
// Delegates to tx.IsTrustlineFrozen.
func isTrustlineFrozen(view tx.LedgerView, accountID, issuerID [20]byte, currency string) bool {
    return tx.IsTrustlineFrozen(view, accountID, issuerID, currency)
}

// isDeepFrozen checks if the trust line is deep-frozen.
// Delegates to tx.IsDeepFrozen.
func isDeepFrozen(view tx.LedgerView, accountID, issuerID [20]byte, currency string) bool {
    return tx.IsDeepFrozen(view, accountID, issuerID, currency)
}

// isMPTFrozen checks if an MPT is frozen (globally or individually) for a given account.
// Reference: rippled isFrozen() for MPTIssue.
func isMPTFrozen(view tx.LedgerView, issuance *state.MPTokenIssuanceData,
    issuanceKey keylet.Keylet, accountID, issuerID [20]byte) bool {
    if accountID == issuerID {
        return false
    }
    // Global lock on issuance
    if issuance.Flags&entry.LsfMPTLocked != 0 {
        return true
    }
    // Individual lock on token
    tokenKey := keylet.MPToken(issuanceKey.Key, accountID)
    tokenData, _ := view.Read(tokenKey)
    if tokenData != nil {
        token, _ := state.ParseMPToken(tokenData)
        if token != nil && token.Flags&entry.LsfMPTLocked != 0 {
            return true
        }
    }
    return false
}

// accountHoldsIOU returns the spendable IOU balance for accountID.
func accountHoldsIOU(view tx.LedgerView, accountID, issuerID [20]byte, currency string) state.Amount {
    trustLineKey := keylet.Line(accountID, issuerID, currency)
    trustLineData, err := view.Read(trustLineKey)
    if err != nil || trustLineData == nil {
        return state.Amount{} // zero
    }
    rs, err := state.ParseRippleState(trustLineData)
    if err != nil {
        return state.Amount{}
    }
    // Convert balance to account's perspective
    accountIsLow := state.CompareAccountIDsForLine(accountID, issuerID) < 0
    bal := rs.Balance
    if !accountIsLow {
        bal = bal.Negate()
    }
    return bal
}

// canAddIOUAmounts checks if adding two IOU amounts would lose precision.
// Reference: rippled STAmount.cpp canAdd()
func canAddIOUAmounts(a, b state.Amount) bool {
    // If either is zero, always safe
    if a.IsZero() || b.IsZero() {
        return true
    }
    // For IOU: check that the sum doesn't lose more than 10^-4 relative precision
    sum, err := a.Add(b)
    if err != nil {
        return false
    }
    diff, _ := sum.Sub(a)
    // Check diff ≈ b within tolerance
    return !diff.IsZero() || b.IsZero()
}
```

- [ ] **Step 6: Write the MPT lock helper**

```go
// escrowLockMPT locks an MPT amount for escrow.
// Decreases sender's MPTAmount, increases sender's LockedAmount and issuance's LockedAmount.
// Reference: rippled View.cpp rippleLockEscrowMPT() lines 2853-2947
func escrowLockMPT(view tx.LedgerView, senderID [20]byte, amount tx.Amount) tx.Result {
    mptRaw, ok := amount.MPTRaw()
    if !ok || mptRaw <= 0 {
        return tx.TefINTERNAL
    }
    lockAmount := uint64(mptRaw)

    mptID := amount.MPTIssuanceID()
    issuanceKey := keylet.MPTIssuanceByHexID(mptID)

    // Update MPToken (sender): decrease MPTAmount, increase LockedAmount
    tokenKey := keylet.MPToken(issuanceKey.Key, senderID)
    tokenData, err := view.Read(tokenKey)
    if err != nil || tokenData == nil {
        return tx.TefINTERNAL
    }
    token, err := state.ParseMPToken(tokenData)
    if err != nil {
        return tx.TefINTERNAL
    }

    if token.MPTAmount < lockAmount {
        return tx.TecINSUFFICIENT_FUNDS
    }
    token.MPTAmount -= lockAmount

    currentLocked := uint64(0)
    if token.LockedAmount != nil {
        currentLocked = *token.LockedAmount
    }
    newLocked := currentLocked + lockAmount
    token.LockedAmount = &newLocked

    updatedToken, err := state.SerializeMPToken(token)
    if err != nil {
        return tx.TefINTERNAL
    }
    if err := view.Update(tokenKey, updatedToken); err != nil {
        return tx.TefINTERNAL
    }

    // Update MPTIssuance: increase LockedAmount
    issuanceData, err := view.Read(issuanceKey)
    if err != nil || issuanceData == nil {
        return tx.TefINTERNAL
    }
    issuance, err := state.ParseMPTokenIssuance(issuanceData)
    if err != nil {
        return tx.TefINTERNAL
    }

    issuanceLocked := uint64(0)
    if issuance.LockedAmount != nil {
        issuanceLocked = *issuance.LockedAmount
    }
    newIssuanceLocked := issuanceLocked + lockAmount
    issuance.LockedAmount = &newIssuanceLocked

    updatedIssuance, err := state.SerializeMPTokenIssuance(issuance)
    if err != nil {
        return tx.TefINTERNAL
    }
    if err := view.Update(issuanceKey, updatedIssuance); err != nil {
        return tx.TefINTERNAL
    }

    return tx.TesSUCCESS
}
```

- [ ] **Step 7: Write the MPT unlock helper**

```go
// escrowUnlockMPT unlocks an MPT amount from escrow to receiver.
// If receiver is the issuer, decreases OutstandingAmount (tokens are destroyed).
// Otherwise, increases receiver's MPTAmount.
// Always decreases sender's LockedAmount and issuance's LockedAmount.
// Reference: rippled View.cpp rippleUnlockEscrowMPT() lines 2950-3094
func escrowUnlockMPT(view tx.LedgerView, senderID, receiverID [20]byte, finalAmount uint64) tx.Result {
    // We need the issuance — read from sender's token
    // First find the issuance key from sender's MPToken
    // Actually we need the issuance ID passed in. Let's add it as a parameter.
    return tx.TefINTERNAL // placeholder — see full signature below
}

// escrowUnlockMPTFull unlocks MPT from escrow.
// Reference: rippled View.cpp rippleUnlockEscrowMPT() lines 2950-3094
func escrowUnlockMPTFull(view tx.LedgerView, senderID, receiverID [20]byte,
    originalAmount, finalAmount uint64, mptID string) tx.Result {

    issuanceKey := keylet.MPTIssuanceByHexID(mptID)

    // Read issuance
    issuanceData, err := view.Read(issuanceKey)
    if err != nil || issuanceData == nil {
        return tx.TefINTERNAL
    }
    issuance, err := state.ParseMPTokenIssuance(issuanceData)
    if err != nil {
        return tx.TefINTERNAL
    }

    issuerID := issuance.Issuer
    receiverIsIssuer := receiverID == issuerID

    // Decrease issuance LockedAmount by original (pre-fee) amount
    issuanceLocked := uint64(0)
    if issuance.LockedAmount != nil {
        issuanceLocked = *issuance.LockedAmount
    }
    if issuanceLocked < originalAmount {
        return tx.TefINTERNAL
    }
    newIssuanceLocked := issuanceLocked - originalAmount
    issuance.LockedAmount = &newIssuanceLocked

    if receiverIsIssuer {
        // Tokens sent to issuer are destroyed
        if issuance.OutstandingAmount < finalAmount {
            return tx.TefINTERNAL
        }
        issuance.OutstandingAmount -= finalAmount
    } else {
        // Increase receiver's MPTAmount
        receiverTokenKey := keylet.MPToken(issuanceKey.Key, receiverID)
        receiverTokenData, err := view.Read(receiverTokenKey)
        if err != nil || receiverTokenData == nil {
            return tx.TefINTERNAL
        }
        receiverToken, err := state.ParseMPToken(receiverTokenData)
        if err != nil {
            return tx.TefINTERNAL
        }
        receiverToken.MPTAmount += finalAmount
        updatedReceiver, err := state.SerializeMPToken(receiverToken)
        if err != nil {
            return tx.TefINTERNAL
        }
        if err := view.Update(receiverTokenKey, updatedReceiver); err != nil {
            return tx.TefINTERNAL
        }
    }

    // Decrease sender's LockedAmount
    senderTokenKey := keylet.MPToken(issuanceKey.Key, senderID)
    senderTokenData, err := view.Read(senderTokenKey)
    if err != nil || senderTokenData == nil {
        return tx.TefINTERNAL
    }
    senderToken, err := state.ParseMPToken(senderTokenData)
    if err != nil {
        return tx.TefINTERNAL
    }
    senderLocked := uint64(0)
    if senderToken.LockedAmount != nil {
        senderLocked = *senderToken.LockedAmount
    }
    if senderLocked < originalAmount {
        return tx.TefINTERNAL
    }
    newSenderLocked := senderLocked - originalAmount
    if newSenderLocked == 0 {
        senderToken.LockedAmount = nil
    } else {
        senderToken.LockedAmount = &newSenderLocked
    }
    updatedSender, err := state.SerializeMPToken(senderToken)
    if err != nil {
        return tx.TefINTERNAL
    }
    if err := view.Update(senderTokenKey, updatedSender); err != nil {
        return tx.TefINTERNAL
    }

    // Write back issuance
    updatedIssuance, err := state.SerializeMPTokenIssuance(issuance)
    if err != nil {
        return tx.TefINTERNAL
    }
    if err := view.Update(issuanceKey, updatedIssuance); err != nil {
        return tx.TefINTERNAL
    }

    return tx.TesSUCCESS
}
```

- [ ] **Step 8: Write the IOU unlock helper**

```go
// escrowUnlockIOU unlocks an IOU amount from escrow to receiver.
// Handles transfer-rate fee deduction, trust-line creation, and limit checks.
// Reference: rippled Escrow.cpp escrowUnlockApplyHelper<Issue> lines 809-942
func escrowUnlockIOU(ctx *tx.ApplyContext, lockedRate uint32,
    destAccount *state.AccountRoot, destID [20]byte,
    amount tx.Amount, senderID, receiverID [20]byte,
    createAsset bool) tx.Result {

    issuerID, err := state.DecodeAccountID(amount.Issuer)
    if err != nil {
        return tx.TefINTERNAL
    }

    senderIsIssuer := issuerID == senderID
    receiverIsIssuer := issuerID == receiverID

    if senderIsIssuer {
        return tx.TecINTERNAL
    }
    if receiverIsIssuer {
        return tx.TesSUCCESS
    }

    // Trust-line auto-creation for destination if submitter == destination
    trustLineKey := keylet.Line(receiverID, issuerID, amount.Currency)
    trustLineExists, _ := ctx.View.Exists(trustLineKey)

    if !trustLineExists && createAsset {
        // Check reserve for new trust line
        reserve := ctx.AccountReserve(destAccount.OwnerCount + 1)
        if destAccount.Balance < reserve {
            return tx.TecNO_LINE_INSUF_RESERVE
        }
        // Create the trust line
        if result := createTrustLineForEscrow(ctx.View, issuerID, receiverID,
            amount.Currency, destAccount, trustLineKey); result != tx.TesSUCCESS {
            return result
        }
        trustLineExists = true
    }

    if !trustLineExists {
        return tx.TecNO_LINE
    }

    // Compute transfer fee
    finalAmount := amount
    parityRate := uint32(1_000_000_000)

    if lockedRate == 0 {
        lockedRate = parityRate
    }

    // Get current transfer rate — use the lower of locked vs current
    currentRate := getTransferRateForIssuer(ctx.View, issuerID)
    if currentRate < lockedRate {
        lockedRate = currentRate
    }

    if !senderIsIssuer && !receiverIsIssuer && lockedRate != parityRate {
        // Fee = amount - divideRound(amount, rate)
        // divideRound: amount * 1e9 / rate, rounded up
        finalAmount = divideAmountByRate(amount, lockedRate)
    }

    // Limit check: if submitter != receiver
    if !createAsset {
        if result := checkTrustLineLimit(ctx.View, receiverID, issuerID,
            amount.Currency, finalAmount); result != tx.TesSUCCESS {
            return result
        }
    }

    // Transfer: rippleCredit from issuer to receiver
    return rippleCreditForEscrow(ctx.View, issuerID, receiverID, finalAmount)
}
```

- [ ] **Step 9: Write transfer-rate and trust-line utility functions**

```go
// getTransferRateForIssuer returns the transfer rate for an IOU issuer.
// Returns parityRate (1e9) if no rate is set.
func getTransferRateForIssuer(view tx.LedgerView, issuerID [20]byte) uint32 {
    accountKey := keylet.Account(issuerID)
    accountData, err := view.Read(accountKey)
    if err != nil || accountData == nil {
        return 1_000_000_000
    }
    account, err := state.ParseAccountRoot(accountData)
    if err != nil {
        return 1_000_000_000
    }
    if account.TransferRate == 0 {
        return 1_000_000_000
    }
    return account.TransferRate
}

// getMPTTransferRate returns the transfer rate for an MPT issuance.
// Formula: fee * 10000 + 1_000_000_000
func getMPTTransferRate(issuance *state.MPTokenIssuanceData) uint32 {
    if issuance.TransferFee == 0 {
        return 1_000_000_000
    }
    return uint32(issuance.TransferFee)*10_000 + 1_000_000_000
}

// divideAmountByRate computes amount * 1e9 / rate (rounded up) for IOU fee deduction.
// This gives the post-fee amount that the receiver gets.
func divideAmountByRate(amount tx.Amount, rate uint32) tx.Amount {
    // Use amount.MulRatio(1e9, rate, roundUp=true) to divide by rate
    // This is equivalent to rippled's divideRound(amount, rate, ...)
    return amount.MulRatio(1_000_000_000, rate, true)
}

// checkTrustLineLimit verifies that crediting finalAmount won't exceed the trust line limit.
// Reference: rippled Escrow.cpp lines 906-931
func checkTrustLineLimit(view tx.LedgerView, receiverID, issuerID [20]byte,
    currency string, finalAmount tx.Amount) tx.Result {
    trustLineKey := keylet.Line(receiverID, issuerID, currency)
    tlData, err := view.Read(trustLineKey)
    if err != nil || tlData == nil {
        return tx.TecINTERNAL
    }
    rs, err := state.ParseRippleState(tlData)
    if err != nil {
        return tx.TecINTERNAL
    }

    issuerHigh := state.CompareAccountIDsForLine(issuerID, receiverID) > 0
    var lineLimit, lineBalance state.Amount
    if issuerHigh {
        lineLimit = rs.LowLimit
    } else {
        lineLimit = rs.HighLimit
    }
    lineBalance = rs.Balance
    if !issuerHigh {
        lineBalance = lineBalance.Negate()
    }

    newBalance, err := lineBalance.Add(finalAmount)
    if err != nil {
        return tx.TecINTERNAL
    }
    cmp, _ := lineLimit.Compare(newBalance)
    if cmp < 0 {
        return tx.TecLIMIT_EXCEEDED
    }
    return tx.TesSUCCESS
}

// createTrustLineForEscrow creates a trust line for the escrow destination.
// Reference: rippled Escrow.cpp trustCreate() call at lines 854-877
func createTrustLineForEscrow(view tx.LedgerView, issuerID, receiverID [20]byte,
    currency string, destAccount *state.AccountRoot, trustLineKey keylet.Keylet) tx.Result {
    // Delegate to the existing trust-line creation infrastructure.
    // This creates a zero-balance trust line with proper flags.
    zeroAmount := state.NewIssuedAmountFromValue(0, 0, currency, "")
    return createTrustLineZeroBalance(view, issuerID, receiverID, zeroAmount, trustLineKey, destAccount)
}

// rippleCreditForEscrow transfers IOU from issuer to receiver via trust line.
// Wraps existing rippleCredit logic.
func rippleCreditForEscrow(view tx.LedgerView, issuerID, receiverID [20]byte, amount tx.Amount) tx.Result {
    // Use escrowLockIOU in reverse: credit from issuer to receiver
    // issuer → receiver means receiver's balance increases
    trustLineKey := keylet.Line(receiverID, issuerID, amount.Currency)
    trustLineData, err := view.Read(trustLineKey)
    if err != nil || trustLineData == nil {
        return tx.TecNO_LINE
    }
    rs, err := state.ParseRippleState(trustLineData)
    if err != nil {
        return tx.TefINTERNAL
    }

    receiverIsLow := state.CompareAccountIDsForLine(receiverID, issuerID) < 0
    if receiverIsLow {
        // Receiver is low: increasing receiver's balance means making balance more negative
        // (issuer pays receiver) — subtract from balance
        newBalance, err := rs.Balance.Sub(amount)
        if err != nil {
            return tx.TefINTERNAL
        }
        rs.Balance = newBalance
    } else {
        // Receiver is high: increasing receiver's balance means making balance more positive
        newBalance, err := rs.Balance.Add(amount)
        if err != nil {
            return tx.TefINTERNAL
        }
        rs.Balance = newBalance
    }

    updated, err := state.SerializeRippleState(rs)
    if err != nil {
        return tx.TefINTERNAL
    }
    if err := view.Update(trustLineKey, updated); err != nil {
        return tx.TefINTERNAL
    }
    return tx.TesSUCCESS
}
```

- [ ] **Step 10: Run compilation check**

Run: `go build ./goXRPL/...`
Expected: Compiles with no errors. (Tests may not pass yet since transaction Apply methods haven't been updated.)

- [ ] **Step 11: Commit**

```bash
git add goXRPL/internal/tx/escrow/token_helpers.go
git commit -m "feat(escrow): add IOU/MPT preclaim validators, lock/unlock helpers"
```

---

## Task 5: Update EscrowCreate.Apply() for Token Escrow

Wire the preclaim helpers and MPT lock path into `EscrowCreate.Apply()`.

**Files:**
- Modify: `goXRPL/internal/tx/escrow/escrow_create.go`

- [ ] **Step 1: Add IOU/MPT preclaim in Apply after destination lookup**

After the destination lookup and before the reserve check (around line 212), add:

```go
// Token escrow preclaim validation
// Reference: rippled Escrow.cpp EscrowCreate::preclaim() lines 362-395
if !isNative && rules.Enabled(amendment.FeatureTokenEscrow) {
    if e.Amount.IsMPT() {
        if result := escrowCreatePreclaimMPT(ctx, ctx.AccountID, destID, e.Amount); result != tx.TesSUCCESS {
            return result
        }
    } else {
        if result := escrowCreatePreclaimIOU(ctx.View, ctx.AccountID, destID, e.Amount); result != tx.TesSUCCESS {
            return result
        }
    }
}
```

- [ ] **Step 2: Add transfer-rate capture to serialization**

After `serializeEscrow()` call, before `ctx.View.Insert()`, add transfer-rate capture. Modify `serializeEscrow` to accept a `transferRate uint32` parameter:

```go
// Capture transfer rate at creation time for non-XRP escrows
var capturedTransferRate uint32
if rules.Enabled(amendment.FeatureTokenEscrow) && !isNative {
    if e.Amount.IsMPT() {
        // MPT: get rate from issuance
        mptID := e.Amount.MPTIssuanceID()
        issuanceKey := keylet.MPTIssuanceByHexID(mptID)
        issuanceData, _ := ctx.View.Read(issuanceKey)
        if issuanceData != nil {
            issuance, _ := state.ParseMPTokenIssuance(issuanceData)
            if issuance != nil {
                capturedTransferRate = getMPTTransferRate(issuance)
            }
        }
    } else {
        // IOU: get rate from issuer account
        issuerID, _ := state.DecodeAccountID(e.Amount.Issuer)
        capturedTransferRate = getTransferRateForIssuer(ctx.View, issuerID)
    }
}

escrowData, err := serializeEscrow(e, accountID, destID, sequence, capturedTransferRate)
```

Update `serializeEscrow` signature to include `transferRate uint32` and add to JSON map when non-zero:

```go
if transferRate > 0 && transferRate != 1_000_000_000 {
    jsonObj["TransferRate"] = transferRate
}
```

- [ ] **Step 3: Add MPT lock path in the balance deduction section**

After the IOU lock path (line 318-321), add MPT:

```go
} else if e.Amount.IsMPT() {
    // MPT: lock via MPToken/MPTIssuance fields
    if lockResult := escrowLockMPT(ctx.View, accountID, e.Amount); lockResult != tx.TesSUCCESS {
        return lockResult
    }
} else {
```

- [ ] **Step 4: Store IssuerNode after directory insertion**

The IssuerNode from `DirInsert` needs to be stored on the SLE. After the issuer directory insertion block, update the SLE with the page number. This requires re-reading, updating, and writing back the escrow SLE.

```go
if !isNative && !e.Amount.IsMPT() {
    issuerID, issuerErr := state.DecodeAccountID(e.Amount.Issuer)
    if issuerErr == nil && issuerID != accountID && issuerID != destID {
        issuerDirKey := keylet.OwnerDir(issuerID)
        issuerPage, err := state.DirInsert(ctx.View, issuerDirKey, escrowKey.Key, func(dir *state.DirectoryNode) {
            dir.Owner = issuerID
        })
        if err != nil {
            return tx.TecDIR_FULL
        }
        // Update the escrow SLE with IssuerNode
        updateEscrowField(ctx.View, escrowKey, "IssuerNode", issuerPage)
    }
}
```

- [ ] **Step 5: Run tests**

Run: `go test ./goXRPL/internal/tx/escrow/... ./goXRPL/internal/testing/escrow/...`
Expected: Existing XRP escrow tests still pass.

- [ ] **Step 6: Commit**

```bash
git add goXRPL/internal/tx/escrow/escrow_create.go
git commit -m "feat(escrow): wire IOU/MPT preclaim, transfer-rate capture, MPT lock into EscrowCreate"
```

---

## Task 6: Update EscrowFinish.Apply() for Token Escrow

The heaviest change — must handle preclaim, transfer-rate fee deduction, IOU/MPT unlock with trust-line/MPToken auto-creation, and issuer directory cleanup.

**Files:**
- Modify: `goXRPL/internal/tx/escrow/escrow_finish.go`

- [ ] **Step 1: Add token preclaim after escrow parsing**

After `state.ParseEscrow()` (line 164), add:

```go
// Token escrow preclaim
// Reference: rippled EscrowFinish::preclaim() lines 760-793
isXRP := escrowEntry.IsXRP
if !isXRP && rules.Enabled(amendment.FeatureTokenEscrow) {
    escrowAmount := reconstructAmountFromEscrow(escrowEntry)
    if escrowAmount.IsMPT() {
        if result := escrowFinishPreclaimMPT(ctx, escrowEntry.DestinationID, escrowAmount); result != tx.TesSUCCESS {
            return result
        }
    } else {
        if result := escrowFinishPreclaimIOU(ctx.View, escrowEntry.DestinationID, escrowAmount); result != tx.TesSUCCESS {
            return result
        }
    }
}
```

- [ ] **Step 2: Replace the balance transfer section with IOU/MPT support**

Replace the current simple `destAccount.Balance += escrowEntry.Amount` block (line 281) with:

```go
// Transfer the escrowed amount to destination
if isXRP {
    destAccount.Balance += escrowEntry.Amount
} else {
    if !rules.Enabled(amendment.FeatureTokenEscrow) {
        return tx.TemDISABLED
    }

    escrowAmount := reconstructAmountFromEscrow(escrowEntry)
    lockedRate := uint32(0)
    if escrowEntry.HasTransferRate {
        lockedRate = escrowEntry.TransferRate
    }

    createAsset := escrowEntry.DestinationID == ctx.AccountID

    if escrowAmount.IsMPT() {
        if result := finishMPTEscrow(ctx, lockedRate, destAccount,
            escrowEntry, escrowAmount, createAsset); result != tx.TesSUCCESS {
            return result
        }
    } else {
        if result := escrowUnlockIOU(ctx, lockedRate, destAccount,
            escrowEntry.DestinationID, escrowAmount,
            escrowEntry.Account, escrowEntry.DestinationID,
            createAsset); result != tx.TesSUCCESS {
            return result
        }
    }

    // Remove escrow from issuer's owner directory
    if escrowEntry.HasIssuerNode {
        issuerID, _ := state.DecodeAccountID(escrowAmount.Issuer)
        issuerDirKey := keylet.OwnerDir(issuerID)
        state.DirRemove(ctx.View, issuerDirKey, escrowEntry.IssuerNode, escrowKey.Key, false)
    }
}
```

- [ ] **Step 3: Add the finishMPTEscrow helper**

```go
// finishMPTEscrow handles MPT unlock for EscrowFinish including MPToken auto-creation.
func finishMPTEscrow(ctx *tx.ApplyContext, lockedRate uint32,
    destAccount *state.AccountRoot, escrow *state.EscrowData,
    amount tx.Amount, createAsset bool) tx.Result {

    mptID := amount.MPTIssuanceID()
    issuanceKey := keylet.MPTIssuanceByHexID(mptID)
    issuerID, _ := state.DecodeAccountID(amount.Issuer)

    receiverIsIssuer := escrow.DestinationID == issuerID

    // Auto-create MPToken for destination if needed
    if !receiverIsIssuer {
        destTokenKey := keylet.MPToken(issuanceKey.Key, escrow.DestinationID)
        if exists, _ := ctx.View.Exists(destTokenKey); !exists && createAsset {
            reserve := ctx.AccountReserve(destAccount.OwnerCount + 1)
            if destAccount.Balance < reserve {
                return tx.TecINSUFFICIENT_RESERVE
            }
            if result := createMPTokenForEscrow(ctx.View, issuanceKey, escrow.DestinationID); result != tx.TesSUCCESS {
                return result
            }
            destAccount.OwnerCount++
        }
        if exists, _ := ctx.View.Exists(destTokenKey); !exists && !receiverIsIssuer {
            return tx.TecNO_PERMISSION
        }
    }

    // Compute transfer fee
    mptRaw, _ := amount.MPTRaw()
    originalAmount := uint64(mptRaw)
    finalAmount := originalAmount

    issuanceData, _ := ctx.View.Read(issuanceKey)
    issuance, _ := state.ParseMPTokenIssuance(issuanceData)

    parityRate := uint32(1_000_000_000)
    if lockedRate == 0 {
        lockedRate = parityRate
    }

    if issuance != nil {
        currentRate := getMPTTransferRate(issuance)
        if currentRate < lockedRate {
            lockedRate = currentRate
        }
    }

    senderIsIssuer := escrow.Account == issuerID
    if !senderIsIssuer && !receiverIsIssuer && lockedRate != parityRate {
        // Fee = original - divideRound(original, rate)
        postFee := mptDivideByRate(originalAmount, lockedRate)
        finalAmount = postFee
    }

    return escrowUnlockMPTFull(ctx.View, escrow.Account, escrow.DestinationID,
        originalAmount, finalAmount, mptID)
}
```

- [ ] **Step 4: Add reconstructAmountFromEscrow helper**

This reconstructs a `tx.Amount` from the parsed `EscrowData`:

```go
// reconstructAmountFromEscrow creates a tx.Amount from parsed escrow data.
func reconstructAmountFromEscrow(escrow *state.EscrowData) tx.Amount {
    if escrow.IsXRP {
        return tx.NewXRPAmount(int64(escrow.Amount))
    }
    if escrow.IOUAmount != nil {
        // Convert state.Amount to tx.Amount
        return convertStateAmountToTx(*escrow.IOUAmount)
    }
    return tx.Amount{} // should not happen
}
```

- [ ] **Step 5: Run tests**

Run: `go test ./goXRPL/internal/tx/escrow/... ./goXRPL/internal/testing/escrow/...`
Expected: Existing XRP escrow tests still pass.

- [ ] **Step 6: Commit**

```bash
git add goXRPL/internal/tx/escrow/escrow_finish.go goXRPL/internal/tx/escrow/token_helpers.go
git commit -m "feat(escrow): wire IOU/MPT unlock, transfer-rate fees into EscrowFinish"
```

---

## Task 7: Update EscrowCancel.Apply() for Token Escrow

Return escrowed IOU/MPT to the sender, with preclaim validation and issuer directory cleanup.

**Files:**
- Modify: `goXRPL/internal/tx/escrow/escrow_cancel.go`

- [ ] **Step 1: Add token preclaim after escrow parsing**

After `state.ParseEscrow()` (line 94), add:

```go
isXRP := escrowEntry.IsXRP
if !isXRP && rules.Enabled(amendment.FeatureTokenEscrow) {
    escrowAmount := reconstructAmountFromEscrow(escrowEntry)
    if escrowAmount.IsMPT() {
        if result := escrowCancelPreclaimMPT(ctx, escrowEntry.Account, escrowAmount); result != tx.TesSUCCESS {
            return result
        }
    } else {
        if result := escrowCancelPreclaimIOU(ctx.View, escrowEntry.Account, escrowAmount); result != tx.TesSUCCESS {
            return result
        }
    }
}
```

- [ ] **Step 2: Replace the balance return section with IOU/MPT support**

Replace the current `ownerAccount.Balance += escrowEntry.Amount` logic with:

```go
if isXRP {
    // Existing XRP return logic (unchanged)
    if ownerIsSelf {
        ctx.Account.Balance += escrowEntry.Amount
    } else {
        // ... existing code to read/update owner account
    }
} else {
    if !rules.Enabled(amendment.FeatureTokenEscrow) {
        return tx.TemDISABLED
    }

    escrowAmount := reconstructAmountFromEscrow(escrowEntry)
    // For cancel, sender == receiver. Transfer rate = parityRate (no fee).
    createAsset := escrowEntry.Account == ctx.AccountID

    if escrowAmount.IsMPT() {
        mptRaw, _ := escrowAmount.MPTRaw()
        originalAmount := uint64(mptRaw)
        if result := escrowUnlockMPTFull(ctx.View, escrowEntry.Account, escrowEntry.Account,
            originalAmount, originalAmount, escrowAmount.MPTIssuanceID()); result != tx.TesSUCCESS {
            return result
        }
    } else {
        // IOU: unlock back to sender with parityRate (no fee)
        var ownerAccount *state.AccountRoot
        if ownerIsSelf {
            ownerAccount = ctx.Account
        } else {
            ownerData, _ := ctx.View.Read(keylet.Account(ownerID))
            ownerAccount, _ = state.ParseAccountRoot(ownerData)
        }
        if result := escrowUnlockIOU(ctx, 1_000_000_000, ownerAccount,
            escrowEntry.Account, escrowAmount,
            escrowEntry.Account, escrowEntry.Account,
            createAsset); result != tx.TesSUCCESS {
            return result
        }
    }

    // Remove escrow from issuer's owner directory
    if escrowEntry.HasIssuerNode {
        issuerID, _ := state.DecodeAccountID(escrowAmount.Issuer)
        issuerDirKey := keylet.OwnerDir(issuerID)
        state.DirRemove(ctx.View, issuerDirKey, escrowEntry.IssuerNode, escrowKey.Key, false)
    }
}
```

- [ ] **Step 3: Keep XRP OwnerCount decrement separate from token path**

The `OwnerCount` decrement at the end of Apply should still work for both XRP and token escrows since it decrements the creator's OwnerCount regardless of asset type. Verify the existing code handles this.

- [ ] **Step 4: Run tests**

Run: `go test ./goXRPL/internal/tx/escrow/... ./goXRPL/internal/testing/escrow/...`
Expected: Existing XRP escrow tests still pass.

- [ ] **Step 5: Commit**

```bash
git add goXRPL/internal/tx/escrow/escrow_cancel.go
git commit -m "feat(escrow): wire IOU/MPT unlock back to sender in EscrowCancel"
```

---

## Task 8: Update Test Builders for IOU/MPT Escrow

Extend the escrow test builders to support IOU and MPT amounts.

**Files:**
- Modify: `goXRPL/internal/testing/escrow/builder.go`

- [ ] **Step 1: Update EscrowCreateBuilder to support IOU/MPT**

Add an `iouAmount` and `mptAmount` field to `EscrowCreateBuilder`:

```go
type EscrowCreateBuilder struct {
    from        *testing.Account
    to          *testing.Account
    amount      int64      // XRP in drops
    iouAmount   *tx.Amount // IOU amount (nil = XRP)
    mptAmount   *tx.Amount // MPT amount (nil = not MPT)
    // ... existing fields
}
```

Add builder methods:

```go
// IOUAmount sets an IOU amount for token escrow.
func (b *EscrowCreateBuilder) IOUAmount(amount tx.Amount) *EscrowCreateBuilder {
    b.iouAmount = &amount
    return b
}

// MPTAmount sets an MPT amount for token escrow.
func (b *EscrowCreateBuilder) MPTAmount(amount tx.Amount) *EscrowCreateBuilder {
    b.mptAmount = &amount
    return b
}
```

Update `Build()` to use the IOU/MPT amount:

```go
func (b *EscrowCreateBuilder) Build() tx.Transaction {
    var amount tx.Amount
    if b.mptAmount != nil {
        amount = *b.mptAmount
    } else if b.iouAmount != nil {
        amount = *b.iouAmount
    } else {
        amount = tx.NewXRPAmount(b.amount)
    }
    e := escrowtx.NewEscrowCreate(b.from.Address, b.to.Address, amount)
    // ... rest unchanged
}
```

- [ ] **Step 2: Run compilation check**

Run: `go build ./goXRPL/internal/testing/escrow/...`
Expected: Compiles.

- [ ] **Step 3: Commit**

```bash
git add goXRPL/internal/testing/escrow/builder.go
git commit -m "feat(escrow): add IOU/MPT amount support to test builders"
```

---

## Task 9: IOU Escrow Integration Tests

Test the full IOU escrow lifecycle: create → finish and create → cancel.

**Files:**
- Create: `goXRPL/internal/testing/escrow/token_escrow_test.go`

- [ ] **Step 1: Write IOU enablement test**

Test that IOU escrow fails without `featureTokenEscrow` and succeeds with it.

```go
func TestIOUEscrow_Enablement(t *testing.T) {
    // Without TokenEscrow amendment: temBAD_AMOUNT
    // With TokenEscrow amendment: succeeds (given all other conditions met)
}
```

- [ ] **Step 2: Write lsfAllowTrustLineLocking test**

Test that IOU escrow requires the issuer to have `lsfAllowTrustLineLocking`:

```go
func TestIOUEscrow_AllowLockingFlag(t *testing.T) {
    // Issuer without flag: tecNO_PERMISSION
    // Issuer with flag: succeeds
}
```

- [ ] **Step 3: Write IOU create preclaim tests**

```go
func TestIOUEscrow_CreatePreclaim(t *testing.T) {
    t.Run("IssuerCannotEscrowOwnToken", ...)   // tecNO_PERMISSION
    t.Run("NoTrustLine", ...)                   // tecNO_LINE
    t.Run("InsufficientFunds", ...)             // tecINSUFFICIENT_FUNDS
    t.Run("RequireAuthNotAuthorized", ...)      // tecNO_AUTH
    t.Run("FrozenTrustLine", ...)               // tecFROZEN
    t.Run("FrozenDestination", ...)             // tecFROZEN
}
```

- [ ] **Step 4: Write IOU finish/cancel lifecycle tests**

```go
func TestIOUEscrow_FinishBasic(t *testing.T) {
    // Create IOU escrow, advance time, finish — verify balances
}

func TestIOUEscrow_CancelBasic(t *testing.T) {
    // Create IOU escrow, advance past cancel time, cancel — verify amounts returned
}

func TestIOUEscrow_FinishWithTransferRate(t *testing.T) {
    // Set transfer rate on issuer, create escrow, finish — verify fee deducted
}

func TestIOUEscrow_FinishPreclaim(t *testing.T) {
    t.Run("DeepFrozenDest", ...)         // tecFROZEN
    t.Run("RequireAuthDestNotAuth", ...)  // tecNO_AUTH
}

func TestIOUEscrow_TrustLineAutoCreation(t *testing.T) {
    // Destination has no trust line, submits EscrowFinish for self — creates trust line
}

func TestIOUEscrow_TrustLineLimitExceeded(t *testing.T) {
    // Trust line at limit, finish would exceed — tecLIMIT_EXCEEDED
}
```

- [ ] **Step 5: Run tests**

Run: `go test -v ./goXRPL/internal/testing/escrow/... -run TestIOUEscrow`
Expected: All IOU escrow tests pass.

- [ ] **Step 6: Commit**

```bash
git add goXRPL/internal/testing/escrow/token_escrow_test.go
git commit -m "test(escrow): add IOU escrow integration tests"
```

---

## Task 10: MPT Escrow Integration Tests

Test the full MPT escrow lifecycle.

**Files:**
- Create: `goXRPL/internal/testing/escrow/token_escrow_mpt_test.go`

- [ ] **Step 1: Write MPT enablement test**

```go
func TestMPTEscrow_Enablement(t *testing.T) {
    // Without TokenEscrow: temBAD_AMOUNT
    // Without MPTokensV1: temDISABLED
    // With both: succeeds (given lsfMPTCanEscrow)
}
```

- [ ] **Step 2: Write lsfMPTCanEscrow flag test**

```go
func TestMPTEscrow_CanEscrowFlag(t *testing.T) {
    // MPTIssuance without lsfMPTCanEscrow: tecNO_PERMISSION
    // With flag: succeeds
}
```

- [ ] **Step 3: Write MPT create preclaim tests**

```go
func TestMPTEscrow_CreatePreclaim(t *testing.T) {
    t.Run("IssuerCannotEscrow", ...)        // tecNO_PERMISSION
    t.Run("NoMPToken", ...)                 // tecOBJECT_NOT_FOUND
    t.Run("InsufficientFunds", ...)         // tecINSUFFICIENT_FUNDS
    t.Run("RequireAuthNotAuth", ...)        // tecNO_AUTH
    t.Run("FrozenMPT", ...)                 // tecLOCKED
    t.Run("CanTransferDisabled", ...)       // tecNO_AUTH
}
```

- [ ] **Step 4: Write MPT finish/cancel lifecycle tests**

```go
func TestMPTEscrow_FinishBasic(t *testing.T) {
    // Create MPT escrow, advance time, finish — verify balances
    // Check MPToken.MPTAmount and LockedAmount
}

func TestMPTEscrow_CancelBasic(t *testing.T) {
    // Create MPT escrow, cancel — verify amounts returned
}

func TestMPTEscrow_FinishWithTransferFee(t *testing.T) {
    // Set TransferFee on issuance, create escrow, finish
    // Verify fee deducted from locked amount
}

func TestMPTEscrow_MPTokenAutoCreation(t *testing.T) {
    // Destination has no MPToken, submits EscrowFinish for self — creates MPToken
}

func TestMPTEscrow_LockedAmountTracking(t *testing.T) {
    // Create escrow, verify LockedAmount on both MPToken and MPTIssuance
    // Finish escrow, verify LockedAmount decremented
}
```

- [ ] **Step 5: Run all escrow tests**

Run: `go test -v ./goXRPL/internal/testing/escrow/...`
Expected: All escrow tests pass (XRP, IOU, and MPT).

- [ ] **Step 6: Commit**

```bash
git add goXRPL/internal/testing/escrow/token_escrow_mpt_test.go
git commit -m "test(escrow): add MPT escrow integration tests"
```

---

## Task 11: EscrowCreate Preflight for Non-XRP

Rippled's preflight does extra validation for non-XRP amounts (IOU: bad currency check; MPT: maxMPTokenAmount check, MPTokensV1 required). Currently the Go `Validate()` only checks `Amount.IsZero() || Amount.IsNegative()`.

**Files:**
- Modify: `goXRPL/internal/tx/escrow/escrow_create.go` (Validate method)

- [ ] **Step 1: Add non-XRP preflight checks in Validate()**

After the positive amount check (line 81), add:

```go
// Non-XRP preflight validation (amendment check deferred to Apply)
// Reference: rippled Escrow.cpp:94-119
if !e.Amount.IsNative() {
    if e.Amount.IsMPT() {
        // MPT: check max amount
        if raw, ok := e.Amount.MPTRaw(); ok {
            if raw > maxMPTokenAmount {
                return tx.Errorf(tx.TemBAD_AMOUNT, "MPT amount exceeds maximum")
            }
        }
    } else {
        // IOU: check bad currency
        if e.Amount.Currency == "" || e.Amount.Currency == "XRP" {
            return tx.Errorf(tx.TemBAD_CURRENCY, "cannot escrow XRP as IOU")
        }
    }
}
```

Add the constant:

```go
const maxMPTokenAmount int64 = 0x7FFFFFFFFFFFFFFF // 9223372036854775807
```

- [ ] **Step 2: Run tests**

Run: `go test ./goXRPL/internal/tx/escrow/...`
Expected: All pass.

- [ ] **Step 3: Commit**

```bash
git add goXRPL/internal/tx/escrow/escrow_create.go
git commit -m "feat(escrow): add non-XRP preflight validation in EscrowCreate"
```

---

## Task 12: Final Integration Testing & Verification

Run the full test suite, fix any issues, and verify conformance.

**Files:**
- All modified files

- [ ] **Step 1: Run full escrow test suite**

Run: `go test -v ./goXRPL/internal/testing/escrow/... -count=1`
Expected: All tests pass.

- [ ] **Step 2: Run full project test suite**

Run: `go test ./goXRPL/... -count=1 2>&1 | tail -20`
Expected: No new failures introduced.

- [ ] **Step 3: Run build**

Run: `go build -o /dev/null ./goXRPL/cmd/xrpld`
Expected: Clean build.

- [ ] **Step 4: Verify conformance test summary (if applicable)**

Run: `cd go-xrpl && ./scripts/conformance-summary.sh Escrow`
Expected: Escrow suite shows improved pass rate.

- [ ] **Step 5: Final commit**

```bash
git add -A
git commit -m "feat(escrow): complete TokenEscrow amendment (IOU + MPT escrow support)"
```

---

## Critical Implementation Notes

### `tx.Amount` is `state.Amount`
`tx.Amount` is a type alias: `type Amount = state.Amount` (in `internal/tx/transaction.go:108`). This means `tx.Amount` and `state.Amount` are identical — no conversion needed. The `EscrowData.IOUAmount` field (type `*Amount`) can be used directly as `tx.Amount`.

### `keylet.MPTIssuance` takes `[24]byte`, not hex string
There is no `keylet.MPTIssuanceByHexID`. Throughout the plan, calls like `keylet.MPTIssuanceByHexID(mptID)` must be replaced with:
```go
mptIDBytes, _ := hex.DecodeString(mptID)
var mptID24 [24]byte
copy(mptID24[:], mptIDBytes)
issuanceKey := keylet.MPTIssuance(mptID24)
```
Extract this into a helper: `func mptIssuanceKeyFromHex(hexID string) keylet.Keylet`.

### Helper Functions Not Fully Defined in Plan
The following utility functions are referenced but left as stubs — implement them in `token_helpers.go`:

1. **`createTrustLineZeroBalance`** — Creates a zero-balance trust line. Adapt from `createTrustLineWithBalance` in `internal/tx/nftoken/iou_helpers.go:480`. Set NoRipple based on `lsfDefaultRipple`.

2. **`createMPTokenForEscrow`** — Creates an empty MPToken for the destination. Adapt from `MPTokenAuthorize.Apply()` in `internal/tx/mpt/mptoken_authorize.go:245` which inserts a new MPToken SLE with zero balance.

3. **`mptDivideByRate`** — `amount * 1e9 / rate` for uint64 MPT amounts. Use `amount * 1_000_000_000 / rate` with `math/big` to avoid overflow.

4. **`updateEscrowField`** — Re-reads the escrow SLE, decodes via binary codec, updates the field, re-encodes, and writes back. Alternative: build the full JSON map upfront in `serializeEscrow` and set IssuerNode before first Insert.

5. **`Amount.MulRatio`** — Verify this method exists on `state.Amount`. If not, implement the IOU divide-by-rate manually using mantissa/exponent arithmetic.

---

## Notes for the Implementer

### Key References in Rippled
- `rippled/src/xrpld/app/tx/detail/Escrow.cpp` — full transaction logic (1410 lines)
- `rippled/src/xrpld/ledger/detail/View.cpp:2853-3094` — rippleLockEscrowMPT/rippleUnlockEscrowMPT
- `rippled/src/test/app/EscrowToken_test.cpp` — comprehensive test suite (3887 lines)

### Existing Go Infrastructure to Reuse
| What | Where | Notes |
|---|---|---|
| `IsTrustlineFrozen` | `internal/tx/utils.go:21` | Checks individual freeze |
| `IsDeepFrozen` | `internal/tx/utils.go:115` | Checks deep freeze |
| `requireAuth` (IOU) | `internal/tx/amm/amm_create.go:442` | Package-private, must reimplement in escrow |
| `requireMPTAuth` | `internal/tx/payment/payment_mpt.go:400` | Package-private, simplified version for escrow |
| `getTransferRate` | `internal/tx/nftoken/iou_helpers.go:289` | Package-private |
| `rippleCreditIOU` | `internal/tx/nftoken/iou_helpers.go:305` | Package-private |
| `escrowLockIOU` | `internal/tx/escrow/escrow_create.go:397` | Already in escrow package |
| `ParseMPToken` | `internal/ledger/state/mptoken_entry.go` | |
| `SerializeMPToken` | `internal/ledger/state/mptoken_entry.go` | |
| `ParseMPTokenIssuance` | `internal/ledger/state/mptoken_entry.go` | |
| `SerializeMPTokenIssuance` | `internal/ledger/state/mptoken_entry.go` | |

### Constants
| Name | Value | Source |
|---|---|---|
| `lsfAllowTrustLineLocking` | `0x40000000` | NEW — add to `account_root.go` |
| `asfAllowTrustLineLocking` | `17` | NEW — add to `account_set.go` |
| `lsfMPTCanEscrow` | `0x00000008` | Already in `ledger/entry/flags.go` |
| `parityRate` | `1_000_000_000` | Transfer rate = no fee |
| `maxMPTokenAmount` | `0x7FFFFFFFFFFFFFFF` | Max int64 |

### Transfer-Rate Formula
- At creation: capture issuer's current transfer rate on the SLE
- At finish: `lockedRate = min(captured_rate, current_rate)`
- Fee = `amount - divideRound(amount, lockedRate)` where `divideRound(a, r) = a * 1e9 / r` (rounded up)
- Final amount = `amount - fee`
- For cancel: always use `parityRate` (no fee — full amount returned)
