package did

import (
	"github.com/LeJamon/go-xrpl/amendment"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
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
func (d *DIDDelete) Apply(ctx *tx.ApplyContext) ter.Result {
	ctx.Log.Trace("did delete apply",
		"account", d.Account,
	)

	didKey := keylet.DID(ctx.AccountID)

	existingData, err := ctx.View.Read(didKey)
	if err != nil || existingData == nil {
		return ter.TecNO_ENTRY
	}

	did, err := state.ParseDID(existingData)
	if err != nil {
		return ter.TefINTERNAL
	}

	// Remove from owner directory, using the page recorded in sfOwnerNode so a
	// DID on a paginated owner directory (page > 0) is correctly unlinked.
	// Reference: rippled DID.cpp:207-208 dirRemove(ownerDir, (*sle)[sfOwnerNode], key, true).
	ownerDirKey := keylet.OwnerDir(ctx.AccountID)
	state.DirRemove(ctx.View, ownerDirKey, did.OwnerNode, didKey.Key, true)

	if err := ctx.View.Erase(didKey); err != nil {
		ctx.Log.Error("did delete: unable to delete DID from owner")
		return ter.TefINTERNAL
	}

	if ctx.Account.OwnerCount > 0 {
		ctx.Account.OwnerCount--
	}

	return ter.TesSUCCESS
}
