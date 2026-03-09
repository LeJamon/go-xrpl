# IOU Payment - Rippled Behavior Pseudocode

Based on analysis of:
- `rippled/src/xrpld/app/tx/detail/Payment.cpp`
- `rippled/src/xrpld/app/paths/RippleCalc.cpp`
- `rippled/src/xrpld/app/paths/Flow.cpp`
- `rippled/src/xrpld/ledger/detail/View.cpp`

## Overview

IOU payments in rippled use the "ripple" path when:
```cpp
bool const ripple = (hasPaths || sendMax || !dstAmount.native()) && !mptDirect;
```

There are three types of IOU payments:

1. **Direct Send (Issuer Involved)**: Sender or receiver is the issuer
2. **Transit Send (3rd Party IOUs)**: Neither sender nor receiver is the issuer
3. **Path-Based Payments**: Cross-currency or multi-hop payments via RippleCalc

## Type 1: Direct Send (Issuer Involved)

When `sender == issuer` OR `receiver == issuer`:

```pseudocode
function rippleSendIOU_Direct(sender, receiver, amount):
    // Direct send: redeeming IOUs and/or sending own IOUs
    // No transfer fee applies

    result = rippleCreditIOU(sender, receiver, amount, checkIssuer=false)
    actualAmount = amount
    return result, actualAmount
```

