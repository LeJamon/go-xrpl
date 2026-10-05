package adaptor

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/crypto/sha512half"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/ledger/inbound"
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/LeJamon/go-xrpl/shamap/backend"
	"github.com/stretchr/testify/require"
)

func TestAcquisitionInboxWaitsForPendingLedgerCapacity(t *testing.T) {
	ledger, replies := newWideWorkLedger(t)
	router := newTestRouter(nil, newTestAdaptor(t), nil)
	router.catchupReplay.fetchTracker.Track(ledger)
	inbox := make(chan *peermanagement.InboundMessage, 1)
	router.SetAcqInbox(inbox)
	lane := newAcquisitionWorkLane(1)
	entered, release := make(chan struct{}), make(chan struct{})
	first := inbound.New([32]byte{1}, 1, 1, serveTestLogger())
	second := inbound.New([32]byte{2}, 2, 2, serveTestLogger())
	lane.process = func(ctx context.Context, current *inbound.Ledger, _ []acquisitionWorkEvent) acquisitionWorkResult {
		if current == first {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
			}
		}
		return acquisitionWorkResult{ledger: current}
	}
	lane.start(t.Context())
	t.Cleanup(lane.stop)
	router.catchupReplay.acquisitionWork = lane
	require.True(t, lane.submit(first, acquisitionWorkEvent{kind: acquisitionWorkLocal}))
	<-entered
	require.True(t, lane.submit(second, acquisitionWorkEvent{kind: acquisitionWorkLocal}))
	hash := ledger.Hash()
	inbox <- &peermanagement.InboundMessage{
		PeerID: 7, Type: message.TypeLedgerData,
		Payload: encodePayload(t, &message.LedgerData{
			LedgerHash: hash[:], InfoType: message.LedgerInfoAsNode, Nodes: replies[:1],
		}),
	}
	require.Zero(t, router.drainAcquisitionInboxBeforeMaintenance(lane))
	require.Len(t, inbox, 1, "saturated acquisition must retain the owned reply in its inbox")
	close(release)
	result := <-lane.results()
	close(result.ack)
	require.Eventually(t, func() bool { return !lane.has(first) }, time.Second, time.Millisecond)
	require.Equal(t, 1, router.drainAcquisitionInboxBeforeMaintenance(lane))
	require.True(t, lane.has(ledger))
	require.Empty(t, inbox)
	for range 2 {
		result = <-lane.results()
		close(result.ack)
	}
}

func TestAcquisitionWorkCountsRepliesHeldByRunningBatch(t *testing.T) {
	lane := newAcquisitionWorkLane(1)
	ledger := inbound.New([32]byte{1}, 1, 1, serveTestLogger())
	entered, release := make(chan struct{}), make(chan struct{})
	lane.process = func(ctx context.Context, current *inbound.Ledger, _ []acquisitionWorkEvent) acquisitionWorkResult {
		close(entered)
		select {
		case <-release:
		case <-ctx.Done():
		}
		return acquisitionWorkResult{ledger: current}
	}
	lane.ctx = t.Context()
	for range acquisitionWorkBatchLimit {
		require.True(t, lane.submit(ledger, acquisitionWorkEvent{kind: acquisitionWorkData}))
	}
	lane.start(t.Context())
	t.Cleanup(lane.stop)
	<-entered
	require.False(t, lane.canAcceptData())
	require.False(t, lane.submit(ledger, acquisitionWorkEvent{kind: acquisitionWorkData}))
	close(release)
	result := <-lane.results()
	close(result.ack)
	require.Eventually(t, lane.canAcceptData, time.Second, time.Millisecond)
}

type gatedAcquisitionStoreFamily struct {
	*acquisitionStoreTestFamily
	entered chan [32]byte
	gates   map[[32]byte]chan struct{}
}

