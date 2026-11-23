package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/service/svcerr"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/shamap/backend"
	"github.com/LeJamon/go-xrpl/storage/nodestore"
	"github.com/LeJamon/go-xrpl/storage/relationaldb"
	"github.com/stretchr/testify/require"
)

var errInjectedPersist = errors.New("injected persistence failure")

// injectedFailures counts the remaining injected failures per ledger sequence.
type injectedFailures struct {
	mu        sync.Mutex
	remaining map[uint32]int
	attempted map[uint32]int
}

func (f *injectedFailures) fail(seq uint32, times int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.remaining == nil {
		f.remaining = make(map[uint32]int)
	}
	f.remaining[seq] = times
}

func (f *injectedFailures) attempts(seq uint32) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.attempted[seq]
}

func (f *injectedFailures) take(seq uint32) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.attempted == nil {
		f.attempted = make(map[uint32]int)
	}
	f.attempted[seq]++
	if f.remaining[seq] == 0 {
		return false
	}
	f.remaining[seq]--
	return true
}

type failingRepositories struct {
	relationaldb.RepositoryManager
	injectedFailures
}

func (r *failingRepositories) PersistValidatedLedger(ctx context.Context, validated relationaldb.ValidatedLedger) error {
	if r.take(uint32(validated.Ledger.Sequence)) {
		return errInjectedPersist
	}
	return r.RepositoryManager.PersistValidatedLedger(ctx, validated)
}

// failingHeaderStore fails the node-store write of selected ledger headers.
type failingHeaderStore struct {
	nodestore.Database
	injectedFailures
}

func (d *failingHeaderStore) Store(ctx context.Context, node *nodestore.Node) error {
	if node.Type == nodestore.NodeLedger && node.Hash != validatedTipKey && d.take(node.LedgerSeq) {
		return errInjectedPersist
	}
	return d.Database.Store(ctx, node)
}

func startPersistedTestService(t *testing.T, db nodestore.Database, relational relationaldb.RepositoryManager) *Service {
	t.Helper()
	cfg := DefaultConfig()
	cfg.NodeStore = db
	cfg.SHAMapFamily = backend.New(db)
	cfg.RelationalDB = relational
	svc, err := New(cfg)
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)
	return svc
}

func requireLedgerIndexed(t *testing.T, repositories relationaldb.RepositoryManager, seq uint32, indexed bool) {
	t.Helper()
	info, err := repositories.Ledger().GetLedgerInfoBySeq(context.Background(), relationaldb.LedgerIndex(seq))
	if !errors.Is(err, relationaldb.ErrLedgerNotFound) {
		require.NoError(t, err)
	}
	require.Equalf(t, indexed, info != nil, "relational index for ledger %d", seq)
}

// A failed relational write leaves a hole that the next healthy validated
// persist repairs, so no ledger below the durable tip stays unindexed.
func TestRelationalPersistFailureLeavesNoUnindexedLedgerBelowTip(t *testing.T) {
	ctx := context.Background()
	db := newTestNodeStore(t, 10_000)
	repositories := newTestRepositories(t, ctx)
	relational := &failingRepositories{RepositoryManager: repositories}
	svc := startPersistedTestService(t, db, relational)

	blob, hash := startupPaymentBlob(t, "persist-gap", 1)
	payment, err := tx.ParseFromBinary(blob)
	require.NoError(t, err)
	submitted, err := svc.SubmitTransaction(payment, blob, false)
	require.NoError(t, err)
	require.True(t, submitted.Applied)

	failed := svc.GetClosedLedgerIndex() + 1
	relational.fail(failed, 1)
	seq, err := svc.AcceptLedger(ctx)
	require.NoError(t, err)
	require.Equal(t, failed, seq)
	require.False(t, svc.HasCompleteLedger(failed))
	requireLedgerIndexed(t, repositories, failed, false)

	_, err = svc.AcceptLedger(ctx)
	require.NoError(t, err)
	svc.FlushPersists()

	tip, err := db.Fetch(ctx, validatedTipKey)
	require.NoError(t, err)
	require.NotNil(t, tip)
	require.Greater(t, tip.LedgerSeq, failed)
	require.True(t, svc.HasCompleteLedger(failed))
	requireLedgerIndexed(t, repositories, failed, true)
	svc.Stop()

	cfg := DefaultConfig()
	cfg.FastLoad = true
	cfg.Standalone = false
	cfg.NodeStore = db
	cfg.SHAMapFamily = backend.New(db)
	cfg.RelationalDB = repositories
	restarted, err := New(cfg)
	require.NoError(t, err)
	require.NoError(t, restarted.Start())
	t.Cleanup(restarted.Stop)
	require.Equal(t, failed+1, restarted.GetValidatedLedgerIndex())

	transaction, err := restarted.GetTransaction(hash)
	require.NoError(t, err)
	require.Equal(t, failed, transaction.LedgerIndex)
	history, err := restarted.GetAccountTransactionsWithDelegate(ctx, jtx.MasterAccount().Address,
		int64(failed), int64(failed), 10, nil, false, nil)
	require.NoError(t, err)
	require.Len(t, history.Transactions, 1)
	require.Equal(t, hash, history.Transactions[0].Hash)
	require.Equal(t, blob, history.Transactions[0].TxBlob)
	require.Equal(t, failed, history.Transactions[0].LedgerIndex)
}

