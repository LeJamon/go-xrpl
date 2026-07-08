package nftoken

import (
	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

// tokenOfferCreatePreflight is the amendment-aware structural validation shared
// by NFTokenCreateOffer and the offer-creating path of NFTokenMint (which always
// creates a sell offer with no Owner). It runs in the per-type preflight body
// (via PreflightRules), so its tem* rejections happen before any ledger read and
// before signature verification.
//
// The check order is identical to rippled: negative → OnlyXRP/zero → buy-zero →
// expiration → owner presence → owner==account → destination. Keeping this order
// matters because the negative-amount temBAD_AMOUNT must win over a later
// temBAD_EXPIRATION/temMALFORMED on the same transaction.
//
// Reference: rippled NFTokenUtils.cpp nft::tokenOfferCreatePreflight.
func tokenOfferCreatePreflight(
	rules *amendment.Rules,
	account string,
	amount tx.Amount,
	dest string,
	expiration *uint32,
	nftFlags uint16,
	owner string,
	isSellOffer bool,
) error {
	// An offer for a negative amount makes no sense (gated on fixNFTokenNegOffer,
	// which the original implementation lacked).
	if amount.IsNegative() && rules.Enabled(amendment.FeatureFixNFTokenNegOffer) {
		return ter.Errorf(ter.TemBAD_AMOUNT, "offer amount cannot be negative")
	}

	if !amount.IsNative() {
		if nftFlags&NFTokenFlagOnlyXRP != 0 {
			return ter.Errorf(ter.TemBAD_AMOUNT, "NFToken requires XRP only")
		}
		if amount.IsZero() {
			return ter.Errorf(ter.TemBAD_AMOUNT, "IOU amount cannot be zero")
		}
	}

	// A buy offer must offer something; a sell offer may ask for nothing.
	if !isSellOffer && amount.IsZero() {
		return ter.Errorf(ter.TemBAD_AMOUNT, "buy offer amount cannot be zero")
	}

	if expiration != nil && *expiration == 0 {
		return ter.Errorf(ter.TemBAD_EXPIRATION, "Expiration cannot be 0")
	}

	// The Owner field must be present when offering to buy, but can't be present
	// when selling (it's implicit).
	if (owner != "") == isSellOffer {
		return ter.Errorf(ter.TemMALFORMED, "Owner is required for buy offers and forbidden on sell offers")
	}
	if owner != "" && owner == account {
		return ter.Errorf(ter.TemMALFORMED, "Owner cannot be the same as Account")
	}

	if dest != "" {
		// A Destination on a buy offer (used to pin a specific broker) was
		// malformed before fixNFTokenNegOffer, which piggy-backed the relaxation.
		if !isSellOffer && !rules.Enabled(amendment.FeatureFixNFTokenNegOffer) {
			return ter.Errorf(ter.TemMALFORMED, "Destination not allowed on buy offer")
		}
		if dest == account {
			return ter.Errorf(ter.TemMALFORMED, "Destination cannot be the same as Account")
		}
	}

	return nil
}
