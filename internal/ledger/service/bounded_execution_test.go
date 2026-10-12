package service

import (
	"context"
	"encoding/hex"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/flatstate"
	"github.com/LeJamon/go-xrpl/internal/ledger/genesis"
	"github.com/LeJamon/go-xrpl/internal/ledger/overlay"
	"github.com/LeJamon/go-xrpl/internal/membudget"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/LeJamon/go-xrpl/shamap/backend"
	kvpebble "github.com/LeJamon/go-xrpl/storage/kvstore/pebble"
	"github.com/LeJamon/go-xrpl/storage/nodestore"
	"github.com/LeJamon/go-xrpl/storage/relationaldb"
	sqlitedb "github.com/LeJamon/go-xrpl/storage/relationaldb/sqlite"
	"github.com/cockroachdb/pebble"
	"github.com/stretchr/testify/require"
)

var errBoundedPublication = errors.New("bounded test publication failure")

type boundedTestPaths struct {
	nodes string
	flat  string
	sql   string
}

type boundedTestRuntime struct {
	service    *Service
	execution  *ledger.ExecutionStore
	flat       *flatstate.Store
	repository relationaldb.RepositoryManager
	database   interface{ Close() error }
	workspace  *membudget.Account
}

type boundedFailingRepository struct {
	relationaldb.RepositoryManager
	failPublication atomic.Bool
	failAfterCommit atomic.Bool
}

func (r *boundedFailingRepository) PersistLedgerStream(
	ctx context.Context,
	ledger relationaldb.LedgerInfo,
	commitID [32]byte,
	publicationKind string,
	next relationaldb.IndexedTransactionStream,
) error {
	if r.failPublication.Load() {
		return errBoundedPublication
	}
	publisher, ok := r.RepositoryManager.(relationaldb.StreamingLedgerPublisher)
	if !ok {
		return errors.New("wrapped repository does not support streaming publication")
	}
	if err := publisher.PersistLedgerStream(ctx, ledger, commitID, publicationKind, next); err != nil {
		return err
	}
	if r.failAfterCommit.Load() {
		return errBoundedPublication
	}
	return nil
}

func newBoundedTestPaths(t *testing.T) boundedTestPaths {
	t.Helper()
	root := t.TempDir()
	return boundedTestPaths{
		nodes: filepath.Join(root, "nodes"),
		flat:  filepath.Join(root, "flat"),
		sql:   filepath.Join(root, "sql"),
	}
}

func newBoundedTestRepository(t *testing.T, paths boundedTestPaths) relationaldb.RepositoryManager {
	t.Helper()
	repository, err := sqlitedb.NewRepositoryManager(t.Context(), paths.sql, sqlitedb.Settings{TempStore: "file"})
	require.NoError(t, err)
	return repository
}

func newBoundedTestRuntime(
	t *testing.T,
	paths boundedTestPaths,
	repository relationaldb.RepositoryManager,
	flatOptions flatstate.Options,
) *boundedTestRuntime {
	return newBoundedTestRuntimeWithStartup(t, paths, repository, flatOptions, StartupConfig{Mode: StartupFresh})
}

func newBoundedTestRuntimeWithStartup(
	t *testing.T,
	paths boundedTestPaths,
	repository relationaldb.RepositoryManager,
	flatOptions flatstate.Options,
	startup StartupConfig,
) *boundedTestRuntime {
	t.Helper()
	pebbleStore, err := kvpebble.New(paths.nodes, kvpebble.Options{BlockCacheBytes: 4 << 20, MaxOpenFiles: 100})
	require.NoError(t, err)
	database, err := nodestore.NewKVDatabase(pebbleStore, nodestore.DatabaseConfig{})
	if err != nil {
		require.NoError(t, pebbleStore.Close())
		require.NoError(t, err)
	}
	family := backend.New(database)

	pool := membudget.New(128 << 20)
	workspace, err := pool.NewAccount("bounded-workspace", 0, 64<<20)
	require.NoError(t, err)
	overlayMemory, err := pool.NewAccount("bounded-overlay", 0, 32<<20)
	require.NoError(t, err)
	flatOptions.MemoryBudget = workspace
	flatStore, err := flatstate.Open(paths.flat, flatOptions)
	if err != nil {
		require.NoError(t, database.Close())
		require.NoError(t, err)
	}
	execution, err := ledger.NewExecutionStore(ledger.ExecutionConfig{
		Family:        family,
		Workspace:     workspace,
		MaxRootLeases: 32,
		Flat:          flatStore,
		Overlay: overlay.Config{
			MemoryBudget:   overlayMemory,
			MaxMemoryBytes: 8 << 20,
		},
	})
	if err != nil {
		require.NoError(t, flatStore.Close())
		require.NoError(t, database.Close())
		require.NoError(t, err)
	}
	service, err := New(Config{
		Standalone:        true,
		Startup:           startup,
		GenesisConfig:     genesis.DefaultConfig(),
		NodeStore:         database,
		SHAMapFamily:      family,
		RelationalDB:      repository,
		Execution:         execution,
		FlatState:         flatStore,
		PersistenceMemory: workspace,
	})
	require.NoError(t, err)
	return &boundedTestRuntime{
		service:    service,
		execution:  execution,
		flat:       flatStore,
		repository: repository,
		database:   database,
		workspace:  workspace,
	}
}

