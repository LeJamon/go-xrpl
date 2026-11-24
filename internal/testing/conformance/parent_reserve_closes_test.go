package conformance

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/openledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx/all"
	"github.com/stretchr/testify/require"
)

// The standalone rippled 3.4.1 recording spans a reserve vote at ledger 257.
func TestParentReserveCloses(t *testing.T) {
	all.RegisterAll()
	data, err := os.ReadFile("testdata/parent-reserve-closes.json")
	require.NoError(t, err)
	var snapshots []snapshotLedger
	require.NoError(t, json.Unmarshal(data, &snapshots))
	require.Len(t, snapshots, 3)
	require.NotEqual(t, snapshots[0].Fees.Reserve, snapshots[1].Fees.Reserve)
	require.Equal(t, snapshots[1].Fees, snapshots[2].Fees)

	initial, err := loadSnapshotLedger(snapshots[0])
	require.NoError(t, err)
	recipient, err := state.DecodeAccountID("rJGyGFdwmDLa4fyAtD7p1jWaySQ6Kv7LEu")
	require.NoError(t, err)
	delivered := []uint64{5_000_000, 4_000_000}
	parent := initial.Ledger
	for i, snapshot := range snapshots[1:] {
		want, err := loadSnapshotLedger(snapshot)
		require.NoError(t, err)
		require.Equal(t, parent.Hash(), want.Header.ParentHash)
		var blobs []string
		for _, transaction := range snapshot.Transactions {
			blobs = append(blobs, transaction.TxBlob)
		}
		pending, err := parseSnapshotCloseSet(blobs)
		require.NoError(t, err)

		fees := parent.Fees()
		built, err := openledger.BuildClosedLedger(parent, pending, openledger.BuildConfig{
			CloseTime: want.Header.CloseTime, CloseFlags: want.Header.CloseFlags,
			Apply: openledger.ApplyConfig{
				BaseFee: uint64(fees.Base), ReserveBase: uint64(fees.Reserve),
				ReserveIncrement: uint64(fees.Increment), Rules: parent.Rules(),
			},
		})
		require.NoError(t, err)
		require.Empty(t, built.Retries)
		require.NoError(t, assertSnapshotLedger(built.Ledger, want), "ledger %d", want.Header.LedgerIndex)
		before, err := state.ReadAccountRoot(parent, recipient)
		require.NoError(t, err)
		require.NotNil(t, before)
		after, err := state.ReadAccountRoot(built.Ledger, recipient)
		require.NoError(t, err)
		require.NotNil(t, after)
		require.Equal(t, before.Balance+delivered[i], after.Balance)
		parent = built.Ledger
	}
}