### Example: Issuer pays holder
- Issuer (A) sends 100 USD to Holder (B)
- Trust line A-B: Balance changes from 0 to +100 (B's perspective)
- This is "minting" IOUs

### Example: Holder pays issuer (redemption)
- Holder (B) sends 100 USD to Issuer (A)
- Trust line A-B: Balance changes from +100 to 0 (B's perspective)
- This is "burning/redeeming" IOUs

## Type 2: Transit Send (3rd Party IOUs)

When `sender != issuer` AND `receiver != issuer`:

```pseudocode
function rippleSendIOU_Transit(sender, receiver, amount, waiveFee):
    issuer = amount.issuer

    // Calculate actual cost with transfer fee
    if waiveFee:
        actualAmount = amount
    else:
        actualAmount = amount * transferRate(issuer)

    // Step 1: Credit receiver from issuer
    result = rippleCreditIOU(issuer, receiver, amount, checkIssuer=true)
    if result != tesSUCCESS:
        return result

    // Step 2: Debit sender to issuer
    result = rippleCreditIOU(sender, issuer, actualAmount, checkIssuer=true)
    return result, actualAmount
```

### Example: Holder A pays Holder B (same issuer)
- Holder A sends 100 USD/GatewayX to Holder B
- Transfer fee is 0.2% (TransferRate = 1.002)
- Actual cost = 100 * 1.002 = 100.2 USD
- Operations:
  1. Trust line GatewayX-B: +100 USD (credit B from issuer)
  2. Trust line A-GatewayX: -100.2 USD (debit A to issuer)

## Type 3: Path-Based Payments (RippleCalc/Flow)

For cross-currency payments or when paths are specified:

```pseudocode
function RippleCalc(view, maxSourceAmount, dstAmount, dst, src, paths, input):
    // Convert paths to strands (execution routes)
    result, strands = toStrands(view, src, dst, dstAmount.issue, paths, ...)
    if result != tesSUCCESS:
        return result

    // Execute flow through strands
    flowResult = flow(
        strands,
        dstAmount,
        partialPayment = input.partialPaymentAllowed,
        limitQuality = input.limitQuality,
        sendMax = maxSourceAmount
    )

    // Apply changes from sandbox
    flowResult.sandbox.apply(view)

    return flowResult
```

### Flow Algorithm

```pseudocode
function flow(strands, deliver, partialPayment, limitQuality, sendMax):
    actualIn = 0
    actualOut = 0

    for each strand in strands:
        // Calculate how much can flow through this strand
        strandResult = flowStrand(strand, deliver - actualOut, ...)

        actualIn += strandResult.in
        actualOut += strandResult.out

        if actualOut >= deliver:
            break

        // Check if sendMax exceeded
        if sendMax && actualIn >= sendMax:
            break

    if actualOut == 0:
        return tecPATH_DRY

    if actualOut < deliver && !partialPayment:
        return tecPATH_PARTIAL

    return tesSUCCESS, actualIn, actualOut
```

## Core Function: rippleCreditIOU

Updates trust lines between two accounts:

```pseudocode
function rippleCreditIOU(sender, receiver, amount, checkIssuer):
    issuer = amount.issuer
    currency = amount.currency

    // Validate issuer involvement if required
    if checkIssuer:
        assert(sender == issuer OR receiver == issuer)

    // Cannot send to self
    assert(sender != receiver)

    // Determine which account is "high" (for trust line direction)
    senderHigh = sender > receiver
    trustLineKey = keylet.line(sender, receiver, currency)

    // Check if trust line exists
    trustLine = view.peek(trustLineKey)

    if trustLine exists:
        // Modify existing trust line
        balance = trustLine.Balance

        if senderHigh:
            balance = -balance  // Put in sender's terms

        previousBalance = balance
        balance -= amount

        // Check if sender's reserve should be cleared
        // (complex conditions involving flags, limits, quality settings)
        if canClearSenderReserve(trustLine, senderHigh, previousBalance, balance):
            adjustOwnerCount(sender, -1)
            clearReserveFlag(trustLine, senderHigh)

        // Check if trust line should be deleted
        if balance == 0 AND noReceiverReserve:
            return trustDelete(trustLine)

        if senderHigh:
            balance = -balance  // Restore to standard representation

        trustLine.Balance = balance
        view.update(trustLine)
        return tesSUCCESS

    else:
        // Create new trust line (receiver side)
        // This happens when issuer pays to a new holder
        receiverAccount = view.peek(keylet.account(receiver))
        noRipple = !(receiverAccount.Flags & lsfDefaultRipple)

        return trustCreate(
            senderHigh,
            sender, receiver,
            trustLineKey,
            receiverAccount,
            initialBalance = amount,
            limit = 0,  // Receiver's limit
            noRipple = noRipple
        )
```

## Trust Line Structure

```
RippleState (Trust Line):
    LedgerEntryType: "RippleState"
    Flags: combination of:
        - lsfLowReserve (0x00010000): Low account has reserve
        - lsfHighReserve (0x00020000): High account has reserve
        - lsfLowAuth (0x00040000): Low account authorized
        - lsfHighAuth (0x00080000): High account authorized
        - lsfLowNoRipple (0x00100000): Low account no-ripple
        - lsfHighNoRipple (0x00200000): High account no-ripple
        - lsfLowFreeze (0x00400000): Low account frozen
        - lsfHighFreeze (0x00800000): High account frozen
    Balance: Current balance (positive = high owes low)
    LowLimit: {value, currency, issuer=low_account}
    HighLimit: {value, currency, issuer=high_account}
    LowNode: Directory node for low account
    HighNode: Directory node for high account
    LowQualityIn, LowQualityOut: Quality settings for low account
    HighQualityIn, HighQualityOut: Quality settings for high account
```

## Transfer Rate

The transfer rate is a fee charged when IOUs move between non-issuer accounts:

```pseudocode
function transferRate(issuer):
    account = view.read(keylet.account(issuer))
    if account has sfTransferRate:
        return account.TransferRate / 1000000000  // Stored as 1000000000 = 1.0
    return 1.0  // No fee
```

## Payment.cpp doApply() for IOU Payments

```pseudocode
function doApply_IOU():
    // Get payment parameters
    dstAmount = tx.Amount
    maxSourceAmount = getMaxSourceAmount(account, dstAmount, tx.SendMax)
    hasPaths = tx.hasField(sfPaths)
    sendMax = tx.SendMax

    // Check if this is a "ripple" payment
    ripple = (hasPaths || sendMax || !dstAmount.native())

    if !ripple:
        // This would be handled by XRP direct payment
        return applyXRPPayment()

    // IOU payment via RippleCalc
    rcInput = {
        partialPaymentAllowed: tx.Flags & tfPartialPayment,
        defaultPathsAllowed: !(tx.Flags & tfNoRippleDirect),
        limitQuality: tx.Flags & tfLimitQuality,
        isLedgerOpen: view.open()
    }

    // Create sandbox for atomic execution
    sandbox = PaymentSandbox(view)

    // Execute path finding and payment
    result = RippleCalc.rippleCalculate(
        sandbox,
        maxSourceAmount,
        dstAmount,
        dst,
        src,
        tx.Paths,
        rcInput
    )

    // Apply sandbox changes
    sandbox.apply(view)

    // Handle partial payment delivery amount
    if result.success AND result.actualAmountOut != dstAmount:
        if deliverMin AND result.actualAmountOut < deliverMin:
            return tecPATH_PARTIAL
        else:
            ctx.deliver(result.actualAmountOut)

    // Convert retry errors to tecPATH_DRY
    if isTerRetry(result):
        return tecPATH_DRY

    return result
```

## Common Error Codes

- `tecPATH_DRY`: No path found or path had no liquidity
- `tecPATH_PARTIAL`: Partial payment not allowed but couldn't deliver full amount
- `tecNO_LINE`: Trust line doesn't exist
- `tecNO_AUTH`: Not authorized to hold this IOU
- `tecUNFUNDED_PAYMENT`: Not enough balance (for direct sends without paths)
- `tecINSUF_RESERVE_LINE`: Not enough XRP for trust line reserve

## Implementation Strategy for goXRPL

For MVP, implement in order of complexity:

### Phase 1: Direct IOU Payments (Same Issuer)
1. Sender == Issuer (minting)
2. Receiver == Issuer (redemption)
3. Holder to Holder (same issuer, with transfer fee)

### Phase 2: Simple Path Payments
1. Default path (no explicit paths in tx)
2. Single-hop cross-currency

### Phase 3: Full Path Finding
1. Multi-hop paths
2. Order book integration
3. AMM integration

For the failing transaction at ledger 771008 (cross-issuer BTC payment), this requires Phase 2/3 since it needs to find a path between different BTC issuers.
