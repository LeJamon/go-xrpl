package nftoken

import (
	"encoding/hex"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/keylet"
)

// NFTokenBurn burns an NFToken.
type NFTokenBurn struct {
	tx.BaseTx

	// NFTokenID is the ID of the token to burn (required)
	NFTokenID string `json:"NFTokenID" xrpl:"NFTokenID"`

	// Owner is the owner of the token (optional, for authorized burns)
	Owner string `json:"Owner,omitempty" xrpl:"Owner,omitempty"`
}

// NewNFTokenBurn creates a new NFTokenBurn transaction
func NewNFTokenBurn(account, nftokenID string) *NFTokenBurn {
	return &NFTokenBurn{
		BaseTx:    *tx.NewBaseTx(tx.TypeNFTokenBurn, account),
		NFTokenID: nftokenID,
	}
}

func (n *NFTokenBurn) TxType() tx.Type {
	return tx.TypeNFTokenBurn
}

// Reference: rippled NFTokenBurn.cpp preflight
func (n *NFTokenBurn) Validate() error {
	if err := n.BaseTx.Validate(); err != nil {
		return err
	}

	if err := tx.CheckFlags(n.GetFlags(), tx.TfUniversalMask); err != nil {
		return tx.Errorf(tx.TemINVALID_FLAG, "invalid NFTokenBurn flags")
	}

	if n.NFTokenID == "" {
		return tx.Errorf(tx.TemMALFORMED, "NFTokenID is required")
	}

	return nil
}

func (n *NFTokenBurn) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(n)
}

func (n *NFTokenBurn) RequiredAmendments() [][32]byte {
	return [][32]byte{amendment.FeatureNonFungibleTokensV1}
}

// Reference: rippled NFTokenBurn.cpp doApply
func (n *NFTokenBurn) Apply(ctx *tx.ApplyContext) tx.Result {
	ctx.Log.Trace("nftoken burn apply",
		"account", n.Account,
		"tokenID", n.NFTokenID,
	)

	accountID := ctx.AccountID

	// Parse the token ID
	tokenIDBytes, err := hex.DecodeString(n.NFTokenID)
	if err != nil || len(tokenIDBytes) != 32 {
		return tx.TemINVALID
	}

	var tokenID [32]byte
	copy(tokenID[:], tokenIDBytes)

	// Determine the owner
	var ownerID [20]byte
	if n.Owner != "" {
		ownerID, err = state.DecodeAccountID(n.Owner)
		if err != nil {
			return tx.TemINVALID
		}
	} else {
		ownerID = accountID
	}

	// Find the NFToken using proper page traversal
	if _, _, _, found := findToken(ctx.View, ownerID, tokenID); !found {
		ctx.Log.Warn("nftoken burn: token not found",
			"tokenID", n.NFTokenID,
		)
		return tx.TecNO_ENTRY
	}

	// Verify burn authorization before any other preclaim check. The owner can
	// always burn its token; the issuer (or the issuer's authorized minter) may
	// burn only a token marked burnable.
	// Reference: rippled NFTokenBurn.cpp preclaim — authorization precedes the
	// offer-count check.
	if ownerID != accountID {
		nftFlags := getNFTFlagsFromID(tokenID)
		if nftFlags&nftFlagBurnable == 0 {
			return tx.TecNO_PERMISSION
		}

		issuerID := getNFTIssuer(tokenID)
		if issuerID != accountID {
			issuerKey := keylet.Account(issuerID)
			issuerData, err := ctx.View.Read(issuerKey)
			if err != nil {
				return tx.TefINTERNAL
			}
			// A missing issuer account cannot designate a minter, so the burn
			// proceeds; only an existing issuer's minter restriction applies.
			if issuerData != nil {
				issuerAccount, err := state.ParseAccountRoot(issuerData)
				if err != nil {
					return tx.TefINTERNAL
				}
				if issuerAccount.NFTokenMinter != n.Account {
					return tx.TecNO_PERMISSION
				}
			}
		}
	}

	// Reject burning a token carrying too many offers (it would produce too much
	// metadata). Only enforced before fixNonFungibleTokensV1_2.
	// Reference: rippled NFTokenBurn.cpp preclaim — notTooManyOffers.
	fixV1_2 := ctx.Rules().Enabled(amendment.FeatureFixNonFungibleTokensV1_2)
	if !fixV1_2 {
		if r := notTooManyOffers(ctx.View, tokenID); r != tx.TesSUCCESS {
			return r
		}
	}

	// Remove the token using proper page management (handles merging)
	fixPageLinks := ctx.Rules().Enabled(amendment.FeatureFixNFTokenPageLinks)
	result, pagesRemoved := removeToken(ctx.View, ownerID, tokenID, fixPageLinks)
	if result != tx.TesSUCCESS {
		return result
	}

	if ownerID != accountID {
		ownerKey := keylet.Account(ownerID)
		ownerData, err := ctx.View.Read(ownerKey)
		if err != nil || ownerData == nil {
			return tx.TefINTERNAL
		}
		ownerAccount, err := state.ParseAccountRoot(ownerData)
		if err != nil {
			return tx.TefINTERNAL
		}
		for range pagesRemoved {
			if ownerAccount.OwnerCount > 0 {
				ownerAccount.OwnerCount--
			}
		}
		if result := ctx.UpdateAccountRoot(ownerID, ownerAccount); result != tx.TesSUCCESS {
			return result
		}
	} else {
		for range pagesRemoved {
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
			issuerAccount, err := state.ParseAccountRoot(issuerData)
			if err == nil {
				issuerAccount.BurnedNFTokens++
				issuerUpdatedData, err := state.SerializeAccountRoot(issuerAccount)
				if err == nil {
					ctx.View.Update(issuerKey, issuerUpdatedData)
				}
			}
		}
	}

	// Reference: rippled NFTokenBurn.cpp:108-139
	selfDeleted := 0
	if !fixV1_2 {
		// Without fixNonFungibleTokensV1_2: delete ALL offers (no limit)
		// notTooManyOffers was already checked above
		r1, res := deleteNFTokenOffers(tokenID, true, maxInt, ctx.View, ctx.AccountID)
		if res != tx.TesSUCCESS {
			return res
		}
		r2, res := deleteNFTokenOffers(tokenID, false, maxInt, ctx.View, ctx.AccountID)
		if res != tx.TesSUCCESS {
			return res
		}
		selfDeleted = r1.SelfDeleted + r2.SelfDeleted
	} else {
		// With fixNonFungibleTokensV1_2: delete up to 500 offers
		// Prioritize sell offers (they're typically fewer)
		r1, res := deleteNFTokenOffers(tokenID, true, maxDeletableTokenOfferEntries, ctx.View, ctx.AccountID)
		if res != tx.TesSUCCESS {
			return res
		}
		remaining := maxDeletableTokenOfferEntries - r1.TotalDeleted
		r2, res := deleteNFTokenOffers(tokenID, false, remaining, ctx.View, ctx.AccountID)
		if res != tx.TesSUCCESS {
			return res
		}
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
