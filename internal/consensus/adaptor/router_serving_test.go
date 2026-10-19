package adaptor

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/ledger/genesis"
	"github.com/LeJamon/go-xrpl/internal/ledger/service"
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/LeJamon/go-xrpl/shamap/backend"
	"github.com/stretchr/testify/require"
)

type servingPeerView struct {
	mu        sync.RWMutex
	connected map[peermanagement.PeerID]bool
}

type blockingServingFamily struct {
	shamap.Family
	entered     chan struct{}
	exited      chan struct{}
	release     chan struct{}
	block       atomic.Bool
	enteredOnce sync.Once
	exitedOnce  sync.Once
}

func (f *blockingServingFamily) Fetch(ctx context.Context, hash [32]byte) ([]byte, error) {
	if f.block.Load() {
		f.enteredOnce.Do(func() { close(f.entered) })
		defer f.exitedOnce.Do(func() { close(f.exited) })
		select {
		case <-f.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return f.Family.Fetch(ctx, hash)
}

type servingFrameRecorder struct {
	noopSender
	mu      sync.Mutex
	frames  []sentFrame
	sent    chan sentFrame
	sendErr error
}

func (s *servingFrameRecorder) SendToPeer(peerID uint64, frame []byte) error {
	recorded := sentFrame{peerID: peerID, frame: append([]byte(nil), frame...)}
	s.mu.Lock()
	s.frames = append(s.frames, recorded)
	s.mu.Unlock()
	if s.sent != nil {
		s.sent <- recorded
	}
	return s.sendErr
}

func (s *servingFrameRecorder) frameCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.frames)
}

func newServingPeerView(peerIDs ...peermanagement.PeerID) *servingPeerView {
	connected := make(map[peermanagement.PeerID]bool, len(peerIDs))
	for _, peerID := range peerIDs {
		connected[peerID] = true
	}
	return &servingPeerView{connected: connected}
}

func (v *servingPeerView) IsPeerConnected(peerID peermanagement.PeerID) bool {
	v.mu.RLock()
	connected := v.connected[peerID]
	v.mu.RUnlock()
	return connected
}

func installServingPools(
	t *testing.T,
	router *Router,
	ctx context.Context,
	pools [ledgerServeClassCount]*peermanagement.ServeScheduler,
	peers *servingPeerView,
) <-chan struct{} {
	t.Helper()
	router.lifecycleMu.Lock()
	router.lifecycleState = routerLifecycleRunning
	router.lifecycleCtx = ctx
	router.servePools = pools
	router.peerSessions = peers
	router.lifecycleMu.Unlock()

	done := make(chan struct{}, len(pools))
	for _, pool := range pools {
		go func() {
			_ = pool.Run(ctx)
			done <- struct{}{}
		}()
	}
	return done
}

func newServingTestRouter(
	t *testing.T,
	workers int,
	peerIDs ...peermanagement.PeerID,
) (*Router, context.Context, [ledgerServeClassCount]*peermanagement.ServeScheduler) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	pools := newLedgerServePools(ctx, workers)
	router := newTestRouter(&mockEngine{}, newTestAdaptor(t), nil)
	peers := newServingPeerView(peerIDs...)
	done := installServingPools(t, router, ctx, pools, peers)
	t.Cleanup(func() {
		cancel()
		for range pools {
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Errorf("serve scheduler did not stop")
			}
		}
	})
	return router, ctx, pools
}

