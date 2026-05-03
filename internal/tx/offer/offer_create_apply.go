package offer

import (
	"github.com/LeJamon/goXRPLd/amendment"
	"github.com/LeJamon/goXRPLd/internal/ledger/state"
	"github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/internal/tx/payment"
	"github.com/LeJamon/goXRPLd/keylet"
)

// lsfHybrid is the ledger flag for hybrid offers
const lsfHybrid uint32 = 0x00040000

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

	// Process cancellation request if specified
	// Reference: lines 608-621
	// CRITICAL: Offer cancellation must happen in BOTH sandboxes
	result := o.processCancelRequest(ctx, sb, sbCancel)

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
		outcome := o.takerCross(ctx, sb, sbCancel, saTakerPays, saTakerGets, uRate, bPassive, bSell, bFillOrKill)
		if outcome.terminated {
			return outcome.result, outcome.applyMain
		}
		saTakerPays = outcome.saTakerPays
		saTakerGets = outcome.saTakerGets
		uRate = outcome.uRate
		crossed = outcome.crossed
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

	return o.placeRemainingOffer(ctx, sb, saTakerPays, saTakerGets, uRate, bPassive, bSell, bHybrid)
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
	accountID, err := state.DecodeAccountID(offer.Account)
	if err != nil {
		return tx.TefINTERNAL
	}
	offerKey := keylet.Offer(accountID, offer.Sequence)

	ownerDirKey := keylet.OwnerDir(accountID)
	_, err = state.DirRemove(view, ownerDirKey, offer.OwnerNode, offerKey.Key, false)
	if err != nil {
		return tx.TefINTERNAL
	}

	bookDirKey := keylet.Keylet{Type: 100, Key: offer.BookDirectory}
	_, err = state.DirRemove(view, bookDirKey, offer.BookNode, offerKey.Key, false)
	if err != nil {
		return tx.TefINTERNAL
	}

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
	offer.Flags |= lsfHybrid

	// Also place in open book (without domain)
	takerPaysCurrency := state.GetCurrencyBytes(takerPays.Currency)
	takerPaysIssuer := state.GetIssuerBytes(takerPays.Issuer)
	takerGetsCurrency := state.GetCurrencyBytes(takerGets.Currency)
	takerGetsIssuer := state.GetIssuerBytes(takerGets.Issuer)

	uRate := state.GetRate(takerGets, takerPays)

	bookBase := keylet.BookDir(takerPaysCurrency, takerPaysIssuer, takerGetsCurrency, takerGetsIssuer)
	openBookDirKey := keylet.Quality(bookBase, uRate)

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

	offer.AdditionalBookDirectory = openBookDirKey.Key
	offer.AdditionalBookNode = bookDirResult.Page

	return tx.TesSUCCESS
}