// A node-store failure must not leave a relational row for a ledger whose
// header is not stored; the ledger is repaired like any other hole.
func TestNodeStorePersistFailureSkipsRelationalIndex(t *testing.T) {
	ctx := context.Background()
	db := &failingHeaderStore{Database: newTestNodeStore(t, 10_000)}
	repositories := newTestRepositories(t, ctx)
	svc := startPersistedTestService(t, db, repositories)

	failed := svc.GetClosedLedgerIndex() + 1
	db.fail(failed, 1)
	_, err := svc.AcceptLedger(ctx)
	require.NoError(t, err)
	require.False(t, svc.HasCompleteLedger(failed))
	requireLedgerIndexed(t, repositories, failed, false)

	_, err = svc.AcceptLedger(ctx)
	require.NoError(t, err)
	svc.FlushPersists()
	require.True(t, svc.HasCompleteLedger(failed))
	requireLedgerIndexed(t, repositories, failed, true)
}

// Consecutive failures are counted until a validated persist succeeds, and
// holes wait for that success before they are written again.
func TestValidatedPersistFailuresCountUntilSuccess(t *testing.T) {
	ctx := context.Background()
	relational := &failingRepositories{RepositoryManager: newTestRepositories(t, ctx)}
	svc := startPersistedTestService(t, newTestNodeStore(t, 10_000), relational)

	first := svc.GetClosedLedgerIndex() + 1
	relational.fail(first, 1)
	relational.fail(first+1, 1)
	for range 2 {
		_, err := svc.AcceptLedger(ctx)
		require.NoError(t, err)
	}
	svc.FlushPersists()
	info := svc.GetServerInfo()
	require.EqualValues(t, 2, info.PersistFailures)
	require.Equal(t, first+1, info.LastFailedPersist)
	require.Equal(t, 1, relational.attempts(first), "holes wait for a healthy persist")

	_, err := svc.AcceptLedger(ctx)
	require.NoError(t, err)
	svc.FlushPersists()
	info = svc.GetServerInfo()
	require.Zero(t, info.PersistFailures)
	require.Equal(t, first+1, info.LastFailedPersist)
	require.True(t, svc.HasCompleteLedger(first))
	require.True(t, svc.HasCompleteLedger(first+1))
}

// Once a ledger's child has left memory, the validated skip list is the
// canonical proof for re-installing it as history.
func TestIngestHistoricalLedgerProvenBySkipList(t *testing.T) {
	for _, test := range []struct {
		name     string
		prepared bool
	}{
		{name: "direct"},
		{name: "prepared", prepared: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			cfg := DefaultConfig()
			cfg.LedgerCacheSize = 2
			svc, err := New(cfg)
			require.NoError(t, err)
			require.NoError(t, svc.Start())
			t.Cleanup(svc.Stop)

			parent := svc.GetClosedLedger()
			target, err := svc.AcceptLedger(ctx)
			require.NoError(t, err)
			historical, err := svc.GetLedgerBySequence(target)
			require.NoError(t, err)
			for svc.GetValidatedLedgerIndex() < target+4 {
				_, err := svc.AcceptLedger(ctx)
				require.NoError(t, err)
			}
			_, err = svc.AdoptedLedgerBySequence(target + 1)
			require.ErrorIs(t, err, svcerr.ErrLedgerNotFound)

			forkTime := historical.CloseTime().Add(time.Hour)
			fork, err := ledger.NewOpen(parent, forkTime)
			require.NoError(t, err)
			require.NoError(t, fork.Close(forkTime, 0))
			require.NotEqual(t, historical.Hash(), fork.Hash())
			require.ErrorContains(t, ingestHistoricalLedgerCopy(ctx, svc, fork, test.prepared), "current validated chain")

			require.NoError(t, ingestHistoricalLedgerCopy(ctx, svc, historical, test.prepared))
			held, err := svc.AdoptedLedgerBySequence(target)
			require.NoError(t, err)
			require.Equal(t, historical.Hash(), held.Hash())
		})
	}
}

