package pebble

import (
	"context"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	cockroachpebble "github.com/cockroachdb/pebble"
	"github.com/cockroachdb/pebble/vfs"
	"github.com/cockroachdb/pebble/vfs/errorfs"
	"github.com/stretchr/testify/require"
)

func TestStoreGetBatchCancellationWaitsForClose(t *testing.T) {
	store, fault := newBlockingBatchReadStore(t)
	archive := store.archive
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	fault.Arm()

	readDone := make(chan error, 1)
	go func() {
		_, err := archive.GetBatch(ctx, [][]byte{[]byte("archive")}, 1, 1024)
		readDone <- err
	}()
	fault.Wait(t)
	cancel()

	closeDone := make(chan error, 1)
	go func() { closeDone <- archive.Close() }()
	select {
	case err := <-closeDone:
		t.Fatalf("Close completed while GetBatch was blocked in a read: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	fault.Release()
	require.ErrorIs(t, <-readDone, context.Canceled)
	require.NoError(t, <-closeDone)
}

func TestRotatingStoreGetBatchCancellationWaitsForRotation(t *testing.T) {
	store, fault := newBlockingBatchReadStore(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	fault.Arm()

	readDone := make(chan error, 1)
	go func() {
		_, err := store.GetBatch(ctx, [][]byte{[]byte("archive")}, 1, 1024)
		readDone <- err
	}()
	fault.Wait(t)
	cancel()

	rotateDone := make(chan error, 1)
	go func() {
		_, err := store.Rotate(21, 12)
		rotateDone <- err
	}()
	select {
	case err := <-rotateDone:
		t.Fatalf("Rotate completed while GetBatch was blocked in a read: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	fault.Release()
	require.ErrorIs(t, <-readDone, context.Canceled)
	require.NoError(t, <-rotateDone)
}

func newBlockingBatchReadStore(t *testing.T) (*RotatingStore, *blockingBatchReadFault) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "nodes")
	options := Options{BlockCacheBytes: 16 << 20, MaxOpenFiles: 200}
	store, err := NewRotating(path, options)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	require.NoError(t, store.Put([]byte("archive"), []byte("value")))
	require.NoError(t, store.writable.db.Flush())
	committed, err := store.Rotate(11, 1)
	require.NoError(t, err)
	require.True(t, committed)

	fault := newBlockingBatchReadFault()
	require.NoError(t, store.archive.Close())
	archiveOptions := makePebbleOptions(store.options, store.blockCache)
	archiveOptions.ReadOnly = true
	archiveOptions.FS = readOnlyFS{FS: errorfs.Wrap(vfs.Default, fault)}
	archiveDB, err := cockroachpebble.Open(store.archivePath, archiveOptions)
	require.NoError(t, err)
	store.archive = &Store{db: archiveDB, readOnly: true}
	t.Cleanup(fault.Release)
	return store, fault
}

type blockingBatchReadFault struct {
	armed   atomic.Bool
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func newBlockingBatchReadFault() *blockingBatchReadFault {
	return &blockingBatchReadFault{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (f *blockingBatchReadFault) Arm() {
	f.armed.Store(true)
}

func (f *blockingBatchReadFault) Wait(t *testing.T) {
	t.Helper()
	select {
	case <-f.entered:
	case <-time.After(time.Second):
		t.Fatal("batch read did not reach a blocking Pebble read")
	}
}

func (f *blockingBatchReadFault) Release() {
	f.once.Do(func() { close(f.release) })
}

func (f *blockingBatchReadFault) MaybeError(op errorfs.Op, path string) error {
	if !f.armed.Load() || filepath.Ext(path) != ".sst" {
		return nil
	}
	if op != errorfs.OpFileRead && op != errorfs.OpFileReadAt {
		return nil
	}
	if f.armed.CompareAndSwap(true, false) {
		close(f.entered)
		<-f.release
	}
	return nil
}
