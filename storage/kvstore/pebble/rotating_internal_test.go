package pebble

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/storage/kvstore"
	"github.com/stretchr/testify/require"
)

func TestRotateKeepsPublishedGenerationAfterDirectorySyncFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nodes")
	store, err := NewRotating(path, Options{BlockCacheBytes: 16 << 20, MaxOpenFiles: 200})
	require.NoError(t, err)
	require.NoError(t, store.Put([]byte("durable"), []byte("value")))
	require.NoError(t, store.Sync())

	syncErr := errors.New("directory sync failed")
	store.syncDir = func(string) error { return syncErr }
	committed, err := store.Rotate(11, 1)
	require.True(t, committed)
	require.ErrorIs(t, err, syncErr)

	value, err := store.Get([]byte("durable"))
	require.NoError(t, err)
	require.Equal(t, []byte("value"), value)
	require.NoError(t, store.Close())

	reopened, err := NewRotating(path, Options{BlockCacheBytes: 16 << 20, MaxOpenFiles: 200})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reopened.Close()) })
	lastRotated, minimumOnline := reopened.RotationState()
	require.Equal(t, uint32(11), lastRotated)
	require.Equal(t, uint32(1), minimumOnline)
	value, err = reopened.Get([]byte("durable"))
	require.NoError(t, err)
	require.Equal(t, []byte("value"), value)
}

func TestGenerationStateValidationIsSharedByLoadAndSave(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nodes")
	ownerID := "00000000000000000000000000000000"
	store := &RotatingStore{
		basePath:  path,
		statePath: path + generationStateSuffix,
		syncDir:   syncDirectory,
	}
	invalid := generationState{
		Version:       generationStateVersion,
		OwnerID:       ownerID,
		Writable:      "writable",
		Archive:       "archive",
		LastRotated:   10,
		MinimumOnline: 11,
	}
	published, err := store.saveState(invalid)
	require.False(t, published)
	require.ErrorContains(t, err, "invalid generation boundaries")
	_, statErr := os.Stat(store.statePath)
	require.ErrorIs(t, statErr, os.ErrNotExist)

	require.NoError(t, os.WriteFile(store.statePath, []byte(
		`{"version":2,"owner_id":"00000000000000000000000000000000","writable":"writable","archive":"archive","last_rotated":10,"minimum_online":11}`,
	), 0o600))
	_, found, err := store.loadState()
	require.False(t, found)
	require.ErrorContains(t, err, "invalid generation boundaries")
}

func TestRotateRejectsInvalidBoundariesBeforeCreatingGeneration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nodes")
	store, err := NewRotating(path, Options{BlockCacheBytes: 16 << 20, MaxOpenFiles: 200})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	before, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".nodes-generation-*"))
	require.NoError(t, err)
	committed, err := store.Rotate(10, 11)
	require.False(t, committed)
	require.ErrorContains(t, err, "invalid generation boundaries")
	after, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".nodes-generation-*"))
	require.NoError(t, err)
	require.Equal(t, before, after)
}

func TestRotatingStoreSyncFlushesArchiveAfterDelete(t *testing.T) {
	store, err := NewRotating(
		filepath.Join(t.TempDir(), "nodes"),
		Options{BlockCacheBytes: 16 << 20, MaxOpenFiles: 200},
	)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	require.NoError(t, store.Put([]byte("key"), []byte("value")))
	committed, err := store.Rotate(11, 1)
	require.True(t, committed)
	require.NoError(t, err)

	batch, err := store.NewBatch()
	require.NoError(t, err)
	require.NoError(t, batch.Delete([]byte("key")))
	require.NoError(t, batch.Write())
	require.NoError(t, batch.Close())

	archiveSyncErr := errors.New("archive sync failed")
	var synced []*Store
	store.syncGeneration = func(generation *Store) error {
		synced = append(synced, generation)
		if generation == store.archive {
			return archiveSyncErr
		}
		return nil
	}

	err = store.Sync()
	require.ErrorIs(t, err, archiveSyncErr)
	require.Equal(t, []*Store{store.writable, store.archive}, synced)
}

func TestPromoteDoesNotOverwriteConcurrentPut(t *testing.T) {
	store := newPromoteRaceStore(t)
	store.archive.mu.Lock()
	archiveLocked := true
	defer func() {
		if archiveLocked {
			store.archive.mu.Unlock()
		}
	}()
	promoteDone := make(chan error, 1)
	go func() {
		_, err := store.Promote([]byte("key"))
		promoteDone <- err
	}()
	waitForLocked(t, &store.mu)

	putDone := make(chan error, 1)
	go func() {
		putDone <- store.Put([]byte("key"), []byte("new"))
	}()
	assertBlocked(t, putDone, "Put")
	store.archive.mu.Unlock()
	archiveLocked = false
	require.NoError(t, <-promoteDone)
	require.NoError(t, <-putDone)

	value, err := store.Get([]byte("key"))
	require.NoError(t, err)
	require.Equal(t, []byte("new"), value)
}

func TestPromoteDoesNotResurrectConcurrentDelete(t *testing.T) {
	store := newPromoteRaceStore(t)
	batch, err := store.NewBatch()
	require.NoError(t, err)
	require.NoError(t, batch.Delete([]byte("key")))
	t.Cleanup(func() { require.NoError(t, batch.Close()) })

	store.archive.mu.Lock()
	archiveLocked := true
	defer func() {
		if archiveLocked {
			store.archive.mu.Unlock()
		}
	}()
	promoteDone := make(chan error, 1)
	go func() {
		_, err := store.Promote([]byte("key"))
		promoteDone <- err
	}()
	waitForLocked(t, &store.mu)

	deleteDone := make(chan error, 1)
	go func() {
		deleteDone <- batch.Write()
	}()
	assertBlocked(t, deleteDone, "Delete")
	store.archive.mu.Unlock()
	archiveLocked = false
	require.NoError(t, <-promoteDone)
	require.NoError(t, <-deleteDone)

	_, err = store.Get([]byte("key"))
	require.ErrorIs(t, err, kvstore.ErrNotFound)
}

func newPromoteRaceStore(t *testing.T) *RotatingStore {
	t.Helper()
	store, err := NewRotating(
		filepath.Join(t.TempDir(), "nodes"),
		Options{BlockCacheBytes: 16 << 20, MaxOpenFiles: 200},
	)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	require.NoError(t, store.Put([]byte("key"), []byte("old")))
	committed, err := store.Rotate(11, 1)
	require.True(t, committed)
	require.NoError(t, err)
	return store
}

func waitForLocked(t *testing.T, mutex *sync.RWMutex) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if mutex.TryLock() {
			mutex.Unlock()
			runtime.Gosched()
			continue
		}
		return
	}
	t.Fatal("operation did not acquire rotating store lock")
}

func assertBlocked(t *testing.T, done <-chan error, operation string) {
	t.Helper()
	select {
	case err := <-done:
		t.Fatalf("%s completed during promotion: %v", operation, err)
	case <-time.After(20 * time.Millisecond):
	}
}
