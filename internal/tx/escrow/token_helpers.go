// Package escrow implements EscrowCreate, EscrowFinish, and EscrowCancel transactions.
// This file contains helpers for IOU and MPT escrow preclaim validation,
// lock/unlock operations, and shared utilities.
// Reference: rippled Escrow.cpp and View.cpp
package escrow

import (
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"

	"github.com/LeJamon/goXRPLd/amendment"
	addresscodec "github.com/LeJamon/goXRPLd/codec/addresscodec"
	binarycodec "github.com/LeJamon/goXRPLd/codec/binarycodec"
	"github.com/LeJamon/goXRPLd/internal/ledger/state"
	"github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/keylet"
	entry "github.com/LeJamon/goXRPLd/ledger/entry"
)

// parityRate is the identity transfer rate (no fee). Matches rippled's parityRate.
const parityRate uint32 = 1_000_000_000

// ---------------------------------------------------------------------------
// 1. EscrowCreate Preclaim Helpers
// ---------------------------------------------------------------------------

// escrowCreatePreclaimIOU validates IOU escrow creation preconditions.
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
	sleIssuer, err := readAccountRoot(view, issuerID)
	if err != nil || sleIssuer == nil {
		return tx.TecNO_ISSUER
	}
	if sleIssuer.Flags&state.LsfAllowTrustLineLocking == 0 {
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
	// Reference: rippled lines 232-237
	// If balance is positive, issuer must have higher address than account
	// If balance is negative, issuer must have lower address than account
	if rs.Balance.Signum() > 0 && state.CompareAccountIDsForLine(issuerID, accountID) < 0 {
		return tx.TecNO_PERMISSION
	}
	if rs.Balance.Signum() < 0 && state.CompareAccountIDsForLine(issuerID, accountID) > 0 {
		return tx.TecNO_PERMISSION
	}

	// requireAuth for sender
	if ter := requireAuthIOU(view, issuerID, accountID, amount.Currency); ter != tx.TesSUCCESS {
		return ter
	}

	// requireAuth for destination
	if ter := requireAuthIOU(view, issuerID, destID, amount.Currency); ter != tx.TesSUCCESS {
		return ter
	}

	// Freeze checks (isFrozen includes global freeze + individual freeze)
	if isFrozenIOU(view, accountID, issuerID, amount.Currency) {
		return tx.TecFROZEN
	}
	if isFrozenIOU(view, destID, issuerID, amount.Currency) {
		return tx.TecFROZEN
	}

	// Spendable amount check (ignore freeze since we already checked)
	spendable := accountHoldsIOU(view, accountID, issuerID, amount.Currency)
	if spendable.Signum() <= 0 {
		return tx.TecINSUFFICIENT_FUNDS
	}
	if spendable.Compare(amount) < 0 {
		return tx.TecINSUFFICIENT_FUNDS
	}

	// TODO: canAdd / precision check (tecPRECISION_LOSS)

	return tx.TesSUCCESS
}

// escrowCreatePreclaimMPT validates MPT escrow creation preconditions.
// Reference: rippled Escrow.cpp escrowCreatePreclaimHelper<MPTIssue> lines 283-359
func escrowCreatePreclaimMPT(view tx.LedgerView, rules *amendment.Rules, accountID, destID [20]byte, amount tx.Amount) tx.Result {
	// FeatureMPTokensV1 must be enabled
	if !rules.Enabled(amendment.FeatureMPTokensV1) {
		return tx.TemDISABLED
	}

	issuerID, err := state.DecodeAccountID(amount.Issuer)
	if err != nil {
		return tx.TefINTERNAL
	}

	// Issuer cannot create escrow
	if issuerID == accountID {
		return tx.TecNO_PERMISSION
	}

	// MPTIssuance must exist
	issuanceKey, err := mptIssuanceKeyFromHex(amount.MPTIssuanceID())
	if err != nil {
		return tx.TefINTERNAL
	}
	issuanceData, err := view.Read(issuanceKey)
	if err != nil || issuanceData == nil {
		return tx.TecOBJECT_NOT_FOUND
	}

	issuance, err := state.ParseMPTokenIssuance(issuanceData)
	if err != nil {
		return tx.TefINTERNAL
	}

	// Must have lsfMPTCanEscrow flag
	if issuance.Flags&entry.LsfMPTCanEscrow == 0 {
		return tx.TecNO_PERMISSION
	}

	// Issuance issuer must match amount issuer
	if issuance.Issuer != issuerID {
		return tx.TecNO_PERMISSION
	}

	// Sender must hold MPToken
	tokenKey := keylet.MPToken(issuanceKey.Key, accountID)
	exists, _ := view.Exists(tokenKey)
	if !exists {
		return tx.TecOBJECT_NOT_FOUND
	}

	// requireAuth for sender (WeakAuth)
	if ter := requireMPTAuthForEscrow(view, issuance.Flags, issuanceKey, accountID, issuerID); ter != tx.TesSUCCESS {
		return ter
	}

	// requireAuth for destination (WeakAuth)
	if ter := requireMPTAuthForEscrow(view, issuance.Flags, issuanceKey, destID, issuerID); ter != tx.TesSUCCESS {
		return ter
	}

	// Frozen checks (global lock on issuance or individual lock on token)
	if isMPTFrozen(view, issuance.Flags, issuanceKey, accountID, issuerID) {
		return tx.TecLOCKED
	}
	if isMPTFrozen(view, issuance.Flags, issuanceKey, destID, issuerID) {
		return tx.TecLOCKED
	}

	// canTransfer check (holder-to-holder needs LsfMPTCanTransfer)
	if ter := canTransferMPT(view, issuanceKey, issuance, accountID, destID); ter != tx.TesSUCCESS {
		return ter
	}

	// Balance check (ignore freeze since we already checked)
	spendable := accountHoldsMPT(view, issuanceKey, accountID)
	if spendable <= 0 {
		return tx.TecINSUFFICIENT_FUNDS
	}

	raw, ok := amount.MPTRaw()
	if !ok {
		// Fallback to IOU value
		raw = amount.IOU().Mantissa()
	}
	if spendable < raw {
		return tx.TecINSUFFICIENT_FUNDS
	}

	return tx.TesSUCCESS
}

