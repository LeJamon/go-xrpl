package nftoken

import (
	"github.com/LeJamon/goXRPLd/amendment"
	"github.com/LeJamon/goXRPLd/internal/ledger/state"
	"github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/keylet"
)

// ---------------------------------------------------------------------------
// Buyer reserve check (fixNFTokenReserve)
// ---------------------------------------------------------------------------

// xrpAvailable returns the available XRP for an account (balance - reserve).
// Matches rippled's accountFunds/xrpLiquid for native XRP.
func xrpAvailable(balance uint64, ownerCount uint32, cfg *tx.ApplyContext) uint64 {
	reserve := cfg.Config.ReserveBase + uint64(ownerCount)*cfg.Config.ReserveIncrement
	if balance > reserve {
		return balance - reserve
	}
	return 0
}

// checkBuyerReserve checks if the buyer has sufficient reserve after receiving
// an NFToken. Only applies when fixNFTokenReserve amendment is enabled.
// Reference: rippled NFTokenAcceptOffer.cpp transferNFToken() lines 457-474
func checkBuyerReserve(ctx *tx.ApplyContext, buyerID [20]byte, pagesCreated int) tx.Result {
	if !ctx.Rules().Enabled(amendment.FeatureFixNFTokenReserve) {
		return tx.TesSUCCESS
	}
	if pagesCreated <= 0 {
		return tx.TesSUCCESS
	}

	// Read buyer's current state to check balance vs reserve
	var buyerBalance uint64
	var buyerOwnerCount uint32
	if buyerID == ctx.AccountID {
		buyerBalance = ctx.Account.Balance
		buyerOwnerCount = ctx.Account.OwnerCount
	} else {
		buyerKey := keylet.Account(buyerID)
		buyerData, err := ctx.View.Read(buyerKey)
		if err != nil || buyerData == nil {
			return tx.TefINTERNAL
		}
		buyerAccount, err := state.ParseAccountRoot(buyerData)
		if err != nil {
			return tx.TefINTERNAL
		}
		buyerBalance = buyerAccount.Balance
		buyerOwnerCount = buyerAccount.OwnerCount
	}

	reserve := ctx.Config.ReserveBase + uint64(buyerOwnerCount)*ctx.Config.ReserveIncrement
	if buyerBalance < reserve {
		return tx.TecINSUFFICIENT_RESERVE
	}
	return tx.TesSUCCESS
}

// syncCtxOwnerCount re-reads the submitter's OwnerCount from the view and
// applies any delta to ctx.Account.OwnerCount. This is needed because the
// engine writes ctx.Account back to the view after Apply(), overwriting
// view-based changes. When IOU payments auto-create trust lines for the
// submitter via adjustOwnerCountViaView, those changes are lost unless
// synced back to ctx.Account.
// Reference: rippled stores the account SLE in the view, so changes are
// automatic. goXRPL uses a separate ctx.Account copy.
func syncCtxOwnerCount(ctx *tx.ApplyContext) {
	acctKey := keylet.Account(ctx.AccountID)
	acctData, err := ctx.View.Read(acctKey)
	if err != nil || acctData == nil {
		return
	}
	acct, err := state.ParseAccountRoot(acctData)
	if err != nil {
		return
	}
	ctx.Account.OwnerCount = acct.OwnerCount
}

// ---------------------------------------------------------------------------
// Brokered mode and direct offer acceptance helpers
// ---------------------------------------------------------------------------

