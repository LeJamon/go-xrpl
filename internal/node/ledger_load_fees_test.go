package node

import (
	"testing"

	"github.com/LeJamon/go-xrpl/config"
	"github.com/LeJamon/go-xrpl/drops"
	"github.com/stretchr/testify/require"
)

func TestConfiguredLedgerLoadFees(t *testing.T) {
	feeDefault := 19
	fees := configuredLedgerLoadFees(&config.Config{
		Voting: config.VotingConfig{
			ReferenceFee:   17,
			AccountReserve: 23_000_000,
			OwnerReserve:   4_000_000,
		},
		FeeDefault: &feeDefault,
	})

	require.Equal(t, drops.Fees{
		Base:      19,
		Reserve:   23_000_000,
		Increment: 4_000_000,
	}, fees)

	zero := configuredLedgerLoadFees(&config.Config{
		Voting: config.VotingConfig{
			ReferenceFeeSet:   true,
			AccountReserveSet: true,
			OwnerReserveSet:   true,
		},
	})
	require.Equal(t, drops.Fees{}, zero)
}
