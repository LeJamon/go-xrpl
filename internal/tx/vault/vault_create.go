package vault

import (
	"encoding/hex"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

// VaultCreate creates a new vault.
type VaultCreate struct {
	tx.BaseTx

	// Asset is the asset the vault holds (required)
	Asset tx.Asset `json:"Asset" xrpl:"Asset"`

	// Data is arbitrary data (optional)
	Data string `json:"Data,omitempty" xrpl:"Data,omitempty"`

	// DomainID is the permissioned domain ID (optional)
	DomainID string `json:"DomainID,omitempty" xrpl:"DomainID,omitempty"`

	// AssetsMaximum is the maximum assets the vault can hold (optional)
	AssetsMaximum *int64 `json:"AssetsMaximum,omitempty" xrpl:"AssetsMaximum,omitempty"`

	// MPTokenMetadata is metadata for the vault shares (optional)
	MPTokenMetadata string `json:"MPTokenMetadata,omitempty" xrpl:"MPTokenMetadata,omitempty"`

	// WithdrawalPolicy configures withdrawal rules (optional)
	WithdrawalPolicy *uint8 `json:"WithdrawalPolicy,omitempty" xrpl:"WithdrawalPolicy,omitempty"`
}

// NewVaultCreate creates a new VaultCreate transaction
func NewVaultCreate(account string, asset tx.Asset) *VaultCreate {
	return &VaultCreate{
		BaseTx: *tx.NewBaseTx(tx.TypeVaultCreate, account),
		Asset:  asset,
	}
}

func (v *VaultCreate) TxType() tx.Type {
	return tx.TypeVaultCreate
}

// Reference: rippled VaultCreate.cpp preflight()
func (v *VaultCreate) Validate() error {
	if err := v.BaseTx.Validate(); err != nil {
		return err
	}

	// Check for invalid flags
	if err := tx.CheckFlags(v.GetFlags(), tfVaultCreateMask); err != nil {
		return err
	}

	// Asset is required
	if v.Asset.Currency == "" {
		return ErrVaultAssetRequired
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

	// Validate WithdrawalPolicy if present
	if v.WithdrawalPolicy != nil {
		if *v.WithdrawalPolicy != VaultStrategyFirstComeFirstServe {
			return ErrVaultWithdrawalPolicy
		}
	}

	// Validate DomainID if present
	if v.DomainID != "" {
		if _, err := tx.ParseHash256NonZero(v.DomainID); err != nil {
			if isZeroHash(v.DomainID) {
				return ErrVaultDomainIDZero
			}
			return ter.Errorf(ter.TemMALFORMED, "DomainID must be a valid 256-bit hash")
		}
		// DomainID only allowed on private vaults
		if v.Common.Flags == nil || (*v.Common.Flags&VaultFlagPrivate) == 0 {
			return ErrVaultDomainNotPrivate
		}
	}

	// Validate AssetsMaximum if present
	if v.AssetsMaximum != nil && *v.AssetsMaximum < 0 {
		return ErrVaultAssetsMaxNeg
	}

	// MPTokenMetadata is a Blob: present-but-empty and over-length (in decoded
	// bytes) are both rejected.
	if v.MPTokenMetadata != "" {
		metaBytes, err := decodeBlob(v.MPTokenMetadata)
		if err != nil {
			return ErrVaultMetadataTooLong
		}
		if len(metaBytes) == 0 {
			return ErrVaultMetadataEmpty
		}
		if len(metaBytes) > MaxMPTokenMetadataLength {
			return ErrVaultMetadataTooLong
		}
	}

	return nil
}

func (v *VaultCreate) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(v)
}

func (v *VaultCreate) RequiredAmendments() [][32]byte {
	return [][32]byte{amendment.FeatureSingleAssetVault}
}

// Apply is intentionally unimplemented. SingleAssetVault is SupportedNo, so the
// engine rejects this transaction at preflight with temDISABLED and Apply is
// unreachable. Returning a hard error that mutates no state guards against the
// amendment being enabled before the real vault semantics are implemented.
func (v *VaultCreate) Apply(ctx *tx.ApplyContext) ter.Result {
	ctx.Log.Trace("vault create apply: not implemented", "account", v.Account)
	return ter.TefINTERNAL
}

// decodeBlob decodes a hex-encoded Blob field to its raw bytes.
func decodeBlob(s string) ([]byte, error) {
	return hex.DecodeString(s)
}

// isZeroHash reports whether s is a valid 64-char hex string decoding to the
// all-zero 256-bit hash.
func isZeroHash(s string) bool {
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != 32 {
		return false
	}
	for _, c := range b {
		if c != 0 {
			return false
		}
	}
	return true
}
