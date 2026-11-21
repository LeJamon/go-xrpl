package replaytool

import (
	_ "embed"
	"testing"

	"github.com/stretchr/testify/require"
)

//go:embed testdata/self_cross_recreated_trust_line_22418919.json
var selfCrossRecreatedTrustLineFixture []byte

// Testnet ledger 22418919 index 5 is an auto-bridged OfferCreate that crosses
// its own offer, deleting and re-creating the account's B18 trust line. The
// re-created line is threaded without a previous transaction pointer.
func TestHistoricalSelfCrossRecreatedTrustLineMetadata(t *testing.T) {
	fixture := parseHistoricalLedgerFixture(t, selfCrossRecreatedTrustLineFixture)
	require.Len(t, fixture.Transactions, 7)
	offerRecord := fixture.Transactions[5]
	require.Equal(t, "940E52E8EE68926196E7812C566C59F85AE55AC7C1A4FD2107D38A3D286283CD", offerRecord.Hash)
	require.Equal(t, "tesSUCCESS", offerRecord.PeerResult)

	replayHistoricalLedgerFixture(t, fixture)
}
