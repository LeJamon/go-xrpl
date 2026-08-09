// Package escrow implements EscrowCreate, EscrowFinish, and EscrowCancel transactions.
// This file contains helpers for IOU and MPT escrow preclaim validation,
// lock/unlock operations, and shared utilities.
// Reference: rippled Escrow.cpp and View.cpp
package escrow

import (
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
	entry "github.com/LeJamon/go-xrpl/ledger/entry"
)

// parityRate is the identity transfer rate (no fee). Matches rippled's parityRate.
const parityRate uint32 = 1_000_000_000

// 1. EscrowCreate Preclaim Helpers

// escrowCreatePreclaimIOU validates IOU escrow creation preconditions.
// Reference: rippled Escrow.cpp escrowCreatePreclaimHelper<Issue> lines 204-279
func escrowCreatePreclaimIOU(
	view tx.LedgerView,
	accountID, destID [20]byte,
	amount tx.Amount,
	numberContext state.NumberContext,
) ter.Result {
	issuerID, err := state.DecodeAccountID(amount.Issuer)
	if err != nil {
		return ter.TefINTERNAL
	}

	// Issuer cannot create escrow of own tokens
	if issuerID == accountID {
		return ter.TecNO_PERMISSION
	}

	// Issuer must exist and have lsfAllowTrustLineLocking
	sleIssuer, err := tx.ReadAccountRoot(view, issuerID)
	if err != nil {
		return ter.TefINTERNAL
	}
	if sleIssuer == nil {
		return ter.TecNO_ISSUER
	}
	if sleIssuer.Flags&state.LsfAllowTrustLineLocking == 0 {
		return ter.TecNO_PERMISSION
	}

	// Trust line must exist
	trustLineKey := keylet.Line(accountID, issuerID, amount.Currency)
	trustLineData, err := view.Read(trustLineKey)
	if err != nil {
		return ter.TefINTERNAL
	}
	if trustLineData == nil {
		return ter.TecNO_LINE
	}

	rs, err := state.ParseRippleState(trustLineData)
	if err != nil {
		return ter.TefINTERNAL
	}

	// Balance direction validation
	// Reference: rippled lines 232-237
	// If balance is positive, issuer must have higher address than account
	// If balance is negative, issuer must have lower address than account
	if rs.Balance.Signum() > 0 && state.CompareAccountIDs(issuerID, accountID) < 0 {
		return ter.TecNO_PERMISSION
	}
	if rs.Balance.Signum() < 0 && state.CompareAccountIDs(issuerID, accountID) > 0 {
		return ter.TecNO_PERMISSION
	}

	// requireAuth for sender
	if tr := requireAuthIOU(view, issuerID, accountID, amount.Currency); tr != ter.TesSUCCESS {
		return tr
	}

	// requireAuth for destination
	if tr := requireAuthIOU(view, issuerID, destID, amount.Currency); tr != ter.TesSUCCESS {
		return tr
	}

	// Freeze checks (global freeze + issuer-side individual freeze)
	frozen, freezeResult := isIOUFrozen(view, accountID, issuerID, amount.Currency)
	if freezeResult != ter.TesSUCCESS {
		return freezeResult
	}
	if frozen {
		return ter.TecFROZEN
	}
	frozen, freezeResult = isIOUFrozen(view, destID, issuerID, amount.Currency)
	if freezeResult != ter.TesSUCCESS {
		return freezeResult
	}
	if frozen {
		return ter.TecFROZEN
	}

	// Spendable amount check (ignore freeze since we already checked)
	spendable, holdsResult := accountHoldsIOU(view, accountID, issuerID, amount.Currency)
	if holdsResult != ter.TesSUCCESS {
		return holdsResult
	}
	if spendable.Signum() <= 0 {
		return ter.TecINSUFFICIENT_FUNDS
	}
	if spendable.Compare(amount) < 0 {
		return ter.TecINSUFFICIENT_FUNDS
	}

	// Precision loss check: if the spendable amount and escrow amount differ
	// so much in magnitude that IOU addition loses the smaller value, reject.
	// Reference: rippled Escrow.cpp line 275: if (!canAdd(spendableAmount, amount))
	if !canAddIOUAmounts(spendable, amount, numberContext) {
		return ter.TecPRECISION_LOSS
	}

	return ter.TesSUCCESS
}

// escrowCreatePreclaimMPT validates MPT escrow creation preconditions.
// Reference: rippled Escrow.cpp escrowCreatePreclaimHelper<MPTIssue> lines 283-359
func escrowCreatePreclaimMPT(view tx.LedgerView, rules *amendment.Rules, accountID, destID [20]byte, amount tx.Amount) ter.Result {
	// FeatureMPTokensV1 must be enabled
	if !rules.Enabled(amendment.FeatureMPTokensV1) {
		return ter.TemDISABLED
	}

	// MPT amounts store the issuer in the MPTIssuanceID (last 20 bytes),
	// not in Amount.Issuer which is empty for MPT.
	issuerID, err := mptIssuerAccountID(amount.MPTIssuanceID())
	if err != nil {
		return ter.TefINTERNAL
	}

	// Issuer cannot create escrow
	if issuerID == accountID {
		return ter.TecNO_PERMISSION
	}

	// MPTIssuance must exist
	issuanceKey, err := mptIssuanceKeyFromHex(amount.MPTIssuanceID())
	if err != nil {
		return ter.TefINTERNAL
	}
	issuanceData, err := view.Read(issuanceKey)
	if err != nil {
		return ter.TefINTERNAL
	}
	if issuanceData == nil {
		return ter.TecOBJECT_NOT_FOUND
	}

	issuance, err := state.ParseMPTokenIssuance(issuanceData)
	if err != nil {
		return ter.TefINTERNAL
	}

	// Must have lsfMPTCanEscrow flag
	if issuance.Flags&entry.LsfMPTCanEscrow == 0 {
		return ter.TecNO_PERMISSION
	}

	// Issuance issuer must match amount issuer
	if issuance.Issuer != issuerID {
		return ter.TecNO_PERMISSION
	}

	// Sender must hold MPToken
	tokenKey := keylet.MPToken(issuanceKey.Key, accountID)
	exists, err := view.Exists(tokenKey)
	if err != nil {
		return ter.TefINTERNAL
	}
	if !exists {
		return ter.TecOBJECT_NOT_FOUND
	}

	// requireAuth for sender (WeakAuth)
	if tr := requireMPTAuthForEscrow(view, issuance.Flags, issuanceKey, accountID, issuerID); tr != ter.TesSUCCESS {
		return tr
	}

	// requireAuth for destination (WeakAuth)
	if tr := requireMPTAuthForEscrow(view, issuance.Flags, issuanceKey, destID, issuerID); tr != ter.TesSUCCESS {
		return tr
	}

	// Frozen checks (global lock on issuance or individual lock on token)
	if frozen, result := isMPTFrozen(view, issuance.Flags, issuanceKey, accountID, issuerID); result != ter.TesSUCCESS {
		return result
	} else if frozen {
		return ter.TecLOCKED
	}
	if frozen, result := isMPTFrozen(view, issuance.Flags, issuanceKey, destID, issuerID); result != ter.TesSUCCESS {
		return result
	} else if frozen {
		return ter.TecLOCKED
	}

	// canTransfer check (holder-to-holder needs LsfMPTCanTransfer)
	if tr := canTransferMPT(issuance, accountID, destID); tr != ter.TesSUCCESS {
		return tr
	}

	// Balance check (ignore freeze since we already checked)
	spendable, holdsResult := accountHoldsMPT(view, issuanceKey, accountID)
	if holdsResult != ter.TesSUCCESS {
		return holdsResult
	}
	if spendable <= 0 {
		return ter.TecINSUFFICIENT_FUNDS
	}

	raw, ok := amount.MPTRaw()
	if !ok {
		// Fallback to IOU value
		raw = amount.IOU().Mantissa()
	}
	if spendable < raw {
		return ter.TecINSUFFICIENT_FUNDS
	}

	return ter.TesSUCCESS
}

