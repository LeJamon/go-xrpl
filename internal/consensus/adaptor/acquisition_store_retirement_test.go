package adaptor

import (
	"bytes"
	"context"
	"sync"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger/inbound"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/stretchr/testify/require"
)

func TestAcquisitionStoreRetirementRejectsAdmission(t *testing.T) {
	base := newAcquisitionStoreTestFamily()
	lane := newAcquisitionStoreLane(base, nil, 1)
	scope := lane.scope().(*acquisitionStoreScope)

	require.NoError(t, scope.Retire(t.Context()))
	require.ErrorIs(t, scope.StoreBatch(t.Context(), []shamap.FlushEntry{acquisitionEntry(1)}), inbound.ErrAcquisitionRetired)
	require.ErrorIs(t, scope.Flush(t.Context()), inbound.ErrAcquisitionRetired)
	_, err := scope.Promote(t.Context())
	require.ErrorIs(t, err, inbound.ErrAcquisitionRetired)
	require.NoError(t, scope.Retire(t.Context()))

	base.mu.Lock()
	calls := append([][32]byte(nil), base.calls...)
	base.mu.Unlock()
	require.Empty(t, calls)
	require.Zero(t, lane.bytes.Load())
}

func TestAcquisitionStoreRetirementDropsQueuedWrite(t *testing.T) {
	base := newAcquisitionStoreTestFamily()
	base.blockFirst = make(chan struct{})
	lane := newAcquisitionStoreLane(base, nil, 1)
	lane.start(context.Background())

	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(base.blockFirst) }) }
	t.Cleanup(func() {
		release()
		lane.stopDrain()
	})

	scope := lane.scope().(*acquisitionStoreScope)
	other := lane.scope().(*acquisitionStoreScope)
	first, second := acquisitionEntry(2), acquisitionEntry(3)
	otherEntry := acquisitionEntry(11)
	require.NoError(t, scope.StoreBatch(t.Context(), []shamap.FlushEntry{first}))
	require.Equal(t, first.Hash, <-base.started)
	require.NoError(t, scope.StoreBatch(t.Context(), []shamap.FlushEntry{second}))
	require.Eventually(t, func() bool {
		lane.pendingMu.RLock()
		defer lane.pendingMu.RUnlock()
		_, ok := lane.pending[pendingAcquisitionKey{scope: scope.id, hash: second.Hash}]
		return ok
	}, time.Second, time.Millisecond)

	require.NoError(t, scope.Retire(t.Context()))
	require.ErrorIs(t, scope.Flush(t.Context()), inbound.ErrAcquisitionRetired)
	release()
	require.NoError(t, other.StoreBatch(t.Context(), []shamap.FlushEntry{otherEntry}))
	require.NoError(t, other.Flush(t.Context()))
	require.Eventually(t, func() bool {
		base.mu.Lock()
		calls := append([][32]byte(nil), base.calls...)
		base.mu.Unlock()
		lane.pendingMu.RLock()
		pending := len(lane.pending)
		lane.pendingMu.RUnlock()
		return len(calls) == 2 && calls[0] == first.Hash && calls[1] == otherEntry.Hash && pending == 0 && lane.bytes.Load() == 0
	}, time.Second, time.Millisecond)
}

func TestAcquisitionStoreRetirementUnblocksQueuedAdmission(t *testing.T) {
	base := newAcquisitionStoreTestFamily()
	base.blockFirst = make(chan struct{})
	lane := newAcquisitionStoreLane(base, nil, 1)
	lane.start(context.Background())

	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(base.blockFirst) }) }
	t.Cleanup(func() {
		release()
		lane.stopDrain()
	})

	scope := lane.scope().(*acquisitionStoreScope)
	first, second, third := acquisitionEntry(4), acquisitionEntry(5), acquisitionEntry(6)
	require.NoError(t, scope.StoreBatch(t.Context(), []shamap.FlushEntry{first}))
	require.Equal(t, first.Hash, <-base.started)
	require.NoError(t, scope.StoreBatch(t.Context(), []shamap.FlushEntry{second}))

	thirdDone := make(chan error, 1)
	go func() { thirdDone <- scope.StoreBatch(t.Context(), []shamap.FlushEntry{third}) }()
	require.Eventually(t, func() bool {
		lane.pendingMu.RLock()
		defer lane.pendingMu.RUnlock()
		_, ok := lane.pending[pendingAcquisitionKey{scope: scope.id, hash: third.Hash}]
		return ok
	}, time.Second, time.Millisecond)
	select {
	case err := <-thirdDone:
		t.Fatalf("queued admission returned before retirement: %v", err)
	default:
	}

	retireDone := make(chan error, 1)
	go func() { retireDone <- scope.Retire(t.Context()) }()
	select {
	case err := <-retireDone:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("retirement waited for a blocked admission")
	}
	require.ErrorIs(t, <-thirdDone, inbound.ErrAcquisitionRetired)
	release()
	require.Eventually(t, func() bool {
		base.mu.Lock()
		calls := append([][32]byte(nil), base.calls...)
		base.mu.Unlock()
		lane.pendingMu.RLock()
		pending := len(lane.pending)
		lane.pendingMu.RUnlock()
		return len(calls) == 1 && calls[0] == first.Hash && pending == 0 && lane.bytes.Load() == 0
	}, time.Second, time.Millisecond)
}