func boundedFlatOptions() flatstate.Options {
	return flatstate.Options{
		CacheBytes:      1 << 20,
		MemTableSize:    1 << 20,
		MaxActiveViews:  16,
		MaxBatchBytes:   1 << 20,
		MaxBatchRecords: 64,
		OutboxMaxCount:  16,
		OutboxMaxBytes:  1 << 20,
		OutboxMaxAge:    time.Hour,
	}
}

func (r *boundedTestRuntime) close(t testing.TB) {
	t.Helper()
	if r.service != nil {
		r.service.Stop()
	}
	if r.execution != nil {
		require.NoError(t, r.execution.Close())
	}
	if r.flat != nil {
		require.NoError(t, r.flat.Close())
	}
	if r.repository != nil {
		require.NoError(t, r.repository.Close())
	}
	if r.database != nil {
		require.NoError(t, r.database.Close())
	}
}

func TestBoundedExecutionServicePebbleSQLiteAcceptPersist(t *testing.T) {
	paths := newBoundedTestPaths(t)
	repository := newBoundedTestRepository(t, paths)
	runtime := newBoundedTestRuntime(t, paths, repository, boundedFlatOptions())
	t.Cleanup(func() { runtime.close(t) })

	require.NoError(t, runtime.service.Start())
	initial := runtime.service.GetClosedLedger()
	require.NotNil(t, initial)
	require.True(t, initial.UsesBoundedExecution())

	descriptor, found, err := runtime.flat.Current()
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, initial.Hash(), descriptor.LedgerHash)
	require.Equal(t, initial.Header().AccountHash, descriptor.StateRoot)
	require.Equal(t, initial.Header().TxHash, descriptor.TxRoot)

	_, err = runtime.service.AcceptLedger(t.Context())
	require.NoError(t, err)
	runtime.service.FlushPersists()
	closed := runtime.service.GetClosedLedger()
	require.NotNil(t, closed)
	require.True(t, closed.UsesBoundedExecution())

	descriptor, found, err = runtime.flat.Current()
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, closed.Hash(), descriptor.LedgerHash)
	require.Equal(t, closed.Header().ParentHash, descriptor.ParentHash)
	require.Equal(t, closed.Header().AccountHash, descriptor.StateRoot)
	require.Equal(t, closed.Header().TxHash, descriptor.TxRoot)

	info, err := repository.Ledger().GetLedgerInfoBySeq(t.Context(), relationaldb.LedgerIndex(closed.Sequence()))
	require.NoError(t, err)
	require.NotNil(t, info)
	require.Equal(t, relationaldb.Hash(closed.Hash()), info.Hash)
	require.Equal(t, relationaldb.Hash(closed.Header().AccountHash), info.AccountHash)
	require.Equal(t, relationaldb.Hash(closed.Header().TxHash), info.TransactionHash)
	outbox, err := runtime.flat.PendingOutbox(t.Context(), 0)
	require.NoError(t, err)
	require.Empty(t, outbox)

	require.NotEmpty(t, runtime.execution.Roots())
	runtime.service.Stop()
	require.NoError(t, runtime.execution.Close())
	require.Zero(t, runtime.workspace.Snapshot().Used)
}

