package adaptor

import (
	"encoding/binary"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/membudget"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/LeJamon/go-xrpl/shamap/backend"
	"github.com/stretchr/testify/require"
)

func TestAcquisitionCompletionMemorySurvivesPreparedReplayPlateau(t *testing.T) {
	const count = 640
	r, _, sender, svc := makeRouter(t)
	_, err := svc.AcceptLedger(t.Context())
	require.NoError(t, err)
	pool := membudget.New(2 << 30)
	parent, err := pool.NewAccount("acquisition", 512<<20, 2<<30)
	require.NoError(t, err)
	cache, err := pool.NewAccount("cache", 0, 2<<30)
	require.NoError(t, err)
	cacheBytes := pool.Snapshot().SharedAvailable - (64 << 20)
	require.True(t, cache.TryReserve(cacheBytes))
	require.NoError(t, r.SetAcquisitionMemoryBudget(parent.Limit(), parent))
	base := backend.NewMemory()
	t.Cleanup(func() { require.NoError(t, base.Close()) })
	r.SetAcquisitionFamily(base)
	store := r.catchupReplay.acquisitionStore
	store.start(t.Context())
	t.Cleanup(store.stopDrain)
	links := make([]standardReplayTestLink, 0, count)
	trees := make([]*shamap.SHAMap, 0, count)
	previous := svc.GetClosedLedger().Header()
	for i := range count {
		tree := shamap.New(shamap.TypeTransaction)
		for index := range 2 {
			payload := make([]byte, 12)
			binary.BigEndian.PutUint64(payload, uint64(i*2+index+1))
			blob, id := txWithMetaBlob(t, payload, uint32(index))
			require.NoError(t, tree.PutWithNodeType(id, blob, shamap.NodeTypeTransactionWithMeta))
		}
		h := previous
		h.LedgerIndex++
		h.ParentHash = previous.Hash
		h.TxHash, err = tree.Hash()
		require.NoError(t, err)
		h.Hash = header.CalculateHash(h)
		r.catchupReplay.recordSeqHash(h.LedgerIndex, h.Hash, h.ParentHash, true)
		links = append(links, standardReplayTestLink{hash: h.Hash, seq: h.LedgerIndex,
			response: &message.ReplayDeltaResponse{LedgerHeader: header.AddRaw(h, false)}})
		trees = append(trees, tree)
		previous = h
	}
	sender.peerSupportsReplay = false
	target := links[len(links)-1]
	trackCatchupPeer(r, 7, target.seq, target.hash)
	require.True(t, r.catchupReplay.tryArmStandardReplayPipeline(svc, svc.GetClosedLedger(), target.seq, target.hash, 7))
	r.catchupReplay.standardReplay.pivotReady = false
	budget := r.catchupReplay.acquisitionMemory
	for i, link := range links {
		il := r.catchupReplay.fetchTracker.Find(link.hash)
		require.NotNil(t, il, "collector stopped at completion %d", i+1)
		root, err := trees[i].SerializeRoot()
		require.NoError(t, err)
		require.NoError(t, il.GotBase([]message.LedgerNode{{NodeData: link.response.LedgerHeader}, {NodeData: []byte{1}}, {NodeData: root}}))
		_, ids := il.CollectMissingRequest(false)
		require.NotEmpty(t, ids, "request metadata could not be admitted at completion %d", i+1)
		wire, err := trees[i].WalkWireNodes()
		require.NoError(t, err)
		nodes := make([]message.LedgerNode, 0, len(wire))
		for _, node := range wire {
			nodes = append(nodes, message.LedgerNode{NodeID: node.NodeID, NodeData: node.Data})
		}
		require.NoError(t, il.GotTransactionNodes(nodes))
		require.NoError(t, il.FlushPersistence(t.Context()))
		_, _, _, err = il.CollectMissingRequestContext(t.Context(), false)
		require.NoError(t, err)
		require.True(t, il.IsComplete(), "%+v", il.Snapshot())
		r.catchupReplay.completeInboundLedger(il)
		_, _, adopted, err := il.Result()
		require.NoError(t, err)
		snapshot, err := adopted.SnapshotImmutableContext(t.Context())
		require.NoError(t, err)
		token := snapshot.PrepareCleanResidencyRelease()
		require.NotNil(t, token)
		require.Same(t, base, token.Family())
		require.Less(t, budget.Snapshot().Used, uint64(128<<20), "transient ownership accumulated after completion %d", i+1)
	}
	require.Equal(t, uint64(count), r.FastSyncMetrics().ReplayPipelineReady)
	require.Zero(t, r.FastSyncMetrics().ReplayPipelineApplied)
	require.Equal(t, count, len(r.catchupReplay.standardReplay.entries))
	require.Equal(t, uint64(count*standardReplayEntryMetadataBytes+standardReplayMapMemoryBytes), budget.Snapshot().Used,
		"only prepared-entry metadata and retained map capacity should survive durable completion")
	t.Logf("prepared=%d charged_bytes=%d parent_committed_bytes=%d", count, budget.Snapshot().Used, parent.Snapshot().Used)
	r.StopAcquisitions()
	require.Zero(t, budget.Snapshot().Used)
	store.stopDrain()
	require.True(t, r.CloseAcquisitionMemoryBudget())
	cache.Release(cacheBytes)
	require.Zero(t, pool.Snapshot().Used)
	require.Zero(t, pool.Snapshot().InvalidReleases)
}