func newRealServingRouter(
	t *testing.T,
	peerIDs ...peermanagement.PeerID,
) (*Router, context.Context, *blockingServingFamily, *servingFrameRecorder, *service.Service) {
	t.Helper()
	baseFamily := backend.NewMemory()
	svc, err := service.New(service.Config{
		Standalone:    true,
		GenesisConfig: genesis.DefaultConfig(),
		SHAMapFamily:  baseFamily,
	})
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)

	closed := svc.GetClosedLedger()
	require.NotNil(t, closed)
	snapshot, err := closed.StateMapSnapshot()
	require.NoError(t, err)
	token := snapshot.PrepareCleanResidencyRelease()
	require.NotNil(t, token, "state map must have clean descendants for provider blocking")
	require.True(t, snapshot.AcknowledgeCleanResidencyRelease(token))

	family := &blockingServingFamily{
		Family:  baseFamily,
		entered: make(chan struct{}),
		exited:  make(chan struct{}),
		release: make(chan struct{}),
	}
	closed.SetStateMapFamily(family)

	sender := &servingFrameRecorder{sent: make(chan sentFrame, serveQueueDepth+serveWorkerCount)}
	identity, err := NewValidatorIdentity("snoPBrXtMeMyMHUVTgbuqAfg1SUTb")
	require.NoError(t, err)
	adaptor := New(Config{
		LedgerService: svc,
		Sender:        sender,
		Identity:      identity,
		Validators:    []consensus.NodeID{identity.NodeID},
	})
	router := newTestRouter(&mockEngine{}, adaptor, nil)
	ctx := context.Background()
	_, ok := router.startLifecycle(ctx)
	require.True(t, ok)
	peers := newServingPeerView(peerIDs...)
	router.lifecycleMu.Lock()
	router.peerSessions = peers
	router.lifecycleMu.Unlock()
	t.Cleanup(router.stopLifecycle)
	return router, ctx, family, sender, svc
}

func realLedgerRequest(t *testing.T, peerID peermanagement.PeerID, infoType message.LedgerInfoType, l interface {
	Hash() [32]byte
	Sequence() uint32
}) *peermanagement.InboundMessage {
	t.Helper()
	hash := l.Hash()
	req := &message.GetLedger{
		InfoType:   infoType,
		LedgerHash: hash[:],
		LedgerSeq:  l.Sequence(),
	}
	if infoType != message.LedgerInfoBase {
		req.NodeIDs = [][]byte{rootNodeID()}
	}
	return &peermanagement.InboundMessage{
		PeerID:  peerID,
		Type:    message.TypeGetLedger,
		Payload: encodePayload(t, req),
	}
}

func realTxSetRequest(t *testing.T, peerID peermanagement.PeerID, txSetID [32]byte) *peermanagement.InboundMessage {
	t.Helper()
	req := &message.GetLedger{
		InfoType:   message.LedgerInfoTsCandidate,
		LedgerHash: txSetID[:],
		NodeIDs:    [][]byte{rootNodeID()},
	}
	return &peermanagement.InboundMessage{
		PeerID:  peerID,
		Type:    message.TypeGetLedger,
		Payload: encodePayload(t, req),
	}
}

func servingRequest(t *testing.T, peerID peermanagement.PeerID, infoType message.LedgerInfoType) *peermanagement.InboundMessage {
	t.Helper()
	req := &message.GetLedger{
		InfoType:   infoType,
		LedgerHash: bytes.Repeat([]byte{0x42}, 32),
	}
	if infoType != message.LedgerInfoBase {
		req.NodeIDs = [][]byte{rootNodeID()}
	}
	return &peermanagement.InboundMessage{
		PeerID:  peerID,
		Type:    message.TypeGetLedger,
		Payload: encodePayload(t, req),
	}
}

func awaitServing(t *testing.T, ready <-chan struct{}, description string) {
	t.Helper()
	select {
	case <-ready:
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %s", description)
	}
}

func awaitServingStats(t *testing.T, pool *peermanagement.ServeScheduler, predicate func(peermanagement.ServeSchedulerStats) bool, description string) peermanagement.ServeSchedulerStats {
	t.Helper()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		stats := pool.Snapshot()
		if predicate(stats) {
			return stats
		}
		select {
		case <-deadline.C:
			t.Fatalf("timed out waiting for %s: %+v", description, stats)
		case <-tick.C:
		}
	}
}

func blockServingTasks(
	t *testing.T,
	ctx context.Context,
	pool *peermanagement.ServeScheduler,
	peerIDs ...peermanagement.PeerID,
) chan struct{} {
	t.Helper()
	release := make(chan struct{})
	started := make(chan struct{}, len(peerIDs))
	for _, peerID := range peerIDs {
		require.True(t, pool.SubmitOwnedBytes(ctx, peerID, 1, func(taskCtx context.Context) {
			select {
			case started <- struct{}{}:
			case <-taskCtx.Done():
			}
			select {
			case <-release:
			case <-taskCtx.Done():
			}
		}, nil), "blocking serve task for peer %d was rejected", peerID)
	}
	for range peerIDs {
		awaitServing(t, started, "blocked serve task")
	}
	return release
}