func TestBoundedExecutionOutboxReplaysAfterRestart(t *testing.T) {
	paths := newBoundedTestPaths(t)
	baseRepository := newBoundedTestRepository(t, paths)
	failingRepository := &boundedFailingRepository{RepositoryManager: baseRepository}
	first := newBoundedTestRuntime(t, paths, failingRepository, boundedFlatOptions())
	runtimes := []*boundedTestRuntime{first}
	t.Cleanup(func() {
		for _, runtime := range runtimes {
			runtime.close(t)
		}
	})

	require.NoError(t, first.service.Start())
	failingRepository.failPublication.Store(true)
	_, err := first.service.AcceptLedger(t.Context())
	require.NoError(t, err)
	target := first.service.GetClosedLedger()
	require.NotNil(t, target)
	require.True(t, target.UsesBoundedExecution())

	pending, err := first.flat.PendingOutbox(t.Context(), 0)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.Equal(t, target.Hash(), pending[0].Descriptor.LedgerHash)
	require.Equal(t, flatstate.SemanticCommitID(target.Hash(), ledgerPublication), pending[0].CommitID)

	first.close(t)
	restartedRepository := newBoundedTestRepository(t, paths)
	restarted := newBoundedTestRuntime(t, paths, restartedRepository, boundedFlatOptions())
	runtimes = append(runtimes, restarted)
	require.NoError(t, restarted.service.recoverFlatState(t.Context()))
	// A second recovery pass must observe the acknowledged semantic commit and
	// remain a no-op after the restart replay has completed.
	require.NoError(t, restarted.service.recoverFlatState(t.Context()))

	pending, err = restarted.flat.PendingOutbox(t.Context(), 0)
	require.NoError(t, err)
	require.Empty(t, pending)
	info, err := restartedRepository.Ledger().GetLedgerInfoBySeq(t.Context(), relationaldb.LedgerIndex(target.Sequence()))
	require.NoError(t, err)
	require.NotNil(t, info)
	require.Equal(t, relationaldb.Hash(target.Hash()), info.Hash)

	descriptor, found, err := restarted.flat.Current()
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, target.Hash(), descriptor.LedgerHash)
	require.Equal(t, target.Header().ParentHash, descriptor.ParentHash)
}

func TestBoundedExecutionOutboxReplayAfterCommittedReceipt(t *testing.T) {
	paths := newBoundedTestPaths(t)
	baseRepository := newBoundedTestRepository(t, paths)
	failingRepository := &boundedFailingRepository{RepositoryManager: baseRepository}
	first := newBoundedTestRuntime(t, paths, failingRepository, boundedFlatOptions())
	runtimes := []*boundedTestRuntime{first}
	t.Cleanup(func() {
		for _, runtime := range runtimes {
			runtime.close(t)
		}
	})

	require.NoError(t, first.service.Start())
	// Let SQL commit the receipt, then fail the caller before it can ack the
	// flat outbox. This is the restart boundary where the in-memory header has
	// sub-second time precision but the durable NodeStore header does not.
	failingRepository.failAfterCommit.Store(true)
	_, err := first.service.AcceptLedger(t.Context())
	require.NoError(t, err)
	target := first.service.GetClosedLedger()
	require.NotNil(t, target)
	pending, err := first.flat.PendingOutbox(t.Context(), 0)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	info, err := baseRepository.Ledger().GetLedgerInfoBySeq(t.Context(), relationaldb.LedgerIndex(target.Sequence()))
	require.NoError(t, err)
	require.Equal(t, relationaldb.Hash(target.Hash()), info.Hash)
	first.close(t)

	restartedRepository := newBoundedTestRepository(t, paths)
	restarted := newBoundedTestRuntime(t, paths, restartedRepository, boundedFlatOptions())
	runtimes = append(runtimes, restarted)
	require.NoError(t, restarted.service.recoverFlatState(t.Context()))
	require.NoError(t, restarted.service.recoverFlatState(t.Context()))
	pending, err = restarted.flat.PendingOutbox(t.Context(), 0)
	require.NoError(t, err)
	require.Empty(t, pending)
}

