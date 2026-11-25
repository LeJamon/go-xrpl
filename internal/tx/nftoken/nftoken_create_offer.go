package nftoken

import (
	"encoding/hex"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/mptutil"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/ledger/entry"
)

// NFTokenCreateOffer creates an offer to buy or sell an NFToken.
type NFTokenCreateOffer struct {
	tx.BaseTx

	// NFTokenID is the ID of the token (required)
	NFTokenID string `json:"NFTokenID" xrpl:"NFTokenID"`

	// Amount is the price for the offer (required)
	Amount tx.Amount `json:"Amount" xrpl:"Amount,amount"`

	// Owner is the owner of the token (required for buy offers)
	Owner string `json:"Owner,omitempty" xrpl:"Owner,omitempty"`

	// Destination is who can accept this offer (optional)
	Destination string `json:"Destination,omitempty" xrpl:"Destination,omitempty"`

	// Expiration is when the offer expires (optional)
	Expiration *uint32 `json:"Expiration,omitempty" xrpl:"Expiration,omitempty"`
}

// NFTokenCreateOffer flags
const (
	// tfSellNFToken indicates this is a sell offer
	NFTokenCreateOfferFlagSellNFToken uint32 = 0x00000001

	// tfNFTokenCreateOfferMask is the mask for invalid flags.
	// Reference: rippled TxFlags.h tfNFTokenCreateOfferMask = ~(tfUniversal | tfSellNFToken).
	tfNFTokenCreateOfferMask uint32 = ^(tx.TfUniversal | NFTokenCreateOfferFlagSellNFToken)
)

// NFToken buy/sell offer directory flags. rippled stamps the NFT's offer
// directory root with these via the dirInsert describe callback
// (NFTokenUtils.cpp:1059-1063); the same value the live ledger carries in the
// DirectoryNode's sfFlags.
// Reference: rippled LedgerFormats.h lsfNFTokenBuyOffers / lsfNFTokenSellOffers.
const (
	lsfNFTokenBuyOffers  = entry.LsfNFTokenBuyOffers
	lsfNFTokenSellOffers = entry.LsfNFTokenSellOffers
)

// NewNFTokenCreateOffer creates a new NFTokenCreateOffer transaction
func NewNFTokenCreateOffer(account, nftokenID string, amount tx.Amount) *NFTokenCreateOffer {
	return &NFTokenCreateOffer{
		BaseTx:    *tx.NewBaseTx(tx.TypeNFTokenCreateOffer, account),
		NFTokenID: nftokenID,
		Amount:    amount,
	}
}

func (n *NFTokenCreateOffer) TxType() tx.Type {
	return tx.TypeNFTokenCreateOffer
}

// GetFlagsMask matches rippled tfNFTokenCreateOfferMask = ~(tfUniversal |
// tfSellNFToken). The engine enforces it at preflight0, ahead of the
// account/fee/signing-key checks.
func (n *NFTokenCreateOffer) GetFlagsMask(*amendment.Rules) uint32 {
	return tfNFTokenCreateOfferMask
}

// Validate performs the rules-free structural checks. The flag mask is enforced
// by the engine (GetFlagsMask), and the amount/expiration/owner/destination
// checks live in PreflightRules because their order and gating depend on the
// active amendments.
// Reference: rippled NFTokenCreateOffer.cpp preflight.
func (n *NFTokenCreateOffer) Validate() error {
	if err := n.BaseTx.Validate(); err != nil {
		return err
	}

	// sfNFTokenID is soeREQUIRED in rippled; enforce its presence here so
	// PreflightRules can safely derive the token flags from it.
	if n.NFTokenID == "" {
		return ter.Errorf(ter.TemMALFORMED, "NFTokenID is required")
	}

	return nil
}

// PreflightRules runs the amendment-aware structural validation shared with
// NFTokenMint, in rippled's exact order.
// Reference: rippled NFTokenCreateOffer.cpp preflight → nft::tokenOfferCreatePreflight.
func (n *NFTokenCreateOffer) PreflightRules(rules *amendment.Rules) error {
	nftFlags := getNFTokenFlags(n.NFTokenID)
	isSellOffer := n.GetFlags()&NFTokenCreateOfferFlagSellNFToken != 0
	return tokenOfferCreatePreflight(rules, n.Account, n.Amount, n.Destination, n.Expiration, nftFlags, n.Owner, isSellOffer)
}

func (n *NFTokenCreateOffer) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(n)
}

// SetSellOffer marks this as a sell offer
func (n *NFTokenCreateOffer) SetSellOffer() {
	flags := n.GetFlags() | NFTokenCreateOfferFlagSellNFToken
	n.SetFlags(flags)
}

func (n *NFTokenCreateOffer) RequiredAmendments() [][32]byte {
	return nil
}