func (f *gatedAcquisitionStoreFamily) StoreBatch(ctx context.Context, entries []shamap.FlushEntry) error {
	if len(entries) > 0 {
		if gate := f.gates[entries[0].Hash]; gate != nil {
			f.entered <- entries[0].Hash
			select {
			case <-gate:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
	return f.acquisitionStoreTestFamily.StoreBatch(ctx, entries)
}

func TestAcquisitionStoreRetainsIndexChargeUntilDrained(t *testing.T) {
	bulk := make([]shamap.FlushEntry, 128)
	for i := range bulk {
		bulk[i] = acquisitionEntry(byte(i))
	}
	last := []shamap.FlushEntry{acquisitionEntry(250)}
	base := &gatedAcquisitionStoreFamily{
		acquisitionStoreTestFamily: newAcquisitionStoreTestFamily(),
		entered:                    make(chan [32]byte, 2),
		gates: map[[32]byte]chan struct{}{
			bulk[0].Hash: make(chan struct{}), last[0].Hash: make(chan struct{}),
		},
	}
	lane := newAcquisitionStoreLane(base, serveTestLogger(), 2)
	charge := acquisitionEntryBytes(bulk) + acquisitionEntryBytes(last)
	lane.memory = shamap.NewAcquisitionBudget(8 * charge)
	lane.start(t.Context())
	t.Cleanup(lane.stopDrain)
	require.NoError(t, lane.StoreBatch(t.Context(), bulk))
	require.Equal(t, bulk[0].Hash, <-base.entered)
	require.NoError(t, lane.StoreBatch(t.Context(), last))
	close(base.gates[bulk[0].Hash])
	require.Equal(t, last[0].Hash, <-base.entered)
	lane.pendingMu.RLock()
	remaining := len(lane.pending)
	lane.pendingMu.RUnlock()
	require.Equal(t, 1, remaining)
	require.Equal(t, charge, lane.bytes.Load())
	require.Equal(t, charge, lane.memory.Snapshot().Used)
	require.False(t, lane.reserveBytes(1))
	close(base.gates[last[0].Hash])
	require.NoError(t, lane.flush(t.Context()))
	require.Zero(t, lane.memory.Snapshot().Used)
	require.Zero(t, lane.bytes.Load())
}

func TestAcquisitionStoreMemoryBackpressure(t *testing.T) {
	base := newAcquisitionStoreTestFamily()
	base.blockFirst = make(chan struct{})
	lane := newAcquisitionStoreLane(base, serveTestLogger(), 2)
	first := []shamap.FlushEntry{acquisitionEntry(1)}
	charge := acquisitionEntryBytes(first)
	lane.memory = shamap.NewAcquisitionBudget(8 * charge)
	lane.start(t.Context())
	t.Cleanup(lane.stopDrain)
	require.NoError(t, lane.StoreBatch(t.Context(), first))
	require.Equal(t, first[0].Hash, <-base.started)
	require.Equal(t, charge, lane.memory.Snapshot().Used)
	second := []shamap.FlushEntry{acquisitionEntry(2)}
	done := make(chan error, 1)
	go func() { done <- lane.StoreBatch(t.Context(), second) }()
	select {
	case err := <-done:
		t.Fatalf("second batch bypassed memory backpressure: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	require.Equal(t, charge, lane.memory.Snapshot().Peak)
	close(base.blockFirst)
	require.NoError(t, <-done)
	require.NoError(t, lane.flush(t.Context()))
	require.Zero(t, lane.memory.Snapshot().Used)
	require.Zero(t, lane.bytes.Load())
	require.LessOrEqual(t, lane.peakBytes.Load(), charge)
	stored, err := base.Fetch(t.Context(), second[0].Hash)
	require.NoError(t, err)
	require.Equal(t, second[0].Data, stored)
}

func TestAcquisitionStoreOversizedBatchUsesSynchronousPersistence(t *testing.T) {
	base := newAcquisitionStoreTestFamily()
	lane := newAcquisitionStoreLane(base, serveTestLogger(), 2)
	lane.memory = shamap.NewAcquisitionBudget(1)
	lane.start(t.Context())
	t.Cleanup(lane.stopDrain)
	entry := acquisitionEntry(3)
	require.NoError(t, lane.StoreBatch(t.Context(), []shamap.FlushEntry{entry}))
	stored, err := base.Fetch(t.Context(), entry.Hash)
	require.NoError(t, err)
	require.Equal(t, entry.Data, stored)
	require.Zero(t, lane.memory.Snapshot().Peak)
	require.Zero(t, lane.peakBytes.Load())
	base.mu.Lock()
	base.failAll = true
	base.mu.Unlock()
	require.Error(t, lane.StoreBatch(t.Context(), []shamap.FlushEntry{acquisitionEntry(4)}))
	require.EqualError(t, lane.flush(t.Context()), "store failed")
}

func TestAcquisitionStoreBorrowedBatchPreservesWriteOrder(t *testing.T) {
	base := newAcquisitionStoreTestFamily()
	base.blockFirst = make(chan struct{})
	lane := newAcquisitionStoreLane(base, serveTestLogger(), 2)
	lane.start(t.Context())
	t.Cleanup(lane.stopDrain)
	first, second, third := acquisitionEntry(1), acquisitionEntry(2), acquisitionEntry(3)
	require.NoError(t, lane.StoreBatch(t.Context(), []shamap.FlushEntry{first}))
	require.Equal(t, first.Hash, <-base.started)
	done := make(chan error, 1)
	go func() {
		done <- lane.storeBorrowed(t.Context(), lane.unscoped, []shamap.FlushEntry{second})
	}()
	require.Eventually(t, func() bool { return len(lane.jobs) == 1 }, time.Second, time.Millisecond)
	require.NoError(t, lane.StoreBatch(t.Context(), []shamap.FlushEntry{third}))
	select {
	case err := <-done:
		t.Fatalf("borrowed batch returned before the preceding write: %v", err)
	default:
	}
	close(base.blockFirst)
	require.NoError(t, <-done)
	require.NoError(t, lane.flush(t.Context()))
	base.mu.Lock()
	calls := append([][32]byte(nil), base.calls...)
	base.mu.Unlock()
	require.Equal(t, [][32]byte{first.Hash, second.Hash, third.Hash}, calls)
	require.Zero(t, lane.memory.Snapshot().Used)
}

func TestAcquisitionStoreBorrowedBatchPropagatesCancellation(t *testing.T) {
	base := newAcquisitionStoreTestFamily()
	base.blockFirst = make(chan struct{})
	lane := newAcquisitionStoreLane(base, serveTestLogger(), 2)
	lane.memory = shamap.NewAcquisitionBudget(1)
	lane.start(t.Context())
	t.Cleanup(lane.stopDrain)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- lane.StoreBatch(ctx, []shamap.FlushEntry{acquisitionEntry(1)}) }()
	<-base.started
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
	require.ErrorIs(t, lane.flush(t.Context()), context.Canceled)
	require.Empty(t, lane.pending)
	require.Zero(t, lane.memory.Snapshot().Used)
}

func TestAcquisitionStoreShutdownReturnsQueuedMemory(t *testing.T) {
	base := newAcquisitionStoreTestFamily()
	base.blockFirst = make(chan struct{})
	lane := newAcquisitionStoreLane(base, serveTestLogger(), 2)
	lane.drainTimeout = time.Millisecond
	lane.start(t.Context())
	require.NoError(t, lane.StoreBatch(t.Context(), []shamap.FlushEntry{acquisitionEntry(5)}))
	<-base.started
	require.NoError(t, lane.StoreBatch(t.Context(), []shamap.FlushEntry{acquisitionEntry(6)}))
	require.Positive(t, lane.memory.Snapshot().Used)
	lane.stopDrain()
	require.Zero(t, lane.memory.Snapshot().Used)
	require.Zero(t, lane.bytes.Load())
	require.Empty(t, lane.pending)
}

func TestAcquisitionWorkMemoryWaitLetsRepliesResume(t *testing.T) {
	lane := newAcquisitionWorkLane(2)
	ledger := inbound.New([32]byte{0xA4}, 14, 7, serveTestLogger())
	other := inbound.New([32]byte{0xB4}, 15, 8, serveTestLogger())
	lane.process = func(_ context.Context, current *inbound.Ledger, events []acquisitionWorkEvent) acquisitionWorkResult {
		if current == ledger && events[0].kind == acquisitionWorkLocal {
			return acquisitionWorkResult{ledger: current, err: shamap.ErrAcquisitionMemory}
		}
		return acquisitionWorkResult{ledger: current}
	}
	lane.start(t.Context())
	t.Cleanup(lane.stop)
	require.True(t, lane.submit(ledger, acquisitionWorkEvent{kind: acquisitionWorkLocal}))
	first := <-lane.results()
	require.NoError(t, first.err)
	require.True(t, first.memoryWait)
	require.False(t, first.yielded)
	require.False(t, first.remove)
	close(first.ack)
	require.True(t, lane.submit(other, acquisitionWorkEvent{kind: acquisitionWorkLocal}))
	second := <-lane.results()
	require.Same(t, other, second.ledger)
	close(second.ack)
	require.True(t, lane.submit(ledger, acquisitionWorkEvent{kind: acquisitionWorkData}))
	third := <-lane.results()
	require.Same(t, ledger, third.ledger)
	require.False(t, third.memoryWait)
	require.False(t, third.remove)
	close(third.ack)
	require.False(t, ledger.Snapshot().MemoryWaiting)
}

func TestAcquisitionMemoryCheckpointFailureIsPersistenceFailure(t *testing.T) {
	base := newAcquisitionStoreTestFamily()
	base.failAll = true
	store := newAcquisitionStoreLane(base, serveTestLogger(), 2)
	store.start(t.Context())
	t.Cleanup(store.stopDrain)
	scope := store.scope().(*acquisitionStoreScope)
	require.NoError(t, scope.StoreBatch(t.Context(), []shamap.FlushEntry{acquisitionEntry(1)}))
	ledger := inbound.New([32]byte{0xA5}, 15, 7, serveTestLogger(), inbound.WithFamily(scope))
	lane := newAcquisitionWorkLane(1)
	lane.process = func(context.Context, *inbound.Ledger, []acquisitionWorkEvent) acquisitionWorkResult {
		return acquisitionWorkResult{ledger: ledger, err: shamap.ErrAcquisitionMemory}
	}
	lane.start(t.Context())
	t.Cleanup(lane.stop)
	require.True(t, lane.submit(ledger, acquisitionWorkEvent{kind: acquisitionWorkLocal}))
	result := <-lane.results()
	require.True(t, result.remove)
	require.Equal(t, standardReplayFailurePersistence, result.failureClass)
	require.True(t, errors.Is(result.persistenceErr, scope.failure()))
	close(result.ack)
}

func TestAcquisitionWorkSharedMemoryResumesWithoutReplacingLedger(t *testing.T) {
	for _, recovery := range []bool{false, true} {
		name := "ordinary"
		if recovery {
			name = "recovery"
		}
		t.Run(name, func(t *testing.T) { testAcquisitionWorkSharedMemoryResume(t, recovery) })
	}
}

func testAcquisitionWorkSharedMemoryResume(t *testing.T, recovery bool) {
	t.Helper()
	source := newWideWorkSource(t, 1)
	rootHash, err := source.Hash()
	require.NoError(t, err)
	rootData, err := source.SerializeRoot()
	require.NoError(t, err)
	h := header.LedgerHeader{LedgerIndex: 2043, AccountHash: rootHash}
	headerData := header.AddRaw(h, false)
	ledgerHash := sha512half.Sum(protocol.HashPrefixLedgerMaster().Bytes(), headerData)
	base := backend.NewMemory()
	store := newAcquisitionStoreLane(base, serveTestLogger(), 2)
	budget := shamap.NewAcquisitionBudget(8 << 20)
	store.memory = budget
	store.start(t.Context())
	t.Cleanup(store.stopDrain)
	coordinator := &catchupReplayCoordinator{acquisitionStore: store, acquisitionMemory: budget}
	create := inbound.New
	if recovery {
		create = inbound.NewRecovery
	}
	ledger := create(ledgerHash, h.LedgerIndex, 7, serveTestLogger(), coordinator.acquisitionOptions(recovery)...)
	require.NoError(t, ledger.GotBase([]message.LedgerNode{{NodeData: headerData}, {NodeData: rootData}}))
	require.NoError(t, ledger.FlushPersistence(t.Context()))
	require.False(t, ledger.IsComplete())

	pack, err := source.WalkFetchPackNodes(1 << 20)
	require.NoError(t, err)
	entries := make([]shamap.FlushEntry, 0, len(pack))
	for _, node := range pack {
		entries = append(entries, shamap.FlushEntry{Hash: node.Hash, Data: node.Data})
	}
	require.NoError(t, base.StoreBatch(t.Context(), entries))
	held := budget.Snapshot().Limit - budget.Snapshot().Used
	require.True(t, budget.TryReserve(held))
	lane := newAcquisitionWorkLane(1)
	lane.flush = func(ctx context.Context, current *inbound.Ledger) error {
		return current.FlushPersistence(ctx)
	}
	lane.start(t.Context())
	t.Cleanup(lane.stop)
	event := acquisitionWorkEvent{kind: acquisitionWorkContinue}
	require.True(t, lane.submit(ledger, event))
	first := <-lane.results()
	require.NoError(t, first.err)
	require.True(t, first.memoryWait)
	require.False(t, first.complete)
	require.False(t, first.remove)
	close(first.ack)
	budget.Release(held)
	require.True(t, lane.submit(ledger, event))
	for passes := 0; ; passes++ {
		require.Less(t, passes, 100)
		result := <-lane.results()
		require.NoError(t, result.err)
		require.NoError(t, result.persistenceErr)
		require.False(t, result.remove)
		require.Same(t, ledger, result.ledger)
		close(result.ack)
		if result.complete {
			break
		}
		require.True(t, result.yielded)
	}
	require.True(t, ledger.IsComplete())
	require.Equal(t, ledgerHash, ledger.Hash())
	require.Zero(t, ledger.Timeouts())
	require.NoError(t, ledger.PromotePersistence(t.Context()))
	require.NoError(t, ledger.RetirePersistence(t.Context()))
	require.Zero(t, budget.Snapshot().Used)
	require.LessOrEqual(t, budget.Snapshot().Peak, budget.Snapshot().Limit)
}

type releasingCheckpointFamily struct {
	shamap.Family
	budget *shamap.AcquisitionBudget
	held   uint64
}

func (f *releasingCheckpointFamily) Flush(context.Context) error {
	f.budget.Release(f.held)
	f.held = 0
	return nil
}

func TestAcquisitionWorkPartialRequestWaitsForReply(t *testing.T) {
	for _, walkErr := range []error{shamap.ErrAcquisitionMemory, shamap.ErrTraversalBudget} {
		t.Run(walkErr.Error(), func(t *testing.T) {
			budget := shamap.NewAcquisitionBudget(8 << 20)
			family := &releasingCheckpointFamily{Family: backend.NewMemory(), budget: budget, held: 1024}
			require.True(t, budget.TryReserve(family.held))
			ledger := inbound.New([32]byte{0xC4}, 2043, 7, serveTestLogger(), inbound.WithFamily(family), inbound.WithAcquisitionBudget(budget))
			lane := newAcquisitionWorkLane(1)
			lane.process = func(_ context.Context, current *inbound.Ledger, events []acquisitionWorkEvent) acquisitionWorkResult {
				if events[0].kind == acquisitionWorkTimer {
					return acquisitionWorkResult{ledger: current, err: walkErr, stateIDs: [][]byte{{1}}}
				}
				return acquisitionWorkResult{ledger: current}
			}
			lane.start(t.Context())
			t.Cleanup(lane.stop)
			require.True(t, lane.submit(ledger, acquisitionWorkEvent{kind: acquisitionWorkTimer}))
			first := <-lane.results()
			require.NoError(t, first.err)
			require.NoError(t, first.persistenceErr)
			require.NotEmpty(t, first.stateIDs)
			require.False(t, first.yielded, "network output must not replay the timer event")
			require.True(t, first.rearmTimer)
			if errors.Is(walkErr, shamap.ErrAcquisitionMemory) {
				require.Zero(t, budget.Snapshot().Used, "checkpoint should recover pressure before waiting for the reply")
			}
			close(first.ack)
			require.Eventually(t, func() bool { return !lane.has(ledger) }, time.Second, time.Millisecond)
			require.True(t, lane.submit(ledger, acquisitionWorkEvent{kind: acquisitionWorkData}))
			next := <-lane.results()
			require.Empty(t, next.stateIDs, "the old timer output must not be sent again")
			require.NoError(t, next.err)
			close(next.ack)
			require.NoError(t, ledger.RetirePersistence(t.Context()))
		})
	}
}

type partialMemoryFamily struct {
	shamap.Family
	blocked [32]byte
}

func (f *partialMemoryFamily) Fetch(ctx context.Context, hash [32]byte) ([]byte, error) {
	if hash == f.blocked {
		return nil, shamap.ErrAcquisitionMemory
	}
	return f.Family.Fetch(ctx, hash)
}

func newMissingWorkLedger(t *testing.T, memoryLimited bool) *inbound.Ledger {
	t.Helper()
	source := newWideWorkSource(t, 4)
	root, err := source.Hash()
	require.NoError(t, err)
	rootData, err := source.SerializeRoot()
	require.NoError(t, err)
	h := header.LedgerHeader{LedgerIndex: 2043, AccountHash: root}
	headerData := header.AddRaw(h, false)
	hash := sha512half.Sum(protocol.HashPrefixLedgerMaster().Bytes(), headerData)
	family := &partialMemoryFamily{Family: backend.NewMemory()}
	ledger := inbound.New(hash, h.LedgerIndex, 7, serveTestLogger(), inbound.WithFamily(family))
	require.NoError(t, ledger.GotBase([]message.LedgerNode{{NodeData: headerData}, {NodeData: rootData}}))
	wire, err := source.WalkWireNodes()
	require.NoError(t, err)
	for _, node := range wire {
		if node.NodeID[32] == 1 && node.NodeID[0] == 0x10 {
			reader, err := shamap.DeserializeFromWire(node.Data)
			require.NoError(t, err)
			family.blocked = reader.Hash()
			break
		}
	}
	require.NotEqual(t, [32]byte{}, family.blocked)
	if !memoryLimited {
		family.blocked = [32]byte{}
	}
	return ledger
}

func TestSynchronousMissingRequestsKeepMemoryPartial(t *testing.T) {
	for _, mode := range []string{"reply", "timer", "added peer"} {
		t.Run(mode, func(t *testing.T) {
			ledger := newMissingWorkLedger(t, true)
			sender := &acqRecordingSender{}
			router := newTestRouter(&mockEngine{}, New(Config{LedgerService: newTestLedgerService(t), Sender: sender}), nil)
			switch mode {
			case "reply":
				router.catchupReplay.requestMissingAcquisitionNodes(ledger, 7)
			case "timer":
				router.catchupReplay.requestMissingAcquisitionNodes(ledger, 0)
			case "added peer":
				router.catchupReplay.requestMissingAcquisitionNodesFromAddedPeer(ledger, 7)
			}
			require.Len(t, sender.stateIndirects(), 1, "resource yield must not discard the partial request")
			require.False(t, ledger.IsComplete())
		})
	}
}

func TestAcquisitionWorkPartialTimerPreservesRetarget(t *testing.T) {
	for _, memoryLimited := range []bool{false, true} {
		t.Run(fmt.Sprint(memoryLimited), func(t *testing.T) {
			ledger := newMissingWorkLedger(t, memoryLimited)
			lane := newAcquisitionWorkLane(1)
			lane.process = processAcquisitionWork
			lane.ctx = t.Context()
			retryIDs := [][]byte{{42}}
			require.True(t, lane.submit(ledger, acquisitionWorkEvent{kind: acquisitionWorkTimer, peers: []uint64{7}}))
			require.True(t, lane.submit(ledger, acquisitionWorkEvent{kind: acquisitionWorkRetarget, peers: []uint64{8}, stateIDs: retryIDs, collect: true}))
			if memoryLimited {
				require.True(t, lane.submit(ledger, acquisitionWorkEvent{kind: acquisitionWorkAdded, peerID: 9}))
			}
			lane.start(t.Context())
			t.Cleanup(lane.stop)
			first := <-lane.results()
			require.NoError(t, first.err)
			require.NotEmpty(t, first.stateIDs)
			require.NotEqual(t, retryIDs, first.stateIDs)
			require.False(t, first.retarget)
			require.False(t, first.yielded)
			ledger.ReleaseUnreservedMissingNodes() // The timer fan-out failed to send.
			close(first.ack)
			second := <-lane.results()
			require.NoError(t, second.err)
			require.True(t, second.retarget)
			require.Equal(t, []uint64{8}, second.targets)
			require.Equal(t, retryIDs, second.stateIDs)
			require.Len(t, second.requests, 1)
			require.Equal(t, uint64(8), second.requests[0].PeerID)
			require.Equal(t, 1, ledger.Snapshot().RequestPeers)
			close(second.ack)
			if memoryLimited {
				third := <-lane.results()
				require.NoError(t, third.err)
				require.Empty(t, third.stateIDs, "retarget payload must not be resent")
				require.Equal(t, 1, ledger.Snapshot().RequestPeers, "independent peer work must preserve the live request")
				close(third.ack)
			}
		})
	}
}
