package payment

import (
	"github.com/LeJamon/goXRPLd/amendment"
	"github.com/LeJamon/goXRPLd/internal/ledger/state"
	tx "github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/internal/tx/permissioneddomain"
	"github.com/LeJamon/goXRPLd/keylet"
)

// checkTrustLineAuthorization checks if a trust line is authorized when the issuer requires auth.
// Reference: rippled DirectStep.cpp:417-430
//
// Parameters:
//   - view: ledger view to read account and trust line data
//   - issuerID: the issuer account ID
//   - holderID: the holder (non-issuer) account ID
//   - trustLine: the parsed RippleState (trust line) object
//
// Returns terNO_AUTH if:
//   - The issuer has lsfRequireAuth flag set, AND
//   - The trust line doesn't have the appropriate auth flag set, AND
//   - The trust line balance is zero (new relationship)
//
// Returns tesSUCCESS if authorized or if auth not required.
func checkTrustLineAuthorization(view tx.LedgerView, issuerID, holderID [20]byte, trustLine *state.RippleState) tx.Result {
	// Read the issuer's account to check for lsfRequireAuth
	issuerKey := keylet.Account(issuerID)
	issuerData, err := view.Read(issuerKey)
	if err != nil || issuerData == nil {
		return tx.TefINTERNAL
	}

	issuerAccount, err := state.ParseAccountRoot(issuerData)
	if err != nil {
		return tx.TefINTERNAL
	}

	// If issuer doesn't require auth, always authorized
	if (issuerAccount.Flags & state.LsfRequireAuth) == 0 {
		return tx.TesSUCCESS
	}

	// Issuer requires auth - check if the trust line is authorized
	// The auth flag depends on account ordering in the trust line
	// Reference: rippled DirectStep.cpp:420
	// auto const authField = (src_ > dst_) ? lsfHighAuth : lsfLowAuth;
	var authFlag uint32
	if state.CompareAccountIDs(issuerID, holderID) > 0 {
		// Issuer is HIGH, holder is LOW - need lsfHighAuth
		authFlag = state.LsfHighAuth
	} else {
		// Issuer is LOW, holder is HIGH - need lsfLowAuth
		authFlag = state.LsfLowAuth
	}

	// Check if trust line has the auth flag
	if (trustLine.Flags & authFlag) != 0 {
		return tx.TesSUCCESS
	}

	// Trust line is not authorized - only block if balance is zero
	// Reference: rippled DirectStep.cpp:424
	// !((*sleLine)[sfFlags] & authField) && (*sleLine)[sfBalance] == beast::zero
	if trustLine.Balance.IsZero() {
		return tx.TerNO_AUTH
	}

	// Non-zero balance means existing relationship, allow it
	return tx.TesSUCCESS
}

