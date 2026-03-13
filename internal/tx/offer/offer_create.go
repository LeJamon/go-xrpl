// Package offer implements the OfferCreate and OfferCancel transactions.
// Reference: rippled CreateOffer.cpp, CancelOffer.cpp
package offer

import (
	"math/big"
	"strings"

	"github.com/LeJamon/goXRPLd/amendment"
	"github.com/LeJamon/goXRPLd/internal/ledger/state"
	"github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/internal/tx/payment"
	"github.com/LeJamon/goXRPLd/internal/tx/permissioneddomain"
	"github.com/LeJamon/goXRPLd/keylet"
)

// OfferCreate flag mask - invalid flags
// Reference: rippled TxFlags.h
const (
	// tfHybrid flag for permissioned DEX hybrid offers
	tfHybrid uint32 = 0x00100000

	// tfOfferCreateMask is the mask for INVALID flags.
	// Any flags set in this mask are invalid for OfferCreate.
	// Reference: rippled TxFlags.h lines 103-104
	// Derived from: ~(tfUniversal | tfPassive | tfImmediateOrCancel | tfFillOrKill | tfSell | tfHybrid)
	// Valid flags: 0xC0000000 | 0x00010000 | 0x00020000 | 0x00040000 | 0x00080000 | 0x00100000 = 0xC01F0000
	// Mask for invalid: ~0xC01F0000 = 0x3FE0FFFF
	tfOfferCreateMask uint32 = 0x3FE0FFFF
)

// Quality constants
const (
	maxTickSize uint8 = 15
)

// OfferCreate places an offer on the decentralized exchange.
type OfferCreate struct {
	tx.BaseTx

	// TakerGets is the amount and currency the offer creator receives (required)
	TakerGets tx.Amount `json:"TakerGets" xrpl:"TakerGets,amount"`

	// TakerPays is the amount and currency the offer creator pays (required)
	TakerPays tx.Amount `json:"TakerPays" xrpl:"TakerPays,amount"`

	// Expiration is the time when the offer expires (optional)
	Expiration *uint32 `json:"Expiration,omitempty" xrpl:"Expiration,omitempty"`

	// OfferSequence is the sequence number of an offer to cancel (optional)
	OfferSequence *uint32 `json:"OfferSequence,omitempty" xrpl:"OfferSequence,omitempty"`

	// DomainID is the permissioned domain for hybrid offers (optional, requires PermissionedDEX amendment)
	DomainID *[32]byte `json:"DomainID,omitempty" xrpl:"DomainID,omitempty"`
}

func init() {
	tx.Register(tx.TypeOfferCreate, func() tx.Transaction {
		return &OfferCreate{BaseTx: *tx.NewBaseTx(tx.TypeOfferCreate, "")}
	})
}

// NewOfferCreate creates a new OfferCreate transaction
func NewOfferCreate(account string, takerGets, takerPays tx.Amount) *OfferCreate {
	return &OfferCreate{
		BaseTx:    *tx.NewBaseTx(tx.TypeOfferCreate, account),
		TakerGets: takerGets,
		TakerPays: takerPays,
	}
}

// TxType returns the transaction type
func (o *OfferCreate) TxType() tx.Type {
	return tx.TypeOfferCreate
}

// Validate performs rules-independent validation on the OfferCreate transaction.
// This is called by the engine's preflight step BEFORE hash computation and fee deduction.
// All checks here must NOT depend on amendment rules (rules-dependent checks go in Preflight).
// Reference: rippled CreateOffer.cpp preflight() - rules-independent subset
func (o *OfferCreate) Validate() error {
	if err := o.BaseTx.Validate(); err != nil {
		return err
	}

	// Check for invalid flags
	// Reference: rippled CreateOffer.cpp preflight() lines 61-65
	flags := o.GetFlags()
	if flags&tfOfferCreateMask != 0 {
		return tx.Errorf(tx.TemINVALID_FLAG, "invalid flags set")
	}

	// IoC and FoK are mutually exclusive
	// Reference: lines 73-80
	bImmediateOrCancel := (flags & OfferCreateFlagImmediateOrCancel) != 0
	bFillOrKill := (flags & OfferCreateFlagFillOrKill) != 0
	if bImmediateOrCancel && bFillOrKill {
		return tx.Errorf(tx.TemINVALID_FLAG, "cannot set both ImmediateOrCancel and FillOrKill")
	}

	// tfHybrid requires DomainID (rules-independent check)
	// Reference: lines 70-71
	if (flags&tfHybrid != 0) && o.DomainID == nil {
		return tx.Errorf(tx.TemINVALID_FLAG, "tfHybrid requires DomainID")
	}

	// Check expiration
	// Reference: lines 82-88
	if o.Expiration != nil && *o.Expiration == 0 {
		return tx.Errorf(tx.TemBAD_EXPIRATION, "expiration cannot be zero")
	}

	// Check OfferSequence
	// Reference: lines 90-95
	if o.OfferSequence != nil && *o.OfferSequence == 0 {
		return tx.Errorf(tx.TemBAD_SEQUENCE, "OfferSequence cannot be zero")
	}

	// Validate amounts
	saTakerPays := o.TakerPays
	saTakerGets := o.TakerGets

	// Check required amounts are present (unset Amount has no type info)
	if !saTakerPays.IsNative() && saTakerPays.Currency == "" {
		return tx.Errorf(tx.TemBAD_OFFER, "TakerPays is required")
	}
	if !saTakerGets.IsNative() && saTakerGets.Currency == "" {
		return tx.Errorf(tx.TemBAD_OFFER, "TakerGets is required")
	}

	// Check amounts are present and valid
	// Reference: lines 97-101
	if !isLegalNetAmount(saTakerPays) || !isLegalNetAmount(saTakerGets) {
		return tx.Errorf(tx.TemBAD_AMOUNT, "invalid amount")
	}

	// Cannot exchange XRP for XRP
	// Reference: lines 103-107
	if saTakerPays.IsNative() && saTakerGets.IsNative() {
		return tx.Errorf(tx.TemBAD_OFFER, "cannot exchange XRP for XRP")
	}

	// Amounts must be positive
	// Reference: lines 108-112
	if isAmountZeroOrNegative(saTakerPays) || isAmountZeroOrNegative(saTakerGets) {
		return tx.Errorf(tx.TemBAD_OFFER, "amounts must be positive")
	}

	// Get currency and issuer info
	uPaysCurrency := saTakerPays.Currency
	uPaysIssuerID := saTakerPays.Issuer
	uGetsCurrency := saTakerGets.Currency
	uGetsIssuerID := saTakerGets.Issuer

	// Check for redundant offer (same currency and issuer)
	// Reference: lines 120-124
	if uPaysCurrency == uGetsCurrency && uPaysIssuerID == uGetsIssuerID {
		return tx.Errorf(tx.TemREDUNDANT, "cannot create offer with same currency and issuer on both sides")
	}

	// Check for bad currency (XRP as non-native currency code)
	// Reference: lines 126-130
	if !saTakerPays.IsNative() && uPaysCurrency == badCurrency() {
		return tx.Errorf(tx.TemBAD_CURRENCY, "cannot use XRP as non-native currency code")
	}
	if !saTakerGets.IsNative() && uGetsCurrency == badCurrency() {
		return tx.Errorf(tx.TemBAD_CURRENCY, "cannot use XRP as non-native currency code")
	}

	// Check issuer consistency
	// Reference: lines 132-137
	if saTakerPays.IsNative() != (uPaysIssuerID == "") {
		return tx.Errorf(tx.TemBAD_ISSUER, "issuer mismatch for TakerPays")
	}
	if saTakerGets.IsNative() != (uGetsIssuerID == "") {
		return tx.Errorf(tx.TemBAD_ISSUER, "issuer mismatch for TakerGets")
	}

	return nil
}

// Flatten returns a flat map of all transaction fields
func (o *OfferCreate) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(o)
}

// SetPassive makes the offer passive
func (o *OfferCreate) SetPassive() {
	flags := o.GetFlags() | OfferCreateFlagPassive
	o.SetFlags(flags)
}

// SetImmediateOrCancel makes the offer immediate-or-cancel
func (o *OfferCreate) SetImmediateOrCancel() {
	flags := o.GetFlags() | OfferCreateFlagImmediateOrCancel
	o.SetFlags(flags)
}

// SetFillOrKill makes the offer fill-or-kill
func (o *OfferCreate) SetFillOrKill() {
	flags := o.GetFlags() | OfferCreateFlagFillOrKill
	o.SetFlags(flags)
}