// 2. EscrowFinish Preclaim Helpers

// escrowFinishPreclaimIOU validates IOU escrow finish preconditions.
// Reference: rippled Escrow.cpp lines 702-724
func escrowFinishPreclaimIOU(view tx.LedgerView, destID [20]byte, amount tx.Amount) ter.Result {
	issuerID, err := state.DecodeAccountID(amount.Issuer)
	if err != nil {
		return ter.TefINTERNAL
	}

	// If dest == issuer, return tesSUCCESS
	if issuerID == destID {
		return ter.TesSUCCESS
	}

	// requireAuth on destination
	if tr := requireAuthIOU(view, issuerID, destID, amount.Currency); tr != ter.TesSUCCESS {
		return tr
	}

	// Deep freeze check on destination
	deepFrozen, deepFreezeResult := isIOUDeepFrozen(view, destID, issuerID, amount.Currency)
	if deepFreezeResult != ter.TesSUCCESS {
		return deepFreezeResult
	}
	if deepFrozen {
		return ter.TecFROZEN
	}

	return ter.TesSUCCESS
}

// escrowFinishPreclaimMPT validates MPT escrow finish preconditions.
// Reference: rippled Escrow.cpp lines 726-758
func escrowFinishPreclaimMPT(view tx.LedgerView, destID [20]byte, amount tx.Amount) ter.Result {
	// MPT amounts store the issuer in the MPTIssuanceID (last 20 bytes),
	// not in Amount.Issuer which is empty for MPT.
	issuerID, err := mptIssuerAccountID(amount.MPTIssuanceID())
	if err != nil {
		return ter.TefINTERNAL
	}

	// If dest == issuer, return tesSUCCESS
	if issuerID == destID {
		return ter.TesSUCCESS
	}

	// MPTIssuance must exist
	issuanceKey, err := mptIssuanceKeyFromHex(amount.MPTIssuanceID())
	if err != nil {
		return ter.TefINTERNAL
	}
	issuanceData, err := view.Read(issuanceKey)
	if err != nil {
		return ter.TefINTERNAL
	}
	if issuanceData == nil {
		return ter.TecOBJECT_NOT_FOUND
	}

	issuance, err := state.ParseMPTokenIssuance(issuanceData)
	if err != nil {
		return ter.TefINTERNAL
	}

	// requireAuth on destination (WeakAuth)
	if tr := requireMPTAuthForEscrow(view, issuance.Flags, issuanceKey, destID, issuerID); tr != ter.TesSUCCESS {
		return tr
	}

	// Frozen check on destination
	if frozen, result := isMPTFrozen(view, issuance.Flags, issuanceKey, destID, issuerID); result != ter.TesSUCCESS {
		return result
	} else if frozen {
		return ter.TecLOCKED
	}

	return ter.TesSUCCESS
}

// 3. EscrowCancel Preclaim Helpers

// escrowCancelPreclaimIOU validates IOU escrow cancel preconditions.
// Reference: rippled Escrow.cpp lines 1219-1237
func escrowCancelPreclaimIOU(view tx.LedgerView, accountID [20]byte, amount tx.Amount) ter.Result {
	issuerID, err := state.DecodeAccountID(amount.Issuer)
	if err != nil {
		return ter.TefINTERNAL
	}

	// Issuer == account is an internal error
	if issuerID == accountID {
		return ter.TecINTERNAL
	}

	// requireAuth on account
	if tr := requireAuthIOU(view, issuerID, accountID, amount.Currency); tr != ter.TesSUCCESS {
		return tr
	}

	return ter.TesSUCCESS
}

// escrowCancelPreclaimMPT validates MPT escrow cancel preconditions.
// Reference: rippled Escrow.cpp lines 1239-1267
func escrowCancelPreclaimMPT(view tx.LedgerView, accountID [20]byte, amount tx.Amount) ter.Result {
	// MPT amounts store the issuer in the MPTIssuanceID (last 20 bytes),
	// not in Amount.Issuer which is empty for MPT.
	issuerID, err := mptIssuerAccountID(amount.MPTIssuanceID())
	if err != nil {
		return ter.TefINTERNAL
	}

	// Issuer == account is an internal error
	if issuerID == accountID {
		return ter.TecINTERNAL
	}

	// MPTIssuance must exist
	issuanceKey, err := mptIssuanceKeyFromHex(amount.MPTIssuanceID())
	if err != nil {
		return ter.TefINTERNAL
	}
	issuanceData, err := view.Read(issuanceKey)
	if err != nil {
		return ter.TefINTERNAL
	}
	if issuanceData == nil {
		return ter.TecOBJECT_NOT_FOUND
	}

	issuance, err := state.ParseMPTokenIssuance(issuanceData)
	if err != nil {
		return ter.TefINTERNAL
	}

	// requireAuth on account (WeakAuth)
	if tr := requireMPTAuthForEscrow(view, issuance.Flags, issuanceKey, accountID, issuerID); tr != ter.TesSUCCESS {
		return tr
	}

	return ter.TesSUCCESS
}

// 4. Lock Helpers

