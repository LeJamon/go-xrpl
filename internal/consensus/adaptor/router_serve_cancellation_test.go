package adaptor

import (
	"context"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger/genesis"
	ledgerservice "github.com/LeJamon/go-xrpl/internal/ledger/service"
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/shamap"
	shamapbackend "github.com/LeJamon/go-xrpl/shamap/backend"
	"github.com/stretchr/testify/require"
)

// routerServeFetchGate makes the state tree lazy after the service has started,
// then blocks the first backing-store read. The map is populated by service
// startup before blocking is enabled, so a request reaching the gate proves it
// traversed the same lazy Family path used by production serving.
type routerServeFetchGate struct {
	inner *shamapbackend.NodeStore

	block          atomic.Bool
	noncancellable bool
	fetches        atomic.Int32
	stores         atomic.Int32

	started     chan struct{}
	canceled    chan struct{}
	release     chan struct{}
	startOnce   sync.Once
	cancelOnce  sync.Once
	releaseOnce sync.Once
}

func newRouterServeFetchGate() *routerServeFetchGate {
	return &routerServeFetchGate{
		inner:    shamapbackend.NewMemory(),
		started:  make(chan struct{}),
		canceled: make(chan struct{}),
		release:  make(chan struct{}),
	}
}

func (f *routerServeFetchGate) Fetch(ctx context.Context, hash [32]byte) ([]byte, error) {
	ctx, err := f.waitFetch(ctx)
	if err != nil {
		return nil, err
	}
	return f.inner.Fetch(ctx, hash)
}

func (f *routerServeFetchGate) FetchBounded(ctx context.Context, hash [32]byte, maxBytes int) ([]byte, bool, error) {
	ctx, err := f.waitFetch(ctx)
	if err != nil {
		return nil, false, err
	}
	return f.inner.FetchBounded(ctx, hash, maxBytes)
}

func (f *routerServeFetchGate) waitFetch(ctx context.Context) (context.Context, error) {
	f.fetches.Add(1)
	if f.block.CompareAndSwap(true, false) {
		f.startOnce.Do(func() { close(f.started) })
		select {
		case <-f.release:
		case <-ctx.Done():
			f.cancelOnce.Do(func() { close(f.canceled) })
			if !f.noncancellable {
				return nil, ctx.Err()
			}
			<-f.release
		}
	}
	if f.noncancellable {
		return context.Background(), nil
	}
	return ctx, nil
}

func (f *routerServeFetchGate) StoreBatch(ctx context.Context, entries []shamap.FlushEntry) error {
	f.stores.Add(1)
	return f.inner.StoreBatch(ctx, entries)
}

func (f *routerServeFetchGate) releaseFetch() {
	f.releaseOnce.Do(func() { close(f.release) })
}

func (f *routerServeFetchGate) armFetch() {
	f.block.Store(true)
}

type routerServeFixture struct {
	router     *Router
	sender     *relayRecorder
	family     *routerServeFetchGate
	ledgerHash [32]byte
}

type routerServeMemoryBudget struct {
	mu       sync.Mutex
	capacity uint64
	used     uint64
	changed  chan struct{}
}

func newRouterServeMemoryBudget(capacity uint64) *routerServeMemoryBudget {
	return &routerServeMemoryBudget{
		capacity: capacity,
		changed:  make(chan struct{}),
	}
}

func (b *routerServeMemoryBudget) TryReserve(size uint64) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if size > b.capacity-b.used {
		return false
	}
	b.used += size
	return true
}

func (b *routerServeMemoryBudget) Release(size uint64) {
	if size == 0 {
		return
	}
	b.mu.Lock()
	if size > b.used {
		b.mu.Unlock()
		panic("router serve test memory budget released below zero")
	}
	b.used -= size
	close(b.changed)
	b.changed = make(chan struct{})
	b.mu.Unlock()
}

func (b *routerServeMemoryBudget) Changed() <-chan struct{} {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.changed
}

func (b *routerServeMemoryBudget) Used() uint64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.used
}

type routerServeUnlimitedMemoryBudget struct{}

func (routerServeUnlimitedMemoryBudget) TryReserve(uint64) bool { return true }
func (routerServeUnlimitedMemoryBudget) Release(uint64)         {}
func (routerServeUnlimitedMemoryBudget) Changed() <-chan struct{} {
	return nil
}

