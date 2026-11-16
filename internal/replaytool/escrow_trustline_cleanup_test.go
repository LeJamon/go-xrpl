package replaytool

import (
	_ "embed"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/stretchr/testify/require"
)

//go:embed testdata/escrow_trustline_cleanup_22280293.json
var issue2212Fixture []byte

func TestIssue2212HistoricalBatchEscrowDeletesExhaustedTrustLine(t *testing.T) {
	fixture := parseHistoricalLedgerFixture(t, issue2212Fixture)
	require.Len(t, fixture.Transactions, 8)
	escrowRecord := fixture.Transactions[7]
	require.Equal(t, "D4FFE85FA99B4BC52686130C3136C24F67A15F02469786A4DD46C2E5A7F2CBCC", escrowRecord.Hash)
	require.Len(t, escrowRecord.PeerMetadata, 2*982)

	rules := replayHistoricalLedgerFixture(t, fixture)
	require.True(t, rules.Enabled(amendment.FeatureBatchV1_1))
	require.True(t, rules.Enabled(amendment.FeatureTokenEscrow))
}
