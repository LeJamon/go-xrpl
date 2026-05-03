package vault

import (
	"encoding/hex"

	"github.com/LeJamon/goXRPLd/amendment"
	"github.com/LeJamon/goXRPLd/internal/ledger/state"
	"github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/keylet"
)

func init() {
	if err := tx.Register(tx.TypeVaultClawback, func() tx.Transaction {
		return &VaultClawback{BaseTx: *tx.NewBaseTx(tx.TypeVaultClawback, "")}
	}); err != nil {
		panic(err)
	}
}

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
	// Reference: rippled VaultClawback.cpp:42-43
	if err := tx.CheckFlags(v.GetFlags(), tx.TfUniversalMask); err != nil {
		return err
	}

	// VaultID is required and cannot be zero
	// Reference: rippled VaultClawback.cpp:45-49
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

	// Holder is required
	// Reference: rippled VaultClawback.cpp:51-52
	if v.Holder == "" {
		return ErrVaultHolderRequired
	}

	// Holder cannot be the same as issuer (Account)
	// Reference: rippled VaultClawback.cpp:54-58
	if v.Holder == v.Account {
		return ErrVaultHolderIsSelf
	}

	// Validate Amount if present
	// Reference: rippled VaultClawback.cpp:60-77
	if v.Amount != nil {
		// Zero amount is valid (means "all"), negative is not
		if !v.Amount.IsZero() {
			amountVal := v.Amount.Float64()
			if amountVal < 0 {
				return ErrVaultAmountNotPos
			}
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

func (v *VaultClawback) Apply(ctx *tx.ApplyContext) tx.Result {
	ctx.Log.Trace("vault clawback apply",
		"account", v.Account,
		"vaultID", v.VaultID,
		"holder", v.Holder,
		"amount", v.Amount,
	)

	if v.VaultID == "" || v.Holder == "" {
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
	_, err = state.DecodeAccountID(v.Holder)
	if err != nil {
		return tx.TecNO_TARGET
	}
	return tx.TesSUCCESS
}
