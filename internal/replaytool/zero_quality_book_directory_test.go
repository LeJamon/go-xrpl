package replaytool

import (
	_ "embed"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/stretchr/testify/require"
)

//go:embed testdata/zero_quality_book_directory_22338972.json
var zeroQualityBookDirectoryFixture []byte

// Testnet ledger 22338972 index 4 is an OfferCreate whose quality underflows to
// zero, creating a book directory root that stores ExchangeRate 0.
func TestHistoricalZeroQualityOfferCreatesBookDirectoryWithExchangeRate(t *testing.T) {
	fixture := parseHistoricalLedgerFixture(t, zeroQualityBookDirectoryFixture)
	require.Len(t, fixture.Transactions, 10)
	offerRecord := fixture.Transactions[4]
	require.Equal(t, "B4B18EB71427341C02E5754FA4F671FE62F83AE028A370F704528CF3D89CB8F6", offerRecord.Hash)
	require.Equal(t, "tesSUCCESS", offerRecord.PeerResult)

	rules := replayHistoricalLedgerFixture(t, fixture)
	require.True(t, rules.Enabled(amendment.FeatureFixCleanup3_2_0))
}
