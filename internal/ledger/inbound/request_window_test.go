package inbound

import (
	"context"
	"testing"

	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/stretchr/testify/require"
)

func TestRequestWindowMetadataReservationReleasedOnRetirement(t *testing.T) {
	budget := shamap.NewAcquisitionBudget(8 << 20)
	ledger := newWideAcquisition(t, WithAcquisitionBudget(budget))
	t.Cleanup(func() { _ = ledger.RetirePersistence(context.Background()) })

	requests, complete, err := ledger.CollectMissingReplyRequestsContext(t.Context(), []uint64{11})
	require.NoError(t, err)
	require.False(t, complete)
	require.Len(t, requests, 1)

	expected := uint64(ledger.requestReservationLimit()) * missingRequestMetadataBytes
	require.Equal(t, expected, ledger.requestMetadataBytes)
	charged := budget.Snapshot().Used
	require.GreaterOrEqual(t, charged, expected)

	ledger.ReleaseMissingRequest(requests[0].PeerID, requests[0].NodeHashes)
	require.Equal(t, expected, ledger.requestMetadataBytes,
		"map and slice high-water capacity stays charged while the acquisition is active")
	require.Equal(t, charged, budget.Snapshot().Used,
		"releasing individual leases must not release retained metadata capacity")

	require.NoError(t, ledger.RetirePersistence(t.Context()))
	require.Zero(t, ledger.requestMetadataBytes)
	require.Less(t, budget.Snapshot().Used, charged)

	ledger.mu.Lock()
	require.Nil(t, ledger.requestLeases)
	require.Nil(t, ledger.requestByNode)
	require.Nil(t, ledger.recentNodes)
	require.Nil(t, ledger.requestPeers)
	require.Nil(t, ledger.readyRequests)
	require.Nil(t, ledger.readySet)
	ledger.mu.Unlock()

	ledger.ReleaseMissingRequest(requests[0].PeerID, requests[0].NodeHashes)
	ledger.ReleaseMissingPeer(requests[0].PeerID)
	after, complete, err := ledger.CollectMissingReplyRequestsContext(t.Context(), []uint64{22})
	require.NoError(t, err)
	require.Empty(t, after)
	require.False(t, complete)
	require.Zero(t, budget.Snapshot().Used)
}

func TestRequestWindowMetadataPressureWaitsForMemory(t *testing.T) {
	budget := shamap.NewAcquisitionBudget(8 << 20)
	ledger := newWideAcquisition(t, WithAcquisitionBudget(budget))
	t.Cleanup(func() { require.NoError(t, ledger.RetirePersistence(context.Background())) })
	metadataBytes := uint64(ledger.requestReservationLimit()) * missingRequestMetadataBytes
	held := budget.Snapshot().Limit - budget.Snapshot().Used - metadataBytes + 1
	require.True(t, budget.TryReserve(held))
	defer budget.Release(held)
	before := ledger.stateMap.BackedWalkStats().NodesDescended
	for range 2 {
		plan, err := ledger.PlanMissingReplyRequestsContext(t.Context(), []uint64{11, 12, 13, 14, 15, 16})
		require.NoError(t, err)
		require.Equal(t, MissingRequestWaitMemory, plan.Outcome)
		require.Empty(t, plan.Requests)
		require.Empty(t, ledger.requestLeases)
		require.Zero(t, ledger.requestMetadataBytes)
		require.True(t, ledger.Snapshot().MemoryWaiting)
		require.True(t, ledger.AcquisitionMemoryWaiting())
		for _, isReply := range []bool{false, true} {
			state, tx, complete, err := ledger.CollectMissingRequestContext(t.Context(), isReply)
			require.ErrorIs(t, err, shamap.ErrAcquisitionMemory)
			require.Empty(t, state)
			require.Empty(t, tx)
			require.False(t, complete)
		}
		require.Equal(t, before, ledger.stateMap.BackedWalkStats().NodesDescended)
	}
}