// parsePreflightError converts a preflight error message to the appropriate TER code.
// Reference: rippled uses specific TER codes for different validation failures.
func parsePreflightError(err error) tx.Result {
	if err == nil {
		return tx.TesSUCCESS
	}
	msg := err.Error()

	// Map error message prefixes to result codes
	prefixes := []struct {
		prefix string
		result tx.Result
	}{
		{"temDISABLED", tx.TemDISABLED},
		{"temINVALID_FLAG", tx.TemINVALID_FLAG},
		{"temBAD_EXPIRATION", tx.TemBAD_EXPIRATION},
		{"temBAD_SEQUENCE", tx.TemBAD_SEQUENCE},
		{"temBAD_AMOUNT", tx.TemBAD_AMOUNT},
		{"temBAD_OFFER", tx.TemBAD_OFFER},
		{"temREDUNDANT", tx.TemREDUNDANT},
		{"temBAD_CURRENCY", tx.TemBAD_CURRENCY},
		{"temBAD_ISSUER", tx.TemBAD_ISSUER},
	}

	for _, p := range prefixes {
		if strings.HasPrefix(msg, p.prefix) {
			return p.result
		}
	}

	return tx.TemMALFORMED
}

// Apply applies an OfferCreate transaction to the ledger state.
// This implements the full rippled CreateOffer flow:
// 1. Preflight validation (with amendment rules)
// 2. Preclaim checks (frozen assets, funds, authorization)
// 3. Offer crossing via flow engine
// 4. Offer placement if not fully filled
// Reference: rippled CreateOffer.cpp doApply()
func (o *OfferCreate) Apply(ctx *tx.ApplyContext) tx.Result {
	ctx.Log.Trace("offer create apply",
		"account", o.Account,
		"takerPays", o.TakerPays,
		"takerGets", o.TakerGets,
		"flags", o.GetFlags(),
	)

	// Run preflight validation with amendment rules
	// Reference: rippled CreateOffer.cpp preflight()
	if err := o.Preflight(ctx.Rules()); err != nil {
		// Convert preflight error to appropriate TER code
		return parsePreflightError(err)
	}

	// Run preclaim checks (frozen assets, authorization, funds, etc.)
	// Reference: rippled CreateOffer.cpp preclaim()
	result := o.Preclaim(ctx)
	if result != tx.TesSUCCESS {
		return result
	}

	// Run the main apply logic
	// Reference: rippled CreateOffer.cpp applyGuts()
	return o.ApplyCreate(ctx)
}

// badCurrency returns the "bad" currency code - using XRP as a non-native currency code
// Reference: rippled protocol/Issue.h badCurrency()
func badCurrency() string {
	return "XRP"
}

// Preflight performs all validation on the OfferCreate transaction.
// This matches rippled's preflight() which does ALL semantic validation.
// Reference: rippled CreateOffer.cpp preflight() lines 46-140
func (o *OfferCreate) Preflight(rules *amendment.Rules) error {
	// Only rules-dependent checks remain here. Rules-independent validation
	// is done in Validate() which runs before hash computation and fee deduction.

	// Check if DomainID field is present without PermissionedDEX amendment
	// Reference: rippled CreateOffer.cpp preflight() lines 49-51
	if o.DomainID != nil && !rules.PermissionedDEXEnabled() {
		return tx.Errorf(tx.TemDISABLED, "DomainID requires PermissionedDEX amendment")
	}

	// Check tfHybrid without PermissionedDEX
	// Reference: lines 67-68
	flags := o.GetFlags()
	if !rules.PermissionedDEXEnabled() && (flags&tfHybrid != 0) {
		return tx.Errorf(tx.TemINVALID_FLAG, "tfHybrid requires PermissionedDEX amendment")
	}

	return nil
}

// Preclaim validates the transaction against ledger state before application.
// Reference: rippled CreateOffer.cpp preclaim() lines 142-225
func (o *OfferCreate) Preclaim(ctx *tx.ApplyContext) tx.Result {
	rules := ctx.Rules()

	saTakerPays := o.TakerPays
	saTakerGets := o.TakerGets

	uPaysIssuerID := saTakerPays.Issuer
	uGetsIssuerID := saTakerGets.Issuer

	// Check global freeze on both issuers
	// Reference: lines 165-170
	if uPaysIssuerID != "" {
		if tx.IsGlobalFrozen(ctx.View, uPaysIssuerID) {
			return tx.TecFROZEN
		}
	}
	if uGetsIssuerID != "" {
		if tx.IsGlobalFrozen(ctx.View, uGetsIssuerID) {
			return tx.TecFROZEN
		}
	}

	// Check account has funds for the offer (at least partially funded)
	// Reference: rippled CreateOffer.cpp preclaim() lines 172-178
	// rippled checks accountFunds <= 0, NOT funds < takerGets.
	// Partially-funded offers are allowed; only completely unfunded offers are rejected.
	funds := tx.AccountFunds(ctx.View, ctx.AccountID, saTakerGets, true, ctx.Config.ReserveBase, ctx.Config.ReserveIncrement)
	if funds.Signum() <= 0 {
		return tx.TecUNFUNDED_OFFER
	}

	// Check cancel sequence is valid (must be less than current account sequence)
	// Reference: lines 182-187
	if o.OfferSequence != nil {
		if ctx.Account.Sequence <= *o.OfferSequence {
			return tx.TemBAD_SEQUENCE
		}
	}

	// Check if offer has expired
	// Reference: lines 189-200
	if hasExpired(ctx, o.Expiration) {
		if rules.DepositPreauthEnabled() {
			return tx.TecEXPIRED
		}
		return tx.TesSUCCESS
	}

	// Check we can accept what the taker will pay us (for non-native)
	// Reference: lines 203-213
	if !saTakerPays.IsNative() {
		paysIssuerID, err := state.DecodeAccountID(uPaysIssuerID)
		if err != nil {
			return tx.TecNO_ISSUER
		}
		result := checkAcceptAsset(ctx, paysIssuerID, saTakerPays.Currency, rules)
		if result != tx.TesSUCCESS {
			return result
		}
	}

	// Check domain membership if DomainID is specified
	// Reference: lines 217-222
	if o.DomainID != nil {
		if !accountInDomain(ctx.View, ctx.AccountID, *o.DomainID, ctx.Config.ParentCloseTime) {
			return tx.TecNO_PERMISSION
		}
	}

	return tx.TesSUCCESS
}

// ApplyCreate applies the OfferCreate transaction to the ledger.
// This is the main entry point called by the engine.
// Reference: rippled CreateOffer.cpp doApply() lines 932-949
//
// This implements the two-sandbox pattern for FillOrKill (FoK) offers:
// - sb: main sandbox for crossing and offer placement
// - sbCancel: cancel sandbox for offer cancellation only
//
// For FoK offers that don't fully fill, we apply sbCancel instead of sb,
// ensuring the cancellation happens but the crossing changes are discarded.
func (o *OfferCreate) ApplyCreate(ctx *tx.ApplyContext) tx.Result {
	// Create TWO independent sandboxes from ctx.View
	// Reference: rippled CreateOffer.cpp lines 938-941
	sb := payment.NewPaymentSandbox(ctx.View)
	sbCancel := payment.NewPaymentSandbox(ctx.View)

	// Set transaction context on both sandboxes
	sb.SetTransactionContext(ctx.TxHash, ctx.Config.LedgerSequence)
	sbCancel.SetTransactionContext(ctx.TxHash, ctx.Config.LedgerSequence)

	// Snapshot OwnerCount before applyGuts.
	// applyGuts may modify ctx.Account.OwnerCount directly (e.g., offer placement OwnerCount++
	// or offer cancel OwnerCount--). It ALSO modifies OwnerCount in the sandbox through
	// trust line creation/deletion during crossing. We need to merge both.
	preGutsOwnerCount := ctx.Account.OwnerCount

	// Execute applyGuts with both sandboxes
	result, applyMain := o.applyGuts(ctx, sb, sbCancel)

	// Compute the OwnerCount delta from ctx.Account changes (offer placement/cancel)
	ctxDelta := int32(ctx.Account.OwnerCount) - int32(preGutsOwnerCount)

	// Apply the correct sandbox to the ledger view
	activeSb := sb
	if !applyMain {
		activeSb = sbCancel
		// sbCancel has no crossing changes, only removable offer deletions.
		// The account balance will be read from the sandbox after applying.
	}
	if err := activeSb.ApplyToView(ctx.View); err != nil {
		return tx.TefINTERNAL
	}

	// Re-read the account from the view to get the sandbox's changes.
	// In rippled, mTxnAccount lives inside the sandbox so changes are automatic.
	// In goXRPL, ctx.Account is separate, so we must merge:
	//   - Balance: read directly from sandbox (crossing modifies it)
	//   - OwnerCount: sandbox changes + ctx delta (offer placement/cancel)
	accountKey := keylet.Account(ctx.AccountID)
	if updatedData, readErr := ctx.View.Read(accountKey); readErr == nil && updatedData != nil {
		if updatedAccount, parseErr := state.ParseAccountRoot(updatedData); parseErr == nil {
			ctx.Account.Balance = updatedAccount.Balance
			ctx.Account.OwnerCount = uint32(int32(updatedAccount.OwnerCount) + ctxDelta)
		}
	}

	return result
}

