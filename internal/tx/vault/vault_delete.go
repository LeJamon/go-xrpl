package vault

import (
	"encoding/hex"

	"github.com/LeJamon/goXRPLd/keylet"
	"github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/amendment"
)

func init() {
	tx.Register(tx.TypeVaultDelete, func() tx.Transaction {
		return &VaultDelete{BaseTx: *tx.NewBaseTx(tx.TypeVaultDelete, "")}
	})
}

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

// TxType returns the transaction type
func (v *VaultDelete) TxType() tx.Type {
	return tx.TypeVaultDelete
}

// Validate validates the VaultDelete transaction
// Reference: rippled VaultDelete.cpp preflight()
func (v *VaultDelete) Validate() error {
	if err := v.BaseTx.Validate(); err != nil {
		return err
	}

	// Check for invalid flags (universal mask)
	// Reference: rippled VaultDelete.cpp:39-40
	if err := tx.CheckFlags(v.GetFlags(), tx.TfUniversalMask); err != nil {
		return err
	}

	// VaultID is required and cannot be zero
	// Reference: rippled VaultDelete.cpp:42-46
	if v.VaultID == "" {
		return ErrVaultIDRequired
	}
	vaultBytes, err := hex.DecodeString(v.VaultID)
	if err != nil || len(vaultBytes) != 32 {
		return tx.Errorf(tx.TemMALFORMED, "VaultID must be a valid 256-bit hash")
	}
	isZero := true
	for _, b := range vaultBytes {
		if b != 0 {
			isZero = false
			break
		}
	}
	if isZero {
		return ErrVaultIDZero
	}

	return nil
}

// Flatten returns a flat map of all transaction fields
func (v *VaultDelete) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(v)
}

// RequiredAmendments returns the amendments required for this transaction type
func (v *VaultDelete) RequiredAmendments() [][32]byte {
	return [][32]byte{amendment.FeatureSingleAssetVault}
}

// Apply applies the VaultDelete transaction to the ledger.
func (v *VaultDelete) Apply(ctx *tx.ApplyContext) tx.Result {
	if v.VaultID == "" {
		return tx.TemINVALID
	}
	vaultBytes, err := hex.DecodeString(v.VaultID)
	if err != nil || len(vaultBytes) != 32 {
		return tx.TemINVALID
	}
	var vaultKey [32]byte
	copy(vaultKey[:], vaultBytes)
	vaultKeylet := keylet.Keylet{Key: vaultKey, Type: 0x0084}
	if err := ctx.View.Erase(vaultKeylet); err != nil {
		return tx.TecNO_ENTRY
	}
	if ctx.Account.OwnerCount > 0 {
		ctx.Account.OwnerCount--
	}
	return tx.TesSUCCESS
}
