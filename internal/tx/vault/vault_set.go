package vault

import (
	"encoding/hex"

	"github.com/LeJamon/goXRPLd/amendment"
	"github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/keylet"
)

func init() {
	if err := tx.Register(tx.TypeVaultSet, func() tx.Transaction {
		return &VaultSet{BaseTx: *tx.NewBaseTx(tx.TypeVaultSet, "")}
	}); err != nil {
		panic(err)
	}
}

// VaultSet modifies a vault.
type VaultSet struct {
	tx.BaseTx

	// VaultID is the ID of the vault to modify (required)
	VaultID string `json:"VaultID" xrpl:"VaultID"`

	// Data is arbitrary data (optional)
	Data string `json:"Data,omitempty" xrpl:"Data,omitempty"`

	// DomainID is the permissioned domain ID (optional)
	DomainID string `json:"DomainID,omitempty" xrpl:"DomainID,omitempty"`

	// AssetsMaximum is the maximum assets (optional)
	AssetsMaximum *int64 `json:"AssetsMaximum,omitempty" xrpl:"AssetsMaximum,omitempty"`
}

// NewVaultSet creates a new VaultSet transaction
func NewVaultSet(account, vaultID string) *VaultSet {
	return &VaultSet{
		BaseTx:  *tx.NewBaseTx(tx.TypeVaultSet, account),
		VaultID: vaultID,
	}
}

func (v *VaultSet) TxType() tx.Type {
	return tx.TypeVaultSet
}

// Reference: rippled VaultSet.cpp preflight()
func (v *VaultSet) Validate() error {
	if err := v.BaseTx.Validate(); err != nil {
		return err
	}

	// Check for invalid flags (universal mask)
	// Reference: rippled VaultSet.cpp:52-53
	if err := tx.CheckFlags(v.GetFlags(), tx.TfUniversalMask); err != nil {
		return err
	}

	// VaultID is required and cannot be zero
	// Reference: rippled VaultSet.cpp:46-50
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

	// Validate Data if present
	// Reference: rippled VaultSet.cpp:55-62
	if v.Data != "" {
		if len(v.Data) > MaxVaultDataLength {
			return ErrVaultDataTooLong
		}
	}

	// Validate AssetsMaximum if present
	// Reference: rippled VaultSet.cpp:64-71
	if v.AssetsMaximum != nil && *v.AssetsMaximum < 0 {
		return ErrVaultAssetsMaxNeg
	}

	// Must update at least one field
	// Reference: rippled VaultSet.cpp:73-79
	if v.DomainID == "" && v.AssetsMaximum == nil && v.Data == "" {
		return ErrVaultNoFieldsToUpdate
	}

	return nil
}

func (v *VaultSet) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(v)
}

func (v *VaultSet) RequiredAmendments() [][32]byte {
	return [][32]byte{amendment.FeatureSingleAssetVault}
}

func (v *VaultSet) Apply(ctx *tx.ApplyContext) tx.Result {
	ctx.Log.Trace("vault set apply",
		"account", v.Account,
		"vaultID", v.VaultID,
		"hasData", v.Data != "",
		"hasDomainID", v.DomainID != "",
		"hasAssetsMaximum", v.AssetsMaximum != nil,
	)

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
	_, err = ctx.View.Read(vaultKeylet)
	if err != nil {
		return tx.TecNO_ENTRY
	}
	return tx.TesSUCCESS
}