func TestBoundedExecutionSQLFailureRetainsAdmissionUntilRetry(t *testing.T) {
	paths := newBoundedTestPaths(t)
	baseRepository := newBoundedTestRepository(t, paths)
	failingRepository := &boundedFailingRepository{RepositoryManager: baseRepository}
	runtime := newBoundedTestRuntime(t, paths, failingRepository, boundedFlatOptions())
	t.Cleanup(func() { runtime.close(t) })
	require.NoError(t, runtime.service.Start())
	failingRepository.failPublication.Store(true)

	for range persistenceAdmissionLimit {
		_, err := runtime.service.AcceptLedger(t.Context())
		require.NoError(t, err)
	}
	require.Eventually(t, func() bool {
		runtime.service.persistMu.Lock()
		pending := len(runtime.service.persistQueue)
		if runtime.service.persistActive != nil {
			pending++
		}
		runtime.service.persistMu.Unlock()
		return pending >= persistenceAdmissionLimit
	}, 2*time.Second, 10*time.Millisecond)

	blockedContext, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	_, err := runtime.service.AcceptLedger(blockedContext)
	require.ErrorIs(t, err, context.DeadlineExceeded)

	failingRepository.failPublication.Store(false)
	require.Eventually(t, func() bool {
		pending, pendingErr := runtime.flat.PendingOutbox(t.Context(), 0)
		if pendingErr != nil || len(pending) != 0 {
			return false
		}
		runtime.service.persistMu.Lock()
		idle := runtime.service.persistActive == nil && len(runtime.service.persistQueue) == 0
		runtime.service.persistMu.Unlock()
		return idle
	}, 5*time.Second, 20*time.Millisecond)

	acceptContext, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	_, err = runtime.service.AcceptLedger(acceptContext)
	require.NoError(t, err)
	pending, err := runtime.flat.PendingOutbox(t.Context(), 0)
	require.NoError(t, err)
	require.Empty(t, pending)
}

func TestBoundedExecutionInterruptedMarkerResumesFromDurableRoots(t *testing.T) {
	paths := newBoundedTestPaths(t)
	firstRepository := newBoundedTestRepository(t, paths)
	first := newBoundedTestRuntime(t, paths, firstRepository, boundedFlatOptions())
	runtimes := []*boundedTestRuntime{first}
	t.Cleanup(func() {
		for _, runtime := range runtimes {
			runtime.close(t)
		}
	})

	require.NoError(t, first.service.Start())
	_, err := first.service.AcceptLedger(t.Context())
	require.NoError(t, err)
	parent := first.service.GetClosedLedger()
	require.NotNil(t, parent)
	parentHash := parent.Hash()
	parentID := hex.EncodeToString(parentHash[:])
	first.close(t)

	// Build the next durable ledger with flat publication disabled. The flat
	// base remains at parentHash while the NodeStore already contains the
	// target roots needed by recovery.
	secondRepository := newBoundedTestRepository(t, paths)
	second := newBoundedTestRuntimeWithStartup(
		t,
		paths,
		secondRepository,
		boundedFlatOptions(),
		StartupConfig{Mode: StartupLoad, Ledger: parentID},
	)
	runtimes = append(runtimes, second)
	require.NoError(t, second.service.Start())
	second.service.config.FlatState = nil
	second.service.config.RelationalDB = nil
	second.service.relationalDB = nil
	_, err = second.service.AcceptLedger(t.Context())
	require.NoError(t, err)
	target := second.service.GetClosedLedger()
	require.NotNil(t, target)
	targetHash := target.Hash()
	second.close(t)

	var syncCalls atomic.Int32
	flatOptions := boundedFlatOptions()
	flatOptions.Sync = func(*pebble.DB) error {
		if syncCalls.Add(1) == 2 {
			return errBoundedPublication
		}
		return nil
	}
	thirdRepository := newBoundedTestRepository(t, paths)
	third := newBoundedTestRuntime(t, paths, thirdRepository, flatOptions)
	runtimes = append(runtimes, third)
	loaded, err := third.service.loadStoredLedgerByHash(t.Context(), targetHash)
	require.NoError(t, err)
	require.NotNil(t, loaded)
	err = third.service.advanceFlatState(t.Context(), loaded)
	require.Error(t, err)
	marker, advancing, err := third.flat.Marker()
	require.NoError(t, err)
	require.True(t, advancing)
	require.Equal(t, targetHash, marker.Target.LedgerHash)
	_, found, err := third.flat.Current()
	require.ErrorIs(t, err, flatstate.ErrUnavailable)
	require.False(t, found)
	third.close(t)

	fourthRepository := newBoundedTestRepository(t, paths)
	fourth := newBoundedTestRuntime(t, paths, fourthRepository, boundedFlatOptions())
	runtimes = append(runtimes, fourth)
	require.NoError(t, fourth.service.recoverFlatState(t.Context()))
	marker, advancing, err = fourth.flat.Marker()
	require.NoError(t, err)
	require.False(t, advancing)
	descriptor, found, err := fourth.flat.Current()
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, targetHash, descriptor.LedgerHash)
	pending, err := fourth.flat.PendingOutbox(t.Context(), 0)
	require.NoError(t, err)
	require.Empty(t, pending)
	info, err := fourthRepository.Ledger().GetLedgerInfoBySeq(t.Context(), relationaldb.LedgerIndex(target.Sequence()))
	require.NoError(t, err)
	require.NotNil(t, info)
	require.Equal(t, relationaldb.Hash(targetHash), info.Hash)
}

