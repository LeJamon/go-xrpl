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

	// The negative-amount and destination-on-buy tem* checks run in preflight
	// (PreflightRules → tokenOfferCreatePreflight), before this point.
	isSellOffer := n.GetFlags()&NFTokenCreateOfferFlagSellNFToken != 0

	if tx.HasExpired(n.Expiration, ctx.Config.ParentCloseTime) {
		ctx.Log.Warn("nftoken create offer: offer expired")
		return ter.TecEXPIRED
	}

	// Verify token ownership using findToken (proper page traversal)
	if isSellOffer {
		if _, _, _, found := findToken(ctx.View, accountID, tokenID); !found {
			return ter.TecNO_ENTRY
		}
	} else {
		var ownerID [20]byte
		ownerID, err = state.DecodeAccountID(n.Owner)
		if err != nil {
			return ter.TemINVALID
		}
		if _, _, _, found := findToken(ctx.View, ownerID, tokenID); !found {
			return ter.TecNO_ENTRY
		}
	}

	// Preclaim checks — order must match rippled's tokenOfferCreatePreclaim exactly.
	// Reference: rippled NFTokenUtils.cpp tokenOfferCreatePreclaim lines 897-1020

	nftFlags := getNFTFlagsFromID(tokenID)
	nftIssuerID := getNFTIssuer(tokenID)

	// 1. NFT issuer trust line + frozen check (when transfer fee is set and no auto-trust flag)
	// Reference: rippled tokenOfferCreatePreclaim lines 909-929
	if !n.Amount.IsNative() {
		iouIssuerID, err := state.DecodeAccountID(n.Amount.Issuer)
		if err != nil {
			return ter.TemINVALID
		}

		if nftFlags&NFTokenFlagTrustLine == 0 && getNFTTransferFee(tokenID) != 0 {
			issuerExists, _ := ctx.View.Exists(keylet.Account(nftIssuerID))
			if !issuerExists {
				return ter.TecNO_ISSUER
			}

			if ctx.Rules().Enabled(amendment.FeatureNFTokenMintOffer) {
				if nftIssuerID != iouIssuerID {
					trustLineKey := keylet.Line(nftIssuerID, iouIssuerID, n.Amount.Currency)
					trustLineData, err := ctx.View.Read(trustLineKey)
					if err != nil || trustLineData == nil {
						return ter.TecNO_LINE
					}
				}
			} else {
				trustLineKey := keylet.Line(nftIssuerID, iouIssuerID, n.Amount.Currency)
				trustLineExists, _ := ctx.View.Exists(trustLineKey)
				if !trustLineExists {
					return ter.TecNO_LINE
				}
			}

			// NFT issuer frozen check
			// Reference: rippled tokenOfferCreatePreclaim line 927-928
			if tx.IsGlobalFrozen(ctx.View, n.Amount.Issuer) || tx.IsTrustlineFrozen(ctx.View, nftIssuerID, iouIssuerID, n.Amount.Currency) {
				return ter.TecFROZEN
			}
		}
	}

	// 2. Transferable check
	// Reference: rippled tokenOfferCreatePreclaim lines 931-938
	if nftIssuerID != accountID && nftFlags&NFTokenFlagTransferable == 0 {
		issuerKey := keylet.Account(nftIssuerID)
		issuerData, err := ctx.View.Read(issuerKey)
		if err != nil {
			return ter.TefNFTOKEN_IS_NOT_TRANSFERABLE
		}
		issuerAccount, err := state.ParseAccountRoot(issuerData)
		if err != nil {
			return ter.TefNFTOKEN_IS_NOT_TRANSFERABLE
		}
		if issuerAccount.NFTokenMinter != n.Account {
			return ter.TefNFTOKEN_IS_NOT_TRANSFERABLE
		}
	}

	// 3. Account frozen check
	// Reference: rippled tokenOfferCreatePreclaim line 941
	if !n.Amount.IsNative() {
		iouIssuerID, _ := state.DecodeAccountID(n.Amount.Issuer)
		if tx.IsGlobalFrozen(ctx.View, n.Amount.Issuer) || tx.IsTrustlineFrozen(ctx.View, accountID, iouIssuerID, n.Amount.Currency) {
			return ter.TecFROZEN
		}
	}

	// 4. Fund check for buy offers. rippled's preclaim rejects a creator with no
	// available funds → tecUNFUNDED_OFFER: accountFunds().signum() <= 0
	// (post-fixNonFungibleTokensV1_2) or accountHolds().signum() <= 0 otherwise.
	// Reference: rippled tokenOfferCreatePreclaim lines 947-967.
	if !isSellOffer {
		if n.Amount.IsNative() {
			// rippled runs this native funds check in preclaim against the
			// pre-fee ReadView; goXRPL folds it into Apply, where the fee is
			// already deducted, so check the pre-fee PriorBalance (like the
			// reserve check below). A fee straddling reserve(OwnerCount) would
			// otherwise flip tecINSUFFICIENT_RESERVE into a spurious
			// tecUNFUNDED_OFFER. AccountFunds(native).signum()<=0 is exactly
			// balance<=reserve.
			if ctx.PriorBalance() <= ctx.AccountReserve(ctx.Account.OwnerCount) {
				return ter.TecUNFUNDED_OFFER
			}
		} else {
			funds := tx.AccountFunds(ctx.View, accountID, n.Amount, true, ctx.Config.ReserveBase, ctx.Config.ReserveIncrement)
			if funds.Signum() <= 0 {
				return ter.TecUNFUNDED_OFFER
			}
		}
	}

	// 5. Destination check
	// Reference: rippled tokenOfferCreatePreclaim lines 970-988
	if n.Destination != "" {
		destAccount, _, result := ctx.LookupAccount(n.Destination)
		if result != ter.TesSUCCESS {
			return result
		}
		if destAccount.Flags&state.LsfDisallowIncomingNFTokenOffer != 0 {
			return ter.TecNO_PERMISSION
		}
	}

	// 6. Owner disallow incoming check (for buy offers)
	// Reference: rippled tokenOfferCreatePreclaim lines 990-1004
	if n.Owner != "" {
		ownerAccount, _, result := ctx.LookupAccount(n.Owner)
		if result != ter.TesSUCCESS {
			return ter.TecNO_TARGET
		}
		if ownerAccount.Flags&state.LsfDisallowIncomingNFTokenOffer != 0 {
			return ter.TecNO_PERMISSION
		}
	}

	// 7. Trust line authorization checks (with fixEnforceNFTokenTrustlineV2)
	// Reference: rippled tokenOfferCreatePreclaim lines 1007-1018
	if !n.Amount.IsNative() && ctx.Rules().Enabled(amendment.FeatureFixEnforceNFTokenTrustlineV2) {
		iouIssuerID, _ := state.DecodeAccountID(n.Amount.Issuer)
		if r := checkNFTTrustlineAuthorized(ctx.View, accountID, n.Amount.Currency, iouIssuerID); r != ter.TesSUCCESS {
			return r
		}
	}

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
		return ter.TefINTERNAL
	}

	if err := ctx.View.Insert(offerKey, offerData); err != nil {
		return ter.TefINTERNAL
	}

	ctx.Account.OwnerCount++

	// The offer adds an owned object; charge its reserve against the pre-fee
	// balance so the source can still dip into the reserve to pay the fee.
	if r := ctx.CheckReserveWithFee(ctx.Account.OwnerCount); r != ter.TesSUCCESS {
		return r
	}

	return ter.TesSUCCESS
}