func TestAcquisitionStoreRetirementPreservesBorrowedBufferLifetime(t *testing.T) {
	base := newAcquisitionStoreTestFamily()
	base.blockFirst = make(chan struct{})
	lane := newAcquisitionStoreLane(base, nil, 1)
	lane.memory = shamap.NewAcquisitionBudget(1)
	lane.start(context.Background())

	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(base.blockFirst) }) }
	t.Cleanup(func() {
		release()
		lane.stopDrain()
	})

	scope := lane.scope().(*acquisitionStoreScope)
	entry := acquisitionEntry(7)
	original := bytes.Clone(entry.Data)
	storeDone := make(chan error, 1)
	go func() { storeDone <- scope.StoreBatch(t.Context(), []shamap.FlushEntry{entry}) }()
	require.Equal(t, entry.Hash, <-base.started)
	require.NoError(t, scope.Retire(t.Context()))
	select {
	case err := <-storeDone:
		t.Fatalf("borrowed caller returned before backing storage released its slices: %v", err)
	case <-time.After(25 * time.Millisecond):
	}

	release()
	require.ErrorIs(t, <-storeDone, inbound.ErrAcquisitionRetired)
	entry.Data[0]++
	require.ErrorIs(t, scope.Flush(t.Context()), inbound.ErrAcquisitionRetired)
	require.Eventually(t, func() bool {
		base.mu.Lock()
		calls := append([][32]byte(nil), base.calls...)
		stored := bytes.Clone(base.data[entry.Hash])
		base.mu.Unlock()
		return len(calls) == 1 && calls[0] == entry.Hash && bytes.Equal(stored, original) && lane.bytes.Load() == 0
	}, time.Second, time.Millisecond)
}

func TestAcquisitionStoreRetirementAbortsPromotionBarrier(t *testing.T) {
	base := newAcquisitionStoreTestFamily()
	base.blockFirst = make(chan struct{})
	lane := newAcquisitionStoreLane(base, nil, 1)
	lane.start(context.Background())

	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(base.blockFirst) }) }
	t.Cleanup(func() {
		release()
		lane.stopDrain()
	})

	scope := lane.scope().(*acquisitionStoreScope)
	entry := acquisitionEntry(8)
	require.NoError(t, scope.StoreBatch(t.Context(), []shamap.FlushEntry{entry}))
	require.Equal(t, entry.Hash, <-base.started)

	promoteDone := make(chan error, 1)
	go func() {
		_, err := scope.Promote(t.Context())
		promoteDone <- err
	}()
	require.Eventually(t, func() bool { return len(lane.jobs) == 1 }, time.Second, time.Millisecond)
	require.NoError(t, scope.Retire(t.Context()))
	require.ErrorIs(t, <-promoteDone, inbound.ErrAcquisitionRetired)

	scope.mu.Lock()
	promoted, retired := scope.promoted, scope.retired
	scope.mu.Unlock()
	require.False(t, promoted)
	require.True(t, retired)
	release()
	require.Eventually(t, func() bool {
		base.mu.Lock()
		calls := append([][32]byte(nil), base.calls...)
		base.mu.Unlock()
		return len(calls) == 1 && calls[0] == entry.Hash && lane.bytes.Load() == 0
	}, time.Second, time.Millisecond)
}

func TestAcquisitionStoreShutdownRecordsDiscardedOwnerFailure(t *testing.T) {
	base := newAcquisitionStoreTestFamily()
	base.blockFirst = make(chan struct{})
	lane := newAcquisitionStoreLane(base, nil, 1)
	lane.drainTimeout = 25 * time.Millisecond
	lane.start(context.Background())

	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(base.blockFirst) }) }
	t.Cleanup(func() {
		release()
		lane.stopDrain()
	})

	firstScope := lane.scope().(*acquisitionStoreScope)
	secondScope := lane.scope().(*acquisitionStoreScope)
	first, second := acquisitionEntry(12), acquisitionEntry(13)
	require.NoError(t, firstScope.StoreBatch(t.Context(), []shamap.FlushEntry{first}))
	require.Equal(t, first.Hash, <-base.started)
	require.NoError(t, secondScope.StoreBatch(t.Context(), []shamap.FlushEntry{second}))

	lane.stopDrain()
	require.ErrorIs(t, secondScope.Flush(t.Context()), context.Canceled)
	_, err := secondScope.Promote(t.Context())
	require.ErrorIs(t, err, context.Canceled)

	base.mu.Lock()
	calls := append([][32]byte(nil), base.calls...)
	stored := bytes.Clone(base.data[second.Hash])
	base.mu.Unlock()
	require.Equal(t, [][32]byte{first.Hash}, calls)
	require.Nil(t, stored)
	require.Zero(t, lane.bytes.Load())
	lane.pendingMu.RLock()
	pending := len(lane.pending)
	lane.pendingMu.RUnlock()
	require.Zero(t, pending)
}

func TestAcquisitionStoreRetirementPreservesBackingFailure(t *testing.T) {
	base := newAcquisitionStoreTestFamily()
	base.failFirst = true
	lane := newAcquisitionStoreLane(base, nil, 1)
	lane.start(context.Background())
	t.Cleanup(lane.stopDrain)

	scope := lane.scope().(*acquisitionStoreScope)
	require.NoError(t, scope.StoreBatch(t.Context(), []shamap.FlushEntry{acquisitionEntry(9)}))
	require.EqualError(t, scope.Flush(t.Context()), "store failed")
	require.EqualError(t, scope.failure(), "store failed")
	require.NoError(t, scope.Retire(t.Context()))
	require.ErrorIs(t, scope.Flush(t.Context()), inbound.ErrAcquisitionRetired)
	_, err := scope.Promote(t.Context())
	require.ErrorIs(t, err, inbound.ErrAcquisitionRetired)
	require.ErrorIs(t, scope.StoreBatch(t.Context(), []shamap.FlushEntry{acquisitionEntry(10)}), inbound.ErrAcquisitionRetired)
}
