package pebble

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/LeJamon/go-xrpl/storage/kvstore"
	cockroachpebble "github.com/cockroachdb/pebble"
	"github.com/cockroachdb/pebble/vfs"
	"github.com/cockroachdb/pebble/vfs/errorfs"
	"github.com/stretchr/testify/require"
)

func TestPromoteBatchPropagatesLazyValueReadError(t *testing.T) {
	const archiveKey = "k/2"
	archiveValue := []byte("archive-value")
	store, fault := newLazyValuePromotionFixture(t)

	fault.Arm()
	batchResults, batchErr := store.GetBatch(t.Context(), [][]byte{[]byte(archiveKey)}, 1, 1<<20)
	if !errors.Is(batchErr, errorfs.ErrInjected) {
		t.Fatalf("GetBatch error = %v, want injected lazy read error", batchErr)
	}
	if len(batchResults) != 0 {
		t.Fatalf("GetBatch returned %d results after read failure", len(batchResults))
	}
	promotions, stats, err := store.PromoteBatch([][]byte{[]byte("k/1"), []byte(archiveKey)}, 1<<20)
	if !errors.Is(err, errorfs.ErrInjected) {
		t.Fatalf("PromoteBatch error = %v, want injected lazy read error", err)
	}
	if len(promotions) != 0 {
		t.Fatalf("PromoteBatch returned %d promotions after read failure", len(promotions))
	}
	if stats.Promoted != 0 || stats.Batches != 0 {
		t.Fatalf("failed promotion stats = %+v, want no staged write", stats)
	}
	if _, err := store.writable.Get([]byte(archiveKey)); !errors.Is(err, kvstore.ErrNotFound) {
		t.Fatalf("writable value after failed promotion: %v, want ErrNotFound", err)
	}
	if _, err := store.writable.Get([]byte("k/1")); !errors.Is(err, kvstore.ErrNotFound) {
		t.Fatalf("staged value committed before later lazy read failure: %v", err)
	}

	writableValue := []byte("writable-value")
	if err := store.writable.Put([]byte(archiveKey), writableValue); err != nil {
		t.Fatalf("replace writable value: %v", err)
	}
	fault.Arm()
	promotions, stats, err = store.PromoteBatch([][]byte{[]byte(archiveKey)}, 1<<20)
	if err != nil {
		t.Fatalf("writable precedence over archive read error: %v", err)
	}
	if len(promotions) != 1 || !promotions[0].Found || !bytes.Equal(promotions[0].Value, writableValue) {
		t.Fatalf("writable precedence promotions = %+v, want writable value", promotions)
	}
	if stats.WritableHits != 1 || stats.Promoted != 0 || stats.ArchiveLookups != 0 || stats.ArchiveLookupsAvoided != 1 {
		t.Fatalf("writable precedence stats = %+v, want one writable hit and no archive lookup or promotion", stats)
	}
	if err := store.writable.db.Delete([]byte(archiveKey), nil); err != nil {
		t.Fatalf("remove writable override: %v", err)
	}

	writeErr := make(chan error, 1)
	onFault := func() { writeErr <- store.Put([]byte(archiveKey), writableValue) }
	fault.onFault.Store(&onFault)
	fault.Arm()
	promotions, stats, err = store.PromoteBatch([][]byte{[]byte(archiveKey)}, 1<<20)
	if err != nil {
		t.Fatalf("concurrent writable replacement did not supersede lazy archive error: %v", err)
	}
	if err := <-writeErr; err != nil {
		t.Fatalf("concurrent Put: %v", err)
	}
	if len(promotions) != 1 || !bytes.Equal(promotions[0].Value, writableValue) || stats.ArchiveLookups != 1 || stats.Promoted != 0 {
		t.Fatalf("concurrent writable precedence: promotions=%+v stats=%+v", promotions, stats)
	}
	if err := store.writable.db.Delete([]byte(archiveKey), nil); err != nil {
		t.Fatalf("remove concurrent writable override: %v", err)
	}

	fault.Disarm()
	promotions, stats, err = store.PromoteBatch([][]byte{[]byte(archiveKey)}, 1<<20)
	if err != nil {
		t.Fatalf("retry PromoteBatch: %v", err)
	}
	if len(promotions) != 1 || !promotions[0].Found || !bytes.Equal(promotions[0].Value, archiveValue) {
		t.Fatalf("retry promotions = %+v, want archive value", promotions)
	}
	if stats.Promoted != 1 || stats.Batches != 1 {
		t.Fatalf("retry promotion stats = %+v, want one committed promotion", stats)
	}
	if got, err := store.writable.Get([]byte(archiveKey)); err != nil || !bytes.Equal(got, archiveValue) {
		t.Fatalf("writable value after retry = %q, %v; want %q", got, err, archiveValue)
	}

	fault.Disarm()
}

