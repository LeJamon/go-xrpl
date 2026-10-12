package service

import (
	"context"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/flatstate"
	"github.com/LeJamon/go-xrpl/internal/ledger/genesis"
	"github.com/LeJamon/go-xrpl/internal/ledger/overlay"
	"github.com/LeJamon/go-xrpl/internal/membudget"
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
	database   *nodestore.KVDatabase
	workspace  *membudget.Account
}

type boundedFailingRepository struct {
	relationaldb.RepositoryManager
	failNext atomic.Bool
}

func (r *boundedFailingRepository) PersistLedgerStream(
	ctx context.Context,
	ledger relationaldb.LedgerInfo,
	commitID [32]byte,
	publicationKind string,
	next relationaldb.IndexedTransactionStream,
) error {
	if r.failNext.CompareAndSwap(true, false) {
		return errBoundedPublication
	}
	publisher, ok := r.RepositoryManager.(relationaldb.StreamingLedgerPublisher)
	if !ok {
		return errors.New("wrapped repository does not support streaming publication")
	}
	return publisher.PersistLedgerStream(ctx, ledger, commitID, publicationKind, next)
}

func boundedE2E(t *testing.T) {
	t.Helper()
	if os.Getenv("GOXRPL_BOUNDED_E2E") == "" {
		t.Skip("set GOXRPL_BOUNDED_E2E=1 to run the real Pebble/NodeStore/SQLite bounded execution tests")
	}
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
	boundedE2E(t)
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
	boundedE2E(t)
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
	failingRepository.failNext.Store(true)
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

func TestBoundedExecutionInterruptedMarkerResumesFromDurableRoots(t *testing.T) {
	boundedE2E(t)
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
	boundedE2E(t)
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

func TestBoundedNodeStoreRetainsMixedRootsAcrossThreeRotations(t *testing.T) {
	boundedE2E(t)
	ctx := t.Context()
	store, err := kvpebble.NewRotating(
		filepath.Join(t.TempDir(), "rotating-nodes"),
		kvpebble.Options{BlockCacheBytes: 4 << 20, MaxOpenFiles: 200},
	)
	require.NoError(t, err)
	database, err := nodestore.NewRotatingKVDatabase(store, nodestore.DatabaseConfig{})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	family := backend.New(database)

	persist := func(tree *shamap.SHAMap) {
		require.NoError(t, tree.StoreDirty(func(entries []shamap.FlushEntry) error {
			return family.StoreBatch(ctx, entries)
		}))
		require.NoError(t, database.Sync(ctx))
	}
	base, err := shamap.NewBacked(shamap.TypeState, family)
	require.NoError(t, err)
	base.SetLedgerSeq(10)
	baseKey := [32]byte{0x10}
	branchAKey := [32]byte{0xa1}
	branchBKey := [32]byte{0xb2}
	require.NoError(t, base.Put(baseKey, []byte("base-state-value")))
	persist(base)
	baseRoot, err := base.Hash()
	require.NoError(t, err)

	branchA, err := shamap.NewFromRootHash(shamap.TypeState, baseRoot, family)
	require.NoError(t, err)
	branchA.SetLedgerSeq(20)
	require.NoError(t, branchA.Put(branchAKey, []byte("branch-a-state")))
	persist(branchA)
	branchARoot, err := branchA.Hash()
	require.NoError(t, err)

	branchB, err := shamap.NewFromRootHash(shamap.TypeState, baseRoot, family)
	require.NoError(t, err)
	branchB.SetLedgerSeq(30)
	require.NoError(t, branchB.Put(branchBKey, []byte("branch-b-state")))
	persist(branchB)
	branchBRoot, err := branchB.Hash()
	require.NoError(t, err)

	promoteClosure := func(tree *shamap.SHAMap) {
		nodes, err := tree.WalkFetchPackNodes(1 << 20)
		require.NoError(t, err)
		for _, node := range nodes {
			promoted, err := database.FetchForPromotion(ctx, nodestore.Hash256(node.Hash))
			require.NoError(t, err)
			require.NotNil(t, promoted)
		}
	}
	for rotation := range 3 {
		committed, err := database.RotateGeneration(ctx, uint32(40+rotation*10), 1)
		require.NoError(t, err)
		require.True(t, committed)
		if rotation < 2 {
			promoteClosure(base)
			promoteClosure(branchA)
			promoteClosure(branchB)
		}
		for _, check := range []struct {
			root  [32]byte
			key   [32]byte
			value string
		}{
			{root: baseRoot, key: baseKey, value: "base-state-value"},
			{root: branchARoot, key: branchAKey, value: "branch-a-state"},
			{root: branchBRoot, key: branchBKey, value: "branch-b-state"},
		} {
			reader, err := shamap.NewFromRootHash(shamap.TypeState, check.root, family)
			require.NoError(t, err)
			item, found, err := reader.Get(check.key)
			require.NoError(t, err)
			require.True(t, found)
			require.Equal(t, []byte(check.value), item.Data())
		}
	}
}