// applyGuts contains the main offer creation logic with two-sandbox pattern.
// Reference: rippled CreateOffer.cpp applyGuts() lines 576-929
//
// The two-sandbox pattern ensures FillOrKill offers that don't fully fill
// only apply the cancellation changes, not the crossing changes.
//
// Parameters:
//   - ctx: the apply context
//   - sb: main sandbox for crossing and offer placement
//   - sbCancel: cancel sandbox for offer cancellation only
//
// Returns:
//   - result: the transaction result code
//   - applyMain: true to apply sb, false to apply sbCancel
func (o *OfferCreate) applyGuts(ctx *tx.ApplyContext, sb, sbCancel *payment.PaymentSandbox) (tx.Result, bool) {
	rules := ctx.Rules()

	flags := o.GetFlags()
	bPassive := (flags & OfferCreateFlagPassive) != 0
	bImmediateOrCancel := (flags & OfferCreateFlagImmediateOrCancel) != 0
	bFillOrKill := (flags & OfferCreateFlagFillOrKill) != 0
	bSell := (flags & OfferCreateFlagSell) != 0
	bHybrid := (flags & tfHybrid) != 0

	saTakerPays := o.TakerPays
	saTakerGets := o.TakerGets

	// Calculate the original rate (quality) for the offer
	// Reference: line 601
	uRate := state.GetRate(saTakerGets, saTakerPays)
	result := tx.TesSUCCESS

	// Process cancellation request if specified
	// Reference: lines 608-621
	// CRITICAL: Offer cancellation must happen in BOTH sandboxes
	if o.OfferSequence != nil {
		sleCancel := peekOffer(ctx.View, ctx.AccountID, *o.OfferSequence)
		if sleCancel != nil {
			// Delete in main sandbox
			result = offerDeleteInView(sb, sleCancel)
			// Delete in cancel sandbox (same operation)
			_ = offerDeleteInView(sbCancel, sleCancel)

			// Also update owner count (once, since we'll only apply one sandbox)
			if result == tx.TesSUCCESS && ctx.Account.OwnerCount > 0 {
				ctx.Account.OwnerCount--
			}
		}
	}

	// Check if offer has expired
	// Reference: lines 623-636
	if hasExpired(ctx, o.Expiration) {
		if rules.DepositPreauthEnabled() {
			return tx.TecEXPIRED, false // Apply cancel sandbox for expired offers
		}
		return tx.TesSUCCESS, true
	}

	crossed := false

	// Capture prior balance BEFORE crossing, matching rippled's mPriorBalance.
	// ctx.Account.Balance has fee already deducted by the engine.
	// Reconstruct the pre-fee balance to match rippled's mPriorBalance.
	// Reference: rippled Transactor.cpp: mPriorBalance = mTxnAccount->getFieldAmount(sfBalance).xrp()
	mPriorBalance := ctx.Account.Balance + parseFee(ctx)

	if result == tx.TesSUCCESS {
		// Apply tick size rounding if applicable
		// Reference: lines 643-685
		saTakerPays, saTakerGets = applyTickSize(ctx.View, saTakerPays, saTakerGets, bSell, rules)
		if isAmountZeroOrNegative(saTakerPays) || isAmountZeroOrNegative(saTakerGets) {
			// Offer rounded to zero
			return tx.TesSUCCESS, true
		}

		// Recalculate rate after tick size
		uRate = state.GetRate(saTakerGets, saTakerPays)

		// Perform offer crossing using the main sandbox (sb)
		// Reference: lines 687-768
		// Note: Passive offers still cross, but only against offers with STRICTLY better quality.
		// The passive flag is passed to FlowCross which increments the quality threshold.
		// Reference: rippled CreateOffer.cpp lines 362-364
		var placeOffer struct {
			in  tx.Amount
			out tx.Amount
		}

		ctx.Log.Trace("offer crossing start",
			"takerPays", saTakerPays,
			"takerGets", saTakerGets,
			"passive", bPassive,
			"sell", bSell,
		)

		// FlowCross operates on the main sandbox (sb)
		crossResult := payment.FlowCross(
			sb, // Use main sandbox for crossing
			ctx.AccountID,
			saTakerGets, // What we're selling (taker pays to counterparty)
			saTakerPays, // What we want (taker receives from counterparty)
			ctx.TxHash,
			ctx.Config.LedgerSequence,
			bPassive, // For passive offers, only cross against strictly better quality
			bSell,    // For sell offers, deliver MAX (sell all input regardless of output)
			ctx.Config.ParentCloseTime,
			ctx.Config.ReserveBase,
			ctx.Config.ReserveIncrement,
			rules.Enabled(amendment.FeatureFixReducedOffersV1),
			rules.Enabled(amendment.FeatureFixReducedOffersV2),
			rules.Enabled(amendment.FeatureFixRmSmallIncreasedQOffers),
			rules.Enabled(amendment.FeatureFlowSortStrands),
			rules.Enabled(amendment.FeatureFixAMMv1_1),
			rules.Enabled(amendment.FeatureFixAMMv1_2),
			rules.Enabled(amendment.FeatureFixAMMOverflowOffer),
			o.DomainID, // Domain ID for permissioned DEX offer crossing
		)

		// Convert result amounts back
		// For remaining offer calculation:
		// - If GROSS >= originalTakerGets: offer fully consumed, no remaining
		// - Else: remaining = originalTakerGets - NET (use net delivered amount)
		// Reference: rippled uses the net amount delivered to matching offers for remaining calculation
		grossPaid := payment.FromEitherAmount(crossResult.TakerPaid)
		netPaid := payment.FromEitherAmount(crossResult.TakerPaidNet)

		// Use GROSS for the "fully consumed" check via subtractAmounts clamping
		// But for actual remaining calculation, use NET when we have a remaining offer
		// The subtractAmounts function clamps negative to zero, so:
		// - If GROSS >= original: remaining = 0 (no offer created)
		// - If GROSS < original: we need remaining = original - NET
		remainingWithGross := subtractAmounts(saTakerGets, grossPaid)
		if isAmountZeroOrNegative(remainingWithGross) {
			// Offer fully consumed with GROSS, use GROSS
			placeOffer.in = grossPaid
		} else {
			// Offer has remainder, use NET for accurate remaining calculation
			placeOffer.in = netPaid
		}
		placeOffer.out = payment.FromEitherAmount(crossResult.TakerGot) // What we received

		result = crossResult.Result
		ctx.Log.Trace("offer crossing done",
			"result", result,
			"takerPaid", grossPaid,
			"takerGot", placeOffer.out,
		)

		// For offer crossing, tecPATH_DRY means no liquidity found to cross
		// This is not an error - we just place the offer with original amounts
		// Reference: rippled's flowCross always returns tesSUCCESS (CreateOffer.cpp line 509)
		if result == tx.TecPATH_DRY {
			result = tx.TesSUCCESS
		}

		if result != tx.TesSUCCESS {
			return result, false // Error during crossing - apply cancel sandbox
		}

		// Check if account's funds were exhausted during crossing.
		// Reference: rippled CreateOffer.cpp lines 432-441.
		// Must use the PaymentSandbox with BalanceHook BEFORE applying it to the view,
		// matching rippled's accountFunds(psb, ...) call. BalanceHook subtracts
		// DeferredCredits, returning zero for self-crossing round-trips even when the
		// on-ledger balance is non-zero.
		var takerInBalance tx.Amount
		if crossResult.Sandbox != nil {
			takerInBalance = payment.AccountFundsInSandbox(crossResult.Sandbox, ctx.AccountID, saTakerGets, true, ctx.Config.ReserveBase, ctx.Config.ReserveIncrement)
		} else {
			takerInBalance = tx.AccountFunds(sb, ctx.AccountID, saTakerGets, true, ctx.Config.ReserveBase, ctx.Config.ReserveIncrement)
		}

		// Apply FlowCross sandbox changes to our main sandbox (sb)
		// Reference: rippled CreateOffer.cpp - sandbox changes must be applied
		// FlowCross creates a root sandbox, so we use ApplyToView with sb as the target
		if crossResult.Sandbox != nil {
			if err := crossResult.Sandbox.ApplyToView(sb); err != nil {
				return tx.TefINTERNAL, false
			}
		}

		// NOTE: We do NOT manually adjust ctx.Account.Balance here.
		// In rippled, mTxnAccount lives inside the sandbox, so balance changes
		// from crossing are applied when the sandbox is applied. In goXRPL,
		// ctx.Account is separate, so we re-read the account balance from the
		// view AFTER applying the sandbox (see ApplyCreate lines 421-424).
		// Manually adjusting here would DOUBLE-COUNT the XRP changes.

		// Remove unfunded/self-crossed offers that were marked during crossing.
		// Must delete from BOTH sandboxes so that regardless of which one is applied
		// (sb for success, sbCancel for FillOrKill failure), orphan offers are cleaned up.
		// Reference: rippled CreateOffer.cpp lines 420-426: deletes from psb AND psbCancel.
		for offerKey := range crossResult.RemovableOffers {
			offerKeylet := keylet.Keylet{Key: offerKey}

			// Delete from main sandbox
			offerData, err := sb.Read(offerKeylet)
			if err == nil && offerData != nil {
				offer, err := state.ParseLedgerOffer(offerData)
				if err == nil {
					ownerID, err := state.DecodeAccountID(offer.Account)
					if err == nil {
						offerDeleteInView(sb, offer)
						adjustOwnerCountInView(sb, ownerID, -1)
					}
				}
			}

			// Delete from cancel sandbox (same offer, independent view)
			offerDataCancel, err := sbCancel.Read(offerKeylet)
			if err == nil && offerDataCancel != nil {
				offer, err := state.ParseLedgerOffer(offerDataCancel)
				if err == nil {
					ownerID, err := state.DecodeAccountID(offer.Account)
					if err == nil {
						_ = offerDeleteInView(sbCancel, offer)
						adjustOwnerCountInView(sbCancel, ownerID, -1)
					}
				}
			}
		}

		if isAmountZeroOrNegative(takerInBalance) {
			return tx.TesSUCCESS, true // Apply main sandbox with crossing results
		}

		// Check if any crossing happened
		// Reference: line 744-745
		// Use isAmountZeroOrNegative because FromEitherAmount returns "0" for zero amounts,
		// not empty string ""
		if !isAmountZeroOrNegative(placeOffer.in) || !isAmountZeroOrNegative(placeOffer.out) {
			crossed = true
		}

		// Calculate remaining amounts for the new offer
		// Reference: rippled CreateOffer.cpp lines 491-504 (flowCross)
		// Rippled preserves the offer quality when calculating remaining amounts.
		//
		// IMPORTANT: When the offer is fully consumed (GROSS >= originalTakerGets),
		// the taker has paid everything (or more with transfer fees), so NO remaining
		// offer should be created. We use subtraction which will give zero/negative.
		//
		// When no crossing happened at all (placeOffer is zero), return original amounts
		// directly to avoid float64 precision errors in ratio calculation.
		//
		// Only when partial crossing happened do we use quality preservation.
		//
		// For non-sell offers (Flags=0) with remaining:
		//   1. remainingPays = originalTakerPays - actualAmountOut (XRP received)
		//   2. remainingGets = remainingPays * (originalTakerGets / originalTakerPays) (quality preserved)
		//
		// For sell offers (tfSell):
		//   1. remainingGets = originalTakerGets - (actualAmountIn / transferRate) (non-gateway amount)
		//   2. remainingPays = remainingGets * (originalTakerPays / originalTakerGets) (quality preserved)
		var remainingGets, remainingPays tx.Amount

		noCrossingHappened := isAmountZeroOrNegative(placeOffer.in) && isAmountZeroOrNegative(placeOffer.out)

		if isAmountZeroOrNegative(remainingWithGross) {
			// Offer fully consumed - taker paid everything (GROSS >= original)
			// Use subtraction which will give zero/negative, triggering "fully crossed" below
			remainingGets = subtractAmounts(saTakerGets, placeOffer.in)
			remainingPays = subtractAmounts(saTakerPays, placeOffer.out)
		} else if noCrossingHappened {
			// No crossing happened - return original amounts directly
			// This avoids float64 precision errors in ratio calculation
			remainingGets = saTakerGets
			remainingPays = saTakerPays
		} else if bSell {
			// Sell offer with remaining: subtract from TakerGets, calculate TakerPays by ratio
			// Reference: rippled CreateOffer.cpp lines 447-489
			// rate = Quality{takerAmount.out, takerAmount.in}.rate() = TakerGets/TakerPays
			// afterCross.out = divRound(afterCross.in, rate, TakerPays.issue, roundUp)
			// The fixReducedOffersV1 amendment changes the rounding:
			// - Before: divRound with roundUp=true
			// - After: divRoundStrict with roundUp=false
			remainingGets = subtractAmounts(saTakerGets, placeOffer.in) // placeOffer.in is NET
			rate := payment.QualityFromAmounts(
				payment.ToEitherAmount(saTakerGets),
				payment.ToEitherAmount(saTakerPays),
			).Rate()
			outNative := saTakerPays.IsNative()
			outCurrency := saTakerPays.Currency
			outIssuer := saTakerPays.Issuer
			if rules.Enabled(amendment.FeatureFixReducedOffersV1) {
				remainingPays = offerDivRoundStrict(remainingGets, rate, outNative, outCurrency, outIssuer, false)
			} else {
				remainingPays = offerDivRound(remainingGets, rate, outNative, outCurrency, outIssuer, true)
			}
		} else {
			// Non-sell offer with remaining: subtract from TakerPays, calculate TakerGets by ratio
			// Reference: rippled CreateOffer.cpp lines 497-503
			// afterCross.in = mulRound(afterCross.out, rate, TakerGets.issue, true)
			// For non-sell offers, always round up (mulRound, non-strict)
			remainingPays = subtractAmounts(saTakerPays, placeOffer.out)
			rate := payment.QualityFromAmounts(
				payment.ToEitherAmount(saTakerGets),
				payment.ToEitherAmount(saTakerPays),
			).Rate()
			outNative := saTakerGets.IsNative()
			outCurrency := saTakerGets.Currency
			outIssuer := saTakerGets.Issuer
			remainingGets = offerMulRound(remainingPays, rate, outNative, outCurrency, outIssuer, true)
		}

		// Check if offer is fully filled
		// Reference: rippled CreateOffer.cpp lines 757-761
		fullyCrossed := isAmountZeroOrNegative(remainingGets) || isAmountZeroOrNegative(remainingPays)

		// Without fixFillOrKill, FoK requires TakerGets to be fully consumed
		// (GROSS paid >= original TakerGets), not just TakerPays fully received.
		// The proportional remaining calculation can yield zero even when TakerGets
		// isn't fully consumed (because TakerPays was fully satisfied at a better rate).
		// Reference: rippled CreateOffer.cpp: pre-amendment requires full TakerGets
		// consumption for FoK; post-amendment relaxes non-sell FoK.
		if fullyCrossed && bFillOrKill && !rules.Enabled(amendment.FeatureFixFillOrKill) {
			if !isAmountZeroOrNegative(remainingWithGross) {
				// FoK not satisfied: TakerGets not fully consumed.
				// Remaining amounts are zero from proportional calc, so we can't
				// create a remaining offer. Return FoK failure directly.
				if rules.Enabled(amendment.FeatureFix1578) {
					return tx.TecKILLED, false
				}
				return tx.TesSUCCESS, false
			}
		}

		if fullyCrossed {
			return tx.TesSUCCESS, true
		}

		// Adjust amounts for remaining offer
		// Reference: lines 766-767
		saTakerPays = remainingPays
		saTakerGets = remainingGets
	}

	// Sanity check: amounts should be positive
	if isAmountZeroOrNegative(saTakerPays) || isAmountZeroOrNegative(saTakerGets) {
		return tx.TefINTERNAL, false
	}

	if result != tx.TesSUCCESS {
		return result, false
	}

	// Handle FillOrKill - offer was NOT fully filled if we reach here
	// Reference: lines 789-795
	// CRITICAL: For FoK, apply sbCancel to discard crossing changes
	if bFillOrKill {
		if rules.Enabled(amendment.FeatureFix1578) {
			return tx.TecKILLED, false // Apply cancel sandbox
		}
		return tx.TesSUCCESS, false // Pre-amendment: still apply cancel sandbox
	}

	// Handle ImmediateOrCancel
	// Reference: lines 799-809
	if bImmediateOrCancel {
		if !crossed && rules.Enabled(amendment.FeatureImmediateOfferKilled) {
			return tx.TecKILLED, false // No crossing - apply cancel sandbox
		}
		return tx.TesSUCCESS, true // Crossing happened - apply main sandbox
	}

	// Check reserve for new offer
	// Reference: rippled CreateOffer.cpp lines 811-834
	// IMPORTANT: Read OwnerCount fresh from the sandbox, not from ctx.Account.
	// The crossing may have modified OwnerCount (e.g., trust line deletion).
	// rippled: sleCreator = sb.peek(keylet::account(account_))
	ownerCount := ctx.Account.OwnerCount
	accountKey := keylet.Account(ctx.AccountID)
	if sbAccountData, sbErr := sb.Read(accountKey); sbErr == nil && sbAccountData != nil {
		if sbAccount, pErr := state.ParseAccountRoot(sbAccountData); pErr == nil {
			ownerCount = sbAccount.OwnerCount
		}
	}
	reserve := ctx.AccountReserve(ownerCount + 1)
	if mPriorBalance < reserve {
		if !crossed {
			return tx.TecINSUF_RESERVE_OFFER, true
		}
		return tx.TesSUCCESS, true
	}

	// Create the offer in the ledger (in main sandbox)
	// Reference: lines 837-925
	offerSequence := o.getOfferSequence()
	offerKey := keylet.Offer(ctx.AccountID, offerSequence)

	// Calculate book directory fields first (needed for both owner and book directories
	// when SortedDirectories is not enabled)
	// Reference: lines 857-887
	takerPaysCurrency := state.GetCurrencyBytes(saTakerPays.Currency)
	takerPaysIssuer := state.GetIssuerBytes(saTakerPays.Issuer)
	takerGetsCurrency := state.GetCurrencyBytes(saTakerGets.Currency)
	takerGetsIssuer := state.GetIssuerBytes(saTakerGets.Issuer)

	// Domain offers go in a separate domain-keyed book directory.
	// Reference: rippled Indexes.cpp getBookBase() includes domain in hash when set
	var bookBase keylet.Keylet
	if o.DomainID != nil {
		bookBase = keylet.BookDirWithDomain(takerPaysCurrency, takerPaysIssuer, takerGetsCurrency, takerGetsIssuer, *o.DomainID)
	} else {
		bookBase = keylet.BookDir(takerPaysCurrency, takerPaysIssuer, takerGetsCurrency, takerGetsIssuer)
	}
	bookDirKey := keylet.Quality(bookBase, uRate)

	// Add to owner directory
	// Reference: lines 839-848
	ownerDirKey := keylet.OwnerDir(ctx.AccountID)
	ownerDirResult, err := state.DirInsert(sb, ownerDirKey, offerKey.Key, func(dir *state.DirectoryNode) {
		dir.Owner = ctx.AccountID
	})
	if err != nil {
		return tx.TefINTERNAL, false
	}

	// Increment owner count
	// Reference: line 851
	ctx.Account.OwnerCount++

	// Check if book exists (for OrderBookDB tracking)
	bookExisted, _ := sb.Exists(bookDirKey)

	// Add to book directory
	// Reference: lines 884-893
	bookDirResult, err := state.DirInsert(sb, bookDirKey, offerKey.Key, func(dir *state.DirectoryNode) {
		dir.TakerPaysCurrency = takerPaysCurrency
		dir.TakerPaysIssuer = takerPaysIssuer
		dir.TakerGetsCurrency = takerGetsCurrency
		dir.TakerGetsIssuer = takerGetsIssuer
		dir.ExchangeRate = uRate
		// Note: DomainID is stored on the offer itself, not the directory
	})
	if err != nil {
		return tx.TefINTERNAL, false
	}

	// Create the offer SLE
	// Reference: lines 895-910
	ledgerOffer := &state.LedgerOffer{
		Account:           ctx.Account.Account,
		Sequence:          offerSequence,
		TakerPays:         saTakerPays,
		TakerGets:         saTakerGets,
		BookDirectory:     bookDirKey.Key,
		BookNode:          bookDirResult.Page,
		OwnerNode:         ownerDirResult.Page,
		Flags:             0,
		PreviousTxnID:     ctx.TxHash,
		PreviousTxnLgrSeq: ctx.Config.LedgerSequence,
	}

	// Set expiration if specified
	// Reference: line 903-904
	if o.Expiration != nil {
		ledgerOffer.Expiration = *o.Expiration
	}

	// Set offer flags
	// Reference: lines 905-910
	if bPassive {
		ledgerOffer.Flags |= lsfOfferPassive
	}
	if bSell {
		ledgerOffer.Flags |= lsfOfferSell
	}

	// Set DomainID if specified
	if o.DomainID != nil {
		ledgerOffer.DomainID = *o.DomainID
	}

	// Handle hybrid offers
	// Reference: lines 912-919
	if bHybrid {
		result = applyHybridInSandbox(sb, ctx, ledgerOffer, offerKey, saTakerPays, saTakerGets, bookDirKey)
		if result != tx.TesSUCCESS {
			return result, false
		}
	}

	// Serialize and store the offer
	offerData, err := state.SerializeLedgerOffer(ledgerOffer)
	if err != nil {
		return tx.TefINTERNAL, false
	}

	if err := sb.Insert(offerKey, offerData); err != nil {
		return tx.TefINTERNAL, false
	}

	// Track new book in OrderBookDB (not implemented yet)
	_ = bookExisted

	return tx.TesSUCCESS, true // Apply main sandbox
}

