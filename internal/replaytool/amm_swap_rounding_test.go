package replaytool

import (
	_ "embed"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/LeJamon/go-xrpl/amendment"
)

//go:embed testdata/amm_swap_rounding_10891.json
var ammSwapRoundingFixture []byte

// TestHistoricalAMMSwapRoundingRetiredFixAMMv1_1 replays testnet ledger 10891,
// produced after the November 2026 testnet reset by rippled 3.5. The ledger's
// Amendments object does not list fixAMMv1_1 because rippled 3.5 retires it,
// yet the AMM swap in inner Batch payment 6E8A84… must still use the
// fixAMMv1_1 rounding (numerator rounded upward), delivering 6499999 drops
// rather than 6500000.
func TestHistoricalAMMSwapRoundingRetiredFixAMMv1_1(t *testing.T) {
	fixture := parseHistoricalLedgerFixture(t, ammSwapRoundingFixture)
	require.Len(t, fixture.Transactions, 6)
	swapRecord := fixture.Transactions[3]
	require.Equal(t, "6E8A8474779747934C52B8658367857D1F2BB25C937A046FF806AFC06235A2ED", swapRecord.Hash)
	require.Equal(t, "tesSUCCESS", swapRecord.PeerResult)

	rules := replayHistoricalLedgerFixture(t, fixture)
	require.True(t, rules.Enabled(amendment.FeatureFixAMMv1_1),
		"fixAMMv1_1 is retired and must be enabled even when absent from the Amendments object")
}
