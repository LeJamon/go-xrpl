package nftoken

import (
	"encoding/hex"
	"errors"

	"github.com/LeJamon/goXRPLd/internal/core/amendment"
	"github.com/LeJamon/goXRPLd/internal/core/ledger/entry"
	"github.com/LeJamon/goXRPLd/internal/core/ledger/keylet"
	"github.com/LeJamon/goXRPLd/internal/core/tx"
	"github.com/LeJamon/goXRPLd/internal/core/tx/sle"
)

func init() {
	tx.Register(tx.TypeNFTokenAcceptOffer, func() tx.Transaction {
		return &NFTokenAcceptOffer{BaseTx: *tx.NewBaseTx(tx.TypeNFTokenAcceptOffer, "")}
	})
}

// NFTokenAcceptOffer accepts an NFToken offer.
type NFTokenAcceptOffer struct {
	tx.BaseTx

	// NFTokenSellOffer is the sell offer to accept (optional)
	NFTokenSellOffer string `json:"NFTokenSellOffer,omitempty" xrpl:"NFTokenSellOffer,omitempty"`

	// NFTokenBuyOffer is the buy offer to accept (optional)
	NFTokenBuyOffer string `json:"NFTokenBuyOffer,omitempty" xrpl:"NFTokenBuyOffer,omitempty"`

	// NFTokenBrokerFee is the broker fee for brokered sales (optional)
	NFTokenBrokerFee *tx.Amount `json:"NFTokenBrokerFee,omitempty" xrpl:"NFTokenBrokerFee,omitempty,amount"`
}

// NFTokenAcceptOffer has no transaction flags
const (
	// tfNFTokenAcceptOfferMask is the mask for invalid flags (all flags are invalid)
	tfNFTokenAcceptOfferMask uint32 = 0xFFFFFFFF
)

// NewNFTokenAcceptOffer creates a new NFTokenAcceptOffer transaction
func NewNFTokenAcceptOffer(account string) *NFTokenAcceptOffer {
	return &NFTokenAcceptOffer{
		BaseTx: *tx.NewBaseTx(tx.TypeNFTokenAcceptOffer, account),
	}
}

// TxType returns the transaction type
func (n *NFTokenAcceptOffer) TxType() tx.Type {
	return tx.TypeNFTokenAcceptOffer
}

// Validate validates the NFTokenAcceptOffer transaction
// Reference: rippled NFTokenAcceptOffer.cpp preflight
func (n *NFTokenAcceptOffer) Validate() error {
	if err := n.BaseTx.Validate(); err != nil {
		return err
	}

	// Check for invalid flags (no flags are valid for NFTokenAcceptOffer)
	if n.GetFlags() != 0 {
		return errors.New("temINVALID_FLAG: NFTokenAcceptOffer does not accept any flags")
	}

	// Must have at least one offer
	if n.NFTokenSellOffer == "" && n.NFTokenBuyOffer == "" {
		return errors.New("temMALFORMED: must specify NFTokenSellOffer or NFTokenBuyOffer")
	}

	// BrokerFee only valid for brokered mode (both offers)
	if n.NFTokenBrokerFee != nil {
		if n.NFTokenSellOffer == "" || n.NFTokenBuyOffer == "" {
			return errors.New("temMALFORMED: NFTokenBrokerFee requires both sell and buy offers")
		}
		// BrokerFee must be positive (greater than zero)
		// Reference: rippled NFTokenAcceptOffer.cpp:56 - if (*bf <= beast::zero)
		if n.NFTokenBrokerFee.IsZero() {
			return errors.New("temMALFORMED: NFTokenBrokerFee must be greater than zero")
		}
	}

	// Validate offer IDs are valid hex strings (64 characters = 32 bytes)
	if n.NFTokenSellOffer != "" && len(n.NFTokenSellOffer) != 64 {
		return errors.New("temMALFORMED: invalid NFTokenSellOffer format")
	}
	if n.NFTokenBuyOffer != "" && len(n.NFTokenBuyOffer) != 64 {
		return errors.New("temMALFORMED: invalid NFTokenBuyOffer format")
	}

	return nil
}

// Flatten returns a flat map of all transaction fields
func (n *NFTokenAcceptOffer) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(n)
}

// SetSellOffer sets the sell offer to accept
func (n *NFTokenAcceptOffer) SetSellOffer(offerID string) {
	n.NFTokenSellOffer = offerID
}

// SetBuyOffer sets the buy offer to accept
func (n *NFTokenAcceptOffer) SetBuyOffer(offerID string) {
	n.NFTokenBuyOffer = offerID
}

// RequiredAmendments returns the amendments required for this transaction type
func (n *NFTokenAcceptOffer) RequiredAmendments() [][32]byte {
	return [][32]byte{amendment.FeatureNonFungibleTokensV1}
}

