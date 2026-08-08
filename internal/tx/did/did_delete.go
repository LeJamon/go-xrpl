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
// GetFlagsMask adopts the engine FlagsMasker seam. DIDDelete defines no
// type-specific flags, so it uses the base universal mask, checked at preflight0.
func (d *DIDDelete) GetFlagsMask(rules *amendment.Rules) uint32 {
	return tx.TfUniversalMask
}

func (d *DIDDelete) Validate() error {
	return d.BaseTx.Validate()
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
	if err != nil {
		return ctx.Internal("DIDDelete.Read", err)
	}
	if existingData == nil {
		return ter.TecNO_ENTRY
	}

	did, err := state.ParseDID(existingData)
	if err != nil {
		return ter.TefINTERNAL
	}

	ownerDirKey := keylet.OwnerDir(ctx.AccountID)
	if result := tx.DirRemoveOrBadLedger(ctx.View, ownerDirKey, did.OwnerNode, didKey.Key); result != ter.TesSUCCESS {
		return result
	}

	if err := ctx.View.Erase(didKey); err != nil {
		ctx.Log.Error("did delete: unable to delete DID from owner")
		return ter.TefINTERNAL
	}

	if ctx.Account.OwnerCount == 0 {
		ctx.Log.Error("did delete: owner count underflow", "account", d.Account)
	}
	ctx.Account.OwnerCount = tx.ConfineOwnerCount(ctx.Account.OwnerCount, -1)

	return ter.TesSUCCESS
}
