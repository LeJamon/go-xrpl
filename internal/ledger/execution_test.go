package ledger

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger/overlay"
	"github.com/LeJamon/go-xrpl/internal/ledger/rootlease"
	"github.com/LeJamon/go-xrpl/internal/membudget"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/LeJamon/go-xrpl/shamap/backend"
)

type recordingExecutionFamily struct {
	shamap.Family
	mu      sync.Mutex
	batches []int
	fetches int
	fail    error
}

type droppingExecutionFamily struct {
	shamap.Family
}

type blockingExecutionFamily struct {
	shamap.Family
	entered chan struct{}
	release chan struct{}
	once    sync.Once
	block   atomic.Bool
}

func (f *blockingExecutionFamily) StoreBatch(ctx context.Context, entries []shamap.FlushEntry) error {
	if !f.block.Load() {
		return f.Family.StoreBatch(ctx, entries)
	}
	f.once.Do(func() { close(f.entered) })
	select {
	case <-f.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	return f.Family.StoreBatch(ctx, entries)
}

func (f *droppingExecutionFamily) StoreBatch(context.Context, []shamap.FlushEntry) error {
	return nil
}

type durableOnlyExecutionFamily struct {
	base *backend.NodeStore
}

func (f *durableOnlyExecutionFamily) Fetch(ctx context.Context, hash [32]byte) ([]byte, error) {
	return f.base.Fetch(ctx, hash)
}

func (f *durableOnlyExecutionFamily) FetchDurable(ctx context.Context, hash [32]byte) ([]byte, error) {
	return f.base.FetchDurable(ctx, hash)
}

func (f *durableOnlyExecutionFamily) StoreBatch(ctx context.Context, entries []shamap.FlushEntry) error {
	return f.base.StoreBatch(ctx, entries)
}

func (f *recordingExecutionFamily) Fetch(ctx context.Context, hash [32]byte) ([]byte, error) {
	f.mu.Lock()
	f.fetches++
	f.mu.Unlock()
	return f.Family.Fetch(ctx, hash)
}

func (f *recordingExecutionFamily) FetchDurable(ctx context.Context, hash [32]byte) ([]byte, error) {
	f.mu.Lock()
	f.fetches++
	f.mu.Unlock()
	if durable, ok := f.Family.(interface {
		FetchDurable(context.Context, [32]byte) ([]byte, error)
	}); ok {
		return durable.FetchDurable(ctx, hash)
	}
	return f.Family.Fetch(ctx, hash)
}

func (f *recordingExecutionFamily) StoreBatch(ctx context.Context, entries []shamap.FlushEntry) error {
	f.mu.Lock()
	f.batches = append(f.batches, len(entries))
	fail := f.fail
	f.mu.Unlock()
	if fail != nil {
		return fail
	}
	return f.Family.StoreBatch(ctx, entries)
}

func executionTestStore(t *testing.T, family shamap.Family) (*ExecutionStore, *membudget.Account) {
	t.Helper()
	pool := membudget.New(32 << 20)
	workspace, err := pool.NewAccount("work", 16<<20, 16<<20)
	if err != nil {
		t.Fatal(err)
	}
	changes, err := pool.NewAccount("changes", 0, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewExecutionStore(ExecutionConfig{Family: family, Workspace: workspace,
		Overlay: overlay.Config{MemoryBudget: changes, MaxMemoryBytes: 128 << 10, MaxDiskBytes: 16 << 20, SpillDir: t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Logf("execution store retained active roots at test cleanup: %v", err)
		}
	})
	return store, workspace
}

func testExecutionStore(t *testing.T) (*ExecutionStore, *membudget.Account) {
	t.Helper()
	pool := membudget.New(32 << 20)
	workspace, err := pool.NewAccount("work", 16<<20, 16<<20)
	if err != nil {
		t.Fatal(err)
	}
	memory, err := pool.NewAccount("changes", 0, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewExecutionStore(ExecutionConfig{Family: backend.NewMemory(), Workspace: workspace,
		Overlay: overlay.Config{MemoryBudget: memory, MaxMemoryBytes: 128 << 10, MaxDiskBytes: 16 << 20, SpillDir: t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Logf("execution store retained active roots at test cleanup: %v", err)
		}
	})
	return store, memory
}

func hasVerifiedExecutionClosure(l *Ledger) bool {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.execution != nil && !l.writable &&
		l.execution.state.closure == executionClosureVerified &&
		l.execution.transactions.closure == executionClosureVerified
}

func TestBoundedExecutionRollbackForkAndReaders(t *testing.T) {
	store, memory := testExecutionStore(t)
	parent := newOpenChildWithStore(t, store)
	if err := parent.Close(time.Unix(1700000000, 0), 0); err != nil {
		t.Fatal(err)
	}
	l, err := NewOpen(parent, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	defer l.Discard()
	parent.ReleaseExecutionBuildLeases()
	key := mutAcct(55)
	if err := l.Insert(key, mutData(3)); err != nil {
		t.Fatal(err)
	}
	before, err := l.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	defer before.Discard()
	failure := errors.New("rollback")
	err = l.ApplyAtomically(func(writer Writer) error {
		if err := writer.Update(key, mutData(4)); err != nil {
			return err
		}
		return failure
	})
	if !errors.Is(err, failure) {
		t.Fatal(err)
	}
	fork, err := l.MutableSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	defer fork.Discard()
	if err := fork.Update(key, mutData(5)); err != nil {
		t.Fatal(err)
	}
	if err := l.ConsumeState(fork); err != nil {
		t.Fatal(err)
	}
	data, err := before.Read(key)
	if err != nil || !bytes.Equal(data, mutData(3)) {
		t.Fatalf("snapshot changed: %x %v", data, err)
	}
	data, err = l.Read(key)
	if err != nil || !bytes.Equal(data, mutData(5)) {
		t.Fatalf("commit failed: %x %v", data, err)
	}
	if err := l.FreezeExecution(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := memory.Snapshot().Used; got != 0 {
		t.Fatalf("frozen view retains %d managed bytes", got)
	}
	seen := false
	if err := l.ForEach(func(k [32]byte, _ []byte) bool {
		_, readErr := l.Read(key)
		if readErr != nil {
			t.Error(readErr)
		}
		seen = seen || k == key.Key
		return true
	}); err != nil {
		t.Fatal(err)
	}
	if !seen {
		t.Fatal("iteration omitted entry")
	}
}

func TestExecutionCommitmentSinkBatchesBorrowedNodes(t *testing.T) {
	family := &recordingExecutionFamily{Family: backend.NewMemory()}
	store, workspace := executionTestStore(t, family)
	base := &executionTree{store: store, kind: shamap.TypeState, sequence: 1}
	tree, err := base.newMutable(1)
	if err != nil {
		t.Fatal(err)
	}
	defer tree.close()
	for seed := byte(1); seed != 0; seed++ {
		key := mutAcct(seed).Key
		if err := tree.Put(key, bytes.Repeat(mutData(seed), 4)); err != nil {
			t.Fatal(err)
		}
	}
	if root, err := tree.hash(context.Background()); err != nil || root == ([32]byte{}) {
		t.Fatalf("hash = %x, %v", root, err)
	}
	family.mu.Lock()
	batches := append([]int(nil), family.batches...)
	family.mu.Unlock()
	if len(batches) < 2 {
		t.Fatalf("commitment batches = %v, want bounded multi-batch output", batches)
	}
	for _, entries := range batches {
		if entries == 0 || entries > executionCommitmentBatchEntries {
			t.Fatalf("batch entry count = %d", entries)
		}
	}
	if used := workspace.Snapshot().Used; used != 0 {
		t.Fatalf("workspace retained %d bytes after commitment", used)
	}
	stats := store.Stats()
	if stats.Builds != 1 || stats.BuildErrors != 0 || stats.RootNodes == 0 || stats.Frontier == 0 || stats.RootBuildTime == 0 {
		t.Fatalf("unexpected execution stats: %+v", stats)
	}
}

func TestExecutionCommitmentSinkFailureReleasesWorkspace(t *testing.T) {
	want := errors.New("store failed")
	family := &recordingExecutionFamily{Family: backend.NewMemory(), fail: want}
	store, workspace := executionTestStore(t, family)
	base := &executionTree{store: store, kind: shamap.TypeState, sequence: 1}
	tree, err := base.newMutable(1)
	if err != nil {
		t.Fatal(err)
	}
	defer tree.close()
	if err := tree.Put(mutAcct(1).Key, bytes.Repeat(mutData(1), 4)); err != nil {
		t.Fatal(err)
	}
	if _, err := tree.hash(context.Background()); !errors.Is(err, want) {
		t.Fatalf("hash error = %v, want %v", err, want)
	}
	if used := workspace.Snapshot().Used; used != 0 {
		t.Fatalf("workspace retained %d bytes after failed commitment: %d", used, used)
	}
}

func TestAutomaticBuildAdmissionCoversBatchedSink(t *testing.T) {
	family := &blockingExecutionFamily{Family: backend.NewMemory(), entered: make(chan struct{}), release: make(chan struct{})}
	family.block.Store(true)
	store, _ := executionTestStore(t, family)
	base := &executionTree{store: store, kind: shamap.TypeState, sequence: 1}
	tree, err := base.newMutable(1)
	if err != nil {
		t.Fatal(err)
	}
	defer tree.close()
	for seed := byte(1); seed != 0; seed++ {
		if err := tree.Put(mutAcct(seed).Key, bytes.Repeat(mutData(seed), 4)); err != nil {
			t.Fatal(err)
		}
	}
	type buildResult struct {
		root [32]byte
		err  error
	}
	done := make(chan buildResult, 1)
	go func() {
		root, err := tree.hash(context.Background())
		done <- buildResult{root: root, err: err}
	}()
	select {
	case <-family.entered:
	case <-time.After(time.Second):
		t.Fatal("commitment build did not reach its storage sink")
	}
	for rotation := 0; rotation < 2; rotation++ {
		if release, ok := store.GuardPreservedRoots(nil); ok || release != nil {
			t.Fatalf("retirement %d admitted while commitment sink was active", rotation)
		}
	}
	close(family.release)
	result := <-done
	if result.err != nil {
		t.Fatal(result.err)
	}
	found := false
	for _, root := range store.Roots() {
		found = found || (root.Hash == result.root && root.Kind == shamap.TypeState)
	}
	if !found {
		t.Fatalf("destination root %x was not retained after build", result.root)
	}
	tree.close()
	if got := store.Stats().RootBorrowers; got != 0 {
		t.Fatalf("root borrowers after tree close = %d, want 0", got)
	}
}

func TestFreezeExecutionContextCancelsBlockedCommit(t *testing.T) {
	family := &blockingExecutionFamily{Family: backend.NewMemory(), entered: make(chan struct{}), release: make(chan struct{})}
	store, _ := executionTestStore(t, family)
	open := newOpenChildWithStore(t, store)
	if err := open.Insert(mutAcct(0xaa), mutData(9)); err != nil {
		t.Fatal(err)
	}
	family.block.Store(true)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- open.FreezeExecution(ctx) }()
	select {
	case <-family.entered:
	case <-time.After(time.Second):
		t.Fatal("freeze did not reach its blocked commitment")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("freeze error = %v, want context cancellation", err)
		}
	case <-time.After(time.Second):
		t.Fatal("freeze did not observe cancellation")
	}
	if got, err := open.Read(mutAcct(0xaa)); err != nil || !bytes.Equal(got, mutData(9)) {
		t.Fatalf("canceled freeze lost original open view: %x %v", got, err)
	}
	if err := open.Insert(mutAcct(0xab), mutData(10)); err != nil {
		t.Fatalf("canceled freeze left open view unwritable: %v", err)
	}
	close(family.release)
	if err := open.FreezeExecution(context.Background()); err != nil {
		t.Fatalf("retry freeze after unblocking: %v", err)
	}
}

func TestSealedBuildLeaseReleaseKeepsDescriptor(t *testing.T) {
	store, _ := testExecutionStore(t)
	parent := newOpenChildWithStore(t, store)
	if err := parent.Close(time.Unix(1700000000, 0), 0); err != nil {
		t.Fatal(err)
	}
	bounded, err := parent.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	child, err := NewOpen(bounded, time.Unix(1700000004, 0))
	if err != nil {
		t.Fatal(err)
	}
	key := mutAcct(0xf1)
	value := mutData(0x51)
	if err := child.Insert(key, value); err != nil {
		t.Fatal(err)
	}
	readSnapshot, err := child.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	borrowersBeforeSnapshotDiscard := store.Stats().RootBorrowers
	readSnapshot.Discard()
	if got := store.Stats().RootBorrowers; got != borrowersBeforeSnapshotDiscard {
		t.Fatalf("read snapshot changed root borrowers on discard: got %d, want %d", got, borrowersBeforeSnapshotDiscard)
	}
	if err := child.Close(time.Unix(1700000004, 0), 0); err != nil {
		t.Fatal(err)
	}
	wantHash := child.Hash()
	wantRoot, err := child.StateMapHash()
	if err != nil {
		t.Fatal(err)
	}
	if got := store.Stats().RootBorrowers; got == 0 {
		t.Fatal("sealed builder did not retain a destination root")
	}
	child.ReleaseExecutionBuildLeases()
	if got := store.Stats().RootBorrowers; got != 2 {
		t.Fatalf("root borrowers after explicit release = %d, want the parent state and transaction roots", got)
	}
	child.Discard()
	if child.Hash() != wantHash {
		t.Fatalf("descriptor hash changed after discard: %x, want %x", child.Hash(), wantHash)
	}
	if got, err := child.StateMapHash(); err != nil || got != wantRoot {
		t.Fatalf("descriptor root after discard = %x, %v, want %x", got, err, wantRoot)
	}
}

func TestBoundedReadsCountsColdTransactionsOnce(t *testing.T) {
	family := &recordingExecutionFamily{Family: backend.NewMemory()}
	store, _ := executionTestStore(t, family)
	historical := newOpenChildWithStore(t, store)
	if err := historical.AddTransactionWithMeta([32]byte{0xc1}, bytes.Repeat([]byte{0x41}, 16)); err != nil {
		t.Fatal(err)
	}
	if err := historical.Close(time.Unix(1700000000, 0), 0); err != nil {
		t.Fatal(err)
	}
	rooted, err := NewClosedFromRootsContext(context.Background(), historical.Header(), historical.Fees(), store)
	if err != nil {
		t.Fatalf("rooted transaction ledger: %v", err)
	}
	family.mu.Lock()
	family.fetches = 0
	family.mu.Unlock()
	if got := rooted.TxCount(); got != 1 {
		t.Fatalf("cold transaction count = %d, want 1", got)
	}
	family.mu.Lock()
	firstFetches := family.fetches
	family.mu.Unlock()
	if firstFetches == 0 {
		t.Fatal("cold transaction count did not read the rooted transaction tree")
	}
	if got := rooted.TxCount(); got != 1 {
		t.Fatalf("cached transaction count = %d, want 1", got)
	}
	family.mu.Lock()
	secondFetches := family.fetches
	family.mu.Unlock()
	if secondFetches != firstFetches {
		t.Fatalf("transaction count reread root: first fetches=%d, second=%d", firstFetches, secondFetches)
	}
	historical.Discard()
}

func TestBoundedTxCountConcurrentMutation(t *testing.T) {
	store, _ := testExecutionStore(t)
	parent := newOpenChildWithStore(t, store)
	if err := parent.Close(time.Unix(1700000000, 0), 0); err != nil {
		t.Fatal(err)
	}
	open, err := NewOpen(parent, time.Unix(1700000004, 0))
	if err != nil {
		t.Fatal(err)
	}
	defer open.Discard()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 256 {
			_ = open.TxCount()
		}
	}()
	for i := byte(0); i != 0xff; i++ {
		key := mutAcct(i)
		if err := open.Insert(key, mutData(i)); err != nil {
			t.Fatal(err)
		}
		if err := open.Erase(key); err != nil {
			t.Fatal(err)
		}
	}
	<-done
}

func TestExecutionBuildAdmissionRequiresDestinationHandoff(t *testing.T) {
	store, _ := testExecutionStore(t)
	parent := rootlease.Root{Hash: [32]byte{1}, Kind: shamap.TypeState}
	destination := rootlease.Root{Hash: [32]byte{2}, Kind: shamap.TypeState}
	build, err := store.roots.BeginBuild()
	if err != nil {
		t.Fatal(err)
	}
	parentLease, err := store.roots.AcquireLease(parent)
	if err != nil {
		t.Fatal(err)
	}
	if release, ok := store.GuardPreservedRoots(map[rootlease.Root]struct{}{parent: {}}); ok || release != nil {
		t.Fatal("retirement admitted while bounded build was active")
	}
	destinationLease, err := store.roots.AcquireLease(destination)
	if err != nil {
		t.Fatal(err)
	}
	parentLease.Release()
	build.End()
	guardRelease, ok := store.GuardPreservedRoots(map[rootlease.Root]struct{}{destination: {}})
	if !ok {
		t.Fatal("retirement rejected after destination handoff")
	}
	guardRelease()
	destinationLease.Release()
}

func TestBoundedExecutionRejectsPartialDestination(t *testing.T) {
	sourceStore, _ := testExecutionStore(t)
	parent := newOpenChildWithStore(t, sourceStore)
	if err := parent.Insert(mutAcct(0x7a), mutData(0x31)); err != nil {
		t.Fatal(err)
	}
	if err := parent.Close(time.Unix(1700000000, 0), 0); err != nil {
		t.Fatal(err)
	}
	stateMap, err := parent.StateMapSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	txMap, err := parent.TxMapSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	family := &droppingExecutionFamily{Family: backend.NewMemory()}
	store, _ := executionTestStore(t, family)
	if _, err := NewFromHeader(parent.Header(), stateMap, txMap, parent.Fees(), store); err == nil {
		t.Fatal("constructor accepted a destination that dropped every node")
	}
}

func TestBoundedExecutionLeavesDurableFamilyPendingUntilBarrier(t *testing.T) {
	family := &durableOnlyExecutionFamily{base: backend.NewMemory()}
	store, _ := executionTestStore(t, family)
	parent := newOpenChildWithStore(t, store)
	if err := parent.Close(time.Unix(1700000000, 0), 0); err != nil {
		t.Fatal(err)
	}
	stateMap, err := parent.StateMapSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	txMap, err := parent.TxMapSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := NewFromHeader(parent.Header(), stateMap, txMap, parent.Fees(), store)
	if err != nil {
		t.Fatalf("durable closure verification failed: %v", err)
	}
	if hasVerifiedExecutionClosure(loaded) {
		t.Fatal("imported closure was marked verified before the durability barrier")
	}
	if err := loaded.EstablishBoundedClosure(context.Background()); err != nil {
		t.Fatalf("durable closure establishment failed: %v", err)
	}
	if !hasVerifiedExecutionClosure(loaded) {
		t.Fatal("durable closure was not marked verified after the barrier")
	}
}

func TestBoundedReadsDropsHistoricalMapsUntilCertification(t *testing.T) {
	ctx := context.Background()
	family := &recordingExecutionFamily{Family: backend.NewMemory()}
	store, _ := executionTestStore(t, family)
	historical := newOpenChildWithStore(t, store)
	key := mutAcct(0x7b)
	want := mutData(0x32)
	if err := historical.Insert(key, want); err != nil {
		t.Fatal(err)
	}
	if err := historical.Close(time.Unix(1700000000, 0), 0); err != nil {
		t.Fatal(err)
	}
	rooted, err := NewClosedFromRootsContext(ctx, historical.Header(), historical.Fees(), store)
	if err != nil {
		t.Fatalf("rooted historical constructor: %v", err)
	}
	if hasVerifiedExecutionClosure(rooted) {
		t.Fatal("historical read conversion certified a closure before verification")
	}
	family.mu.Lock()
	family.fetches = 0
	family.mu.Unlock()
	got, err := rooted.Read(key)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("cold historical read = %x, want %x", got, want)
	}
	seen := false
	if err := rooted.ForEach(func(gotKey [32]byte, _ []byte) bool {
		seen = seen || gotKey == key.Key
		return true
	}); err != nil {
		t.Fatal(err)
	}
	if !seen {
		t.Fatal("cold historical iteration omitted the inserted object")
	}
	if err := rooted.AuthenticateClosure(ctx); err != nil {
		t.Fatalf("authenticate historical closure: %v", err)
	}
	if hasVerifiedExecutionClosure(rooted) {
		t.Fatal("authenticated historical closure was promoted before the durability barrier")
	}
	if err := rooted.EstablishBoundedClosure(ctx); err != nil {
		t.Fatal(err)
	}
	if !hasVerifiedExecutionClosure(rooted) {
		t.Fatal("historical closure was not certified after explicit verification")
	}
}

func TestBoundedExecutionClosureProofIsOneTimeAndPromotable(t *testing.T) {
	family := &recordingExecutionFamily{Family: backend.NewMemory()}
	store, _ := executionTestStore(t, family)
	parent := newOpenChildWithStore(t, store)
	if err := parent.Close(time.Unix(1700000000, 0), 0); err != nil {
		t.Fatal(err)
	}
	if hasVerifiedExecutionClosure(parent) {
		t.Fatal("imported bounded execution was verified before the durability barrier")
	}
	child, err := NewOpen(parent, time.Unix(1700000004, 0))
	if err != nil {
		t.Fatal(err)
	}
	parent.ReleaseExecutionBuildLeases()
	if err := child.Insert(mutAcct(0xef), mutData(9)); err != nil {
		t.Fatal(err)
	}
	if err := child.AddTransactionWithMeta([32]byte{0xef}, bytes.Repeat([]byte{0x41}, 16)); err != nil {
		t.Fatal(err)
	}
	if err := child.Close(time.Unix(1700000004, 0), 0); err != nil {
		t.Fatal(err)
	}
	if hasVerifiedExecutionClosure(child) {
		t.Fatal("newly emitted closure was marked durable before establishment")
	}
	family.mu.Lock()
	family.fetches = 0
	family.mu.Unlock()
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := child.EstablishBoundedClosure(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled closure establishment error = %v, want context cancellation", err)
	}
	if hasVerifiedExecutionClosure(child) {
		t.Fatal("canceled closure establishment marked the root verified")
	}
	if err := child.EstablishBoundedClosure(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !hasVerifiedExecutionClosure(child) {
		t.Fatal("pending closure was not promoted")
	}
	family.mu.Lock()
	fetches := family.fetches
	family.mu.Unlock()
	if fetches != 0 {
		t.Fatalf("promoting pending closure fetched %d nodes", fetches)
	}
	child.Discard()
}

func TestPendingClosureLineageSurvivesFrozenOpenFork(t *testing.T) {
	family := &recordingExecutionFamily{Family: backend.NewMemory()}
	store, _ := executionTestStore(t, family)
	parent := newOpenChildWithStore(t, store)
	if err := parent.Close(time.Unix(1700000000, 0), 0); err != nil {
		t.Fatal(err)
	}
	open, err := NewOpen(parent, time.Unix(1700000004, 0))
	if err != nil {
		t.Fatal(err)
	}
	parent.ReleaseExecutionBuildLeases()
	if err := open.Insert(mutAcct(0xed), mutData(7)); err != nil {
		t.Fatal(err)
	}
	if err := open.FreezeExecution(context.Background()); err != nil {
		t.Fatal(err)
	}
	borrowersAfterFreeze := store.Stats().RootBorrowers
	if err := open.FreezeExecution(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := store.Stats().RootBorrowers; got != borrowersAfterFreeze {
		t.Fatalf("repeated FreezeExecution changed root borrowers: got %d, want %d", got, borrowersAfterFreeze)
	}
	fork, err := open.MutableSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	open.ReleaseExecutionBuildLeases()
	if err := fork.Insert(mutAcct(0xee), mutData(8)); err != nil {
		t.Fatal(err)
	}
	if err := fork.Close(time.Unix(1700000004, 0), 0); err != nil {
		t.Fatal(err)
	}
	if hasVerifiedExecutionClosure(fork) {
		t.Fatal("forked pending closure was marked verified before the barrier")
	}
	family.mu.Lock()
	family.fetches = 0
	family.mu.Unlock()
	if err := fork.EstablishBoundedClosure(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !hasVerifiedExecutionClosure(fork) {
		t.Fatal("forked pending closure was not promoted")
	}
	family.mu.Lock()
	fetches := family.fetches
	family.mu.Unlock()
	if fetches != 0 {
		t.Fatalf("promoting frozen pending lineage fetched %d nodes", fetches)
	}
	fork.Discard()
}