// Apply applies the NFTokenAcceptOffer transaction to the ledger.
// Reference: rippled NFTokenAcceptOffer.cpp doApply
func (a *NFTokenAcceptOffer) Apply(ctx *tx.ApplyContext) tx.Result {
	accountID := ctx.AccountID

	// Load offers
	var buyOffer, sellOffer *sle.NFTokenOfferData
	var buyOfferKey, sellOfferKey keylet.Keylet

	if a.NFTokenBuyOffer != "" {
		buyOfferIDBytes, err := hex.DecodeString(a.NFTokenBuyOffer)
		if err != nil || len(buyOfferIDBytes) != 32 {
			return tx.TemINVALID
		}
		var buyOfferKeyBytes [32]byte
		copy(buyOfferKeyBytes[:], buyOfferIDBytes)
		buyOfferKey = keylet.Keylet{Type: entry.TypeNFTokenOffer, Key: buyOfferKeyBytes}

		buyOfferData, err := ctx.View.Read(buyOfferKey)
		if err != nil || buyOfferData == nil {
			return tx.TecOBJECT_NOT_FOUND
		}
		buyOffer, err = sle.ParseNFTokenOffer(buyOfferData)
		if err != nil {
			return tx.TefINTERNAL
		}

		// Check expiration
		if buyOffer.Expiration != 0 && buyOffer.Expiration <= ctx.Config.ParentCloseTime {
			return tx.TecEXPIRED
		}

		// Verify it's a buy offer (flag not set)
		if buyOffer.Flags&lsfSellNFToken != 0 {
			return tx.TecNFTOKEN_OFFER_TYPE_MISMATCH
		}

		// Cannot accept your own offer
		if buyOffer.Owner == accountID {
			return tx.TecCANT_ACCEPT_OWN_NFTOKEN_OFFER
		}
	}

	if a.NFTokenSellOffer != "" {
		sellOfferIDBytes, err := hex.DecodeString(a.NFTokenSellOffer)
		if err != nil || len(sellOfferIDBytes) != 32 {
			return tx.TemINVALID
		}
		var sellOfferKeyBytes [32]byte
		copy(sellOfferKeyBytes[:], sellOfferIDBytes)
		sellOfferKey = keylet.Keylet{Type: entry.TypeNFTokenOffer, Key: sellOfferKeyBytes}

		sellOfferData, err := ctx.View.Read(sellOfferKey)
		if err != nil || sellOfferData == nil {
			return tx.TecOBJECT_NOT_FOUND
		}
		sellOffer, err = sle.ParseNFTokenOffer(sellOfferData)
		if err != nil {
			return tx.TefINTERNAL
		}

		// Check expiration
		if sellOffer.Expiration != 0 && sellOffer.Expiration <= ctx.Config.ParentCloseTime {
			return tx.TecEXPIRED
		}

		// Verify it's a sell offer (flag set)
		if sellOffer.Flags&lsfSellNFToken == 0 {
			return tx.TecNFTOKEN_OFFER_TYPE_MISMATCH
		}

		// Cannot accept your own offer
		if sellOffer.Owner == accountID {
			return tx.TecCANT_ACCEPT_OWN_NFTOKEN_OFFER
		}
	}

	// IOU preclaim checks for all modes
	// Reference: rippled NFTokenAcceptOffer.cpp preclaim — IOU authorization and fund checks
	if r := a.iouPreclaimChecks(ctx, accountID, buyOffer, sellOffer); r != tx.TesSUCCESS {
		return r
	}

	// Brokered mode (both offers)
	if buyOffer != nil && sellOffer != nil {
		return a.acceptNFTokenBrokeredMode(ctx, accountID, buyOffer, sellOffer, buyOfferKey, sellOfferKey)
	}

	// Direct mode - sell offer only
	if sellOffer != nil {
		return a.acceptNFTokenSellOfferDirect(ctx, accountID, sellOffer, sellOfferKey)
	}

	// Direct mode - buy offer only
	if buyOffer != nil {
		return a.acceptNFTokenBuyOfferDirect(ctx, accountID, buyOffer, buyOfferKey)
	}

	return tx.TemINVALID
}

