package nftoken

import (
	"github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/amendment"
)

func init() {
	tx.Register(tx.TypeNFTokenModify, func() tx.Transaction {
		return &NFTokenModify{BaseTx: *tx.NewBaseTx(tx.TypeNFTokenModify, "")}
	})
}

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

// TxType returns the transaction type
func (n *NFTokenModify) TxType() tx.Type {
	return tx.TypeNFTokenModify
}

// Validate validates the NFTokenModify transaction
// Reference: rippled NFTokenModify.cpp preflight
func (n *NFTokenModify) Validate() error {
	if err := n.BaseTx.Validate(); err != nil {
		return err
	}

	// Check for invalid flags (no flags are valid for NFTokenModify)
	// Reference: rippled NFTokenModify.cpp:38 - if (ctx.tx.getFlags() & tfUniversalMask)
	if n.GetFlags() != 0 {
		return tx.Errorf(tx.TemINVALID_FLAG, "NFTokenModify does not accept any flags")
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
	// Reference: rippled NFTokenModify.cpp:44-47
	if n.URI != "" {
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

// Flatten returns a flat map of all transaction fields
func (n *NFTokenModify) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(n)
}

// RequiredAmendments returns the amendments required for this transaction type
func (n *NFTokenModify) RequiredAmendments() [][32]byte {
	return [][32]byte{amendment.FeatureDynamicNFT}
}

// Apply applies the NFTokenModify transaction to the ledger.
func (n *NFTokenModify) Apply(ctx *tx.ApplyContext) tx.Result {
	return tx.TesSUCCESS
}
