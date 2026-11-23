package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/shamap/backend"
	"github.com/LeJamon/go-xrpl/storage/relationaldb"
	"github.com/stretchr/testify/require"
)

var errRetryTestPersist = errors.New("retry test persistence failure")

type retryTestRepository struct {
	relationaldb.RepositoryManager

	mu           sync.Mutex
	failSeq      uint32
	blockSeq     uint32
	blockRetries bool
	attempts     map[uint32]int

	failedOnce         sync.Once
	enteredOnce        sync.Once
	releaseOnce        sync.Once
	retryCompletedOnce sync.Once
	failed             chan struct{}
	entered            chan struct{}
	release            chan struct{}
	retryCompleted     chan struct{}
}

func newRetryTestRepository(manager relationaldb.RepositoryManager) *retryTestRepository {
	return &retryTestRepository{
		RepositoryManager: manager,
		attempts:          make(map[uint32]int),
		failed:            make(chan struct{}),
		entered:           make(chan struct{}),
		release:           make(chan struct{}),
		retryCompleted:    make(chan struct{}),
	}
}

func (r *retryTestRepository) configure(failSeq, blockSeq uint32, blockRetries bool) {
	r.mu.Lock()
	r.failSeq = failSeq
	r.blockSeq = blockSeq
	r.blockRetries = blockRetries
	r.mu.Unlock()
}

func (r *retryTestRepository) PersistValidatedLedger(ctx context.Context, value relationaldb.ValidatedLedger) error {
	seq := uint32(value.Ledger.Sequence)
	r.mu.Lock()
	r.attempts[seq]++
	attempt := r.attempts[seq]
	failSeq := r.failSeq
	blockSeq := r.blockSeq
	blockRetries := r.blockRetries
	r.mu.Unlock()

	if seq == failSeq && attempt == 1 {
		r.failedOnce.Do(func() { close(r.failed) })
		return errRetryTestPersist
	}

	blocked := seq == blockSeq && ((!blockRetries && attempt == 1) || (blockRetries && attempt >= 2))
	if blocked {
		r.enteredOnce.Do(func() { close(r.entered) })
		select {
		case <-r.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	err := r.RepositoryManager.PersistValidatedLedger(ctx, value)
	if blocked && blockRetries {
		r.retryCompletedOnce.Do(func() { close(r.retryCompleted) })
	}
	return err
}

func (r *retryTestRepository) releaseGate() {
	r.releaseOnce.Do(func() { close(r.release) })
}

func (r *retryTestRepository) attemptsFor(seq uint32) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.attempts[seq]
}

func newRetryTestService(t *testing.T, cacheSize uint32, standalone bool) (*Service, *retryTestRepository) {
	t.Helper()
	db := newTestNodeStore(t, 10_000)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	manager := newTestRepositories(t, t.Context())
	repositories := newRetryTestRepository(manager)

	cfg := DefaultConfig()
	cfg.LedgerCacheSize = cacheSize
	cfg.Standalone = standalone
	cfg.NodeStore = db
	cfg.SHAMapFamily = backend.New(db)
	cfg.RelationalDB = repositories
	svc, err := New(cfg)
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(func() {
		repositories.releaseGate()
		svc.Stop()
	})
	return svc, repositories
}

func requireRetryTestSignal(t *testing.T, signal <-chan struct{}, message string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(5 * time.Second):
		t.Fatal(message)
	}
}

func TestValidatedPersistRetryAtFloorWithSingleEntryCache(t *testing.T) {
	svc, repositories := newRetryTestService(t, 1, true)
	failed := svc.GetClosedLedgerIndex() + 1
	repositories.configure(failed, 0, false)

	seq, err := svc.AcceptLedger(t.Context())
	require.NoError(t, err)
	require.Equal(t, failed, seq)
	requireRetryTestSignal(t, repositories.failed, "validated persistence did not fail")
	require.Eventually(t, func() bool {
		consecutive, lastFailed := svc.ValidatedPersistFailures()
		return consecutive == 1 && lastFailed == failed
	}, 5*time.Second, time.Millisecond)

	// Keep the failed ledger exactly at the retention floor while its child is persisted.
	svc.AdvanceMinimumOnlineFloor(failed)
	_, err = svc.AcceptLedger(t.Context())
	require.NoError(t, err)
	svc.FlushPersists()

	require.Eventually(t, func() bool {
		return repositories.attemptsFor(failed) >= 2 && svc.HasCompleteLedger(failed)
	}, 5*time.Second, time.Millisecond)
	requireLedgerIndexed(t, repositories.RepositoryManager, failed, true)
}

