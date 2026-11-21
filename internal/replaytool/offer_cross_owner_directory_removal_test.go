package replaytool

import (
	_ "embed"
	"testing"

	"github.com/stretchr/testify/require"
)

//go:embed testdata/offer_cross_owner_directory_removal_22465396.json
var offerCrossOwnerDirectoryRemovalFixture []byte

func TestHistoricalOfferCrossOwnerDirectoryRemoval(t *testing.T) {
	fixture := parseHistoricalLedgerFixture(t, offerCrossOwnerDirectoryRemovalFixture)
	require.Len(t, fixture.Transactions, 34)
	offerRecord := fixture.Transactions[22]
	require.Equal(t, "66AA746E1FF3F9B5743E52206979512E40764BB5FDA3EA9176D58D14DA5F9FEC", offerRecord.Hash)
	require.Equal(t, "tesSUCCESS", offerRecord.PeerResult)

	replayHistoricalLedgerFixture(t, fixture)
}