// executeBrokeredMode handles the doApply part of brokered NFToken sales.
// All preclaim checks have already been done in Apply().
// Reference: rippled NFTokenAcceptOffer.cpp doApply (brokered mode)
func (n *NFTokenAcceptOffer) executeBrokeredMode(ctx *tx.ApplyContext, accountID [20]byte,
	buyOffer, sellOffer *state.NFTokenOfferData, buyOfferKey, sellOfferKey keylet.Keylet,
	buyOfferNegative, sellOfferNegative bool) tx.Result {

	sellerID := sellOffer.Owner
	buyerID := buyOffer.Owner

	// --- Preclaim-style funds check BEFORE any state changes ---
	// Reference: rippled preclaim uses accountFunds (considers reserve) BEFORE doApply
	if !(buyOfferNegative || sellOfferNegative) && buyOffer.AmountIOU == nil {
		buyerKey := keylet.Account(buyerID)
		buyerData, err := ctx.View.Read(buyerKey)
		if err != nil {
			return tx.TefINTERNAL
		}
		buyerAccount, err := state.ParseAccountRoot(buyerData)
		if err != nil {
			return tx.TefINTERNAL
		}
		available := xrpAvailable(buyerAccount.Balance, buyerAccount.OwnerCount, ctx)
		if available < buyOffer.Amount {
			return tx.TecINSUFFICIENT_FUNDS
		}
	}

	// Delete both offers FIRST, matching rippled's doApply order.
	// Reference: rippled NFTokenAcceptOffer.cpp doApply() lines 527-539
	deleteTokenOffer(ctx.View, buyOfferKey)
	deleteTokenOffer(ctx.View, sellOfferKey)
	adjustOwnerCountViaView(ctx.View, buyerID, -1)
	adjustOwnerCountViaView(ctx.View, sellerID, -1)

	// When offers have negative amounts (pre-fixNFTokenNegOffer), rippled's
	// brokered path skips payments because `amount > beast::zero` is false.
	// Only the token transfer and offer cleanup happen.
	// Reference: rippled NFTokenAcceptOffer.cpp doApply lines 593-597
	if !(buyOfferNegative || sellOfferNegative) {
		buyIsXRP := buyOffer.AmountIOU == nil

		var brokerFee uint64
		var brokerFeeIOU tx.Amount
		if n.NFTokenBrokerFee != nil {
			if buyIsXRP {
				brokerFee = uint64(n.NFTokenBrokerFee.Drops())
			} else {
				brokerFeeIOU = *n.NFTokenBrokerFee
			}
		}

		transferFee := getNFTTransferFee(sellOffer.NFTokenID)
		nftIssuerID := getNFTIssuer(sellOffer.NFTokenID)

		if !buyIsXRP {
			// IOU brokered payment path
			buyAmount := offerIOUToAmount(buyOffer)

			// Step 1: Pay broker fee
			if n.NFTokenBrokerFee != nil && !brokerFeeIOU.IsZero() {
				if r := payIOU(ctx, buyerID, accountID, brokerFeeIOU); r != tx.TesSUCCESS {
					return r
				}
				buyAmount, _ = buyAmount.Sub(brokerFeeIOU)
			}

			// Step 2: Pay issuer cut from transfer fee
			if transferFee != 0 && !buyAmount.IsZero() && sellerID != nftIssuerID && buyerID != nftIssuerID {
				// Check issuer trust line (fixEnforceNFTokenTrustline)
				nftFlags := getNFTFlagsFromID(sellOffer.NFTokenID)
				if r := checkIssuerTrustLineForAccept(ctx, nftIssuerID, buyAmount, nftFlags); r != tx.TesSUCCESS {
					return r
				}
				issuerCut := buyAmount.MulRatio(uint32(transferFee), transferFeeDivisor32, true)
				if !issuerCut.IsZero() {
					if r := payIOU(ctx, buyerID, nftIssuerID, issuerCut); r != tx.TesSUCCESS {
						return r
					}
					buyAmount, _ = buyAmount.Sub(issuerCut)
				}
			}

			// Step 3: Pay seller remainder
			if !buyAmount.IsZero() {
				if r := payIOU(ctx, buyerID, sellerID, buyAmount); r != tx.TesSUCCESS {
					return r
				}
			}

			// Sync ctx.Account.OwnerCount after IOU payments that may auto-create trust lines
			syncCtxOwnerCount(ctx)
		} else {
			// XRP brokered payment path — deduct from buyer, pay broker + issuer + seller
			amount := buyOffer.Amount

			// Deduct full amount from buyer's account (funds already checked above)
			buyerKey := keylet.Account(buyerID)
			buyerData, err := ctx.View.Read(buyerKey)
			if err != nil {
				return tx.TefINTERNAL
			}
			buyerAccount, err := state.ParseAccountRoot(buyerData)
			if err != nil {
				return tx.TefINTERNAL
			}
			buyerAccount.Balance -= amount
			buyerUpdated, _ := state.SerializeAccountRoot(buyerAccount)
			if err := ctx.View.Update(buyerKey, buyerUpdated); err != nil {
				return tx.TefINTERNAL
			}

			var issuerCut uint64
			if transferFee != 0 && amount > 0 {
				issuerCut = nftTransferFeeXRP(amount-brokerFee, transferFee)
				if sellerID == nftIssuerID || buyerID == nftIssuerID {
					issuerCut = 0
				}
			}

			// Pay broker fee
			if brokerFee > 0 {
				ctx.Account.Balance += brokerFee
				amount -= brokerFee
			}

			// Pay issuer cut
			if issuerCut > 0 {
				issuerKey := keylet.Account(nftIssuerID)
				issuerData, err := ctx.View.Read(issuerKey)
				if err == nil {
					issuerAccount, err := state.ParseAccountRoot(issuerData)
					if err == nil {
						issuerAccount.Balance += issuerCut
						issuerUpdatedData, _ := state.SerializeAccountRoot(issuerAccount)
						ctx.View.Update(issuerKey, issuerUpdatedData)
					}
				}
				amount -= issuerCut
			}

			// Pay seller
			sellerKey := keylet.Account(sellerID)
			sellerData, err := ctx.View.Read(sellerKey)
			if err != nil {
				return tx.TefINTERNAL
			}
			sellerAccount, err := state.ParseAccountRoot(sellerData)
			if err != nil {
				return tx.TefINTERNAL
			}
			sellerAccount.Balance += amount
			sellerUpdatedData, err := state.SerializeAccountRoot(sellerAccount)
			if err != nil {
				return tx.TefINTERNAL
			}
			if err := ctx.View.Update(sellerKey, sellerUpdatedData); err != nil {
				return tx.TefINTERNAL
			}
		}
	}

	// Transfer the NFToken from seller to buyer
	fixPageLinks := ctx.Rules().Enabled(amendment.FeatureFixNFTokenPageLinks)
	fixDirV1 := ctx.Rules().Enabled(amendment.FeatureFixNFTokenDirV1)
	xferResult := transferNFToken(sellerID, buyerID, sellOffer.NFTokenID, ctx.View, fixPageLinks, fixDirV1)
	if xferResult.Result != tx.TesSUCCESS {
		return xferResult.Result
	}

	// Adjust OwnerCount for page changes from the transfer.
	adjustOwnerCountViaView(ctx.View, sellerID, -xferResult.FromPagesRemoved)
	adjustOwnerCountViaView(ctx.View, buyerID, xferResult.ToPagesCreated)

	// Check buyer reserve (fixNFTokenReserve)
	if r := checkBuyerReserve(ctx, buyerID, xferResult.ToPagesCreated); r != tx.TesSUCCESS {
		return r
	}

	return tx.TesSUCCESS
}

