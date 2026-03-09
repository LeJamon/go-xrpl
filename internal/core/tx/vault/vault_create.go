package vault

import (
	"encoding/binary"
	"encoding/hex"
	"errors"

	"github.com/LeJamon/goXRPLd/keylet"
	"github.com/LeJamon/goXRPLd/internal/core/tx"
	"github.com/LeJamon/goXRPLd/amendment"
)

func init() {
	tx.Register(tx.TypeVaultCreate, func() tx.Transaction {
		return &VaultCreate{BaseTx: *tx.NewBaseTx(tx.TypeVaultCreate, "")}
	})
}

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

// TxType returns the transaction type
func (v *VaultCreate) TxType() tx.Type {
	return tx.TypeVaultCreate
}

// Validate validates the VaultCreate transaction
// Reference: rippled VaultCreate.cpp preflight()
func (v *VaultCreate) Validate() error {
	if err := v.BaseTx.Validate(); err != nil {
		return err
	}

	// Check for invalid flags
	// Reference: rippled VaultCreate.cpp:50-51
	if v.Common.Flags != nil && *v.Common.Flags&tfVaultCreateMask != 0 {
		return tx.ErrInvalidFlags
	}

	// Asset is required
	if v.Asset.Currency == "" {
		return ErrVaultAssetRequired
	}

	// Validate Data if present
	// Reference: rippled VaultCreate.cpp:53-57
	if v.Data != "" {
		if len(v.Data) > MaxVaultDataLength {
			return ErrVaultDataTooLong
		}
	}

	// Validate WithdrawalPolicy if present
	// Reference: rippled VaultCreate.cpp:59-63
	if v.WithdrawalPolicy != nil {
		if *v.WithdrawalPolicy != VaultStrategyFirstComeFirstServe {
			return ErrVaultWithdrawalPolicy
		}
	}

	// Validate DomainID if present
	// Reference: rippled VaultCreate.cpp:66-72
	if v.DomainID != "" {
		domainBytes, err := hex.DecodeString(v.DomainID)
		if err != nil || len(domainBytes) != 32 {
			return errors.New("temMALFORMED: DomainID must be a valid 256-bit hash")
		}
		// Check if zero
		isZero := true
		for _, b := range domainBytes {
			if b != 0 {
				isZero = false
				break
			}
		}
		if isZero {
			return ErrVaultDomainIDZero
		}
		// DomainID only allowed on private vaults
		if v.Common.Flags == nil || (*v.Common.Flags&VaultFlagPrivate) == 0 {
			return ErrVaultDomainNotPrivate
		}
	}

	// Validate AssetsMaximum if present
	// Reference: rippled VaultCreate.cpp:74-78
	if v.AssetsMaximum != nil && *v.AssetsMaximum < 0 {
		return ErrVaultAssetsMaxNeg
	}

	// Validate MPTokenMetadata if present
	// Reference: rippled VaultCreate.cpp:80-84
	if v.MPTokenMetadata != "" {
		if len(v.MPTokenMetadata) > MaxMPTokenMetadataLength {
			return ErrVaultMetadataTooLong
		}
	}

	return nil
}

// Flatten returns a flat map of all transaction fields
func (v *VaultCreate) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(v)
}

// RequiredAmendments returns the amendments required for this transaction type
func (v *VaultCreate) RequiredAmendments() [][32]byte {
	return [][32]byte{amendment.FeatureSingleAssetVault}
}

// Apply applies the VaultCreate transaction to the ledger.
func (v *VaultCreate) Apply(ctx *tx.ApplyContext) tx.Result {
	if v.Asset.Currency == "" {
		return tx.TemINVALID
	}
	var vaultKey [32]byte
	copy(vaultKey[:20], ctx.AccountID[:])
	binary.BigEndian.PutUint32(vaultKey[20:], ctx.Account.Sequence)
	vaultKeylet := keylet.Keylet{Key: vaultKey, Type: 0x0084}
	vaultData := make([]byte, 64)
	copy(vaultData[:20], ctx.AccountID[:])
	if err := ctx.View.Insert(vaultKeylet, vaultData); err != nil {
		return tx.TefINTERNAL
	}
	ctx.Account.OwnerCount++
	return tx.TesSUCCESS
}
