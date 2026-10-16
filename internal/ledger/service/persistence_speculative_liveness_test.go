package service

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/membudget"
	"github.com/LeJamon/go-xrpl/storage/nodestore"
	"github.com/stretchr/testify/require"
)

// persistenceSpeculativeBudget gates the first small persistence reservation so
// the preferred transition can reserve the shared account first. Once the gate
// opens, the real account enforces the shared-capacity dependency. The forced
// path is used only to unwind a failed liveness assertion before cleanup.
type persistenceSpeculativeBudget struct {
	account *membudget.Account

	mu                sync.Mutex
	blockSmall        bool
	forceSmall        bool
	gate              chan struct{}
	attempted         chan struct{}
	sharedDenied      chan struct{}
	virtualSmallBytes uint64
}

func newPersistenceSpeculativeBudget(account *membudget.Account) *persistenceSpeculativeBudget {
	return &persistenceSpeculativeBudget{
		account:      account,
		blockSmall:   true,
		gate:         make(chan struct{}),
		attempted:    make(chan struct{}, 1),
		sharedDenied: make(chan struct{}, 1),
	}
}

func (b *persistenceSpeculativeBudget) Limit() uint64 { return b.account.Limit() }

func (b *persistenceSpeculativeBudget) Capacity() uint64 { return b.account.Capacity() }

func (b *persistenceSpeculativeBudget) Changed() <-chan struct{} {
	b.mu.Lock()
	wake := b.gate
	b.mu.Unlock()
	return wake
}

func (b *persistenceSpeculativeBudget) TryReserve(bytes uint64) bool {
	b.mu.Lock()
	if bytes < preferredTransitionBaseBytes {
		if b.blockSmall {
			select {
			case b.attempted <- struct{}{}:
			default:
			}
			b.mu.Unlock()
			return false
		}
		if b.forceSmall {
			b.virtualSmallBytes += bytes
			b.mu.Unlock()
			return true
		}
	}
	b.mu.Unlock()
	reserved := b.account.TryReserve(bytes)
	if !reserved && bytes < preferredTransitionBaseBytes {
		select {
		case b.sharedDenied <- struct{}{}:
		default:
		}
	}
	return reserved
}

func (b *persistenceSpeculativeBudget) Release(bytes uint64) {
	b.mu.Lock()
	if bytes < preferredTransitionBaseBytes && b.virtualSmallBytes >= bytes {
		b.virtualSmallBytes -= bytes
		b.mu.Unlock()
		return
	}
	b.mu.Unlock()
	b.account.Release(bytes)
	b.mu.Lock()
	wake := b.gate
	b.gate = make(chan struct{})
	b.mu.Unlock()
	close(wake)
}

func (b *persistenceSpeculativeBudget) allowSmallReservations(force bool) {
	b.mu.Lock()
	if b.blockSmall {
		b.blockSmall = false
	}
	b.forceSmall = force
	wake := b.gate
	b.gate = make(chan struct{})
	b.mu.Unlock()
	close(wake)
}

func (b *persistenceSpeculativeBudget) virtualBytes() uint64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.virtualSmallBytes
}

