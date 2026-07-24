package pebble_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/storage/kvstore"
	"github.com/LeJamon/go-xrpl/storage/kvstore/kvstoretest"
	kvpebble "github.com/LeJamon/go-xrpl/storage/kvstore/pebble"
	"github.com/stretchr/testify/require"
)

func TestRotatingStoreConformance(t *testing.T) {
	kvstoretest.RunConformance(t, func(t *testing.T) kvstore.KeyValueStore {
		store, err := kvpebble.NewRotating(filepath.Join(t.TempDir(), "nodes"), 16<<20, 128)
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, store.Close()) })
		return store
	})
}

func TestRotatingStoreCanSkipRefreshOnlyForEmptyArchive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nodes")
	legacy, err := kvpebble.New(path, 8<<20, 64, false)
	require.NoError(t, err)
	require.NoError(t, legacy.Put([]byte("legacy"), []byte("value")))
	require.NoError(t, legacy.Sync())
	require.NoError(t, legacy.Close())

	store, err := kvpebble.NewRotating(path, 16<<20, 128)
	require.NoError(t, err)
	canSkip, err := store.CanRotateWithoutRefresh()
	require.NoError(t, err)
	require.True(t, canSkip)
	require.NoError(t, store.Put([]byte("new"), []byte("value")))
	canSkip, err = store.CanRotateWithoutRefresh()
	require.NoError(t, err)
	require.True(t, canSkip)

	committed, err := store.Rotate(11, 1)
	require.True(t, committed)
	require.NoError(t, err)
	canSkip, err = store.CanRotateWithoutRefresh()
	require.NoError(t, err)
	require.False(t, canSkip)
	require.NoError(t, store.Close())

	reopened, err := kvpebble.NewRotating(path, 16<<20, 128)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reopened.Close()) })
	canSkip, err = reopened.CanRotateWithoutRefresh()
	require.NoError(t, err)
	require.False(t, canSkip)
	for _, key := range []string{"legacy", "new"} {
		value, err := reopened.Get([]byte(key))
		require.NoError(t, err)
		require.Equal(t, []byte("value"), value)
	}
}

func TestRotatingStoreExplicitPromotionPreservesLiveRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nodes")
	legacy, err := kvpebble.New(path, 8<<20, 64, false)
	require.NoError(t, err)
	require.NoError(t, legacy.Put([]byte("live"), []byte("live-value")))
	require.NoError(t, legacy.Put([]byte("historical"), []byte("historical-value")))
	require.NoError(t, legacy.Sync())
	require.NoError(t, legacy.Close())

	store, err := kvpebble.NewRotating(path, 16<<20, 128)
	require.NoError(t, err)

	committed, err := store.Rotate(11, 1)
	require.True(t, committed)
	require.NoError(t, err)
	committed, err = store.Rotate(11, 1)
	require.True(t, committed)
	require.NoError(t, err)
	lastRotated, minimumOnline := store.RotationState()
	require.Equal(t, uint32(11), lastRotated)
	require.Equal(t, uint32(1), minimumOnline)

	value, err := store.Get([]byte("historical"))
	require.NoError(t, err)
	require.Equal(t, []byte("historical-value"), value)

	value, err = store.Promote([]byte("live"))
	require.NoError(t, err)
	require.Equal(t, []byte("live-value"), value)

	committed, err = store.Rotate(21, 12)
	require.True(t, committed)
	require.NoError(t, err)

	_, err = store.Get([]byte("historical"))
	require.ErrorIs(t, err, kvstore.ErrNotFound)
	value, err = store.Get([]byte("live"))
	require.NoError(t, err)
	require.Equal(t, []byte("live-value"), value)

	_, err = store.Promote([]byte("live"))
	require.NoError(t, err)
	committed, err = store.Rotate(31, 22)
	require.True(t, committed)
	require.NoError(t, err)
	require.NoError(t, store.Close())

	reopened, err := kvpebble.NewRotating(path, 16<<20, 128)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reopened.Close()) })
	value, err = reopened.Get([]byte("live"))
	require.NoError(t, err)
	require.Equal(t, []byte("live-value"), value)
}