// applyIOUPayment applies an IOU (issued currency) or cross-currency payment.
// This is called for any payment with paths, SendMax, or non-native Amount.
// Reference: rippled/src/xrpld/app/tx/detail/Payment.cpp
func (p *Payment) applyIOUPayment(ctx *tx.ApplyContext) tx.Result {
	// Validate the amount
	if p.Amount.IsZero() {
		return tx.TemBAD_AMOUNT
	}
	if p.Amount.IsNegative() {
		return tx.TemBAD_AMOUNT
	}

	// Get account IDs
	senderAccountID, err := state.DecodeAccountID(ctx.Account.Account)
	if err != nil {
		return tx.TefINTERNAL
	}

	destAccountID, err := state.DecodeAccountID(p.Destination)
	if err != nil {
		return tx.TemDST_NEEDED
	}

	// For cross-currency payments where Amount is XRP, we always need the flow engine
	// (no issuer to decode, no direct IOU path possible)
	if p.Amount.IsNative() {
		// Cross-currency: Amount=XRP with SendMax=IOU or paths
		// Always requires the flow engine
		return p.applyRipplePayment(ctx, senderAccountID, destAccountID)
	}

	issuerAccountID, err := state.DecodeAccountID(p.Amount.Issuer)
	if err != nil {
		return tx.TemBAD_ISSUER
	}

	// Use the tx.Amount directly (no conversion needed)
	amount := p.Amount

	// Reference: rippled Payment.cpp:435-436:
	// bool const ripple = (hasPaths || sendMax || !dstAmount.native()) && !mptDirect;
	// Since we're in the IOU branch (past IsNative() check), !dstAmount.native() is always
	// true, so ALL IOU payments go through the flow engine (RippleCalc).
	requiresPathFinding := true

	// Determine payment type: is this a direct payment to/from issuer?
	senderIsIssuer := senderAccountID == issuerAccountID
	destIsIssuer := destAccountID == issuerAccountID

	// Check destination exists (needed for DepositAuth check and destination flags)
	destKey := keylet.Account(destAccountID)
	destExists, err := ctx.View.Exists(destKey)
	if err != nil {
		return tx.TefINTERNAL
	}
	if !destExists {
		return tx.TecNO_DST
	}

	// Get destination account to check flags
	destData, err := ctx.View.Read(destKey)
	if err != nil {
		return tx.TefINTERNAL
	}
	destAccount, err := state.ParseAccountRoot(destData)
	if err != nil {
		return tx.TefINTERNAL
	}

	// Check destination tag requirement
	if (destAccount.Flags&state.LsfRequireDestTag) != 0 && p.DestinationTag == nil {
		return tx.TecDST_TAG_NEEDED
	}

	// Validate credentials (preclaim)
	if result := p.validateCredentials(ctx); result != tx.TesSUCCESS {
		return result
	}

	// Check deposit authorization for IOU payments (including path-finding payments)
	// Reference: rippled Payment.cpp:429-464
	depositPreauth := ctx.Rules().Enabled(amendment.FeatureDepositPreauth)
	reqDepositAuth := (destAccount.Flags & state.LsfDepositAuth) != 0

	// Before DepositPreauth amendment: ALL ripple payments to accounts with
	// DepositAuth are blocked (including self-payments). This was a bug that
	// the DepositPreauth amendment fixed.
	// Reference: rippled Payment.cpp:440-441
	if !depositPreauth && reqDepositAuth {
		return tx.TecNO_PERMISSION
	}

	// With DepositPreauth amendment: self-payments and preauthorized accounts are allowed.
	if depositPreauth && reqDepositAuth {
		if result := p.verifyDepositPreauth(ctx, senderAccountID, destAccountID, destAccount); result != tx.TesSUCCESS {
			return result
		}
	}

	// For path-finding payments, use the Flow Engine (RippleCalculate)
	if requiresPathFinding {
		return p.applyIOUPaymentWithPaths(ctx, senderAccountID, destAccountID, issuerAccountID)
	}

	// Determine if partial payment is allowed
	flags := p.GetFlags()
	partialPayment := (flags & PaymentFlagPartialPayment) != 0

	// Handle three cases:
	// 1. Sender is issuer - creating new tokens
	// 2. Destination is issuer - redeeming tokens
	// 3. Neither - transfer between accounts via trust lines

	var result tx.Result
	var deliveredAmount tx.Amount

	if senderIsIssuer {
		// Sender is issuing their own currency to destination
		// Need trust line from destination to sender (issuer)
		result, deliveredAmount = p.applyIOUIssueWithDelivered(ctx, destAccount, senderAccountID, destAccountID, amount, partialPayment)
	} else if destIsIssuer {
		// Destination is the issuer - sender is redeeming tokens
		// Need trust line from sender to destination (issuer)
		result, deliveredAmount = p.applyIOURedeemWithDelivered(ctx, destAccount, senderAccountID, destAccountID, amount, partialPayment)
	} else {
		// Neither is issuer - transfer between two non-issuer accounts
		// This requires trust lines from both parties to the issuer
		result, deliveredAmount = p.applyIOUTransferWithDelivered(ctx, destAccount, senderAccountID, destAccountID, issuerAccountID, amount, partialPayment)
	}

	// DeliverMin enforcement for partial payments
	// Reference: rippled Payment.cpp:496-500
	// If tfPartialPayment is set and DeliverMin is specified, check that delivered >= DeliverMin
	if result == tx.TesSUCCESS && p.DeliverMin != nil {
		flags := p.GetFlags()
		if (flags & PaymentFlagPartialPayment) != 0 {
			if deliveredAmount.Compare(*p.DeliverMin) < 0 {
				return tx.TecPATH_PARTIAL
			}
		}
	}

	return result
}

