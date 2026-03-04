package nftoken

import (
	"encoding/hex"
	"errors"

	"github.com/LeJamon/goXRPLd/internal/core/amendment"
	"github.com/LeJamon/goXRPLd/internal/core/ledger/keylet"
	"github.com/LeJamon/goXRPLd/internal/core/tx"
	"github.com/LeJamon/goXRPLd/internal/core/tx/sle"
)

func init() {
	tx.Register(tx.TypeNFTokenBurn, func() tx.Transaction {
		return &NFTokenBurn{BaseTx: *tx.NewBaseTx(tx.TypeNFTokenBurn, "")}
	})
}

// NFTokenBurn burns an NFToken.
type NFTokenBurn struct {
	tx.BaseTx

	// NFTokenID is the ID of the token to burn (required)
	NFTokenID string `json:"NFTokenID" xrpl:"NFTokenID"`

	// Owner is the owner of the token (optional, for authorized burns)
	Owner string `json:"Owner,omitempty" xrpl:"Owner,omitempty"`
}

// tfBurnNFToken is the only valid flag for NFTokenBurn (0x00000001)
const tfBurnNFToken uint32 = 0x00000001

// NewNFTokenBurn creates a new NFTokenBurn transaction
func NewNFTokenBurn(account, nftokenID string) *NFTokenBurn {
	return &NFTokenBurn{
		BaseTx:    *tx.NewBaseTx(tx.TypeNFTokenBurn, account),
		NFTokenID: nftokenID,
	}
}

// TxType returns the transaction type
func (n *NFTokenBurn) TxType() tx.Type {
	return tx.TypeNFTokenBurn
}

// Validate validates the NFTokenBurn transaction
// Reference: rippled NFTokenBurn.cpp preflight
func (n *NFTokenBurn) Validate() error {
	if err := n.BaseTx.Validate(); err != nil {
		return err
	}

	// Check for invalid flags
	if n.GetFlags()&^tfBurnNFToken != 0 {
		return errors.New("temINVALID_FLAG: invalid NFTokenBurn flags")
	}

	if n.NFTokenID == "" {
		return errors.New("temMALFORMED: NFTokenID is required")
	}

	// Owner must not be the same as Account
	if n.Owner != "" && n.Owner == n.Account {
		return errors.New("temMALFORMED: Owner cannot be the same as Account")
	}

	return nil
}

// Flatten returns a flat map of all transaction fields
func (n *NFTokenBurn) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(n)
}

// RequiredAmendments returns the amendments required for this transaction type
func (n *NFTokenBurn) RequiredAmendments() [][32]byte {
	return [][32]byte{amendment.FeatureNonFungibleTokensV1}
}

