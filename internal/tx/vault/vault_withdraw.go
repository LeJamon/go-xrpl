package vault

import (
	"encoding/hex"
	"errors"
	"github.com/LeJamon/goXRPLd/keylet"

	"github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/amendment"
)

func init() {
	tx.Register(tx.TypeVaultWithdraw, func() tx.Transaction {
		return &VaultWithdraw{BaseTx: *tx.NewBaseTx(tx.TypeVaultWithdraw, "")}
	})
}

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

// TxType returns the transaction type
func (v *VaultWithdraw) TxType() tx.Type {
	return tx.TypeVaultWithdraw
}

// Validate validates the VaultWithdraw transaction
// Reference: rippled VaultWithdraw.cpp preflight()
func (v *VaultWithdraw) Validate() error {
	if err := v.BaseTx.Validate(); err != nil {
		return err
	}

	// Check for invalid flags (universal mask)
	// Reference: rippled VaultWithdraw.cpp:42-43
	if v.Common.Flags != nil && *v.Common.Flags&tx.TfUniversalMask != 0 {
		return tx.ErrInvalidFlags
	}

	// VaultID is required and cannot be zero
	// Reference: rippled VaultWithdraw.cpp:45-49
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
	// Reference: rippled VaultWithdraw.cpp:51-52
	if v.Amount.IsZero() {
		return ErrVaultAmountRequired
	}
	amountVal := v.Amount.Float64()
	if amountVal <= 0 {
		return ErrVaultAmountNotPos
	}

	// Validate Destination if present
	// Reference: rippled VaultWithdraw.cpp:54-63
	if v.Destination != "" {
		// Destination cannot be zero (empty is handled by field being absent)
		// In rippled this checks for beast::zero which is all zeros
		// For our case, if Destination is set but empty, that's an error
	}

	// DestinationTag without Destination is invalid
	// Reference: rippled VaultWithdraw.cpp:64-69
	if v.Destination == "" && v.DestinationTag != nil {
		return ErrVaultDestTagNoAccount
	}

	return nil
}

// Flatten returns a flat map of all transaction fields
func (v *VaultWithdraw) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(v)
}

// RequiredAmendments returns the amendments required for this transaction type
func (v *VaultWithdraw) RequiredAmendments() [][32]byte {
	return [][32]byte{amendment.FeatureSingleAssetVault}
}

// Apply applies the VaultWithdraw transaction to the ledger.
func (v *VaultWithdraw) Apply(ctx *tx.ApplyContext) tx.Result {
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
		ctx.Account.Balance += amount
	}
	return tx.TesSUCCESS
}
