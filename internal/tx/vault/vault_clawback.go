package vault

import (
	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

type VaultClawback struct {
	tx.BaseTx

	// VaultID is the ID of the vault (required)
	VaultID string `json:"VaultID" xrpl:"VaultID"`

	// Holder is the holder to claw back from (required)
	Holder string `json:"Holder" xrpl:"Holder"`

	// Amount is the amount to claw back (optional)
	Amount *tx.Amount `json:"Amount,omitempty" xrpl:"Amount,omitempty,amount"`
}

// NewVaultClawback creates a new VaultClawback transaction
func NewVaultClawback(account, vaultID, holder string) *VaultClawback {
	return &VaultClawback{
		BaseTx:  *tx.NewBaseTx(tx.TypeVaultClawback, account),
		VaultID: vaultID,
		Holder:  holder,
	}
}

func (v *VaultClawback) TxType() tx.Type {
	return tx.TypeVaultClawback
}

// Reference: rippled VaultClawback.cpp preflight()
func (v *VaultClawback) Validate() error {
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

	// Holder is required
	if v.Holder == "" {
		return ErrVaultHolderRequired
	}

	// Holder cannot be the same as issuer (Account)
	if v.Holder == v.Account {
		return ErrVaultHolderIsSelf
	}

	// Validate Amount if present
	if v.Amount != nil {
		// Zero amount is valid (means "all"); negative is not.
		if v.Amount.Signum() < 0 {
			return ErrVaultAmountNotPos
		}
		// Cannot clawback XRP
		if v.Amount.IsNative() {
			return ErrVaultAmountXRP
		}
		// Asset issuer must match Account
		if v.Amount.Issuer != "" && v.Amount.Issuer != v.Account {
			return ErrVaultAmountNotIssuer
		}
	}

	return nil
}

func (v *VaultClawback) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(v)
}

func (v *VaultClawback) RequiredAmendments() [][32]byte {
	return [][32]byte{amendment.FeatureSingleAssetVault}
}

// Apply is intentionally unimplemented. See VaultCreate.Apply.
func (v *VaultClawback) Apply(ctx *tx.ApplyContext) ter.Result {
	ctx.Log.Trace("vault clawback apply: not implemented", "account", v.Account)
	return ter.TefINTERNAL
}