// escrowLockMPT locks MPT tokens by decreasing sender's MPTAmount and increasing
// LockedAmount on both the MPToken and MPTIssuance.
// Reference: rippled View.cpp rippleLockEscrowMPT() lines 2853-2947
func escrowLockMPT(view tx.LedgerView, senderID [20]byte, amount tx.Amount) ter.Result {
	issuanceKey, err := mptIssuanceKeyFromHex(amount.MPTIssuanceID())
	if err != nil {
		return ter.TefINTERNAL
	}

	issuanceData, err := view.Read(issuanceKey)
	if err != nil {
		return ter.TefINTERNAL
	}
	if issuanceData == nil {
		return ter.TecOBJECT_NOT_FOUND
	}

	issuance, err := state.ParseMPTokenIssuance(issuanceData)
	if err != nil {
		return ter.TefINTERNAL
	}

	issuerID := issuance.Issuer
	if issuerID == senderID {
		return ter.TecINTERNAL
	}

	raw, ok := amount.MPTRaw()
	if !ok {
		raw = amount.IOU().Mantissa()
	}
	pay := uint64(raw)

	// 1. Update sender's MPToken: decrease MPTAmount, increase LockedAmount
	tokenKey := keylet.MPToken(issuanceKey.Key, senderID)
	tokenData, err := view.Read(tokenKey)
	if err != nil {
		return ter.TefINTERNAL
	}
	if tokenData == nil {
		return ter.TecOBJECT_NOT_FOUND
	}

	token, err := state.ParseMPToken(tokenData)
	if err != nil {
		return ter.TefINTERNAL
	}

	// Underflow check
	if token.MPTAmount < pay {
		return ter.TecINTERNAL
	}
	token.MPTAmount -= pay

	// Overflow check for locked amount
	locked := uint64(0)
	if token.LockedAmount != nil {
		locked = *token.LockedAmount
	}
	if locked > ^uint64(0)-pay {
		return ter.TecINTERNAL
	}
	newLocked := locked + pay
	token.LockedAmount = &newLocked

	updatedToken, err := state.SerializeMPToken(token)
	if err != nil {
		return ter.TefINTERNAL
	}
	if err := view.Update(tokenKey, updatedToken); err != nil {
		return ter.TefINTERNAL
	}

	// 2. Update MPTIssuance: increase LockedAmount
	issuanceLocked := uint64(0)
	if issuance.LockedAmount != nil {
		issuanceLocked = *issuance.LockedAmount
	}
	if issuanceLocked > ^uint64(0)-pay {
		return ter.TecINTERNAL
	}
	newIssuanceLocked := issuanceLocked + pay
	issuance.LockedAmount = &newIssuanceLocked

	updatedIssuance, err := state.SerializeMPTokenIssuance(issuance)
	if err != nil {
		return ter.TefINTERNAL
	}
	if err := view.Update(issuanceKey, updatedIssuance); err != nil {
		return ter.TefINTERNAL
	}

	return ter.TesSUCCESS
}

// 5. Unlock Helpers

// escrowUnlockIOU unlocks IOU tokens during EscrowFinish or EscrowCancel.
// Handles trust line creation, transfer fee calculation, limit checking,
// and crediting the receiver.
// Reference: rippled Escrow.cpp escrowUnlockApplyHelper<Issue> lines 809-942
func escrowUnlockIOU(
	view tx.LedgerView,
	ctx *tx.ApplyContext,
	common *tx.Common,
	lockedRate uint32,
	destBalance uint64,
	destID [20]byte,
	amount tx.Amount,
	senderID, receiverID [20]byte,
	createAsset bool,
	bumpDestOwnerCount bool,
	numberContext state.NumberContext,
) ter.Result {
	issuerID, err := state.DecodeAccountID(amount.Issuer)
	if err != nil {
		return ter.TefINTERNAL
	}

	senderIsIssuer := issuerID == senderID
	receiverIsIssuer := issuerID == receiverID
	recvLow := state.CompareAccountIDs(receiverID, issuerID) < 0
	issuerHigh := state.CompareAccountIDs(issuerID, receiverID) > 0

	// Sender should never be the issuer for a locked escrow
	if senderIsIssuer {
		return ter.TecINTERNAL
	}

	// If receiver is the issuer, nothing to credit (tokens return to issuer)
	if receiverIsIssuer {
		return ter.TesSUCCESS
	}

	trustLineKey := keylet.Line(receiverID, issuerID, amount.Currency)
	trustLineData, err := view.Read(trustLineKey)
	if err != nil {
		return ter.TefINTERNAL
	}
	trustLineExists := trustLineData != nil

	if !trustLineExists && createAsset && !receiverIsIssuer {
		// Post-fixCleanup3_2_0 the reserve check and owner-count bump are scoped to
		// the destination account (bumpDestOwnerCount=true). Pre-amendment, cancel
		// scoped them to the soon-erased escrow SLE, which has no sfOwnerCount:
		// rippled throws reading it, yielding tefEXCEPTION whenever a new trust line
		// must be created during a cancel refund.
		if !bumpDestOwnerCount {
			return ter.TefEXCEPTION
		}
		destAccount, err := tx.ReadAccountRoot(view, destID)
		if err != nil || destAccount == nil {
			return ter.TefINTERNAL
		}
		if destID == ctx.AccountID {
			destAccount = ctx.Account
		}
		if result := tx.CheckReserve(ctx, common, destID, destAccount, destBalance, tx.ReserveAdjustment{OwnerCountDelta: 1}, ter.TecNO_LINE_INSUF_RESERVE); result != ter.TesSUCCESS {
			return result
		}

		if tr := createTrustLineForEscrow(ctx, common, issuerID, receiverID, amount.Currency, destAccount, recvLow, bumpDestOwnerCount); tr != ter.TesSUCCESS {
			return tr
		}
		// Re-read after creation
		trustLineData, err = view.Read(trustLineKey)
		if err != nil {
			return ter.TefINTERNAL
		}
		if trustLineData == nil {
			return ter.TecINTERNAL
		}
		trustLineExists = true
	}

	if !trustLineExists && !receiverIsIssuer {
		return ter.TecNO_LINE
	}

	// Compute transfer fee
	// Get current rate from issuer, use min(lockedRate, currentRate)
	currentRate, rateResult := getIOUTransferRate(view, issuerID)
	if rateResult != ter.TesSUCCESS {
		return rateResult
	}
	effectiveRate := lockedRate
	if currentRate != 0 && currentRate < effectiveRate {
		effectiveRate = currentRate
	}
	if effectiveRate == 0 {
		effectiveRate = currentRate
	}
	if effectiveRate == 0 {
		effectiveRate = parityRate
	}

	// Compute final amount after transfer fee
	finalAmt := amount
	if !senderIsIssuer && !receiverIsIssuer && effectiveRate != parityRate {
		// fee = amount - divideRound(amount, rate, issue, true)
		// finalAmt = amount - fee = divideRound(amount, rate, issue, true)
		finalAmt = divideAmountByRate(amount, effectiveRate, numberContext)
	}

	// Validate the line limit if the receiver is not creating a new trust line
	// (createAsset = false means receiver already submitted the finish tx)
	if !createAsset {
		if tr := checkTrustLineLimit(
			view,
			receiverID,
			issuerID,
			amount.Currency,
			finalAmt,
			issuerHigh,
			numberContext,
		); tr != ter.TesSUCCESS {
			return tr
		}
	}

	// Credit the receiver via rippleCredit (issuer -> receiver)
	if !receiverIsIssuer {
		if tr := rippleCreditForEscrow(
			view,
			issuerID,
			receiverID,
			finalAmt,
			numberContext,
		); tr != ter.TesSUCCESS {
			return tr
		}
	}

	return ter.TesSUCCESS
}

