package inbound

import (
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/LeJamon/go-xrpl/shamap/backend"
	"github.com/stretchr/testify/require"
)

func TestByHashPipeline_ReplyDrivenDisjointAndBounded(t *testing.T) {
	source := shamap.New(shamap.TypeState)
	for i := 0; i < 4096; i++ {
		var key [32]byte
		key[0], key[1] = byte(i>>4), byte(i<<4)
		key[31] = 0xA5
		require.NoError(t, source.Put(key, make([]byte, 12)))
	}
	root, err := source.Hash()
	require.NoError(t, err)
	rootData, err := source.SerializeRoot()
	require.NoError(t, err)
	nodes, err := source.WalkFetchPackNodes(1 << 24)
	require.NoError(t, err)
	objects := make(map[[32]byte][]byte)
	for _, n := range nodes {
		objects[n.Hash] = n.Data
	}
	hdr, hash := encodeHeader(header.LedgerHeader{LedgerIndex: 321, AccountHash: root})
	budget := shamap.NewAcquisitionBudget(16 << 20)
	il := New(hash, 321, 1, discardLogger(), WithFamily(backend.NewMemory()), WithAcquisitionBudget(budget))
	for peer := uint64(2); peer <= 8; peer++ {
		il.AddPeer(peer)
	}
	require.NoError(t, il.GotBase([]message.LedgerNode{{NodeData: hdr}, {NodeData: rootData}}))
	now := time.Now()
	for tick := 1; tick <= 6; tick++ {
		il.OnTimer(now.Add(time.Duration(tick) * 4 * time.Second))
	}
	require.True(t, il.ByHashMode())
	timeouts := il.Timeouts()
	peers := []uint64{1, 2, 3, 4, 5, 6, 7, 8}
	cache := make(map[[32]byte][]byte)
	fetch := func(h [32]byte) ([]byte, bool) {
		d, ok := cache[h]
		delete(cache, h)
		return d, ok
	}
	maxBatch := 0
	for round := 0; round < 128 && !il.IsComplete(); round++ {
		plan, err := il.PlanMissingReplyRequestsContext(t.Context(), peers)
		require.NoError(t, err)
		if plan.Outcome == MissingRequestComplete {
			break
		}
		if len(plan.Requests) == 0 && plan.Outcome == MissingRequestContinue {
			continue
		}
		require.NotEmpty(t, plan.Requests, "round=%d outcome=%d useful=%d leases=%d cached=%d", round, plan.Outcome, il.Snapshot().StateUseful, len(il.requestLeases), len(cache))
		require.LessOrEqual(t, len(plan.Requests), requestPeerLimit)
		seen := make(map[[32]byte]bool)
		for _, req := range plan.Requests {
			require.True(t, req.ByHash)
			require.LessOrEqual(t, len(req.NodeHashes), reqNodesReply)
			maxBatch = max(maxBatch, len(req.NodeHashes))
			for _, h := range req.NodeHashes {
				require.False(t, seen[h], "peers must not request duplicate hashes")
				seen[h] = true
				require.Contains(t, objects, h)
				cache[h] = objects[h]
			}
		}
		require.LessOrEqual(t, len(il.requestLeases), il.requestReservationLimit())
		_, _, err = il.CheckLocalContext(t.Context(), fetch)
		require.NoError(t, err)
	}
	require.True(t, il.IsComplete())
	require.Greater(t, maxBatch, 4)
	require.Equal(t, timeouts, il.Timeouts(), "no timer ticks needed after escalation")
	snap := il.Snapshot()
	require.Greater(t, snap.StateUseful, uint64(4096))
	require.Equal(t, snap.StateUseful, snap.StateCacheUseful)
	require.Equal(t, snap.StateUseful, snap.StateReceived)
	require.Zero(t, snap.RequestPeers)
	require.Zero(t, snap.OutstandingNodes)
	verified, err := il.stateMap.Hash()
	require.NoError(t, err)
	require.Equal(t, root, verified)
	require.LessOrEqual(t, budget.Snapshot().Peak, budget.Snapshot().Limit)
	require.Positive(t, il.requestMetadataBytes)
	require.NoError(t, il.RetirePersistence(t.Context()))
	require.Zero(t, budget.Snapshot().Used)
}

