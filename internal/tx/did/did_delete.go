package did

import (
	"github.com/LeJamon/goXRPLd/amendment"

	"github.com/LeJamon/goXRPLd/internal/ledger/state"
	"github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/keylet"
)

// DIDDelete deletes a DID document.
type DIDDelete struct {
	tx.BaseTx
}

func NewDIDDelete(account string) *DIDDelete {
	return &DIDDelete{
		BaseTx: *tx.NewBaseTx(tx.TypeDIDDelete, account),
	}
}

func (d *DIDDelete) TxType() tx.Type {
	return tx.TypeDIDDelete
}

// Reference: rippled DID.cpp DIDDelete::preflight
func (d *DIDDelete) Validate() error {
	if err := d.BaseTx.Validate(); err != nil {
		return err
	}

	// Check for invalid flags (tfUniversalMask)
	if err := tx.CheckFlags(d.GetFlags(), tx.TfUniversalMask); err != nil {
		return err
	}

	return nil
}

func (d *DIDDelete) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(d)
}

func (d *DIDDelete) RequiredAmendments() [][32]byte {
	return [][32]byte{amendment.FeatureDID}
}

// Reference: rippled DID.cpp DIDDelete::doApply
func (d *DIDDelete) Apply(ctx *tx.ApplyContext) tx.Result {
	ctx.Log.Trace("did delete apply",
		"account", d.Account,
	)

	didKey := keylet.DID(ctx.AccountID)

	existingData, err := ctx.View.Read(didKey)
	if err != nil || existingData == nil {
		return tx.TecNO_ENTRY
	}

	// Remove from owner directory
	// Reference: rippled DID.cpp deleteSLE → dirRemove
	ownerDirKey := keylet.OwnerDir(ctx.AccountID)
	state.DirRemove(ctx.View, ownerDirKey, 0, didKey.Key, true)

	if err := ctx.View.Erase(didKey); err != nil {
		ctx.Log.Error("did delete: unable to delete DID from owner")
		return tx.TefINTERNAL
	}

	if ctx.Account.OwnerCount > 0 {
		ctx.Account.OwnerCount--
	}

	return tx.TesSUCCESS
}
