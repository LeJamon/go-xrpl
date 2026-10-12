package flatstate

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cockroachdb/pebble"
)

func testHash(value byte) Hash {
	var result Hash
	result[31] = value
	return result
}

func testDescriptor(sequence byte, parent, ledger, state, tx Hash) Descriptor {
	return Descriptor{
		LedgerHash: ledger,
		ParentHash: parent,
		Sequence:   uint32(sequence),
		StateRoot:  state,
		TxRoot:     tx,
		Schema:     7,
		Epoch:      19,
	}
}

func testAuthorization(source, target Descriptor) RootClosureAuthorization {
	return RootClosureAuthorization{SourceRoot: source.StateRoot, TargetRoot: target.StateRoot, Token: []byte("retention-certificate")}
}

type flatTestBudget struct {
	limit atomic.Uint64
	used  atomic.Uint64
}

func newFlatTestBudget(limit uint64) *flatTestBudget {
	budget := &flatTestBudget{}
	budget.limit.Store(limit)
	return budget
}

func (b *flatTestBudget) TryReserve(bytes uint64) bool {
	for {
		used := b.used.Load()
		if bytes > b.limit.Load()-minUint64(b.limit.Load(), used) {
			return false
		}
		if b.used.CompareAndSwap(used, used+bytes) {
			return true
		}
	}
}

func (b *flatTestBudget) Release(bytes uint64) { b.used.Add(^(bytes - 1)) }
func (b *flatTestBudget) Limit() uint64        { return b.limit.Load() }
func (b *flatTestBudget) Changed() <-chan struct{} {
	return nil
}

func minUint64(a, b uint64) uint64 {
	if a < b {
		return a
	}
	return b
}

func importInitial(t *testing.T, store *Store, target Descriptor, changes []Change) {
	t.Helper()
	if err := store.Advance(context.Background(), AdvanceRequest{
		Target:        target,
		Changes:       NewSliceChangeIterator(changes),
		Authorization: RootClosureAuthorization{TargetRoot: target.StateRoot, Token: []byte("genesis-closure")},
	}); err != nil {
		t.Fatalf("initial advance: %v", err)
	}
}

func TestResolveMemTableSize(t *testing.T) {
	for _, tc := range []struct {
		name      string
		cache     int64
		requested int64
		want      int64
	}{
		{name: "cache budget", cache: 8 << 20, requested: 4 << 20, want: 2 << 20},
		{name: "requested ceiling", cache: 8 << 20, requested: 1 << 20, want: 1 << 20},
		{name: "minimum target", cache: 128 << 10, requested: 8 << 20, want: 64 << 10},
		{name: "default sizing unchanged", cache: 64 << 20, requested: 16 << 20, want: 16 << 20},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveMemTableSize(tc.cache, tc.requested); got != tc.want {
				t.Fatalf("resolveMemTableSize(%d, %d) = %d, want %d", tc.cache, tc.requested, got, tc.want)
			}
		})
	}
}

func TestStoreUsesCacheBoundedMemTableTarget(t *testing.T) {
	store, err := Open(t.TempDir(), Options{CacheBytes: 8 << 20, MemTableSize: 4 << 20})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	if store.opts.MemTableSize != 4<<20 {
		t.Fatalf("configured memtable ceiling = %d, want %d", store.opts.MemTableSize, 4<<20)
	}
	if store.effectiveMemTableSize != 2<<20 {
		t.Fatalf("effective memtable target = %d, want %d", store.effectiveMemTableSize, 2<<20)
	}
}

