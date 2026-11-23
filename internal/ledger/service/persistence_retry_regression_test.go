package service

import (
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger/service/svcerr"
	"github.com/LeJamon/go-xrpl/shamap/backend"
	"github.com/LeJamon/go-xrpl/storage/relationaldb"
	"github.com/stretchr/testify/require"
)

func newRetryTestService(t *testing.T, standalone bool) (*Service, *failingRepositories, *gatedValidatedRepository) {
	t.Helper()
	db := newTestNodeStore(t, 10_000)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	gate := newGatedValidatedRepository(newTestRepositories(t, t.Context()))
	repos := &failingRepositories{RepositoryManager: gate}
	cfg := DefaultConfig()
	cfg.LedgerCacheSize = 1
	cfg.Standalone = standalone
	cfg.NodeStore = db
	cfg.SHAMapFamily = backend.New(db)
	cfg.RelationalDB = repos
	svc, err := New(cfg)
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)
	t.Cleanup(gate.releaseGate)
	return svc, repos, gate
}

func TestValidatedPersistRetryAtFloorWithSingleEntryCache(t *testing.T) {
	svc, repos, _ := newRetryTestService(t, true)
	failed := svc.GetClosedLedgerIndex() + 1
	repos.fail(failed, 1)
	_, err := svc.AcceptLedger(t.Context())
	require.NoError(t, err)
	requireLedgerIndexed(t, repos, failed, false)
	svc.AdvanceMinimumOnlineFloor(failed)
	_, err = svc.AcceptLedger(t.Context())
	require.NoError(t, err)
	svc.FlushPersists()
	require.Equal(t, 2, repos.attempts(failed))
	require.True(t, svc.HasCompleteLedger(failed))
	requireLedgerIndexed(t, repos, failed, true)
}

func TestAsyncValidatedPersistRetryAfterLastNetworkClose(t *testing.T) {
	svc, repos, gate := newRetryTestService(t, false)
	closeValidated := func() uint32 {
		t.Helper()
		parent := svc.GetClosedLedger()
		seq, err := svc.AcceptConsensusResult(t.Context(), parent, nil, nil, parent.CloseTime().Add(time.Second), true)
		require.NoError(t, err)
		svc.SetValidatedLedger(seq, svc.GetClosedLedger().Hash())
		return seq
	}
	failed := svc.GetClosedLedgerIndex() + 1
	repos.fail(failed, 1)
	require.Equal(t, failed, closeValidated())
	svc.FlushPersists()
	requireLedgerIndexed(t, repos, failed, false)
	gate.sequence.Store(failed + 1)
	gate.enable()
	require.Equal(t, failed+1, closeValidated())
	waitRetrySignal(t, gate.entered)
	flushed := make(chan struct{})
	go func() {
		svc.FlushPersists()
		close(flushed)
	}()
	require.Eventually(t, func() bool { return persistenceBarrierCount(svc) > 0 }, 5*time.Second, time.Millisecond)
	gate.releaseGate()
	waitRetrySignal(t, flushed)
	require.Equal(t, 2, repos.attempts(failed))
	requireLedgerIndexed(t, repos, failed, true)
}

func TestAdvanceMinimumOnlineFloorWaitsForInFlightValidatedRetry(t *testing.T) {
	svc, repos, gate := newRetryTestService(t, true)
	failed := svc.GetClosedLedgerIndex() + 1
	repos.fail(failed, 1)
	_, err := svc.AcceptLedger(t.Context())
	require.NoError(t, err)
	historical := svc.GetValidatedLedger()
	gate.sequence.Store(failed)
	gate.enable()
	_, err = svc.AcceptLedger(t.Context())
	require.NoError(t, err)
	waitRetrySignal(t, gate.entered)
	advanced := make(chan struct{})
	go func() {
		svc.AdvanceMinimumOnlineFloor(failed + 1)
		close(advanced)
	}()
	select {
	case <-advanced:
		t.Fatal("retention advanced while the old ledger was being written")
	case <-time.After(50 * time.Millisecond):
	}
	gate.releaseGate()
	waitRetrySignal(t, advanced)
	require.NoError(t, relationaldb.NewLedgerPruner(repos, 100).DeleteLedgersBefore(t.Context(), failed+1))
	svc.enqueueValidatedHistoryPersist(historical)
	svc.FlushPersists()
	require.Equal(t, 2, repos.attempts(failed))
	require.ErrorIs(t, svc.RepairLedgerTransactions(t.Context(), failed), svcerr.ErrLedgerNotFound)
	requireLedgerIndexed(t, repos, failed, false)
}

func waitRetrySignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for persistence")
	}
}