// escrowUnlockMPT unlocks MPT tokens during EscrowFinish or EscrowCancel. The
// caller handles MPToken creation and transfer-fee calculation, passing the net
// amount delivered to the receiver (finalAmount) and the gross amount that was
// originally locked (grossAmount).
//
// LockedAmount (on both the issuance and the sender's MPToken) is cleared by the
// gross amount, the receiver is credited the net amount, and the fee portion
// (gross - net) is burned from the issuance OutstandingAmount so that supply
// accounting stays consistent.
//
// Without fixTokenEscrowV1 the caller passes grossAmount == finalAmount, so the
// fee stays permanently locked and no supply is burned (legacy behaviour).
func escrowUnlockMPT(
	view tx.LedgerView,
	ctx *tx.ApplyContext,
	common *tx.Common,
	senderID, receiverID [20]byte,
	finalAmount uint64,
	grossAmount uint64,
	mptHexID string,
	createAsset bool,
	destBalance uint64,
	destID [20]byte,
	bumpDestOwnerCount bool,
) ter.Result {
	issuanceKey, err := mptIssuanceKeyFromHex(mptHexID)
	if err != nil {
		return ter.TefINTERNAL
	}

	issuanceData, err := view.Read(issuanceKey)
	if err != nil {
		return ter.TefINTERNAL
	}
	if issuanceData == nil {
		return ter.TecOBJECT_NOT_FOUND
	}

	issuance, err := state.ParseMPTokenIssuance(issuanceData)
	if err != nil {
		return ter.TefINTERNAL
	}

	issuerID := issuance.Issuer
	receiverIsIssuer := issuerID == receiverID

	// Handle MPToken creation for receiver (from escrowUnlockApplyHelper)
	if !receiverIsIssuer {
		receiverTokenKey := keylet.MPToken(issuanceKey.Key, receiverID)
		receiverExists, existsErr := view.Exists(receiverTokenKey)
		if existsErr != nil {
			return ter.TefINTERNAL
		}

		if !receiverExists && createAsset {
			// Post-fixCleanup3_2_0 the reserve check and owner-count bump are scoped
			// to the destination account. Pre-amendment, cancel scoped them to the
			// soon-erased escrow SLE, which has no sfOwnerCount: rippled throws
			// reading it, yielding tefEXCEPTION whenever a new MPToken must be
			// created during a cancel refund.
			if !bumpDestOwnerCount {
				return ter.TefEXCEPTION
			}
			destAccount, err := tx.ReadAccountRoot(view, destID)
			if err != nil || destAccount == nil {
				return ter.TefINTERNAL
			}
			if destID == ctx.AccountID {
				destAccount = ctx.Account
			}
			if result := tx.CheckReserve(ctx, common, destID, destAccount, destBalance, tx.ReserveAdjustment{OwnerCountDelta: 1}, ter.TecINSUFFICIENT_RESERVE); result != ter.TesSUCCESS {
				return result
			}

			if tr := createMPTokenForEscrow(ctx, common, issuanceKey, mptHexID, receiverID, destAccount, bumpDestOwnerCount); tr != ter.TesSUCCESS {
				return tr
			}
		}

		// Re-check existence after potential creation
		receiverExists, existsErr = view.Exists(receiverTokenKey)
		if existsErr != nil {
			return ter.TefINTERNAL
		}
		if !receiverExists {
			return ter.TecNO_PERMISSION
		}
	}

	// --- rippleUnlockEscrowMPT logic below ---

	// 1. Decrease the Issuance LockedAmount by the gross (originally locked) amount.
	if issuance.LockedAmount == nil {
		return ter.TecINTERNAL
	}
	issuanceLocked := *issuance.LockedAmount
	if issuanceLocked < grossAmount {
		return ter.TecINTERNAL
	}
	newIssuanceLocked := issuanceLocked - grossAmount
	if newIssuanceLocked == 0 {
		issuance.LockedAmount = nil
	} else {
		issuance.LockedAmount = &newIssuanceLocked
	}

	// 2. Handle receiver (credited the net amount, after transfer fee).
	if receiverIsIssuer {
		// Decrease OutstandingAmount by finalAmount (tokens are redeemed)
		if issuance.OutstandingAmount < finalAmount {
			return ter.TecINTERNAL
		}
		issuance.OutstandingAmount -= finalAmount
	} else {
		// Increase receiver's MPTAmount by the net amount.
		receiverTokenKey := keylet.MPToken(issuanceKey.Key, receiverID)
		receiverTokenData, err := view.Read(receiverTokenKey)
		if err != nil {
			return ter.TefINTERNAL
		}
		if receiverTokenData == nil {
			return ter.TecOBJECT_NOT_FOUND
		}

		receiverToken, err := state.ParseMPToken(receiverTokenData)
		if err != nil {
			return ter.TefINTERNAL
		}

		// Overflow check
		if receiverToken.MPTAmount > ^uint64(0)-finalAmount {
			return ter.TecINTERNAL
		}
		receiverToken.MPTAmount += finalAmount

		updatedReceiverToken, err := state.SerializeMPToken(receiverToken)
		if err != nil {
			return ter.TefINTERNAL
		}
		if err := view.Update(receiverTokenKey, updatedReceiverToken); err != nil {
			return ter.TefINTERNAL
		}
	}

	// 3. Burn the transfer fee (gross - net) from the issuance OutstandingAmount.
	// The fee tokens were counted as outstanding while escrowed but are destroyed
	// on delivery. Without fixTokenEscrowV1 gross == net, so this is a no-op.
	if diff := grossAmount - finalAmount; diff != 0 {
		if issuance.OutstandingAmount < diff {
			return ter.TecINTERNAL
		}
		issuance.OutstandingAmount -= diff
	}

	// Write back issuance (with updated LockedAmount and possibly OutstandingAmount)
	updatedIssuance, err := state.SerializeMPTokenIssuance(issuance)
	if err != nil {
		return ter.TefINTERNAL
	}
	if err := view.Update(issuanceKey, updatedIssuance); err != nil {
		return ter.TefINTERNAL
	}

	// 4. Decrease sender's MPToken LockedAmount by the gross (originally locked) amount.
	if issuerID == senderID {
		return ter.TecINTERNAL
	}

	senderTokenKey := keylet.MPToken(issuanceKey.Key, senderID)
	senderTokenData, err := view.Read(senderTokenKey)
	if err != nil {
		return ter.TefINTERNAL
	}
	if senderTokenData == nil {
		return ter.TecOBJECT_NOT_FOUND
	}

	senderToken, err := state.ParseMPToken(senderTokenData)
	if err != nil {
		return ter.TefINTERNAL
	}

	if senderToken.LockedAmount == nil {
		return ter.TecINTERNAL
	}
	senderLocked := *senderToken.LockedAmount
	if senderLocked < grossAmount {
		return ter.TecINTERNAL
	}
	newSenderLocked := senderLocked - grossAmount
	if newSenderLocked == 0 {
		senderToken.LockedAmount = nil
	} else {
		senderToken.LockedAmount = &newSenderLocked
	}

	updatedSenderToken, err := state.SerializeMPToken(senderToken)
	if err != nil {
		return ter.TefINTERNAL
	}
	if err := view.Update(senderTokenKey, updatedSenderToken); err != nil {
		return ter.TefINTERNAL
	}

	return ter.TesSUCCESS
}

// 6. Shared Utilities

func getIOUTransferRate(view tx.LedgerView, issuerID [20]byte) (uint32, ter.Result) {
	issuer, err := tx.ReadAccountRoot(view, issuerID)
	if err != nil {
		return 0, ter.TefINTERNAL
	}
	if issuer == nil {
		return 0, ter.TecNO_ISSUER
	}
	if issuer.TransferRate == 0 {
		return parityRate, ter.TesSUCCESS
	}
	return issuer.TransferRate, ter.TesSUCCESS
}

