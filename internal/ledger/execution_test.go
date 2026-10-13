package ledger

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger/flatstate"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
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
	charges []uint64
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
	var charge uint64
	for _, entry := range entries {
		charge += uint64(len(entry.Data)) + executionCommitmentEntryOverhead
	}
	f.charges = append(f.charges, charge)
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

func flatExecutionHash(value byte) [32]byte {
	var result [32]byte
	result[31] = value
	return result
}

func flatExecutionDescriptor(sequence uint32, parent, ledger, state, tx byte) flatstate.Descriptor {
	return flatstate.Descriptor{
		LedgerHash: flatExecutionHash(ledger),
		ParentHash: flatExecutionHash(parent),
		Sequence:   sequence,
		StateRoot:  flatExecutionHash(state),
		TxRoot:     flatExecutionHash(tx),
		Schema:     7,
		Epoch:      19,
	}
}

func flatExecutionTree(store *ExecutionStore, descriptor flatstate.Descriptor) *executionTree {
	return &executionTree{
		store: store,
		kind:  shamap.TypeState,
		base: header.LedgerHeader{
			Hash:        descriptor.LedgerHash,
			ParentHash:  descriptor.ParentHash,
			LedgerIndex: descriptor.Sequence,
			AccountHash: descriptor.StateRoot,
			TxHash:      descriptor.TxRoot,
		},
		root: descriptor.StateRoot,
	}
}

