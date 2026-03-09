package vault

import "errors"

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
	ErrVaultIDRequired       = errors.New("temMALFORMED: VaultID is required")
	ErrVaultIDZero           = errors.New("temMALFORMED: VaultID cannot be zero")
	ErrVaultAssetRequired    = errors.New("temMALFORMED: Asset is required")
	ErrVaultDataTooLong      = errors.New("temMALFORMED: Data exceeds maximum length")
	ErrVaultDataEmpty        = errors.New("temMALFORMED: Data cannot be empty if present")
	ErrVaultDomainIDZero     = errors.New("temMALFORMED: DomainID cannot be zero")
	ErrVaultDomainNotPrivate = errors.New("temMALFORMED: DomainID only allowed on private vaults")
	ErrVaultAmountRequired   = errors.New("temBAD_AMOUNT: Amount is required")
	ErrVaultAmountNotPos     = errors.New("temBAD_AMOUNT: Amount must be positive")
	ErrVaultHolderRequired   = errors.New("temMALFORMED: Holder is required")
	ErrVaultHolderIsSelf     = errors.New("temMALFORMED: Holder cannot be same as issuer")
	ErrVaultDestZero         = errors.New("temMALFORMED: Destination cannot be zero")
	ErrVaultDestTagNoAccount = errors.New("temMALFORMED: DestinationTag without Destination")
	ErrVaultNoFieldsToUpdate = errors.New("temMALFORMED: nothing to update")
	ErrVaultAssetsMaxNeg     = errors.New("temMALFORMED: AssetsMaximum cannot be negative")
	ErrVaultWithdrawalPolicy = errors.New("temMALFORMED: invalid withdrawal policy")
	ErrVaultMetadataTooLong  = errors.New("temMALFORMED: MPTokenMetadata exceeds maximum length")
	ErrVaultMetadataEmpty    = errors.New("temMALFORMED: MPTokenMetadata cannot be empty if present")
	ErrVaultAmountXRP        = errors.New("temMALFORMED: cannot clawback XRP from vault")
	ErrVaultAmountNotIssuer  = errors.New("temMALFORMED: only asset issuer can clawback")
)