func isIOUFrozen(
	view tx.LedgerView,
	accountID, issuerID [20]byte,
	currency string,
) (bool, ter.Result) {
	issuer, err := tx.ReadAccountRoot(view, issuerID)
	if err != nil {
		return false, ter.TefINTERNAL
	}
	if issuer == nil {
		return false, ter.TecNO_ISSUER
	}
	if issuer.Flags&state.LsfGlobalFreeze != 0 {
		return true, ter.TesSUCCESS
	}
	if accountID == issuerID {
		return false, ter.TesSUCCESS
	}

	data, err := view.Read(keylet.Line(accountID, issuerID, currency))
	if err != nil {
		return false, ter.TefINTERNAL
	}
	if data == nil {
		return false, ter.TesSUCCESS
	}
	line, err := state.ParseRippleState(data)
	if err != nil {
		return false, ter.TefINTERNAL
	}
	if state.CompareAccountIDs(issuerID, accountID) > 0 {
		return line.Flags&state.LsfHighFreeze != 0, ter.TesSUCCESS
	}
	return line.Flags&state.LsfLowFreeze != 0, ter.TesSUCCESS
}

func isIOUDeepFrozen(
	view tx.LedgerView,
	accountID, issuerID [20]byte,
	currency string,
) (bool, ter.Result) {
	if currency == "" || currency == "XRP" || accountID == issuerID {
		return false, ter.TesSUCCESS
	}
	data, err := view.Read(keylet.Line(accountID, issuerID, currency))
	if err != nil {
		return false, ter.TefINTERNAL
	}
	if data == nil {
		return false, ter.TesSUCCESS
	}
	line, err := state.ParseRippleState(data)
	if err != nil {
		return false, ter.TefINTERNAL
	}
	return line.Flags&(state.LsfLowDeepFreeze|state.LsfHighDeepFreeze) != 0, ter.TesSUCCESS
}

// requireAuthIOU checks if an issuer requires authorization and if the account
// is authorized on the trust line.
// Reference: rippled View.cpp requireAuth(view, Issue, account) for IOU
// Uses the default (legacy) auth type: trust line must exist if requireAuth is set.
func requireAuthIOU(view tx.LedgerView, issuerID, accountID [20]byte, currency string) ter.Result {
	// Issuer is always authorized for own currency
	if issuerID == accountID {
		return ter.TesSUCCESS
	}

	// Read issuer account. A missing issuer carries no auth requirement, matching
	// rippled's `if (issuerAccount && requireAuth)` guard, so it passes.
	issuerAccount, err := tx.ReadAccountRoot(view, issuerID)
	if err != nil {
		return ter.TefINTERNAL
	}
	if issuerAccount == nil {
		return ter.TesSUCCESS
	}

	// If issuer doesn't require auth, pass
	if issuerAccount.Flags&state.LsfRequireAuth == 0 {
		return ter.TesSUCCESS
	}

	// Issuer requires auth — check if the trust line exists and is authorized
	trustLineKey := keylet.Line(accountID, issuerID, currency)
	trustLineData, err := view.Read(trustLineKey)
	if err != nil {
		return ter.TefINTERNAL
	}
	if trustLineData == nil {
		return ter.TecNO_LINE
	}

	rs, err := state.ParseRippleState(trustLineData)
	if err != nil {
		return ter.TefINTERNAL
	}

	// Check authorization flag based on account ordering
	// Reference: rippled — if (account > issue.account) check lsfLowAuth else lsfHighAuth
	// When account > issuer: issuer is the LOW account → check LsfLowAuth
	// When account < issuer: issuer is the HIGH account → check LsfHighAuth
	if state.CompareAccountIDs(accountID, issuerID) > 0 {
		if rs.Flags&state.LsfLowAuth == 0 {
			return ter.TecNO_AUTH
		}
	} else {
		if rs.Flags&state.LsfHighAuth == 0 {
			return ter.TecNO_AUTH
		}
	}

	return ter.TesSUCCESS
}

// requireMPTAuthForEscrow checks MPT authorization for escrow operations.
// Uses WeakAuth semantics: if account has no MPToken, pass (don't fail).
// Only fail if lsfMPTRequireAuth is set AND MPToken exists but is not authorized.
// Reference: rippled View.cpp requireAuth(view, MPTIssue, account, WeakAuth)
func requireMPTAuthForEscrow(view tx.LedgerView, issuanceFlags uint32, issuanceKey keylet.Keylet, accountID, issuerID [20]byte) ter.Result {
	// Issuer is always authorized
	if issuerID == accountID {
		return ter.TesSUCCESS
	}

	// If requireAuth is not set, pass
	if issuanceFlags&entry.LsfMPTRequireAuth == 0 {
		return ter.TesSUCCESS
	}

	// WeakAuth: if MPToken doesn't exist, pass (destination may not hold yet)
	tokenKey := keylet.MPToken(issuanceKey.Key, accountID)
	tokenData, err := view.Read(tokenKey)
	if err != nil {
		return ter.TefINTERNAL
	}
	if tokenData == nil {
		// WeakAuth: no token is OK
		return ter.TesSUCCESS
	}

	token, err := state.ParseMPToken(tokenData)
	if err != nil {
		return ter.TefINTERNAL
	}

	// Token exists but is not authorized
	if token.Flags&entry.LsfMPTAuthorized == 0 {
		return ter.TecNO_AUTH
	}

	return ter.TesSUCCESS
}

// isMPTFrozen checks if an MPT is frozen for a given account.
// Checks global lock on issuance + individual lock on MPToken.
// Reference: rippled View.cpp isFrozen(view, account, MPTIssue)
func isMPTFrozen(view tx.LedgerView, issuanceFlags uint32, issuanceKey keylet.Keylet, accountID, issuerID [20]byte) (bool, ter.Result) {
	// Issuer is never frozen
	if issuerID == accountID {
		return false, ter.TesSUCCESS
	}

	// Global lock: issuance has lsfMPTLocked
	if issuanceFlags&entry.LsfMPTLocked != 0 {
		return true, ter.TesSUCCESS
	}

	// Individual lock: MPToken has lsfMPTLocked
	tokenKey := keylet.MPToken(issuanceKey.Key, accountID)
	tokenData, err := view.Read(tokenKey)
	if err != nil {
		return false, ter.TefINTERNAL
	}
	if tokenData == nil {
		return false, ter.TesSUCCESS
	}

	token, err := state.ParseMPToken(tokenData)
	if err != nil {
		return false, ter.TefINTERNAL
	}

	return token.Flags&entry.LsfMPTLocked != 0, ter.TesSUCCESS
}

// canTransferMPT checks if MPT can be transferred between two accounts.
// If LsfMPTCanTransfer is not set, at least one party must be the issuer.
// Reference: rippled View.cpp canTransfer(view, MPTIssue, from, to)
func canTransferMPT(issuance *state.MPTokenIssuanceData, fromID, toID [20]byte) ter.Result {
	if issuance.Flags&entry.LsfMPTCanTransfer != 0 {
		return ter.TesSUCCESS
	}

	// If neither party is the issuer, cannot transfer
	if fromID != issuance.Issuer && toID != issuance.Issuer {
		return ter.TecNO_AUTH
	}

	return ter.TesSUCCESS
}

