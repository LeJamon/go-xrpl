package nftoken

import (
	"github.com/LeJamon/goXRPLd/amendment"
	"github.com/LeJamon/goXRPLd/internal/ledger/state"
	"github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/keylet"
)

// ---------------------------------------------------------------------------
// Brokered mode and direct offer acceptance helpers
// ---------------------------------------------------------------------------

// acceptNFTokenBrokeredMode handles brokered NFToken sales
// Reference: rippled NFTokenAcceptOffer.cpp doApply (brokered mode)
func (n *NFTokenAcceptOffer) acceptNFTokenBrokeredMode(ctx *tx.ApplyContext, accountID [20]byte,
	buyOffer, sellOffer *state.NFTokenOfferData, buyOfferKey, sellOfferKey keylet.Keylet) tx.Result {

	if buyOffer.NFTokenID != sellOffer.NFTokenID {
		return tx.TecNFTOKEN_BUY_SELL_MISMATCH
	}

	buyIsXRP := buyOffer.AmountIOU == nil
	sellIsXRP := sellOffer.AmountIOU == nil
	if buyIsXRP != sellIsXRP {
		return tx.TecNFTOKEN_BUY_SELL_MISMATCH
	}
	if !buyIsXRP && !sellIsXRP {
		if buyOffer.AmountIOU.Currency != sellOffer.AmountIOU.Currency ||
			buyOffer.AmountIOU.Issuer != sellOffer.AmountIOU.Issuer {
			return tx.TecNFTOKEN_BUY_SELL_MISMATCH
		}
	}

	if buyOffer.Owner == sellOffer.Owner {
		return tx.TecCANT_ACCEPT_OWN_NFTOKEN_OFFER
	}

	// Verify the seller owns the token
	sellerID := sellOffer.Owner
	if _, _, _, found := findToken(ctx.View, sellerID, sellOffer.NFTokenID); !found {
		return tx.TecNO_PERMISSION
	}

	// Amount comparison — IOU-aware
	if buyIsXRP {
		if buyOffer.Amount < sellOffer.Amount {
			return tx.TecINSUFFICIENT_PAYMENT
		}
	} else {
		buyAmount := offerIOUToAmount(buyOffer)
		sellAmount := offerIOUToAmount(sellOffer)
		if buyAmount.Compare(sellAmount) < 0 {
			return tx.TecINSUFFICIENT_PAYMENT
		}
	}

	if buyOffer.HasDestination && buyOffer.Destination != accountID {
		return tx.TecNO_PERMISSION
	}
	if sellOffer.HasDestination && sellOffer.Destination != accountID {
		return tx.TecNO_PERMISSION
	}

	buyerID := buyOffer.Owner

	var brokerFee uint64
	var brokerFeeIOU tx.Amount
	if n.NFTokenBrokerFee != nil {
		brokerFeeIsXRP := n.NFTokenBrokerFee.Currency == ""
		if brokerFeeIsXRP != buyIsXRP {
			return tx.TecNFTOKEN_BUY_SELL_MISMATCH
		}

		if buyIsXRP {
			brokerFee = uint64(n.NFTokenBrokerFee.Drops())
			if brokerFee >= buyOffer.Amount {
				return tx.TecINSUFFICIENT_PAYMENT
			}
			if sellOffer.Amount > buyOffer.Amount-brokerFee {
				return tx.TecINSUFFICIENT_PAYMENT
			}
		} else {
			brokerFeeIOU = *n.NFTokenBrokerFee
			buyAmount := offerIOUToAmount(buyOffer)
			sellAmount := offerIOUToAmount(sellOffer)
			if brokerFeeIOU.Compare(buyAmount) >= 0 {
				return tx.TecINSUFFICIENT_PAYMENT
			}
			remainder, _ := buyAmount.Sub(brokerFeeIOU)
			if sellAmount.Compare(remainder) > 0 {
				return tx.TecINSUFFICIENT_PAYMENT
			}
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
	} else {
		// XRP brokered payment path — deduct from buyer, pay broker + issuer + seller
		// Reference: rippled NFTokenAcceptOffer.cpp — uses accountSend()
		amount := buyOffer.Amount

		// Deduct full amount from buyer's account
		buyerKey := keylet.Account(buyerID)
		buyerData, err := ctx.View.Read(buyerKey)
		if err != nil {
			return tx.TefINTERNAL
		}
		buyerAccount, err := state.ParseAccountRoot(buyerData)
		if err != nil {
			return tx.TefINTERNAL
		}
		if buyerAccount.Balance < amount {
			return tx.TecINSUFFICIENT_FUNDS
		}
		buyerAccount.Balance -= amount
		buyerUpdated, _ := state.SerializeAccountRoot(buyerAccount)
		if err := ctx.View.Update(buyerKey, buyerUpdated); err != nil {
			return tx.TefINTERNAL
		}

		var issuerCut uint64
		if transferFee != 0 && amount > 0 {
			issuerCut = (amount - brokerFee) * uint64(transferFee) / transferFeeDivisor
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

	// Transfer the NFToken from seller to buyer
	fixPageLinks := ctx.Rules().Enabled(amendment.FeatureFixNFTokenPageLinks)
	xferResult := transferNFToken(sellerID, buyerID, sellOffer.NFTokenID, ctx.View, fixPageLinks)
	if xferResult.Result != tx.TesSUCCESS {
		return xferResult.Result
	}

	// Adjust OwnerCount for page changes from the transfer.
	adjustOwnerCountViaView(ctx.View, sellerID, -xferResult.FromPagesRemoved)
	adjustOwnerCountViaView(ctx.View, buyerID, xferResult.ToPagesCreated)

	// Delete both offers using proper directory cleanup
	deleteTokenOffer(ctx.View, buyOfferKey)
	deleteTokenOffer(ctx.View, sellOfferKey)

	// Decrement owner counts for the deleted offers
	adjustOwnerCountViaView(ctx.View, buyerID, -1)
	adjustOwnerCountViaView(ctx.View, sellerID, -1)

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
	} else {
		// XRP payment path (existing logic)
		amount := sellOffer.Amount
		var issuerCut uint64

		if transferFee != 0 && amount > 0 {
			issuerCut = amount * uint64(transferFee) / transferFeeDivisor
			if sellerID == nftIssuerID || accountID == nftIssuerID {
				issuerCut = 0
			}
		}

		totalCost := amount
		if ctx.Account.Balance < totalCost {
			return tx.TecINSUFFICIENT_FUNDS
		}
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
	xferResult := transferNFToken(sellerID, accountID, sellOffer.NFTokenID, ctx.View, fixPageLinks)
	if xferResult.Result != tx.TesSUCCESS {
		return xferResult.Result
	}

	// Adjust OwnerCount for page changes from the transfer.
	adjustOwnerCountViaView(ctx.View, sellerID, -xferResult.FromPagesRemoved)
	ctx.Account.OwnerCount += uint32(xferResult.ToPagesCreated)

	// Delete offer with proper directory cleanup
	deleteTokenOffer(ctx.View, sellOfferKey)

	// Decrement seller's owner count for the deleted offer
	adjustOwnerCountViaView(ctx.View, sellerID, -1)

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
	} else {
		// XRP payment path — deduct from buyer, pay issuer + seller
		// Reference: rippled NFTokenAcceptOffer.cpp — uses accountSend()
		amount := buyOffer.Amount

		// Deduct full amount from buyer's account (buyer != ctx.Account)
		buyerKey := keylet.Account(buyerID)
		buyerData, err := ctx.View.Read(buyerKey)
		if err != nil {
			return tx.TefINTERNAL
		}
		buyerAccount, err := state.ParseAccountRoot(buyerData)
		if err != nil {
			return tx.TefINTERNAL
		}
		if buyerAccount.Balance < amount {
			return tx.TecINSUFFICIENT_FUNDS
		}
		buyerAccount.Balance -= amount
		buyerUpdated, _ := state.SerializeAccountRoot(buyerAccount)
		if err := ctx.View.Update(buyerKey, buyerUpdated); err != nil {
			return tx.TefINTERNAL
		}

		var issuerCut uint64
		if transferFee != 0 && amount > 0 {
			issuerCut = amount * uint64(transferFee) / transferFeeDivisor
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
	xferResult := transferNFToken(accountID, buyerID, buyOffer.NFTokenID, ctx.View, fixPageLinks)
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

	// Delete offer with proper directory cleanup
	deleteTokenOffer(ctx.View, buyOfferKey)

	// Decrement buyer's owner count for the deleted offer
	adjustOwnerCountViaView(ctx.View, buyerID, -1)

	return tx.TesSUCCESS
}