// iouPreclaimChecks performs IOU-specific preclaim checks for NFTokenAcceptOffer.
// Reference: rippled NFTokenAcceptOffer.cpp preclaim
func (a *NFTokenAcceptOffer) iouPreclaimChecks(ctx *tx.ApplyContext, accountID [20]byte,
	buyOffer, sellOffer *sle.NFTokenOfferData) tx.Result {

	fixV2 := ctx.Rules().Enabled(amendment.FeatureFixEnforceNFTokenTrustlineV2)

	fixV1_2 := ctx.Rules().Enabled(amendment.FeatureFixNonFungibleTokensV1_2)

	// Check buy offer IOU constraints
	if buyOffer != nil && buyOffer.AmountIOU != nil {
		currency := buyOffer.AmountIOU.Currency
		issuerID := buyOffer.AmountIOU.Issuer
		buyAmount := offerIOUToAmount(buyOffer)

		// Fund check: buyer must have sufficient IOU balance
		// Reference: rippled — with fixNonFungibleTokensV1_2 uses accountFunds,
		// without uses accountHolds (no issuer exception)
		if fixV1_2 {
			funds := tx.AccountFunds(ctx.View, buyOffer.Owner, buyAmount, true, ctx.Config.ReserveBase, ctx.Config.ReserveIncrement)
			if funds.Compare(buyAmount) < 0 {
				return tx.TecINSUFFICIENT_FUNDS
			}
		} else {
			funds := accountHoldsIOU(ctx.View, buyOffer.Owner, buyAmount)
			if funds.Compare(buyAmount) < 0 {
				return tx.TecINSUFFICIENT_FUNDS
			}
		}

		if fixV2 {
			// Buyer must be authorized
			if r := checkNFTTrustlineAuthorized(ctx.View, buyOffer.Owner, currency, issuerID); r != tx.TesSUCCESS {
				return r
			}

			// Direct buy offer: seller (acceptor = ctx.Account) must be authorized
			if sellOffer == nil {
				if r := checkNFTTrustlineAuthorized(ctx.View, accountID, currency, issuerID); r != tx.TesSUCCESS {
					return r
				}
			}
		}
	}

	// Check sell offer IOU constraints
	// Reference: rippled preclaim — fund checks BEFORE auth checks
	if sellOffer != nil && sellOffer.AmountIOU != nil {
		currency := sellOffer.AmountIOU.Currency
		issuerID := sellOffer.AmountIOU.Issuer

		// Fund check for direct sell mode: buyer (acceptor) must have funds
		// Reference: rippled — without fixNonFungibleTokensV1_2 always checks;
		// with fix, only checks in direct mode (not brokered)
		if !fixV1_2 {
			sellAmount := offerIOUToAmount(sellOffer)
			funds := accountHoldsIOU(ctx.View, accountID, sellAmount)
			if funds.Compare(sellAmount) < 0 {
				return tx.TecINSUFFICIENT_FUNDS
			}
		} else if buyOffer == nil {
			sellAmount := offerIOUToAmount(sellOffer)
			funds := tx.AccountFunds(ctx.View, accountID, sellAmount, true, ctx.Config.ReserveBase, ctx.Config.ReserveIncrement)
			if funds.Compare(sellAmount) < 0 {
				return tx.TecINSUFFICIENT_FUNDS
			}
		}

		if fixV2 {
			// Seller must be authorized
			if r := checkNFTTrustlineAuthorized(ctx.View, sellOffer.Owner, currency, issuerID); r != tx.TesSUCCESS {
				return r
			}

			// Direct sell offer: buyer (acceptor = ctx.Account) must be authorized
			if buyOffer == nil {
				if r := checkNFTTrustlineAuthorized(ctx.View, accountID, currency, issuerID); r != tx.TesSUCCESS {
					return r
				}
			}
		}
	}

	// Brokered mode broker fee check
	if buyOffer != nil && sellOffer != nil && a.NFTokenBrokerFee != nil && !a.NFTokenBrokerFee.IsNative() {
		if fixV2 {
			brokerFeeIssuerID, err := sle.DecodeAccountID(a.NFTokenBrokerFee.Issuer)
			if err == nil {
				if r := checkNFTTrustlineAuthorized(ctx.View, accountID, a.NFTokenBrokerFee.Currency, brokerFeeIssuerID); r != tx.TesSUCCESS {
					return r
				}
			}
		}
	}

	// NFT issuer transfer fee check — when an IOU sale has an NFT transfer fee,
	// the NFT issuer must be authorized to receive the IOU.
	// Reference: rippled NFTokenAcceptOffer.cpp preclaim — checkTrustlineAuthorized for issuer
	var tokenID [32]byte
	if buyOffer != nil {
		tokenID = buyOffer.NFTokenID
	} else if sellOffer != nil {
		tokenID = sellOffer.NFTokenID
	}

	transferFee := getNFTTransferFee(tokenID)
	if transferFee != 0 && fixV2 {
		nftIssuerID := getNFTIssuer(tokenID)

		// Determine the IOU currency/issuer from whichever offer is IOU
		var iouOffer *sle.NFTokenOfferData
		if buyOffer != nil && buyOffer.AmountIOU != nil {
			iouOffer = buyOffer
		} else if sellOffer != nil && sellOffer.AmountIOU != nil {
			iouOffer = sellOffer
		}

		if iouOffer != nil {
			iouIssuerID := iouOffer.AmountIOU.Issuer
			// Only check if NFT issuer != IOU issuer (NFT issuer needs to hold the IOU)
			if nftIssuerID != iouIssuerID {
				if r := checkNFTTrustlineAuthorized(ctx.View, nftIssuerID, iouOffer.AmountIOU.Currency, iouIssuerID); r != tx.TesSUCCESS {
					return r
				}
			}
		}
	}

	return tx.TesSUCCESS
}