func TestAsyncValidatedPersistRetryAfterLastNetworkClose(t *testing.T) {
	svc, repositories := newRetryTestService(t, 10, false)
	failed := svc.GetClosedLedgerIndex() + 1
	repositories.configure(failed, failed+1, false)

	parent := svc.GetClosedLedger()
	seq, err := svc.AcceptConsensusResult(
		t.Context(), parent, nil, nil, parent.CloseTime().Add(time.Second), true,
	)
	require.NoError(t, err)
	require.Equal(t, failed, seq)
	closed := svc.GetClosedLedger()
	require.Equal(t, failed, closed.Sequence())
	svc.SetValidatedLedger(seq, closed.Hash())
	requireRetryTestSignal(t, repositories.failed, "network validation did not fail persistence")
	require.Eventually(t, func() bool {
		consecutive, lastFailed := svc.ValidatedPersistFailures()
		return consecutive == 1 && lastFailed == failed
	}, 5*time.Second, time.Millisecond)

	lastParent := svc.GetClosedLedger()
	lastSeq, err := svc.AcceptConsensusResult(
		t.Context(), lastParent, nil, nil, lastParent.CloseTime().Add(time.Second), true,
	)
	require.NoError(t, err)
	require.Equal(t, failed+1, lastSeq)
	lastClosed := svc.GetClosedLedger()
	svc.SetValidatedLedger(lastSeq, lastClosed.Hash())
	requireRetryTestSignal(t, repositories.entered, "last validated persistence did not reach the gate")

	barrierDone := make(chan struct{})
	go func() {
		svc.FlushPersists()
		close(barrierDone)
	}()
	select {
	case <-barrierDone:
		t.Fatal("persistence barrier returned before the last validated write was released")
	case <-time.After(50 * time.Millisecond):
	}
	repositories.releaseGate()
	select {
	case <-barrierDone:
	case <-time.After(5 * time.Second):
		t.Fatal("persistence barrier did not drain the last validated write")
	}

	require.Eventually(t, func() bool { return repositories.attemptsFor(failed) >= 2 }, 5*time.Second, time.Millisecond)
	requireLedgerIndexed(t, repositories.RepositoryManager, failed, true)
}

func TestAdvanceMinimumOnlineFloorWaitsForInFlightValidatedRetry(t *testing.T) {
	svc, repositories := newRetryTestService(t, 10, true)
	failed := svc.GetClosedLedgerIndex() + 1
	repositories.configure(failed, failed, true)

	_, err := svc.AcceptLedger(t.Context())
	require.NoError(t, err)
	requireRetryTestSignal(t, repositories.failed, "validated persistence did not fail")
	_, err = svc.AcceptLedger(t.Context())
	require.NoError(t, err)
	requireRetryTestSignal(t, repositories.entered, "failed validated ledger was not retried")

	floorAdvanced := make(chan struct{})
	go func() {
		svc.AdvanceMinimumOnlineFloor(failed + 1)
		close(floorAdvanced)
	}()
	select {
	case <-floorAdvanced:
		t.Fatal("minimum online floor advanced before the in-flight retry completed")
	case <-time.After(50 * time.Millisecond):
	}

	repositories.releaseGate()
	requireRetryTestSignal(t, repositories.retryCompleted, "in-flight validated retry did not finish")
	select {
	case <-floorAdvanced:
	case <-time.After(5 * time.Second):
		t.Fatal("minimum online floor did not advance after the retry completed")
	}

	svc.FlushPersists()
	require.Equal(t, 2, repositories.attemptsFor(failed), "floor advancement scheduled a stale retry")
}
