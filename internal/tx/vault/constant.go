package vault

import "github.com/LeJamon/goXRPLd/internal/tx"

// Vault constants
const (
	// MaxVaultDataLength is the maximum length of Data field
	MaxVaultDataLength = 256

	// MaxMPTokenMetadataLength is the maximum length of MPTokenMetadata
	MaxMPTokenMetadataLength = 1024

	// VaultStrategyFirstComeFirstServe is the only valid withdrawal policy
	VaultStrategyFirstComeFirstServe uint8 = 1
)

// VaultCreate flags
const (
	// tfVaultPrivate makes the vault private (requires authorization)
	VaultFlagPrivate uint32 = 0x00000001
	// tfVaultShareNonTransferable makes vault shares non-transferable
	VaultFlagShareNonTransferable uint32 = 0x00000002

	// tfVaultCreateMask is the mask for invalid VaultCreate flags
	tfVaultCreateMask uint32 = ^(VaultFlagPrivate | VaultFlagShareNonTransferable)
)

// Vault errors
var (
	ErrVaultIDRequired       = tx.Errorf(tx.TemMALFORMED, "VaultID is required")
	ErrVaultIDZero           = tx.Errorf(tx.TemMALFORMED, "VaultID cannot be zero")
	ErrVaultAssetRequired    = tx.Errorf(tx.TemMALFORMED, "Asset is required")
	ErrVaultDataTooLong      = tx.Errorf(tx.TemMALFORMED, "Data exceeds maximum length")
	ErrVaultDataEmpty        = tx.Errorf(tx.TemMALFORMED, "Data cannot be empty if present")
	ErrVaultDomainIDZero     = tx.Errorf(tx.TemMALFORMED, "DomainID cannot be zero")
	ErrVaultDomainNotPrivate = tx.Errorf(tx.TemMALFORMED, "DomainID only allowed on private vaults")
	ErrVaultAmountRequired   = tx.Errorf(tx.TemBAD_AMOUNT, "Amount is required")
	ErrVaultAmountNotPos     = tx.Errorf(tx.TemBAD_AMOUNT, "Amount must be positive")
	ErrVaultHolderRequired   = tx.Errorf(tx.TemMALFORMED, "Holder is required")
	ErrVaultHolderIsSelf     = tx.Errorf(tx.TemMALFORMED, "Holder cannot be same as issuer")
	ErrVaultDestZero         = tx.Errorf(tx.TemMALFORMED, "Destination cannot be zero")
	ErrVaultDestTagNoAccount = tx.Errorf(tx.TemMALFORMED, "DestinationTag without Destination")
	ErrVaultNoFieldsToUpdate = tx.Errorf(tx.TemMALFORMED, "nothing to update")
	ErrVaultAssetsMaxNeg     = tx.Errorf(tx.TemMALFORMED, "AssetsMaximum cannot be negative")
	ErrVaultWithdrawalPolicy = tx.Errorf(tx.TemMALFORMED, "invalid withdrawal policy")
	ErrVaultMetadataTooLong  = tx.Errorf(tx.TemMALFORMED, "MPTokenMetadata exceeds maximum length")
	ErrVaultMetadataEmpty    = tx.Errorf(tx.TemMALFORMED, "MPTokenMetadata cannot be empty if present")
	ErrVaultAmountXRP        = tx.Errorf(tx.TemMALFORMED, "cannot clawback XRP from vault")
	ErrVaultAmountNotIssuer  = tx.Errorf(tx.TemMALFORMED, "only asset issuer can clawback")
)
