package vault

import (
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

// Vault constants
const (
	// MaxVaultDataLength is the maximum length of Data field
	MaxVaultDataLength = 256

	// MaxMPTokenMetadataLength is the maximum length of MPTokenMetadata
	MaxMPTokenMetadataLength = 1024

	// VaultStrategyFirstComeFirstServe is the only valid withdrawal policy
	VaultStrategyFirstComeFirstServe uint8 = 1

	// vaultMaximumIOUScale is the largest Scale permitted on an IOU vault.
	vaultMaximumIOUScale uint8 = 18

	// vaultDefaultIOUScale is the Scale applied to IOU/XRP vault shares when the
	// transaction omits sfScale.
	vaultDefaultIOUScale uint8 = 6
)

// VaultCreate flags (tf*) and the vault SLE flag (lsf*). tfVaultPrivate mirrors
// lsfVaultPrivate, the only flag persisted on the vault ledger entry.
const (
	// VaultFlagPrivate makes the vault private (shares require authorization).
	VaultFlagPrivate uint32 = 0x00010000
	// VaultFlagShareNonTransferable makes vault shares non-transferable.
	VaultFlagShareNonTransferable uint32 = 0x00020000

	// tfVaultCreateMask marks every bit invalid on VaultCreate.
	tfVaultCreateMask uint32 = ^(tx.TfUniversal | VaultFlagPrivate | VaultFlagShareNonTransferable)
)

// Vault errors
var (
	ErrVaultIDRequired       = ter.Errorf(ter.TemMALFORMED, "VaultID is required")
	ErrVaultIDZero           = ter.Errorf(ter.TemMALFORMED, "VaultID cannot be zero")
	ErrVaultAssetRequired    = ter.Errorf(ter.TemMALFORMED, "Asset is required")
	ErrVaultDataTooLong      = ter.Errorf(ter.TemMALFORMED, "Data exceeds maximum length")
	ErrVaultDataEmpty        = ter.Errorf(ter.TemMALFORMED, "Data cannot be empty if present")
	ErrVaultDomainIDZero     = ter.Errorf(ter.TemMALFORMED, "DomainID cannot be zero")
	ErrVaultDomainNotPrivate = ter.Errorf(ter.TemMALFORMED, "DomainID only allowed on private vaults")
	ErrVaultAmountNotPos     = ter.Errorf(ter.TemBAD_AMOUNT, "Amount must be positive")
	ErrVaultHolderRequired   = ter.Errorf(ter.TemMALFORMED, "Holder is required")
	ErrVaultHolderIsSelf     = ter.Errorf(ter.TemMALFORMED, "Holder cannot be same as issuer")
	ErrVaultDestZero         = ter.Errorf(ter.TemMALFORMED, "Destination cannot be zero")
	ErrVaultDestTagNoAccount = ter.Errorf(ter.TemMALFORMED, "DestinationTag without Destination")
	ErrVaultNoFieldsToUpdate = ter.Errorf(ter.TemMALFORMED, "nothing to update")
	ErrVaultAssetsMaxNeg     = ter.Errorf(ter.TemMALFORMED, "AssetsMaximum cannot be negative")
	ErrVaultWithdrawalPolicy = ter.Errorf(ter.TemMALFORMED, "invalid withdrawal policy")
	ErrVaultMetadataTooLong  = ter.Errorf(ter.TemMALFORMED, "MPTokenMetadata exceeds maximum length")
	ErrVaultMetadataEmpty    = ter.Errorf(ter.TemMALFORMED, "MPTokenMetadata cannot be empty if present")
	ErrVaultAmountXRP        = ter.Errorf(ter.TemMALFORMED, "cannot clawback XRP from vault")
	ErrVaultAmountNotIssuer  = ter.Errorf(ter.TemMALFORMED, "only asset issuer can clawback")
	ErrVaultScaleForbidden   = ter.Errorf(ter.TemMALFORMED, "Scale not allowed for MPT or native asset")
	ErrVaultScaleTooLarge    = ter.Errorf(ter.TemMALFORMED, "Scale exceeds maximum")
)