// ============================================================================
// Helper functions
// ============================================================================

// isLegalNetAmount checks if an amount is a valid net amount.
// Reference: rippled protocol/STAmount.h isLegalNet()
func isLegalNetAmount(amt tx.Amount) bool {
	// A legal net amount is non-zero
	return !amt.IsZero()
}

// isAmountZeroOrNegative checks if an amount is zero or negative.
func isAmountZeroOrNegative(amt tx.Amount) bool {
	return amt.IsZero() || amt.IsNegative()
}

// subtractAmounts subtracts b from a.
// a - b = result
func subtractAmounts(a, b tx.Amount) tx.Amount {
	result, err := a.Sub(b)
	if err != nil {
		// Type mismatch - return zero amount of a's type
		if a.IsNative() {
			return tx.NewXRPAmount(0)
		}
		return tx.NewIssuedAmount(0, -100, a.Currency, a.Issuer)
	}

	// Clamp negative results to zero
	if result.IsNegative() {
		if result.IsNative() {
			return tx.NewXRPAmount(0)
		}
		return tx.NewIssuedAmount(0, -100, a.Currency, a.Issuer)
	}

	return result
}

// offerDivRound divides num by den using rippled's divRound (non-strict) algorithm
// with native-aware canonicalization. When native=true, uses canonicalizeRound for
// XRP drops; when native=false, uses IOU canonicalize.
// Reference: rippled STAmount.cpp divRoundImpl with canonicalizeRound + DontAffectNumberRoundMode
func offerDivRound(num, den tx.Amount, native bool, currency, issuer string, roundUp bool) tx.Amount {
	if den.IsZero() || num.IsZero() {
		if native {
			return tx.NewXRPAmount(0)
		}
		return tx.NewIssuedAmount(0, -100, currency, issuer)
	}

	numVal := num.Mantissa()
	numOff := num.Exponent()
	denVal := den.Mantissa()
	denOff := den.Exponent()

	if num.IsNative() {
		if numVal < 0 {
			numVal = -numVal
		}
		for numVal < state.MinMantissa {
			numVal *= 10
			numOff--
		}
	}
	if den.IsNative() {
		if denVal < 0 {
			denVal = -denVal
		}
		for denVal < state.MinMantissa {
			denVal *= 10
			denOff--
		}
	}

	resultNegative := num.IsNegative() != den.IsNegative()

	if numVal < 0 {
		numVal = -numVal
	}
	if denVal < 0 {
		denVal = -denVal
	}

	// muldiv_round: (numVal * 10^17 + rounding) / denVal
	tenTo17 := new(big.Int).Exp(big.NewInt(10), big.NewInt(17), nil)
	numerator := new(big.Int).Mul(big.NewInt(numVal), tenTo17)
	if resultNegative != roundUp {
		numerator.Add(numerator, new(big.Int).Sub(big.NewInt(denVal), big.NewInt(1)))
	}
	quotient := new(big.Int).Div(numerator, big.NewInt(denVal))
	amount := quotient.Uint64()
	offset := numOff - denOff - 17

	if resultNegative != roundUp {
		if native {
			// canonicalizeRound for native (XRP drops)
			drops := payment.CanonicalizeDrops(int64(amount), offset)
			if resultNegative {
				drops = -drops
			}
			return tx.NewXRPAmount(drops)
		}
		// canonicalizeRound for IOU
		if amount > uint64(state.MaxMantissa) {
			for amount > 10*uint64(state.MaxMantissa) {
				amount /= 10
				offset++
			}
			amount += 9
			amount /= 10
			offset++
		}
	} else if native {
		// No canonicalize needed, but still need to convert to drops
		drops := int64(amount)
		for offset > 0 {
			drops *= 10
			offset--
		}
		for offset < 0 {
			drops /= 10
			offset++
		}
		if resultNegative {
			drops = -drops
		}
		return tx.NewXRPAmount(drops)
	}

	// DontAffectNumberRoundMode: NO guard
	mantissa := int64(amount)
	if resultNegative {
		mantissa = -mantissa
	}
	result := state.NewIssuedAmountFromValue(mantissa, offset, currency, issuer)

	if roundUp && !resultNegative && result.IsZero() {
		if native {
			return tx.NewXRPAmount(1)
		}
		return state.NewIssuedAmountFromValue(state.MinMantissa, state.MinExponent, currency, issuer)
	}

	return result
}