func ingestHistoricalLedgerCopy(ctx context.Context, svc *Service, l *ledger.Ledger, prepared bool) error {
	h := l.Header()
	stateMap, err := l.StateMapSnapshot()
	if err != nil {
		return err
	}
	txMap, err := l.TxMapSnapshot()
	if err != nil {
		return err
	}
	if !prepared {
		return svc.IngestHistoricalLedgerWithState(ctx, &h, stateMap, txMap)
	}
	acquired, err := svc.PrepareAcquiredLedger(ctx, &h, stateMap, txMap)
	if err != nil {
		return err
	}
	defer acquired.Release()
	return svc.IngestHistoricalPreparedLedger(ctx, acquired)
}

func TestIngestHistoricalRejectsResidentCandidateOutsideValidatedTip(t *testing.T) {
	for _, test := range []struct {
		name     string
		prepared bool
	}{
		{name: "direct"},
		{name: "prepared", prepared: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			svc, err := New(DefaultConfig())
			require.NoError(t, err)
			require.NoError(t, svc.Start())
			t.Cleanup(svc.Stop)
			for range 3 {
				_, err := svc.AcceptLedger(ctx)
				require.NoError(t, err)
			}

			tip := svc.GetValidatedLedger()
			require.NotNil(t, tip)
			targetSeq := tip.Sequence() - 2
			h, stateMap, txMap := acquiredLedgerFixture(t, targetSeq, 0xE1)
			canonical, err := svc.GetLedgerBySequence(targetSeq)
			require.NoError(t, err)
			require.NotEqual(t, canonical.Hash(), h.Hash)
			resident, err := ledger.NewFromHeader(*h, stateMap, txMap, drops.Fees{})
			require.NoError(t, err)
			require.False(t, resident.IsValidated(), "a preferred resident may be unvalidated")

			svc.mu.Lock()
			svc.historyComponent.mu.Lock()
			svc.historyComponent.deleteHistoryLocked(targetSeq + 1)
			svc.historyComponent.putHistoryLocked(resident)
			svc.historyComponent.mu.Unlock()
			svc.mu.Unlock()

			require.ErrorContains(
				ingestHistoricalLedgerCopy(ctx, svc, resident, test.prepared),
				"current validated chain",
			)
			held, err := svc.AdoptedLedgerBySequence(targetSeq)
			require.NoError(t, err)
			require.Equal(t, resident.Hash(), held.Hash())
			require.False(t, held.IsValidated(), "contradictory resident must not be promoted")
		})
	}
}

func TestHistoricalProofRejectsInterveningValidatedTipChange(t *testing.T) {
	ctx := context.Background()
	svc, err := New(DefaultConfig())
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)
	for range 3 {
		_, err := svc.AcceptLedger(ctx)
		require.NoError(t, err)
	}

	tip := svc.GetValidatedLedger()
	require.NotNil(t, tip)
	target, err := svc.GetLedgerBySequence(tip.Sequence() - 2)
	require.NoError(t, err)
	h := target.Header()
	stateMap, err := target.StateMapSnapshot()
	require.NoError(t, err)
	txMap, err := target.TxMapSnapshot()
	require.NoError(t, err)
	proof := svc.skipListProvesHistory(ctx, &h)
	require.True(t, proof.found)
	require.Equal(t, target.Hash(), proof.hash)
	require.Same(t, tip, proof.tip)

	replacement := makeStubLedger(t, tip.Sequence(), [32]byte{0xE2}, tip.ParentHash())
	svc.mu.Lock()
	svc.historyComponent.mu.Lock()
	svc.historyComponent.deleteHistoryLocked(target.Sequence())
	svc.historyComponent.deleteHistoryLocked(target.Sequence() + 1)
	svc.validatedLedger = replacement
	err = svc.ingestHistoricalLedgerWithStateLocked(ctx, &h, stateMap, txMap, proof)
	svc.historyComponent.mu.Unlock()
	svc.mu.Unlock()

	require.ErrorContains(t, err, "current validated chain")
	_, err = svc.AdoptedLedgerBySequence(target.Sequence())
	require.ErrorIs(t, err, svcerr.ErrLedgerNotFound)
}
