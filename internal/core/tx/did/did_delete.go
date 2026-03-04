package did

import (
	"github.com/LeJamon/goXRPLd/internal/core/amendment"

	"github.com/LeJamon/goXRPLd/internal/core/ledger/keylet"
	"github.com/LeJamon/goXRPLd/internal/core/tx"
	"github.com/LeJamon/goXRPLd/internal/core/tx/sle"
)

func init() {
	tx.Register(tx.TypeDIDDelete, func() tx.Transaction {
		return &DIDDelete{BaseTx: *tx.NewBaseTx(tx.TypeDIDDelete, "")}
	})
}

// DIDDelete deletes a DID document.
type DIDDelete struct {
	tx.BaseTx
}

// NewDIDDelete creates a new DIDDelete transaction
func NewDIDDelete(account string) *DIDDelete {
	return &DIDDelete{
		BaseTx: *tx.NewBaseTx(tx.TypeDIDDelete, account),
	}
}

// TxType returns the transaction type
func (d *DIDDelete) TxType() tx.Type {
	return tx.TypeDIDDelete
}

// Validate validates the DIDDelete transaction
// Reference: rippled DID.cpp DIDDelete::preflight
func (d *DIDDelete) Validate() error {
	if err := d.BaseTx.Validate(); err != nil {
		return err
	}

	// Check for invalid flags (tfUniversalMask)
	flags := d.GetFlags()
	if flags&tx.TfUniversalMask != 0 {
		return tx.ErrInvalidFlags
	}

	return nil
}

// Flatten returns a flat map of all transaction fields
func (d *DIDDelete) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(d)
}

// RequiredAmendments returns the amendments required for this transaction type
func (d *DIDDelete) RequiredAmendments() [][32]byte {
	return [][32]byte{amendment.FeatureDID}
}

// Apply applies a DIDDelete transaction to the ledger state.
// Reference: rippled DID.cpp DIDDelete::doApply
func (d *DIDDelete) Apply(ctx *tx.ApplyContext) tx.Result {
	didKey := keylet.DID(ctx.AccountID)

	existingData, err := ctx.View.Read(didKey)
	if err != nil || existingData == nil {
		return tx.TecNO_ENTRY
	}

	// Remove from owner directory
	// Reference: rippled DID.cpp deleteSLE → dirRemove
	ownerDirKey := keylet.OwnerDir(ctx.AccountID)
	sle.DirRemove(ctx.View, ownerDirKey, 0, didKey.Key, true)

	// Delete the DID entry
	if err := ctx.View.Erase(didKey); err != nil {
		return tx.TefINTERNAL
	}

	if ctx.Account.OwnerCount > 0 {
		ctx.Account.OwnerCount--
	}

	return tx.TesSUCCESS
}