// acceptNFTokenSellOfferDirect handles direct sell offer acceptance
func (n *NFTokenAcceptOffer) acceptNFTokenSellOfferDirect(ctx *tx.ApplyContext, accountID [20]byte,
	sellOffer *state.NFTokenOfferData, sellOfferKey keylet.Keylet) tx.Result {
	if sellOffer.HasDestination && sellOffer.Destination != accountID {
		return tx.TecNO_PERMISSION
	}

	// Verify seller owns the token
	sellerID := sellOffer.Owner
	if _, _, _, found := findToken(ctx.View, sellerID, sellOffer.NFTokenID); !found {
		return tx.TecNO_PERMISSION
	}

	transferFee := getNFTTransferFee(sellOffer.NFTokenID)
	nftIssuerID := getNFTIssuer(sellOffer.NFTokenID)

	// --- Preclaim-style funds check BEFORE any state changes ---
	// Reference: rippled preclaim uses accountFunds (considers reserve) BEFORE doApply
	if sellOffer.AmountIOU == nil {
		available := xrpAvailable(ctx.Account.Balance, ctx.Account.OwnerCount, ctx)
		if available < sellOffer.Amount {
			return tx.TecINSUFFICIENT_FUNDS
		}
	}

	// Delete offer FIRST, matching rippled's doApply order.
	// Offer data is already parsed into sellOffer struct.
	deleteTokenOffer(ctx.View, sellOfferKey)
	adjustOwnerCountViaView(ctx.View, sellerID, -1)

	if sellOffer.AmountIOU != nil {
		// IOU payment path
		sellAmount := offerIOUToAmount(sellOffer)

		// Calculate issuer cut for transfer fee
		if transferFee != 0 && !sellAmount.IsZero() && sellerID != nftIssuerID && accountID != nftIssuerID {
			// Check issuer trust line (fixEnforceNFTokenTrustline)
			nftFlags := getNFTFlagsFromID(sellOffer.NFTokenID)
			if r := checkIssuerTrustLineForAccept(ctx, nftIssuerID, sellAmount, nftFlags); r != tx.TesSUCCESS {
				return r
			}
			issuerCut := sellAmount.MulRatio(uint32(transferFee), transferFeeDivisor32, true)
			if !issuerCut.IsZero() {
				if r := payIOU(ctx, accountID, nftIssuerID, issuerCut); r != tx.TesSUCCESS {
					return r
				}
				sellAmount, _ = sellAmount.Sub(issuerCut)
			}
		}

		// Pay seller remainder
		if !sellAmount.IsZero() {
			if r := payIOU(ctx, accountID, sellerID, sellAmount); r != tx.TesSUCCESS {
				return r
			}
		}

		// Sync ctx.Account.OwnerCount after IOU payments that may auto-create trust lines
		syncCtxOwnerCount(ctx)
	} else {
		// XRP payment path
		amount := sellOffer.Amount
		var issuerCut uint64

		if transferFee != 0 && amount > 0 {
			issuerCut = nftTransferFeeXRP(amount, transferFee)
			if sellerID == nftIssuerID || accountID == nftIssuerID {
				issuerCut = 0
			}
		}

		totalCost := amount
		ctx.Account.Balance -= totalCost

		if issuerCut > 0 {
			issuerKey := keylet.Account(nftIssuerID)
			issuerData, err := ctx.View.Read(issuerKey)
			if err == nil {
				issuerAccount, err := state.ParseAccountRoot(issuerData)
				if err == nil {
					issuerAccount.Balance += issuerCut
					issuerUpdatedData, _ := state.SerializeAccountRoot(issuerAccount)
					ctx.View.Update(issuerKey, issuerUpdatedData)
				}
			}
			amount -= issuerCut
		}

		sellerKey := keylet.Account(sellerID)
		sellerData, err := ctx.View.Read(sellerKey)
		if err != nil {
			return tx.TefINTERNAL
		}
		sellerAccount, err := state.ParseAccountRoot(sellerData)
		if err != nil {
			return tx.TefINTERNAL
		}
		sellerAccount.Balance += amount
		sellerUpdatedData, err := state.SerializeAccountRoot(sellerAccount)
		if err != nil {
			return tx.TefINTERNAL
		}
		if err := ctx.View.Update(sellerKey, sellerUpdatedData); err != nil {
			return tx.TefINTERNAL
		}
	}

	// Transfer the NFToken
	fixPageLinks := ctx.Rules().Enabled(amendment.FeatureFixNFTokenPageLinks)
	fixDirV1 := ctx.Rules().Enabled(amendment.FeatureFixNFTokenDirV1)
	xferResult := transferNFToken(sellerID, accountID, sellOffer.NFTokenID, ctx.View, fixPageLinks, fixDirV1)
	if xferResult.Result != tx.TesSUCCESS {
		return xferResult.Result
	}

	// Adjust OwnerCount for page changes from the transfer.
	adjustOwnerCountViaView(ctx.View, sellerID, -xferResult.FromPagesRemoved)
	ctx.Account.OwnerCount += uint32(xferResult.ToPagesCreated)

	// Check buyer reserve (fixNFTokenReserve) — buyer is ctx.Account
	if r := checkBuyerReserve(ctx, accountID, xferResult.ToPagesCreated); r != tx.TesSUCCESS {
		return r
	}

	return tx.TesSUCCESS
}

