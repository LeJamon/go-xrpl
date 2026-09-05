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
	cockroachpebble "github.com/cockroachdb/pebble"
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

func TestUnpublishedInitializationRollsBackBaseMarkerAndCanRetry(t *testing.T) {
	openErr := errors.New("generation open failed")
	for _, test := range []struct {
		name      string
		configure func(*RotatingStore) generationOpener
	}{
		{
			name: "writable open",
			configure: func(*RotatingStore) generationOpener {
				return func(string, *cockroachpebble.Cache, Options) (*Store, error) {
					return nil, openErr
				}
			},
		},
		{
			name: "archive open",
			configure: func(*RotatingStore) generationOpener {
				calls := 0
				return func(path string, cache *cockroachpebble.Cache, options Options) (*Store, error) {
					calls++
					if calls == 2 {
						return nil, openErr
					}
					return newWithCache(path, cache, options)
				}
			},
		},
		{
			name: "manifest save",
			configure: func(store *RotatingStore) generationOpener {
				store.statePath = filepath.Join(filepath.Dir(store.basePath), "missing", "state.json")
				return newWithCache
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "nodes")
			legacy, err := New(path, Options{BlockCacheBytes: 8 << 20, MaxOpenFiles: 80})
			require.NoError(t, err)
			require.NoError(t, legacy.Put([]byte("legacy"), []byte("value")))
			require.NoError(t, legacy.Sync())
			require.NoError(t, legacy.Close())

			resolved, perGeneration, err := resolveRotatingOptions(
				Options{BlockCacheBytes: 16 << 20, MaxOpenFiles: 200},
			)
			require.NoError(t, err)
			store, found, err := prepareRotatingStore(path, perGeneration)
			require.NoError(t, err)
			require.False(t, found)
			require.True(t, store.unpublishedBaseMarker)

			err = store.openGenerations(resolved.BlockCacheBytes, false, test.configure(store))
			require.Error(t, err)
			_, markerErr := os.Lstat(filepath.Join(path, generationMarkerName))
			require.ErrorIs(t, markerErr, os.ErrNotExist)
			_, stateErr := os.Lstat(path + generationStateSuffix)
			require.ErrorIs(t, stateErr, os.ErrNotExist)
			orphans, err := filepath.Glob(filepath.Join(root, ".nodes-generation-*"))
			require.NoError(t, err)
			require.Empty(t, orphans)
			hasState, err := HasRotationState(path)
			require.NoError(t, err)
			require.False(t, hasState)

			retried, err := NewRotating(path, Options{BlockCacheBytes: 16 << 20, MaxOpenFiles: 200})
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, retried.Close()) })
			value, err := retried.Get([]byte("legacy"))
			require.NoError(t, err)
			require.Equal(t, []byte("value"), value)
		})
	}
}

func TestRollbackBaseMarkerAcceptsMarkerAlreadyRemoved(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nodes")
	require.NoError(t, os.Mkdir(path, 0o755))
	store := &RotatingStore{
		basePath:              path,
		ownerID:               "00000000000000000000000000000000",
		unpublishedBaseMarker: true,
	}
	require.NoError(t, store.rollbackBaseMarker())
	require.False(t, store.unpublishedBaseMarker)
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

func TestRotatingBatchArchiveFailureDoesNotCommitWritableOperations(t *testing.T) {
	store := newPromoteRaceStore(t)
	require.NoError(t, store.Put([]byte("key"), []byte("current")))

	batch, err := store.NewBatch()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, batch.Close()) })
	require.NoError(t, batch.Delete([]byte("key")))
	require.NoError(t, batch.Put([]byte("new"), []byte("value")))
	require.NoError(t, store.archive.Close())

	err = batch.Write()
	require.ErrorIs(t, err, kvstore.ErrClosed)
	value, err := store.writable.Get([]byte("key"))
	require.NoError(t, err)
	require.Equal(t, []byte("current"), value)
	_, err = store.writable.Get([]byte("new"))
	require.ErrorIs(t, err, kvstore.ErrNotFound)
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

func TestPromoteDoesNotBlockUnrelatedPut(t *testing.T) {
	store := newPromoteRaceStore(t)
	otherKey := []byte("other")
	require.NotEqual(t, mutationStripe([]byte("key")), mutationStripe(otherKey))

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
		putDone <- store.Put(otherKey, []byte("new"))
	}()
	select {
	case err := <-putDone:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("unrelated Put blocked behind promotion")
	}

	store.archive.mu.Unlock()
	archiveLocked = false
	require.NoError(t, <-promoteDone)
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

func TestPromoteBatchDoesNotResurrectConcurrentDeleteDuringPrefetch(t *testing.T) {
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

	type promotionResult struct {
		promotions []kvstore.Promotion
		err        error
	}
	promoteDone := make(chan promotionResult, 1)
	go func() {
		promotions, _, err := store.PromoteBatch([][]byte{[]byte("key")}, 1<<20)
		promoteDone <- promotionResult{promotions, err}
	}()
	waitForLocked(t, &store.archiveMu)

	key := []byte("key")
	putDone := make(chan error, 1)
	go func() { putDone <- store.Put(key, []byte("new value")) }()
	select {
	case err := <-putDone:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("Put blocked during archive prefetch")
	}

	deleteDone := make(chan error, 1)
	go func() { deleteDone <- batch.Write() }()
	assertBlocked(t, deleteDone, "Delete")

	store.archive.mu.Unlock()
	archiveLocked = false
	result := <-promoteDone
	require.NoError(t, result.err)
	require.Len(t, result.promotions, 1)
	require.Equal(t, []byte("new value"), result.promotions[0].Value)
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