func saturateServingQueue(t *testing.T, ctx context.Context, pool *peermanagement.ServeScheduler, firstPeer peermanagement.PeerID) int {
	t.Helper()
	accepted := 0
	for i := 0; i < serveQueueDepth; i++ {
		peerID := firstPeer + peermanagement.PeerID(i/ledgerServePerPeerQueue)
		if !pool.SubmitOwnedBytes(ctx, peerID, 1, func(context.Context) {}, nil) {
			break
		}
		accepted++
	}
	require.Positive(t, accepted, "serve queue accepted no work")
	require.False(t, pool.SubmitOwnedBytes(ctx, firstPeer+10_000, 1, func(context.Context) {}, nil), "saturated serve queue accepted more work")
	return accepted
}

func TestRouterServingClassesRemainIndependent(t *testing.T) {
	t.Run("txset progresses while ordinary is saturated", func(t *testing.T) {
		router, ctx, pools := newServingTestRouter(t, 4, 1, 2, 3, 99)
		release := blockServingTasks(t, ctx, pools[ledgerServeOrdinary], 1, 2, 3)
		defer close(release)
		queued := saturateServingQueue(t, ctx, pools[ledgerServeOrdinary], 100)
		ordinaryBefore := pools[ledgerServeOrdinary].Snapshot()
		require.GreaterOrEqual(t, ordinaryBefore.Queued, queued)

		msg := servingRequest(t, 99, message.LedgerInfoTsCandidate)
		require.True(t, router.handleMessage(msg))
		awaitServingStats(t, pools[ledgerServeTxSet], func(stats peermanagement.ServeSchedulerStats) bool {
			return stats.Completed >= 1
		}, "tx-set request completion")
		require.GreaterOrEqual(t, pools[ledgerServeOrdinary].Snapshot().Queued, ordinaryBefore.Queued)
	})

	t.Run("ordinary progresses while txset is saturated", func(t *testing.T) {
		router, ctx, pools := newServingTestRouter(t, 4, 1, 99)
		release := blockServingTasks(t, ctx, pools[ledgerServeTxSet], 1)
		defer close(release)
		queued := saturateServingQueue(t, ctx, pools[ledgerServeTxSet], 200)
		txSetBefore := pools[ledgerServeTxSet].Snapshot()
		require.GreaterOrEqual(t, txSetBefore.Queued, queued)

		msg := servingRequest(t, 99, message.LedgerInfoBase)
		require.True(t, router.handleMessage(msg))
		awaitServingStats(t, pools[ledgerServeOrdinary], func(stats peermanagement.ServeSchedulerStats) bool {
			return stats.Completed >= 1
		}, "ordinary ledger request completion")
		require.GreaterOrEqual(t, pools[ledgerServeTxSet].Snapshot().Queued, txSetBefore.Queued)
	})
}

