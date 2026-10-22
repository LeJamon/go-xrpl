package adaptor

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/ledger/inbound"
	"github.com/LeJamon/go-xrpl/internal/ledger/service"
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/stretchr/testify/require"
)

func canonicalSignedPaymentBlob(t *testing.T, env *jtx.TestEnv, sender, receiver *jtx.Account, sequence uint32) []byte {
	t.Helper()
	env.SetVerifySignatures(true)
	txn := payment.Pay(sender, receiver, 100_000_000).Sequence(sequence).Build()
	env.SignWith(txn, sender)
	fields, err := txn.Flatten()
	require.NoError(t, err)
	blob, err := binarycodec.EncodeBytes(fields)
	require.NoError(t, err)
	return blob
}

func buildCanonicalReplayPaymentSuccessors(
	t *testing.T,
	parent *ledger.Ledger,
) (*service.Service, *ledger.Ledger, *ledger.Ledger) {
	t.Helper()
	builder := newTestLedgerService(t)
	t.Cleanup(builder.Stop)
	require.NoError(t, builder.SwitchToPreferredLedger(parent))

	env := jtx.NewTestEnv(t)
	master := jtx.MasterAccount()
	firstReceiver := jtx.NewAccount("replay-first")
	firstSecondReceiver := jtx.NewAccount("replay-first-second")
	secondReceiver := jtx.NewAccount("replay-second")
	secondSecondReceiver := jtx.NewAccount("replay-second-second")

	firstBlob := canonicalSignedPaymentBlob(t, env, master, firstReceiver, 1)
	firstSecondBlob := canonicalSignedPaymentBlob(t, env, master, firstSecondReceiver, 2)
	_, err := builder.AcceptConsensusResult(
		t.Context(), parent, [][]byte{firstBlob, firstSecondBlob}, nil, parent.CloseTime().Add(10*time.Second), true,
	)
	require.NoError(t, err)
	first := builder.GetClosedLedger()
	require.NotNil(t, first)

	secondBlob := canonicalSignedPaymentBlob(t, env, master, secondReceiver, 3)
	secondSecondBlob := canonicalSignedPaymentBlob(t, env, master, secondSecondReceiver, 4)
	_, err = builder.AcceptConsensusResult(
		t.Context(), first, [][]byte{secondBlob, secondSecondBlob}, nil, first.CloseTime().Add(10*time.Second), true,
	)
	require.NoError(t, err)
	second := builder.GetClosedLedger()
	require.NotNil(t, second)
	return builder, first, second
}

func canonicalReplayTxNodes(t *testing.T, source *ledger.Ledger) ([]message.LedgerNode, []message.LedgerNode) {
	t.Helper()
	txMap, err := source.TxMapSnapshot()
	require.NoError(t, err)
	require.NotNil(t, txMap)
	wire, err := txMap.WalkWireNodes()
	require.NoError(t, err)
	require.Greater(t, len(wire), 1, "the signed payment must leave a hedgeable transaction frontier")

	base := []message.LedgerNode{
		{NodeData: header.AddRaw(source.Header(), false)},
		{NodeData: []byte{1}},
		{NodeData: wire[0].Data},
	}
	txNodes := make([]message.LedgerNode, 0, len(wire)-1)
	for _, node := range wire[1:] {
		txNodes = append(txNodes, message.LedgerNode{NodeID: node.NodeID, NodeData: node.Data})
	}
	return base, txNodes
}

func feedCanonicalReplayData(t *testing.T, router *Router, peerID uint64, source *ledger.Ledger, infoType message.LedgerInfoType, nodes []message.LedgerNode) {
	t.Helper()
	hash := source.Hash()
	router.handleInboundMessage(&peermanagement.InboundMessage{
		PeerID: peermanagement.PeerID(peerID),
		Type:   message.TypeLedgerData,
		Payload: encodePayload(t, &message.LedgerData{
			LedgerHash: hash[:],
			LedgerSeq:  source.Sequence(),
			InfoType:   infoType,
			Nodes:      nodes,
		}),
	})
}

func replayPeerSnapshot(t *testing.T, acquisition *inbound.Ledger, peerID uint64) inbound.RequestPeerSnapshot {
	t.Helper()
	for _, peer := range acquisition.Snapshot().RequestPeerSnapshots {
		if peer.PeerID == peerID {
			return peer
		}
	}
	t.Fatalf("peer %d is absent from request snapshots: %+v", peerID, acquisition.Snapshot().RequestPeerSnapshots)
	return inbound.RequestPeerSnapshot{}
}