// Preclaim keeps retryable offer failures before fee, sequence, and ticket claim.
func (n *NFTokenCreateOffer) Preclaim(view tx.ReadOnlyLedgerView, config tx.EngineConfig) ter.Result {
	if tx.HasExpired(n.Expiration, config.ParentCloseTime) {
		return ter.TecEXPIRED
	}

	accountID, err := state.DecodeAccountID(n.Account)
	if err != nil {
		return ter.TemBAD_SRC_ACCOUNT
	}

	tokenIDBytes, err := hex.DecodeString(n.NFTokenID)
	if err != nil || len(tokenIDBytes) != 32 {
		return ter.TemINVALID
	}
	var tokenID [32]byte
	copy(tokenID[:], tokenIDBytes)

	isSellOffer := n.GetFlags()&NFTokenCreateOfferFlagSellNFToken != 0
	ownerID := accountID
	var buyOfferOwner *[20]byte
	if !isSellOffer {
		ownerID, err = state.DecodeAccountID(n.Owner)
		if err != nil {
			return ter.TemINVALID
		}
		buyOfferOwner = &ownerID
	}
	if _, _, _, found := findToken(view, ownerID, tokenID); !found {
		return ter.TecNO_ENTRY
	}

	token := offerToken{
		issuer:      getNFTIssuer(tokenID),
		flags:       getNFTFlagsFromID(tokenID),
		transferFee: getNFTTransferFee(tokenID),
	}
	return tokenOfferCreatePreclaim(view, config, accountID, token, n.Amount, n.Destination, buyOfferOwner)
}

type offerToken struct {
	issuer      [20]byte
	flags       uint16
	transferFee uint16
}

func tokenOfferCreatePreclaim(
	view tx.ReadOnlyLedgerView,
	config tx.EngineConfig,
	accountID [20]byte,
	token offerToken,
	amount tx.Amount,
	destination string,
	buyOfferOwner *[20]byte,
) ter.Result {
	rules := config.RequireRules()
	var iouIssuerID [20]byte

	if !amount.IsNative() {
		var err error
		iouIssuerID, err = state.DecodeAccountID(amount.Issuer)
		if err != nil {
			return ter.TemINVALID
		}

		if token.flags&NFTokenFlagTrustLine == 0 && token.transferFee != 0 {
			issuerExists, _ := view.Exists(keylet.Account(token.issuer))
			if !issuerExists {
				return ter.TecNO_ISSUER
			}

			if rules.Enabled(amendment.FeatureNFTokenMintOffer) {
				if token.issuer != iouIssuerID {
					trustLineData, err := view.Read(keylet.Line(token.issuer, iouIssuerID, amount.Currency))
					if err != nil || trustLineData == nil {
						return ter.TecNO_LINE
					}
				}
			} else {
				trustLineExists, _ := view.Exists(keylet.Line(token.issuer, iouIssuerID, amount.Currency))
				if !trustLineExists {
					return ter.TecNO_LINE
				}
			}

			if tx.IsGlobalFrozen(view, amount.Issuer) || tx.IsTrustlineFrozen(view, token.issuer, iouIssuerID, amount.Currency) {
				return ter.TecFROZEN
			}
		}
	}

	if token.issuer != accountID && token.flags&NFTokenFlagTransferable == 0 {
		issuerAccount, err := tx.ReadAccountRoot(view, token.issuer)
		if err != nil || issuerAccount == nil || issuerAccount.NFTokenMinter != state.EncodeAccountIDSafe(accountID) {
			return ter.TefNFTOKEN_IS_NOT_TRANSFERABLE
		}
	}

	if !amount.IsNative() && !(rules.Enabled(amendment.FeatureFixCleanup3_4_0) && accountID == iouIssuerID) &&
		(tx.IsGlobalFrozen(view, amount.Issuer) || tx.IsTrustlineFrozen(view, accountID, iouIssuerID, amount.Currency)) {
		return ter.TecFROZEN
	}

	if buyOfferOwner != nil {
		funds := mptutil.AccountFunds(view, accountID, amount, true, config.ReserveBase, config.ReserveIncrement)
		if funds.Signum() <= 0 {
			return ter.TecUNFUNDED_OFFER
		}
	}

	if destination != "" {
		destinationID, err := state.DecodeAccountID(destination)
		if err != nil {
			return ter.TemINVALID
		}
		destAccount, err := tx.ReadAccountRoot(view, destinationID)
		if err != nil {
			return ter.TefINTERNAL
		}
		if destAccount == nil {
			return ter.TecNO_DST
		}
		if destAccount.Flags&state.LsfDisallowIncomingNFTokenOffer != 0 {
			return ter.TecNO_PERMISSION
		}
	}

	if buyOfferOwner != nil {
		ownerAccount, err := tx.ReadAccountRoot(view, *buyOfferOwner)
		if err != nil {
			return ter.TefINTERNAL
		}
		if ownerAccount == nil {
			return ter.TecNO_TARGET
		}
		if ownerAccount.Flags&state.LsfDisallowIncomingNFTokenOffer != 0 {
			return ter.TecNO_PERMISSION
		}
	}

	if !amount.IsNative() && rules.Enabled(amendment.FeatureFixEnforceNFTokenTrustlineV2) {
		return checkNFTTrustlineAuthorized(view, accountID, amount.Currency, iouIssuerID)
	}

	return ter.TesSUCCESS
}

// Reference: rippled NFTokenCreateOffer.cpp doApply
func (n *NFTokenCreateOffer) Apply(ctx *tx.ApplyContext) ter.Result {
	ctx.Log.Trace("nftoken create offer apply",
		"account", n.Account,
		"tokenID", n.NFTokenID,
		"amount", n.Amount,
		"destination", n.Destination,
	)

	tokenIDBytes, err := hex.DecodeString(n.NFTokenID)
	if err != nil || len(tokenIDBytes) != 32 {
		return ter.TemINVALID
	}

	var tokenID [32]byte
	copy(tokenID[:], tokenIDBytes)

	isSellOffer := n.GetFlags()&NFTokenCreateOfferFlagSellNFToken != 0
	return tokenOfferCreateApply(ctx, tokenID, n.GetCommon().SeqProxy(), n.Amount, n.Destination, n.Expiration, isSellOffer)
}