// applyRipplePayment handles cross-currency payments where Amount is XRP but
// the payment goes through the order book (has SendMax or paths).
// Reference: rippled Payment.cpp doApply() when ripple=true
func (p *Payment) applyRipplePayment(ctx *tx.ApplyContext, senderID, destID [20]byte) tx.Result {
	// Check destination exists
	destKey := keylet.Account(destID)
	destData, err := ctx.View.Read(destKey)
	if err != nil || destData == nil {
		return tx.TecNO_DST
	}
	destAccount, err := state.ParseAccountRoot(destData)
	if err != nil {
		return tx.TefINTERNAL
	}

	// Check destination tag requirement
	if (destAccount.Flags&state.LsfRequireDestTag) != 0 && p.DestinationTag == nil {
		return tx.TecDST_TAG_NEEDED
	}

	// Validate credentials
	if result := p.validateCredentials(ctx); result != tx.TesSUCCESS {
		return result
	}

	// Check deposit authorization
	if result := p.verifyDepositPreauth(ctx, senderID, destID, destAccount); result != tx.TesSUCCESS {
		return result
	}

	// Use the flow engine (issuerID is unused for XRP amount, pass zero)
	var zeroID [20]byte
	return p.applyIOUPaymentWithPaths(ctx, senderID, destID, zeroID)
}

// applyIOUPaymentWithPaths handles IOU payments that require path finding using the Flow Engine.
// This is the main entry point for cross-currency payments and payments with explicit paths.
// Reference: rippled/src/xrpld/app/paths/RippleCalc.cpp
func (p *Payment) applyIOUPaymentWithPaths(ctx *tx.ApplyContext, senderID, destID, issuerID [20]byte) tx.Result {
	// Determine payment flags
	flags := p.GetFlags()
	partialPayment := (flags & PaymentFlagPartialPayment) != 0
	limitQuality := (flags & PaymentFlagLimitQuality) != 0
	noDirectRipple := (flags & PaymentFlagNoDirectRipple) != 0

	// addDefaultPath is true unless tfNoRippleDirect is set
	addDefaultPath := !noDirectRipple

	// Execute RippleCalculate
	rules := ctx.Rules()
	rcOpts := []RippleCalculateOption{
		WithAmendments(
			ctx.Config.ParentCloseTime,
			rules.Enabled(amendment.FeatureFixReducedOffersV1),
			rules.Enabled(amendment.FeatureFixReducedOffersV2),
			rules.Enabled(amendment.FeatureFixRmSmallIncreasedQOffers),
			rules.Enabled(amendment.FeatureFlowSortStrands),
		),
		WithAMMAmendments(
			rules.Enabled(amendment.FeatureFixAMMv1_1),
			rules.Enabled(amendment.FeatureFixAMMv1_2),
			rules.Enabled(amendment.FeatureFixAMMOverflowOffer),
		),
	}
	// Thread domain ID to the flow engine for permissioned domain payments.
	if p.DomainID != nil {
		domainID, err := permissioneddomain.ParseDomainID(*p.DomainID)
		if err == nil {
			rcOpts = append(rcOpts, WithDomainID(&domainID))
		}
	}
	_, actualOut, _, sandbox, result := RippleCalculate(
		ctx.View,
		senderID,
		destID,
		p.Amount,
		p.SendMax,
		p.Paths,
		addDefaultPath,
		partialPayment,
		limitQuality,
		ctx.TxHash,
		ctx.Config.LedgerSequence,
		rcOpts...,
	)

	// Because of its overhead, if RippleCalc fails with a retry code (ter*),
	// claim a fee instead. Reference: rippled Payment.cpp:509-510
	if result.IsTer() {
		result = tx.TecPATH_DRY
	}

	// Handle result
	if result != tx.TesSUCCESS && result != tx.TecPATH_PARTIAL {
		return result
	}

	// Apply sandbox changes back to the ledger view (through ApplyStateTable for tracking)
	if sandbox != nil {
		if err := sandbox.ApplyToView(ctx.View); err != nil {
			return tx.TefINTERNAL
		}
	}

	// Re-read the sender account from the view so the engine's post-Apply
	// write-back includes balance changes made by the flow engine.
	// Without this, ctx.Account has stale data that the engine would overwrite.
	{
		updatedData, err := ctx.View.Read(keylet.Account(senderID))
		if err == nil && updatedData != nil {
			if updated, parseErr := state.ParseAccountRoot(updatedData); parseErr == nil {
				*ctx.Account = *updated
			}
		}
	}

	// Check if partial payment delivered enough (DeliverMin)
	if partialPayment && p.DeliverMin != nil {
		deliverMin := ToEitherAmount(*p.DeliverMin)
		if actualOut.Compare(deliverMin) < 0 {
			return tx.TecPATH_PARTIAL
		}
	}

	// Record delivered amount in metadata
	deliveredAmt := FromEitherAmount(actualOut)
	ctx.Metadata.DeliveredAmount = &deliveredAmt

	// Offer deletions and trust line modifications tracked automatically by ApplyStateTable

	return result
}

