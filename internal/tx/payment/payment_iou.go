package payment

import (
	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	tx "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/credential"
	"github.com/LeJamon/go-xrpl/internal/tx/permissioneddomain"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
)

// checkIOUDestPreamble runs the Apply-phase destination preamble for IOU and
// cross-currency (ripple) payments: deposit-authorization / deposit-preauth.
// The destination-tag and credential-validity checks run earlier, in Preclaim.
// Reference: rippled Payment.cpp:429-465 (ripple == true).
func (p *Payment) checkIOUDestPreamble(ctx *tx.ApplyContext, senderID, destID [20]byte, destAccount *state.AccountRoot) ter.Result {
	depositAuth := ctx.Rules().Enabled(amendment.FeatureDepositAuth)
	depositPreauth := ctx.Rules().Enabled(amendment.FeatureDepositPreauth)
	reqDepositAuth := (destAccount.Flags&state.LsfDepositAuth) != 0 && depositAuth

	// Before DepositPreauth amendment: ALL ripple payments to accounts with
	// DepositAuth are blocked (including self-payments). This was a bug that
	// the DepositPreauth amendment fixed.
	// Reference: rippled Payment.cpp:440-441
	if !depositPreauth && reqDepositAuth {
		return ter.TecNO_PERMISSION
	}

	// With DepositPreauth amendment: self-payments and preauthorized accounts
	// are allowed. The check runs regardless of the destination's flags so
	// that expired credentials are removed (tecEXPIRED).
	if depositPreauth && depositAuth {
		if result := credential.VerifyDepositPreauth(ctx, p.CredentialIDs, senderID, destID, destAccount); result != ter.TesSUCCESS {
			return result
		}
	}

	return ter.TesSUCCESS
}

// applyIOUPayment applies an IOU (issued currency) or cross-currency payment.
// This is called for any payment with paths, SendMax, or non-native Amount.
// Reference: rippled/src/xrpld/app/tx/detail/Payment.cpp
func (p *Payment) applyIOUPayment(ctx *tx.ApplyContext) ter.Result {
	// Validate the amount
	if p.Amount.IsZero() {
		return ter.TemBAD_AMOUNT
	}
	if p.Amount.IsNegative() {
		return ter.TemBAD_AMOUNT
	}

	// Get account IDs
	senderAccountID, err := state.DecodeAccountID(ctx.Account.Account)
	if err != nil {
		return ter.TefINTERNAL
	}

	destAccountID, err := state.DecodeAccountID(p.Destination)
	if err != nil {
		return ter.TemDST_NEEDED
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
		return ter.TemBAD_ISSUER
	}

	// Check destination exists (needed for DepositAuth check and destination flags)
	destKey := keylet.Account(destAccountID)
	destExists, err := ctx.View.Exists(destKey)
	if err != nil {
		return ter.TefINTERNAL
	}
	if !destExists {
		return ter.TecNO_DST
	}

	// Get destination account to check flags
	destData, err := ctx.View.Read(destKey)
	if err != nil {
		return ter.TefINTERNAL
	}
	destAccount, err := state.ParseAccountRoot(destData)
	if err != nil {
		return ter.TefINTERNAL
	}

	if result := p.checkIOUDestPreamble(ctx, senderAccountID, destAccountID, destAccount); result != ter.TesSUCCESS {
		return result
	}

	// Mark the existing destination AccountRoot as intentionally touched, even
	// when the delivered IOU never changes one of its own fields (the value
	// lands on a RippleState trustline). rippled does this unconditionally for
	// an existing destination of a ripple payment via view().update(sleDst)
	// (Payment.cpp:420-426). The marking promotes the node to a modify so that,
	// if owner-threading later rewrites its PreviousTxnID (e.g. it owns a
	// trustline created or deleted by this payment, as when the destination is
	// the issuer being redeemed to), the node is emitted as a ModifiedNode with
	// FinalFields instead of a bare threaded node. When nothing rewrites it the
	// no-op modify is dropped (bytes unchanged), matching rippled's
	// `*curNode == *origNode` skip.
	if err := ctx.View.Update(destKey, destData); err != nil {
		return ter.TefINTERNAL
	}

	return p.applyIOUPaymentWithPaths(ctx, senderAccountID, destAccountID, issuerAccountID)
}

