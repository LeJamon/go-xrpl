package config

import (
	"math"
	"strconv"
	"testing"

	"github.com/LeJamon/go-xrpl/drops"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVotingConfigValidateAcceptsBoundaries(t *testing.T) {
	if strconv.IntSize < 64 {
		t.Skip("VotingConfig cannot represent protocol maxima on 32-bit platforms")
	}
	maxDrops := int64(drops.MaxDrops)
	maxUint32 := uint64(math.MaxUint32)
	tests := []struct {
		name   string
		config VotingConfig
	}{
		{
			name: "zero",
		},
		{
			name: "exact upper bounds",
			config: VotingConfig{
				ReferenceFee:   int(maxDrops),
				AccountReserve: int(maxUint32),
				OwnerReserve:   int(maxUint32),
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			require.NoError(t, test.config.Validate())
		})
	}
}

func TestVotingConfigValidateRejectsOutOfRangeValues(t *testing.T) {
	if strconv.IntSize < 64 {
		t.Skip("VotingConfig cannot represent protocol maxima on 32-bit platforms")
	}
	aboveMaxDrops := int64(drops.MaxDrops) + 1
	aboveMaxUint32 := uint64(math.MaxUint32) + 1
	tests := []struct {
		name   string
		config VotingConfig
		key    string
	}{
		{
			name:   "negative reference fee",
			config: VotingConfig{ReferenceFee: -1},
			key:    "reference_fee",
		},
		{
			name:   "reference fee above maximum",
			config: VotingConfig{ReferenceFee: int(aboveMaxDrops)},
			key:    "reference_fee",
		},
		{
			name:   "negative account reserve",
			config: VotingConfig{AccountReserve: -1},
			key:    "account_reserve",
		},
		{
			name:   "account reserve above maximum",
			config: VotingConfig{AccountReserve: int(aboveMaxUint32)},
			key:    "account_reserve",
		},
		{
			name:   "negative owner reserve",
			config: VotingConfig{OwnerReserve: -1},
			key:    "owner_reserve",
		},
		{
			name:   "owner reserve above maximum",
			config: VotingConfig{OwnerReserve: int(aboveMaxUint32)},
			key:    "owner_reserve",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.config.Validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), test.key)
		})
	}
}

func TestFeeDefaultValidateRejectsOutOfRangeValue(t *testing.T) {
	if strconv.IntSize < 64 {
		t.Skip("FeeDefault cannot represent the protocol maximum on 32-bit platforms")
	}
	aboveMax := int(int64(drops.MaxDrops) + 1)
	errs := validateMiscSettings(&Config{FeeDefault: &aboveMax})
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0].Error(), "fee_default")
}
