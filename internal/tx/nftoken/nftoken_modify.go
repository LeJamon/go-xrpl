package nftoken

import (
	"encoding/hex"

	"github.com/LeJamon/goXRPLd/amendment"
	"github.com/LeJamon/goXRPLd/internal/ledger/state"
	"github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/keylet"
)

// NFTokenModify modifies an existing NFToken.
type NFTokenModify struct {
	tx.BaseTx

	// NFTokenID is the ID of the token to modify (required)
	NFTokenID string `json:"NFTokenID" xrpl:"NFTokenID"`

	// Owner is the owner of the token (optional)
	Owner string `json:"Owner,omitempty" xrpl:"Owner,omitempty"`

	// URI is the new URI for the token (optional)
	URI string `json:"URI,omitempty" xrpl:"URI,omitempty"`
}

// NewNFTokenModify creates a new NFTokenModify transaction
func NewNFTokenModify(account, nftokenID string) *NFTokenModify {
	return &NFTokenModify{
		BaseTx:    *tx.NewBaseTx(tx.TypeNFTokenModify, account),
		NFTokenID: nftokenID,
	}
}

func (n *NFTokenModify) TxType() tx.Type {
	return tx.TypeNFTokenModify
}

// Reference: rippled NFTokenModify.cpp preflight
func (n *NFTokenModify) Validate() error {
	if err := n.BaseTx.Validate(); err != nil {
		return err
	}

	// Reference: rippled NFTokenModify.cpp:38 - if (ctx.tx.getFlags() & tfUniversalMask)
	if err := tx.CheckFlags(n.GetFlags(), tx.TfUniversalMask); err != nil {
		return err
	}

	if n.NFTokenID == "" {
		return tx.Errorf(tx.TemMALFORMED, "NFTokenID is required")
	}

	// Owner cannot be the same as Account
	// Reference: rippled NFTokenModify.cpp:41 - if (auto owner = ctx.tx[~sfOwner]; owner == ctx.tx[sfAccount])
	if n.Owner != "" && n.Owner == n.Account {
		return tx.Errorf(tx.TemMALFORMED, "Owner cannot be the same as Account")
	}

	// URI validation: if present, must not be empty and not exceed maxTokenURILength
	// Reference: rippled NFTokenModify.cpp:44-47 - if (auto uri = ctx.tx[~sfURI])
	if n.URI != "" || n.HasField("URI") {
		// URI in transactions is hex-encoded, so actual byte length is len/2
		uriBytes := len(n.URI) / 2
		if uriBytes == 0 {
			return tx.Errorf(tx.TemMALFORMED, "URI cannot be empty")
		}
		if uriBytes > maxTokenURILength {
			return tx.Errorf(tx.TemMALFORMED, "URI too long")
		}
	}

	return nil
}

func (n *NFTokenModify) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(n)
}

// Reference: rippled NFTokenModify.cpp preflight — requires both NonFungibleTokensV1_1 and DynamicNFT.
func (n *NFTokenModify) RequiredAmendments() [][32]byte {
	return [][32]byte{amendment.FeatureNonFungibleTokensV1_1, amendment.FeatureDynamicNFT}
}

// Reference: rippled NFTokenModify.cpp preclaim + doApply
func (n *NFTokenModify) Apply(ctx *tx.ApplyContext) tx.Result {
	ctx.Log.Trace("nftoken modify apply",
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

	// Determine the owner: if Owner is present use it, otherwise use Account
	// Reference: rippled NFTokenModify.cpp preclaim:57-58
	var ownerID [20]byte
	if n.Owner != "" {
		ownerID, err = state.DecodeAccountID(n.Owner)
		if err != nil {
			return tx.TemINVALID
		}
	} else {
		ownerID = accountID
	}

	// --- Preclaim checks ---

	// Verify the token exists
	// Reference: rippled NFTokenModify.cpp preclaim:60
	if _, _, _, found := findToken(ctx.View, ownerID, tokenID); !found {
		return tx.TecNO_ENTRY
	}

	// Reference: rippled NFTokenModify.cpp preclaim:64
	if getNFTFlagsFromID(tokenID)&nftFlagMutable == 0 {
		return tx.TecNO_PERMISSION
	}

	// Verify permissions: account must be the issuer or the issuer's authorized minter
	// Reference: rippled NFTokenModify.cpp preclaim:68-76
	issuerID := getNFTIssuer(tokenID)
	if issuerID != accountID {
		issuerKey := keylet.Account(issuerID)
		issuerData, err := ctx.View.Read(issuerKey)
		if err != nil || issuerData == nil {
			return tx.TecINTERNAL
		}
		issuerAccount, err := state.ParseAccountRoot(issuerData)
		if err != nil {
			return tx.TecINTERNAL
		}
		if issuerAccount.NFTokenMinter != n.Account {
			return tx.TecNO_PERMISSION
		}
	}

	// --- doApply: changeTokenURI ---
	// Reference: rippled NFTokenUtils.cpp changeTokenURI

	// Locate the page containing the token
	kl, page, locateErr := locatePage(ctx.View, ownerID, tokenID)
	if locateErr != nil || page == nil {
		return tx.TecINTERNAL
	}

	// Find the token in the page
	tokenIdx := -1
	for i, t := range page.NFTokens {
		if t.NFTokenID == tokenID {
			tokenIdx = i
			break
		}
	}
	if tokenIdx == -1 {
		return tx.TecINTERNAL
	}

	// Reference: rippled NFTokenModify.cpp doApply:88 — ctx_.tx[~sfURI]
	// If URI is present in the tx, set it on the token.
	// If URI is absent, remove the existing URI from the token.
	if n.HasField("URI") || n.URI != "" {
		// URI is present — set it
		page.NFTokens[tokenIdx].URI = n.URI
	} else {
		// URI is absent — remove it
		page.NFTokens[tokenIdx].URI = ""
	}

	// Serialize and update the page
	pageBytes, err := serializeNFTokenPage(page)
	if err != nil {
		return tx.TecINTERNAL
	}
	if err := ctx.View.Update(kl, pageBytes); err != nil {
		return tx.TecINTERNAL
	}

	return tx.TesSUCCESS
}