// applyIOUIssueWithDelivered wraps applyIOUIssue to return the delivered amount
// If partialPayment is true and the full amount cannot be issued, it will issue
// as much as possible up to the destination's trust limit.
func (p *Payment) applyIOUIssueWithDelivered(ctx *tx.ApplyContext, dest *state.AccountRoot, senderID, destID [20]byte, amount tx.Amount, partialPayment bool) (tx.Result, tx.Amount) {
	return p.applyIOUIssuePartial(ctx, dest, senderID, destID, amount, partialPayment)
}

// applyIOURedeemWithDelivered wraps applyIOURedeem to return the delivered amount
// If partialPayment is true and the full amount cannot be redeemed, it will redeem
// as much as possible based on sender's balance.
func (p *Payment) applyIOURedeemWithDelivered(ctx *tx.ApplyContext, dest *state.AccountRoot, senderID, destID [20]byte, amount tx.Amount, partialPayment bool) (tx.Result, tx.Amount) {
	return p.applyIOURedeemPartial(ctx, dest, senderID, destID, amount, partialPayment)
}

// applyIOUTransferWithDelivered wraps applyIOUTransfer to return the delivered amount
// If partialPayment is true and the full amount cannot be transferred, it will transfer
// as much as possible based on sender's balance and destination's trust limit.
func (p *Payment) applyIOUTransferWithDelivered(ctx *tx.ApplyContext, dest *state.AccountRoot, senderID, destID, issuerID [20]byte, amount tx.Amount, partialPayment bool) (tx.Result, tx.Amount) {
	return p.applyIOUTransferPartial(ctx, dest, senderID, destID, issuerID, amount, partialPayment)
}

// ============================================================================
// Partial Payment Implementations
// Reference: rippled Flow.cpp, RippleCalc.cpp
// ============================================================================

// applyIOUIssuePartial handles issuing currency with partial payment support
func (p *Payment) applyIOUIssuePartial(ctx *tx.ApplyContext, dest *state.AccountRoot, senderID, destID [20]byte, amount tx.Amount, partialPayment bool) (tx.Result, tx.Amount) {
	// Look up the trust line between destination and issuer (sender)
	trustLineKey := keylet.Line(destID, senderID, amount.Currency)

	trustLineExists, err := ctx.View.Exists(trustLineKey)
	if err != nil {
		return tx.TefINTERNAL, tx.Amount{}
	}

	if !trustLineExists {
		return tx.TerNO_LINE, tx.Amount{}
	}

	// Read and parse the trust line
	trustLineData, err := ctx.View.Read(trustLineKey)
	if err != nil {
		return tx.TefINTERNAL, tx.Amount{}
	}

	rippleState, err := state.ParseRippleState(trustLineData)
	if err != nil {
		return tx.TefINTERNAL, tx.Amount{}
	}

	// Check trust line authorization
	if result := checkTrustLineAuthorization(ctx.View, senderID, destID, rippleState); result != tx.TesSUCCESS {
		return result, tx.Amount{}
	}

	// Determine which side is low/high account
	destIsLow := state.CompareAccountIDsForLine(destID, senderID) < 0

	// Get the current balance and trust limit
	var currentBalance, trustLimit tx.Amount
	if destIsLow {
		currentBalance = rippleState.Balance
		trustLimit = rippleState.LowLimit
	} else {
		currentBalance = rippleState.Balance.Negate()
		trustLimit = rippleState.HighLimit
	}

	// Calculate maximum we can issue based on trust limit
	var maxDeliverable tx.Amount
	if trustLimit.IsZero() {
		// No limit set, can issue any amount
		maxDeliverable = amount
	} else {
		// Calculate room available: limit - current balance
		room, _ := trustLimit.Sub(currentBalance)
		if room.IsNegative() || room.IsZero() {
			if partialPayment {
				return tx.TesSUCCESS, tx.Amount{} // Nothing to deliver, but partial is OK
			}
			return tx.TecPATH_PARTIAL, tx.Amount{}
		}
		if room.Compare(amount) < 0 {
			maxDeliverable = room
		} else {
			maxDeliverable = amount
		}
	}

	// If we can't deliver the full amount and partial payment is not allowed, fail
	if maxDeliverable.Compare(amount) < 0 && !partialPayment {
		return tx.TecPATH_PARTIAL, tx.Amount{}
	}

	// If nothing to deliver
	if maxDeliverable.IsZero() {
		if partialPayment {
			return tx.TesSUCCESS, tx.Amount{}
		}
		return tx.TecPATH_PARTIAL, tx.Amount{}
	}

	// Calculate new balance
	var newBalance tx.Amount
	if destIsLow {
		newBalance, _ = rippleState.Balance.Add(maxDeliverable)
	} else {
		newBalance, _ = rippleState.Balance.Sub(maxDeliverable)
	}

	// Ensure the new balance has the correct currency and issuer
	newBalance.Currency = amount.Currency
	newBalance.Issuer = amount.Issuer

	// Update the trust line
	rippleState.Balance = newBalance
	rippleState.PreviousTxnID = ctx.TxHash
	rippleState.PreviousTxnLgrSeq = ctx.Config.LedgerSequence

	// Check for trust line cleanup (default state → delete)
	// Reference: rippled View.cpp rippleCreditIOU() lines 1692-1745
	if result := trustLineCleanup(ctx, dest, senderID, destID, trustLineKey, rippleState); result != tx.TesSUCCESS {
		return result, tx.Amount{}
	}

	ctx.Metadata.DeliveredAmount = &maxDeliverable

	return tx.TesSUCCESS, maxDeliverable
}

