package delegate

import (
	"github.com/LeJamon/goXRPLd/keylet"
	"github.com/LeJamon/goXRPLd/internal/core/tx"
	"github.com/LeJamon/goXRPLd/amendment"
	"github.com/LeJamon/goXRPLd/internal/ledger/state"
)

func init() {
	tx.Register(tx.TypeDelegateSet, func() tx.Transaction {
		return &DelegateSet{BaseTx: *tx.NewBaseTx(tx.TypeDelegateSet, "")}
	})
}

// DelegateSet sets up delegation for an account.
type DelegateSet struct {
	tx.BaseTx

	// Authorize is the account to delegate to (optional)
	Authorize string `json:"Authorize,omitempty" xrpl:"Authorize,omitempty"`

	// Permissions defines what the delegate can do (optional)
	Permissions []Permission `json:"Permissions,omitempty" xrpl:"Permissions,omitempty"`
}

// Permission defines a permission grant
type Permission struct {
	Permission PermissionData `json:"Permission"`
}

// PermissionData contains permission details
type PermissionData struct {
	PermissionType   string `json:"PermissionType"`
	PermissionValue  string `json:"PermissionValue,omitempty"`
	PermissionedFlag uint32 `json:"PermissionedFlag,omitempty"`
}

// NewDelegateSet creates a new DelegateSet transaction
func NewDelegateSet(account string) *DelegateSet {
	return &DelegateSet{
		BaseTx: *tx.NewBaseTx(tx.TypeDelegateSet, account),
	}
}

// TxType returns the transaction type
func (d *DelegateSet) TxType() tx.Type {
	return tx.TypeDelegateSet
}

// Validate validates the DelegateSet transaction
func (d *DelegateSet) Validate() error {
	return d.BaseTx.Validate()
}

// Flatten returns a flat map of all transaction fields
func (d *DelegateSet) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(d)
}

// RequiredAmendments returns the amendments required for this transaction type
func (d *DelegateSet) RequiredAmendments() [][32]byte {
	return [][32]byte{amendment.FeaturePermissionDelegation}
}

// Apply applies the DelegateSet transaction to the ledger.
func (d *DelegateSet) Apply(ctx *tx.ApplyContext) tx.Result {
	if d.Authorize != "" {
		delegateID, err := state.DecodeAccountID(d.Authorize)
		if err != nil {
			return tx.TecNO_TARGET
		}
		var delegateKey [32]byte
		copy(delegateKey[:20], ctx.AccountID[:])
		copy(delegateKey[20:], delegateID[:12])
		delegateKeylet := keylet.Keylet{Key: delegateKey, Type: 0x0083}
		delegateData := make([]byte, 40)
		copy(delegateData[:20], ctx.AccountID[:])
		copy(delegateData[20:40], delegateID[:])
		if err := ctx.View.Insert(delegateKeylet, delegateData); err != nil {
			ctx.View.Update(delegateKeylet, delegateData)
		}
	}
	return tx.TesSUCCESS
}