func TestRouterServingRealLedgerWorkRemainsClassIndependent(t *testing.T) {
	t.Run("cached txset progresses while state provider is blocked", func(t *testing.T) {
		peerIDs := make([]peermanagement.PeerID, serveWorkerCount+1)
		for i := range peerIDs {
			peerIDs[i] = peermanagement.PeerID(i + 1)
		}
		router, _, family, sender, svc := newRealServingRouter(t, peerIDs...)
		closed := svc.GetClosedLedger()
		require.NotNil(t, closed)

		family.block.Store(true)
		ordinary := router.servePools[ledgerServeOrdinary]
		for i := 0; i < ordinary.Workers(); i++ {
			require.True(t, router.handleMessage(realLedgerRequest(t, peerIDs[i], message.LedgerInfoAsNode, closed)))
		}
		awaitServing(t, family.entered, "state provider entry")
		awaitServingStats(t, ordinary, func(stats peermanagement.ServeSchedulerStats) bool {
			return stats.Active == ordinary.Workers()
		}, "all ordinary workers busy")

		ts, err := router.adaptor.BuildTxSet([][]byte{
			bytes.Repeat([]byte{0x11}, 16),
			bytes.Repeat([]byte{0x55}, 16),
		})
		require.NoError(t, err)
		txPeer := peerIDs[len(peerIDs)-1]
		require.True(t, router.handleMessage(realTxSetRequest(t, txPeer, [32]byte(ts.ID()))))

		select {
		case frame := <-sender.sent:
			require.Equal(t, uint64(txPeer), frame.peerID, "tx-set class should run while state class is blocked")
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for cached tx-set response")
		}
		require.Equal(t, 1, sender.frameCount(), "ordinary response sent before its provider was released")

		close(family.release)
		select {
		case frame := <-sender.sent:
			require.NotEqual(t, uint64(txPeer), frame.peerID, "ordinary class should send after provider release")
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for ordinary ledger response")
		}
		awaitServing(t, family.exited, "state provider completion")
	})

	t.Run("ordinary ledger response progresses while txset provider is blocked", func(t *testing.T) {
		router, _, _, sender, svc := newRealServingRouter(t, 1, 2)
		closed := svc.GetClosedLedger()
		require.NotNil(t, closed)

		ts, err := router.adaptor.BuildTxSet([][]byte{
			bytes.Repeat([]byte{0x21}, 16),
			bytes.Repeat([]byte{0x65}, 16),
		})
		require.NoError(t, err)
		txSetImpl, ok := ts.(*txSetImpl)
		require.True(t, ok, "test adaptor must build its native tx-set implementation")
		txFamily := &blockingServingFamily{
			Family:  backend.NewMemory(),
			entered: make(chan struct{}),
			exited:  make(chan struct{}),
			release: make(chan struct{}),
		}
		txMap := txSetImpl.shamap()
		require.NoError(t, txMap.StoreDirty(func(entries []shamap.FlushEntry) error {
			return txFamily.Family.StoreBatch(context.Background(), entries)
		}))
		txMap.SetFamily(txFamily)
		token := txMap.PrepareCleanResidencyRelease()
		require.NotNil(t, token, "tx-set map must have clean descendants for provider blocking")
		require.True(t, txMap.AcknowledgeCleanResidencyRelease(token))
		txFamily.block.Store(true)

		require.True(t, router.handleMessage(realTxSetRequest(t, 2, [32]byte(ts.ID()))))
		awaitServing(t, txFamily.entered, "tx-set provider entry")

		require.True(t, router.handleMessage(realLedgerRequest(t, 1, message.LedgerInfoBase, closed)))
		select {
		case frame := <-sender.sent:
			require.Equal(t, uint64(1), frame.peerID, "ordinary class should run while tx-set class is blocked")
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for ordinary ledger response")
		}

		router.HandlePeerDisconnect(2)
		awaitServing(t, txFamily.exited, "tx-set provider cancellation")
		awaitServingStats(t, router.servePools[ledgerServeTxSet], func(stats peermanagement.ServeSchedulerStats) bool {
			return stats.Active == 0 && stats.Queued == 0 && stats.RetainedBytes == 0
		}, "tx-set cancellation accounting")
		require.Equal(t, 1, sender.frameCount(), "cancelled tx-set handler must not send")
	})
}

func TestRouterServingDisconnectCancelsRealHandlerBeforeSend(t *testing.T) {
	router, _, family, sender, svc := newRealServingRouter(t, 7)
	closed := svc.GetClosedLedger()
	require.NotNil(t, closed)
	family.block.Store(true)

	require.True(t, router.handleMessage(realLedgerRequest(t, 7, message.LedgerInfoAsNode, closed)))
	awaitServing(t, family.entered, "running state provider")
	require.True(t, router.handleMessage(realLedgerRequest(t, 7, message.LedgerInfoAsNode, closed)))
	require.Equal(t, 1, router.servePools[ledgerServeOrdinary].Snapshot().Queued)
	router.HandlePeerDisconnect(7)
	awaitServing(t, family.exited, "disconnected state provider")
	awaitServingStats(t, router.servePools[ledgerServeOrdinary], func(stats peermanagement.ServeSchedulerStats) bool {
		return stats.Active == 0 && stats.Queued == 0 && stats.RetainedBytes == 0
	}, "disconnect scheduler accounting")
	require.Zero(t, sender.frameCount(), "disconnected handler sent a ledger response")
	require.EqualValues(t, 2, router.servePools[ledgerServeOrdinary].Snapshot().Canceled)
}

