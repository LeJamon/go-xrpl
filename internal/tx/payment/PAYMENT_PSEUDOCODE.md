# XRP Direct Payment - Rippled Behavior Pseudocode

Based on analysis of `rippled/src/xrpld/app/tx/detail/Payment.cpp` and `Transactor.cpp`.

## Transaction Flow Overview

```
preflight()  -> Stateless validation (tx structure, flags, amounts)
preclaim()   -> Stateful validation (destination exists or can be created, reserves)
apply()      -> Execute the transaction:
    1. Load sender account
    2. mPriorBalance = sender.Balance (BEFORE fee)
    3. mSourceBalance = mPriorBalance
    4. consumeSeqProxy() - increment sequence
    5. payFee() - deduct fee: mSourceBalance -= fee; sender.Balance = mSourceBalance
    6. doApply() - the actual payment logic
```

## Key Variables

- **mPriorBalance**: Sender's balance BEFORE fee deduction
- **mSourceBalance**: Sender's balance AFTER fee deduction (updated by payFee)
- Both are XRP amounts in drops

## Preclaim for XRP Direct Payment

```pseudocode
function preclaim(tx, view):
    dstAccountID = tx.Destination
    dstAmount = tx.Amount  // must be XRP for direct payment

    sleDst = view.read(keylet.account(dstAccountID))

    if sleDst == nil:
        // Destination doesn't exist
        if !dstAmount.isNative():
            return tecNO_DST  // Can't create account with IOU

        if view.open() && tx.Flags & tfPartialPayment:
            return telNO_DST_PARTIAL  // Can't create account with partial payment

        if dstAmount < accountReserve(0):
            return tecNO_DST_INSUF_XRP  // Not enough to meet reserve
    else:
        // Destination exists
        if (sleDst.Flags & lsfRequireDestTag) && !tx.hasDestinationTag:
            return tecDST_TAG_NEEDED

    return tesSUCCESS
```

## DoApply for XRP Direct Payment

```pseudocode
function doApply(tx):
    dstAccountID = tx.Destination
    dstAmount = tx.Amount  // XRP amount in drops

    // === STEP 1: Handle destination account ===
    sleDst = view.peek(keylet.account(dstAccountID))

    if sleDst == nil:
        // Create new account
        seqno = 1
        if rules.enabled(featureDeletableAccounts):
            seqno = view.seq()  // current ledger sequence

        sleDst = new SLE(keylet.account(dstAccountID))
        sleDst.Account = dstAccountID
        sleDst.Sequence = seqno
        // Balance will be set later
        view.insert(sleDst)
    else:
        // Mark destination for update
        view.update(sleDst)

    // === STEP 2: Check sender has enough funds ===
    sleSrc = view.peek(keylet.account(account_))
    if sleSrc == nil:
        return tefINTERNAL

    ownerCount = sleSrc.OwnerCount
    reserve = fees.accountReserve(ownerCount)  // ReserveBase + ownerCount * ReserveIncrement

    // Allow final spend to use reserve for fee
    // mmm = minimum balance that must remain (either reserve OR fee, whichever is larger)
    mmm = max(reserve, tx.Fee)

    // IMPORTANT: Check against mPriorBalance (balance BEFORE fee deduction)
    if mPriorBalance < dstAmount + mmm:
        return tecUNFUNDED_PAYMENT

    // === STEP 3: Check deposit authorization (amendment-gated) ===
    if rules.enabled(featureDepositAuth):
        // Complex deposit auth checks...
        // Skip for early ledgers

    // === STEP 4: Execute the transfer ===
    // mSourceBalance already has fee deducted (done in payFee())
    sleSrc.Balance = mSourceBalance - dstAmount
    sleDst.Balance = sleDst.Balance + dstAmount  // For new account, Balance was 0

    // === STEP 5: Clear password spent flag if set ===
    if sleDst.Flags & lsfPasswordSpent:
        sleDst.clearFlag(lsfPasswordSpent)

    return tesSUCCESS
```

## Metadata Generation

For the sender (ModifiedNode):
- LedgerEntryType: "AccountRoot"
- LedgerIndex: sender's account key
- PreviousFields: { Balance: mPriorBalance, Sequence: oldSequence }
- FinalFields: { Balance: newBalance, Sequence: newSequence, ... }

For the destination:
- If new account (CreatedNode):
  - LedgerEntryType: "AccountRoot"
  - NewFields: { Account: dstAccountID, Balance: dstAmount, Sequence: seqno }
- If existing account (ModifiedNode):
  - PreviousFields: { Balance: oldBalance }
  - FinalFields: { Balance: newBalance }

## Critical Implementation Notes

1. **Balance Check Order**:
   - Fee is deducted FIRST (in payFee, before doApply)
   - Balance check uses PRE-FEE balance (mPriorBalance)
   - Actual debit uses POST-FEE balance (mSourceBalance)

2. **New Account Sequence**:
   - Without DeletableAccounts: Sequence = 1
   - With DeletableAccounts: Sequence = current ledger sequence

3. **Reserve Calculation**:
   - reserve = ReserveBase + (OwnerCount * ReserveIncrement)
   - mmm = max(reserve, fee)
   - Check: mPriorBalance >= dstAmount + mmm

4. **tecUNFUNDED_PAYMENT**:
   - This is a "tec" code - transaction fails but fee is claimed
   - Metadata should show the sender's balance decreasing by fee only
   - Sequence still increments
