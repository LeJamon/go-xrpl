package adaptor

import (
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/ledger/genesis"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/ledger/inbound"
	"github.com/LeJamon/go-xrpl/internal/ledger/service"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/stretchr/testify/require"
)

func completeIssue1863FullStatePivot(t *testing.T, r *Router, pivot standardReplayTestLink) {
	t.Helper()

	il := r.catchupReplay.fetchTracker.Find(pivot.hash)
	require.NotNil(t, il)
	require.False(t, il.TransactionOnly())

	h := pivot.ledger.Header()
	stateMap, err := pivot.ledger.StateMapSnapshot()
	require.NoError(t, err)
	stateRoot, err := stateMap.SerializeRoot()
	require.NoError(t, err)

	base := []message.LedgerNode{
		{NodeData: header.AddRaw(h, false)},
		{NodeData: stateRoot},
	}
	if h.TxHash != ([32]byte{}) {
		txMap, txErr := pivot.ledger.TxMapSnapshot()
		require.NoError(t, txErr)
		txRoot, txErr := txMap.SerializeRoot()
		require.NoError(t, txErr)
		base = append(base, message.LedgerNode{NodeData: txRoot})
	}
	require.NoError(t, il.GotBase(base))

	stateWire, err := stateMap.WalkWireNodes()
	require.NoError(t, err)
	stateNodes := make([]message.LedgerNode, 0, len(stateWire))
	for _, node := range stateWire {
		stateNodes = append(stateNodes, message.LedgerNode{NodeID: node.NodeID, NodeData: node.Data})
	}
	require.NoError(t, il.GotStateNodes(stateNodes))
	if h.TxHash != ([32]byte{}) {
		txMap, txErr := pivot.ledger.TxMapSnapshot()
		require.NoError(t, txErr)
		txWire, txErr := txMap.WalkWireNodes()
		require.NoError(t, txErr)
		txNodes := make([]message.LedgerNode, 0, len(txWire))
		for _, node := range txWire {
			txNodes = append(txNodes, message.LedgerNode{NodeID: node.NodeID, NodeData: node.Data})
		}
		require.NoError(t, il.GotTransactionNodes(txNodes))
	}
	il.CollectMissingRequest(false)
	require.True(t, il.IsComplete())
	r.catchupReplay.completeInboundLedger(il)
}

func TestIssue1863MixedRecoveryRetainsPivotAndResumes(t *testing.T) {
	for _, orphan := range []bool{false, true} {
		name := "retained pivot"
		if orphan {
			name = "rearmed orphan"
		}
		t.Run(name, func(t *testing.T) { testIssue1863MixedRecovery(t, orphan) })
	}
}