func publishFlatExecution(t *testing.T, store *flatstate.Store, expected *flatstate.Descriptor, target flatstate.Descriptor, changes []flatstate.Change) {
	t.Helper()
	authorization := flatstate.RootClosureAuthorization{TargetRoot: target.StateRoot, Token: []byte("execution-flat-test")}
	if expected != nil {
		authorization.SourceRoot = expected.StateRoot
	}
	if err := store.Advance(context.Background(), flatstate.AdvanceRequest{
		Expected: expected, Target: target, Changes: flatstate.NewSliceChangeIterator(changes), Authorization: authorization,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestExecutionFlatViewCacheReusesBoundedLease(t *testing.T) {
	flat, err := flatstate.Open(t.TempDir(), flatstate.Options{CacheBytes: 1 << 20, MemTableSize: 1 << 20, MaxActiveViews: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer flat.Close()
	store, _ := testExecutionStore(t)
	store.config.Flat = flat
	target := flatExecutionDescriptor(1, 0, 1, 2, 3)
	key := flatExecutionHash(11)
	publishFlatExecution(t, flat, nil, target, []flatstate.Change{{Key: key, Value: []byte("cached")}})
	tree := flatExecutionTree(store, target)
	base := executionBase{tree: tree}
	for i := 0; i < 8; i++ {
		value, found, err := base.Get(context.Background(), key)
		if err != nil || !found || string(value) != "cached" {
			t.Fatalf("cached read %d = %q, %v, %v", i, value, found, err)
		}
	}
	stats := store.Stats()
	if stats.FlatHits != 8 || stats.FlatFallbacks != 0 {
		t.Fatalf("flat stats = %#v", stats)
	}
	if _, err := flat.OpenView(context.Background()); !errors.Is(err, flatstate.ErrLimit) {
		t.Fatalf("cached view was not retained: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestExecutionFlatViewCacheRefreshesDescriptorAndRetiresOnAdvance(t *testing.T) {
	flat, err := flatstate.Open(t.TempDir(), flatstate.Options{CacheBytes: 1 << 20, MemTableSize: 1 << 20, MaxActiveViews: 1, MaxBatchRecords: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer flat.Close()
	store, _ := testExecutionStore(t)
	store.config.Flat = flat
	first := flatExecutionDescriptor(1, 0, 1, 2, 3)
	key := flatExecutionHash(11)
	publishFlatExecution(t, flat, nil, first, []flatstate.Change{{Key: key, Value: []byte("first")}})
	firstTree := flatExecutionTree(store, first)
	if value, found, err := (executionBase{tree: firstTree}).Get(context.Background(), key); err != nil || !found || string(value) != "first" {
		t.Fatalf("first read = %q, %v, %v", value, found, err)
	}

	second := flatExecutionDescriptor(2, 1, 4, 5, 6)
	publishFlatExecution(t, flat, &first, second, []flatstate.Change{{Key: key, Value: []byte("second")}})
	probe, err := flat.OpenView(context.Background())
	if err != nil {
		t.Fatalf("idle cached view pinned transition: %v", err)
	}
	if err := probe.Close(); err != nil {
		t.Fatal(err)
	}
	secondTree := flatExecutionTree(store, second)
	if value, found, err := (executionBase{tree: secondTree}).Get(context.Background(), key); err != nil || !found || string(value) != "second" {
		t.Fatalf("second read = %q, %v, %v", value, found, err)
	}
	if _, err := flat.OpenView(context.Background()); !errors.Is(err, flatstate.ErrLimit) {
		t.Fatalf("refreshed view was not retained: %v", err)
	}

	third := flatExecutionDescriptor(3, 4, 7, 8, 9)
	err = flat.Advance(context.Background(), flatstate.AdvanceRequest{
		Expected: &second, Target: third,
		Changes:       flatstate.NewSliceChangeIterator([]flatstate.Change{{Key: flatExecutionHash(9), Value: []byte("out of order")}, {Key: flatExecutionHash(10), Value: []byte("ten")}, {Key: flatExecutionHash(1), Value: []byte("out of order")}}),
		Authorization: flatstate.RootClosureAuthorization{SourceRoot: second.StateRoot, TargetRoot: third.StateRoot, Token: []byte("execution-flat-test")},
	})
	if !errors.Is(err, flatstate.ErrInvalidChangeOrder) {
		t.Fatalf("invalid advance = %v", err)
	}
	if _, _, err := flat.Current(); !errors.Is(err, flatstate.ErrUnavailable) {
		t.Fatalf("current during advancing = %v", err)
	}
	lease, err := secondTree.flatView(context.Background())
	if err != nil {
		t.Fatalf("flat view during advancing: %v", err)
	}
	if lease != nil {
		lease.Close()
		t.Fatal("cached view remained available during advancing")
	}
	if _, err := flat.OpenView(context.Background()); !errors.Is(err, flatstate.ErrUnavailable) {
		t.Fatalf("open view during advancing = %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestExecutionFlatViewCacheIncludesSchemaEpochIdentity(t *testing.T) {
	flat, err := flatstate.Open(t.TempDir(), flatstate.Options{CacheBytes: 1 << 20, MemTableSize: 1 << 20, MaxActiveViews: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer flat.Close()
	store, _ := testExecutionStore(t)
	store.config.Flat = flat
	first := flatExecutionDescriptor(1, 0, 1, 2, 3)
	key := flatExecutionHash(11)
	publishFlatExecution(t, flat, nil, first, []flatstate.Change{{Key: key, Value: []byte("same-root")}})
	firstTree := flatExecutionTree(store, first)
	if _, found, err := (executionBase{tree: firstTree}).Get(context.Background(), key); err != nil || !found {
		t.Fatalf("first read = found %v, err %v", found, err)
	}

	second := first
	second.Schema++
	second.Epoch++
	if err := flat.Rebase(context.Background(), flatstate.RebaseRequest{
		Expected: &first, Target: second, Changes: flatstate.NewSliceChangeIterator(nil),
		PublicationKind: "schema-epoch",
		Authorization:   flatstate.RootClosureAuthorization{SourceRoot: first.StateRoot, TargetRoot: second.StateRoot, Token: []byte("execution-flat-test")},
	}); err != nil {
		t.Fatal(err)
	}
	lease, err := flat.BorrowView(context.Background(), second)
	if err != nil {
		t.Fatalf("borrow schema/epoch view: %v", err)
	}
	got, hasDescriptor, err := lease.View().Descriptor()
	lease.Close()
	if err != nil || !hasDescriptor || !got.Equal(second) {
		t.Fatalf("cached descriptor = %#v, has=%v, err=%v, want %#v", got, hasDescriptor, err, second)
	}
	secondTree := flatExecutionTree(store, second)
	if _, found, err := (executionBase{tree: secondTree}).Get(context.Background(), key); err != nil || !found {
		t.Fatalf("schema/epoch read = found %v, err %v", found, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestExecutionFlatViewCacheReleasesRetiredLeaseAfterAdvance(t *testing.T) {
	flat, err := flatstate.Open(t.TempDir(), flatstate.Options{CacheBytes: 1 << 20, MemTableSize: 1 << 20, MaxActiveViews: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer flat.Close()
	store, _ := testExecutionStore(t)
	store.config.Flat = flat
	first := flatExecutionDescriptor(1, 0, 1, 2, 3)
	key := flatExecutionHash(11)
	publishFlatExecution(t, flat, nil, first, []flatstate.Change{{Key: key, Value: []byte("first")}})
	firstTree := flatExecutionTree(store, first)
	lease, err := firstTree.flatView(context.Background())
	if err != nil || lease == nil {
		t.Fatalf("acquire cached lease = %v, %v", lease, err)
	}

	second := flatExecutionDescriptor(2, 1, 4, 5, 6)
	publishFlatExecution(t, flat, &first, second, []flatstate.Change{{Key: key, Value: []byte("second")}})
	lease.Close()

	probe, err := flat.OpenView(context.Background())
	if err != nil {
		t.Fatalf("retired lease remained pinned after release: %v", err)
	}
	if err := probe.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestExecutionFlatViewCacheCloseWaitsForLease(t *testing.T) {
	flat, err := flatstate.Open(t.TempDir(), flatstate.Options{CacheBytes: 1 << 20, MemTableSize: 1 << 20, MaxActiveViews: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer flat.Close()
	store, _ := testExecutionStore(t)
	store.config.Flat = flat
	target := flatExecutionDescriptor(1, 0, 1, 2, 3)
	key := flatExecutionHash(11)
	publishFlatExecution(t, flat, nil, target, []flatstate.Change{{Key: key, Value: []byte("lease")}})
	tree := flatExecutionTree(store, target)
	lease, err := tree.flatView(context.Background())
	if err != nil || lease == nil {
		t.Fatalf("acquire cached lease = %v, %v", lease, err)
	}
	closed := make(chan error, 1)
	go func() { closed <- flat.Close() }()
	deadline := time.After(2 * time.Second)
	for {
		if _, _, currentErr := flat.Current(); errors.Is(currentErr, flatstate.ErrClosed) {
			break
		}
		select {
		case <-deadline:
			t.Fatal("flat close did not publish closing state")
		case <-time.After(time.Millisecond):
		}
	}
	lease.Close()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("flat close did not finish after lease release")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
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
	if len(batches) != 1 || batches[0] < 255 {
		t.Fatalf("small commitment fragmented into batches %v", batches)
	}
	if used := workspace.Snapshot().Used; used != 0 {
		t.Fatalf("workspace retained %d bytes after commitment", used)
	}
	stats := store.Stats()
	if stats.Builds != 1 || stats.BuildErrors != 0 || stats.RootNodes == 0 || stats.Frontier == 0 || stats.RootBuildTime == 0 {
		t.Fatalf("unexpected execution stats: %+v", stats)
	}
}

func TestExecutionCommitmentFlushesBeforeBudgetWait(t *testing.T) {
	family := &recordingExecutionFamily{Family: backend.NewMemory()}
	store, _ := executionTestStore(t, family)
	workspace, err := membudget.New(4096).NewAccount("small-commitment", 0, 4096)
	if err != nil {
		t.Fatal(err)
	}
	store.config.Workspace = workspace
	base := &executionTree{store: store, kind: shamap.TypeState, sequence: 1}
	tree, err := base.newMutable(1)
	if err != nil {
		t.Fatal(err)
	}
	defer tree.close()
	expected := shamap.New(shamap.TypeState)
	for seed := byte(1); seed != 0; seed++ {
		key, data := mutAcct(seed).Key, bytes.Repeat(mutData(seed), 4)
		if err := tree.Put(key, data); err != nil {
			t.Fatal(err)
		}
		if err := expected.Put(key, data); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	root, err := tree.hash(ctx)
	if err != nil {
		t.Fatalf("small commitment stalled or failed: %v", err)
	}
	want, err := expected.Hash()
	if err != nil || root != want {
		t.Fatalf("root %x, want %x: %v", root, want, err)
	}
	if len(family.batches) < 2 {
		t.Fatalf("no pressure flush: %v", family.batches)
	}
	if snapshot := workspace.Snapshot(); snapshot.Used != 0 || snapshot.Peak > 4096 {
		t.Fatalf("workspace = %+v", snapshot)
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

func TestExecutionCommitmentSinkBoundsAdmission(t *testing.T) {
	for _, tc := range []struct {
		name   string
		budget uint64
		bytes  int
	}{
		{name: "account-pressure", budget: 1024, bytes: 200},
		{name: "batch-ceiling", budget: 4 << 20, bytes: 128 << 10},
	} {
		t.Run(tc.name, func(t *testing.T) {
			family := &recordingExecutionFamily{Family: backend.NewMemory()}
			account, err := membudget.New(tc.budget).NewAccount("sink", 0, tc.budget)
			if err != nil {
				t.Fatal(err)
			}
			sink := newExecutionCommitmentSink(context.Background(), family, account)
			defer sink.abort()
			data := make([]byte, tc.bytes)
			for i := range 20 {
				data[0] = byte(i)
				if err := sink.add(shamap.FlushEntry{Hash: [32]byte{byte(i + 1)}, Data: data, MapType: shamap.TypeState}); err != nil {
					t.Fatal(err)
				}
			}
			clear(data)
			if err := sink.flush(); err != nil {
				t.Fatal(err)
			}
			if len(family.batches) < 2 {
				t.Fatalf("unbounded batch: %v", family.batches)
			}
			for _, charge := range family.charges {
				if charge > min(tc.budget, executionCommitmentBatchBytes) {
					t.Fatalf("batch charge %d exceeds allowance", charge)
				}
			}
			for i := range 20 {
				stored, err := family.Fetch(context.Background(), [32]byte{byte(i + 1)})
				if err != nil || len(stored) != len(data) || stored[0] != byte(i) {
					t.Fatalf("borrowed node %d was not copied before reuse: %v", i, err)
				}
			}
			if snapshot := account.Snapshot(); snapshot.Used != 0 || snapshot.Peak > tc.budget {
				t.Fatalf("sink admission: %+v", snapshot)
			}
		})
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

func TestTransactionDataStatsTrackSealedAndImportedRoots(t *testing.T) {
	family := &recordingExecutionFamily{Family: backend.NewMemory()}
	store, _ := executionTestStore(t, family)
	open := newOpenChildWithStore(t, store)
	for i, size := range []int{16, 48} {
		if err := open.AddTransactionWithMeta([32]byte{byte(i + 1)}, bytes.Repeat([]byte{0x41}, size)); err != nil {
			t.Fatal(err)
		}
	}
	frozen, err := open.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if err := open.AddTransactionWithMeta([32]byte{3}, bytes.Repeat([]byte{0x42}, 24)); err != nil {
		t.Fatal(err)
	}
	fork, err := open.MutableSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if err := fork.execution.transactions.Delete([32]byte{1}); err != nil {
		t.Fatal(err)
	}
	if err := fork.execution.transactions.PutWithNodeType([32]byte{2}, bytes.Repeat([]byte{0x43}, 40), shamap.NodeTypeTransactionWithMeta); err != nil {
		t.Fatal(err)
	}
	for _, view := range []*Ledger{open, fork} {
		if err := view.Close(time.Unix(1700000000, 0), 0); err != nil {
			t.Fatal(err)
		}
	}
	imported, err := NewClosedFromRootsContext(context.Background(), open.Header(), open.Fees(), store)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name        string
		view        *Ledger
		count, size uint64
		fetch       bool
	}{
		{"sealed", open, 3, 88, false},
		{"snapshot", frozen, 2, 64, false},
		{"replaced and deleted", fork, 2, 64, false},
		{"imported", imported, 3, 88, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			family.mu.Lock()
			family.fetches = 0
			family.mu.Unlock()
			count, size, err := tc.view.TransactionDataStatsContext(context.Background())
			if err != nil || count != tc.count || size != tc.size {
				t.Fatalf("stats = %d, %d, %v; want %d, %d", count, size, err, tc.count, tc.size)
			}
			family.mu.Lock()
			fetched := family.fetches != 0
			family.mu.Unlock()
			if fetched != tc.fetch {
				t.Fatalf("read canonical nodes = %v, want %v", fetched, tc.fetch)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := open.TransactionDataStatsContext(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled stats = %v", err)
	}
	for _, view := range []*Ledger{open, fork, frozen, imported} {
		view.Discard()
	}
}