func TestReplayHeadTailRecoversCanonicalPaymentSuccessor(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		router, adaptor, sender, svc := makeRouter(t)
		t.Cleanup(svc.Stop)
		parent := svc.GetClosedLedger()
		require.NotNil(t, parent)
		_, first, second := buildCanonicalReplayPaymentSuccessors(t, parent)
		engine := &mockEngine{switchResult: consensus.LedgerSwitchAccepted}
		engine.switchHook = func(id consensus.LedgerID) {
			selected, err := adaptor.GetLedger(id)
			require.NoError(t, err)
			require.NoError(t, adaptor.OnLedgerSwitched(selected))
		}
		router.engine = engine

		coordinator := router.catchupReplay
		coordinator.engine = engine
		coordinator.recordSeqHash(first.Sequence(), first.Hash(), parent.Hash(), true)
		coordinator.recordSeqHash(second.Sequence(), second.Hash(), first.Hash(), true)
		sender.mu.Lock()
		sender.peerSupportsReplay = false
		sender.mu.Unlock()
		trackCatchupPeer(router, 7, second.Sequence(), second.Hash())
		trackCatchupPeer(router, 8, second.Sequence(), second.Hash())

		runCtx, cancel := context.WithCancel(t.Context())
		go router.Run(runCtx)
		synctest.Wait()
		t.Cleanup(func() {
			cancel()
			router.StopAcquisitions()
		})

		require.True(t, coordinator.tryArmStandardReplayPipeline(svc, parent, second.Sequence(), second.Hash(), 7))
		head := coordinator.fetchTracker.Find(first.Hash())
		tail := coordinator.fetchTracker.Find(second.Hash())
		require.NotNil(t, head)
		require.NotNil(t, tail)
		require.True(t, head.TransactionOnly())
		require.True(t, tail.TransactionOnly())

		headBase, headTxNodes := canonicalReplayTxNodes(t, first)
		tailBase, tailTxNodes := canonicalReplayTxNodes(t, second)
		feedCanonicalReplayData(t, router, 7, first, message.LedgerInfoBase, headBase)
		synctest.Wait()
		headSnapshot := head.Snapshot()
		require.True(t, headSnapshot.HaveHeader)
		require.True(t, headSnapshot.HaveState)
		require.False(t, headSnapshot.HaveTransactions)
		require.Zero(t, headSnapshot.StateReceived)
		require.Zero(t, headSnapshot.StateNodesLoaded)
		require.Empty(t, headSnapshot.NeededState)

		feedCanonicalReplayData(t, router, 7, second, message.LedgerInfoBase, tailBase)
		synctest.Wait()
		feedCanonicalReplayData(t, router, 7, second, message.LedgerInfoTxNode, tailTxNodes)
		synctest.Wait()
		require.True(t, tail.IsComplete(), "successor transaction-only acquisition did not complete")
		synctest.Wait()

		coordinator.acquisitionMu.Lock()
		headEntry := coordinator.standardReplay.entries[first.Sequence()]
		tailEntry := coordinator.standardReplay.entries[second.Sequence()]
		var headReady, tailReady time.Time
		if headEntry != nil {
			headReady = headEntry.readyAt
		}
		if tailEntry != nil {
			tailReady = tailEntry.readyAt
		}
		coordinator.acquisitionMu.Unlock()
		require.NotNil(t, headEntry)
		require.NotNil(t, tailEntry)
		require.Zero(t, headReady)
		require.False(t, tailReady.IsZero(), "successor completion must be retained behind the blocked head")

		require.True(t, head.AddPeer(8))
		time.Sleep(time.Second)
		coordinator.retryReplayHeadTail(time.Now())
		synctest.Wait()
		headSnapshot = head.Snapshot()
		require.Positive(t, headSnapshot.Hedges)
		require.Equal(t, uint64(0), headSnapshot.StateReceived)
		require.Equal(t, uint64(0), headSnapshot.StateNodesLoaded)
		require.Empty(t, headSnapshot.NeededState)
		require.Zero(t, replayPeerSnapshot(t, head, 7).Hedges)
		require.Positive(t, replayPeerSnapshot(t, head, 8).Hedges)

		feedCanonicalReplayData(t, router, 8, first, message.LedgerInfoTxNode, headTxNodes)
		synctest.Wait()
		require.True(t, head.IsComplete(), "hedged head transaction-only acquisition did not complete")
		synctest.Wait()
		coordinator.drainStandardReplayPipeline()
		synctest.Wait()

		got := svc.GetClosedLedger()
		require.NotNil(t, got)
		expectedHeader := second.Header()
		actualHeader := got.Header()
		expectedHeader.ParentCloseTime = expectedHeader.ParentCloseTime.UTC()
		expectedHeader.CloseTime = expectedHeader.CloseTime.UTC()
		actualHeader.ParentCloseTime = actualHeader.ParentCloseTime.UTC()
		actualHeader.CloseTime = actualHeader.CloseTime.UTC()
		require.Equal(t, expectedHeader, actualHeader)
		require.Equal(t, second.Hash(), got.Hash())
		require.Zero(t, tail.Snapshot().StateReceived)
		require.Zero(t, tail.Snapshot().StateNodesLoaded)
		require.Empty(t, tail.Snapshot().NeededState)

		cancel()
		synctest.Wait()
		router.StopAcquisitions()
		synctest.Wait()
		require.Empty(t, coordinator.fetchTracker.Active())
	})
}