// offerDivRoundStrict divides num by den using rippled's divRoundStrict algorithm
// with native-aware canonicalization.
// Reference: rippled STAmount.cpp divRoundImpl with canonicalizeRoundStrict + NumberRoundModeGuard
func offerDivRoundStrict(num, den tx.Amount, native bool, currency, issuer string, roundUp bool) tx.Amount {
	if den.IsZero() || num.IsZero() {
		if native {
			return tx.NewXRPAmount(0)
		}
		return tx.NewIssuedAmount(0, -100, currency, issuer)
	}

	numVal := num.Mantissa()
	numOff := num.Exponent()
	denVal := den.Mantissa()
	denOff := den.Exponent()

	if num.IsNative() {
		if numVal < 0 {
			numVal = -numVal
		}
		for numVal < state.MinMantissa {
			numVal *= 10
			numOff--
		}
	}
	if den.IsNative() {
		if denVal < 0 {
			denVal = -denVal
		}
		for denVal < state.MinMantissa {
			denVal *= 10
			denOff--
		}
	}

	resultNegative := num.IsNegative() != den.IsNegative()

	if numVal < 0 {
		numVal = -numVal
	}
	if denVal < 0 {
		denVal = -denVal
	}

	// muldiv_round: (numVal * 10^17 + rounding) / denVal
	tenTo17 := new(big.Int).SetUint64(100_000_000_000_000_000) // 10^17
	bigNum := new(big.Int).Mul(big.NewInt(numVal), tenTo17)
	bigDen := new(big.Int).SetInt64(denVal)
	if resultNegative != roundUp {
		bigNum.Add(bigNum, new(big.Int).Sub(bigDen, big.NewInt(1)))
	}
	bigResult := new(big.Int).Div(bigNum, bigDen)

	amount := bigResult.Uint64()
	offset := numOff - denOff - 17

	if resultNegative != roundUp {
		if native {
			// canonicalizeRoundStrict for native (XRP drops)
			drops := payment.CanonicalizeDropsStrict(int64(amount), offset, roundUp)
			if resultNegative {
				drops = -drops
			}
			return tx.NewXRPAmount(drops)
		}
		// canonicalizeRoundStrict for IOU
		if amount > uint64(state.MaxMantissa) {
			for amount > 10*uint64(state.MaxMantissa) {
				amount /= 10
				offset++
			}
			amount += 9
			amount /= 10
			offset++
		}
	} else if native {
		// No canonicalize needed (resultNegative == roundUp), just convert to drops
		drops := int64(amount)
		for offset > 0 {
			drops *= 10
			offset--
		}
		for offset < 0 {
			drops /= 10
			offset++
		}
		if resultNegative {
			drops = -drops
		}
		return tx.NewXRPAmount(drops)
	}

	// NumberRoundModeGuard with appropriate mode
	var mode state.RoundingMode
	if roundUp != resultNegative {
		mode = state.RoundUpward
	} else {
		mode = state.RoundDownward
	}
	guard := state.NewNumberRoundModeGuard(mode)
	mantissa := int64(amount)
	if resultNegative {
		mantissa = -mantissa
	}
	result := state.NewIssuedAmountFromValue(mantissa, offset, currency, issuer)
	guard.Release()

	if roundUp && !resultNegative && result.IsZero() {
		if native {
			return tx.NewXRPAmount(1)
		}
		return state.NewIssuedAmountFromValue(state.MinMantissa, state.MinExponent, currency, issuer)
	}

	return result
}

