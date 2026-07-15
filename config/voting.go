package config

// VotingConfig represents the [voting] section: the fee/reserve values
// this validator votes toward on flag ledgers. The Set fields distinguish an
// omitted value from an explicitly configured zero.
type VotingConfig struct {
	ReferenceFee      int  `toml:"reference_fee" mapstructure:"reference_fee"`
	AccountReserve    int  `toml:"account_reserve" mapstructure:"account_reserve"`
	OwnerReserve      int  `toml:"owner_reserve" mapstructure:"owner_reserve"`
	ReferenceFeeSet   bool `toml:"-" mapstructure:"-"`
	AccountReserveSet bool `toml:"-" mapstructure:"-"`
	OwnerReserveSet   bool `toml:"-" mapstructure:"-"`
}

// Validate performs validation on the voting configuration
func (v *VotingConfig) Validate() error {
	if err := validateNonNegative("reference_fee", v.ReferenceFee); err != nil {
		return err
	}
	if err := validateNonNegative("account_reserve", v.AccountReserve); err != nil {
		return err
	}
	return validateNonNegative("owner_reserve", v.OwnerReserve)
}