func TestRouterServingLifecycleShutdownCancelsRealHandlerBeforeSend(t *testing.T) {
	router, _, family, sender, svc := newRealServingRouter(t, 8)
	closed := svc.GetClosedLedger()
	require.NotNil(t, closed)
	family.block.Store(true)

	require.True(t, router.handleMessage(realLedgerRequest(t, 8, message.LedgerInfoAsNode, closed)))
	awaitServing(t, family.entered, "running state provider")
	require.True(t, router.handleMessage(realLedgerRequest(t, 8, message.LedgerInfoAsNode, closed)))
	require.Equal(t, 1, router.servePools[ledgerServeOrdinary].Snapshot().Queued)
	router.stopLifecycle()
	awaitServing(t, family.exited, "shutdown state provider")
	require.Zero(t, sender.frameCount(), "shutdown handler sent a ledger response")
	require.EqualValues(t, 2, router.servePools[ledgerServeOrdinary].Snapshot().Canceled)
	for class, pool := range router.servePools {
		stats := pool.Snapshot()
		require.Zero(t, stats.Active, "class %d retained an active handler after shutdown", class)
		require.Zero(t, stats.Queued, "class %d retained queued work after shutdown", class)
		require.Zero(t, stats.RetainedBytes, "class %d retained inbound bytes after shutdown", class)
	}
}

func TestRouterServingSendFailureReleasesAdmissionBytes(t *testing.T) {
	router, _, _, sender, svc := newRealServingRouter(t, 9)
	sender.sendErr = errors.New("test send failure")
	closed := svc.GetClosedLedger()
	require.NotNil(t, closed)

	require.True(t, router.handleMessage(realLedgerRequest(t, 9, message.LedgerInfoBase, closed)))
	awaitServingStats(t, router.servePools[ledgerServeOrdinary], func(stats peermanagement.ServeSchedulerStats) bool {
		return stats.Completed >= 1 && stats.Active == 0 && stats.Queued == 0 && stats.RetainedBytes == 0
	}, "send-failure completion and ownership release")
	require.Equal(t, 1, sender.frameCount(), "send failure should still exercise the peer sender")
}

func TestRouterServingRejectsAtClassCapacity(t *testing.T) {
	router, _, _, sender, svc := newRealServingRouter(t, 10_000)
	closed := svc.GetClosedLedger()
	require.NotNil(t, closed)
	p := router.servePools[ledgerServeOrdinary]
	workerPeers := make([]peermanagement.PeerID, p.Workers())
	for i := range workerPeers {
		workerPeers[i] = peermanagement.PeerID(20_000 + i)
	}
	block := blockServingTasks(t, context.Background(), p, workerPeers...)
	defer close(block)
	require.Equal(t, serveQueueDepth*3/4, saturateServingQueue(t, context.Background(), p, 30_000))

	droppedBefore := router.DroppedServeJobs()
	require.True(t, router.handleMessage(realLedgerRequest(t, 10_000, message.LedgerInfoBase, closed)))
	require.Greater(t, router.DroppedServeJobs(), droppedBefore, "router did not account for rejected serving work")
	require.Zero(t, sender.frameCount(), "rejected serving request reached the sender")

	router.stopLifecycle()
	stats := p.Snapshot()
	require.Zero(t, stats.Active)
	require.Zero(t, stats.Queued)
	require.Zero(t, stats.RetainedBytes, "scheduler retained rejected request bytes after shutdown")
}