// getMPTTransferRate computes the transfer rate from an MPT transfer fee.
// Formula: uint32(transferFee) * 10_000 + 1_000_000_000
// Reference: rippled View.cpp transferRate(view, MPTID) — "1'000'000'000u + 10'000 * sle->getFieldU16(sfTransferFee)"
func getMPTTransferRate(transferFee uint16) uint32 {
	return uint32(transferFee)*10_000 + 1_000_000_000
}

// mptIssuanceKeyFromHex decodes a hex MPT issuance ID and returns the keylet.
func mptIssuanceKeyFromHex(hexID string) (keylet.Keylet, error) {
	idBytes, err := hex.DecodeString(hexID)
	if err != nil || len(idBytes) != 24 {
		return keylet.Keylet{}, fmt.Errorf("invalid MPT issuance ID hex: %s", hexID)
	}
	var mptID [24]byte
	copy(mptID[:], idBytes)
	return keylet.MPTIssuance(mptID), nil
}

// reconstructAmountFromEscrow builds a tx.Amount from EscrowData.
// Used when reading back the escrow SLE to determine what was locked.
func reconstructAmountFromEscrow(escrow *state.EscrowData) tx.Amount {
	if escrow.IsXRP {
		return tx.NewXRPAmount(int64(escrow.Amount))
	}

	if escrow.MPTIssuanceID != "" {
		// MPT amount — extract issuer r-address from the issuance ID (last 20 bytes)
		var raw int64
		if escrow.MPTAmount != nil {
			raw = *escrow.MPTAmount
		} else if escrow.IOUAmount != nil {
			raw = escrow.IOUAmount.IOU().Mantissa()
		}
		issuer := mptIssuerFromIssuanceID(escrow.MPTIssuanceID)
		return state.NewMPTAmountWithIssuanceID(raw, issuer, escrow.MPTIssuanceID)
	}

	// IOU amount
	if escrow.IOUAmount != nil {
		return *escrow.IOUAmount
	}

	return tx.NewXRPAmount(0)
}

// mptIssuerAccountID extracts the raw 20-byte issuer account ID from a
// hex-encoded MPTIssuanceID (24 bytes = 4-byte sequence + 20-byte account).
// This is the binary equivalent of rippled's MPTIssue::getIssuer().
func mptIssuerAccountID(hexID string) ([20]byte, error) {
	idBytes, err := hex.DecodeString(hexID)
	if err != nil || len(idBytes) < 24 {
		return [20]byte{}, fmt.Errorf("invalid MPTIssuanceID hex: %q", hexID)
	}
	var accountID [20]byte
	copy(accountID[:], idBytes[4:24])
	return accountID, nil
}

// mptIssuerFromIssuanceID extracts the issuer r-address from a hex-encoded
// MPTIssuanceID (24 bytes = 4-byte sequence + 20-byte account).
func mptIssuerFromIssuanceID(hexID string) string {
	accountID, err := mptIssuerAccountID(hexID)
	if err != nil {
		return ""
	}
	addr, err := state.EncodeAccountID(accountID)
	if err != nil {
		return ""
	}
	return addr
}

// 7. Trust Line Helpers for Unlock

// createTrustLineForEscrow creates a zero-balance trust line between issuer and
// receiver for escrow unlock, delegating to the shared tx.TrustCreate. The
// receiver is the account being set (it pays the reserve); the issuer is the
// peer. The receiver owns the new line, so its OwnerCount is bumped via the view.
// Reference: rippled Escrow.cpp lines 837-877 (calls trustCreate)
func createTrustLineForEscrow(
	ctx *tx.ApplyContext,
	common *tx.Common,
	issuerID, receiverID [20]byte,
	currency string,
	receiverAcct *state.AccountRoot,
	recvLow bool,
	bumpOwnerCount bool,
) ter.Result {
	trustLineKey := keylet.Line(receiverID, issuerID, currency)

	receiverStr, err := state.EncodeAccountID(receiverID)
	if err != nil {
		return ter.TefINTERNAL
	}

	// The account-being-set's (receiver's) noRipple is derived from its own
	// lsfDefaultRipple, exactly as rippled's escrow trustCreate call.
	// Reference: rippled Escrow.cpp:862 (sleDest->getFlags() & lsfDefaultRipple) == 0.
	receiverNoRipple := receiverAcct.Flags&state.LsfDefaultRipple == 0

	result := tx.TrustCreate(ctx.View, tx.TrustCreateParams{
		SrcHigh:     recvLow,
		Src:         issuerID,
		Dst:         receiverID,
		LineKey:     trustLineKey,
		LimitIssuer: receiverID,
		NoRipple:    receiverNoRipple,
		Balance:     state.NewIssuedAmountFromValue(0, state.MinExponent, currency, state.AccountOneAddress),
		Limit:       tx.NewIssuedAmount(0, state.MinExponent, currency, receiverStr),
	})
	if result != ter.TesSUCCESS {
		return result
	}

	// Increment OwnerCount for the destination (receiver). On the cancel path
	// rippled bumps the soon-erased escrow SLE instead of a real account, so the
	// caller passes bumpOwnerCount=false and no account is charged for the line.
	if bumpOwnerCount {
		sponsor, result := tx.IncreaseOwnerCount(ctx, common, receiverID, receiverAcct, 1)
		if result != ter.TesSUCCESS {
			return result
		}
		if sponsor != "" {
			lineData, err := ctx.View.Read(trustLineKey)
			if err != nil || lineData == nil {
				return ter.TefINTERNAL
			}
			field := "HighSponsor"
			if recvLow {
				field = "LowSponsor"
			}
			lineData, err = tx.SetLedgerEntrySponsor(lineData, field, sponsor)
			if err != nil || ctx.View.Update(trustLineKey, lineData) != nil {
				return ter.TefINTERNAL
			}
		}
	}

	return ter.TesSUCCESS
}

// rippleCreditForEscrow credits IOU from issuer to receiver by modifying the
// trust line balance. This is the unlock-side direction of rippleCreditEscrow.
// Reference: rippled View.cpp rippleCredit(issuer, receiver, amount)
func rippleCreditForEscrow(
	view tx.LedgerView,
	issuerID, receiverID [20]byte,
	amount tx.Amount,
	numberContext state.NumberContext,
) ter.Result {
	return rippleCreditEscrow(
		view,
		issuerID,
		receiverID,
		amount,
		ter.TecNO_LINE,
		numberContext,
	)
}