// ---------------------------------------------------------------------------
// 2. EscrowFinish Preclaim Helpers
// ---------------------------------------------------------------------------

// escrowFinishPreclaimIOU validates IOU escrow finish preconditions.
// Reference: rippled Escrow.cpp lines 702-724
func escrowFinishPreclaimIOU(view tx.LedgerView, destID [20]byte, amount tx.Amount) tx.Result {
	issuerID, err := state.DecodeAccountID(amount.Issuer)
	if err != nil {
		return tx.TefINTERNAL
	}

	// If dest == issuer, return tesSUCCESS
	if issuerID == destID {
		return tx.TesSUCCESS
	}

	// requireAuth on destination
	if ter := requireAuthIOU(view, issuerID, destID, amount.Currency); ter != tx.TesSUCCESS {
		return ter
	}

	// Deep freeze check on destination
	if tx.IsDeepFrozen(view, destID, issuerID, amount.Currency) {
		return tx.TecFROZEN
	}

	return tx.TesSUCCESS
}

// escrowFinishPreclaimMPT validates MPT escrow finish preconditions.
// Reference: rippled Escrow.cpp lines 726-758
func escrowFinishPreclaimMPT(view tx.LedgerView, destID [20]byte, amount tx.Amount) tx.Result {
	issuerID, err := state.DecodeAccountID(amount.Issuer)
	if err != nil {
		return tx.TefINTERNAL
	}

	// If dest == issuer, return tesSUCCESS
	if issuerID == destID {
		return tx.TesSUCCESS
	}

	// MPTIssuance must exist
	issuanceKey, err := mptIssuanceKeyFromHex(amount.MPTIssuanceID())
	if err != nil {
		return tx.TefINTERNAL
	}
	issuanceData, err := view.Read(issuanceKey)
	if err != nil || issuanceData == nil {
		return tx.TecOBJECT_NOT_FOUND
	}

	issuance, err := state.ParseMPTokenIssuance(issuanceData)
	if err != nil {
		return tx.TefINTERNAL
	}

	// requireAuth on destination (WeakAuth)
	if ter := requireMPTAuthForEscrow(view, issuance.Flags, issuanceKey, destID, issuerID); ter != tx.TesSUCCESS {
		return ter
	}

	// Frozen check on destination
	if isMPTFrozen(view, issuance.Flags, issuanceKey, destID, issuerID) {
		return tx.TecLOCKED
	}

	return tx.TesSUCCESS
}

// ---------------------------------------------------------------------------
// 3. EscrowCancel Preclaim Helpers
// ---------------------------------------------------------------------------

// escrowCancelPreclaimIOU validates IOU escrow cancel preconditions.
// Reference: rippled Escrow.cpp lines 1219-1237
func escrowCancelPreclaimIOU(view tx.LedgerView, accountID [20]byte, amount tx.Amount) tx.Result {
	issuerID, err := state.DecodeAccountID(amount.Issuer)
	if err != nil {
		return tx.TefINTERNAL
	}

	// Issuer == account is an internal error
	if issuerID == accountID {
		return tx.TecINTERNAL
	}

	// requireAuth on account
	if ter := requireAuthIOU(view, issuerID, accountID, amount.Currency); ter != tx.TesSUCCESS {
		return ter
	}

	return tx.TesSUCCESS
}

// escrowCancelPreclaimMPT validates MPT escrow cancel preconditions.
// Reference: rippled Escrow.cpp lines 1239-1267
func escrowCancelPreclaimMPT(view tx.LedgerView, accountID [20]byte, amount tx.Amount) tx.Result {
	issuerID, err := state.DecodeAccountID(amount.Issuer)
	if err != nil {
		return tx.TefINTERNAL
	}

	// Issuer == account is an internal error
	if issuerID == accountID {
		return tx.TecINTERNAL
	}

	// MPTIssuance must exist
	issuanceKey, err := mptIssuanceKeyFromHex(amount.MPTIssuanceID())
	if err != nil {
		return tx.TefINTERNAL
	}
	issuanceData, err := view.Read(issuanceKey)
	if err != nil || issuanceData == nil {
		return tx.TecOBJECT_NOT_FOUND
	}

	issuance, err := state.ParseMPTokenIssuance(issuanceData)
	if err != nil {
		return tx.TefINTERNAL
	}

	// requireAuth on account (WeakAuth)
	if ter := requireMPTAuthForEscrow(view, issuance.Flags, issuanceKey, accountID, issuerID); ter != tx.TesSUCCESS {
		return ter
	}

	return tx.TesSUCCESS
}

// ---------------------------------------------------------------------------
// 4. Lock Helpers
// ---------------------------------------------------------------------------

