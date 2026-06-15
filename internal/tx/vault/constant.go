package vault

import "github.com/LeJamon/go-xrpl/internal/tx/ter"

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
)