func TestRotatingStoreMissingCommittedGenerationIsFatal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nodes")
	store, err := kvpebble.NewRotating(path, 16<<20, 128)
	require.NoError(t, err)
	require.NoError(t, store.Close())

	stateData, err := os.ReadFile(path + ".generations.json")
	require.NoError(t, err)
	var state struct {
		Writable string `json:"writable"`
		Archive  string `json:"archive"`
	}
	require.NoError(t, json.Unmarshal(stateData, &state))
	require.NotEmpty(t, state.Archive)
	require.NoError(t, os.RemoveAll(filepath.Join(filepath.Dir(path), state.Archive)))

	_, err = kvpebble.NewRotating(path, 16<<20, 128)
	require.Error(t, err)
	require.ErrorContains(t, err, "generation")
	require.ErrorContains(t, err, "unavailable")
}

func TestRotatingStoreBatchTargetsWritableAtCommitTime(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nodes")
	store, err := kvpebble.NewRotating(path, 16<<20, 128)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	batch := store.NewBatch()
	require.NoError(t, batch.Put([]byte("late"), []byte("value")))
	committed, err := store.Rotate(11, 1)
	require.True(t, committed)
	require.NoError(t, err)
	require.NoError(t, batch.Write())

	committed, err = store.Rotate(21, 12)
	require.True(t, committed)
	require.NoError(t, err)
	value, err := store.Get([]byte("late"))
	require.NoError(t, err)
	require.Equal(t, []byte("value"), value)
}

func TestRotatingStoreIteratorPinsGenerationsUntilRelease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nodes")
	store, err := kvpebble.NewRotating(path, 16<<20, 128)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	it := store.NewIterator(nil, nil)
	done := make(chan error, 1)
	go func() {
		_, err := store.Rotate(11, 1)
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("rotation completed while iterator pinned generations: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	it.Release()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("rotation did not resume after iterator release")
	}
}

func TestRotatingStoreIteratorMergesGenerationsInKeyOrder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nodes")
	store, err := kvpebble.NewRotating(path, 16<<20, 128)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	require.NoError(t, store.Put([]byte("b"), []byte("archive-b")))
	require.NoError(t, store.Put([]byte("d"), []byte("archive-d")))
	committed, err := store.Rotate(11, 1)
	require.True(t, committed)
	require.NoError(t, err)
	require.NoError(t, store.Put([]byte("a"), []byte("writable-a")))
	require.NoError(t, store.Put([]byte("c"), []byte("writable-c")))
	require.NoError(t, store.Put([]byte("d"), []byte("writable-d")))

	it := store.NewIterator(nil, nil)
	defer it.Release()
	var keys []string
	var values []string
	for it.Next() {
		keys = append(keys, string(it.Key()))
		values = append(values, string(it.Value()))
	}
	require.NoError(t, it.Error())
	require.Equal(t, []string{"a", "b", "c", "d"}, keys)
	require.Equal(t, []string{"writable-a", "archive-b", "writable-c", "writable-d"}, values)
}

func TestRotatingStoreManifestFailureRollsBackCutover(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nodes")
	store, err := kvpebble.NewRotating(path, 16<<20, 128)
	require.NoError(t, err)
	require.NoError(t, store.Put([]byte("durable"), []byte("value")))
	require.NoError(t, store.Sync())

	statePath := path + ".generations.json"
	backupPath := statePath + ".backup"
	require.NoError(t, os.Rename(statePath, backupPath))
	require.NoError(t, os.Mkdir(statePath, 0o755))

	committed, err := store.Rotate(11, 1)
	require.False(t, committed)
	require.Error(t, err)
	value, fetchErr := store.Get([]byte("durable"))
	require.NoError(t, fetchErr)
	require.Equal(t, []byte("value"), value)

	require.NoError(t, os.Remove(statePath))
	require.NoError(t, os.Rename(backupPath, statePath))
	require.NoError(t, store.Close())

	reopened, err := kvpebble.NewRotating(path, 16<<20, 128)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reopened.Close()) })
	value, err = reopened.Get([]byte("durable"))
	require.NoError(t, err)
	require.Equal(t, []byte("value"), value)
}