// escrowLockMPT locks MPT tokens by decreasing sender's MPTAmount and increasing
// LockedAmount on both the MPToken and MPTIssuance.
// Reference: rippled View.cpp rippleLockEscrowMPT() lines 2853-2947
func escrowLockMPT(view tx.LedgerView, senderID [20]byte, amount tx.Amount) tx.Result {
	issuanceKey, err := mptIssuanceKeyFromHex(amount.MPTIssuanceID())
	if err != nil {
		return tx.TefINTERNAL
	}

	issuanceData, err := view.Read(issuanceKey)
	if err != nil || issuanceData == nil {
		return tx.TecOBJECT_NOT_FOUND
	}

	issuance, err := state.ParseMPTokenIssuance(issuanceData)
	if err != nil {
		return tx.TefINTERNAL
	}

	issuerID := issuance.Issuer
	if issuerID == senderID {
		return tx.TecINTERNAL
	}

	raw, ok := amount.MPTRaw()
	if !ok {
		raw = amount.IOU().Mantissa()
	}
	pay := uint64(raw)

	// 1. Update sender's MPToken: decrease MPTAmount, increase LockedAmount
	tokenKey := keylet.MPToken(issuanceKey.Key, senderID)
	tokenData, err := view.Read(tokenKey)
	if err != nil || tokenData == nil {
		return tx.TecOBJECT_NOT_FOUND
	}

	token, err := state.ParseMPToken(tokenData)
	if err != nil {
		return tx.TefINTERNAL
	}

	// Underflow check
	if token.MPTAmount < pay {
		return tx.TecINTERNAL
	}
	token.MPTAmount -= pay

	// Overflow check for locked amount
	locked := uint64(0)
	if token.LockedAmount != nil {
		locked = *token.LockedAmount
	}
	if locked > ^uint64(0)-pay {
		return tx.TecINTERNAL
	}
	newLocked := locked + pay
	token.LockedAmount = &newLocked

	updatedToken, err := state.SerializeMPToken(token)
	if err != nil {
		return tx.TefINTERNAL
	}
	if err := view.Update(tokenKey, updatedToken); err != nil {
		return tx.TefINTERNAL
	}

	// 2. Update MPTIssuance: increase LockedAmount
	issuanceLocked := uint64(0)
	if issuance.LockedAmount != nil {
		issuanceLocked = *issuance.LockedAmount
	}
	if issuanceLocked > ^uint64(0)-pay {
		return tx.TecINTERNAL
	}
	newIssuanceLocked := issuanceLocked + pay
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

// ---------------------------------------------------------------------------
// 5. Unlock Helpers
// ---------------------------------------------------------------------------

// escrowUnlockIOU unlocks IOU tokens during EscrowFinish or EscrowCancel.
// Handles trust line creation, transfer fee calculation, limit checking,
// and crediting the receiver.
// Reference: rippled Escrow.cpp escrowUnlockApplyHelper<Issue> lines 809-942
func escrowUnlockIOU(
	view tx.LedgerView,
	lockedRate uint32,
	destBalance uint64,
	destOwnerCount uint32,
	destID [20]byte,
	amount tx.Amount,
	senderID, receiverID [20]byte,
	createAsset bool,
	reserveBase, reserveIncrement uint64,
) tx.Result {
	issuerID, err := state.DecodeAccountID(amount.Issuer)
	if err != nil {
		return tx.TefINTERNAL
	}

	senderIsIssuer := issuerID == senderID
	receiverIsIssuer := issuerID == receiverID
	recvLow := state.CompareAccountIDsForLine(receiverID, issuerID) < 0
	issuerHigh := state.CompareAccountIDsForLine(issuerID, receiverID) > 0

	// Sender should never be the issuer for a locked escrow
	if senderIsIssuer {
		return tx.TecINTERNAL
	}

	// If receiver is the issuer, nothing to credit (tokens return to issuer)
	if receiverIsIssuer {
		return tx.TesSUCCESS
	}

	// Check if trust line exists
	trustLineKey := keylet.Line(receiverID, issuerID, amount.Currency)
	trustLineData, err := view.Read(trustLineKey)
	trustLineExists := err == nil && trustLineData != nil

	// Create trust line if needed
	if !trustLineExists && createAsset && !receiverIsIssuer {
		// Check reserve
		reserve := reserveBase + uint64(destOwnerCount+1)*reserveIncrement
		if destBalance < reserve {
			return tx.TecNO_LINE_INSUF_RESERVE
		}

		if ter := createTrustLineForEscrow(view, issuerID, receiverID, amount.Currency, destID, recvLow); ter != tx.TesSUCCESS {
			return ter
		}
		// Re-read after creation
		trustLineData, err = view.Read(trustLineKey)
		if err != nil || trustLineData == nil {
			return tx.TecINTERNAL
		}
		trustLineExists = true
	}

	if !trustLineExists && !receiverIsIssuer {
		return tx.TecNO_LINE
	}

	// Compute transfer fee
	// Get current rate from issuer, use min(lockedRate, currentRate)
	currentRate := getTransferRateForIssuer(view, issuerID)
	effectiveRate := lockedRate
	if currentRate != 0 && currentRate < effectiveRate {
		effectiveRate = currentRate
	}
	// If no rate was locked (0 or parityRate), use currentRate
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
		finalAmt = divideAmountByRate(amount, effectiveRate)
	}

	// Validate the line limit if the receiver is not creating a new trust line
	// (createAsset = false means receiver already submitted the finish tx)
	if !createAsset {
		if ter := checkTrustLineLimit(view, receiverID, issuerID, amount.Currency, finalAmt, issuerHigh); ter != tx.TesSUCCESS {
			return ter
		}
	}

	// Credit the receiver via rippleCredit (issuer -> receiver)
	if !receiverIsIssuer {
		if ter := rippleCreditForEscrow(view, issuerID, receiverID, finalAmt); ter != tx.TesSUCCESS {
			return ter
		}
	}

	return tx.TesSUCCESS
}

