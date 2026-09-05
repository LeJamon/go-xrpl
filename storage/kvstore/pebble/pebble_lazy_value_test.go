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
)

func TestPromoteBatchPropagatesLazyValueReadError(t *testing.T) {
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

	fault.Arm()
	promotions, stats, err := store.PromoteBatch([][]byte{[]byte(archiveKey)}, 1<<20)
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
	if stats.WritableHits != 1 || stats.Promoted != 0 {
		t.Fatalf("writable precedence stats = %+v, want one writable hit and no promotion", stats)
	}
	if err := store.writable.db.Delete([]byte(archiveKey), nil); err != nil {
		t.Fatalf("remove writable override: %v", err)
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
	armed atomic.Bool
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
		return errorfs.ErrInjected
	}
	return nil
}