func TestStoreCacheBudgetLeavesBlockCacheAvailable(t *testing.T) {
	const (
		cacheBytes   = int64(8 << 20)
		memTableSize = int64(4 << 20)
	)
	store, err := Open(t.TempDir(), Options{CacheBytes: cacheBytes, MemTableSize: memTableSize})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	target := testDescriptor(1, Hash{}, testHash(1), testHash(2), testHash(3))
	value := bytes.Repeat([]byte{'v'}, 2<<10)
	changes := make([]Change, 256)
	for i := range changes {
		var key Hash
		key[31] = byte(i)
		changes[i] = Change{Key: key, Value: value}
	}
	importInitial(t, store, target, changes)
	if err := store.db.Flush(); err != nil {
		t.Fatal(err)
	}

	view, err := store.OpenView(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range changes {
		if got, found, err := view.Get(change.Key); err != nil || !found || !bytes.Equal(got, value) {
			_ = view.Close()
			t.Fatalf("seed read = %d bytes, found=%v, err=%v", len(got), found, err)
		}
	}
	if err := view.Close(); err != nil {
		t.Fatal(err)
	}

	memMetrics := store.db.Metrics().MemTable
	cacheMetrics := store.cache.Metrics()
	if memMetrics.Size > uint64(store.effectiveMemTableSize) {
		t.Fatalf("memtable bytes = %d, effective target = %d", memMetrics.Size, store.effectiveMemTableSize)
	}
	if cacheMetrics.Size == 0 || cacheMetrics.Size > cacheBytes {
		t.Fatalf("block cache bytes = %d, want (0, %d]", cacheMetrics.Size, cacheBytes)
	}
}

func TestStoreInitialImportReadAndDescriptor(t *testing.T) {
	store, err := Open(t.TempDir(), Options{CacheBytes: 1 << 20, MemTableSize: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	target := testDescriptor(1, Hash{}, testHash(1), testHash(2), testHash(3))
	changes := []Change{{Key: testHash(1), Value: []byte("one")}, {Key: testHash(3), Value: []byte("three")}}
	importInitial(t, store, target, changes)

	got, ok, err := store.Current()
	if err != nil || !ok || !got.Equal(target) {
		t.Fatalf("current = %#v, %v, %v; target %#v", got, ok, err, target)
	}
	view, err := store.OpenView(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer view.Close()
	descriptor, ok, err := view.Descriptor()
	if err != nil || !ok || !descriptor.Equal(target) {
		t.Fatalf("view descriptor = %#v, %v, %v", descriptor, ok, err)
	}
	value, found, err := view.Get(testHash(1))
	if err != nil || !found || string(value) != "one" {
		t.Fatalf("get one = %q, %v, %v", value, found, err)
	}
	value[0] = 'X'
	value, found, err = view.Get(testHash(1))
	if err != nil || !found || string(value) != "one" {
		t.Fatalf("get returned mutable storage = %q, %v, %v", value, found, err)
	}
	if _, found, err := view.Get(testHash(2)); err != nil || found {
		t.Fatalf("missing get = found %v, err %v", found, err)
	}

	var keys []Hash
	if err := view.ForEach(context.Background(), func(key Hash, _ []byte) bool {
		keys = append(keys, key)
		return true
	}); err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 || keys[0] != testHash(1) || keys[1] != testHash(3) {
		t.Fatalf("ordered keys = %v", keys)
	}
	key, value, found, err := view.Succ(testHash(1))
	if err != nil || !found || key != testHash(3) || string(value) != "three" {
		t.Fatalf("successor = %x, %q, %v, %v", key, value, found, err)
	}
}

func TestStoreAdvancingKeepsSnapshotStable(t *testing.T) {
	store, err := Open(t.TempDir(), Options{CacheBytes: 1 << 20, MemTableSize: 1 << 20, MaxBatchRecords: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	first := testDescriptor(1, Hash{}, testHash(1), testHash(2), testHash(3))
	importInitial(t, store, first, []Change{{Key: testHash(1), Value: []byte("old")}})
	view, err := store.OpenView(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	second := testDescriptor(2, first.LedgerHash, testHash(4), testHash(5), testHash(6))
	if err := store.Advance(context.Background(), AdvanceRequest{
		Expected:      &first,
		Target:        second,
		Changes:       NewSliceChangeIterator([]Change{{Key: testHash(1), Value: []byte("new")}, {Key: testHash(2), Value: []byte("added")}}),
		Authorization: testAuthorization(first, second),
	}); err != nil {
		t.Fatal(err)
	}
	old, found, err := view.Get(testHash(1))
	if err != nil || !found || string(old) != "old" {
		t.Fatalf("old view = %q, %v, %v", old, found, err)
	}
	_ = view.Close()
	newView, err := store.OpenView(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer newView.Close()
	newValue, found, err := newView.Get(testHash(1))
	if err != nil || !found || string(newValue) != "new" {
		t.Fatalf("new view = %q, %v, %v", newValue, found, err)
	}
	if _, found, err := newView.Get(testHash(3)); err != nil || found {
		t.Fatalf("stale object survived = found %v err %v", found, err)
	}
}

func TestStoreRebaseKeepsSnapshotStable(t *testing.T) {
	store, err := Open(t.TempDir(), Options{CacheBytes: 1 << 20, MemTableSize: 1 << 20, MaxBatchRecords: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	first := testDescriptor(1, Hash{}, testHash(1), testHash(2), testHash(3))
	importInitial(t, store, first, []Change{{Key: testHash(1), Value: []byte("old")}, {Key: testHash(3), Value: []byte("stale")}})
	view, err := store.OpenView(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer view.Close()

	target := testDescriptor(1, testHash(99), testHash(4), testHash(5), testHash(6))
	target.Epoch = first.Epoch + 1
	changes := []Change{
		{Key: testHash(1), Value: []byte("new")},
		{Key: testHash(2), Value: []byte("added")},
		{Key: testHash(3), Delete: true},
	}
	if err := store.Rebase(context.Background(), RebaseRequest{
		Expected: &first, Target: target, Changes: NewSliceChangeIterator(changes),
		Authorization: testAuthorization(first, target),
	}); err != nil {
		t.Fatal(err)
	}
	old, found, err := view.Get(testHash(1))
	if err != nil || !found || string(old) != "old" {
		t.Fatalf("old rebase view = %q, %v, %v", old, found, err)
	}
	if _, found, err := view.Get(testHash(2)); err != nil || found {
		t.Fatalf("old rebase view unexpectedly contains new key: found %v err %v", found, err)
	}
	current, found, err := store.Current()
	if err != nil || !found || !current.Equal(target) {
		t.Fatalf("rebased current = %#v, %v, %v", current, found, err)
	}
	newView, err := store.OpenView(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer newView.Close()
	for key, expected := range map[Hash]string{testHash(1): "new", testHash(2): "added"} {
		value, found, err := newView.Get(key)
		if err != nil || !found || string(value) != expected {
			t.Fatalf("rebased %x = %q, %v, %v", key, value, found, err)
		}
	}
	if _, found, err := newView.Get(testHash(3)); err != nil || found {
		t.Fatalf("rebased stale key = found %v err %v", found, err)
	}
}

func TestStoreRejectsAuthorizationAndParentMismatch(t *testing.T) {
	store, err := Open(t.TempDir(), Options{CacheBytes: 1 << 20, MemTableSize: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	first := testDescriptor(1, Hash{}, testHash(1), testHash(2), testHash(3))
	err = store.Advance(context.Background(), AdvanceRequest{Target: first, Changes: NewSliceChangeIterator(nil)})
	if !errors.Is(err, ErrAuthorization) {
		t.Fatalf("missing initial authorization error = %v", err)
	}
	importInitial(t, store, first, nil)
	second := testDescriptor(2, testHash(99), testHash(4), testHash(5), testHash(6))
	err = store.Advance(context.Background(), AdvanceRequest{
		Expected: &first, Target: second, Changes: NewSliceChangeIterator(nil),
		Authorization: RootClosureAuthorization{SourceRoot: first.StateRoot, TargetRoot: second.StateRoot, Token: []byte("ok")},
	})
	if !errors.Is(err, ErrParentMismatch) {
		t.Fatalf("wrong parent error = %v", err)
	}
	wrongExpected := first
	wrongExpected.Epoch++
	err = store.Rebase(context.Background(), RebaseRequest{
		Expected: &wrongExpected, Target: second, Changes: NewSliceChangeIterator(nil),
		Authorization: testAuthorization(first, second),
	})
	if !errors.Is(err, ErrParentMismatch) {
		t.Fatalf("rebase compare-and-swap error = %v", err)
	}
}

func TestStoreInvalidChangesPreserveAtomicity(t *testing.T) {
	for _, maxRecords := range []int{1, 4} {
		t.Run(fmt.Sprint(maxRecords), func(t *testing.T) {
			store, err := Open(t.TempDir(), Options{CacheBytes: 1 << 20, MemTableSize: 1 << 20, MaxBatchRecords: maxRecords})
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			first := testDescriptor(1, Hash{}, testHash(1), testHash(2), testHash(3))
			importInitial(t, store, first, []Change{{Key: testHash(1), Value: []byte("old")}})
			second := testDescriptor(2, first.LedgerHash, testHash(4), testHash(5), testHash(6))
			err = store.Advance(context.Background(), AdvanceRequest{
				Expected: &first, Target: second,
				Changes:       NewSliceChangeIterator([]Change{{Key: testHash(3), Value: []byte("three")}, {Key: testHash(4), Value: []byte("four")}, {Key: testHash(2), Value: []byte("two")}}),
				Authorization: testAuthorization(first, second),
			})
			if !errors.Is(err, ErrInvalidChangeOrder) {
				t.Fatalf("unsorted changes error = %v", err)
			}
			marker, marked, err := store.Marker()
			if err != nil || marked != (maxRecords == 1) {
				t.Fatalf("marker = %#v, %v, %v", marker, marked, err)
			}
			view, err := store.OpenView(context.Background())
			if marked {
				if view != nil {
					_ = view.Close()
				}
				if !errors.Is(err, ErrUnavailable) || !marker.Target.Equal(second) {
					t.Fatalf("partial transition was available: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer view.Close()
			if value, found, err := view.Get(testHash(1)); err != nil || !found || string(value) != "old" {
				t.Fatalf("old object = %q, %v, %v", value, found, err)
			}
			if _, found, err := view.Get(testHash(3)); err != nil || found {
				t.Fatalf("failed atomic batch leaked a mutation: %v, %v", found, err)
			}
			if current, _, err := store.Current(); err != nil || !current.Equal(first) {
				t.Fatalf("failed atomic batch changed descriptor: %+v, %v", current, err)
			}
		})
	}
}

func TestStoreDeleteAndOversizedRecordStreaming(t *testing.T) {
	store, err := Open(t.TempDir(), Options{CacheBytes: 1 << 20, MemTableSize: 1 << 20, MaxBatchBytes: 32, MaxBatchRecords: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	first := testDescriptor(1, Hash{}, testHash(1), testHash(2), testHash(3))
	large := make([]byte, 256)
	for i := range large {
		large[i] = byte(i)
	}
	importInitial(t, store, first, []Change{{Key: testHash(1), Value: large}, {Key: testHash(2), Value: []byte("keep")}})
	second := testDescriptor(2, first.LedgerHash, testHash(4), testHash(5), testHash(6))
	if err := store.Advance(context.Background(), AdvanceRequest{
		Expected: &first, Target: second,
		Changes: NewSliceChangeIterator([]Change{{Key: testHash(1), Delete: true}}), Authorization: testAuthorization(first, second),
	}); err != nil {
		t.Fatal(err)
	}
	view, err := store.OpenView(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer view.Close()
	if _, found, err := view.Get(testHash(1)); err != nil || found {
		t.Fatalf("deleted object = found %v, err %v", found, err)
	}
	value, found, err := view.Get(testHash(2))
	if err != nil || !found || string(value) != "keep" {
		t.Fatalf("kept object = %q, %v, %v", value, found, err)
	}
}

func TestStoreMemoryBudgetAdmitsOversizedRecordAsSingleBatch(t *testing.T) {
	budget := newFlatTestBudget(512)
	store, err := Open(t.TempDir(), Options{
		CacheBytes: 1 << 20, MemTableSize: 1 << 20, MaxBatchBytes: 32,
		MemoryBudget: budget,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	target := testDescriptor(1, Hash{}, testHash(1), testHash(2), testHash(3))
	value := make([]byte, 256)
	importInitial(t, store, target, []Change{{Key: testHash(1), Value: value}})
	if got := budget.used.Load(); got != 0 {
		t.Fatalf("batch reservation leaked: %d", got)
	}
	view, err := store.OpenView(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer view.Close()
	got, found, err := view.Get(testHash(1))
	if err != nil || !found || len(got) != len(value) {
		t.Fatalf("oversized value = %d bytes, found %v, err %v", len(got), found, err)
	}
}

func TestStoreMemoryBudgetRejectsRecordOverAccount(t *testing.T) {
	budget := newFlatTestBudget(64)
	store, err := Open(t.TempDir(), Options{
		CacheBytes: 1 << 20, MemTableSize: 1 << 20, MaxBatchBytes: 32,
		MemoryBudget: budget,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	target := testDescriptor(1, Hash{}, testHash(1), testHash(2), testHash(3))
	err = store.Advance(context.Background(), AdvanceRequest{
		Target:        target,
		Changes:       NewSliceChangeIterator([]Change{{Key: testHash(1), Value: make([]byte, 256)}}),
		Authorization: RootClosureAuthorization{TargetRoot: target.StateRoot, Token: []byte("genesis-closure")},
	})
	if !errors.Is(err, ErrLimit) {
		t.Fatalf("oversized admission error = %v", err)
	}
	if got := budget.used.Load(); got != 0 {
		t.Fatalf("failed admission leaked: %d", got)
	}
}

func TestStoreActiveViewLimit(t *testing.T) {
	store, err := Open(t.TempDir(), Options{CacheBytes: 1 << 20, MemTableSize: 1 << 20, MaxActiveViews: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	target := testDescriptor(1, Hash{}, testHash(1), testHash(2), testHash(3))
	importInitial(t, store, target, nil)
	first, err := store.OpenView(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.OpenView(context.Background()); !errors.Is(err, ErrLimit) {
		t.Fatalf("second view error = %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := store.OpenView(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	_ = second.Close()
}

func TestStoreOutboxLimitsBlockAdvanceButAllowAck(t *testing.T) {
	store, err := Open(t.TempDir(), Options{CacheBytes: 1 << 20, MemTableSize: 1 << 20, OutboxMaxCount: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	first := testDescriptor(1, Hash{}, testHash(1), testHash(2), testHash(3))
	importInitial(t, store, first, nil)
	second := testDescriptor(2, first.LedgerHash, testHash(4), testHash(5), testHash(6))
	err = store.Advance(context.Background(), AdvanceRequest{
		Expected: &first, Target: second, Changes: NewSliceChangeIterator(nil), Authorization: testAuthorization(first, second),
	})
	if !errors.Is(err, ErrLimit) {
		t.Fatalf("outbox count error = %v", err)
	}
	if err := store.AckOutbox(context.Background(), SemanticCommitID(first.LedgerHash, "flat-state")); err != nil {
		t.Fatal(err)
	}
	if err := store.Advance(context.Background(), AdvanceRequest{
		Expected: &first, Target: second, Changes: NewSliceChangeIterator(nil), Authorization: testAuthorization(first, second),
	}); err != nil {
		t.Fatalf("advance after ack: %v", err)
	}
}

func TestStoreInterruptedChunksResumeFromZero(t *testing.T) {
	path := t.TempDir()
	store, err := Open(path, Options{
		CacheBytes: 1 << 20, MemTableSize: 1 << 20, MaxBatchRecords: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	first := testDescriptor(1, Hash{}, testHash(1), testHash(2), testHash(3))
	importInitial(t, store, first, nil)
	second := testDescriptor(2, first.LedgerHash, testHash(4), testHash(5), testHash(6))
	changes := []Change{{Key: testHash(1), Value: []byte("one")}, {Key: testHash(2), Value: []byte("two")}}
	err = store.Advance(context.Background(), AdvanceRequest{
		Expected: &first, Target: second, Changes: &interruptedChanges{NewSliceChangeIterator(changes)}, Authorization: testAuthorization(first, second),
	})
	if err == nil {
		t.Fatal("expected interrupted changes")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = Open(path, Options{CacheBytes: 1 << 20, MemTableSize: 1 << 20, MaxBatchRecords: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.OpenView(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("open view during recovery = %v", err)
	}
	marker, ok, err := store.Marker()
	if err != nil || !ok || !marker.Target.Equal(second) {
		t.Fatalf("marker = %#v, %v, %v", marker, ok, err)
	}
	if err := store.Resume(context.Background(), ResumeRequest{
		Authorization: testAuthorization(first, second), Changes: NewSliceChangeIterator(changes),
	}); err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	view, err := store.OpenView(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer view.Close()
	for key, expected := range map[Hash]string{testHash(1): "one", testHash(2): "two"} {
		value, found, err := view.Get(key)
		if err != nil || !found || string(value) != expected {
			t.Fatalf("recovered %x = %q, %v, %v", key, value, found, err)
		}
	}
}

func TestStoreInterruptedRebaseChunksResumeFromZero(t *testing.T) {
	path := t.TempDir()
	store, err := Open(path, Options{
		CacheBytes: 1 << 20, MemTableSize: 1 << 20, MaxBatchRecords: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	first := testDescriptor(1, Hash{}, testHash(1), testHash(2), testHash(3))
	importInitial(t, store, first, nil)
	target := testDescriptor(1, testHash(99), testHash(4), testHash(5), testHash(6))
	target.Epoch = first.Epoch + 1
	changes := []Change{{Key: testHash(1), Value: []byte("one")}, {Key: testHash(2), Value: []byte("two")}}
	err = store.Rebase(context.Background(), RebaseRequest{
		Expected: &first, Target: target, Changes: &interruptedChanges{NewSliceChangeIterator(changes)},
		Authorization: testAuthorization(first, target),
	})
	if err == nil {
		t.Fatal("expected interrupted rebase changes")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = Open(path, Options{CacheBytes: 1 << 20, MemTableSize: 1 << 20, MaxBatchRecords: 1})
	if err != nil {
		t.Fatal(err)
	}
	marker, ok, err := store.Marker()
	if err != nil || !ok || !marker.Target.Equal(target) {
		t.Fatalf("rebase marker = %#v, %v, %v", marker, ok, err)
	}
	if err := store.Resume(context.Background(), ResumeRequest{
		Authorization: testAuthorization(first, target), Changes: NewSliceChangeIterator(changes),
	}); err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	view, err := store.OpenView(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer view.Close()
	for key, expected := range map[Hash]string{testHash(1): "one", testHash(2): "two"} {
		value, found, err := view.Get(key)
		if err != nil || !found || string(value) != expected {
			t.Fatalf("recovered rebased %x = %q, %v, %v", key, value, found, err)
		}
	}
}

func TestStoreOutboxAckAndReopen(t *testing.T) {
	path := t.TempDir()
	store, err := Open(path, Options{CacheBytes: 1 << 20, MemTableSize: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	target := testDescriptor(1, Hash{}, testHash(1), testHash(2), testHash(3))
	importInitial(t, store, target, nil)
	commitID := SemanticCommitID(target.LedgerHash, "flat-state")
	pending, err := store.PendingOutbox(context.Background(), 10)
	if err != nil || len(pending) != 1 || pending[0].CommitID != commitID {
		t.Fatalf("pending = %#v, %v", pending, err)
	}
	if err := store.AckOutbox(context.Background(), commitID); err != nil {
		t.Fatal(err)
	}
	if err := store.AckOutbox(context.Background(), commitID); err != nil {
		t.Fatalf("idempotent ack: %v", err)
	}
	if pending, err := store.PendingOutbox(context.Background(), 10); err != nil || len(pending) != 0 {
		t.Fatalf("pending after ack = %#v, %v", pending, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(path, Options{CacheBytes: 1 << 20, MemTableSize: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	got, ok, err := store.Current()
	if err != nil || !ok || !got.Equal(target) {
		t.Fatalf("reopened current = %#v, %v, %v", got, ok, err)
	}
}

func TestStoreCloseRacesViewOperations(t *testing.T) {
	store, err := Open(t.TempDir(), Options{CacheBytes: 1 << 20, MemTableSize: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	target := testDescriptor(1, Hash{}, testHash(1), testHash(2), testHash(3))
	importInitial(t, store, target, []Change{{Key: testHash(1), Value: []byte("value")}})
	view, err := store.OpenView(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	done := make(chan struct{})
	go func() {
		close(started)
		for i := 0; i < 100; i++ {
			_, _, _ = view.Get(testHash(1))
		}
		close(done)
	}()
	<-started
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("view operation did not finish after store close")
	}
}

func TestStorePathIsCreated(t *testing.T) {
	path := t.TempDir() + "/nested/store"
	store, err := Open(path, Options{CacheBytes: 1 << 20, MemTableSize: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
}

// The first chunk has been applied by the time the caller requests EOF.
type interruptedChanges struct{ *SliceChangeIterator }

func (i *interruptedChanges) Next(ctx context.Context) (Change, bool, error) {
	change, ok, err := i.SliceChangeIterator.Next(ctx)
	if err == nil && !ok {
		return Change{}, false, errors.New("interrupted change stream")
	}
	return change, ok, err
}

func TestStorePublicationSyncsAllChunks(t *testing.T) {
	for _, records := range []int{1, 8} {
		t.Run(fmt.Sprint(records), func(t *testing.T) {
			var syncs int
			store, err := Open(t.TempDir(), Options{
				CacheBytes: 1 << 20, MemTableSize: 1 << 20, MaxBatchRecords: 1,
				Sync: func(db *pebble.DB) error {
					syncs++
					return db.LogData(nil, pebble.Sync)
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			target := testDescriptor(1, Hash{}, testHash(1), testHash(2), testHash(3))
			changes := make([]Change, records)
			for i := range changes {
				changes[i] = Change{Key: testHash(byte(i + 1)), Value: []byte("value")}
			}
			importInitial(t, store, target, changes)
			wantSyncs := 1
			if records > 1 {
				wantSyncs = 2
			}
			if syncs != wantSyncs {
				t.Fatalf("transition used %d syncs; want %d", syncs, wantSyncs)
			}
			view, err := store.OpenView(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer view.Close()
			for _, change := range changes {
				value, found, err := view.Get(change.Key)
				if err != nil || !found || string(value) != "value" {
					t.Fatalf("published key %x = %q, %v, %v", change.Key, value, found, err)
				}
			}
			if _, advancing, err := store.Marker(); err != nil || advancing {
				t.Fatalf("completed transition marker: advancing=%v err=%v", advancing, err)
			}
		})
	}
}

func TestStoreUncertainTransitionReopensConsistently(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		maxRecords, failSync int
	}{
		{"atomic", 2, 1}, {"marker", 1, 1}, {"chunked-final", 1, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := t.TempDir()
			var armed bool
			var syncs int
			store, err := Open(path, Options{
				CacheBytes: 1 << 20, MemTableSize: 1 << 20, MaxBatchRecords: tc.maxRecords,
				Sync: func(db *pebble.DB) error {
					if err := db.LogData(nil, pebble.Sync); err != nil {
						return err
					}
					if armed {
						syncs++
						if syncs == tc.failSync {
							return errors.New("uncertain sync outcome")
						}
					}
					return nil
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			first := testDescriptor(1, Hash{}, testHash(1), testHash(2), testHash(3))
			importInitial(t, store, first, []Change{{Key: testHash(1), Value: []byte("old")}})
			target := testDescriptor(2, first.LedgerHash, testHash(4), testHash(5), testHash(6))
			changes := []Change{{Key: testHash(1), Value: []byte("new")}, {Key: testHash(2), Value: []byte("added")}}
			armed = true
			err = store.Advance(context.Background(), AdvanceRequest{
				Expected: &first, Target: target, Changes: NewSliceChangeIterator(changes),
				Authorization: testAuthorization(first, target),
			})
			if !errors.Is(err, errSyncUncertain) {
				t.Fatalf("uncertain transition: %v", err)
			}
			if view, err := store.OpenView(context.Background()); !errors.Is(err, ErrUnavailable) {
				if view != nil {
					_ = view.Close()
				}
				t.Fatalf("uncertain transition exposed a new view: %v", err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			store, err = Open(path, Options{CacheBytes: 1 << 20, MemTableSize: 1 << 20, MaxBatchRecords: 1})
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			if tc.name == "marker" {
				if err := store.Resume(context.Background(), ResumeRequest{
					Authorization: testAuthorization(first, target), Changes: NewSliceChangeIterator(changes),
				}); err != nil {
					t.Fatal(err)
				}
			}
			view, err := store.OpenView(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer view.Close()
			if current, found, err := store.Current(); err != nil || !found || current != target {
				t.Fatalf("recovered descriptor = %+v, %v, %v", current, found, err)
			}
			for _, change := range changes {
				value, found, err := view.Get(change.Key)
				if err != nil || !found || string(value) != string(change.Value) {
					t.Fatalf("recovered key %x = %q, %v, %v", change.Key, value, found, err)
				}
			}
			if stats := store.Stats(); stats.OutboxCount != 2 {
				t.Fatalf("publication obligation missing: %+v", stats)
			}
		})
	}
}
