package adaptor

import (
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/membudget"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/stretchr/testify/require"
)

func completePaymentReplayTestLink(t *testing.T, r *Router, link standardReplayTestLink, source *ledger.Ledger) {
	t.Helper()
	il := r.catchupReplay.fetchTracker.Find(link.hash)
	require.NotNil(t, il)
	require.True(t, il.TransactionOnly())

	txMap, err := source.TxMapSnapshot()
	require.NoError(t, err)
	require.NotNil(t, txMap)
	wire, err := txMap.WalkWireNodes()
	require.NoError(t, err)
	require.NotEmpty(t, wire)

	byID := make(map[string]message.LedgerNode, len(wire))
	for _, node := range wire {
		byID[string(node.NodeID)] = message.LedgerNode{
			NodeID:   node.NodeID,
			NodeData: node.Data,
		}
	}
	require.NoError(t, il.GotBase([]message.LedgerNode{
		{NodeData: link.response.LedgerHeader},
		{NodeData: []byte{1}},
		{NodeData: wire[0].Data},
	}))

	for !il.IsComplete() {
		_, ids := il.CollectMissingRequest(false)
		if il.IsComplete() {
			break
		}
		require.NotEmpty(t, ids, "payment replay acquisition stopped making progress: snapshot=%+v wire=%d", il.Snapshot(), len(wire))
		nodes := make([]message.LedgerNode, 0, len(ids))
		for _, id := range ids {
			node, ok := byID[string(id)]
			require.Truef(t, ok, "source payment tree is missing requested node %x", id)
			nodes = append(nodes, node)
		}
		require.NoError(t, il.GotTransactionNodes(nodes))
	}
	gotHeader, _, gotTxMap, err := il.Result()
	require.NoError(t, err)
	require.Equal(t, link.hash, gotHeader.Hash)
	require.NotNil(t, gotTxMap)
	r.catchupReplay.completeInboundLedger(il)
}