// offerMulRound multiplies v1 by v2 using rippled's mulRound (non-strict) algorithm
// with native-aware canonicalization.
// Reference: rippled STAmount.cpp mulRoundImpl with canonicalizeRound + DontAffectNumberRoundMode
func offerMulRound(v1, v2 tx.Amount, native bool, currency, issuer string, roundUp bool) tx.Amount {
	if v1.IsZero() || v2.IsZero() {
		if native {
			return tx.NewXRPAmount(0)
		}
		return tx.NewIssuedAmount(0, -100, currency, issuer)
	}

	value1 := v1.Mantissa()
	offset1 := v1.Exponent()
	value2 := v2.Mantissa()
	offset2 := v2.Exponent()

	if v1.IsNative() {
		if value1 < 0 {
			value1 = -value1
		}
		for value1 < state.MinMantissa {
			value1 *= 10
			offset1--
		}
	}
	if v2.IsNative() {
		if value2 < 0 {
			value2 = -value2
		}
		for value2 < state.MinMantissa {
			value2 *= 10
			offset2--
		}
	}

	resultNegative := v1.IsNegative() != v2.IsNegative()

	if value1 < 0 {
		value1 = -value1
	}
	if value2 < 0 {
		value2 = -value2
	}

	// muldiv_round: (value1 * value2 + rounding) / 10^14
	tenTo14 := new(big.Int).SetUint64(100_000_000_000_000)
	tenTo14m1 := new(big.Int).SetUint64(99_999_999_999_999)
	product := new(big.Int).Mul(big.NewInt(value1), big.NewInt(value2))
	if resultNegative != roundUp {
		product.Add(product, tenTo14m1)
	}
	product.Div(product, tenTo14)

	amount := product.Uint64()
	offset := offset1 + offset2 + 14

	if resultNegative != roundUp {
		if native {
			// canonicalizeRound for native (XRP drops)
			drops := payment.CanonicalizeDrops(int64(amount), offset)
			if resultNegative {
				drops = -drops
			}
			return tx.NewXRPAmount(drops)
		}
		// canonicalizeRound for IOU
		if amount > uint64(state.MaxMantissa) {
			for amount > 10*uint64(state.MaxMantissa) {
				amount /= 10
				offset++
			}
			amount += 9
			amount /= 10
			offset++
		}
	} else if native {
		// No canonicalize needed (resultNegative == roundUp), just convert to drops
		drops := int64(amount)
		for offset > 0 {
			drops *= 10
			offset--
		}
		for offset < 0 {
			drops /= 10
			offset++
		}
		if resultNegative {
			drops = -drops
		}
		return tx.NewXRPAmount(drops)
	}

	// DontAffectNumberRoundMode: NO guard
	mantissa := int64(amount)
	if resultNegative {
		mantissa = -mantissa
	}
	result := state.NewIssuedAmountFromValue(mantissa, offset, currency, issuer)

	if roundUp && !resultNegative && result.IsZero() {
		if native {
			return tx.NewXRPAmount(1)
		}
		return state.NewIssuedAmountFromValue(state.MinMantissa, state.MinExponent, currency, issuer)
	}

	return result
}

// hasExpired checks if an offer has expired.
// Reference: rippled app/tx/impl/Transactor.cpp hasExpired()
func hasExpired(ctx *tx.ApplyContext, expiration *uint32) bool {
	if expiration == nil {
		return false
	}
	return *expiration <= ctx.Config.ParentCloseTime
}

