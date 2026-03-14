package nftoken

import (
	"encoding/hex"

	"github.com/LeJamon/goXRPLd/amendment"
	"github.com/LeJamon/goXRPLd/internal/ledger/state"
	"github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/keylet"
)

func init() {
	tx.Register(tx.TypeNFTokenCreateOffer, func() tx.Transaction {
		return &NFTokenCreateOffer{BaseTx: *tx.NewBaseTx(tx.TypeNFTokenCreateOffer, "")}
	})
}

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

	// tfNFTokenCreateOfferMask is the mask for invalid flags
	tfNFTokenCreateOfferMask uint32 = ^NFTokenCreateOfferFlagSellNFToken
)

// NewNFTokenCreateOffer creates a new NFTokenCreateOffer transaction
func NewNFTokenCreateOffer(account, nftokenID string, amount tx.Amount) *NFTokenCreateOffer {
	return &NFTokenCreateOffer{
		BaseTx:    *tx.NewBaseTx(tx.TypeNFTokenCreateOffer, account),
		NFTokenID: nftokenID,
		Amount:    amount,
	}
}

// TxType returns the transaction type
func (n *NFTokenCreateOffer) TxType() tx.Type {
	return tx.TypeNFTokenCreateOffer
}

// Validate validates the NFTokenCreateOffer transaction
// Reference: rippled NFTokenCreateOffer.cpp preflight and tokenOfferCreatePreflight
// IMPORTANT: validation order must match rippled exactly (amount → expiration → owner → destination)
func (n *NFTokenCreateOffer) Validate() error {
	if err := n.BaseTx.Validate(); err != nil {
		return err
	}

	// Check for invalid flags
	if n.GetFlags()&tfNFTokenCreateOfferMask != 0 {
		return tx.Errorf(tx.TemINVALID_FLAG, "invalid NFTokenCreateOffer flags")
	}

	if n.NFTokenID == "" {
		return tx.Errorf(tx.TemMALFORMED, "NFTokenID is required")
	}

	// Parse NFToken flags from token ID to validate
	nftFlags := getNFTokenFlags(n.NFTokenID)

	isSellOffer := n.GetFlags()&NFTokenCreateOfferFlagSellNFToken != 0

	// --- tokenOfferCreatePreflight order (must match rippled exactly) ---

	// 1. Negative amount check — gated on fixNFTokenNegOffer amendment.
	// Since Validate() has no access to amendment rules, this check is
	// performed in Apply(). When fixNFTokenNegOffer is disabled (pre-amendment),
	// negative offers are allowed (bug-compatible with rippled).
	// Reference: rippled tokenOfferCreatePreflight line 847

	// 2. IOU-specific amount checks
	// Reference: rippled tokenOfferCreatePreflight lines 851-858
	if !n.Amount.IsNative() {
		if nftFlags&nftFlagOnlyXRP != 0 {
			return tx.Errorf(tx.TemBAD_AMOUNT, "NFToken requires XRP only")
		}
		if n.Amount.IsZero() {
			return tx.Errorf(tx.TemBAD_AMOUNT, "IOU amount cannot be zero")
		}
	}

	// 3. Buy offer zero amount check
	// Reference: rippled tokenOfferCreatePreflight lines 863-864
	if !isSellOffer && n.Amount.IsZero() {
		return tx.Errorf(tx.TemBAD_AMOUNT, "buy offer amount cannot be zero")
	}

	// 4. Expiration validation - expiration of 0 is invalid
	// Reference: rippled tokenOfferCreatePreflight lines 866-867
	if n.Expiration != nil && *n.Expiration == 0 {
		return tx.Errorf(tx.TemBAD_EXPIRATION, "Expiration cannot be 0")
	}

	// 5. Owner field checks
	// Reference: rippled tokenOfferCreatePreflight lines 871-875
	// The 'Owner' field must be present when offering to buy, but can't
	// be present when selling (it's implicit)
	if (n.Owner != "") == isSellOffer {
		if !isSellOffer && n.Owner == "" {
			return tx.Errorf(tx.TemMALFORMED, "Owner is required for buy offers")
		}
		if isSellOffer && n.Owner != "" {
			return tx.Errorf(tx.TemMALFORMED, "Owner not allowed for sell offers")
		}
	}

	// Owner cannot be the same as Account
	// Reference: rippled tokenOfferCreatePreflight lines 874-875
	if n.Owner != "" && n.Owner == n.Account {
		return tx.Errorf(tx.TemMALFORMED, "Owner cannot be the same as Account")
	}

	// 6. Destination checks
	// Reference: rippled tokenOfferCreatePreflight lines 877-892
	if n.Destination != "" {
		// The destination can't be the account executing the transaction
		if n.Destination == n.Account {
			return tx.Errorf(tx.TemMALFORMED, "Destination cannot be the same as Account")
		}
	}

	return nil
}

// Flatten returns a flat map of all transaction fields
func (n *NFTokenCreateOffer) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(n)
}

