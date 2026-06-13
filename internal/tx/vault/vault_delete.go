package vault

import (
	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/tx"
)

// VaultDelete deletes a vault.
type VaultDelete struct {
	tx.BaseTx

	// VaultID is the ID of the vault to delete (required)
	VaultID string `json:"VaultID" xrpl:"VaultID"`
}

// NewVaultDelete creates a new VaultDelete transaction
func NewVaultDelete(account, vaultID string) *VaultDelete {
	return &VaultDelete{
		BaseTx:  *tx.NewBaseTx(tx.TypeVaultDelete, account),
		VaultID: vaultID,
	}
}

func (v *VaultDelete) TxType() tx.Type {
	return tx.TypeVaultDelete
}

// Reference: rippled VaultDelete.cpp preflight()
func (v *VaultDelete) Validate() error {
	if err := v.BaseTx.Validate(); err != nil {
		return err
	}

	// Check for invalid flags (universal mask)
	if err := tx.CheckFlags(v.GetFlags(), tx.TfUniversalMask); err != nil {
		return err
	}

	// VaultID is required and cannot be zero
	if v.VaultID == "" {
		return ErrVaultIDRequired
	}
	if _, err := tx.ParseHash256NonZero(v.VaultID); err != nil {
		if isZeroHash(v.VaultID) {
			return ErrVaultIDZero
		}
		return tx.Errorf(tx.TemMALFORMED, "VaultID must be a valid 256-bit hash")
	}

	return nil
}

func (v *VaultDelete) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(v)
}

func (v *VaultDelete) RequiredAmendments() [][32]byte {
	return [][32]byte{amendment.FeatureSingleAssetVault}
}

// Apply is intentionally unimplemented. See VaultCreate.Apply.
func (v *VaultDelete) Apply(ctx *tx.ApplyContext) tx.Result {
	ctx.Log.Trace("vault delete apply: not implemented", "account", v.Account)
	return tx.TefINTERNAL
}