// rippleCreditEscrow moves an IOU amount from payerID to payeeID along their
// existing trust line, with no auto-creation: the escrow lock/unlock callers
// guarantee the line exists. Balance convention: a positive balance means the
// low account owes the high account, so the payer subtracts when it is the low
// account and adds when it is the high account.
//
// A genuine view read error is internal; missingResult is returned when the
// line is absent.
// Reference: rippled View.cpp rippleCredit(sender, receiver, amount).
func rippleCreditEscrow(
	view tx.LedgerView,
	payerID, payeeID [20]byte,
	amount tx.Amount,
	missingResult ter.Result,
	numberContext state.NumberContext,
) ter.Result {
	if amount.IsZero() {
		return ter.TesSUCCESS
	}

	trustLineKey := keylet.Line(payerID, payeeID, amount.Currency)
	trustLineData, err := view.Read(trustLineKey)
	if err != nil {
		return ter.TefINTERNAL
	}
	if trustLineData == nil {
		return missingResult
	}

	rs, err := state.ParseRippleState(trustLineData)
	if err != nil {
		return ter.TefINTERNAL
	}

	payerIsLow := state.CompareAccountIDs(payerID, payeeID) < 0
	if payerIsLow {
		newBalance, err := rs.Balance.SubWithNumberContext(
			amount,
			numberContext,
			state.RoundToNearest,
		)
		if err != nil {
			return ter.TefINTERNAL
		}
		rs.Balance = newBalance
	} else {
		newBalance, err := rs.Balance.AddWithNumberContext(
			amount,
			numberContext,
			state.RoundToNearest,
		)
		if err != nil {
			return ter.TefINTERNAL
		}
		rs.Balance = newBalance
	}

	updated, err := state.SerializeRippleState(rs)
	if err != nil {
		return ter.TefINTERNAL
	}
	if err := view.Update(trustLineKey, updated); err != nil {
		return ter.TefINTERNAL
	}

	return ter.TesSUCCESS
}

// checkTrustLineLimit verifies the trust line limit isn't exceeded by the unlock.
// Reference: rippled Escrow.cpp lines 908-931
func checkTrustLineLimit(
	view tx.LedgerView,
	receiverID, issuerID [20]byte,
	currency string,
	finalAmount tx.Amount,
	issuerHigh bool,
	numberContext state.NumberContext,
) ter.Result {
	trustLineKey := keylet.Line(receiverID, issuerID, currency)
	trustLineData, err := view.Read(trustLineKey)
	if err != nil {
		return ter.TefINTERNAL
	}
	if trustLineData == nil {
		return ter.TecINTERNAL
	}

	rs, err := state.ParseRippleState(trustLineData)
	if err != nil {
		return ter.TefINTERNAL
	}

	// If the issuer is the high, then we use the low limit, otherwise the high limit
	// Reference: rippled line 916-917
	var lineLimit tx.Amount
	if issuerHigh {
		lineLimit = rs.LowLimit
	} else {
		lineLimit = rs.HighLimit
	}

	// Get the balance, flip sign if issuer is not high
	lineBalance := rs.Balance
	if !issuerHigh {
		lineBalance = lineBalance.Negate()
	}

	newBalance, err := lineBalance.AddWithNumberContext(
		finalAmount,
		numberContext,
		state.RoundToNearest,
	)
	if err != nil {
		return ter.TefINTERNAL
	}

	// If the transfer would exceed the line limit, return tecLIMIT_EXCEEDED
	if lineLimit.Compare(newBalance) < 0 {
		return ter.TecLIMIT_EXCEEDED
	}

	return ter.TesSUCCESS
}

// divideAmountByRate computes amount * QUALITY_ONE / rate for IOU amounts.
// This implements rippled's divideRound(amount, rate, issue, true) for escrow.
// Reference: rippled Rate2.cpp divideRound → divRound
func divideAmountByRate(
	amount tx.Amount,
	rate uint32,
	numberContext state.NumberContext,
) tx.Amount {
	if rate == parityRate {
		return amount
	}

	rateAmount := state.NewIssuedAmountFromValue(int64(rate), -9, "", "")
	return state.DivRoundWithNumberContext(
		amount,
		rateAmount,
		amount.Currency,
		amount.Issuer,
		numberContext,
		true,
	)
}

// createMPTokenForEscrow creates a new MPToken SLE for holderID during escrow unlock.
// Reference: rippled MPTokenAuthorize::createMPToken pattern
func createMPTokenForEscrow(
	ctx *tx.ApplyContext,
	common *tx.Common,
	issuanceKey keylet.Keylet,
	mptHexID string,
	holderID [20]byte,
	holderAccount *state.AccountRoot,
	bumpOwnerCount bool,
) ter.Result {
	// Decode MPT issuance ID to [24]byte
	idBytes, err := hex.DecodeString(mptHexID)
	if err != nil || len(idBytes) != 24 {
		return ter.TefINTERNAL
	}
	var mptIssuanceID [24]byte
	copy(mptIssuanceID[:], idBytes)

	tokenKey := keylet.MPToken(issuanceKey.Key, holderID)

	tokenData := &state.MPTokenData{
		Account:           holderID,
		MPTokenIssuanceID: mptIssuanceID,
		Flags:             0,
		MPTAmount:         0,
	}

	// Insert into owner directory first so sfOwnerNode records the actual page.
	// Reference: rippled MPTokenAuthorize.cpp:161-171 (mirrored by createMPToken).
	ownerDirKey := keylet.OwnerDir(holderID)
	dirResult, err := state.DirInsert(ctx.View, ownerDirKey, tokenKey.Key, false, func(dir *state.DirectoryNode) {
		dir.Owner = holderID
	})
	if err != nil {
		return mapDirInsertError(err)
	}
	tokenData.OwnerNode = dirResult.Page

	if bumpOwnerCount {
		sponsor, result := tx.IncreaseOwnerCount(ctx, common, holderID, holderAccount, 1)
		if result != ter.TesSUCCESS {
			return result
		}
		tokenData.Sponsor = sponsor
	}
	data, err := state.SerializeMPToken(tokenData)
	if err != nil {
		return ter.TefINTERNAL
	}
	if err := ctx.View.Insert(tokenKey, data); err != nil {
		return ter.TefINTERNAL
	}

	// On the cancel path rippled bumps the soon-erased escrow SLE rather than a
	// real account, so the caller passes bumpOwnerCount=false.
	return ter.TesSUCCESS
}

// Internal helpers

// accountHoldsIOU returns the IOU balance for an account (ignoring freeze).
// Positive balance means the account holds tokens.
// Reference: rippled View.cpp accountHolds with fhIGNORE_FREEZE
func accountHoldsIOU(view tx.LedgerView, accountID, issuerID [20]byte, currency string) (tx.Amount, ter.Result) {
	issuerStr, err := state.EncodeAccountID(issuerID)
	if err != nil {
		return tx.NewIssuedAmount(0, 0, currency, ""), ter.TefINTERNAL
	}

	trustLineKey := keylet.Line(accountID, issuerID, currency)
	trustLineData, err := view.Read(trustLineKey)
	if err != nil {
		return tx.NewIssuedAmount(0, 0, currency, issuerStr), ter.TefINTERNAL
	}
	if trustLineData == nil {
		return tx.NewIssuedAmount(0, 0, currency, issuerStr), ter.TecNO_LINE
	}

	rs, err := state.ParseRippleState(trustLineData)
	if err != nil {
		return tx.NewIssuedAmount(0, 0, currency, issuerStr), ter.TefINTERNAL
	}

	// Determine balance based on canonical ordering
	accountIsLow := state.CompareAccountIDs(accountID, issuerID) < 0
	balance := rs.Balance
	if !accountIsLow {
		balance = balance.Negate()
	}

	if balance.Signum() <= 0 {
		return tx.NewIssuedAmount(0, 0, currency, issuerStr), ter.TesSUCCESS
	}

	return state.NewIssuedAmountFromValue(balance.IOU().Mantissa(), balance.IOU().Exponent(), currency, issuerStr), ter.TesSUCCESS
}