// applyIOURedeemPartial handles redeeming currency with partial payment support
func (p *Payment) applyIOURedeemPartial(ctx *tx.ApplyContext, dest *state.AccountRoot, senderID, destID [20]byte, amount tx.Amount, partialPayment bool) (tx.Result, tx.Amount) {
	// Look up the trust line between sender and issuer (destination)
	trustLineKey := keylet.Line(senderID, destID, amount.Currency)

	trustLineExists, err := ctx.View.Exists(trustLineKey)
	if err != nil {
		return tx.TefINTERNAL, tx.Amount{}
	}

	if !trustLineExists {
		return tx.TerNO_LINE, tx.Amount{}
	}

	// Read and parse the trust line
	trustLineData, err := ctx.View.Read(trustLineKey)
	if err != nil {
		return tx.TefINTERNAL, tx.Amount{}
	}

	rippleState, err := state.ParseRippleState(trustLineData)
	if err != nil {
		return tx.TefINTERNAL, tx.Amount{}
	}

	// Determine which side is low/high account
	senderIsLow := state.CompareAccountIDsForLine(senderID, destID) < 0

	// Get sender's current balance
	var senderBalance tx.Amount
	if senderIsLow {
		senderBalance = rippleState.Balance
	} else {
		senderBalance = rippleState.Balance.Negate()
	}

	// Calculate maximum we can redeem based on sender's balance
	var maxDeliverable tx.Amount
	if senderBalance.Compare(amount) < 0 {
		if senderBalance.IsNegative() || senderBalance.IsZero() {
			if partialPayment {
				return tx.TesSUCCESS, tx.Amount{}
			}
			return tx.TecPATH_PARTIAL, tx.Amount{}
		}
		maxDeliverable = senderBalance
	} else {
		maxDeliverable = amount
	}

	// If we can't deliver the full amount and partial payment is not allowed, fail
	if maxDeliverable.Compare(amount) < 0 && !partialPayment {
		return tx.TecPATH_PARTIAL, tx.Amount{}
	}

	// If nothing to deliver
	if maxDeliverable.IsZero() {
		if partialPayment {
			return tx.TesSUCCESS, tx.Amount{}
		}
		return tx.TecPATH_PARTIAL, tx.Amount{}
	}

	// Update balance
	var newBalance tx.Amount
	if senderIsLow {
		newBalance, _ = rippleState.Balance.Sub(maxDeliverable)
	} else {
		newBalance, _ = rippleState.Balance.Add(maxDeliverable)
	}

	newBalance.Currency = amount.Currency
	newBalance.Issuer = amount.Issuer
	rippleState.Balance = newBalance
	rippleState.PreviousTxnID = ctx.TxHash
	rippleState.PreviousTxnLgrSeq = ctx.Config.LedgerSequence

	// Check for trust line cleanup (default state → delete)
	// Reference: rippled View.cpp rippleCreditIOU() lines 1692-1745
	if result := trustLineCleanup(ctx, dest, senderID, destID, trustLineKey, rippleState); result != tx.TesSUCCESS {
		return result, tx.Amount{}
	}

	ctx.Metadata.DeliveredAmount = &maxDeliverable

	return tx.TesSUCCESS, maxDeliverable
}