func TestByHashPipeline_MetadataPressureDoesNotIssueUnboundedRequests(t *testing.T) {
	budget := shamap.NewAcquisitionBudget(8 << 20)
	il := newWideAcquisition(t, WithAcquisitionBudget(budget))
	il.byHashMode = true
	held := budget.Snapshot().Limit - budget.Snapshot().Used
	require.True(t, budget.TryReserve(held))
	plan, err := il.PlanMissingReplyRequestsContext(t.Context(), []uint64{1, 2, 3, 4, 5, 6})
	require.NoError(t, err)
	require.Equal(t, MissingRequestWaitMemory, plan.Outcome)
	require.Empty(t, plan.Requests)
	require.Empty(t, il.requestLeases)
	budget.Release(held)
	plan, err = il.PlanMissingReplyRequestsContext(t.Context(), []uint64{1, 2, 3, 4, 5, 6})
	require.NoError(t, err)
	require.NotEmpty(t, plan.Requests)
	for _, req := range plan.Requests {
		require.True(t, req.ByHash)
	}
	require.NoError(t, il.RetirePersistence(t.Context()))
	require.Zero(t, budget.Snapshot().Used)
}

func TestByHashPipeline_TimersRetainLiveLeasesAndRetryExpiredOnes(t *testing.T) {
	il := newWideAcquisition(t)
	il.byHashMode = true
	peers := []uint64{1, 2, 3, 4, 5, 6}
	plan, err := il.PlanMissingReplyRequestsContext(t.Context(), peers)
	require.NoError(t, err)
	require.Len(t, plan.Requests, requestPeerLimit)
	now := time.Now()
	il.lastTimer = now
	before := make(map[[32]byte]uint64)
	for h, lease := range il.requestLeases {
		before[h] = lease.peerID
	}
	il.OnTimer(now.Add(acquireTimerInterval))
	for h, peer := range before {
		require.Equal(t, peer, il.requestLeases[h].peerID, "a progress tick must not duplicate a live request")
	}
	il.OnTimer(now.Add(missingRequestLeaseTTL + time.Second))
	retry, err := il.PlanMissingReplyRequestsContext(t.Context(), []uint64{11, 12, 13, 14, 15, 16})
	require.NoError(t, err)
	require.NotEmpty(t, retry.Requests)
	seen := make(map[[32]byte]bool)
	for _, req := range retry.Requests {
		require.True(t, req.ByHash)
		require.GreaterOrEqual(t, req.PeerID, uint64(11))
		for _, hash := range req.NodeHashes {
			require.False(t, seen[hash], "expired work must still have a single owner")
			seen[hash] = true
		}
	}
	require.LessOrEqual(t, len(il.requestLeases), il.requestReservationLimit())
}

func TestCheckLocal_CountersRejectInvalidAndIgnoreDuplicates(t *testing.T) {
	source, root, rootData := buildSourceStateMap(t)
	nodes, err := source.WalkFetchPackNodes(1 << 20)
	require.NoError(t, err)
	cache := make(map[[32]byte][]byte)
	for _, n := range nodes {
		cache[n.Hash] = n.Data
	}
	hdr, hash := encodeHeader(header.LedgerHeader{LedgerIndex: 322, AccountHash: root})
	il := New(hash, 322, 1, discardLogger(), WithFamily(backend.NewMemory()))
	require.NoError(t, il.GotBase([]message.LedgerNode{{NodeData: hdr}, {NodeData: rootData}}))
	plan, err := il.PlanMissingReplyRequestsContext(t.Context(), []uint64{1, 2})
	require.NoError(t, err)
	require.NotEmpty(t, plan.Requests)
	before := il.Snapshot()
	il.CheckLocal(func([32]byte) ([]byte, bool) { return []byte("invalid"), true })
	after := il.Snapshot()
	require.Equal(t, before.StateUseful, after.StateUseful)
	require.Equal(t, before.OutstandingNodes, after.OutstandingNodes)
	require.False(t, il.IsComplete())
	il.CheckLocal(func(h [32]byte) ([]byte, bool) { d, ok := cache[h]; return d, ok })
	require.True(t, il.IsComplete())
	after = il.Snapshot()
	require.Equal(t, uint64(84), after.StateCacheUseful)
	require.Equal(t, after.StateCacheUseful, after.StateUseful)
	require.Zero(t, after.OutstandingNodes)
	il.CheckLocal(func(h [32]byte) ([]byte, bool) { d, ok := cache[h]; return d, ok })
	require.Equal(t, after.StateUseful, il.Snapshot().StateUseful)
}