func TestBoundedExecutionPebbleMixedBranchesKeepIndependentRoots(t *testing.T) {
	paths := newBoundedTestPaths(t)
	repository := newBoundedTestRepository(t, paths)
	runtime := newBoundedTestRuntime(t, paths, repository, boundedFlatOptions())
	t.Cleanup(func() { runtime.close(t) })
	require.NoError(t, runtime.service.Start())

	parent := runtime.service.GetClosedLedger()
	require.NotNil(t, parent)
	branchA, err := ledger.NewOpen(parent, time.Unix(1_700_000_100, 0))
	require.NoError(t, err)
	defer branchA.Discard()
	branchB, err := ledger.NewOpen(parent, time.Unix(1_700_000_101, 0))
	require.NoError(t, err)
	defer branchB.Discard()
	require.NoError(t, branchA.AddTransactionWithMeta([32]byte{0xa1}, []byte("branch-a-metadata")))
	require.NoError(t, branchB.AddTransactionWithMeta([32]byte{0xb2}, []byte("branch-b-metadata")))
	require.NoError(t, branchA.Close(time.Unix(1_700_000_102, 0), 0))
	require.NoError(t, branchB.Close(time.Unix(1_700_000_103, 0), 0))
	require.NotEqual(t, branchA.Hash(), branchB.Hash())

	snapshotA, err := branchA.Snapshot()
	require.NoError(t, err)
	defer snapshotA.Discard()
	snapshotB, err := branchB.Snapshot()
	require.NoError(t, err)
	defer snapshotB.Discard()
	require.NotEqual(t, snapshotA.Hash(), snapshotB.Hash())
	seenA := false
	require.NoError(t, snapshotA.ForEachTransaction(func(hash [32]byte, _ []byte) bool {
		seenA = seenA || hash == [32]byte{0xa1}
		return true
	}))
	seenB := false
	require.NoError(t, snapshotB.ForEachTransaction(func(hash [32]byte, _ []byte) bool {
		seenB = seenB || hash == [32]byte{0xb2}
		return true
	}))
	require.True(t, seenA)
	require.True(t, seenB)
	snapshotA.Discard()
	snapshotB.Discard()
	branchA.Discard()
	branchB.Discard()
	runtime.service.Stop()
	require.NoError(t, runtime.execution.Close())
}

func newBoundedRetentionRuntime(
	t *testing.T,
) (*boundedTestRuntime, *nodestore.RotatingKVDatabase, *backend.NodeStore) {
	t.Helper()
	store, err := kvpebble.NewRotating(
		filepath.Join(t.TempDir(), "rotating-nodes"),
		kvpebble.Options{BlockCacheBytes: 4 << 20, MaxOpenFiles: 200},
	)
	require.NoError(t, err)
	database, err := nodestore.NewRotatingKVDatabase(store, nodestore.DatabaseConfig{})
	require.NoError(t, err)
	family := backend.New(database)

	pool := membudget.New(128 << 20)
	workspace, err := pool.NewAccount("bounded-retention-workspace", 0, 64<<20)
	require.NoError(t, err)
	overlayMemory, err := pool.NewAccount("bounded-retention-overlay", 0, 32<<20)
	require.NoError(t, err)
	flatStore, err := flatstate.Open(filepath.Join(t.TempDir(), "flat"), flatstate.Options{
		CacheBytes:      1 << 20,
		MemTableSize:    1 << 20,
		MaxActiveViews:  16,
		MaxBatchBytes:   1 << 20,
		MaxBatchRecords: 64,
		OutboxMaxCount:  16,
		OutboxMaxBytes:  1 << 20,
		OutboxMaxAge:    time.Hour,
		MemoryBudget:    workspace,
	})
	require.NoError(t, err)
	execution, err := ledger.NewExecutionStore(ledger.ExecutionConfig{
		Family:        family,
		Workspace:     workspace,
		MaxRootLeases: 32,
		Flat:          flatStore,
		Overlay: overlay.Config{
			MemoryBudget:   overlayMemory,
			MaxMemoryBytes: 8 << 20,
		},
	})
	require.NoError(t, err)
	service, err := New(Config{
		Standalone:        true,
		GenesisConfig:     genesis.DefaultConfig(),
		NodeStore:         database,
		SHAMapFamily:      family,
		Execution:         execution,
		FlatState:         flatStore,
		PersistenceMemory: workspace,
		LedgerCacheSize:   2,
	})
	require.NoError(t, err)
	return &boundedTestRuntime{
		service:   service,
		execution: execution,
		flat:      flatStore,
		database:  database,
		workspace: workspace,
	}, database, family
}

