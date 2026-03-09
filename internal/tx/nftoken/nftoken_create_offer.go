package nftoken

import (
	"encoding/hex"
	"errors"
	"github.com/LeJamon/goXRPLd/keylet"
	"github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/amendment"
	"github.com/LeJamon/goXRPLd/internal/ledger/state"
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
func (n *NFTokenCreateOffer) Validate() error {
	if err := n.BaseTx.Validate(); err != nil {
		return err
	}

	// Check for invalid flags
	if n.GetFlags()&tfNFTokenCreateOfferMask != 0 {
		return errors.New("temINVALID_FLAG: invalid NFTokenCreateOffer flags")
	}

	if n.NFTokenID == "" {
		return errors.New("temMALFORMED: NFTokenID is required")
	}

	// Parse NFToken flags from token ID to validate
	nftFlags := getNFTokenFlags(n.NFTokenID)

	isSellOffer := n.GetFlags()&NFTokenCreateOfferFlagSellNFToken != 0

	// Buy offers must have Owner
	if !isSellOffer && n.Owner == "" {
		return errors.New("temMALFORMED: Owner is required for buy offers")
	}

	// Sell offers cannot specify Owner
	if isSellOffer && n.Owner != "" {
		return errors.New("temMALFORMED: Owner not allowed for sell offers")
	}

	// Owner cannot be the same as Account
	// Reference: rippled tokenOfferCreatePreflight — "if (owner && owner == acctID)"
	if n.Owner != "" && n.Owner == n.Account {
		return errors.New("temMALFORMED: Owner cannot be the same as Account")
	}

	// Destination cannot be the same as the account creating the offer
	if n.Destination != "" && n.Destination == n.Account {
		return errors.New("temMALFORMED: Destination cannot be the same as Account")
	}

	// Expiration validation - expiration of 0 is invalid
	if n.Expiration != nil && *n.Expiration == 0 {
		return errors.New("temBAD_EXPIRATION: Expiration cannot be 0")
	}

	// Amount validation
	if n.Amount.Currency == "" {
		// XRP amount
		// For buy offers, zero amount is not allowed
		if !isSellOffer && n.Amount.IsZero() {
			return errors.New("temBAD_AMOUNT: buy offer amount cannot be zero")
		}
	} else {
		// IOU amount - check if OnlyXRP flag is set on the token
		if nftFlags&nftFlagOnlyXRP != 0 {
			return errors.New("temBAD_AMOUNT: NFToken requires XRP only")
		}
		// IOU amount of 0 is not allowed
		if n.Amount.IsZero() {
			return errors.New("temBAD_AMOUNT: IOU amount cannot be zero")
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
func (c *NFTokenCreateOffer) Apply(ctx *tx.ApplyContext) tx.Result {
	accountID := ctx.AccountID

	// Parse token ID
	tokenIDBytes, err := hex.DecodeString(c.NFTokenID)
	if err != nil || len(tokenIDBytes) != 32 {
		return tx.TemINVALID
	}

	var tokenID [32]byte
	copy(tokenID[:], tokenIDBytes)

	// Check expiration
	if c.Expiration != nil && *c.Expiration <= ctx.Config.ParentCloseTime {
		return tx.TecEXPIRED
	}

	isSellOffer := c.GetFlags()&NFTokenCreateOfferFlagSellNFToken != 0

	// Verify token ownership using findToken (proper page traversal)
	if isSellOffer {
		if _, _, _, found := findToken(ctx.View, accountID, tokenID); !found {
			return tx.TecNO_ENTRY
		}
	} else {
		var ownerID [20]byte
		ownerID, err = state.DecodeAccountID(c.Owner)
		if err != nil {
			return tx.TemINVALID
		}
		if _, _, _, found := findToken(ctx.View, ownerID, tokenID); !found {
			return tx.TecNO_ENTRY
		}
	}

	// Check transferable flag for ALL offers (buy and sell)
	// Reference: rippled tokenOfferCreatePreclaim — if issuer != account and
	// token is not transferable, only the issuer or authorized minter may create offers
	nftFlags := getNFTFlagsFromID(tokenID)
	issuerID := getNFTIssuer(tokenID)
	if issuerID != accountID && nftFlags&nftFlagTransferable == 0 {
		// Not transferable — only issuer's authorized minter can create offers
		issuerKey := keylet.Account(issuerID)
		issuerData, err := ctx.View.Read(issuerKey)
		if err != nil {
			return tx.TefNFTOKEN_IS_NOT_TRANSFERABLE
		}
		issuerAccount, err := state.ParseAccountRoot(issuerData)
		if err != nil {
			return tx.TefNFTOKEN_IS_NOT_TRANSFERABLE
		}
		if issuerAccount.NFTokenMinter != c.Account {
			return tx.TefNFTOKEN_IS_NOT_TRANSFERABLE
		}
	}

	// Check destination exists and doesn't disallow incoming NFT offers
	// Reference: rippled tokenOfferCreatePreclaim
	if c.Destination != "" {
		destID, err := state.DecodeAccountID(c.Destination)
		if err != nil {
			return tx.TemINVALID
		}
		destKey := keylet.Account(destID)
		destData, err := ctx.View.Read(destKey)
		if err != nil || destData == nil {
			return tx.TecNO_DST
		}
		destAccount, err := state.ParseAccountRoot(destData)
		if err != nil {
			return tx.TefINTERNAL
		}
		if destAccount.Flags&state.LsfDisallowIncomingNFTokenOffer != 0 {
			return tx.TecNO_PERMISSION
		}
	}

	// IOU preclaim checks
	// Reference: rippled tokenOfferCreatePreclaim — IOU-specific validation
	if !c.Amount.IsNative() {
		iouIssuerID, err := state.DecodeAccountID(c.Amount.Issuer)
		if err != nil {
			return tx.TemINVALID
		}

		// Fund check for buy offers
		// Reference: rippled tokenOfferCreatePreclaim — checks signum() <= 0,
		// i.e., only rejects if buyer has ZERO balance, not if they can't fully afford.
		// With fixNonFungibleTokensV1_2 uses accountFunds (allows issuer unlimited),
		// without it uses accountHolds (issuer has no special treatment)
		if !isSellOffer {
			if ctx.Rules().Enabled(amendment.FeatureFixNonFungibleTokensV1_2) {
				funds := tx.AccountFunds(ctx.View, accountID, c.Amount, true, ctx.Config.ReserveBase, ctx.Config.ReserveIncrement)
				if funds.Signum() <= 0 {
					return tx.TecUNFUNDED_OFFER
				}
			} else {
				funds := accountHoldsIOU(ctx.View, accountID, c.Amount)
				if funds.Signum() <= 0 {
					return tx.TecUNFUNDED_OFFER
				}
			}
		}

		// Trust line authorization checks (with fixEnforceNFTokenTrustlineV2)
		if ctx.Rules().Enabled(amendment.FeatureFixEnforceNFTokenTrustlineV2) {
			if r := checkNFTTrustlineAuthorized(ctx.View, accountID, c.Amount.Currency, iouIssuerID); r != tx.TesSUCCESS {
				return r
			}
		}

		// NFT issuer must have trust line if transfer fee is set
		// Reference: rippled tokenOfferCreatePreclaim — only check trust line EXISTENCE
		// (not authorization). Auth for NFT issuer is checked at acceptance time, not creation.
		// With featureNFTokenMintOffer, skip check when NFT issuer == IOU issuer
		// (issuer can receive their own IOU as transfer fee without a trust line)
		nftIssuerID := getNFTIssuer(tokenID)
		if getNFTTransferFee(tokenID) != 0 && nftFlags&nftFlagTrustLine == 0 {
			skipCheck := nftIssuerID == iouIssuerID && ctx.Rules().Enabled(amendment.FeatureNFTokenMintOffer)
			if !skipCheck {
				trustLineKey := keylet.Line(nftIssuerID, iouIssuerID, c.Amount.Currency)
				trustLineExists, _ := ctx.View.Exists(trustLineKey)
				if !trustLineExists {
					return tx.TecNO_LINE
				}
			}
		}
	}

	// For buy offers, check the buyer has enough XRP for reserve but do NOT
	// escrow/deduct the offer amount. NFToken buy offers are unfunded promises
	// — the buyer's balance is only checked, not held.
	// Reference: rippled NFTokenUtils.cpp tokenOfferCreateApply — no balance deduction

	// Create the offer
	sequence := c.GetCommon().SeqProxy()
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
	offerData, err := serializeNFTokenOffer(c, accountID, tokenID, sequence, ownerNode, offerNode)
	if err != nil {
		return tx.TefINTERNAL
	}

	if err := ctx.View.Insert(offerKey, offerData); err != nil {
		return tx.TefINTERNAL
	}

	// Increase owner count
	ctx.Account.OwnerCount++

	// Check reserve
	reserve := ctx.AccountReserve(ctx.Account.OwnerCount)
	if ctx.Account.Balance < reserve {
		return tx.TecINSUFFICIENT_RESERVE
	}

	return tx.TesSUCCESS
}
