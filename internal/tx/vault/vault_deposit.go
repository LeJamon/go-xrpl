package vault

import (
	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

// VaultDeposit deposits assets into a vault.
type VaultDeposit struct {
	tx.BaseTx

	// VaultID is the ID of the vault (required)
	VaultID string `json:"VaultID" xrpl:"VaultID"`

	// Amount is the amount to deposit (required)
	Amount tx.Amount `json:"Amount" xrpl:"Amount,amount"`
}

// NewVaultDeposit creates a new VaultDeposit transaction
func NewVaultDeposit(account, vaultID string, amount tx.Amount) *VaultDeposit {
	return &VaultDeposit{
		BaseTx:  *tx.NewBaseTx(tx.TypeVaultDeposit, account),
		VaultID: vaultID,
		Amount:  amount,
	}
}

func (v *VaultDeposit) TxType() tx.Type {
	return tx.TypeVaultDeposit
}

// Reference: rippled VaultDeposit.cpp preflight()
func (v *VaultDeposit) Validate() error {
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
		return ter.Errorf(ter.TemMALFORMED, "VaultID must be a valid 256-bit hash")
	}

	// Amount must be positive — rejects default, explicit zero, and negative.
	if v.Amount.Signum() <= 0 {
		return ErrVaultAmountNotPos
	}

	return nil
}

func (v *VaultDeposit) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(v)
}

func (v *VaultDeposit) RequiredAmendments() [][32]byte {
	return [][32]byte{amendment.FeatureSingleAssetVault}
}

// Apply is intentionally unimplemented. See VaultCreate.Apply.
func (v *VaultDeposit) Apply(ctx *tx.ApplyContext) ter.Result {
	ctx.Log.Trace("vault deposit apply: not implemented", "account", v.Account)
	return ter.TefINTERNAL
}