// escrowUnlockMPT unlocks MPT tokens during EscrowFinish or EscrowCancel.
// The caller (escrowUnlockApplyHelper<MPTIssue>) handles MPToken creation
// and transfer fee calculation, then calls rippleUnlockEscrowMPT with the
// final amount. In rippled, the finalAmount is used for ALL operations
// (LockedAmount decrement, receiver credit, OutstandingAmount decrement).
// The difference between originalAmount and finalAmount (the fee) stays
// permanently locked on the issuance and sender's token.
//
// This function combines the MPToken creation logic from
// escrowUnlockApplyHelper<MPTIssue> with the actual unlock from
// rippleUnlockEscrowMPT for convenience.
//
// Reference: rippled Escrow.cpp escrowUnlockApplyHelper<MPTIssue> lines 944-1012
// Reference: rippled View.cpp rippleUnlockEscrowMPT() lines 2950-3094
func escrowUnlockMPT(
	view tx.LedgerView,
	senderID, receiverID [20]byte,
	finalAmount uint64,
	mptHexID string,
	createAsset bool,
	destBalance uint64,
	destOwnerCount uint32,
	destID [20]byte,
	reserveBase, reserveIncrement uint64,
) tx.Result {
	issuanceKey, err := mptIssuanceKeyFromHex(mptHexID)
	if err != nil {
		return tx.TefINTERNAL
	}

	issuanceData, err := view.Read(issuanceKey)
	if err != nil || issuanceData == nil {
		return tx.TecOBJECT_NOT_FOUND
	}

	issuance, err := state.ParseMPTokenIssuance(issuanceData)
	if err != nil {
		return tx.TefINTERNAL
	}

	issuerID := issuance.Issuer
	receiverIsIssuer := issuerID == receiverID

	// Handle MPToken creation for receiver (from escrowUnlockApplyHelper)
	if !receiverIsIssuer {
		receiverTokenKey := keylet.MPToken(issuanceKey.Key, receiverID)
		receiverExists, _ := view.Exists(receiverTokenKey)

		if !receiverExists && createAsset {
			// Check reserve
			reserve := reserveBase + uint64(destOwnerCount+1)*reserveIncrement
			if destBalance < reserve {
				return tx.TecINSUFFICIENT_RESERVE
			}

			if ter := createMPTokenForEscrow(view, issuanceKey, mptHexID, receiverID, destID); ter != tx.TesSUCCESS {
				return ter
			}
		}

		// Re-check existence after potential creation
		receiverExists, _ = view.Exists(receiverTokenKey)
		if !receiverExists {
			return tx.TecNO_PERMISSION
		}
	}

	// --- rippleUnlockEscrowMPT logic below ---
	// Re-read issuance (might have been modified by createMPTokenForEscrow if it
	// is in the same view, but it shouldn't be — MPToken creation doesn't touch issuance)

	// 1. Decrease the Issuance LockedAmount by finalAmount
	// Reference: rippled lines 2968-2997
	if issuance.LockedAmount == nil {
		return tx.TecINTERNAL
	}
	issuanceLocked := *issuance.LockedAmount
	if issuanceLocked < finalAmount {
		return tx.TecINTERNAL
	}
	newIssuanceLocked := issuanceLocked - finalAmount
	if newIssuanceLocked == 0 {
		issuance.LockedAmount = nil
	} else {
		issuance.LockedAmount = &newIssuanceLocked
	}

	// 2. Handle receiver
	if receiverIsIssuer {
		// Decrease OutstandingAmount by finalAmount (tokens are redeemed)
		// Reference: rippled lines 3027-3044
		if issuance.OutstandingAmount < finalAmount {
			return tx.TecINTERNAL
		}
		issuance.OutstandingAmount -= finalAmount
	} else {
		// Increase receiver's MPTAmount by finalAmount
		// Reference: rippled lines 2999-3025
		receiverTokenKey := keylet.MPToken(issuanceKey.Key, receiverID)
		receiverTokenData, err := view.Read(receiverTokenKey)
		if err != nil || receiverTokenData == nil {
			return tx.TecOBJECT_NOT_FOUND
		}

		receiverToken, err := state.ParseMPToken(receiverTokenData)
		if err != nil {
			return tx.TefINTERNAL
		}

		// Overflow check
		if receiverToken.MPTAmount > ^uint64(0)-finalAmount {
			return tx.TecINTERNAL
		}
		receiverToken.MPTAmount += finalAmount

		updatedReceiverToken, err := state.SerializeMPToken(receiverToken)
		if err != nil {
			return tx.TefINTERNAL
		}
		if err := view.Update(receiverTokenKey, updatedReceiverToken); err != nil {
			return tx.TefINTERNAL
		}
	}

	// Write back issuance (with updated LockedAmount and possibly OutstandingAmount)
	updatedIssuance, err := state.SerializeMPTokenIssuance(issuance)
	if err != nil {
		return tx.TefINTERNAL
	}
	if err := view.Update(issuanceKey, updatedIssuance); err != nil {
		return tx.TefINTERNAL
	}

	// 3. Decrease sender's MPToken LockedAmount by finalAmount
	// Reference: rippled lines 3047-3092
	if issuerID == senderID {
		return tx.TecINTERNAL
	}

	senderTokenKey := keylet.MPToken(issuanceKey.Key, senderID)
	senderTokenData, err := view.Read(senderTokenKey)
	if err != nil || senderTokenData == nil {
		return tx.TecOBJECT_NOT_FOUND
	}

	senderToken, err := state.ParseMPToken(senderTokenData)
	if err != nil {
		return tx.TefINTERNAL
	}

	if senderToken.LockedAmount == nil {
		return tx.TecINTERNAL
	}
	senderLocked := *senderToken.LockedAmount
	if senderLocked < finalAmount {
		return tx.TecINTERNAL
	}
	newSenderLocked := senderLocked - finalAmount
	if newSenderLocked == 0 {
		senderToken.LockedAmount = nil
	} else {
		senderToken.LockedAmount = &newSenderLocked
	}

	updatedSenderToken, err := state.SerializeMPToken(senderToken)
	if err != nil {
		return tx.TefINTERNAL
	}
	if err := view.Update(senderTokenKey, updatedSenderToken); err != nil {
		return tx.TefINTERNAL
	}

	return tx.TesSUCCESS
}