// Apply applies the NFTokenBurn transaction to the ledger.
// Reference: rippled NFTokenBurn.cpp doApply
func (b *NFTokenBurn) Apply(ctx *tx.ApplyContext) tx.Result {
	accountID := ctx.AccountID

	// Parse the token ID
	tokenIDBytes, err := hex.DecodeString(b.NFTokenID)
	if err != nil || len(tokenIDBytes) != 32 {
		return tx.TemINVALID
	}

	var tokenID [32]byte
	copy(tokenID[:], tokenIDBytes)

	// Determine the owner
	var ownerID [20]byte
	if b.Owner != "" {
		ownerID, err = sle.DecodeAccountID(b.Owner)
		if err != nil {
			return tx.TemINVALID
		}
	} else {
		ownerID = accountID
	}

	// Find the NFToken using proper page traversal
	if _, _, _, found := findToken(ctx.View, ownerID, tokenID); !found {
		return tx.TecNO_ENTRY
	}

	// Check if there are too many offers (preclaim check)
	// Reference: rippled NFTokenBurn.cpp preclaim — notTooManyOffers
	// Only applies when fixNonFungibleTokensV1_2 is NOT enabled
	fixV1_2 := ctx.Rules().Enabled(amendment.FeatureFixNonFungibleTokensV1_2)
	if !fixV1_2 {
		if r := notTooManyOffers(ctx.View, tokenID); r != tx.TesSUCCESS {
			return r
		}
	}

	// Verify burn authorization
	if ownerID != accountID {
		nftFlags := getNFTFlagsFromID(tokenID)
		if nftFlags&nftFlagBurnable == 0 {
			return tx.TecNO_PERMISSION
		}

		issuerID := getNFTIssuer(tokenID)
		if issuerID != accountID {
			issuerKey := keylet.Account(issuerID)
			issuerData, err := ctx.View.Read(issuerKey)
			if err != nil || issuerData == nil {
				return tx.TecNO_PERMISSION
			}
			issuerAccount, err := sle.ParseAccountRoot(issuerData)
			if err != nil {
				return tx.TefINTERNAL
			}
			if issuerAccount.NFTokenMinter != b.Account {
				return tx.TecNO_PERMISSION
			}
		}
	}

	// Remove the token using proper page management (handles merging)
	fixPageLinks := ctx.Rules().Enabled(amendment.FeatureFixNFTokenPageLinks)
	result, pagesRemoved := removeToken(ctx.View, ownerID, tokenID, fixPageLinks)
	if result != tx.TesSUCCESS {
		return result
	}

	// Update owner count for pages removed
	if ownerID != accountID {
		ownerKey := keylet.Account(ownerID)
		ownerData, err := ctx.View.Read(ownerKey)
		if err != nil || ownerData == nil {
			return tx.TefINTERNAL
		}
		ownerAccount, err := sle.ParseAccountRoot(ownerData)
		if err != nil {
			return tx.TefINTERNAL
		}
		for i := 0; i < pagesRemoved; i++ {
			if ownerAccount.OwnerCount > 0 {
				ownerAccount.OwnerCount--
			}
		}
		ownerUpdatedData, err := sle.SerializeAccountRoot(ownerAccount)
		if err != nil {
			return tx.TefINTERNAL
		}
		if err := ctx.View.Update(ownerKey, ownerUpdatedData); err != nil {
			return tx.TefINTERNAL
		}
	} else {
		for i := 0; i < pagesRemoved; i++ {
			if ctx.Account.OwnerCount > 0 {
				ctx.Account.OwnerCount--
			}
		}
	}

	// Update BurnedNFTokens on the issuer
	// When issuer == sender, modify ctx.Account directly (engine writes it back).
	// Otherwise, read/update via view.
	issuerID := getNFTIssuer(tokenID)
	if issuerID == ctx.AccountID {
		ctx.Account.BurnedNFTokens++
	} else {
		issuerKey := keylet.Account(issuerID)
		issuerData, err := ctx.View.Read(issuerKey)
		if err == nil {
			issuerAccount, err := sle.ParseAccountRoot(issuerData)
			if err == nil {
				issuerAccount.BurnedNFTokens++
				issuerUpdatedData, err := sle.SerializeAccountRoot(issuerAccount)
				if err == nil {
					ctx.View.Update(issuerKey, issuerUpdatedData)
				}
			}
		}
	}

	// Delete associated buy and sell offers
	// Reference: rippled NFTokenBurn.cpp:108-139
	selfDeleted := 0
	if !fixV1_2 {
		// Without fixNonFungibleTokensV1_2: delete ALL offers (no limit)
		// notTooManyOffers was already checked above
		r1 := deleteNFTokenOffers(tokenID, true, maxInt, ctx.View, ctx.AccountID)
		r2 := deleteNFTokenOffers(tokenID, false, maxInt, ctx.View, ctx.AccountID)
		selfDeleted = r1.SelfDeleted + r2.SelfDeleted
	} else {
		// With fixNonFungibleTokensV1_2: delete up to 500 offers
		// Prioritize sell offers (they're typically fewer)
		r1 := deleteNFTokenOffers(tokenID, true, maxDeletableTokenOfferEntries, ctx.View, ctx.AccountID)
		remaining := maxDeletableTokenOfferEntries - r1.TotalDeleted
		r2 := deleteNFTokenOffers(tokenID, false, remaining, ctx.View, ctx.AccountID)
		selfDeleted = r1.SelfDeleted + r2.SelfDeleted
	}

	// Adjust ctx.Account for offers owned by the burner
	// (view changes to ctx.Account are overwritten by the engine)
	for i := 0; i < selfDeleted; i++ {
		if ctx.Account.OwnerCount > 0 {
			ctx.Account.OwnerCount--
		}
	}

	return tx.TesSUCCESS
}