func TestPersistenceWaitDoesNotHoldStateTreeForPreferredPreparation(t *testing.T) {
	const preferredBytes = uint64(preferredTransitionBaseBytes)

	pool := membudget.New(preferredBytes + 1)
	account, err := pool.NewAccount("persistence", 0, preferredBytes+1)
	require.NoError(t, err)
	budget := newPersistenceSpeculativeBudget(account)
	store := newTestNodeStore(t, 10_000)
	cfg := DefaultConfig()
	cfg.NodeStore = store
	cfg.PersistenceMemory = budget
	// SpeculativeMemory intentionally remains nil: embedded service callers use
	// PersistenceMemory for preferred preparation and persistence.
	svc, err := New(cfg)
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(func() {
		// Production persistence uses context.Background. If an assertion fails
		// while the worker is waiting, force the small test reservations through
		// so Service.Stop can still join it.
		budget.allowSmallReservations(true)
		svc.Stop()
		require.NoError(t, store.Close())
	})

	closed := svc.GetClosedLedger()
	headerValue, stateMap, txMap := acquiredLedgerFixture(t, closed.Sequence()+1, 0xE7)
	require.NoError(t, stateMap.Put([32]byte{0xE7, 0x20}, make([]byte, 32)))
	stateRoot, err := stateMap.Hash()
	require.NoError(t, err)
	headerValue.ParentHash = closed.Hash()
	headerValue.Drops = closed.TotalDrops()
	headerValue.AccountHash = stateRoot
	headerValue.CloseTime = closed.CloseTime().Add(time.Second)
	headerValue.ParentCloseTime = closed.CloseTime()
	headerValue.CloseTimeResolution = uint8(closed.CloseTimeResolution())
	headerValue.Hash = header.CalculateHash(*headerValue)

	require.NoError(t, svc.StoreLedgerWithState(t.Context(), headerValue, stateMap, txMap))
	select {
	case <-budget.attempted:
	case <-time.After(5 * time.Second):
		t.Fatal("persistence did not reach its first bounded reservation")
	}

	parent, err := svc.GetLedgerByHash(headerValue.Hash)
	require.NoError(t, err)
	require.NotNil(t, parent)
	sameParent, err := svc.GetLedgerByHash(headerValue.Hash)
	require.NoError(t, err)
	require.Same(t, parent, sameParent)

	type prepareResult struct {
		prepared *PreparedPreferredLedger
		err      error
	}
	preparedDone := make(chan prepareResult, 1)
	go func() {
		prepared, prepareErr := svc.PreparePreferredLedger(t.Context(), parent)
		preparedDone <- prepareResult{prepared: prepared, err: prepareErr}
	}()

	var result prepareResult
	preparationStalled := false
	select {
	case result = <-preparedDone:
	case <-time.After(5 * time.Second):
		preparationStalled = true
		budget.allowSmallReservations(true)
		select {
		case result = <-preparedDone:
		case <-time.After(5 * time.Second):
			t.Fatal("preferred preparation did not unwind after releasing persistence")
		}
	}
	require.NoError(t, result.err)
	require.NotNil(t, result.prepared)
	preparedOwner := result.prepared
	t.Cleanup(func() {
		budget.allowSmallReservations(true)
		preparedOwner.Release()
	})

	waitCtx, cancelWait := context.WithTimeout(t.Context(), 5*time.Second)
	waitErr := result.prepared.Wait(waitCtx)
	cancelWait()
	if waitErr != nil && !preparationStalled {
		preparationStalled = true
		budget.allowSmallReservations(true)
		recoveryCtx, cancelRecovery := context.WithTimeout(t.Context(), 5*time.Second)
		recoveryErr := result.prepared.Wait(recoveryCtx)
		cancelRecovery()
		require.NoError(t, recoveryErr)
	}
	if preparationStalled {
		result.prepared.Release()
		t.Fatalf("preferred preparation remained dependent on persistence's blocked state-tree reservation")
	}
	require.NoError(t, waitErr)
	require.Equal(t, preferredBytes, account.Snapshot().Used)

	budget.allowSmallReservations(false)
	select {
	case <-budget.sharedDenied:
	case <-time.After(5 * time.Second):
		budget.allowSmallReservations(true)
		result.prepared.Release()
		t.Fatal("persistence did not observe shared-capacity denial before preferred release")
	}
	result.prepared.Release()
	flushCtx, cancelFlush := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancelFlush()
	require.NoError(t, svc.flushPersists(flushCtx))

	persisted, err := store.Fetch(t.Context(), nodestore.Hash256(headerValue.Hash))
	require.NoError(t, err)
	require.NotNil(t, persisted)
	require.Positive(t, account.Snapshot().Denied)
	require.Zero(t, budget.virtualBytes())

	stopped := make(chan struct{})
	go func() {
		svc.Stop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		budget.allowSmallReservations(true)
		t.Fatal("service shutdown did not complete after preferred preparation")
	}
	persistentSnapshot := pool.Snapshot()
	require.Zero(t, persistentSnapshot.Used)
	require.Zero(t, persistentSnapshot.InvalidReleases)
}