func TestPromotionCopyForwardSupersedesCachedArchiveError(t *testing.T) {
	const archiveKey = "k/2"
	archiveValue := []byte("archive-value")
	store, fault := newLazyValuePromotionFixture(t)
	fault.Arm()
	keys := [][]byte{[]byte("k/1"), []byte(archiveKey)}
	store.mu.RLock()
	defer store.mu.RUnlock()
	store.archiveMu.RLock()
	defer store.archiveMu.RUnlock()
	candidates, versions, err := store.prefetchPromotionPass(
		keys, 1<<20, nil, [rotatingStoreMutationStripes]uint64{}, &kvstore.PromotionStats{},
	)
	if err != nil || len(candidates) != 2 || !errors.Is(candidates[1].err, errorfs.ErrInjected) {
		t.Fatalf("prefetched archive failure: candidates=%+v err=%v", candidates, err)
	}

	fault.Disarm()
	// Complete another batch between this batch's prefetch and commit.
	_, _, err = store.PromoteBatch([][]byte{[]byte(archiveKey)}, 1<<20)
	if err != nil {
		t.Fatalf("interleaved promotion: %v", err)
	}
	locked := store.lockMutations(keys)
	if store.mutationVersions[mutationStripe(keys[1])] != versions[mutationStripe(keys[1])] {
		store.unlockMutations(&locked)
		t.Fatal("copy-forward invalidated a logical value observation")
	}
	var stats kvstore.PromotionStats
	promotions, err := store.commitPromotions(keys, candidates, 1<<20, nil, &stats)
	store.unlockMutations(&locked)
	if err != nil || len(promotions) != 2 || !bytes.Equal(promotions[1].Value, archiveValue) {
		t.Fatalf("cached error supersession: promotions=%+v err=%v", promotions, err)
	}
	if stats.WritableHits != 1 || stats.Promoted != 1 || stats.ArchiveLookupsAvoided != 1 {
		t.Fatalf("cached error supersession stats: %+v", stats)
	}
}

func newLazyValuePromotionFixture(t *testing.T) (*RotatingStore, *lazyValueReadFault) {
	t.Helper()
	const archiveKey = "k/2"
	archiveValue := []byte("archive-value")
	comparer := testValueBlockComparer()

	archivePath := filepath.Join(t.TempDir(), "archive")
	if err := os.MkdirAll(archivePath, 0o755); err != nil {
		t.Fatalf("create archive directory: %v", err)
	}
	fixtureCache := cockroachpebble.NewCache(16 << 20)
	fixtureDB, err := cockroachpebble.Open(
		archivePath,
		testValueBlockOptions(vfs.Default, fixtureCache, comparer),
	)
	if err != nil {
		fixtureCache.Unref()
		t.Fatalf("open archive fixture: %v", err)
	}
	t.Cleanup(func() {
		if fixtureDB != nil {
			_ = fixtureDB.Close()
		}
		fixtureCache.Unref()
	})
	if err := fixtureDB.Set([]byte("k/1"), []byte("older-value"), nil); err != nil {
		t.Fatalf("write first archive value: %v", err)
	}
	if err := fixtureDB.Set([]byte(archiveKey), archiveValue, nil); err != nil {
		t.Fatalf("write archive value: %v", err)
	}
	if err := fixtureDB.Flush(); err != nil {
		t.Fatalf("flush archive fixture: %v", err)
	}
	if valueBlocks := valueBlockBytes(t, fixtureDB); valueBlocks == 0 {
		t.Fatal("archive fixture did not persist any value blocks")
	}
	err = fixtureDB.Close()
	fixtureDB = nil
	if err != nil {
		t.Fatalf("close archive fixture: %v", err)
	}

	cache := cockroachpebble.NewCache(16 << 20)
	fault := newLazyValueReadFault()
	archiveFS := errorfs.Wrap(vfs.Default, fault)
	archiveOptions := testValueBlockOptions(readOnlyFS{FS: archiveFS}, cache, comparer)
	archiveOptions.ReadOnly = true
	archiveDB, err := cockroachpebble.Open(archivePath, archiveOptions)
	if err != nil {
		cache.Unref()
		t.Fatalf("reopen archive: %v", err)
	}
	writablePath := filepath.Join(t.TempDir(), "writable")
	if err := os.MkdirAll(writablePath, 0o755); err != nil {
		_ = archiveDB.Close()
		cache.Unref()
		t.Fatalf("create writable directory: %v", err)
	}
	writableDB, err := cockroachpebble.Open(
		writablePath,
		testValueBlockOptions(vfs.Default, cache, comparer),
	)
	if err != nil {
		_ = archiveDB.Close()
		cache.Unref()
		t.Fatalf("open writable: %v", err)
	}
	store := &RotatingStore{
		writable:   &Store{db: writableDB},
		archive:    &Store{db: archiveDB, readOnly: true},
		blockCache: cache,
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close rotating store: %v", err)
		}
	})

	prime, err := archiveDB.NewIter(nil)
	if err != nil {
		t.Fatalf("open priming iterator: %v", err)
	}
	if !prime.SeekGE([]byte(archiveKey)) || !bytes.Equal(prime.Key(), []byte(archiveKey)) {
		_ = prime.Close()
		t.Fatal("archive fixture key was not found")
	}
	lazy := prime.LazyValue()
	if lazy.Fetcher == nil {
		_ = prime.Close()
		t.Fatal("archive fixture value was not lazy")
	}
	if got := lazy.Len(); got != len(archiveValue) {
		_ = prime.Close()
		t.Fatalf("lazy value length = %d, want %d", got, len(archiveValue))
	}
	if err := prime.Close(); err != nil {
		t.Fatalf("close priming iterator: %v", err)
	}

	return store, fault
}