// ---------------------------------------------------------------------------
// 6. Shared Utilities
// ---------------------------------------------------------------------------

// requireAuthIOU checks if an issuer requires authorization and if the account
// is authorized on the trust line.
// Reference: rippled View.cpp requireAuth(view, Issue, account) for IOU
// Uses the default (legacy) auth type: trust line must exist if requireAuth is set.
func requireAuthIOU(view tx.LedgerView, issuerID, accountID [20]byte, currency string) tx.Result {
	// Issuer is always authorized for own currency
	if issuerID == accountID {
		return tx.TesSUCCESS
	}

	// Read issuer account
	issuerAccount, err := readAccountRoot(view, issuerID)
	if err != nil || issuerAccount == nil {
		return tx.TefINTERNAL
	}

	// If issuer doesn't require auth, pass
	if issuerAccount.Flags&state.LsfRequireAuth == 0 {
		return tx.TesSUCCESS
	}

	// Issuer requires auth — check if the trust line exists and is authorized
	trustLineKey := keylet.Line(accountID, issuerID, currency)
	trustLineData, err := view.Read(trustLineKey)
	if err != nil || trustLineData == nil {
		return tx.TecNO_LINE
	}

	rs, err := state.ParseRippleState(trustLineData)
	if err != nil {
		return tx.TefINTERNAL
	}

	// Check authorization flag based on account ordering
	// Reference: rippled — if (account > issue.account) check lsfLowAuth else lsfHighAuth
	// When account > issuer: issuer is the LOW account → check LsfLowAuth
	// When account < issuer: issuer is the HIGH account → check LsfHighAuth
	if state.CompareAccountIDsForLine(accountID, issuerID) > 0 {
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

// requireMPTAuthForEscrow checks MPT authorization for escrow operations.
// Uses WeakAuth semantics: if account has no MPToken, pass (don't fail).
// Only fail if lsfMPTRequireAuth is set AND MPToken exists but is not authorized.
// Reference: rippled View.cpp requireAuth(view, MPTIssue, account, WeakAuth)
func requireMPTAuthForEscrow(view tx.LedgerView, issuanceFlags uint32, issuanceKey keylet.Keylet, accountID, issuerID [20]byte) tx.Result {
	// Issuer is always authorized
	if issuerID == accountID {
		return tx.TesSUCCESS
	}

	// If requireAuth is not set, pass
	if issuanceFlags&entry.LsfMPTRequireAuth == 0 {
		return tx.TesSUCCESS
	}

	// WeakAuth: if MPToken doesn't exist, pass (destination may not hold yet)
	tokenKey := keylet.MPToken(issuanceKey.Key, accountID)
	tokenData, err := view.Read(tokenKey)
	if err != nil || tokenData == nil {
		// WeakAuth: no token is OK
		return tx.TesSUCCESS
	}

	token, err := state.ParseMPToken(tokenData)
	if err != nil {
		return tx.TefINTERNAL
	}

	// Token exists but is not authorized
	if token.Flags&entry.LsfMPTAuthorized == 0 {
		return tx.TecNO_AUTH
	}

	return tx.TesSUCCESS
}

// isMPTFrozen checks if an MPT is frozen for a given account.
// Checks global lock on issuance + individual lock on MPToken.
// Reference: rippled View.cpp isFrozen(view, account, MPTIssue)
func isMPTFrozen(view tx.LedgerView, issuanceFlags uint32, issuanceKey keylet.Keylet, accountID, issuerID [20]byte) bool {
	// Issuer is never frozen
	if issuerID == accountID {
		return false
	}

	// Global lock: issuance has lsfMPTLocked
	if issuanceFlags&entry.LsfMPTLocked != 0 {
		return true
	}

	// Individual lock: MPToken has lsfMPTLocked
	tokenKey := keylet.MPToken(issuanceKey.Key, accountID)
	tokenData, err := view.Read(tokenKey)
	if err != nil || tokenData == nil {
		return false
	}

	token, err := state.ParseMPToken(tokenData)
	if err != nil {
		return false
	}

	return token.Flags&entry.LsfMPTLocked != 0
}

// canTransferMPT checks if MPT can be transferred between two accounts.
// If LsfMPTCanTransfer is not set, at least one party must be the issuer.
// Reference: rippled View.cpp canTransfer(view, MPTIssue, from, to)
func canTransferMPT(view tx.LedgerView, issuanceKey keylet.Keylet, issuance *state.MPTokenIssuanceData, fromID, toID [20]byte) tx.Result {
	if issuance.Flags&entry.LsfMPTCanTransfer != 0 {
		return tx.TesSUCCESS
	}

	// If neither party is the issuer, cannot transfer
	if fromID != issuance.Issuer && toID != issuance.Issuer {
		return tx.TecNO_AUTH
	}

	return tx.TesSUCCESS
}

// getTransferRateForIssuer reads the transfer rate from an issuer's AccountRoot.
// Returns parityRate if not set.
// Reference: rippled View.cpp transferRate(view, issuer)
func getTransferRateForIssuer(view tx.LedgerView, issuerID [20]byte) uint32 {
	account, err := readAccountRoot(view, issuerID)
	if err != nil || account == nil {
		return parityRate
	}
	if account.TransferRate == 0 {
		return parityRate
	}
	return account.TransferRate
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
		// MPT amount
		var raw int64
		if escrow.MPTAmount != nil {
			raw = *escrow.MPTAmount
		} else if escrow.IOUAmount != nil {
			raw = escrow.IOUAmount.IOU().Mantissa()
		}
		return state.NewMPTAmountDirect(raw, "", "")
	}

	// IOU amount
	if escrow.IOUAmount != nil {
		return *escrow.IOUAmount
	}

	return tx.NewXRPAmount(0)
}

// ---------------------------------------------------------------------------
// 7. Trust Line Helpers for Unlock
// ---------------------------------------------------------------------------

// createTrustLineForEscrow creates a zero-balance trust line between issuer and
// receiver for escrow unlock. Matches rippled's trustCreate pattern.
// Reference: rippled Escrow.cpp lines 837-877 (calls trustCreate)
func createTrustLineForEscrow(
	view tx.LedgerView,
	issuerID, receiverID [20]byte,
	currency string,
	destID [20]byte,
	recvLow bool,
) tx.Result {
	trustLineKey := keylet.Line(receiverID, issuerID, currency)

	// Determine low/high accounts
	var lowAccountID, highAccountID [20]byte
	if recvLow {
		lowAccountID = receiverID
		highAccountID = issuerID
	} else {
		lowAccountID = issuerID
		highAccountID = receiverID
	}

	lowAccountStr, err := state.EncodeAccountID(lowAccountID)
	if err != nil {
		return tx.TefINTERNAL
	}
	highAccountStr, err := state.EncodeAccountID(highAccountID)
	if err != nil {
		return tx.TefINTERNAL
	}

	// Zero initial balance with AccountOne as issuer (per rippled convention)
	balance := state.NewIssuedAmountFromValue(0, state.MinExponent-3, currency, state.AccountOneAddress)

	// Receiver gets a reserve flag. Set NoRipple based on DefaultRipple.
	// Reference: rippled trustCreate — bSetHigh ? lsfHighReserve : lsfLowReserve
	var flags uint32
	if recvLow {
		flags |= state.LsfLowReserve
	} else {
		flags |= state.LsfHighReserve
	}

	// Set NoRipple based on receiver's DefaultRipple
	receiverAcctData, err := view.Read(keylet.Account(receiverID))
	if err != nil || receiverAcctData == nil {
		return tx.TefINTERNAL
	}
	receiverAcct, err := state.ParseAccountRoot(receiverAcctData)
	if err != nil {
		return tx.TefINTERNAL
	}
	if receiverAcct.Flags&state.LsfDefaultRipple == 0 {
		if recvLow {
			flags |= state.LsfLowNoRipple
		} else {
			flags |= state.LsfHighNoRipple
		}
	}

	rs := &state.RippleState{
		Balance:   balance,
		LowLimit:  tx.NewIssuedAmount(0, state.MinExponent-3, currency, lowAccountStr),
		HighLimit: tx.NewIssuedAmount(0, state.MinExponent-3, currency, highAccountStr),
		Flags:     flags,
	}

	// Insert into LOW account's owner directory
	lowDirKey := keylet.OwnerDir(lowAccountID)
	lowDirResult, err := state.DirInsert(view, lowDirKey, trustLineKey.Key, func(dir *state.DirectoryNode) {
		dir.Owner = lowAccountID
	})
	if err != nil {
		return tx.TefINTERNAL
	}
	rs.LowNode = lowDirResult.Page

	// Insert into HIGH account's owner directory
	highDirKey := keylet.OwnerDir(highAccountID)
	highDirResult, err := state.DirInsert(view, highDirKey, trustLineKey.Key, func(dir *state.DirectoryNode) {
		dir.Owner = highAccountID
	})
	if err != nil {
		return tx.TefINTERNAL
	}
	rs.HighNode = highDirResult.Page

	// Serialize and insert the trust line
	trustLineData, err := state.SerializeRippleState(rs)
	if err != nil {
		return tx.TefINTERNAL
	}
	if err := view.Insert(trustLineKey, trustLineData); err != nil {
		return tx.TefINTERNAL
	}

	// Increment OwnerCount for the destination (receiver)
	adjustOwnerCountViaView(view, destID, 1)

	return tx.TesSUCCESS
}

// rippleCreditForEscrow credits IOU from issuer to receiver by modifying
// the trust line balance. This is the reverse of escrowLockIOU.
// Reference: rippled View.cpp rippleCredit(issuer, receiver, amount)
func rippleCreditForEscrow(view tx.LedgerView, issuerID, receiverID [20]byte, amount tx.Amount) tx.Result {
	if amount.IsZero() {
		return tx.TesSUCCESS
	}

	trustLineKey := keylet.Line(issuerID, receiverID, amount.Currency)
	trustLineData, err := view.Read(trustLineKey)
	if err != nil || trustLineData == nil {
		return tx.TecNO_LINE
	}

	rs, err := state.ParseRippleState(trustLineData)
	if err != nil {
		return tx.TefINTERNAL
	}

	// rippleCredit(issuer, receiver, amount) means issuer sends to receiver.
	// Convention: positive balance = low account holds tokens.
	// When issuer is low: issuer sends → receiver (high) gets → balance decreases (issuer owes less)
	//   Wait, that's wrong. Let me think again:
	//   positive balance = low account OWES high account. So if issuer is low and sends tokens
	//   to receiver (high), the low account's debt decreases → balance should decrease? No.
	//   rippleCredit(sender, receiver, amount): sender pays receiver.
	//   When sender is low: subtract from balance (low owes less / receiver gives back)
	//   Actually: rippleCredit means crediting the receiver. Let me match the existing escrowLockIOU pattern.
	//
	// For escrow unlock, we're doing the reverse of lock:
	//   Lock: rippleCredit(sender, issuer, amount) — sender pays issuer
	//   Unlock: rippleCredit(issuer, receiver, amount) — issuer pays receiver
	//
	// rippleCredit(sender=issuer, receiver, amount):
	//   When issuer is low: subtract from balance (issuer pays → balance decreases, issuer owes more)
	//   When issuer is high: add to balance (issuer pays → balance increases, receiver has more)
	issuerIsLow := state.CompareAccountIDsForLine(issuerID, receiverID) < 0

	if issuerIsLow {
		// Issuer is low, sending to receiver (high) → subtract from balance
		// (low account sends, balance decreases, meaning low owes more to high)
		newBalance, err := rs.Balance.Sub(amount)
		if err != nil {
			return tx.TefINTERNAL
		}
		rs.Balance = newBalance
	} else {
		// Issuer is high, sending to receiver (low) → add to balance
		// (high account sends, balance increases, meaning low has more from high)
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

// checkTrustLineLimit verifies the trust line limit isn't exceeded by the unlock.
// Reference: rippled Escrow.cpp lines 908-931
func checkTrustLineLimit(view tx.LedgerView, receiverID, issuerID [20]byte, currency string, finalAmount tx.Amount, issuerHigh bool) tx.Result {
	trustLineKey := keylet.Line(receiverID, issuerID, currency)
	trustLineData, err := view.Read(trustLineKey)
	if err != nil || trustLineData == nil {
		return tx.TecINTERNAL
	}

	rs, err := state.ParseRippleState(trustLineData)
	if err != nil {
		return tx.TefINTERNAL
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

	// Add the final amount to the balance
	newBalance, err := lineBalance.Add(finalAmount)
	if err != nil {
		return tx.TefINTERNAL
	}

	// If the transfer would exceed the line limit, return tecLIMIT_EXCEEDED
	if lineLimit.Compare(newBalance) < 0 {
		return tx.TecLIMIT_EXCEEDED
	}

	return tx.TesSUCCESS
}

// divideAmountByRate computes amount * QUALITY_ONE / rate for IOU amounts.
// This implements rippled's divideRound(amount, rate, issue, true) for escrow.
// Reference: rippled Rate2.cpp divideRound → divRound
func divideAmountByRate(amount tx.Amount, rate uint32) tx.Amount {
	if rate == parityRate {
		return amount
	}

	// For IOU: result = amount * 1_000_000_000 / rate
	// Use MulRatio which does amount * num / den
	return amount.MulRatio(parityRate, rate, true)
}

// createMPTokenForEscrow creates a new MPToken SLE for holderID during escrow unlock.
// Reference: rippled MPTokenAuthorize::createMPToken pattern
func createMPTokenForEscrow(
	view tx.LedgerView,
	issuanceKey keylet.Keylet,
	mptHexID string,
	holderID [20]byte,
	destID [20]byte,
) tx.Result {
	// Decode MPT issuance ID to [24]byte
	idBytes, err := hex.DecodeString(mptHexID)
	if err != nil || len(idBytes) != 24 {
		return tx.TefINTERNAL
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

	data, err := state.SerializeMPToken(tokenData)
	if err != nil {
		return tx.TefINTERNAL
	}
	if err := view.Insert(tokenKey, data); err != nil {
		return tx.TefINTERNAL
	}

	// Insert into owner directory
	ownerDirKey := keylet.OwnerDir(holderID)
	_, err = state.DirInsert(view, ownerDirKey, tokenKey.Key, func(dir *state.DirectoryNode) {
		dir.Owner = holderID
	})
	if err != nil {
		return tx.TecDIR_FULL
	}

	// Increment owner count for the destination
	adjustOwnerCountViaView(view, destID, 1)

	return tx.TesSUCCESS
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

// readAccountRoot reads and parses an AccountRoot from the ledger.
func readAccountRoot(view tx.LedgerView, accountID [20]byte) (*state.AccountRoot, error) {
	key := keylet.Account(accountID)
	data, err := view.Read(key)
	if err != nil || data == nil {
		return nil, fmt.Errorf("account not found")
	}
	return state.ParseAccountRoot(data)
}

// isFrozenIOU checks if an IOU is frozen for a given account.
// Checks global freeze on issuer + individual freeze on trust line.
// Reference: rippled View.cpp isFrozen(view, account, currency, issuer)
func isFrozenIOU(view tx.LedgerView, accountID, issuerID [20]byte, currency string) bool {
	// Check global freeze
	issuerAccount, err := readAccountRoot(view, issuerID)
	if err != nil || issuerAccount == nil {
		return false
	}
	if issuerAccount.Flags&state.LsfGlobalFreeze != 0 {
		return true
	}

	// Check individual freeze if not self
	if issuerID != accountID {
		return tx.IsTrustlineFrozen(view, accountID, issuerID, currency)
	}

	return false
}

// accountHoldsIOU returns the IOU balance for an account (ignoring freeze).
// Positive balance means the account holds tokens.
// Reference: rippled View.cpp accountHolds with fhIGNORE_FREEZE
func accountHoldsIOU(view tx.LedgerView, accountID, issuerID [20]byte, currency string) tx.Amount {
	issuerStr, err := state.EncodeAccountID(issuerID)
	if err != nil {
		return tx.NewIssuedAmount(0, 0, currency, "")
	}

	trustLineKey := keylet.Line(accountID, issuerID, currency)
	trustLineData, err := view.Read(trustLineKey)
	if err != nil || trustLineData == nil {
		return tx.NewIssuedAmount(0, 0, currency, issuerStr)
	}

	rs, err := state.ParseRippleState(trustLineData)
	if err != nil {
		return tx.NewIssuedAmount(0, 0, currency, issuerStr)
	}

	// Determine balance based on canonical ordering
	accountIsLow := state.CompareAccountIDsForLine(accountID, issuerID) < 0
	balance := rs.Balance
	if !accountIsLow {
		balance = balance.Negate()
	}

	if balance.Signum() <= 0 {
		return tx.NewIssuedAmount(0, 0, currency, issuerStr)
	}

	return state.NewIssuedAmountFromValue(balance.IOU().Mantissa(), balance.IOU().Exponent(), currency, issuerStr)
}

// accountHoldsMPT returns the MPT balance for an account (ignoring freeze/auth).
// Reference: rippled View.cpp accountHolds(view, account, MPTIssue, fhIGNORE_FREEZE, ahIGNORE_AUTH)
func accountHoldsMPT(view tx.LedgerView, issuanceKey keylet.Keylet, accountID [20]byte) int64 {
	tokenKey := keylet.MPToken(issuanceKey.Key, accountID)
	tokenData, err := view.Read(tokenKey)
	if err != nil || tokenData == nil {
		return 0
	}

	token, err := state.ParseMPToken(tokenData)
	if err != nil {
		return 0
	}

	return int64(token.MPTAmount)
}

// adjustOwnerCountViaView adjusts an account's OwnerCount by reading, modifying,
// and writing back the AccountRoot. adj can be positive or negative.
func adjustOwnerCountViaView(view tx.LedgerView, accountID [20]byte, adj int) {
	acctKey := keylet.Account(accountID)
	data, err := view.Read(acctKey)
	if err != nil || data == nil {
		return
	}

	acct, err := state.ParseAccountRoot(data)
	if err != nil {
		return
	}

	if adj > 0 {
		acct.OwnerCount += uint32(adj)
	} else if adj < 0 {
		decrement := uint32(-adj)
		if acct.OwnerCount >= decrement {
			acct.OwnerCount -= decrement
		} else {
			acct.OwnerCount = 0
		}
	}

	// Re-serialize and update
	address, err := addresscodec.EncodeAccountIDToClassicAddress(accountID[:])
	if err != nil {
		return
	}

	jsonObj := buildAccountRootJSON(acct, address)
	hexStr, err := binarycodec.Encode(jsonObj)
	if err != nil {
		return
	}

	updated, err := hex.DecodeString(hexStr)
	if err != nil {
		return
	}

	_ = view.Update(acctKey, updated)
}

// buildAccountRootJSON creates a JSON map for AccountRoot serialization.
// This replicates the pattern used elsewhere in the codebase.
func buildAccountRootJSON(acct *state.AccountRoot, address string) map[string]any {
	jsonObj := map[string]any{
		"LedgerEntryType": "AccountRoot",
		"Account":         address,
		"Balance":         fmt.Sprintf("%d", acct.Balance),
		"Sequence":        acct.Sequence,
		"OwnerCount":      acct.OwnerCount,
		"Flags":           acct.Flags,
	}

	if acct.TransferRate != 0 {
		jsonObj["TransferRate"] = acct.TransferRate
	}
	if acct.Domain != "" {
		jsonObj["Domain"] = strings.ToUpper(acct.Domain)
	}
	if acct.RegularKey != "" {
		jsonObj["RegularKey"] = acct.RegularKey
	}
	if acct.NFTokenMinter != "" {
		jsonObj["NFTokenMinter"] = acct.NFTokenMinter
	}
	if acct.MintedNFTokens != 0 {
		jsonObj["MintedNFTokens"] = acct.MintedNFTokens
	}
	if acct.BurnedNFTokens != 0 {
		jsonObj["BurnedNFTokens"] = acct.BurnedNFTokens
	}
	if acct.HasFirstNFTSeq {
		jsonObj["FirstNFTokenSequence"] = acct.FirstNFTokenSequence
	}
	if acct.TickSize != 0 {
		jsonObj["TickSize"] = acct.TickSize
	}
	if acct.TicketCount != 0 {
		jsonObj["TicketCount"] = acct.TicketCount
	}
	if acct.PreviousTxnID != [32]byte{} {
		jsonObj["PreviousTxnID"] = strings.ToUpper(hex.EncodeToString(acct.PreviousTxnID[:]))
	}
	if acct.PreviousTxnLgrSeq != 0 {
		jsonObj["PreviousTxnLgrSeq"] = acct.PreviousTxnLgrSeq
	}
	if acct.AccountTxnID != [32]byte{} {
		jsonObj["AccountTxnID"] = strings.ToUpper(hex.EncodeToString(acct.AccountTxnID[:]))
	}
	if acct.AMMID != [32]byte{} {
		jsonObj["AMMID"] = strings.ToUpper(hex.EncodeToString(acct.AMMID[:]))
	}
	if acct.WalletLocator != "" {
		jsonObj["WalletLocator"] = acct.WalletLocator
	}
	if acct.EmailHash != "" {
		jsonObj["EmailHash"] = acct.EmailHash
	}
	if acct.MessageKey != "" {
		jsonObj["MessageKey"] = acct.MessageKey
	}

	return jsonObj
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
) (uint64, uint64) {
	issuanceKey, err := mptIssuanceKeyFromHex(mptHexID)
	if err != nil {
		return originalAmount, originalAmount
	}

	issuanceData, err := view.Read(issuanceKey)
	if err != nil || issuanceData == nil {
		return originalAmount, originalAmount
	}

	issuance, err := state.ParseMPTokenIssuance(issuanceData)
	if err != nil {
		return originalAmount, originalAmount
	}

	issuerID := issuance.Issuer
	senderIsIssuer := issuerID == senderID
	receiverIsIssuer := issuerID == receiverID

	// Get current transfer rate
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
		// For MPT amounts (uint64), use big.Int:
		// divideRound = amount * 1_000_000_000 / rate (rounded up)
		bigAmount := new(big.Int).SetUint64(originalAmount)
		bigParity := new(big.Int).SetUint64(uint64(parityRate))
		bigRate := new(big.Int).SetUint64(uint64(effectiveRate))

		// amount * parityRate / rate, rounded up
		numerator := new(big.Int).Mul(bigAmount, bigParity)
		divided := new(big.Int).Div(numerator, bigRate)
		// Round up: if numerator % rate != 0, add 1
		remainder := new(big.Int).Mod(numerator, bigRate)
		if remainder.Sign() > 0 {
			divided.Add(divided, big.NewInt(1))
		}

		dividedResult := divided.Uint64()
		fee := originalAmount - dividedResult
		return originalAmount, originalAmount - fee
	}

	return originalAmount, originalAmount
}