func testIssue1863MixedRecovery(t *testing.T, orphan bool) {
	svc, err := service.New(service.Config{GenesisConfig: genesis.DefaultConfig()})
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)
	require.NoError(t, svc.SwitchToPreferredLedger(svc.GetClosedLedger()))
	require.False(t, svc.NeedsInitialSync())
	require.False(t, svc.IsFastLoadProvisional())

	a, sender := newRecordingAdaptor(t, svc)
	sender.mu.Lock()
	sender.peerSupportsReplay = false
	sender.mu.Unlock()

	engine := &mockEngine{switchResult: consensus.LedgerSwitchAccepted}
	engine.switchHook = func(id consensus.LedgerID) {
		selected, getErr := a.GetLedger(id)
		require.NoError(t, getErr)
		require.NoError(t, a.OnLedgerSwitched(selected))
	}
	r := newTestRouter(engine, a, nil)

	parent := svc.GetClosedLedger()
	require.NotNil(t, parent)
	pivot := buildAlternativeReplaySuccessor(t, parent, time.Second)
	child := buildAlternativeReplaySuccessor(t, pivot.ledger, time.Second)
	r.catchupReplay.recordSeqHash(pivot.seq, pivot.hash, parent.Hash(), true)
	r.catchupReplay.recordSeqHash(child.seq, child.hash, pivot.hash, true)
	trackCatchupPeer(r, 7, child.seq, child.hash)
	require.True(t, r.catchupReplay.beginFrozenPivotRecovery(pivot.seq, pivot.hash, 7))

	for svc.GetClosedLedgerIndex() < child.seq {
		_, err := svc.AcceptConsensusResult(t.Context(), svc.GetClosedLedger(), nil, nil, time.Now(), true)
		require.NoError(t, err)
	}
	require.Less(t, svc.GetValidatedLedgerIndex(), child.seq)

	trusted, err := a.GetValidatorKey()
	require.NoError(t, err)
	r.catchupReplay.maybeAcquireFromValidation(&consensus.Validation{
		NodeID:    trusted,
		LedgerSeq: child.seq,
		LedgerID:  consensus.LedgerID(child.hash),
		Full:      true,
	}, 7)
	childAcquisition := r.catchupReplay.fetchTracker.Find(child.hash)
	require.NotNil(t, childAcquisition)
	require.True(t, childAcquisition.TransactionOnly())
	completeStandardReplayTestLink(t, r, child)

	fallback := r.catchupReplay.fetchTracker.Find(child.hash)
	require.NotNil(t, fallback)
	require.False(t, fallback.TransactionOnly())
	require.Equal(t, 2, r.catchupReplay.protectedCatchupInFlight())

	pivotAcquisition := r.catchupReplay.fetchTracker.Find(pivot.hash)
	require.NotNil(t, pivotAcquisition)
	now := time.Now()
	pivotAcquisition.RearmTimer(now)
	for range 2 {
		now = now.Add(4 * time.Second)
		require.Equal(t, inbound.TimerEscalate, pivotAcquisition.OnTimer(now))
		pivotAcquisition.RearmTimer(now)
	}

	r.catchupReplay.onLedgerFullyValidated(child.seq, child.hash)
	require.Same(t, pivotAcquisition, r.catchupReplay.fetchTracker.Find(pivot.hash),
		"quorum eviction must retain the active automatic recovery pivot")
	require.True(t, r.catchupReplay.standardReplay.active)
	require.False(t, r.catchupReplay.standardReplay.pivotReady)
	require.Equal(t, pivot.hash, r.catchupReplay.standardReplay.pivotHash)

	r.catchupReplay.failInboundAcquisition(fallback)
	require.Same(t, pivotAcquisition, r.catchupReplay.fetchTracker.Find(pivot.hash))
	require.True(t, r.catchupReplay.standardReplay.active)
	require.False(t, r.catchupReplay.standardReplay.pivotReady)

	successor := r.catchupReplay.fetchTracker.Find(child.hash)
	require.NotNil(t, successor)
	require.True(t, successor.TransactionOnly())
	completeStandardReplayTestLink(t, r, child)
	entry := r.catchupReplay.standardReplay.entries[child.seq]
	require.NotNil(t, entry)
	require.False(t, entry.readyAt.IsZero())
	require.Equal(t, pivot.seq, r.catchupReplay.standardReplay.anchorSeq)

	if orphan {
		generation := r.catchupReplay.standardReplay.generation
		require.True(t, r.catchupReplay.fetchTracker.DiscardExpected(pivotAcquisition))
		r.catchupReplay.retireLegacyAcquisitions([]*inbound.Ledger{pivotAcquisition})
		r.maintenanceTick()
		rearmed := r.catchupReplay.fetchTracker.Find(pivot.hash)
		require.NotNil(t, rearmed)
		require.NotSame(t, pivotAcquisition, rearmed)
		require.Equal(t, generation, r.catchupReplay.standardReplay.generation)
		require.Same(t, entry, r.catchupReplay.standardReplay.entries[child.seq])
	}

	completeIssue1863FullStatePivot(t, r, pivot)

	require.Eventually(t, func() bool {
		closed := svc.GetClosedLedger()
		return closed != nil && closed.Hash() == child.hash &&
			r.catchupReplay.standardReplay.anchorSeq == child.seq
	}, time.Second, time.Millisecond)
	require.Equal(t, child.hash, svc.GetClosedLedger().Hash())
	require.Contains(t, engine.getLedgers(), consensus.LedgerID(child.hash))
}
