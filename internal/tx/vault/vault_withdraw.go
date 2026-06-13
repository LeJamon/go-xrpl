package vault

import (
	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
)

// VaultWithdraw withdraws assets from a vault.
type VaultWithdraw struct {
	tx.BaseTx

	// VaultID is the ID of the vault (required)
	VaultID string `json:"VaultID" xrpl:"VaultID"`

	// Amount is the amount to withdraw (required)
	Amount tx.Amount `json:"Amount" xrpl:"Amount,amount"`

	// Destination is the destination account (optional)
	Destination string `json:"Destination,omitempty" xrpl:"Destination,omitempty"`

	// DestinationTag is the destination tag (optional)
	DestinationTag *uint32 `json:"DestinationTag,omitempty" xrpl:"DestinationTag,omitempty"`
}

// NewVaultWithdraw creates a new VaultWithdraw transaction
func NewVaultWithdraw(account, vaultID string, amount tx.Amount) *VaultWithdraw {
	return &VaultWithdraw{
		BaseTx:  *tx.NewBaseTx(tx.TypeVaultWithdraw, account),
		VaultID: vaultID,
		Amount:  amount,
	}
}

func (v *VaultWithdraw) TxType() tx.Type {
	return tx.TypeVaultWithdraw
}

// Reference: rippled VaultWithdraw.cpp preflight()
func (v *VaultWithdraw) Validate() error {
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

	// Amount must be positive — rejects default, explicit zero, and negative.
	if v.Amount.Signum() <= 0 {
		return ErrVaultAmountNotPos
	}

	// A present Destination must not be the zero account. When no Destination
	// is given, a DestinationTag is meaningless and rejected.
	if v.Destination != "" {
		if id, err := state.DecodeAccountID(v.Destination); err == nil && id == ([20]byte{}) {
			return ErrVaultDestZero
		}
	} else if v.DestinationTag != nil {
		return ErrVaultDestTagNoAccount
	}

	return nil
}

func (v *VaultWithdraw) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(v)
}

func (v *VaultWithdraw) RequiredAmendments() [][32]byte {
	return [][32]byte{amendment.FeatureSingleAssetVault}
}

// Apply is intentionally unimplemented. See VaultCreate.Apply.
func (v *VaultWithdraw) Apply(ctx *tx.ApplyContext) tx.Result {
	ctx.Log.Trace("vault withdraw apply: not implemented", "account", v.Account)
	return tx.TefINTERNAL
}
