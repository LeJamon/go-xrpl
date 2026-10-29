package nftoken

import (
	"encoding/hex"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
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
	if !isSellOffer {
		ownerID, err = state.DecodeAccountID(n.Owner)
		if err != nil {
			return ter.TemINVALID
		}
	}
	if _, _, _, found := findToken(view, ownerID, tokenID); !found {
		return ter.TecNO_ENTRY
	}

	rules := config.RequireRules()
	nftFlags := getNFTFlagsFromID(tokenID)
	nftIssuerID := getNFTIssuer(tokenID)
	var iouIssuerID [20]byte

	if !n.Amount.IsNative() {
		iouIssuerID, err = state.DecodeAccountID(n.Amount.Issuer)
		if err != nil {
			return ter.TemINVALID
		}

		if nftFlags&NFTokenFlagTrustLine == 0 && getNFTTransferFee(tokenID) != 0 {
			issuerExists, _ := view.Exists(keylet.Account(nftIssuerID))
			if !issuerExists {
				return ter.TecNO_ISSUER
			}

			if rules.Enabled(amendment.FeatureNFTokenMintOffer) {
				if nftIssuerID != iouIssuerID {
					trustLineData, err := view.Read(keylet.Line(nftIssuerID, iouIssuerID, n.Amount.Currency))
					if err != nil || trustLineData == nil {
						return ter.TecNO_LINE
					}
				}
			} else {
				trustLineExists, _ := view.Exists(keylet.Line(nftIssuerID, iouIssuerID, n.Amount.Currency))
				if !trustLineExists {
					return ter.TecNO_LINE
				}
			}

			if tx.IsGlobalFrozen(view, n.Amount.Issuer) || tx.IsTrustlineFrozen(view, nftIssuerID, iouIssuerID, n.Amount.Currency) {
				return ter.TecFROZEN
			}
		}
	}

	if nftIssuerID != accountID && nftFlags&NFTokenFlagTransferable == 0 {
		issuerAccount, err := tx.ReadAccountRoot(view, nftIssuerID)
		if err != nil || issuerAccount == nil || issuerAccount.NFTokenMinter != n.Account {
			return ter.TefNFTOKEN_IS_NOT_TRANSFERABLE
		}
	}

	if !n.Amount.IsNative() && !(rules.Enabled(amendment.FeatureFixCleanup3_4_0) && accountID == iouIssuerID) &&
		(tx.IsGlobalFrozen(view, n.Amount.Issuer) || tx.IsTrustlineFrozen(view, accountID, iouIssuerID, n.Amount.Currency)) {
		return ter.TecFROZEN
	}

	if !isSellOffer {
		funds := tx.AccountFunds(view, accountID, n.Amount, true, config.ReserveBase, config.ReserveIncrement)
		if funds.Signum() <= 0 {
			return ter.TecUNFUNDED_OFFER
		}
	}

	if n.Destination != "" {
		destinationID, err := state.DecodeAccountID(n.Destination)
		if err != nil {
			return ter.TemINVALID
		}
		destination, err := tx.ReadAccountRoot(view, destinationID)
		if err != nil {
			return ter.TefINTERNAL
		}
		if destination == nil {
			return ter.TecNO_DST
		}
		if destination.Flags&state.LsfDisallowIncomingNFTokenOffer != 0 {
			return ter.TecNO_PERMISSION
		}
	}

	if n.Owner != "" {
		ownerAccount, err := tx.ReadAccountRoot(view, ownerID)
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

	if !n.Amount.IsNative() && rules.Enabled(amendment.FeatureFixEnforceNFTokenTrustlineV2) {
		if result := checkNFTTrustlineAuthorized(view, accountID, n.Amount.Currency, iouIssuerID); result != ter.TesSUCCESS {
			return result
		}
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

	accountID := ctx.AccountID

	// Parse token ID
	tokenIDBytes, err := hex.DecodeString(n.NFTokenID)
	if err != nil || len(tokenIDBytes) != 32 {
		return ter.TemINVALID
	}

	var tokenID [32]byte
	copy(tokenID[:], tokenIDBytes)

	isSellOffer := n.GetFlags()&NFTokenCreateOfferFlagSellNFToken != 0

	// For buy offers, check the buyer has enough XRP for reserve but do NOT
	// escrow/deduct the offer amount. NFToken buy offers are unfunded promises
	// — the buyer's balance is only checked, not held.
	// Reference: rippled NFTokenUtils.cpp tokenOfferCreateApply — no balance deduction

	sequence := n.GetCommon().SeqProxy()
	offerKey := keylet.NFTokenOffer(accountID, sequence)

	// Insert into owner's directory. The describe callback stamps sfOwner on
	// a newly created owner-dir root/page (rippled describeOwnerDir, View.cpp:
	// 1048); without it the SLE bytes (and CreatedNode NewFields) diverge.
	ownerDirKey := keylet.OwnerDir(accountID)
	dirResult, err := state.DirInsert(ctx.View, ownerDirKey, offerKey.Key, false, func(dir *state.DirectoryNode) {
		dir.Owner = accountID
	})
	if err != nil {
		return ter.TefINTERNAL
	}
	ownerNode := dirResult.Page

	// Insert into NFTSells or NFTBuys directory. rippled stamps the offer
	// directory root with sfFlags (lsfNFTokenSellOffers/BuyOffers) and
	// sfNFTokenID via the describe callback (NFTokenUtils.cpp:1059-1063).
	var tokenDirKey keylet.Keylet
	dirFlags := lsfNFTokenBuyOffers
	if isSellOffer {
		tokenDirKey = keylet.NFTSells(tokenID)
		dirFlags = lsfNFTokenSellOffers
	} else {
		tokenDirKey = keylet.NFTBuys(tokenID)
	}
	tokenDirResult, err := state.DirInsert(ctx.View, tokenDirKey, offerKey.Key, false, func(dir *state.DirectoryNode) {
		dir.Flags = dirFlags
		dir.NFTokenID = tokenID
	})
	if err != nil {
		return ter.TefINTERNAL
	}
	offerNode := tokenDirResult.Page

	// Serialize the offer with directory page numbers
	offerData, err := serializeNFTokenOffer(n, accountID, tokenID, sequence, ownerNode, offerNode)
	if err != nil {
		return ctx.Internal("serializeNFTokenOffer", err)
	}

	if err := ctx.View.Insert(offerKey, offerData); err != nil {
		return ctx.Internal("insert NFTokenOffer", err)
	}

	ctx.Account.OwnerCount++

	// The offer adds an owned object; charge its reserve against the pre-fee
	// balance so the source can still dip into the reserve to pay the fee.
	if r := ctx.CheckReserveWithFee(ctx.Account.OwnerCount); r != ter.TesSUCCESS {
		return r
	}

	return ter.TesSUCCESS
}