func TestBoundedExecutionRetentionKeepsFlatRootsAcrossRotations(t *testing.T) {
	ctx := t.Context()
	runtime, database, family := newBoundedRetentionRuntime(t)
	t.Cleanup(func() { runtime.close(t) })
	require.NoError(t, runtime.service.Start())

	// Close several service windows first so the retention path sees a real
	// bounded execution history rather than a hand-built SHAMap fixture.
	for range 3 {
		_, err := runtime.service.AcceptLedger(ctx)
		require.NoError(t, err)
		validated := runtime.service.GetValidatedLedger()
		require.NotNil(t, validated)
		seq, guard, err := runtime.service.PrepareStateRetention(ctx, validated.Sequence(), nil)
		require.NoError(t, err)
		committed, err := database.RotateGenerationWithRetention(ctx, seq, 1, guard, family.BeginPrune)
		require.NoError(t, err)
		require.True(t, committed)
	}

	// Build one additional durable bounded ledger and publish its flat version
	// without acknowledging the outbox. Its root lease and header inventory
	// must survive every subsequent destructive generation rotation.
	parent := runtime.service.GetClosedLedger()
	require.NotNil(t, parent)
	future, err := ledger.NewOpen(parent, parent.CloseTime().Add(2*time.Second))
	require.NoError(t, err)
	futureKey := [32]byte{0x7a}
	futureValue := []byte("retained-bounded-state")
	require.NoError(t, future.Insert(keylet.Keylet{Key: futureKey}, futureValue))
	closeTime := parent.CloseTime().Add(3 * time.Second)
	require.NoError(t, future.Close(closeTime, 0))
	require.NoError(t, runtime.service.persistToNodeStore(ctx, future, future.Sequence()))
	require.NoError(t, runtime.service.advanceFlatState(ctx, future))
	futureHash := future.Hash()
	futureRoot, err := future.StateMapHash()
	require.NoError(t, err)
	future.Discard()

	pending, err := runtime.flat.PendingOutbox(ctx, 0)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	flatDescriptor, found, err := runtime.flat.Current()
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, futureHash, flatDescriptor.LedgerHash)

	hasFutureLease := func() bool {
		for _, root := range runtime.execution.Roots() {
			if root.Hash == futureRoot && root.Kind == shamap.TypeState {
				return true
			}
		}
		return false
	}
	require.True(t, hasFutureLease(), "flat outbox target root lease was not captured")
	runtime.service.flatHeadersMu.Lock()
	_, headerRetained := runtime.service.flatHeaders[futureHash]
	runtime.service.flatHeadersMu.Unlock()
	require.True(t, headerRetained, "flat outbox header was not retained")

	readRetainedState := func() {
		reader, err := shamap.NewFromRootHashContext(ctx, shamap.TypeState, futureRoot, family)
		require.NoError(t, err)
		item, found, err := reader.GetContext(ctx, futureKey)
		require.NoError(t, err)
		require.True(t, found)
		require.Equal(t, futureValue, item.Data())
		node, err := database.Fetch(ctx, nodestore.Hash256(futureHash))
		require.NoError(t, err)
		require.NotNil(t, node)
	}

	validated := runtime.service.GetValidatedLedger()
	lastRotated := validated.Sequence()
	for range 3 {
		_, guard, err := runtime.service.PrepareStateRetention(ctx, validated.Sequence(), nil)
		require.NoError(t, err)
		lastRotated++
		committed, err := database.RotateGenerationWithRetention(ctx, lastRotated, 1, guard, family.BeginPrune)
		require.NoError(t, err)
		require.True(t, committed)
		readRetainedState()
		pending, err := runtime.flat.PendingOutbox(ctx, 0)
		require.NoError(t, err)
		require.Len(t, pending, 1)
		current, found, err := runtime.flat.Current()
		require.NoError(t, err)
		require.True(t, found)
		require.Equal(t, flatDescriptor, current)
		require.True(t, hasFutureLease())
		runtime.service.flatHeadersMu.Lock()
		_, headerRetained = runtime.service.flatHeaders[futureHash]
		runtime.service.flatHeadersMu.Unlock()
		require.True(t, headerRetained)
	}
}