func startRunningRouterServeIngressOverlay(
	t *testing.T,
	inbound peermanagement.MemoryBudget,
	opts ...peermanagement.Option,
) *runningManifestOverlay {
	t.Helper()
	allOpts := append([]peermanagement.Option{peermanagement.WithListenAddr("127.0.0.1:0")}, opts...)
	overlay, err := peermanagement.New(allOpts...)
	require.NoError(t, err)
	require.NoError(t, overlay.SetMemoryBudgets(
		inbound,
		routerServeUnlimitedMemoryBudget{},
		routerServeUnlimitedMemoryBudget{},
	))
	running := &runningManifestOverlay{overlay: overlay, done: make(chan error, 1)}
	go func() { running.done <- overlay.Run(context.Background()) }()
	t.Cleanup(func() { running.stop(t) })
	select {
	case <-overlay.ListenerReady():
	case err := <-running.done:
		t.Fatalf("overlay stopped before listener became ready: %v", err)
	case <-time.After(5 * time.Second):
		stack := make([]byte, 1<<20)
		n := runtime.Stack(stack, true)
		t.Fatalf("overlay listener did not become ready\n%s", stack[:n])
	}
	return running
}

func newRouterServeFixture(t *testing.T, noncancellable bool) routerServeFixture {
	t.Helper()
	family := newRouterServeFetchGate()
	family.noncancellable = noncancellable
	svc, err := ledgerservice.New(ledgerservice.Config{
		Standalone:    true,
		GenesisConfig: genesis.DefaultConfig(),
		SHAMapFamily:  family,
	})
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(func() { svc.Stop() })

	closed := svc.GetClosedLedger()
	require.NotNil(t, closed)
	// Service startup leaves the closed ledger's resident tree attached. A
	// persisted, released snapshot preserves its hashes while making the next
	// serve walk load descendants through Family.Fetch.
	closed.SetStateMapFamily(family)
	snapshot, err := closed.StateMapSnapshotContext(context.Background())
	require.NoError(t, err)
	require.NoError(t, snapshot.AcknowledgePersistedAndReleaseContext(context.Background()))

	identity, err := NewValidatorIdentity("snoPBrXtMeMyMHUVTgbuqAfg1SUTb")
	require.NoError(t, err)
	sender := &relayRecorder{}
	adaptor := New(Config{
		LedgerService: svc,
		Identity:      identity,
		Sender:        sender,
	})
	inbox := make(chan *peermanagement.InboundMessage, 32)
	router := newTestRouter(&mockEngine{}, adaptor, inbox)
	return routerServeFixture{
		router:     router,
		sender:     sender,
		family:     family,
		ledgerHash: closed.Hash(),
	}
}

func startRouterServeFixture(t *testing.T, fixture routerServeFixture) (context.CancelFunc, <-chan struct{}) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		fixture.router.Run(ctx)
		close(done)
	}()
	t.Cleanup(func() {
		fixture.family.releaseFetch()
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Errorf("router did not join during cleanup")
		}
	})
	require.Eventually(t, func() bool {
		fixture.router.lifecycleMu.RLock()
		defer fixture.router.lifecycleMu.RUnlock()
		return fixture.router.lifecycleState == routerLifecycleRunning
	}, time.Second, time.Millisecond)
	return cancel, done
}

func routerServePayload(t *testing.T, hash [32]byte) []byte {
	t.Helper()
	req := &message.GetLedger{
		InfoType:   message.LedgerInfoAsNode,
		LedgerHash: append([]byte(nil), hash[:]...),
		NodeIDs:    [][]byte{rootNodeID()},
	}
	return encodePayload(t, req)
}

func routerServeRequest(peerID peermanagement.PeerID, payload []byte) *peermanagement.InboundMessage {
	return &peermanagement.InboundMessage{
		PeerID:  peerID,
		Type:    message.TypeGetLedger,
		Payload: append([]byte(nil), payload...),
	}
}

func waitRouterServeFetch(t *testing.T, family *routerServeFetchGate) {
	t.Helper()
	select {
	case <-family.started:
	case <-time.After(2 * time.Second):
		t.Fatal("serve request did not reach the backing Family")
	}
}

func TestRouterIssue2080_ShutdownCancelsServeFetchAndJoins(t *testing.T) {
	fixture := newRouterServeFixture(t, false)
	cancel, done := startRouterServeFixture(t, fixture)

	fixture.family.armFetch()
	payload := routerServePayload(t, fixture.ledgerHash)
	msg := routerServeRequest(11, payload)
	require.True(t, fixture.router.handleMessage(msg))
	waitRouterServeFetch(t, fixture.family)
	require.Positive(t, fixture.router.serveMemory.Snapshot().Used)

	cancel()
	select {
	case <-fixture.family.canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("router cancellation did not reach the in-flight Family fetch")
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("router did not join after the cancellable serve fetch returned")
	}

	require.GreaterOrEqual(t, fixture.family.fetches.Load(), int32(1))
	require.Zero(t, fixture.router.serveMemory.Snapshot().Used)
	require.Empty(t, fixture.sender.sentFrames(), "a canceled serve must not emit a reply")
	fixture.router.lifecycleMu.RLock()
	require.Equal(t, routerLifecycleStopped, fixture.router.lifecycleState)
	fixture.router.lifecycleMu.RUnlock()
	metrics := fixture.router.LedgerServeMetrics()
	for _, stats := range []peermanagement.ServeSchedulerStats{metrics.Ledger, metrics.TxSet} {
		require.Zero(t, stats.Queued)
		require.Zero(t, stats.Active)
		require.Zero(t, stats.RetainedBytes)
	}
}

