package service

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/stretchr/testify/require"
)

type preferredMemoryBudget struct {
	mu       sync.Mutex
	limit    uint64
	capacity uint64
	used     uint64

	changed     chan struct{}
	attempted   chan struct{}
	attemptOnce sync.Once
}

func (b *preferredMemoryBudget) Limit() uint64 { return b.limit }

func (b *preferredMemoryBudget) Capacity() uint64 { return b.capacity }

func (b *preferredMemoryBudget) Changed() <-chan struct{} { return b.changed }

func (b *preferredMemoryBudget) TryReserve(bytes uint64) bool {
	b.attemptOnce.Do(func() {
		if b.attempted != nil {
			close(b.attempted)
		}
	})
	b.mu.Lock()
	defer b.mu.Unlock()
	if bytes > b.bytesAvailable() {
		return false
	}
	b.used += bytes
	return true
}

func (b *preferredMemoryBudget) bytesAvailable() uint64 {
	if b.used >= b.limit {
		return 0
	}
	return b.limit - b.used
}

func (b *preferredMemoryBudget) Release(bytes uint64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if bytes > b.used {
		panic("preferred memory budget underflow")
	}
	b.used -= bytes
}

func (b *preferredMemoryBudget) Used() uint64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.used
}

func TestPreparedPreferredLedgerConfiguredMemoryFinishReleasesBeforeStop(t *testing.T) {
	budget := &preferredMemoryBudget{
		limit:    512 << 20,
		capacity: 512 << 20,
		changed:  make(chan struct{}),
	}
	cfg := DefaultConfig()
	cfg.PersistenceMemory = budget
	svc, err := New(cfg)
	require.NoError(t, err)
	require.NoError(t, svc.Start())

	parent := svc.GetClosedLedger()
	preferred, err := ledger.NewOpen(parent, time.Now())
	require.NoError(t, err)
	require.NoError(t, preferred.Close(time.Now(), 0))
	prepared, err := svc.PreparePreferredLedger(context.Background(), preferred)
	require.NoError(t, err)
	require.NoError(t, prepared.Wait(context.Background()))
	require.NotZero(t, budget.Used())

	prepared.relay = []preferredRelay{{hash: [32]byte{1}, blob: []byte{1}}}
	callbackDone := make(chan struct{})
	prepared.relayFn = func([]byte) {
		svc.Stop()
		close(callbackDone)
	}
	published, err := prepared.TryPublish()
	require.NoError(t, err)
	require.True(t, published)

	finishDone := make(chan struct{})
	go func() {
		prepared.Finish()
		close(finishDone)
	}()
	select {
	case <-callbackDone:
	case <-time.After(time.Second):
		t.Fatal("configured-memory callback deadlocked while stopping the service")
	}
	select {
	case <-finishDone:
	case <-time.After(time.Second):
		t.Fatal("configured-memory finish did not drain")
	}
	require.Zero(t, budget.Used())
	prepared.Release()
}

func TestAcquirePreferredTransitionMemoryRejectsCapacityOversize(t *testing.T) {
	budget := &preferredMemoryBudget{
		limit:    8 << 20,
		capacity: 1 << 20,
		changed:  make(chan struct{}),
	}
	svc := &Service{config: Config{PersistenceMemory: budget}}

	release, err := svc.acquirePreferredTransitionMemory(context.Background(), 1<<20+1)
	require.ErrorIs(t, err, ErrPreferredTransitionMemory)
	require.Nil(t, release)
	require.Zero(t, budget.Used())
}

func TestPreparedPreferredLedgerStopCancelsBlockedMemoryAdmission(t *testing.T) {
	budget := &preferredMemoryBudget{
		limit:     8 << 20,
		capacity:  8 << 20,
		used:      8 << 20,
		changed:   make(chan struct{}),
		attempted: make(chan struct{}),
	}
	cfg := DefaultConfig()
	cfg.PersistenceMemory = budget
	svc, err := New(cfg)
	require.NoError(t, err)
	require.NoError(t, svc.Start())

	parent := svc.GetClosedLedger()
	preferred, err := ledger.NewOpen(parent, time.Now())
	require.NoError(t, err)
	require.NoError(t, preferred.Close(time.Now(), 0))

	type prepareResult struct {
		prepared *PreparedPreferredLedger
		err      error
	}
	preparedDone := make(chan prepareResult, 1)
	go func() {
		prepared, err := svc.PreparePreferredLedger(context.Background(), preferred)
		preparedDone <- prepareResult{prepared: prepared, err: err}
	}()
	select {
	case <-budget.attempted:
	case <-time.After(time.Second):
		t.Fatal("preferred preparation did not reach memory admission")
	}

	stopDone := make(chan struct{})
	go func() {
		svc.Stop()
		close(stopDone)
	}()
	select {
	case <-stopDone:
	case <-time.After(time.Second):
		t.Fatal("service stop remained blocked behind canceled preparation")
	}
	select {
	case result := <-preparedDone:
		require.ErrorIs(t, result.err, context.Canceled)
		if result.prepared != nil {
			result.prepared.Release()
		}
	case <-time.After(time.Second):
		t.Fatal("blocked preparation did not return after service stop")
	}
}
