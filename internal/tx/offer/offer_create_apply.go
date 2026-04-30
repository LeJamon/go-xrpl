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
	result := tx.TesSUCCESS

	// Process cancellation request if specified
	// Reference: lines 608-621
	// CRITICAL: Offer cancellation must happen in BOTH sandboxes
	if o.OfferSequence != nil {
		sleCancel := peekOffer(ctx.View, ctx.AccountID, *o.OfferSequence)
		if sleCancel != nil {
			result = offerDeleteInView(sb, sleCancel)
			// Delete in cancel sandbox (same operation)
			_ = offerDeleteInView(sbCancel, sleCancel)

			// Also update owner count (once, since we'll only apply one sandbox)
			if result == tx.TesSUCCESS && ctx.Account.OwnerCount > 0 {
				ctx.Account.OwnerCount--
			}
		}
	}

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
			rules.Enabled(amendment.FeatureFix1781),
			o.DomainID, // Domain ID for permissioned DEX offer crossing
		)

		// Convert result amounts back.
		// Reference: rippled CreateOffer.cpp flowCross() result handling
		grossPaid := payment.FromEitherAmount(crossResult.TakerPaid)
		placeOffer.in = payment.FromEitherAmount(crossResult.TakerPaidNet)
		placeOffer.out = payment.FromEitherAmount(crossResult.TakerGot)

		result = crossResult.Result
		ctx.Log.Trace("offer crossing done",
			"result", result,
			"takerPaid", placeOffer.in,
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

		// Reference: line 744-745
		// Use isAmountZeroOrNegative because FromEitherAmount returns "0" for zero amounts,
		// not empty string ""
		if !isAmountZeroOrNegative(placeOffer.in) || !isAmountZeroOrNegative(placeOffer.out) {
			crossed = true
		}

		// Calculate remaining amounts for the new offer.
		// Reference: rippled CreateOffer.cpp lines 429-504 (flowCross afterCross calculation)
		//
		// Rippled's approach:
		// 1. If takerInBalance <= 0: offer fully consumed (funds exhausted)
		// 2. For sell: subtract NET input from TakerGets, compute TakerPays via quality
		// 3. For non-sell: subtract output received from TakerPays, compute TakerGets via quality
		//
		// The quality rate = Quality{takerAmount.out, takerAmount.in}.rate()
		// where out=TakerPays, in=TakerGets (from taker's perspective).
		var remainingGets, remainingPays tx.Amount

		noCrossingHappened := isAmountZeroOrNegative(placeOffer.in) && isAmountZeroOrNegative(placeOffer.out)

		if isAmountZeroOrNegative(takerInBalance) {
			// Funds exhausted during crossing — no remaining offer
			// Reference: rippled CreateOffer.cpp lines 435-441
			remainingGets = zeroAmount(saTakerGets)
			remainingPays = zeroAmount(saTakerPays)
		} else if noCrossingHappened {
			// No crossing happened - return original amounts directly
			// Reference: rippled CreateOffer.cpp line 429: afterCross = takerAmount (unchanged)
			remainingGets = saTakerGets
			remainingPays = saTakerPays
		} else if bSell {
			// Sell offer: subtract NET input from TakerGets, compute TakerPays by quality
			// Reference: rippled CreateOffer.cpp lines 447-489
			//   nonGatewayAmountIn = divideRound(actualAmountIn, gatewayXferRate, ...)
			//   afterCross.in -= nonGatewayAmountIn
			//   afterCross.out = divRound(afterCross.in, rate, ...) or divRoundStrict
			remainingGets = subtractAmounts(saTakerGets, placeOffer.in) // placeOffer.in is NET
			if isAmountNegative(remainingGets) {
				remainingGets = zeroAmount(saTakerGets)
			}
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
			// Non-sell offer: subtract output received from TakerPays, compute TakerGets by quality
			// Reference: rippled CreateOffer.cpp lines 491-503
			//   afterCross.out -= result.actualAmountOut
			//   afterCross.in = mulRound(afterCross.out, rate, takerAmount.in.issue(), true)
			remainingPays = subtractAmounts(saTakerPays, placeOffer.out)
			if isAmountNegative(remainingPays) {
				remainingPays = zeroAmount(saTakerPays)
			}
			rate := payment.QualityFromAmounts(
				payment.ToEitherAmount(saTakerGets),
				payment.ToEitherAmount(saTakerPays),
			).Rate()
			outNative := saTakerGets.IsNative()
			outCurrency := saTakerGets.Currency
			outIssuer := saTakerGets.Issuer
			remainingGets = offerMulRound(remainingPays, rate, outNative, outCurrency, outIssuer, true)
		}

		// Reference: rippled CreateOffer.cpp lines 757-761
		fullyCrossed := isAmountZeroOrNegative(remainingGets) || isAmountZeroOrNegative(remainingPays)

		// Without fixFillOrKill, FoK requires TakerGets to be fully consumed
		// (GROSS paid >= original TakerGets), not just remaining being zero.
		// The proportional remaining calculation can yield zero even when TakerGets
		// isn't fully consumed (because TakerPays was fully satisfied at a better rate).
		// Reference: rippled CreateOffer.cpp: pre-amendment requires full TakerGets
		// consumption for FoK; post-amendment relaxes non-sell FoK.
		// Note: goXRPL uses partialPayment=true for FlowCross (unlike rippled which
		// passes partialPayment=!(txFlags & tfFillOrKill)), so FoK handling is manual.
		if fullyCrossed && bFillOrKill && !rules.Enabled(amendment.FeatureFixFillOrKill) {
			remainingWithGross := subtractAmounts(saTakerGets, grossPaid)
			if !isAmountZeroOrNegative(remainingWithGross) {
				// FoK not satisfied: TakerGets not fully consumed by GROSS amount.
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

	// Reference: lines 839-848
	ownerDirKey := keylet.OwnerDir(ctx.AccountID)
	ownerDirResult, err := state.DirInsert(sb, ownerDirKey, offerKey.Key, func(dir *state.DirectoryNode) {
		dir.Owner = ctx.AccountID
	})
	if err != nil {
		return tx.TefINTERNAL, false
	}

	// Reference: line 851
	ctx.Account.OwnerCount++

	// Check if book exists (for OrderBookDB tracking)
	bookExisted, _ := sb.Exists(bookDirKey)

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

	// Reference: line 903-904
	if o.Expiration != nil {
		ledgerOffer.Expiration = *o.Expiration
	}

	// Reference: lines 905-910
	if bPassive {
		ledgerOffer.Flags |= lsfOfferPassive
	}
	if bSell {
		ledgerOffer.Flags |= lsfOfferSell
	}

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
