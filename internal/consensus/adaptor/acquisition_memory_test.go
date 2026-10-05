package adaptor

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/crypto/sha512half"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/ledger/inbound"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/LeJamon/go-xrpl/shamap/backend"
	"github.com/stretchr/testify/require"
)

func TestAcquisitionStoreMemoryBackpressure(t *testing.T) {
	base := newAcquisitionStoreTestFamily()
	base.blockFirst = make(chan struct{})
	lane := newAcquisitionStoreLane(base, serveTestLogger(), 2)
	first := []shamap.FlushEntry{acquisitionEntry(1)}
	charge := acquisitionEntryBytes(first)
	lane.memory = shamap.NewAcquisitionBudget(4 * charge)
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
	ledger := inbound.New(ledgerHash, h.LedgerIndex, 7, serveTestLogger(),
		inbound.WithFamily(store.scope()), inbound.WithAcquisitionBudget(budget))
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
	event := acquisitionWorkEvent{kind: acquisitionWorkLocal, fetch: func([32]byte) ([]byte, bool) { return nil, false }}
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