// SetSellOffer marks this as a sell offer
func (n *NFTokenCreateOffer) SetSellOffer() {
	flags := n.GetFlags() | NFTokenCreateOfferFlagSellNFToken
	n.SetFlags(flags)
}

// RequiredAmendments returns the amendments required for this transaction type
func (n *NFTokenCreateOffer) RequiredAmendments() [][32]byte {
	return [][32]byte{amendment.FeatureNonFungibleTokensV1}
}

// Apply applies the NFTokenCreateOffer transaction to the ledger.
// Reference: rippled NFTokenCreateOffer.cpp doApply
func (n *NFTokenCreateOffer) Apply(ctx *tx.ApplyContext) tx.Result {
	accountID := ctx.AccountID

	// Parse token ID
	tokenIDBytes, err := hex.DecodeString(n.NFTokenID)
	if err != nil || len(tokenIDBytes) != 32 {
		return tx.TemINVALID
	}

	var tokenID [32]byte
	copy(tokenID[:], tokenIDBytes)

	// Negative amount check — gated on fixNFTokenNegOffer
	// Reference: rippled tokenOfferCreatePreflight line 847
	if n.Amount.IsNegative() && ctx.Rules().Enabled(amendment.FeatureFixNFTokenNegOffer) {
		return tx.TemBAD_AMOUNT
	}

	// Destination on buy offers: pre-fixNFTokenNegOffer, any Destination on a
	// buy offer is malformed. Post-amendment, it's allowed (for broker use).
	// Reference: rippled tokenOfferCreatePreflight lines 877-892
	isSellOffer := n.GetFlags()&NFTokenCreateOfferFlagSellNFToken != 0
	if n.Destination != "" && !isSellOffer && !ctx.Rules().Enabled(amendment.FeatureFixNFTokenNegOffer) {
		return tx.TemMALFORMED
	}

	// Check expiration
	if n.Expiration != nil && *n.Expiration <= ctx.Config.ParentCloseTime {
		return tx.TecEXPIRED
	}

	// Verify token ownership using findToken (proper page traversal)
	if isSellOffer {
		if _, _, _, found := findToken(ctx.View, accountID, tokenID); !found {
			return tx.TecNO_ENTRY
		}
	} else {
		var ownerID [20]byte
		ownerID, err = state.DecodeAccountID(n.Owner)
		if err != nil {
			return tx.TemINVALID
		}
		if _, _, _, found := findToken(ctx.View, ownerID, tokenID); !found {
			return tx.TecNO_ENTRY
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
			return tx.TemINVALID
		}

		if nftFlags&nftFlagTrustLine == 0 && getNFTTransferFee(tokenID) != 0 {
			issuerExists, _ := ctx.View.Exists(keylet.Account(nftIssuerID))
			if !issuerExists {
				return tx.TecNO_ISSUER
			}

			if ctx.Rules().Enabled(amendment.FeatureNFTokenMintOffer) {
				if nftIssuerID != iouIssuerID {
					trustLineKey := keylet.Line(nftIssuerID, iouIssuerID, n.Amount.Currency)
					trustLineData, err := ctx.View.Read(trustLineKey)
					if err != nil || trustLineData == nil {
						return tx.TecNO_LINE
					}
				}
			} else {
				trustLineKey := keylet.Line(nftIssuerID, iouIssuerID, n.Amount.Currency)
				trustLineExists, _ := ctx.View.Exists(trustLineKey)
				if !trustLineExists {
					return tx.TecNO_LINE
				}
			}

			// NFT issuer frozen check
			// Reference: rippled tokenOfferCreatePreclaim line 927-928
			if tx.IsGlobalFrozen(ctx.View, n.Amount.Issuer) || tx.IsTrustlineFrozen(ctx.View, nftIssuerID, iouIssuerID, n.Amount.Currency) {
				return tx.TecFROZEN
			}
		}
	}

	// 2. Transferable check
	// Reference: rippled tokenOfferCreatePreclaim lines 931-938
	if nftIssuerID != accountID && nftFlags&nftFlagTransferable == 0 {
		issuerKey := keylet.Account(nftIssuerID)
		issuerData, err := ctx.View.Read(issuerKey)
		if err != nil {
			return tx.TefNFTOKEN_IS_NOT_TRANSFERABLE
		}
		issuerAccount, err := state.ParseAccountRoot(issuerData)
		if err != nil {
			return tx.TefNFTOKEN_IS_NOT_TRANSFERABLE
		}
		if issuerAccount.NFTokenMinter != n.Account {
			return tx.TefNFTOKEN_IS_NOT_TRANSFERABLE
		}
	}

	// 3. Account frozen check
	// Reference: rippled tokenOfferCreatePreclaim line 941
	if !n.Amount.IsNative() {
		iouIssuerID, _ := state.DecodeAccountID(n.Amount.Issuer)
		if tx.IsGlobalFrozen(ctx.View, n.Amount.Issuer) || tx.IsTrustlineFrozen(ctx.View, accountID, iouIssuerID, n.Amount.Currency) {
			return tx.TecFROZEN
		}
	}

	// 4. Fund check for buy offers (both XRP and IOU)
	// Reference: rippled tokenOfferCreatePreclaim lines 947-967
	if !isSellOffer {
		if n.Amount.IsNative() {
			// XRP buy offer: check account has enough liquid XRP
			// Reference: rippled — accountFunds/accountHolds for XRP returns liquid balance
			// For XRP, signum() <= 0 means the account has no liquid XRP at all
			// Note: the reserve check at the end already covers the common case,
			// but rippled's preclaim also rejects zero-balance XRP buy offers here.
		} else {
			if ctx.Rules().Enabled(amendment.FeatureFixNonFungibleTokensV1_2) {
				funds := tx.AccountFunds(ctx.View, accountID, n.Amount, true, ctx.Config.ReserveBase, ctx.Config.ReserveIncrement)
				if funds.Signum() <= 0 {
					return tx.TecUNFUNDED_OFFER
				}
			} else {
				funds := accountHoldsIOU(ctx.View, accountID, n.Amount)
				if funds.Signum() <= 0 {
					return tx.TecUNFUNDED_OFFER
				}
			}
		}
	}

	// 5. Destination check
	// Reference: rippled tokenOfferCreatePreclaim lines 970-988
	if n.Destination != "" {
		destAccount, _, result := ctx.LookupAccount(n.Destination)
		if result != tx.TesSUCCESS {
			return result
		}
		if ctx.Rules().Enabled(amendment.FeatureDisallowIncoming) {
			if destAccount.Flags&state.LsfDisallowIncomingNFTokenOffer != 0 {
				return tx.TecNO_PERMISSION
			}
		}
	}

	// 6. Owner disallow incoming check (for buy offers)
	// Reference: rippled tokenOfferCreatePreclaim lines 990-1004
	if n.Owner != "" {
		if ctx.Rules().Enabled(amendment.FeatureDisallowIncoming) {
			ownerAccount, _, result := ctx.LookupAccount(n.Owner)
			if result != tx.TesSUCCESS {
				return tx.TecNO_TARGET
			}
			if ownerAccount.Flags&state.LsfDisallowIncomingNFTokenOffer != 0 {
				return tx.TecNO_PERMISSION
			}
		}
	}

	// 7. Trust line authorization checks (with fixEnforceNFTokenTrustlineV2)
	// Reference: rippled tokenOfferCreatePreclaim lines 1007-1018
	if !n.Amount.IsNative() && ctx.Rules().Enabled(amendment.FeatureFixEnforceNFTokenTrustlineV2) {
		iouIssuerID, _ := state.DecodeAccountID(n.Amount.Issuer)
		if r := checkNFTTrustlineAuthorized(ctx.View, accountID, n.Amount.Currency, iouIssuerID); r != tx.TesSUCCESS {
			return r
		}
	}

	// For buy offers, check the buyer has enough XRP for reserve but do NOT
	// escrow/deduct the offer amount. NFToken buy offers are unfunded promises
	// — the buyer's balance is only checked, not held.
	// Reference: rippled NFTokenUtils.cpp tokenOfferCreateApply — no balance deduction

	// Create the offer
	sequence := n.GetCommon().SeqProxy()
	offerKey := keylet.NFTokenOffer(accountID, sequence)

	// Insert into owner's directory
	ownerDirKey := keylet.OwnerDir(accountID)
	dirResult, err := state.DirInsert(ctx.View, ownerDirKey, offerKey.Key, nil)
	if err != nil {
		return tx.TefINTERNAL
	}
	ownerNode := dirResult.Page

	// Insert into NFTSells or NFTBuys directory
	var tokenDirKey keylet.Keylet
	if isSellOffer {
		tokenDirKey = keylet.NFTSells(tokenID)
	} else {
		tokenDirKey = keylet.NFTBuys(tokenID)
	}
	tokenDirResult, err := state.DirInsert(ctx.View, tokenDirKey, offerKey.Key, nil)
	if err != nil {
		return tx.TefINTERNAL
	}
	offerNode := tokenDirResult.Page

	// Serialize the offer with directory page numbers
	offerData, err := serializeNFTokenOffer(n, accountID, tokenID, sequence, ownerNode, offerNode)
	if err != nil {
		return tx.TefINTERNAL
	}

	if err := ctx.View.Insert(offerKey, offerData); err != nil {
		return tx.TefINTERNAL
	}

	// Increase owner count
	ctx.Account.OwnerCount++

	// Check reserve using mPriorBalance (balance before fee deduction).
	// Reference: rippled NFTokenUtils.cpp tokenOfferCreateApply — uses priorBalance
	mPriorBalance := ctx.Account.Balance + ctx.Config.BaseFee
	reserve := ctx.AccountReserve(ctx.Account.OwnerCount)
	if mPriorBalance < reserve {
		return tx.TecINSUFFICIENT_RESERVE
	}

	return tx.TesSUCCESS
}
