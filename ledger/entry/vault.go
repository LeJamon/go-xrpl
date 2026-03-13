package entry

import (
	"errors"
)

// WithdrawalPolicy represents the withdrawal policy for a vault
type WithdrawalPolicy uint8

const (
	// WithdrawalPolicyStrict requires full collateralization
	WithdrawalPolicyStrict WithdrawalPolicy = 0
	// WithdrawalPolicyAllowLoss allows withdrawals even at a loss
	WithdrawalPolicyAllowLoss WithdrawalPolicy = 1
)

// Vault represents an asset vault ledger entry
// Reference: rippled/include/xrpl/protocol/detail/ledger_entries.macro ltVAULT
type Vault struct {
	BaseEntry

	// Required fields
	Sequence         uint32           // Sequence number when created
	OwnerNode        uint64           // Directory node hint
	Owner            [20]byte         // Account that owns the vault
	Account          [20]byte         // Vault's pseudo-account
	Asset            Issue            // The asset held in the vault
	AssetsTotal      uint64           // Total assets in the vault
	AssetsAvailable  uint64           // Assets available for withdrawal
	LossUnrealized   int64            // Unrealized loss (can be negative for gains)
	ShareMPTID       [32]byte         // MPToken issuance ID for vault shares
	WithdrawalPolicy WithdrawalPolicy // Vault's withdrawal policy

	// Default fields (always present but may be zero)
	AssetsMaximum uint64 // Maximum assets the vault can hold (0 = unlimited)

	// Optional fields
	Data *[]byte // Arbitrary data associated with the vault
}

func (v *Vault) Type() Type {
	return TypeVault
}

func (v *Vault) Validate() error {
	if v.Owner == [20]byte{} {
		return errors.New("owner is required")
	}
	if v.Account == [20]byte{} {
		return errors.New("account is required")
	}
	if v.ShareMPTID == [32]byte{} {
		return errors.New("share MPT ID is required")
	}
	if v.AssetsAvailable > v.AssetsTotal {
		return errors.New("available assets cannot exceed total assets")
	}
	if v.AssetsMaximum > 0 && v.AssetsTotal > v.AssetsMaximum {
		return errors.New("total assets cannot exceed maximum")
	}
	return nil
}

func (v *Vault) Hash() ([32]byte, error) {
	hash := v.BaseEntry.Hash()
	for i := 0; i < 20; i++ {
		hash[i] ^= v.Owner[i]
		hash[i] ^= v.Account[i]
	}
	return hash, nil
}
