package nftoken

import (
	"encoding/hex"

	"github.com/LeJamon/goXRPLd/amendment"
	"github.com/LeJamon/goXRPLd/internal/ledger/state"
	"github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/keylet"
	"github.com/LeJamon/goXRPLd/ledger/entry"
)

func init() {
	tx.Register(tx.TypeNFTokenCancelOffer, func() tx.Transaction {
		return &NFTokenCancelOffer{BaseTx: *tx.NewBaseTx(tx.TypeNFTokenCancelOffer, "")}
	})
}

// NFTokenCancelOffer cancels NFToken offers.
type NFTokenCancelOffer struct {
	tx.BaseTx

	// NFTokenOffers is the list of offer IDs to cancel (required)
	NFTokenOffers []string `json:"NFTokenOffers" xrpl:"NFTokenOffers"`
}

// NewNFTokenCancelOffer creates a new NFTokenCancelOffer transaction
func NewNFTokenCancelOffer(account string, offerIDs []string) *NFTokenCancelOffer {
	return &NFTokenCancelOffer{
		BaseTx:        *tx.NewBaseTx(tx.TypeNFTokenCancelOffer, account),
		NFTokenOffers: offerIDs,
	}
}

// TxType returns the transaction type
func (n *NFTokenCancelOffer) TxType() tx.Type {
	return tx.TypeNFTokenCancelOffer
}

// Validate validates the NFTokenCancelOffer transaction
// Reference: rippled NFTokenCancelOffer.cpp preflight
func (n *NFTokenCancelOffer) Validate() error {
	if err := n.BaseTx.Validate(); err != nil {
		return err
	}

	// Check flags - no flags are valid for NFTokenCancelOffer
	if n.GetFlags()&tfNFTokenCancelOfferMask != 0 {
		return tx.Errorf(tx.TemINVALID_FLAG, "invalid flags for NFTokenCancelOffer")
	}

	// Must have at least one offer ID
	if len(n.NFTokenOffers) == 0 {
		return tx.Errorf(tx.TemMALFORMED, "NFTokenOffers is required")
	}

	// Cannot exceed maximum offer count
	if len(n.NFTokenOffers) > maxTokenOfferCancelCount {
		return tx.Errorf(tx.TemMALFORMED, "NFTokenOffers exceeds maximum count")
	}

	// Check for duplicates
	seen := make(map[string]bool)
	for _, offerID := range n.NFTokenOffers {
		if seen[offerID] {
			return tx.Errorf(tx.TemMALFORMED, "duplicate offer ID in NFTokenOffers")
		}
		seen[offerID] = true
	}

	return nil
}

// Flatten returns a flat map of all transaction fields
func (n *NFTokenCancelOffer) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(n)
}

// RequiredAmendments returns the amendments required for this transaction type
func (n *NFTokenCancelOffer) RequiredAmendments() [][32]byte {
	return [][32]byte{amendment.FeatureNonFungibleTokensV1}
}

// Apply applies the NFTokenCancelOffer transaction to the ledger.
// Reference: rippled NFTokenCancelOffer.cpp doApply and preclaim
func (n *NFTokenCancelOffer) Apply(ctx *tx.ApplyContext) tx.Result {
	accountID := ctx.AccountID

	for _, offerIDHex := range n.NFTokenOffers {
		offerIDBytes, err := hex.DecodeString(offerIDHex)
		if err != nil || len(offerIDBytes) != 32 {
			continue
		}

		var offerKeyBytes [32]byte
		copy(offerKeyBytes[:], offerIDBytes)
		offerKey := keylet.Keylet{Type: entry.TypeNFTokenOffer, Key: offerKeyBytes}

		offerData, err := ctx.View.Read(offerKey)
		if err != nil || offerData == nil {
			continue
		}

		offer, err := state.ParseNFTokenOffer(offerData)
		if err != nil {
			continue
		}

		// Check authorization to cancel
		isExpired := offer.Expiration != 0 && offer.Expiration <= ctx.Config.ParentCloseTime
		isOwner := offer.Owner == accountID
		isDestination := offer.HasDestination && offer.Destination == accountID

		if !isOwner && !isDestination && !isExpired {
			return tx.TecNO_PERMISSION
		}

		// NFToken buy offers do NOT escrow XRP — no refund needed on cancellation.
		// Reference: rippled NFTokenUtils.cpp deleteTokenOffer — no balance adjustment

		// Decrease owner count
		if offer.Owner == accountID {
			if ctx.Account.OwnerCount > 0 {
				ctx.Account.OwnerCount--
			}
		} else {
			ownerKey := keylet.Account(offer.Owner)
			ownerData, err := ctx.View.Read(ownerKey)
			if err == nil && ownerData != nil {
				ownerAccount, err := state.ParseAccountRoot(ownerData)
				if err == nil && ownerAccount.OwnerCount > 0 {
					ownerAccount.OwnerCount--
					ownerUpdated, _ := state.SerializeAccountRoot(ownerAccount)
					if ownerUpdated != nil {
						ctx.View.Update(ownerKey, ownerUpdated)
					}
				}
			}
		}

		// Delete the offer with proper directory cleanup
		deleteTokenOffer(ctx.View, offerKey)
	}

	return tx.TesSUCCESS
}