func TestStandardReplaySharedMemoryWakeAppliesPaymentAfterParentCacheRelease(t *testing.T) {
	source := closedLedgerWithPayment(t)
	t.Cleanup(source.Stop)
	sourceTarget := source.GetClosedLedger()
	require.NotNil(t, sourceTarget)
	sourceHash := sourceTarget.Hash()
	sourceParent, err := source.GetLedgerBySequence(sourceTarget.Sequence() - 1)
	require.NoError(t, err)
	require.NotNil(t, sourceParent)

	r, _, sender, svc := makeRouter(t)
	t.Cleanup(func() {
		r.StopAcquisitions()
		_ = r.CloseAcquisitionMemoryBudget()
		svc.Stop()
	})

	routerParent := svc.GetClosedLedger()
	require.NotNil(t, routerParent)
	require.Equal(t, sourceParent.Hash(), routerParent.Hash(), "standalone fixtures must share the genesis parent")

	pool := membudget.New(2 << 30)
	parent, err := pool.NewAccount("acquisition", 512<<20, 2<<30)
	require.NoError(t, err)
	cache, err := pool.NewAccount("optional_cache", 0, 2<<30)
	require.NoError(t, err)
	require.NoError(t, r.SetAcquisitionMemoryBudget(parent.Limit(), parent))

	r.catchupReplay.acquisitionWorkMu.RLock()
	budget := r.catchupReplay.acquisitionMemory
	r.catchupReplay.acquisitionWorkMu.RUnlock()
	require.NotNil(t, budget)

	const processingFill = 512 << 20
	require.True(t, budget.TryReserveReplayProcessing(processingFill))
	processingHeld := true
	cacheHeld := false
	cacheBytes := uint64(0)
	defer func() {
		if processingHeld {
			budget.ReleaseReplayProcessing(processingFill)
		}
		if cacheHeld {
			cache.Release(cacheBytes)
		}
	}()

	head := standardReplayTestLink{
		response: &message.ReplayDeltaResponse{
			LedgerHash:   sourceHash[:],
			LedgerHeader: header.AddRaw(sourceTarget.Header(), false),
		},
		ledger: sourceTarget,
		hash:   sourceHash,
		seq:    sourceTarget.Sequence(),
	}
	links := make([]standardReplayTestLink, 0, standardReplayPipelineWindow+2)
	links = append(links, head)
	r.catchupReplay.recordSeqHash(head.seq, head.hash, routerParent.Hash(), true)

	previous := sourceTarget
	for i := 1; i < cap(links); i++ {
		link := buildAlternativeReplaySuccessor(t, previous, time.Duration(i)*time.Second)
		r.catchupReplay.recordSeqHash(link.seq, link.hash, previous.Hash(), true)
		links = append(links, link)
		previous = link.ledger
	}

	sender.mu.Lock()
	sender.peerSupportsReplay = false
	sender.mu.Unlock()
	target := links[len(links)-1]
	trackCatchupPeer(r, 7, target.seq, target.hash)
	require.True(t, r.catchupReplay.tryArmStandardReplayPipeline(svc, routerParent, target.seq, target.hash, 7))
	require.Equal(t, standardReplayPipelineWindow, len(r.catchupReplay.standardReplay.entries),
		"the initial prepared window should be full before cache pressure")
	require.NotNil(t, r.catchupReplay.fetchTracker.Find(head.hash))

	// Hold the ready head until the external cache has consumed the pool's
	// elastic remainder. The first 4 MiB processing reservation then succeeds,
	// while the stored replay constructor's larger admission is forced to wait.
	r.catchupReplay.acquisitionMu.Lock()
	r.catchupReplay.standardReplay.pivotReady = false
	r.catchupReplay.standardReplay.applying = true
	preparedHead := r.catchupReplay.standardReplay.entries[head.seq]
	require.NotNil(t, preparedHead)
	require.Equal(t, head.hash, preparedHead.hash)
	require.Same(t, r.catchupReplay.fetchTracker.Find(head.hash), preparedHead.acquisition)
	r.catchupReplay.acquisitionMu.Unlock()
	completePaymentReplayTestLink(t, r, head, sourceTarget)
	r.catchupReplay.acquisitionMu.Lock()
	r.catchupReplay.standardReplay.applying = false
	r.catchupReplay.acquisitionMu.Unlock()

	available := pool.Snapshot().SharedAvailable
	require.Greater(t, available, uint64(4<<20))
	cacheBytes = available - (4 << 20)
	require.True(t, cache.TryReserve(cacheBytes))
	cacheHeld = true
	require.Equal(t, uint64(4<<20), pool.Snapshot().SharedAvailable)
	require.False(t, budget.TryReserveReplay(8<<20),
		"future replay prefetch must remain parked while the optional cache owns the elastic remainder")

	r.catchupReplay.acquisitionMu.Lock()
	readyHead := r.catchupReplay.standardReplay.entries[head.seq]
	require.NotNil(t, readyHead)
	require.False(t, readyHead.readyAt.IsZero())
	require.NotNil(t, readyHead.txMap)
	r.catchupReplay.standardReplay.pivotReady = true
	r.catchupReplay.acquisitionMu.Unlock()

	r.catchupReplay.drainStandardReplayPipeline()
	parkedMetrics := r.FastSyncMetrics()
	require.Zero(t, parkedMetrics.ReplayPipelineApplied,
		"stored replay admission must park the ready payment head before apply")
	require.Zero(t, parkedMetrics.ReplayPipelineFallbacks,
		"memory pressure must not enter the replay fault or peer fallback path")
	require.False(t, svc.ReplayBlocked(), "a transient memory wait must keep validator duties healthy")
	r.catchupReplay.acquisitionMu.Lock()
	require.False(t, r.catchupReplay.standardReplay.applying)
	require.False(t, r.catchupReplay.standardReplay.headBlockedAt.IsZero())
	r.catchupReplay.acquisitionMu.Unlock()

	usedBeforeRelease := budget.Snapshot().Used
	cache.Release(cacheBytes)
	cacheHeld = false
	require.Equal(t, usedBeforeRelease, budget.Snapshot().Used,
		"releasing a sibling parent account must not change child-owned accounting")
	require.Greater(t, pool.Snapshot().SharedAvailable, uint64(128<<20))

	r.catchupReplay.notifyAcquisitionMemoryAvailable()
	select {
	case <-r.catchupReplay.standardReplayDrainWake:
	default:
		t.Fatal("memory wake did not schedule the parked replay head")
	}
	r.catchupReplay.drainStandardReplayPipeline()

	require.Equal(t, uint64(1), r.FastSyncMetrics().ReplayPipelineApplied)
	require.Zero(t, r.FastSyncMetrics().ReplayPipelineFallbacks)
	require.False(t, svc.ReplayBlocked())
	stored, err := svc.GetLedgerByHash(head.hash)
	require.NoError(t, err)
	require.NotNil(t, stored)
	require.Equal(t, head.hash, stored.Hash())
	require.Empty(t, r.catchupReplay.standardReplayDrainWake,
		"the memory wake must not create a self-retry housekeeping loop")

	r.StopAcquisitions()
	budget.ReleaseReplayProcessing(processingFill)
	processingHeld = false
	require.True(t, r.CloseAcquisitionMemoryBudget())
	require.Zero(t, budget.Snapshot().Used)
	require.Zero(t, pool.Snapshot().Used)
	require.Zero(t, pool.Snapshot().InvalidReleases)
}