// applyRipplePayment handles cross-currency payments where Amount is XRP but
// the payment goes through the order book (has SendMax or paths).
// Reference: rippled Payment.cpp doApply() when ripple=true
func (p *Payment) applyRipplePayment(ctx *tx.ApplyContext, senderID, destID [20]byte) ter.Result {
	// This path only handles a native (XRP) delivered amount, so a missing
	// destination can be funded by the payment. The normal engine flow gates
	// these cases in Payment.Preclaim; this branch is also the authoritative
	// path for batch inner transactions, which apply directly and bypass the
	// engine's Preclaimer dispatch.
	destKey := keylet.Account(destID)
	destExists, err := ctx.View.Exists(destKey)
	if err != nil {
		return ter.TefINTERNAL
	}

	if !destExists {
		// Destination account does not exist. Mirror rippled's preclaim/doApply
		// branching for a native delivered amount.
		// Reference: rippled Payment.cpp:296-332 (preclaim) + :407-419 (doApply).
		flags := p.GetFlags()
		partialPayment := (flags & PaymentFlagPartialPayment) != 0

		// You cannot fund an account with a partial payment on an open ledger.
		// Reference: rippled Payment.cpp:308-318
		if ctx.Config.OpenLedger && partialPayment {
			return ter.TelNO_DST_PARTIAL
		}

		// Insufficient payment to meet the account reserve (accountReserve(0)).
		// Reference: rippled Payment.cpp:319-331
		amountDrops := uint64(p.Amount.Drops())
		if amountDrops < ctx.AccountReserve(0) {
			return ter.TecNO_DST_INSUF_XRP
		}

		// Create the destination account before running the flow engine, which
		// credits the delivered XRP to it. With featureDeletableAccounts the new
		// account's sequence is the current ledger sequence, otherwise 1.
		// Reference: rippled Payment.cpp:407-419
		var accountSequence uint32
		if ctx.Rules().DeletableAccountsEnabled() {
			accountSequence = ctx.Config.LedgerSequence
		} else {
			accountSequence = 1
		}
		newAccount := &state.AccountRoot{
			Account:           p.Destination,
			Balance:           0,
			Sequence:          accountSequence,
			Flags:             0,
			PreviousTxnID:     ctx.TxHash,
			PreviousTxnLgrSeq: ctx.Config.LedgerSequence,
		}
		newAccountData, serErr := state.SerializeAccountRoot(newAccount)
		if serErr != nil {
			return ter.TefINTERNAL
		}
		if insErr := ctx.View.Insert(destKey, newAccountData); insErr != nil {
			return ter.TefINTERNAL
		}

		// A freshly created account carries no flags, so destination-tag and
		// deposit-authorization checks do not apply.
		var zeroID [20]byte
		return p.applyIOUPaymentWithPaths(ctx, senderID, destID, zeroID)
	}

	destData, err := ctx.View.Read(destKey)
	if err != nil || destData == nil {
		return ter.TefINTERNAL
	}
	destAccount, err := state.ParseAccountRoot(destData)
	if err != nil {
		return ter.TefINTERNAL
	}

	if result := p.checkIOUDestPreamble(ctx, senderID, destID, destAccount); result != ter.TesSUCCESS {
		return result
	}

	// Use the flow engine (issuerID is unused for XRP amount, pass zero)
	var zeroID [20]byte
	return p.applyIOUPaymentWithPaths(ctx, senderID, destID, zeroID)
}

