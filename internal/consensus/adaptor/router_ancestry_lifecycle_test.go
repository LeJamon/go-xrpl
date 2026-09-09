package adaptor

import (
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/ledger/inbound"
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/stretchr/testify/require"
)

func TestHeaderDiscoveryLateCanceledReplyDoesNotChargePeer(t *testing.T) {
	r, sender := makeRouterWithBadDataRecorder(t)
	base := r.adaptor.LedgerService().GetClosedLedger()
	parent := base
	var target standardReplayTestLink
	for range 30 {
		target = buildAlternativeReplaySuccessor(t, parent, time.Second)
		parent = target.ledger
	}
	svc := r.adaptor.LedgerService()
	signTime := base.CloseTime()
	svc.SetValidatedLedgerAt(base.Sequence(), base.Hash(), signTime)
	svc.SetValidatedLedgerAgeClock(func() time.Time { return signTime.Add(90 * time.Second) })
	require.False(t, r.invalidFutureLedgerSequence(target.seq))
	startTestHeaderDiscovery(t, r, base.Sequence(), target, 7, catchupSourceQuorum)
	r.cancelHeaderDiscovery()
	sendTestHeaderReply(t, r, 7, target)
	require.Empty(t, sender.getBadDataCalls())
	require.Nil(t, r.headerDiscovery)
	_, published := r.lookupSeqHash(target.seq)
	require.False(t, published)
}

func TestRequestedRecoveryTransactionReplyRespectsValidationWindow(t *testing.T) {
	r, sender := makeRouterWithBadDataRecorder(t)
	seq := r.adaptor.LedgerService().GetValidatedLedgerIndex() + 30
	paymentService := closedLedgerWithPayment(t)
	t.Cleanup(paymentService.Stop)
	paymentLedger := paymentService.GetClosedLedger()
	h := paymentLedger.Header()
	h.LedgerIndex = seq
	hash := header.CalculateHash(h)
	txMap, err := paymentLedger.TxMapSnapshot()
	require.NoError(t, err)
	wire, err := txMap.WalkWireNodes()
	require.NoError(t, err)
	require.Greater(t, len(wire), 1)
	il := inbound.New(hash, seq, 7, r.logger, inbound.WithTransactionOnly())
	require.NoError(t, il.GotBase([]message.LedgerNode{
		{NodeData: header.AddRaw(h, false)}, {}, {NodeData: wire[0].Data},
	}))
	require.False(t, il.IsComplete())
	r.recordValidationCatchupTarget(seq, hash, 7, catchupSourceQuorum)
	r.fetchTracker.Track(il)
	require.True(t, r.invalidFutureLedgerSequence(seq))
	data := &message.LedgerData{
		LedgerHash: hash[:], LedgerSeq: seq, InfoType: message.LedgerInfoTxNode,
	}
	for _, node := range wire[1:] {
		data.Nodes = append(data.Nodes, message.LedgerNode{NodeID: node.NodeID, NodeData: node.Data})
	}
	r.handleMessage(&peermanagement.InboundMessage{
		PeerID: 7, Type: message.TypeLedgerData, Payload: encodePayload(t, data),
	})
	bad := sender.getBadDataCalls()
	require.Len(t, bad, 1)
	require.Equal(t, "ledger-data-sequence", bad[0].reason)
	require.False(t, il.IsComplete())
	svc := r.adaptor.LedgerService()
	base := svc.GetValidatedLedger()
	signTime := base.CloseTime()
	svc.SetValidatedLedgerAt(base.Sequence(), base.Hash(), signTime)
	svc.SetValidatedLedgerAgeClock(func() time.Time { return signTime.Add(90 * time.Second) })
	require.False(t, r.invalidFutureLedgerSequence(seq))
	r.handleMessage(&peermanagement.InboundMessage{
		PeerID: 7, Type: message.TypeLedgerData, Payload: encodePayload(t, data),
	})
	require.Len(t, sender.getBadDataCalls(), 1)
	require.True(t, il.IsComplete())
}

func TestHeaderDiscoveryReplacesWeakerAcquiredBranch(t *testing.T) {
	r, _, _, svc := makeRouter(t)
	base := svc.GetClosedLedger()
	first := buildAlternativeReplaySuccessor(t, base, time.Second)
	second := buildAlternativeReplaySuccessor(t, first.ledger, time.Second)
	old := buildAlternativeReplaySuccessor(t, base, 2*time.Second)
	r.recordAcquiredSeqHash(old.seq, old.hash, base.Hash())
	startTestHeaderDiscovery(t, r, base.Sequence(), second, 7, catchupSourceQuorum)
	sendTestHeaderReply(t, r, 7, second)
	sendTestHeaderReply(t, r, 7, first)
	entry, known := r.lookupSeqHash(first.seq)
	require.True(t, known)
	require.Equal(t, first.hash, entry.hash)
	require.True(t, r.recoveryAnchorReachesTarget(base.Sequence(), base.Hash(), second.hash))
}

func TestHeaderDiscoveryRetiresSameTargetFullStatePivot(t *testing.T) {
	r, _, _, svc := makeRouter(t)
	base := svc.GetClosedLedger()
	target := buildAlternativeReplaySuccessor(t, base, time.Second)
	require.True(t, r.beginFrozenPivotRecovery(target.seq, target.hash, 7))
	acquisition := r.fetchTracker.Find(target.hash)
	require.NotNil(t, acquisition)
	require.False(t, acquisition.TransactionOnly())
	require.True(t, r.standardReplay.active)
	startTestHeaderDiscovery(t, r, base.Sequence(), target, 7, catchupSourceQuorum)
	require.False(t, r.standardReplay.active)
	require.Nil(t, r.fetchTracker.Find(target.hash))
	sendTestHeaderReply(t, r, 7, target)
	entry, known := r.lookupSeqHash(target.seq)
	require.True(t, known)
	require.Equal(t, target.hash, entry.hash)
}
