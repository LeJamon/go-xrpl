package replaytool

import (
	_ "embed"
	"testing"

	"github.com/stretchr/testify/require"
)

//go:embed testdata/amm_withdraw_issuer_reserve_22530796.json
var ammWithdrawIssuerReserveFixture []byte

func TestHistoricalAMMWithdrawIssuerReserve(t *testing.T) {
	fixture := parseHistoricalLedgerFixture(t, ammWithdrawIssuerReserveFixture)
	require.Len(t, fixture.Transactions, 20)
	withdrawRecord := fixture.Transactions[19]
	require.Equal(t, "D80E5CC4711724EC7A8A3FC30004078354D3AFEB0EC3EB94D16F8388F5C4D29D", withdrawRecord.Hash)
	require.Equal(t, "tecINSUFFICIENT_RESERVE", withdrawRecord.PeerResult)

	replayHistoricalLedgerFixture(t, fixture)
}
