package config

import (
	"fmt"
	"math"

	"github.com/LeJamon/go-xrpl/drops"
)

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
	if err := validateVotingValue("reference_fee", v.ReferenceFee, uint64(drops.MaxDrops)); err != nil {
		return err
	}
	if err := validateVotingValue("account_reserve", v.AccountReserve, math.MaxUint32); err != nil {
		return err
	}
	return validateVotingValue("owner_reserve", v.OwnerReserve, math.MaxUint32)
}

func validateVotingValue(name string, value int, max uint64) error {
	if err := validateNonNegative(name, value); err != nil {
		return err
	}
	if uint64(value) > max {
		return fmt.Errorf("%s must be at most %d, got %d", name, max, value)
	}
	return nil
}