// acceptNFTokenBuyOfferDirect handles direct buy offer acceptance
func (n *NFTokenAcceptOffer) acceptNFTokenBuyOfferDirect(ctx *tx.ApplyContext, accountID [20]byte,
	buyOffer *state.NFTokenOfferData, buyOfferKey keylet.Keylet) tx.Result {
	// Verify account owns the token
	if _, _, _, found := findToken(ctx.View, accountID, buyOffer.NFTokenID); !found {
		return tx.TecNO_PERMISSION
	}

	if buyOffer.HasDestination && buyOffer.Destination != accountID {
		return tx.TecNO_PERMISSION
	}

	buyerID := buyOffer.Owner
	transferFee := getNFTTransferFee(buyOffer.NFTokenID)
	nftIssuerID := getNFTIssuer(buyOffer.NFTokenID)

	// --- Preclaim-style funds check BEFORE any state changes ---
	// Reference: rippled preclaim uses accountFunds (considers reserve) BEFORE doApply
	if buyOffer.AmountIOU == nil {
		buyerKey := keylet.Account(buyerID)
		buyerData, err := ctx.View.Read(buyerKey)
		if err != nil {
			return tx.TefINTERNAL
		}
		buyerAccount, err := state.ParseAccountRoot(buyerData)
		if err != nil {
			return tx.TefINTERNAL
		}
		available := xrpAvailable(buyerAccount.Balance, buyerAccount.OwnerCount, ctx)
		if available < buyOffer.Amount {
			return tx.TecINSUFFICIENT_FUNDS
		}
	}

	// Delete offer FIRST, matching rippled's doApply order.
	// Offer data is already parsed into buyOffer struct.
	deleteTokenOffer(ctx.View, buyOfferKey)
	adjustOwnerCountViaView(ctx.View, buyerID, -1)

	if buyOffer.AmountIOU != nil {
		// IOU payment path: buyer pays seller via trust lines
		buyAmount := offerIOUToAmount(buyOffer)

		// Calculate issuer cut for transfer fee
		if transferFee != 0 && !buyAmount.IsZero() && accountID != nftIssuerID && buyerID != nftIssuerID {
			// Check issuer trust line (fixEnforceNFTokenTrustline)
			nftFlags := getNFTFlagsFromID(buyOffer.NFTokenID)
			if r := checkIssuerTrustLineForAccept(ctx, nftIssuerID, buyAmount, nftFlags); r != tx.TesSUCCESS {
				return r
			}
			issuerCut := buyAmount.MulRatio(uint32(transferFee), transferFeeDivisor32, true)
			if !issuerCut.IsZero() {
				if r := payIOU(ctx, buyerID, nftIssuerID, issuerCut); r != tx.TesSUCCESS {
					return r
				}
				buyAmount, _ = buyAmount.Sub(issuerCut)
			}
		}

		// Pay seller remainder
		if !buyAmount.IsZero() {
			if r := payIOU(ctx, buyerID, accountID, buyAmount); r != tx.TesSUCCESS {
				return r
			}
		}

		// Sync ctx.Account.OwnerCount after IOU payments that may auto-create trust lines
		syncCtxOwnerCount(ctx)
	} else {
		// XRP payment path — deduct from buyer, pay issuer + seller
		// Reference: rippled NFTokenAcceptOffer.cpp — uses accountSend()
		amount := buyOffer.Amount

		// Re-read buyer account (OwnerCount reduced by offer deletion)
		buyerKey := keylet.Account(buyerID)
		buyerData, err := ctx.View.Read(buyerKey)
		if err != nil {
			return tx.TefINTERNAL
		}
		buyerAccount, err := state.ParseAccountRoot(buyerData)
		if err != nil {
			return tx.TefINTERNAL
		}
		buyerAccount.Balance -= amount
		buyerUpdated, _ := state.SerializeAccountRoot(buyerAccount)
		if err := ctx.View.Update(buyerKey, buyerUpdated); err != nil {
			return tx.TefINTERNAL
		}

		var issuerCut uint64
		if transferFee != 0 && amount > 0 {
			issuerCut = nftTransferFeeXRP(amount, transferFee)
			if accountID == nftIssuerID || buyerID == nftIssuerID {
				issuerCut = 0
			}
		}

		if issuerCut > 0 {
			issuerKey := keylet.Account(nftIssuerID)
			issuerData, err := ctx.View.Read(issuerKey)
			if err == nil {
				issuerAccount, err := state.ParseAccountRoot(issuerData)
				if err == nil {
					issuerAccount.Balance += issuerCut
					issuerUpdatedData, _ := state.SerializeAccountRoot(issuerAccount)
					ctx.View.Update(issuerKey, issuerUpdatedData)
				}
			}
			amount -= issuerCut
		}

		// Pay seller (the account accepting the buy offer)
		ctx.Account.Balance += amount
	}

	// Transfer the NFToken
	fixPageLinks := ctx.Rules().Enabled(amendment.FeatureFixNFTokenPageLinks)
	fixDirV1 := ctx.Rules().Enabled(amendment.FeatureFixNFTokenDirV1)
	xferResult := transferNFToken(accountID, buyerID, buyOffer.NFTokenID, ctx.View, fixPageLinks, fixDirV1)
	if xferResult.Result != tx.TesSUCCESS {
		return xferResult.Result
	}

	// Adjust OwnerCount for page changes from the transfer.
	for i := 0; i < xferResult.FromPagesRemoved; i++ {
		if ctx.Account.OwnerCount > 0 {
			ctx.Account.OwnerCount--
		}
	}
	adjustOwnerCountViaView(ctx.View, buyerID, xferResult.ToPagesCreated)

	// Check buyer reserve (fixNFTokenReserve)
	if r := checkBuyerReserve(ctx, buyerID, xferResult.ToPagesCreated); r != tx.TesSUCCESS {
		return r
	}

	return tx.TesSUCCESS
}