// checkAcceptAsset validates that an account can receive an asset.
// Reference: rippled CreateOffer.cpp checkAcceptAsset() lines 227-312
func checkAcceptAsset(ctx *tx.ApplyContext, issuerID [20]byte, currency string, rules *amendment.Rules) tx.Result {
	// Read issuer account
	issuerKey := keylet.Account(issuerID)
	issuerData, err := ctx.View.Read(issuerKey)
	if err != nil || issuerData == nil {
		return tx.TecNO_ISSUER
	}

	issuerAccount, err := state.ParseAccountRoot(issuerData)
	if err != nil {
		return tx.TecNO_ISSUER
	}

	// If account is the issuer, always allowed
	// Reference: lines 254-256
	if rules.DepositPreauthEnabled() && ctx.AccountID == issuerID {
		return tx.TesSUCCESS
	}

	// Check RequireAuth flag on issuer
	// Reference: lines 258-282
	if (issuerAccount.Flags & state.LsfRequireAuth) != 0 {
		trustLineKey := keylet.Line(ctx.AccountID, issuerID, currency)
		trustLineData, err := ctx.View.Read(trustLineKey)
		if err != nil || trustLineData == nil {
			return tx.TecNO_LINE
		}

		rs, err := state.ParseRippleState(trustLineData)
		if err != nil {
			return tx.TecNO_LINE
		}

		// Check authorization based on canonical ordering
		canonicalGT := state.CompareAccountIDsForLine(ctx.AccountID, issuerID) > 0
		var isAuthorized bool
		if canonicalGT {
			isAuthorized = (rs.Flags & state.LsfLowAuth) != 0
		} else {
			isAuthorized = (rs.Flags & state.LsfHighAuth) != 0
		}

		if !isAuthorized {
			return tx.TecNO_AUTH
		}
	}

	// If account is issuer, always allowed (redundant check but matches rippled)
	// Reference: lines 288-291
	if ctx.AccountID == issuerID {
		return tx.TesSUCCESS
	}

	// Check for deep freeze on trustline
	// Reference: lines 293-309
	trustLineKey := keylet.Line(ctx.AccountID, issuerID, currency)
	trustLineData, err := ctx.View.Read(trustLineKey)
	if err != nil || trustLineData == nil {
		// No trustline = OK (will be created if needed)
		return tx.TesSUCCESS
	}

	rs, err := state.ParseRippleState(trustLineData)
	if err != nil {
		return tx.TesSUCCESS
	}

	// Check deep freeze
	deepFrozen := (rs.Flags & (state.LsfLowDeepFreeze | state.LsfHighDeepFreeze)) != 0
	if deepFrozen {
		return tx.TecFROZEN
	}

	return tx.TesSUCCESS
}

// accountInDomain checks if an account is a member of a permissioned domain.
// Reference: rippled app/misc/PermissionedDEXHelpers.cpp accountInDomain()
func accountInDomain(view tx.LedgerView, accountID [20]byte, domainID [32]byte, parentCloseTime uint32) bool {
	return permissioneddomain.AccountInDomain(view, accountID, domainID, parentCloseTime)
}

// applyTickSize applies tick size rounding to offer amounts.
// Reference: rippled CreateOffer.cpp lines 643-685
func applyTickSize(view tx.LedgerView, takerPays, takerGets tx.Amount, bSell bool, rules *amendment.Rules) (tx.Amount, tx.Amount) {
	// Get tick sizes from both issuers
	tickSize := maxTickSize

	if !takerPays.IsNative() {
		issuerTickSize := getTickSize(view, takerPays.Issuer)
		if issuerTickSize > 0 && issuerTickSize < tickSize {
			tickSize = issuerTickSize
		}
	}

	if !takerGets.IsNative() {
		issuerTickSize := getTickSize(view, takerGets.Issuer)
		if issuerTickSize > 0 && issuerTickSize < tickSize {
			tickSize = issuerTickSize
		}
	}

	// If no tick size applies, return unchanged
	if tickSize >= maxTickSize {
		return takerPays, takerGets
	}

	// Apply tick size rounding
	// Reference: lines 660-685
	quality := state.CalculateQuality(takerGets, takerPays)
	roundedQuality := roundToTickSize(quality, tickSize)

	if bSell {
		// Round TakerPays
		takerPays = multiplyByQuality(takerGets, roundedQuality, takerPays.Currency, takerPays.Issuer)
	} else {
		// Round TakerGets
		takerGets = divideByQuality(takerPays, roundedQuality, takerGets.Currency, takerGets.Issuer)
	}

	return takerPays, takerGets
}

// getTickSize returns the tick size for an issuer.
func getTickSize(view tx.LedgerView, issuerAddress string) uint8 {
	if issuerAddress == "" {
		return 0
	}

	issuerID, err := state.DecodeAccountID(issuerAddress)
	if err != nil {
		return 0
	}

	accountKey := keylet.Account(issuerID)
	data, err := view.Read(accountKey)
	if err != nil || data == nil {
		return 0
	}

	account, err := state.ParseAccountRoot(data)
	if err != nil {
		return 0
	}

	return account.TickSize
}

// roundToTickSize rounds a quality value to the specified tick size.
// Reference: rippled Quality.cpp round() function lines 182-212
// The tick size determines how many significant digits are kept in the mantissa.
// Quality is encoded as: (exponent << 56) | mantissa where mantissa is in [10^15, 10^16)
func roundToTickSize(quality uint64, tickSize uint8) uint64 {
	// If tick size is max or zero, no rounding needed
	if tickSize >= maxTickSize || tickSize == 0 {
		return quality
	}

	// Modulus for mantissa - determines rounding granularity
	// These are powers of 10 that determine rounding precision
	mod := []uint64{
		10000000000000000, // 0: 10^16 (no rounding)
		1000000000000000,  // 1: 10^15
		100000000000000,   // 2: 10^14
		10000000000000,    // 3: 10^13
		1000000000000,     // 4: 10^12
		100000000000,      // 5: 10^11
		10000000000,       // 6: 10^10
		1000000000,        // 7: 10^9
		100000000,         // 8: 10^8
		10000000,          // 9: 10^7
		1000000,           // 10: 10^6
		100000,            // 11: 10^5
		10000,             // 12: 10^4
		1000,              // 13: 10^3
		100,               // 14: 10^2
		10,                // 15: 10^1
		1,                 // 16: 10^0
	}

	// Extract exponent (top 8 bits) and mantissa (lower 56 bits)
	exponent := quality >> 56
	mantissa := quality & 0x00ffffffffffffff

	// Round up: add (mod-1) then truncate
	mantissa += mod[tickSize] - 1
	mantissa -= mantissa % mod[tickSize]

	// Reconstruct quality
	return (exponent << 56) | mantissa
}

// qualityToRate converts a quality value (encoded as (exponent << 56) | mantissa) to a big.Rat.
// Quality encoding: exponent is stored as (actual_exponent + 100) in the top 8 bits,
// mantissa is in the lower 56 bits and is in range [10^15, 10^16).
func qualityToRate(quality uint64) *big.Rat {
	if quality == 0 {
		return new(big.Rat).SetInt64(0)
	}

	// Extract exponent (top 8 bits) and mantissa (lower 56 bits)
	storedExponent := quality >> 56
	mantissa := quality & 0x00ffffffffffffff

	// Actual exponent = storedExponent - 100
	actualExponent := int(storedExponent) - 100

	// Rate = mantissa * 10^actualExponent
	rat := new(big.Rat).SetInt64(int64(mantissa))

	if actualExponent > 0 {
		// Multiply by 10^actualExponent
		scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(actualExponent)), nil)
		rat.Mul(rat, new(big.Rat).SetInt(scale))
	} else if actualExponent < 0 {
		// Divide by 10^(-actualExponent)
		scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(-actualExponent)), nil)
		rat.Quo(rat, new(big.Rat).SetInt(scale))
	}

	return rat
}

// multiplyByQuality multiplies an amount by a quality rate.
// Reference: rippled uses mulRound to multiply amount by quality rate.
// The result type is determined by currency/issuer parameters.
func multiplyByQuality(amount tx.Amount, quality uint64, currency, issuer string) tx.Amount {
	if quality == 0 || amount.IsZero() {
		if currency == "" || currency == "XRP" {
			return tx.NewXRPAmount(0)
		}
		return tx.NewIssuedAmount(0, -100, currency, issuer)
	}

	// Convert amount to big.Rat
	var amtRat *big.Rat
	if amount.IsNative() {
		amtRat = new(big.Rat).SetInt64(amount.Drops())
	} else {
		mantissa := amount.Mantissa()
		exponent := amount.Exponent()
		amtRat = new(big.Rat).SetInt64(mantissa)
		if exponent > 0 {
			scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(exponent)), nil)
			amtRat.Mul(amtRat, new(big.Rat).SetInt(scale))
		} else if exponent < 0 {
			scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(-exponent)), nil)
			amtRat.Quo(amtRat, new(big.Rat).SetInt(scale))
		}
	}

	// Get quality as rate
	rateRat := qualityToRate(quality)

	// Result = amount * rate
	result := new(big.Rat).Mul(amtRat, rateRat)

	// Convert result to the target type
	if currency == "" || currency == "XRP" {
		// Return XRP amount
		f, _ := result.Float64()
		return tx.NewXRPAmount(int64(f + 0.5)) // Round
	}

	// Return IOU amount - normalize to mantissa/exponent form
	return ratToIssuedAmount(result, currency, issuer, true)
}

