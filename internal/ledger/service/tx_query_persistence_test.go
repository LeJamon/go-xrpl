package service

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/storage/relationaldb"
	sqlitedb "github.com/LeJamon/go-xrpl/storage/relationaldb/sqlite"
	"github.com/stretchr/testify/require"
)

type gatedValidatedRepository struct {
	relationaldb.RepositoryManager

	enabled     atomic.Bool
	enteredOnce sync.Once
	releaseOnce sync.Once
	entered     chan struct{}
	release     chan struct{}
}

func newGatedValidatedRepository(manager relationaldb.RepositoryManager) *gatedValidatedRepository {
	return &gatedValidatedRepository{
		RepositoryManager: manager,
		entered:           make(chan struct{}),
		release:           make(chan struct{}),
	}
}

func (m *gatedValidatedRepository) PersistValidatedLedger(ctx context.Context, value relationaldb.ValidatedLedger) error {
	if m.enabled.Load() {
		m.enteredOnce.Do(func() { close(m.entered) })
		select {
		case <-m.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return m.RepositoryManager.PersistValidatedLedger(ctx, value)
}

func (m *gatedValidatedRepository) enable() {
	m.enabled.Store(true)
}

func (m *gatedValidatedRepository) releaseGate() {
	m.releaseOnce.Do(func() { close(m.release) })
}

func newPersistenceQueryService(t *testing.T) (*Service, *gatedValidatedRepository) {
	t.Helper()
	manager, err := sqlitedb.NewRepositoryManager(t.Context(), t.TempDir(), sqlitedb.Settings{})
	require.NoError(t, err)
	gated := newGatedValidatedRepository(manager)
	t.Cleanup(func() { require.NoError(t, manager.Close()) })

	cfg := DefaultConfig()
	cfg.RelationalDB = gated
	svc, err := New(cfg)
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(func() {
		gated.releaseGate()
		svc.Stop()
	})
	svc.FlushPersists()
	gated.enable()
	return svc, gated
}

func acceptAndValidatePersistenceTestPayment(t *testing.T, svc *Service, gated *gatedValidatedRepository, destination string) (uint32, [32]byte) {
	t.Helper()
	parent := svc.GetClosedLedger()
	blob, txHash := startupPaymentBlob(t, destination, 1)
	seq, err := svc.AcceptConsensusResult(
		t.Context(),
		parent,
		[][]byte{blob},
		nil,
		parent.CloseTime().Add(time.Second),
		true,
	)
	require.NoError(t, err)
	closed := svc.GetClosedLedger()
	require.Equal(t, seq, closed.Sequence())
	svc.SetValidatedLedger(seq, closed.Hash())
	select {
	case <-gated.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("validated-ledger persistence did not reach the SQLite gate")
	}
	return seq, txHash
}

func persistenceBarrierCount(svc *Service) int {
	svc.persistMu.Lock()
	defer svc.persistMu.Unlock()
	count := 0
	for _, job := range svc.persistQueue {
		if job != nil && job.done != nil {
			count++
		}
	}
	return count
}

func TestGetTransactionWaitsForValidatedPersistence(t *testing.T) {
	svc, gated := newPersistenceQueryService(t)
	seq, txHash := acceptAndValidatePersistenceTestPayment(t, svc, gated, "tx-query-persistence")

	type result struct {
		value    *TransactionResult
		searched relationaldb.TxSearchResult
		err      error
	}
	plain := make(chan result, 1)
	ranged := make(chan result, 1)
	go func() {
		value, err := svc.GetTransaction(txHash)
		plain <- result{value: value, err: err}
	}()
	go func() {
		value, searched, err := svc.GetTransactionWithRange(t.Context(), txHash, 1, seq)
		ranged <- result{value: value, searched: searched, err: err}
	}()
	require.Eventually(t, func() bool { return persistenceBarrierCount(svc) >= 2 }, 5*time.Second, time.Millisecond)

	select {
	case <-plain:
		t.Fatal("plain lookup returned before validated persistence was released")
	case <-ranged:
		t.Fatal("ranged lookup returned before validated persistence was released")
	default:
	}
	gated.releaseGate()

	select {
	case got := <-plain:
		require.NoError(t, got.err)
		require.NotNil(t, got.value)
		require.True(t, got.value.Validated)
		require.Equal(t, seq, got.value.LedgerIndex)
	case <-time.After(5 * time.Second):
		t.Fatal("plain lookup did not complete after validated persistence")
	}
	select {
	case got := <-ranged:
		require.NoError(t, got.err)
		require.NotNil(t, got.value)
		require.True(t, got.value.Validated)
		require.Equal(t, seq, got.value.LedgerIndex)
		require.Equal(t, relationaldb.TxSearchAll, got.searched)
	case <-time.After(5 * time.Second):
		t.Fatal("ranged lookup did not complete after validated persistence")
	}
}

func TestGetTransactionWithRangeCancellationDuringValidatedPersistence(t *testing.T) {
	svc, gated := newPersistenceQueryService(t)
	seq, txHash := acceptAndValidatePersistenceTestPayment(t, svc, gated, "tx-query-persistence-cancel")

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	type result struct {
		value    *TransactionResult
		searched relationaldb.TxSearchResult
		err      error
	}
	resultCh := make(chan result, 1)
	go func() {
		value, searched, err := svc.GetTransactionWithRange(ctx, txHash, 1, seq)
		resultCh <- result{value: value, searched: searched, err: err}
	}()
	require.Eventually(t, func() bool { return persistenceBarrierCount(svc) >= 1 }, 5*time.Second, time.Millisecond)
	cancel()

	select {
	case got := <-resultCh:
		require.Nil(t, got.value)
		require.ErrorIs(t, got.err, context.Canceled)
	case <-time.After(5 * time.Second):
		t.Fatal("ranged lookup did not honor cancellation while waiting for persistence")
	}
	gated.releaseGate()
}
