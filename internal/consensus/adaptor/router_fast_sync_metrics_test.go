package adaptor

import (
	"context"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/crypto/sha512half"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/ledger/inbound"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/LeJamon/go-xrpl/shamap/backend"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func buildPivotMetricsState(t *testing.T, extra int) *shamap.SHAMap {
	t.Helper()
	state := shamap.New(shamap.TypeState)
	put := func(first, second byte) {
		var key [32]byte
		key[0] = first
		key[1] = second
		key[31] = 0xA5
		require.NoError(t, state.Put(key, []byte{first, second, 0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88, 0x99, 0xAA}))
	}
	for branch := range byte(4) {
		for item := range byte(4) {
			put(branch<<4, item<<4)
		}
	}
	for item := range extra {
		put(0xE0, byte(item)<<4)
	}
	return state
}

func TestRouterFastSyncMetricsUseLivePivotAcquisitionSnapshot(t *testing.T) {
	base := buildPivotMetricsState(t, 0)
	pivot := buildPivotMetricsState(t, 2)
	baseRoot, err := base.Hash()
	require.NoError(t, err)
	pivotRoot, err := pivot.Hash()
	require.NoError(t, err)
	pivotRootData, err := pivot.SerializeRoot()
	require.NoError(t, err)

	family := backend.NewMemory()
	baseNodes, err := base.WalkFetchPackNodes(1 << 20)
	require.NoError(t, err)
	entries := make([]shamap.FlushEntry, 0, len(baseNodes))
	for _, node := range baseNodes {
		entries = append(entries, shamap.FlushEntry{Hash: node.Hash, Data: node.Data})
	}
	require.NoError(t, family.StoreBatch(context.Background(), entries))

	headerData := header.AddRaw(header.LedgerHeader{LedgerIndex: 500, AccountHash: pivotRoot}, false)
	pivotHash := sha512half.Sum(protocol.HashPrefixLedgerMaster().Bytes(), headerData)
	acquisition := inbound.New(pivotHash, 500, 7, serveTestLogger(), inbound.WithFamily(family))
	require.NoError(t, acquisition.SetVerifiedStateBaseContext(t.Context(), baseRoot))
	require.NoError(t, acquisition.GotBase([]message.LedgerNode{
		{NodeData: headerData},
		{NodeData: pivotRootData},
	}))

	wireNodes, err := pivot.WalkWireNodes()
	require.NoError(t, err)
	wire := make([]message.LedgerNode, 0, len(wireNodes))
	for _, node := range wireNodes {
		wire = append(wire, message.LedgerNode{NodeID: node.NodeID, NodeData: node.Data})
	}
	require.NoError(t, acquisition.GotStateNodes(wire))
	acquisition.CollectMissingRequest(false)

	snapshot := acquisition.Snapshot()
	require.Positive(t, snapshot.StateNodesDescended)
	require.Positive(t, snapshot.StateEqualSubtreesSkipped)
	require.Positive(t, snapshot.StateMissingDiscovered)
	require.Positive(t, snapshot.StateUseful)

	router := newTestRouter(nil, nil, nil)
	router.fetchTracker.Track(acquisition)
	router.standardReplay.pivotHash = pivotHash
	router.standardReplay.pivotStartedAt = time.Now().Add(-time.Second)

	metrics := router.FastSyncMetrics()
	assert.Equal(t, snapshot.StateNodesDescended, metrics.ReplayPipelinePivotNodesExamined)
	assert.Equal(t, snapshot.StateEqualSubtreesSkipped, metrics.ReplayPipelinePivotEqualSubtrees)
	assert.Equal(t, snapshot.StateMissingDiscovered, metrics.ReplayPipelinePivotMissingNodes)
	assert.Equal(t, snapshot.StateUseful, metrics.ReplayPipelinePivotStateDownloaded)
	assert.Positive(t, metrics.ReplayPipelinePivotStateNodesPerSec)
}