func TestRouterIssue2080_NoncancellableServeKeepsJoinUntilReadReturns(t *testing.T) {
	fixture := newRouterServeFixture(t, true)
	cancel, done := startRouterServeFixture(t, fixture)

	fixture.family.armFetch()
	payload := routerServePayload(t, fixture.ledgerHash)
	msg := routerServeRequest(12, payload)
	require.True(t, fixture.router.handleMessage(msg))
	waitRouterServeFetch(t, fixture.family)
	require.Positive(t, fixture.router.serveMemory.Snapshot().Used)

	cancel()
	select {
	case <-fixture.family.canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("router cancellation did not reach the noncancellable fetch")
	}
	select {
	case <-done:
		t.Fatal("router joined before the deliberate noncancellable read returned")
	case <-time.After(100 * time.Millisecond):
	}

	fetchesBeforeRelease := fixture.family.fetches.Load()
	fixture.family.releaseFetch()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("router did not join after the noncancellable read returned")
	}

	require.Equal(t, fetchesBeforeRelease, fixture.family.fetches.Load(),
		"joining a released serve must not start another backing fetch")
	require.Zero(t, fixture.router.serveMemory.Snapshot().Used)
	require.Empty(t, fixture.sender.sentFrames(), "a canceled serve must not emit a late reply")
}

func TestRouterIssue2080_PeerDisconnectReleasesInboundReservationsAtOwnershipBoundary(t *testing.T) {
	oldWorkerCount := serveWorkerCount
	serveWorkerCount = 2
	t.Cleanup(func() { serveWorkerCount = oldWorkerCount })

	fixture := newRouterServeFixture(t, true)
	budget := newRouterServeMemoryBudget(1 << 30)
	source := startRunningRouterServeIngressOverlay(t, routerServeUnlimitedMemoryBudget{}, peermanagement.WithCompression(false))
	connections := make(chan manifestOverlayConnection, 1)
	source.overlay.SetPeerConnectCallback(func(peerID peermanagement.PeerID) {
		connections <- manifestOverlayConnection{overlay: source, peerID: peerID}
	})
	client := startRunningRouterServeIngressOverlay(t, budget,
		peermanagement.WithCompression(false),
		peermanagement.WithMaxOutbound(1),
		peermanagement.WithFixedPeers(source.overlay.ListenAddr()),
	)
	var connection manifestOverlayConnection
	select {
	case connection = <-connections:
	case <-time.After(5 * time.Second):
		t.Fatal("serve source did not connect")
	}

	sessions := &testPeerSessions{connected: make(map[peermanagement.PeerID]bool)}
	fixture.router.peerSessions = sessions
	cancel, done := startRouterServeFixture(t, fixture)
	payload := routerServePayload(t, fixture.ledgerHash)
	send := func() {
		t.Helper()
		frame, err := message.BuildWireMessage(message.TypeGetLedger, payload)
		require.NoError(t, err)
		require.NoError(t, connection.overlay.overlay.Send(connection.peerID, frame))
	}
	receive := func() *peermanagement.InboundMessage {
		t.Helper()
		for {
			select {
			case inbound := <-client.overlay.Messages():
				if inbound.Type != message.TypeGetLedger {
					require.NoError(t, inbound.Close())
					continue
				}
				return inbound
			case <-time.After(5 * time.Second):
				t.Fatal("serve request did not reach the production ingress lane")
				return nil
			}
		}
	}

	baseline := budget.Used()
	fixture.family.armFetch()
	send()
	running := receive()
	sessions.set(running.PeerID, true)
	require.True(t, fixture.router.handleMessage(running))
	waitRouterServeFetch(t, fixture.family)
	require.Positive(t, fixture.router.serveMemory.Snapshot().Used)
	require.Eventually(t, func() bool {
		return budget.Used() > baseline
	}, time.Second, time.Millisecond, "running inbound message did not retain its ingress reservation")
	runningHeld := budget.Used()
	require.Greater(t, runningHeld, baseline)

	send()
	queued := receive()
	require.True(t, fixture.router.handleMessage(queued))
	require.Eventually(t, func() bool {
		return fixture.router.LedgerServeMetrics().Ledger.Queued == 1
	}, time.Second, time.Millisecond, "second production-ingress request did not remain queued")
	queuedHeld := budget.Used()
	require.Greater(t, queuedHeld, runningHeld)

	sessions.set(running.PeerID, false)
	fixture.router.queuePeerDisconnect(running.PeerID)
	select {
	case <-fixture.family.canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("peer disconnect did not cancel the active read")
	}
	require.Eventually(t, func() bool {
		return budget.Used() == runningHeld
	}, time.Second, time.Millisecond,
		"peer disconnect did not release the queued ingress reservation")
	select {
	case <-done:
		t.Fatal("router joined while the deliberately noncancellable read was still active")
	case <-time.After(100 * time.Millisecond):
	}
	require.Positive(t, fixture.router.serveMemory.Snapshot().Used)
	require.Equal(t, runningHeld, budget.Used(),
		"running ingress reservation must remain owned until the serve returns")

	fixture.family.releaseFetch()
	require.Eventually(t, func() bool {
		return budget.Used() == baseline
	}, 2*time.Second, time.Millisecond,
		"running ingress reservation was not released after the serve returned")
	require.Zero(t, fixture.router.serveMemory.Snapshot().Used)
	require.Empty(t, fixture.sender.sentFrames(), "disconnected peer must receive no serve reply")

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		stack := make([]byte, 1<<20)
		n := runtime.Stack(stack, true)
		t.Fatalf("router did not stop after peer serve cancellation\n%s", stack[:n])
	}
	require.NoError(t, running.Close())
	require.NoError(t, queued.Close())
	require.Equal(t, baseline, budget.Used(), "message close must release each ingress reservation once")
}