func testValueBlockComparer() *cockroachpebble.Comparer {
	comparer := *cockroachpebble.DefaultComparer
	comparer.Name = "goxrpl.lazy-value-test"
	comparer.Split = func(key []byte) int { return min(len(key), 1) }
	return &comparer
}

func testValueBlockOptions(
	fs vfs.FS,
	cache *cockroachpebble.Cache,
	comparer *cockroachpebble.Comparer,
) *cockroachpebble.Options {
	options := &cockroachpebble.Options{
		FS:                 fs,
		Cache:              cache,
		Comparer:           comparer,
		FormatMajorVersion: cockroachpebble.FormatSSTableValueBlocks,
	}
	options.Experimental.EnableValueBlocks = func() bool { return true }
	return options
}

func valueBlockBytes(t *testing.T, db *cockroachpebble.DB) uint64 {
	t.Helper()
	tables, err := db.SSTables(cockroachpebble.WithProperties())
	if err != nil {
		t.Fatalf("read table properties: %v", err)
	}
	var total uint64
	for _, level := range tables {
		for _, table := range level {
			total += table.Properties.ValueBlocksSize
		}
	}
	return total
}

type lazyValueReadFault struct {
	armed   atomic.Bool
	onFault atomic.Pointer[func()]
}

func newLazyValueReadFault() *lazyValueReadFault {
	return &lazyValueReadFault{}
}

func (f *lazyValueReadFault) Arm() {
	f.armed.Store(true)
}

func (f *lazyValueReadFault) Disarm() {
	f.armed.Store(false)
}

func (f *lazyValueReadFault) MaybeError(op errorfs.Op, path string) error {
	if !f.armed.Load() || filepath.Ext(path) != ".sst" {
		return nil
	}
	if op == errorfs.OpFileRead || op == errorfs.OpFileReadAt {
		if callback := f.onFault.Swap(nil); callback != nil {
			(*callback)()
		}
		return errorfs.ErrInjected
	}
	return nil
}

func TestPromotionPrefetchStopsWritablePayloadAtArchivePrefix(t *testing.T) {
	fs := vfs.NewMem()
	cache := cockroachpebble.NewCache(8 << 20)
	t.Cleanup(cache.Unref)
	comparer := testValueBlockComparer()
	writable, err := cockroachpebble.Open("writable", testValueBlockOptions(fs, cache, comparer))
	require.NoError(t, err)
	require.NoError(t, writable.Set([]byte("b/1"), bytes.Repeat([]byte{1}, 512), nil))
	require.NoError(t, writable.Set([]byte("b/2"), bytes.Repeat([]byte{2}, 512), nil))
	require.NoError(t, writable.Flush())
	require.Positive(t, valueBlockBytes(t, writable))
	require.NoError(t, writable.Close())

	fault := newLazyValueReadFault()
	loaded := make(chan struct{})
	options := testValueBlockOptions(errorfs.Wrap(fs, fault), cache, comparer)
	options.EventListener = &cockroachpebble.EventListener{
		TableStatsLoaded: func(cockroachpebble.TableStatsInfo) { close(loaded) },
	}
	writable, err = cockroachpebble.Open("writable", options)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, writable.Close()) })
	waitPromotionTableStats(t, loaded)
	prime, err := writable.NewIter(nil)
	require.NoError(t, err)
	require.True(t, prime.SeekGE([]byte("a/1")))
	require.True(t, prime.SeekGE([]byte("b/2")))
	require.NotNil(t, prime.LazyValue().Fetcher)
	require.NoError(t, prime.Close())

	archive, err := cockroachpebble.Open("archive", testValueBlockOptions(fs, cache, comparer))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, archive.Close()) })
	archiveValue := bytes.Repeat([]byte{3}, 1024)
	require.NoError(t, archive.Set([]byte("a/1"), archiveValue, nil))
	store := &RotatingStore{writable: &Store{db: writable}, archive: &Store{db: archive}}
	fault.Arm()
	t.Cleanup(fault.Disarm)

	promotions, stats, err := store.PromoteBatch([][]byte{[]byte("a/1"), []byte("b/2")}, len(archiveValue))
	require.NoError(t, err)
	require.Len(t, promotions, 1)
	require.Equal(t, []byte("a/1"), promotions[0].Key)
	require.Equal(t, archiveValue, promotions[0].Value)
	require.Equal(t, 1, stats.Promoted)
	_, _, err = store.PromoteBatch([][]byte{[]byte("b/2")}, len(archiveValue))
	require.ErrorIs(t, err, errorfs.ErrInjected)
}
