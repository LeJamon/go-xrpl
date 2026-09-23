package adaptor

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/stretchr/testify/require"
)

type blockedTxSetLearningLookup struct {
	entered chan struct{}
	release <-chan struct{}
	once    sync.Once
}

type txSetLearningCaptureEngine struct {
	*mockEngine
	blobs [][]byte
}

func (e *txSetLearningCaptureEngine) OnTxSet(id consensus.TxSetID, blobs [][]byte) error {
	for _, blob := range blobs {
		e.blobs = append(e.blobs, append([]byte(nil), blob...))
	}
	return e.mockEngine.OnTxSet(id, blobs)
}

func (l *blockedTxSetLearningLookup) OpenLedgerHasTx([32]byte) (bool, error) {
	l.once.Do(func() { close(l.entered) })
	<-l.release
	return false, nil
}

func txSetLearningTestData(t *testing.T) (*message.LedgerData, consensus.TxID, []byte) {
	t.Helper()
	env := jtx.NewTestEnv(t)
	env.SetVerifySignatures(true)
	blob, id := signedPaymentFrame(t, env, 1)
	sm := shamap.New(shamap.TypeTransaction)
	require.NoError(t, sm.PutWithNodeType([32]byte(id), blob, shamap.NodeTypeTransactionNoMeta))
	hash, err := sm.Hash()
	require.NoError(t, err)
	nodes, err := sm.WalkWireNodes()
	require.NoError(t, err)
	data := &message.LedgerData{LedgerHash: hash[:], InfoType: message.LedgerInfoTsCandidate}
	for _, n := range nodes {
		data.Nodes = append(data.Nodes, message.LedgerNode{NodeID: n.NodeID, NodeData: n.Data})
	}
	return data, id, blob
}

func TestTxSetLearningDoesNotBlockConsensusDelivery(t *testing.T) {
	a := newTestAdaptor(t)
	release := make(chan struct{})
	lookup := &blockedTxSetLearningLookup{entered: make(chan struct{}), release: release}
	a.txLookup = lookup
	engine := &mockEngine{}
	inbox := make(chan *peermanagement.InboundMessage, 1)
	r := newTestRouter(engine, a, inbox)
	data, id, _ := txSetLearningTestData(t)
	var setID consensus.TxSetID
	copy(setID[:], data.LedgerHash)
	r.MarkTxSetStillNeeded(setID)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.Run(ctx); close(done) }()
	t.Cleanup(func() {
		close(release)
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("router did not join its transaction workers")
		}
	})
	<-r.lifecycleReadyChannel()
	inbox <- &peermanagement.InboundMessage{PeerID: 5, Type: message.TypeLedgerData, Payload: encodePayload(t, data)}
	select {
	case <-lookup.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("transaction learning did not start")
	}
	require.Eventually(t, func() bool {
		engine.mu.Lock()
		defer engine.mu.Unlock()
		return len(engine.txSets) == 1 && engine.txSets[0] == setID
	}, time.Second, time.Millisecond, "consensus must receive the complete set while open-ledger access is blocked")
	exists, err := a.ledgerService.OpenLedgerHasTx([32]byte(id))
	require.NoError(t, err)
	require.False(t, exists, "delivery must not wait for open-ledger admission")
}

func TestTxSetLearningQueueCopiesBlob(t *testing.T) {
	r := newTestRouter(&mockEngine{}, newTestAdaptor(t), nil)
	r.lifecycleState = routerLifecycleRunning
	r.txSetLearnJobs = make(chan txSetLearnJob, 1)
	_, id, blob := txSetLearningTestData(t)
	wire := txLeafWire(blob)
	r.learnTxFromLeaf(7, wire)
	require.Len(t, r.txSetLearnJobs, 1)
	wire[0] ^= 0xff // the overlay frame may be released/reused immediately
	job := <-r.txSetLearnJobs
	require.Equal(t, uint64(7), job.peer)
	require.Equal(t, [32]byte(id), job.id)
	require.Equal(t, blob, job.blob)
}

func TestTxSetLearningSaturationPreservesConsensusSet(t *testing.T) {
	a := newTestAdaptor(t)
	engine := &txSetLearningCaptureEngine{mockEngine: &mockEngine{}}
	r := newTestRouter(engine, a, nil)
	r.lifecycleState = routerLifecycleRunning
	r.txSetLearnJobs = make(chan txSetLearnJob, 1)
	r.txSetLearnJobs <- txSetLearnJob{}
	data, _, blob := txSetLearningTestData(t)
	var setID consensus.TxSetID
	copy(setID[:], data.LedgerHash)
	r.MarkTxSetStillNeeded(setID)
	r.handleTxSetData(data, 7)
	require.Equal(t, uint64(1), r.DroppedTxJobs())
	require.Len(t, r.txSetLearnJobs, 1, "learning must remain bounded")
	require.Equal(t, []consensus.TxSetID{setID}, engine.txSets)
	require.Equal(t, [][]byte{blob}, engine.blobs, "shedding optional learning must not discard consensus data")
}

func TestTxSetLearningRejectedAfterShutdown(t *testing.T) {
	a := newTestAdaptor(t)
	r := newTestRouter(&mockEngine{}, a, nil)
	_, ok := r.startLifecycle(context.Background())
	require.True(t, ok)
	r.stopLifecycle()
	require.Nil(t, r.txSetLearnJobs)
	_, id, blob := txSetLearningTestData(t)
	r.learnTxFromLeaf(7, txLeafWire(blob))
	require.Equal(t, uint64(1), r.DroppedTxJobs())
	require.False(t, adaptorHasTx(t, a, id), "shutdown must not fall back to inline admission")
}