func TestRouterServingPerPeerCapPreservesOtherPeers(t *testing.T) {
	_, ctx, pools := newServingTestRouter(t, 4, 17, 23)
	pool := pools[ledgerServeOrdinary]
	release := blockServingTasks(t, ctx, pool, 17)
	defer close(release)

	var overCapRan atomic.Int32
	accepted := 0
	for range ledgerServePerPeerQueue {
		if pool.SubmitOwnedBytes(ctx, 17, 1, func(context.Context) {
			overCapRan.Add(1)
		}, nil) {
			accepted++
		}
	}
	require.Positive(t, accepted)
	require.False(t, pool.SubmitOwnedBytes(ctx, 17, 1, func(context.Context) {}, nil), "per-peer queue accepted over-limit work")

	otherPeerProgressed := make(chan struct{})
	require.True(t, pool.SubmitOwnedBytes(ctx, 23, 1, func(context.Context) {
		close(otherPeerProgressed)
	}, nil))
	awaitServing(t, otherPeerProgressed, "other peer admission")

	pool.CancelPeer(17)
	awaitServingStats(t, pool, func(stats peermanagement.ServeSchedulerStats) bool {
		return stats.Active == 0 && stats.Queued == 0 && stats.RetainedBytes == 0
	}, "over-cap peer cancellation")
	require.Zero(t, overCapRan.Load(), "over-cap peer work ran before cancellation")
}

func TestRouterServingByteBounds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pools := newLedgerServePools(ctx, 4)
	for class, pool := range pools {
		maxBytes := int64(ledgerServeMaxBytes)
		if ledgerServeClass(class) == ledgerServeOrdinary {
			maxBytes = maxBytes * 3 / 4
		} else {
			maxBytes /= 4
		}
		perPeerBytes := maxBytes / 4
		for peerID := peermanagement.PeerID(1); peerID <= 3; peerID++ {
			require.True(t, pool.SubmitOwnedBytes(ctx, peerID, perPeerBytes, func(context.Context) {}, nil))
		}
		require.True(t, pool.SubmitOwnedBytes(ctx, 4, perPeerBytes-1, func(context.Context) {}, nil))
		stats := pool.Snapshot()
		require.Equal(t, maxBytes-1, stats.RetainedBytes)
		require.Equal(t, 4, stats.Queued)
		require.False(t, pool.SubmitOwnedBytes(ctx, 5, 2, func(context.Context) {}, nil), "class %d exceeded its byte bound", class)
		require.LessOrEqual(t, pool.Snapshot().RetainedBytes, maxBytes)
		for peerID := peermanagement.PeerID(1); peerID <= 4; peerID++ {
			pool.CancelPeer(peerID)
		}
		require.Zero(t, pool.Snapshot().RetainedBytes)

		require.True(t, pool.SubmitOwnedBytes(ctx, 1, perPeerBytes, func(context.Context) {}, nil))
		require.False(t, pool.SubmitOwnedBytes(ctx, 1, 1, func(context.Context) {}, nil), "peer %d exceeded its byte bound", 1)
		require.True(t, pool.SubmitOwnedBytes(ctx, 2, 1, func(context.Context) {}, nil), "peer %d was blocked by peer %d byte usage", 2, 1)
		require.LessOrEqual(t, pool.Snapshot().RetainedBytes, maxBytes)
		pool.CancelPeer(1)
		pool.CancelPeer(2)
		require.Zero(t, pool.Snapshot().RetainedBytes)
	}
}

func TestRouterServingRejectsInvalidTxSetBeforePriorityAdmission(t *testing.T) {
	router, _, _ := newServingTestRouter(t, 4, 7)
	msg := &peermanagement.InboundMessage{
		PeerID:  7,
		Type:    message.TypeGetLedger,
		Payload: encodePayload(t, &message.GetLedger{InfoType: message.LedgerInfoTsCandidate}),
	}
	require.False(t, router.handleMessage(msg))
	require.NoError(t, msg.Close())

	metrics := router.LedgerServeMetrics()
	require.Zero(t, metrics.Ledger.Queued)
	require.Zero(t, metrics.Ledger.Dropped)
	require.Zero(t, metrics.TxSet.Queued)
	require.Zero(t, metrics.TxSet.Dropped)
	require.Zero(t, router.DroppedServeJobs())
}