// accountHoldsMPT returns the MPT balance for an account (ignoring freeze/auth).
// Reference: rippled View.cpp accountHolds(view, account, MPTIssue, fhIGNORE_FREEZE, ahIGNORE_AUTH)
func accountHoldsMPT(view tx.LedgerView, issuanceKey keylet.Keylet, accountID [20]byte) (int64, ter.Result) {
	tokenKey := keylet.MPToken(issuanceKey.Key, accountID)
	tokenData, err := view.Read(tokenKey)
	if err != nil {
		return 0, ter.TefINTERNAL
	}
	if tokenData == nil {
		return 0, ter.TecOBJECT_NOT_FOUND
	}

	token, err := state.ParseMPToken(tokenData)
	if err != nil {
		return 0, ter.TefINTERNAL
	}

	return int64(token.MPTAmount), ter.TesSUCCESS
}

func mapDirInsertError(err error) ter.Result {
	if errors.Is(err, state.ErrDirFull) {
		return ter.TecDIR_FULL
	}
	return ter.TefINTERNAL
}

// computeMPTTransferFee computes the final amount after applying MPT transfer fee.
// Returns (originalAmount, finalAmount) where finalAmount accounts for the fee.
// If no fee applies, finalAmount == originalAmount.
// Reference: rippled Escrow.cpp escrowUnlockApplyHelper<MPTIssue> lines 1001-1009
func computeMPTTransferFee(
	view tx.LedgerView,
	lockedRate uint32,
	mptHexID string,
	senderID, receiverID [20]byte,
	originalAmount uint64,
	numberContext state.NumberContext,
) (uint64, uint64, ter.Result) {
	issuanceKey, err := mptIssuanceKeyFromHex(mptHexID)
	if err != nil {
		return originalAmount, originalAmount, ter.TefINTERNAL
	}

	issuanceData, err := view.Read(issuanceKey)
	if err != nil {
		return originalAmount, originalAmount, ter.TefINTERNAL
	}
	if issuanceData == nil {
		return originalAmount, originalAmount, ter.TecOBJECT_NOT_FOUND
	}

	issuance, err := state.ParseMPTokenIssuance(issuanceData)
	if err != nil {
		return originalAmount, originalAmount, ter.TefINTERNAL
	}

	issuerID := issuance.Issuer
	senderIsIssuer := issuerID == senderID
	receiverIsIssuer := issuerID == receiverID

	currentRate := parityRate
	if issuance.TransferFee > 0 {
		currentRate = getMPTTransferRate(issuance.TransferFee)
	}

	// Use min(lockedRate, currentRate)
	effectiveRate := lockedRate
	if effectiveRate == 0 {
		effectiveRate = currentRate
	} else if currentRate < effectiveRate {
		effectiveRate = currentRate
	}

	// Transfer fee only applies when neither party is issuer
	if (!senderIsIssuer && !receiverIsIssuer) && effectiveRate != parityRate {
		// fee = amount - divideRound(amount, rate, asset, true)
		amount := state.NewMPTAmountWithIssuanceID(
			int64(originalAmount),
			state.EncodeAccountIDSafe(issuerID),
			mptHexID,
		)
		rate := state.NewIssuedAmountFromValue(int64(effectiveRate), -9, "", "")

		// rippled's muldivRound throws if the pre-canonicalization quotient does
		// not fit in uint64. Guard this path before DivRoundMPT's Uint64 conversion
		// so an oversized MPT finish becomes tefEXCEPTION instead of truncating.
		numVal, _ := state.PrepareMulDivOperand(amount)
		denVal, _ := state.PrepareMulDivOperand(rate)
		rounded := new(big.Int).Mul(big.NewInt(numVal), big.NewInt(100_000_000_000_000_000))
		rounded.Add(rounded, big.NewInt(denVal-1))
		rounded.Quo(rounded, big.NewInt(denVal))
		if !rounded.IsUint64() {
			panic("MPT divRound overflow")
		}

		finalAmount := uint64(state.DivRoundMPTWithNumberContext(
			amount,
			rate,
			numberContext,
			true,
		))
		return originalAmount, finalAmount, ter.TesSUCCESS
	}

	return originalAmount, originalAmount, ter.TesSUCCESS
}

// canAddIOUAmounts checks whether adding two IOU amounts would lose unacceptable
// precision due to the mantissa/exponent representation of IOU values.
// Reference: rippled STAmount.cpp canAdd() lines 527-588 (IOU case, lines 557-565)
//
// The check works by round-tripping through IOU add/sub (which can lose precision)
// and then measuring the relative error:
//
//	lhs = ((a - b) + b) / a - 1
//	rhs = ((b - a) + a) / b - 1
//	return |lhs| + |rhs| <= 1e-4
func canAddIOUAmounts(a, b tx.Amount, numberContext state.NumberContext) bool {
	// If either is zero, addition is always safe
	if a.IsZero() || b.IsZero() {
		return true
	}

	// Perform (a - b) + b using IOU precision (this is where precision loss occurs)
	aMinusB, _ := a.SubWithNumberContext(b, numberContext, state.RoundToNearest)
	roundTripA, _ := aMinusB.AddWithNumberContext(
		b,
		numberContext,
		state.RoundToNearest,
	)

	// Perform (b - a) + a using IOU precision
	bMinusA, _ := b.SubWithNumberContext(a, numberContext, state.RoundToNearest)
	roundTripB, _ := bMinusA.AddWithNumberContext(
		a,
		numberContext,
		state.RoundToNearest,
	)

	// Convert to big.Rat for exact division and comparison
	ratA := iouAmountToRat(a)
	ratB := iouAmountToRat(b)
	ratRTA := iouAmountToRat(roundTripA)
	ratRTB := iouAmountToRat(roundTripB)

	// one = 1
	one := new(big.Rat).SetInt64(1)

	// lhs = roundTripA / a - 1
	lhs := new(big.Rat).Quo(ratRTA, ratA)
	lhs.Sub(lhs, one)

	// rhs = roundTripB / b - 1
	rhs := new(big.Rat).Quo(ratRTB, ratB)
	rhs.Sub(rhs, one)

	// |lhs| + |rhs|
	lhs.Abs(lhs)
	rhs.Abs(rhs)
	total := new(big.Rat).Add(lhs, rhs)

	// maxLoss = 1e-4 (IOUAmount{1, -4})
	maxLoss := new(big.Rat).SetFrac64(1, 10000)

	return total.Cmp(maxLoss) <= 0
}

// iouAmountToRat converts an IOU Amount to a big.Rat for exact arithmetic.
// Returns mantissa * 10^exponent as a rational number.
func iouAmountToRat(a tx.Amount) *big.Rat {
	mantissa := a.Mantissa()
	exponent := a.Exponent()

	r := new(big.Rat).SetInt64(mantissa)
	if exponent > 0 {
		scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(exponent)), nil)
		r.Mul(r, new(big.Rat).SetInt(scale))
	} else if exponent < 0 {
		scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(-exponent)), nil)
		r.Quo(r, new(big.Rat).SetInt(scale))
	}
	return r
}
