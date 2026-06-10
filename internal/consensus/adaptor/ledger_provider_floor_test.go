package adaptor

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubFloor is a fixed-value MinimumOnlineFloor for serving/acquisition tests.
type stubFloor uint32

func (s stubFloor) MinimumOnline() uint32 { return uint32(s) }

// TestLedgerProvider_Floor_DeclinesBelowBoundary verifies that once the
// online-delete floor is installed, every serve path refuses a ledger whose
// sequence sits below the floor — mirroring rippled, where a peer cannot serve
// what online-delete physically removed.
func TestLedgerProvider_Floor_DeclinesBelowBoundary(t *testing.T) {
	txs := []struct {
		key  [32]byte
		blob []byte
	}{
		{fixedKey32(1), []byte("tx-blob-one--padded")},
	}
	closed := makeClosedLedgerWithTxs(t, txs) // seq 2 (built on genesis seq 1)
	require.Equal(t, uint32(2), closed.Sequence())

	lookup := newFakeLookup()
	lookup.add(closed)
	provider := newLedgerProviderForTest(lookup)
	// Floor above the ledger's sequence: the ledger is "below the boundary".
	provider.SetMinimumOnlineFloor(stubFloor(3))

	hash := closed.Hash()

	hdr, err := provider.GetLedgerHeader(hash[:], 0)
	require.NoError(t, err)
	assert.Nil(t, hdr, "GetLedgerHeader must decline a below-floor ledger")

	node, err := provider.GetAccountStateNode(hash[:], txs[0].key[:])
	require.NoError(t, err)
	assert.Nil(t, node, "GetAccountStateNode must decline a below-floor ledger")

	txNode, err := provider.GetTransactionNode(hash[:], txs[0].key[:])
	require.NoError(t, err)
	assert.Nil(t, txNode, "GetTransactionNode must decline a below-floor ledger")

	rdHdr, leaves, err := provider.GetReplayDelta(hash[:])
	require.NoError(t, err)
	assert.Nil(t, rdHdr, "GetReplayDelta must decline a below-floor ledger")
	assert.Nil(t, leaves)

	_, _, err = provider.GetProofPath(hash[:], txs[0].key[:], message.LedgerMapTransaction)
	require.ErrorIs(t, err, peermanagement.ErrLedgerNotFound,
		"GetProofPath must report a below-floor ledger as not found")
}

// TestLedgerProvider_Floor_MakeFetchPackDeclinesBelowBoundary verifies the
// fetch-pack serve path withholds a pack whose "want" (the served parent
// ledger) is below the floor.
func TestLedgerProvider_Floor_MakeFetchPackDeclinesBelowBoundary(t *testing.T) {
	// have = seq 2, want = its parent = genesis seq 1.
	closed := makeClosedLedgerWithTxs(t, nil)
	genesisL := makeGenesisLedger(t)
	require.Equal(t, uint32(1), genesisL.Sequence())
	require.Equal(t, genesisL.Hash(), closed.Header().ParentHash)

	lookup := newFakeLookup()
	lookup.add(closed)
	lookup.add(genesisL)
	provider := newLedgerProviderForTest(lookup)
	provider.SetMinimumOnlineFloor(stubFloor(2)) // want (seq 1) < floor

	have := closed.Hash()
	pack, err := provider.MakeFetchPack(have, 0)
	require.NoError(t, err)
	assert.Nil(t, pack, "fetch-pack must be withheld when want is below the floor")
}

// TestLedgerProvider_Floor_ServesAtOrAboveBoundary verifies that a ledger at
// or above the floor is served normally, and that a zero floor (no rotation
// yet) withholds nothing.
func TestLedgerProvider_Floor_ServesAtOrAboveBoundary(t *testing.T) {
	closed := makeClosedLedgerWithTxs(t, nil) // seq 2
	lookup := newFakeLookup()
	lookup.add(closed)
	provider := newLedgerProviderForTest(lookup)
	hash := closed.Hash()

	// Floor at the ledger's own sequence: not below, must serve.
	provider.SetMinimumOnlineFloor(stubFloor(2))
	hdr, err := provider.GetLedgerHeader(hash[:], 0)
	require.NoError(t, err)
	assert.NotNil(t, hdr, "ledger at the floor must still be served")

	// Zero floor = no rotation has happened; nothing is withheld.
	provider.SetMinimumOnlineFloor(stubFloor(0))
	hdr, err = provider.GetLedgerHeader(hash[:], 0)
	require.NoError(t, err)
	assert.NotNil(t, hdr, "a zero floor must withhold nothing")
}

// TestLedgerProvider_NilFloor_Unchanged verifies that without a floor installed
// (online_delete off / standalone), serving behaves exactly as before.
func TestLedgerProvider_NilFloor_Unchanged(t *testing.T) {
	closed := makeClosedLedgerWithTxs(t, nil) // seq 2
	lookup := newFakeLookup()
	lookup.add(closed)
	provider := newLedgerProviderForTest(lookup) // no SetMinimumOnlineFloor

	hash := closed.Hash()
	hdr, err := provider.GetLedgerHeader(hash[:], 0)
	require.NoError(t, err)
	assert.NotNil(t, hdr, "nil floor must leave serving unrestricted")

	rdHdr, _, err := provider.GetReplayDelta(hash[:])
	require.NoError(t, err)
	assert.NotNil(t, rdHdr, "nil floor must leave replay-delta serving unrestricted")
}
