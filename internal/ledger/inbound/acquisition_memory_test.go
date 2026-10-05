package inbound

import (
	"errors"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/LeJamon/go-xrpl/shamap/backend"
	"github.com/stretchr/testify/require"
)

func TestMemoryCheckpointBelowUsefulNodeThreshold(t *testing.T) {
	family := &checkpointFamily{Family: backend.NewMemory()}
	ledger := New([32]byte{1}, 1, 1, discardLogger(), WithFamily(family))
	require.NoError(t, ledger.CheckpointPersistence(t.Context(), 1))
	require.Zero(t, family.flushes)
	require.NoError(t, ledger.CheckpointMemory(t.Context()))
	require.Equal(t, 1, family.flushes)
	require.Zero(t, ledger.checkpointUseful)
	require.Equal(t, StateWantBase, ledger.State())
}

func TestMemoryCheckpointFailureKeepsProgressRetryable(t *testing.T) {
	wantErr := errors.New("checkpoint failed")
	family := &checkpointFamily{Family: backend.NewMemory(), err: wantErr}
	ledger := New([32]byte{1}, 1, 1, discardLogger(), WithFamily(family))
	require.NoError(t, ledger.CheckpointPersistence(t.Context(), 1))
	require.ErrorIs(t, ledger.CheckpointMemory(t.Context()), wantErr)
	require.Equal(t, uint64(1), ledger.checkpointUseful)
	require.Equal(t, StateWantBase, ledger.State())
	family.err = nil
	require.NoError(t, ledger.CheckpointMemory(t.Context()))
	require.Zero(t, ledger.checkpointUseful)
}

func TestMemoryWaitDoesNotExhaustPeerRetries(t *testing.T) {
	ledger := New([32]byte{1}, 1, 1, discardLogger())
	ledger.SetMemoryWaiting(true)
	now := time.Now()
	for range ledgerTimeoutRetriesMax + 2 {
		now = now.Add(acquireTimerInterval)
		require.Equal(t, TimerRefresh, ledger.OnTimer(now))
	}
	require.Zero(t, ledger.Timeouts())
	require.Equal(t, StateWantBase, ledger.State())

	ledger.SetMemoryWaiting(false)
	require.Equal(t, TimerEscalate, ledger.OnTimer(now.Add(acquireTimerInterval)))
	require.Equal(t, 1, ledger.Timeouts())
}

func TestMemoryWaitStillDetectsUnansweredPeerRequests(t *testing.T) {
	ledger := New([32]byte{1}, 1, 1, discardLogger())
	ledger.SetMemoryWaiting(true)
	now := time.Now()
	for attempt := 0; attempt <= ledgerTimeoutRetriesMax; attempt++ {
		ledger.mu.Lock()
		ledger.requestPeers[1] = struct{}{}
		ledger.mu.Unlock()
		now = now.Add(acquireTimerInterval)
		if attempt == ledgerTimeoutRetriesMax {
			require.Equal(t, TimerFailed, ledger.OnTimer(now))
		} else {
			require.Equal(t, TimerEscalate, ledger.OnTimer(now))
		}
	}
	require.Equal(t, StateFailed, ledger.State())
}

func TestRetirementDefersMemoryReleaseWithoutWaitingForTraversal(t *testing.T) {
	budget := shamap.NewAcquisitionBudget(0)
	ledger := newWideAcquisition(t, WithFamily(backend.NewMemory()), WithAcquisitionBudget(budget))
	require.Positive(t, budget.Snapshot().Used)
	ledger.workMu.Lock()
	retired := make(chan error, 1)
	go func() { retired <- ledger.RetirePersistence(t.Context()) }()
	select {
	case err := <-retired:
		require.NoError(t, err)
	case <-time.After(time.Second):
		ledger.finishWork()
		t.Fatal("retirement waited for traversal")
	}
	require.Positive(t, budget.Snapshot().Used, "the active traversal still owns its references")
	ledger.finishWork()
	require.Zero(t, budget.Snapshot().Used)
	require.NoError(t, ledger.RetirePersistence(t.Context()))
	require.Zero(t, budget.Snapshot().Used)
}

func TestPartialTraversalKeepsPeerRequestOwnership(t *testing.T) {
	ledger := newWideAcquisition(t, WithFamily(backend.NewMemory()))
	peers := []uint64{11, 12, 13, 14, 15, 16}
	requests, complete, err := ledger.CollectMissingReplyRequestsContext(shamap.WithTraversalBudget(t.Context(), 128), peers)
	require.ErrorIs(t, err, shamap.ErrTraversalBudget)
	require.False(t, complete)
	require.Len(t, requests, 1, "a limited frontier should fill one request before adding peers")
	require.NotEmpty(t, requests[0].NodeIDs)
	require.LessOrEqual(t, len(requests[0].NodeIDs), reqNodesReply)
	seen := make(map[[32]byte]struct{})
	for _, request := range requests {
		require.Contains(t, ledger.requestPeers, request.PeerID)
		for _, hash := range request.NodeHashes {
			require.Equal(t, request.PeerID, ledger.recentNodes[hash])
			seen[hash] = struct{}{}
		}
	}
	next, complete, err := ledger.CollectMissingReplyRequestsContext(t.Context(), peers)
	require.NoError(t, err)
	require.False(t, complete)
	require.Len(t, next, len(peers)-len(requests))
	for _, request := range next {
		require.Len(t, request.NodeIDs, reqNodesReply)
		for _, hash := range request.NodeHashes {
			require.NotContains(t, seen, hash)
			seen[hash] = struct{}{}
		}
	}
	ledger.releaseMissingRequests(append(requests, next...))
	require.Empty(t, ledger.requestPeers)
	require.Empty(t, ledger.recentNodes)
}

func TestPartialTraversalKeepsTimerAndByHashRequests(t *testing.T) {
	t.Run("timer", func(t *testing.T) {
		ledger := newWideAcquisition(t, WithFamily(backend.NewMemory()))
		state, tx, complete, err := ledger.CollectMissingRequestContext(shamap.WithTraversalBudget(t.Context(), 128), true)
		require.ErrorIs(t, err, shamap.ErrTraversalBudget)
		require.NotEmpty(t, state)
		require.Empty(t, tx)
		require.False(t, complete)
		require.False(t, ledger.haveState)
		require.NotEmpty(t, ledger.missingState)
	})
	t.Run("by hash", func(t *testing.T) {
		ledger := newWideAcquisition(t, WithFamily(backend.NewMemory()))
		ledger.byHash = true
		ledger.timeouts = ledgerBecomeAggressiveThreshold + 1
		state, tx, err := ledger.TakeByHashRequestContext(shamap.WithTraversalBudget(t.Context(), 128), missingNodesFind)
		require.ErrorIs(t, err, shamap.ErrTraversalBudget)
		require.NotEmpty(t, state)
		require.Empty(t, tx)
		require.True(t, ledger.byHash)
		require.False(t, ledger.haveState)
	})
}