// divideByQuality divides an amount by a quality rate.
// Reference: rippled uses divRound to divide amount by quality rate.
// The result type is determined by currency/issuer parameters.
func divideByQuality(amount tx.Amount, quality uint64, currency, issuer string) tx.Amount {
	if quality == 0 {
		// Division by zero - return zero
		if currency == "" || currency == "XRP" {
			return tx.NewXRPAmount(0)
		}
		return tx.NewIssuedAmount(0, -100, currency, issuer)
	}

	if amount.IsZero() {
		if currency == "" || currency == "XRP" {
			return tx.NewXRPAmount(0)
		}
		return tx.NewIssuedAmount(0, -100, currency, issuer)
	}

	// Convert amount to big.Rat
	var amtRat *big.Rat
	if amount.IsNative() {
		amtRat = new(big.Rat).SetInt64(amount.Drops())
	} else {
		mantissa := amount.Mantissa()
		exponent := amount.Exponent()
		amtRat = new(big.Rat).SetInt64(mantissa)
		if exponent > 0 {
			scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(exponent)), nil)
			amtRat.Mul(amtRat, new(big.Rat).SetInt(scale))
		} else if exponent < 0 {
			scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(-exponent)), nil)
			amtRat.Quo(amtRat, new(big.Rat).SetInt(scale))
		}
	}

	// Get quality as rate
	rateRat := qualityToRate(quality)

	// Result = amount / rate
	result := new(big.Rat).Quo(amtRat, rateRat)

	// Convert result to the target type
	if currency == "" || currency == "XRP" {
		// Return XRP amount
		f, _ := result.Float64()
		return tx.NewXRPAmount(int64(f + 0.5)) // Round
	}

	// Return IOU amount - normalize to mantissa/exponent form
	return ratToIssuedAmount(result, currency, issuer, true)
}

// ratToIssuedAmount converts a big.Rat to an IOU Amount with the given currency and issuer.
// The roundUp parameter controls rounding direction.
func ratToIssuedAmount(rat *big.Rat, currency, issuer string, roundUp bool) tx.Amount {
	if rat.Sign() == 0 {
		return tx.NewIssuedAmount(0, -100, currency, issuer)
	}

	// Handle sign
	negative := rat.Sign() < 0
	if negative {
		rat = new(big.Rat).Neg(rat)
	}

	// Normalize to mantissa in [10^15, 10^16)
	minMant := big.NewInt(1000000000000000)  // 10^15
	maxMant := big.NewInt(10000000000000000) // 10^16

	exponent := 0
	scaled := new(big.Rat).Set(rat)

	for {
		intPart := new(big.Int).Quo(scaled.Num(), scaled.Denom())
		if intPart.Cmp(maxMant) >= 0 {
			// Too large, divide by 10
			scaled.Quo(scaled, big.NewRat(10, 1))
			exponent++
		} else if intPart.Cmp(minMant) < 0 {
			// Too small, multiply by 10
			if scaled.Sign() == 0 {
				break
			}
			scaled.Mul(scaled, big.NewRat(10, 1))
			exponent--
		} else {
			break
		}
		// Safety limit
		if exponent > 80 || exponent < -96 {
			break
		}
	}

	// Get final mantissa with rounding
	intPart := new(big.Int).Quo(scaled.Num(), scaled.Denom())
	remainder := new(big.Int).Mod(scaled.Num(), scaled.Denom())

	if roundUp && remainder.Sign() != 0 {
		intPart.Add(intPart, big.NewInt(1))
	}

	mantissa := intPart.Int64()
	if negative {
		mantissa = -mantissa
	}

	return tx.NewIssuedAmount(mantissa, exponent, currency, issuer)
}

// peekOffer reads an offer from the ledger without modifying it.
func peekOffer(view tx.LedgerView, accountID [20]byte, sequence uint32) *state.LedgerOffer {
	offerKey := keylet.Offer(accountID, sequence)
	data, err := view.Read(offerKey)
	if err != nil || data == nil {
		return nil
	}

	offer, err := state.ParseLedgerOffer(data)
	if err != nil {
		return nil
	}

	return offer
}

// offerDeleteInView removes an offer from the given view without modifying account state.
// This is used by the two-sandbox pattern to delete offers in both sandboxes.
func offerDeleteInView(view tx.LedgerView, offer *state.LedgerOffer) tx.Result {
	// Get offer key
	accountID, err := state.DecodeAccountID(offer.Account)
	if err != nil {
		return tx.TefINTERNAL
	}
	offerKey := keylet.Offer(accountID, offer.Sequence)

	// Remove from owner directory
	ownerDirKey := keylet.OwnerDir(accountID)
	_, err = state.DirRemove(view, ownerDirKey, offer.OwnerNode, offerKey.Key, false)
	if err != nil {
		return tx.TefINTERNAL
	}

	// Remove from book directory
	bookDirKey := keylet.Keylet{Type: 100, Key: offer.BookDirectory}
	_, err = state.DirRemove(view, bookDirKey, offer.BookNode, offerKey.Key, false)
	if err != nil {
		return tx.TefINTERNAL
	}

	// Delete the offer
	if err := view.Erase(offerKey); err != nil {
		return tx.TefINTERNAL
	}

	return tx.TesSUCCESS
}

// adjustOwnerCountInView adjusts an account's OwnerCount directly through the view.
// Used for offer deletion when the offer owner is NOT ctx.Account (e.g., self-crossed
// offers or unfunded offers from other accounts removed during crossing).
// Reference: rippled adjustOwnerCount() in View.cpp
func adjustOwnerCountInView(view tx.LedgerView, accountID [20]byte, delta int) {
	_ = tx.AdjustOwnerCount(view, accountID, delta)
}

// getOfferSequence returns the sequence number to use for a new offer.
// Reference: rippled CreateOffer.cpp - uses transaction's Sequence or TicketSequence
func (o *OfferCreate) getOfferSequence() uint32 {
	// Use the transaction's Sequence field directly
	// If TicketSequence is used, that becomes the offer's sequence
	if o.TicketSequence != nil {
		return *o.TicketSequence
	}
	if o.Sequence != nil {
		return *o.Sequence
	}
	return 0
}

// parseFee extracts the fee from the transaction context.
func parseFee(ctx *tx.ApplyContext) uint64 {
	// The fee is already deducted in the engine before Apply is called
	// Return a reasonable default for reserve calculations
	return ctx.Config.BaseFee
}

// applyHybridInSandbox handles hybrid offer placement in a specific view/sandbox.
// Reference: rippled CreateOffer.cpp applyHybrid() lines 528-573
func applyHybridInSandbox(view tx.LedgerView, ctx *tx.ApplyContext, offer *state.LedgerOffer, offerKey keylet.Keylet, takerPays, takerGets tx.Amount, domainBookDir keylet.Keylet) tx.Result {
	// Set hybrid flag
	offer.Flags |= lsfHybrid

	// Also place in open book (without domain)
	takerPaysCurrency := state.GetCurrencyBytes(takerPays.Currency)
	takerPaysIssuer := state.GetIssuerBytes(takerPays.Issuer)
	takerGetsCurrency := state.GetCurrencyBytes(takerGets.Currency)
	takerGetsIssuer := state.GetIssuerBytes(takerGets.Issuer)

	uRate := state.GetRate(takerGets, takerPays)

	bookBase := keylet.BookDir(takerPaysCurrency, takerPaysIssuer, takerGetsCurrency, takerGetsIssuer)
	openBookDirKey := keylet.Quality(bookBase, uRate)

	// Add to open book directory
	bookDirResult, err := state.DirInsert(view, openBookDirKey, offerKey.Key, func(dir *state.DirectoryNode) {
		dir.TakerPaysCurrency = takerPaysCurrency
		dir.TakerPaysIssuer = takerPaysIssuer
		dir.TakerGetsCurrency = takerGetsCurrency
		dir.TakerGetsIssuer = takerGetsIssuer
		dir.ExchangeRate = uRate
		// No DomainID for open book
	})
	if err != nil {
		return tx.TefINTERNAL
	}

	// Store additional book info in offer
	offer.AdditionalBookDirectory = openBookDirKey.Key
	offer.AdditionalBookNode = bookDirResult.Page

	return tx.TesSUCCESS
}

// lsfHybrid is the ledger flag for hybrid offers
const lsfHybrid uint32 = 0x00040000
