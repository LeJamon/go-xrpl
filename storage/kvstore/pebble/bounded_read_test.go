package pebble

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/LeJamon/go-xrpl/storage/kvstore"
)

func TestRotatingStoreGetBoundedReadsWritableAndArchive(t *testing.T) {
	store, err := NewRotating(t.TempDir(), Options{BlockCacheBytes: 8 << 20, MaxOpenFiles: 200})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	key := []byte("key")
	archiveValue := []byte("archive-value")
	if err := store.Put(key, archiveValue); err != nil {
		t.Fatal(err)
	}
	committed, err := store.Rotate(11, 1)
	if err != nil || !committed {
		t.Fatalf("Rotate = (%v, %v), want committed", committed, err)
	}

	got, err := store.GetBounded(context.Background(), key, len(archiveValue))
	if err != nil || !bytes.Equal(got, archiveValue) {
		t.Fatalf("archive GetBounded = %q, %v; want %q", got, err, archiveValue)
	}
	if _, err := store.GetBounded(context.Background(), key, len(archiveValue)-1); !errors.Is(err, kvstore.ErrValueTooLarge) {
		t.Fatalf("oversized archive GetBounded error = %v, want ErrValueTooLarge", err)
	}

	writableValue := []byte("writable-value")
	if err := store.Put(key, writableValue); err != nil {
		t.Fatal(err)
	}
	got, err = store.GetBounded(context.Background(), key, len(writableValue))
	if err != nil || !bytes.Equal(got, writableValue) {
		t.Fatalf("writable GetBounded = %q, %v; want %q", got, err, writableValue)
	}
	if _, err := store.GetBounded(context.Background(), key, len(writableValue)-1); !errors.Is(err, kvstore.ErrValueTooLarge) {
		t.Fatalf("oversized writable GetBounded error = %v, want ErrValueTooLarge", err)
	}
}

func TestRotatingStoreGetBoundedOversizedArchiveDoesNotPromoteOrCopy(t *testing.T) {
	store, err := NewRotating(t.TempDir(), Options{BlockCacheBytes: 8 << 20, MaxOpenFiles: 200})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	key := []byte("large")
	value := bytes.Repeat([]byte{0x5a}, 1<<20)
	if err := store.Put(key, value); err != nil {
		t.Fatal(err)
	}
	committed, err := store.Rotate(11, 1)
	if err != nil || !committed {
		t.Fatalf("Rotate = (%v, %v), want committed", committed, err)
	}

	var rejectedErr error
	rejectedAllocs := testing.AllocsPerRun(20, func() {
		got, err := store.GetBounded(context.Background(), key, len(value)-1)
		if got != nil || !errors.Is(err, kvstore.ErrValueTooLarge) {
			rejectedErr = fmt.Errorf("GetBounded = (%d bytes, %v)", len(got), err)
		}
	})
	if rejectedErr != nil {
		t.Fatal(rejectedErr)
	}

	if _, err := store.writable.Get(key); !errors.Is(err, kvstore.ErrNotFound) {
		t.Fatalf("oversized archive read changed writable generation: %v", err)
	}
	archiveValue, err := store.archive.Get(key)
	if err != nil || !bytes.Equal(archiveValue, value) {
		t.Fatalf("archive value after rejected read = %d bytes, %v; want %d", len(archiveValue), err, len(value))
	}

	var acceptedErr error
	acceptedAllocs := testing.AllocsPerRun(20, func() {
		got, err := store.GetBounded(context.Background(), key, len(value))
		if err != nil || !bytes.Equal(got, value) {
			acceptedErr = fmt.Errorf("GetBounded = (%d bytes, %v)", len(got), err)
		}
	})
	if acceptedErr != nil {
		t.Fatal(acceptedErr)
	}
	if rejectedAllocs >= acceptedAllocs {
		t.Fatalf("oversized read allocations = %v, accepted read allocations = %v; oversized path copied a value", rejectedAllocs, acceptedAllocs)
	}
}

func TestRotatingStoreGetBoundedConcurrentRotation(t *testing.T) {
	store, err := NewRotating(t.TempDir(), Options{BlockCacheBytes: 8 << 20, MaxOpenFiles: 200})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	key := []byte("key")
	if err := store.Put(key, []byte("initial")); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	readErrs := make(chan error, 1)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for range 200 {
			value, err := store.GetBounded(context.Background(), key, 128)
			if err != nil && !errors.Is(err, kvstore.ErrNotFound) {
				select {
				case readErrs <- err:
				default:
				}
				return
			}
			if value != nil && len(value) == 0 {
				select {
				case readErrs <- errors.New("bounded read returned an empty non-nil value"):
				default:
				}
				return
			}
		}
	}()
	for sequence := uint32(11); sequence < 31; sequence++ {
		if err := store.Put(key, []byte("rotation-value")); err != nil {
			t.Fatal(err)
		}
		committed, err := store.Rotate(sequence, 1)
		if err != nil || !committed {
			t.Fatalf("Rotate(%d) = (%v, %v), want committed", sequence, committed, err)
		}
	}
	wg.Wait()
	select {
	case err := <-readErrs:
		t.Fatal(err)
	default:
	}
}