// applyIOUPaymentWithPaths handles IOU payments that require path finding using the Flow Engine.
// This is the main entry point for cross-currency payments and payments with explicit paths.
// Reference: rippled/src/xrpld/app/paths/RippleCalc.cpp
func (p *Payment) applyIOUPaymentWithPaths(ctx *tx.ApplyContext, senderID, destID, issuerID [20]byte) ter.Result {
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
		WithFix1781(rules.Enabled(amendment.FeatureFix1781)),
		WithOpenLedger(ctx.Config.IsViewOpen()),
	}
	// Thread domain ID to the flow engine for permissioned domain payments.
	if p.DomainID != nil {
		domainID, err := permissioneddomain.ParseDomainID(*p.DomainID)
		if err == nil {
			rcOpts = append(rcOpts, WithDomainID(&domainID))
		}
	}
	// Derive the maximum source amount. When SendMax is absent, rippled does
	// not leave it unset: getMaxSourceAmount() defaults it to the delivered
	// Amount re-issued by the source account (for an IOU delivery). This
	// becomes the flow engine's input bound, so the destination receives
	// Amount/transferRate rather than the full Amount when the issuer charges
	// a transfer fee. RippleCalc.cpp then only treats it as a sendMax when it
	// is non-negative, or differs in currency/issuer from the source-issued
	// delivery. Reference: rippled Payment.cpp:50-66, RippleCalc.cpp:88-96.
	maxSourceAmount := p.getMaxSourceAmount(senderID)
	var srcAmount *tx.Amount
	srcIsSelfIssued := !maxSourceAmount.IsNative() && !maxSourceAmount.IsMPT() &&
		maxSourceAmount.Currency == p.Amount.Currency &&
		maxSourceAmount.Issuer == state.EncodeAccountIDSafe(senderID)
	if maxSourceAmount.Signum() >= 0 || !srcIsSelfIssued {
		srcAmount = &maxSourceAmount
	}

	rc := RippleCalculate(
		ctx.View,
		senderID,
		destID,
		p.Amount,
		srcAmount,
		p.Paths,
		addDefaultPath,
		partialPayment,
		limitQuality,
		ctx.TxHash,
		ctx.Config.LedgerSequence,
		rcOpts...,
	)
	actualOut, sandbox, result := rc.ActualOut, rc.Sandbox, rc.Result

	// Because of its overhead, if RippleCalc fails with a retry code (ter*),
	// claim a fee instead. Reference: rippled Payment.cpp:509-510
	if result.IsTer() {
		result = ter.TecPATH_DRY
	}

	// Handle result
	if result != ter.TesSUCCESS && result != ter.TecPATH_PARTIAL {
		return result
	}

	// Apply sandbox changes back to the ledger view (through ApplyStateTable for tracking)
	if sandbox != nil {
		if err := sandbox.ApplyToView(ctx.View); err != nil {
			return ter.TefINTERNAL
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
			return ter.TecPATH_PARTIAL
		}
	}

	// Record delivered amount in metadata only on a successful delivery whose
	// amount differs from the requested Amount. rippled sets sfDeliveredAmount
	// only when result == tesSUCCESS && actualAmountOut != dstAmount
	// (Payment.cpp:495); a full delivery omits it, and a non-success result
	// (e.g. tecPATH_PARTIAL when partial payment is disallowed) never sets it.
	// Emitting it on full payments or on a tec result forks the transaction
	// tree from rippled — observed as a mixed-network transaction_hash
	// divergence (identical account_hash) at low seqs before amendments settle.
	//
	// The delivered amount carries the requested Amount's issue (currency and
	// issuer), taking only the value from the flow engine. This mirrors
	// rippled's actualAmountOut = toSTAmount(flowOut.out, dstIssue), where
	// dstIssue is the delivered Amount's issue. The flow engine's own output
	// amount may carry a different (path-internal) issuer.
	// Reference: rippled Flow.cpp:49,79.
	deliveredAmt := deliveredWithDstIssue(actualOut, p.Amount)
	if result == ter.TesSUCCESS && deliveredAmt.Compare(p.Amount) != 0 {
		ctx.Metadata.DeliveredAmount = &deliveredAmt
	}

	// Offer deletions and trust line modifications tracked automatically by ApplyStateTable

	return result
}

// getMaxSourceAmount returns the maximum amount the source will spend. When
// SendMax is present it is used directly. Otherwise the bound defaults to the
// delivered Amount: native XRP and MPT amounts are used as-is, while an IOU is
// re-issued by the source account so the flow engine charges the issuer's
// transfer fee against the delivery rather than grossing it onto the source.
// Reference: rippled Payment.cpp getMaxSourceAmount() lines 50-66.
func (p *Payment) getMaxSourceAmount(srcAccount [20]byte) tx.Amount {
	if p.SendMax != nil {
		return *p.SendMax
	}
	if p.Amount.IsNative() || p.Amount.IsMPT() {
		return p.Amount
	}
	iou := p.Amount.IOU()
	return state.NewIssuedAmountFromValue(
		iou.Mantissa(),
		iou.Exponent(),
		p.Amount.Currency,
		state.EncodeAccountIDSafe(srcAccount),
	)
}

// deliveredWithDstIssue returns the flow engine's delivered amount carrying the
// requested Amount's issue. Only the value comes from the flow output; the
// currency and issuer are taken from dstAmount. This mirrors rippled's
// actualAmountOut = toSTAmount(flowOut.out, dstIssue).
// Reference: rippled Flow.cpp:49,79.
func deliveredWithDstIssue(actualOut EitherAmount, dstAmount tx.Amount) tx.Amount {
	out := FromEitherAmount(actualOut)
	if out.IsNative() || dstAmount.IsNative() || dstAmount.IsMPT() {
		return out
	}
	iou := out.IOU()
	return state.NewIssuedAmountFromValue(
		iou.Mantissa(),
		iou.Exponent(),
		dstAmount.Currency,
		dstAmount.Issuer,
	)
}

// ApplyOnTec implements TecApplier. When tecEXPIRED is returned, this re-runs
// credential expiration deletion against the engine's view so the side-effects persist.
// Reference: rippled Transactor.cpp - tecEXPIRED re-applies removeExpiredCredentials
func (p *Payment) ApplyOnTec(ctx *tx.ApplyContext) {
	credential.RemoveExpiredCredentials(ctx, p.CredentialIDs)
}
