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
	if err := tx.Register(tx.TypeNFTokenCancelOffer, func() tx.Transaction {
		return &NFTokenCancelOffer{BaseTx: *tx.NewBaseTx(tx.TypeNFTokenCancelOffer, "")}
	}); err != nil {
		panic(err)
	}
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

func (n *NFTokenCancelOffer) TxType() tx.Type {
	return tx.TypeNFTokenCancelOffer
}

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

	seen := make(map[string]bool)
	for _, offerID := range n.NFTokenOffers {
		if seen[offerID] {
			return tx.Errorf(tx.TemMALFORMED, "duplicate offer ID in NFTokenOffers")
		}
		seen[offerID] = true
	}

	return nil
}

func (n *NFTokenCancelOffer) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(n)
}

func (n *NFTokenCancelOffer) RequiredAmendments() [][32]byte {
	return [][32]byte{amendment.FeatureNonFungibleTokensV1}
}

// Reference: rippled NFTokenCancelOffer.cpp preclaim + doApply
func (n *NFTokenCancelOffer) Apply(ctx *tx.ApplyContext) tx.Result {
	ctx.Log.Trace("nftoken cancel offer apply",
		"account", n.Account,
		"offerCount", len(n.NFTokenOffers),
	)

	accountID := ctx.AccountID

	// --- Preclaim: verify all offers can be cancelled ---
	// Reference: rippled NFTokenCancelOffer.cpp preclaim()
	for _, offerIDHex := range n.NFTokenOffers {
		offerIDBytes, err := hex.DecodeString(offerIDHex)
		if err != nil || len(offerIDBytes) != 32 {
			continue
		}

		var offerKeyBytes [32]byte
		copy(offerKeyBytes[:], offerIDBytes)
		offerKey := keylet.Keylet{Key: offerKeyBytes}

		offerData, err := ctx.View.Read(offerKey)
		if err != nil || offerData == nil {
			// Not in ledger — assume consumed. No permission error.
			continue
		}

		// If the entry exists but is NOT an NFTokenOffer, return tecNO_PERMISSION.
		// Reference: rippled preclaim() line 75: if (offer->getType() != ltNFTOKEN_OFFER) return true;
		entryType, err := state.GetLedgerEntryType(offerData)
		if err != nil || entry.Type(entryType) != entry.TypeNFTokenOffer {
			return tx.TecNO_PERMISSION
		}

		offer, err := state.ParseNFTokenOffer(offerData)
		if err != nil {
			return tx.TecNO_PERMISSION
		}

		// Anyone can cancel if expired
		isExpired := offer.Expiration != 0 && offer.Expiration <= ctx.Config.ParentCloseTime
		if isExpired {
			continue
		}

		// Owner can always cancel
		if offer.Owner == accountID {
			continue
		}

		// Destination can always cancel
		if offer.HasDestination && offer.Destination == accountID {
			continue
		}

		// No permission to cancel this offer
		return tx.TecNO_PERMISSION
	}

	// --- doApply: delete all offers ---
	// Reference: rippled NFTokenCancelOffer.cpp doApply()
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

		// Decrease owner count
		if offer.Owner == accountID {
			if ctx.Account.OwnerCount > 0 {
				ctx.Account.OwnerCount--
			}
		} else {
			adjustOwnerCountViaView(ctx.View, offer.Owner, -1)
		}

		// Delete the offer with proper directory cleanup
		deleteTokenOffer(ctx.View, offerKey)
	}

	return tx.TesSUCCESS
}
