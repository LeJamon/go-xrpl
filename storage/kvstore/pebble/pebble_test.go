package pebble_test

import (
	"bytes"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/storage/kvstore"
	"github.com/LeJamon/go-xrpl/storage/kvstore/kvstoretest"
	"github.com/LeJamon/go-xrpl/storage/kvstore/pebble"
)

// The ledger-persist fsync path depends on the store exposing a real Sync;
// keep this explicit so it can never regress to a silently-missed type assert.
var _ interface{ Sync() error } = (*pebble.Store)(nil)

func TestStoreConformance(t *testing.T) {
	kvstoretest.RunConformance(t, func(t *testing.T) kvstore.KeyValueStore {
		store, err := pebble.New(t.TempDir(), pebble.Options{})
		if err != nil {
			t.Fatalf("open pebble: %v", err)
		}
		t.Cleanup(func() { _ = store.Close() })
		return store
	})
}

// TestStorePersistence verifies the production backend actually persists data
// to disk across a close/reopen cycle.
func TestStorePersistence(t *testing.T) {
	dir := t.TempDir()

	store, err := pebble.New(dir, pebble.Options{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := store.Put([]byte("durable"), []byte("value")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened, err := pebble.New(dir, pebble.Options{})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()

	got, err := reopened.Get([]byte("durable"))
	if err != nil {
		t.Fatalf("Get after reopen: %v", err)
	}
	if !bytes.Equal(got, []byte("value")) {
		t.Fatalf("Get after reopen = %q, want %q", got, "value")
	}
}

// TestStoreSyncDurability verifies Sync succeeds after non-durable writes and
// that the data is durable across a close/reopen cycle.
func TestStoreSyncDurability(t *testing.T) {
	dir := t.TempDir()

	store, err := pebble.New(dir, pebble.Options{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := store.Put([]byte("k"), []byte("v")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	b, err := store.NewBatch()
	if err != nil {
		t.Fatalf("NewBatch: %v", err)
	}
	if err := b.Put([]byte("k2"), []byte("v2")); err != nil {
		t.Fatalf("batch Put: %v", err)
	}
	if err := b.Write(); err != nil {
		t.Fatalf("batch Write: %v", err)
	}
	if err := b.Close(); err != nil {
		t.Fatalf("batch Close: %v", err)
	}
	if err := store.Sync(); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened, err := pebble.New(dir, pebble.Options{})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()
	for _, kv := range [][2]string{{"k", "v"}, {"k2", "v2"}} {
		got, err := reopened.Get([]byte(kv[0]))
		if err != nil || !bytes.Equal(got, []byte(kv[1])) {
			t.Fatalf("Get(%q) after reopen = %q, %v; want %q", kv[0], got, err, kv[1])
		}
	}
}

// TestConcurrentCloseNoPanic races every point operation against Close.
// Pebble panics ("pebble: closed") on any op against a closed DB, so the old
// check-then-act atomic guard let that panic escape once Close landed in the
// window between the closed-check and the s.db call. Each op must instead be
// serialised against Close by the RWMutex and return cleanly. Run under -race,
// this also proves the closed flag and db handle are never touched
// unsynchronised. Every op must return success, ErrClosed, or ErrNotFound —
// never a panic.
func TestConcurrentCloseNoPanic(t *testing.T) {
	dir := t.TempDir()
	store, err := pebble.New(dir, pebble.Options{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := store.Put([]byte("seed"), []byte("v")); err != nil {
		t.Fatalf("seed Put: %v", err)
	}

	accept := func(op string, err error) {
		if err == nil ||
			errors.Is(err, kvstore.ErrClosed) ||
			errors.Is(err, kvstore.ErrNotFound) {
			return
		}
		t.Errorf("%s: unexpected error %v", op, err)
	}

	const workers = 16
	var wg sync.WaitGroup
	var panics atomic.Int64
	start := make(chan struct{})

	for i := range workers {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					panics.Add(1)
					t.Errorf("worker %d panicked (use-after-close leaked through): %v", id, r)
				}
			}()
			<-start
			key := []byte{byte(id)}
			for range 300 {
				accept("Put", store.Put(key, []byte("v")))
				_, gerr := store.Get(key)
				accept("Get", gerr)
				b, berr := store.NewBatch()
				accept("NewBatch", berr)
				if berr == nil {
					accept("Batch.Put", b.Put(key, []byte("v")))
					accept("Batch.Write", b.Write())
					accept("Batch.Close", b.Close())
				}
				accept("Sync", store.Sync())
			}
		}(i)
	}

	close(start)
	// Let the workers get going so some ops land before Close and some after.
	time.Sleep(time.Millisecond)
	if err := store.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	wg.Wait()

	if n := panics.Load(); n != 0 {
		t.Fatalf("%d worker(s) panicked on use-after-close", n)
	}
	// Close is idempotent.
	if err := store.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
	// Every op is rejected once closed.
	if _, err := store.Get([]byte("seed")); !errors.Is(err, kvstore.ErrClosed) {
		t.Errorf("Get after close = %v, want ErrClosed", err)
	}
}

func TestIteratorPinsStoreUntilClose(t *testing.T) {
	store, err := pebble.New(t.TempDir(), pebble.Options{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	it, err := store.NewIterator(nil, nil)
	if err != nil {
		t.Fatalf("NewIterator: %v", err)
	}

	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		close(started)
		done <- store.Close()
	}()
	<-started
	select {
	case err := <-done:
		t.Fatalf("Close completed while iterator was open: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	if err := it.Close(); err != nil {
		t.Fatalf("iterator Close: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("store Close: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("store Close did not resume after iterator Close")
	}
}
