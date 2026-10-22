package replaytool

import (
	"context"
	"encoding/hex"
	"fmt"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/all"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	fixture "github.com/LeJamon/go-xrpl/internal/tx/testdata"
	"github.com/stretchr/testify/require"
)

func TestReplayFixtureAcceptsLargeMetadata(t *testing.T) {
	all.RegisterAll()
	nodes := make([]tx.AffectedNode, 513)
	for i := range nodes {
		nodes[i] = tx.AffectedNode{
			NodeType:        "DeletedNode",
			LedgerEntryType: "Offer",
			LedgerIndex:     fmt.Sprintf("%064X", i+1),
		}
	}
	meta, err := tx.SerializeMetadata(&tx.Metadata{TransactionResult: ter.TesSUCCESS, AffectedNodes: nodes})
	require.NoError(t, err)
	_, expected, err := validateFixtureTransactions(context.Background(),
		[]fixtureTxEntry{{Index: 0, Hash: fixture.Issue1861HistoricalTxIDHex, TxBlob: fixture.Issue1861HistoricalTxBlobHex}},
		[]expectedTxEntry{{Index: 0, Hash: fixture.Issue1861HistoricalTxIDHex, Meta: hex.EncodeToString(meta)}},
	)
	require.NoError(t, err)
	require.Len(t, expected, 1)
	require.Equal(t, meta, expected[0].MetaBlob)
}