// applyIOUTransferPartial handles transfers between non-issuer accounts with partial payment support
func (p *Payment) applyIOUTransferPartial(ctx *tx.ApplyContext, dest *state.AccountRoot, senderID, destID, issuerID [20]byte, amount tx.Amount, partialPayment bool) (tx.Result, tx.Amount) {
	// Check if issuer has GlobalFreeze enabled
	issuerKey := keylet.Account(issuerID)
	issuerData, err := ctx.View.Read(issuerKey)
	if err != nil {
		return tx.TefINTERNAL, tx.Amount{}
	}
	issuerAccount, err := state.ParseAccountRoot(issuerData)
	if err != nil {
		return tx.TefINTERNAL, tx.Amount{}
	}
	if (issuerAccount.Flags & state.LsfGlobalFreeze) != 0 {
		return tx.TerNO_LINE, tx.Amount{}
	}

	// Get transfer rate from issuer
	transferRate := GetTransferRate(ctx.View, issuerID)

	// Get sender's trust line to issuer
	senderTrustLineKey := keylet.Line(senderID, issuerID, amount.Currency)
	senderTrustExists, err := ctx.View.Exists(senderTrustLineKey)
	if err != nil {
		return tx.TefINTERNAL, tx.Amount{}
	}
	if !senderTrustExists {
		return tx.TerNO_LINE, tx.Amount{}
	}

	// Get destination's trust line to issuer
	destTrustLineKey := keylet.Line(destID, issuerID, amount.Currency)
	destTrustExists, err := ctx.View.Exists(destTrustLineKey)
	if err != nil {
		return tx.TefINTERNAL, tx.Amount{}
	}
	if !destTrustExists {
		return tx.TerNO_LINE, tx.Amount{}
	}

	// Read sender's trust line
	senderTrustData, err := ctx.View.Read(senderTrustLineKey)
	if err != nil {
		return tx.TefINTERNAL, tx.Amount{}
	}
	senderRippleState, err := state.ParseRippleState(senderTrustData)
	if err != nil {
		return tx.TefINTERNAL, tx.Amount{}
	}

	// Check trust line authorization for sender
	if result := checkTrustLineAuthorization(ctx.View, issuerID, senderID, senderRippleState); result != tx.TesSUCCESS {
		return result, tx.Amount{}
	}

	// Check if sender's trust line is frozen
	senderIsLowInTrustLine := state.CompareAccountIDsForLine(senderID, issuerID) < 0
	if senderIsLowInTrustLine {
		if (senderRippleState.Flags & state.LsfHighFreeze) != 0 {
			return tx.TerNO_LINE, tx.Amount{}
		}
	} else {
		if (senderRippleState.Flags & state.LsfLowFreeze) != 0 {
			return tx.TerNO_LINE, tx.Amount{}
		}
	}

	// Read destination's trust line
	destTrustData, err := ctx.View.Read(destTrustLineKey)
	if err != nil {
		return tx.TefINTERNAL, tx.Amount{}
	}
	destRippleState, err := state.ParseRippleState(destTrustData)
	if err != nil {
		return tx.TefINTERNAL, tx.Amount{}
	}

	// Check trust line authorization for destination
	if result := checkTrustLineAuthorization(ctx.View, issuerID, destID, destRippleState); result != tx.TesSUCCESS {
		return result, tx.Amount{}
	}

	// Check if destination's trust line is frozen
	destIsLowInTrustLine := state.CompareAccountIDsForLine(destID, issuerID) < 0
	if destIsLowInTrustLine {
		if (destRippleState.Flags & state.LsfLowFreeze) != 0 {
			return tx.TerNO_LINE, tx.Amount{}
		}
	} else {
		if (destRippleState.Flags & state.LsfHighFreeze) != 0 {
			return tx.TerNO_LINE, tx.Amount{}
		}
	}

	// Calculate sender's balance with issuer
	senderIsLowWithIssuer := state.CompareAccountIDsForLine(senderID, issuerID) < 0
	var senderBalance tx.Amount
	if senderIsLowWithIssuer {
		senderBalance = senderRippleState.Balance
	} else {
		senderBalance = senderRippleState.Balance.Negate()
	}

	// Calculate destination's balance and trust limit
	destIsLowWithIssuer := state.CompareAccountIDsForLine(destID, issuerID) < 0
	var destBalance, destLimit tx.Amount
	if destIsLowWithIssuer {
		destBalance = destRippleState.Balance
		destLimit = destRippleState.LowLimit
	} else {
		destBalance = destRippleState.Balance.Negate()
		destLimit = destRippleState.HighLimit
	}

	// Calculate maximum deliverable based on:
	// 1. Sender's available balance (accounting for transfer fee)
	// 2. Destination's trust limit room

	// Max based on sender's balance: senderBalance * (QualityOne / transferRate)
	// This accounts for the transfer fee in reverse
	maxFromSender := senderBalance.MulRatio(QualityOne, transferRate, false)

	// Max based on destination's trust limit
	var maxFromDestLimit tx.Amount
	if destLimit.IsZero() {
		// No limit - use a very large value (effectively unlimited)
		maxFromDestLimit = amount
	} else {
		room, _ := destLimit.Sub(destBalance)
		if room.IsNegative() {
			room = tx.NewIssuedAmountFromFloat64(0, amount.Currency, amount.Issuer)
		}
		maxFromDestLimit = room
	}

	// Actual max is minimum of the two constraints
	var maxDeliverable tx.Amount
	if maxFromSender.Compare(maxFromDestLimit) < 0 {
		maxDeliverable = maxFromSender
	} else {
		maxDeliverable = maxFromDestLimit
	}

	// Cap at requested amount
	if maxDeliverable.Compare(amount) > 0 {
		maxDeliverable = amount
	}

	// If we can't deliver anything
	if maxDeliverable.IsZero() || maxDeliverable.IsNegative() {
		if partialPayment {
			return tx.TesSUCCESS, tx.Amount{}
		}
		return tx.TecPATH_PARTIAL, tx.Amount{}
	}

	// If we can't deliver the full amount and partial payment is not allowed, fail
	if maxDeliverable.Compare(amount) < 0 && !partialPayment {
		return tx.TecPATH_PARTIAL, tx.Amount{}
	}

	// Calculate gross amount sender needs to spend (includes transfer fee)
	grossAmount := maxDeliverable.MulRatio(transferRate, QualityOne, true)

	// Update sender's trust line
	var newSenderRippleBalance tx.Amount
	if senderIsLowWithIssuer {
		newSenderRippleBalance, _ = senderRippleState.Balance.Sub(grossAmount)
	} else {
		newSenderRippleBalance, _ = senderRippleState.Balance.Add(grossAmount)
	}
	newSenderRippleBalance.Currency = amount.Currency
	newSenderRippleBalance.Issuer = amount.Issuer
	senderRippleState.Balance = newSenderRippleBalance
	senderRippleState.PreviousTxnID = ctx.TxHash
	senderRippleState.PreviousTxnLgrSeq = ctx.Config.LedgerSequence

	// Update destination's trust line
	var newDestRippleBalance tx.Amount
	if destIsLowWithIssuer {
		newDestRippleBalance, _ = destRippleState.Balance.Add(maxDeliverable)
	} else {
		newDestRippleBalance, _ = destRippleState.Balance.Sub(maxDeliverable)
	}
	newDestRippleBalance.Currency = amount.Currency
	newDestRippleBalance.Issuer = amount.Issuer
	destRippleState.Balance = newDestRippleBalance
	destRippleState.PreviousTxnID = ctx.TxHash
	destRippleState.PreviousTxnLgrSeq = ctx.Config.LedgerSequence

	// Serialize and update sender's trust line
	updatedSenderTrust, err := state.SerializeRippleState(senderRippleState)
	if err != nil {
		return tx.TefINTERNAL, tx.Amount{}
	}
	if err := ctx.View.Update(senderTrustLineKey, updatedSenderTrust); err != nil {
		return tx.TefINTERNAL, tx.Amount{}
	}

	// Serialize and update destination's trust line
	updatedDestTrust, err := state.SerializeRippleState(destRippleState)
	if err != nil {
		return tx.TefINTERNAL, tx.Amount{}
	}
	if err := ctx.View.Update(destTrustLineKey, updatedDestTrust); err != nil {
		return tx.TefINTERNAL, tx.Amount{}
	}

	ctx.Metadata.DeliveredAmount = &maxDeliverable

	return tx.TesSUCCESS, maxDeliverable
}

