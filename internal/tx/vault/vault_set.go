package vault

import (
	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/tx"
)

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

	// Data is a Blob: present-but-empty and over-length (in decoded bytes)
	// are both rejected.
	if v.Data != "" {
		dataBytes, err := decodeBlob(v.Data)
		if err != nil {
			return ErrVaultDataTooLong
		}
		if len(dataBytes) == 0 {
			return ErrVaultDataEmpty
		}
		if len(dataBytes) > MaxVaultDataLength {
			return ErrVaultDataTooLong
		}
	}

	// Validate AssetsMaximum if present
	if v.AssetsMaximum != nil && *v.AssetsMaximum < 0 {
		return ErrVaultAssetsMaxNeg
	}

	// Must update at least one field
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

// Apply is intentionally unimplemented. See VaultCreate.Apply.
func (v *VaultSet) Apply(ctx *tx.ApplyContext) tx.Result {
	ctx.Log.Trace("vault set apply: not implemented", "account", v.Account)
	return tx.TefINTERNAL
}