func TestRouterIssue2080_ServeAdmissionRaceWithPeerDisconnect(t *testing.T) {
	oldWorkerCount := serveWorkerCount
	serveWorkerCount = 2
	t.Cleanup(func() { serveWorkerCount = oldWorkerCount })

	fixture := newRouterServeFixture(t, false)
	const peerID = peermanagement.PeerID(22)
	sessions := &testPeerSessions{connected: map[peermanagement.PeerID]bool{peerID: true}}
	fixture.router.peerSessions = sessions
	cancel, done := startRouterServeFixture(t, fixture)

	fixture.family.armFetch()
	payload := routerServePayload(t, fixture.ledgerHash)
	running := routerServeRequest(peerID, payload)
	require.True(t, fixture.router.handleMessage(running))
	waitRouterServeFetch(t, fixture.family)
	require.Positive(t, fixture.router.serveMemory.Snapshot().Used)

	const submitters = 64
	start := make(chan struct{})
	var wg sync.WaitGroup
	for range submitters {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			msg := routerServeRequest(peerID, payload)
			if !fixture.router.handleMessage(msg) {
				_ = msg.Close()
			}
		}()
	}
	close(start)
	sessions.set(peerID, false)
	fixture.router.queuePeerDisconnect(peerID)
	wg.Wait()

	select {
	case <-fixture.family.canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("admission race did not cancel the running serve")
	}
	require.Eventually(t, func() bool {
		stats := fixture.router.LedgerServeMetrics().Ledger
		return stats.Queued == 0 && stats.Active == 0 && stats.RetainedBytes == 0
	}, time.Second, time.Millisecond, "admission race left serve jobs queued after disconnect")
	require.Empty(t, fixture.sender.sentFrames(), "admission race must not produce a late peer reply")
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("router did not stop after admission-race cancellation")
	}
}

type routerServeCancelOnDepthSender struct {
	relayRecorder
	cancel context.CancelFunc
}

func (s *routerServeCancelOnDepthSender) PeerSupportsNodeDepth(uint64) bool {
	s.cancel()
	return false
}

func TestRouterIssue2080_EncodingCancellationSuppressesServeReply(t *testing.T) {
	fixture := newRouterServeFixture(t, false)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	sender := &routerServeCancelOnDepthSender{cancel: cancel}
	fixture.router.serve = sender
	resp := &message.LedgerData{
		LedgerHash: fixture.ledgerHash[:],
		LedgerSeq:  1,
		InfoType:   message.LedgerInfoAsNode,
		Nodes: []message.LedgerNode{{
			NodeID:   rootNodeID(),
			NodeData: []byte{1},
		}},
	}

	require.False(t, fixture.router.sendLedgerResponse(ctx, testLedgerServeWorkspace(), 24, resp))
	require.ErrorIs(t, ctx.Err(), context.Canceled)
	require.Empty(t, sender.sentFrames(), "cancellation during node-depth preparation must suppress encoding/send")
}
