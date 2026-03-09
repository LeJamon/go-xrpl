package vault

import (
	"encoding/hex"
	"errors"

	"github.com/LeJamon/goXRPLd/keylet"
	"github.com/LeJamon/goXRPLd/internal/core/tx"
	"github.com/LeJamon/goXRPLd/amendment"
)

func init() {
	tx.Register(tx.TypeVaultDeposit, func() tx.Transaction {
		return &VaultDeposit{BaseTx: *tx.NewBaseTx(tx.TypeVaultDeposit, "")}
	})
}

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

// TxType returns the transaction type
func (v *VaultDeposit) TxType() tx.Type {
	return tx.TypeVaultDeposit
}

// Validate validates the VaultDeposit transaction
// Reference: rippled VaultDeposit.cpp preflight()
func (v *VaultDeposit) Validate() error {
	if err := v.BaseTx.Validate(); err != nil {
		return err
	}

	// Check for invalid flags (universal mask)
	// Reference: rippled VaultDeposit.cpp:44-45
	if v.Common.Flags != nil && *v.Common.Flags&tx.TfUniversalMask != 0 {
		return tx.ErrInvalidFlags
	}

	// VaultID is required and cannot be zero
	// Reference: rippled VaultDeposit.cpp:47-51
	if v.VaultID == "" {
		return ErrVaultIDRequired
	}
	vaultBytes, err := hex.DecodeString(v.VaultID)
	if err != nil || len(vaultBytes) != 32 {
		return errors.New("temMALFORMED: VaultID must be a valid 256-bit hash")
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

	// Amount is required and must be positive
	// Reference: rippled VaultDeposit.cpp:53-54
	if v.Amount.IsZero() {
		return ErrVaultAmountRequired
	}
	amountVal := v.Amount.Float64()
	if amountVal <= 0 {
		return ErrVaultAmountNotPos
	}

	return nil
}

// Flatten returns a flat map of all transaction fields
func (v *VaultDeposit) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(v)
}

// RequiredAmendments returns the amendments required for this transaction type
func (v *VaultDeposit) RequiredAmendments() [][32]byte {
	return [][32]byte{amendment.FeatureSingleAssetVault}
}

// Apply applies the VaultDeposit transaction to the ledger.
func (v *VaultDeposit) Apply(ctx *tx.ApplyContext) tx.Result {
	if v.VaultID == "" || v.Amount.IsZero() {
		return tx.TemINVALID
	}
	vaultBytes, err := hex.DecodeString(v.VaultID)
	if err != nil || len(vaultBytes) != 32 {
		return tx.TemINVALID
	}
	var vaultKey [32]byte
	copy(vaultKey[:], vaultBytes)
	vaultKeylet := keylet.Keylet{Key: vaultKey, Type: 0x0084}
	_, err = ctx.View.Read(vaultKeylet)
	if err != nil {
		return tx.TecNO_ENTRY
	}
	if v.Amount.Currency == "" || v.Amount.Currency == "XRP" {
		amount := uint64(v.Amount.Drops())
		if ctx.Account.Balance < amount {
			return tx.TecINSUFFICIENT_FUNDS
		}
		ctx.Account.Balance -= amount
	}
	return tx.TesSUCCESS
}
