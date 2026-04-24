package config

import "fmt"

// VotingConfig represents the [voting] section
// Configuration for network-wide voting parameters
type VotingConfig struct {
	ReferenceFee   int `toml:"reference_fee" mapstructure:"reference_fee"`
	AccountReserve int `toml:"account_reserve" mapstructure:"account_reserve"`
	OwnerReserve   int `toml:"owner_reserve" mapstructure:"owner_reserve"`
}

// Validate performs validation on the voting configuration
func (v *VotingConfig) Validate() error {
	if v.ReferenceFee < 0 {
		return fmt.Errorf("reference_fee must be non-negative, got %d", v.ReferenceFee)
	}

	if v.AccountReserve < 0 {
		return fmt.Errorf("account_reserve must be non-negative, got %d", v.AccountReserve)
	}

	if v.OwnerReserve < 0 {
		return fmt.Errorf("owner_reserve must be non-negative, got %d", v.OwnerReserve)
	}

	return nil
}

// GetReferenceFee returns the reference fee in drops
// If not specified, returns 0 to indicate using internal default
func (v *VotingConfig) GetReferenceFee() int {
	return v.ReferenceFee
}

// GetAccountReserve returns the account reserve in drops
// If not specified, returns 0 to indicate using internal default
func (v *VotingConfig) GetAccountReserve() int {
	return v.AccountReserve
}

// GetOwnerReserve returns the owner reserve in drops
// If not specified, returns 0 to indicate using internal default
func (v *VotingConfig) GetOwnerReserve() int {
	return v.OwnerReserve
}

// HasCustomReferenceFee returns true if a custom reference fee is set
func (v *VotingConfig) HasCustomReferenceFee() bool {
	return v.ReferenceFee > 0
}

// HasCustomAccountReserve returns true if a custom account reserve is set
func (v *VotingConfig) HasCustomAccountReserve() bool {
	return v.AccountReserve > 0
}

// HasCustomOwnerReserve returns true if a custom owner reserve is set
func (v *VotingConfig) HasCustomOwnerReserve() bool {
	return v.OwnerReserve > 0
}

// IsEmpty returns true if no voting parameters are configured
func (v *VotingConfig) IsEmpty() bool {
	return v.ReferenceFee == 0 && v.AccountReserve == 0 && v.OwnerReserve == 0
}