// trustLineCleanup checks if a trust line is in default state after a balance modification
// and deletes it if so, adjusting OwnerCount for both accounts.
// This matches rippled's rippleCreditIOU() logic in View.cpp lines 1692-1745.
//
// Parameters:
//   - ctx: apply context (ctx.Account = transaction sender, identified by senderID)
//   - dest: the other account's AccountRoot (identified by destID)
//   - senderID, destID: the two account IDs on the trust line
//   - tlKey: the keylet for the trust line
//   - rs: the already-modified RippleState (balance updated, not yet serialized)
//
// On success, either updates or erases the trust line via ctx.View. Returns TesSUCCESS.
func trustLineCleanup(ctx *tx.ApplyContext, dest *state.AccountRoot, senderID, destID [20]byte, tlKey keylet.Keylet, rs *state.RippleState) tx.Result {
	senderIsLow := state.CompareAccountIDsForLine(senderID, destID) < 0

	// Get both accounts' DefaultRipple flags
	var lowDefRipple, highDefRipple bool
	if senderIsLow {
		lowDefRipple = (ctx.Account.Flags & state.LsfDefaultRipple) != 0
		highDefRipple = (dest.Flags & state.LsfDefaultRipple) != 0
	} else {
		lowDefRipple = (dest.Flags & state.LsfDefaultRipple) != 0
		highDefRipple = (ctx.Account.Flags & state.LsfDefaultRipple) != 0
	}

	bLowReserveSet := rs.LowQualityIn != 0 || rs.LowQualityOut != 0 ||
		((rs.Flags&state.LsfLowNoRipple) == 0) != lowDefRipple ||
		(rs.Flags&state.LsfLowFreeze) != 0 || !rs.LowLimit.IsZero() ||
		rs.Balance.Signum() > 0

	bHighReserveSet := rs.HighQualityIn != 0 || rs.HighQualityOut != 0 ||
		((rs.Flags&state.LsfHighNoRipple) == 0) != highDefRipple ||
		(rs.Flags&state.LsfHighFreeze) != 0 || !rs.HighLimit.IsZero() ||
		rs.Balance.Signum() < 0

	bLowReserved := (rs.Flags & state.LsfLowReserve) != 0
	bHighReserved := (rs.Flags & state.LsfHighReserve) != 0

	bDefault := !bLowReserveSet && !bHighReserveSet

	if bDefault && rs.Balance.IsZero() {
		// Remove from both owner directories before erasing
		// Reference: rippled trustDelete() in View.cpp
		var lowID, highID [20]byte
		if senderIsLow {
			lowID = senderID
			highID = destID
		} else {
			lowID = destID
			highID = senderID
		}
		lowDirKey := keylet.OwnerDir(lowID)
		state.DirRemove(ctx.View, lowDirKey, rs.LowNode, tlKey.Key, false)
		highDirKey := keylet.OwnerDir(highID)
		state.DirRemove(ctx.View, highDirKey, rs.HighNode, tlKey.Key, false)

		// Delete the trust line
		if err := ctx.View.Erase(tlKey); err != nil {
			return tx.TefINTERNAL
		}

		// Decrement OwnerCount for both sides that had reserve set
		if bLowReserved {
			if senderIsLow {
				if ctx.Account.OwnerCount > 0 {
					ctx.Account.OwnerCount--
				}
			} else {
				if dest.OwnerCount > 0 {
					dest.OwnerCount--
				}
			}
		}
		if bHighReserved {
			if !senderIsLow {
				if ctx.Account.OwnerCount > 0 {
					ctx.Account.OwnerCount--
				}
			} else {
				if dest.OwnerCount > 0 {
					dest.OwnerCount--
				}
			}
		}

		// Write dest account back if its OwnerCount changed
		destChanged := (bLowReserved && !senderIsLow) || (bHighReserved && senderIsLow)
		if destChanged {
			destKey := keylet.Account(destID)
			destUpdatedData, serErr := state.SerializeAccountRoot(dest)
			if serErr != nil {
				return tx.TefINTERNAL
			}
			if err := ctx.View.Update(destKey, destUpdatedData); err != nil {
				return tx.TefINTERNAL
			}
		}
	} else {
		// Adjust reserve flags
		if bLowReserveSet && !bLowReserved {
			rs.Flags |= state.LsfLowReserve
		} else if !bLowReserveSet && bLowReserved {
			rs.Flags &^= state.LsfLowReserve
		}
		if bHighReserveSet && !bHighReserved {
			rs.Flags |= state.LsfHighReserve
		} else if !bHighReserveSet && bHighReserved {
			rs.Flags &^= state.LsfHighReserve
		}

		// Serialize and update
		updatedData, serErr := state.SerializeRippleState(rs)
		if serErr != nil {
			return tx.TefINTERNAL
		}
		if err := ctx.View.Update(tlKey, updatedData); err != nil {
			return tx.TefINTERNAL
		}
	}

	return tx.TesSUCCESS
}
