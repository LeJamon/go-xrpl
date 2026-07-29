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

func rotatingTestOptions() kvpebble.Options {
	return kvpebble.Options{BlockCacheBytes: 16 << 20, MaxOpenFiles: 200}
}

func legacyTestOptions() kvpebble.Options {
	return kvpebble.Options{BlockCacheBytes: 8 << 20, MaxOpenFiles: 80}
}

func TestRotatingStoreConformance(t *testing.T) {
	kvstoretest.RunConformance(t, func(t *testing.T) kvstore.KeyValueStore {
		store, err := kvpebble.NewRotating(filepath.Join(t.TempDir(), "nodes"), rotatingTestOptions())
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, store.Close()) })
		return store
	})
}

func TestRotatingStoreCanSkipRefreshOnlyForEmptyArchive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nodes")
	legacy, err := kvpebble.New(path, legacyTestOptions())
	require.NoError(t, err)
	require.NoError(t, legacy.Put([]byte("legacy"), []byte("value")))
	require.NoError(t, legacy.Sync())
	require.NoError(t, legacy.Close())

	store, err := kvpebble.NewRotating(path, rotatingTestOptions())
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

	reopened, err := kvpebble.NewRotating(path, rotatingTestOptions())
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
	legacy, err := kvpebble.New(path, legacyTestOptions())
	require.NoError(t, err)
	require.NoError(t, legacy.Put([]byte("live"), []byte("live-value")))
	require.NoError(t, legacy.Put([]byte("historical"), []byte("historical-value")))
	require.NoError(t, legacy.Sync())
	require.NoError(t, legacy.Close())

	store, err := kvpebble.NewRotating(path, rotatingTestOptions())
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

	reopened, err := kvpebble.NewRotating(path, rotatingTestOptions())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reopened.Close()) })
	value, err = reopened.Get([]byte("live"))
	require.NoError(t, err)
	require.Equal(t, []byte("live-value"), value)
}

func TestRotatingStoreMissingCommittedGenerationIsFatal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nodes")
	store, err := kvpebble.NewRotating(path, rotatingTestOptions())
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

	_, err = kvpebble.NewRotating(path, rotatingTestOptions())
	require.Error(t, err)
	require.ErrorContains(t, err, "generation")
	require.ErrorContains(t, err, "unavailable")
}

func TestRotatingStoreRejectsManifestPathOutsideGenerationDirectory(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "store")
	require.NoError(t, os.MkdirAll(parent, 0o755))
	path := filepath.Join(parent, "nodes")
	sentinelPath := filepath.Join(root, "sentinel")
	sentinel := []byte("must remain unchanged")
	require.NoError(t, os.WriteFile(sentinelPath, sentinel, 0o600))

	state := map[string]any{
		"version":  2,
		"owner_id": "00000000000000000000000000000000",
		"writable": "..",
		"archive":  "archive",
	}
	stateData, err := json.Marshal(state)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path+".generations.json", stateData, 0o600))

	_, err = kvpebble.NewRotating(path, rotatingTestOptions())
	require.ErrorContains(t, err, "invalid writable generation")
	got, readErr := os.ReadFile(sentinelPath)
	require.NoError(t, readErr)
	require.Equal(t, sentinel, got)
}

func TestRotatingStoreLegacyManifestRequiresExplicitMigration(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "nodes")
	archiveName := ".nodes-generation-legacy"
	archivePath := filepath.Join(root, archiveName)

	writable, err := kvpebble.New(path, legacyTestOptions())
	require.NoError(t, err)
	require.NoError(t, writable.Put([]byte("writable"), []byte("value")))
	require.NoError(t, writable.Sync())
	require.NoError(t, writable.Close())
	archive, err := kvpebble.New(archivePath, legacyTestOptions())
	require.NoError(t, err)
	require.NoError(t, archive.Put([]byte("archive"), []byte("value")))
	require.NoError(t, archive.Sync())
	require.NoError(t, archive.Close())
	writeLegacyRotationState(t, path, filepath.Base(path), archiveName)

	_, err = kvpebble.NewRotating(path, rotatingTestOptions())
	require.ErrorIs(t, err, kvpebble.ErrLegacyRotationState)
	require.ErrorContains(t, err, "MigrateLegacyRotationState")
	for _, generationPath := range []string{path, archivePath} {
		_, markerErr := os.Lstat(filepath.Join(generationPath, ".goxrpl-generation.json"))
		require.ErrorIs(t, markerErr, os.ErrNotExist)
	}

	require.NoError(t, kvpebble.MigrateLegacyRotationState(path))
	require.NoError(t, kvpebble.MigrateLegacyRotationState(path))
	stateData, err := os.ReadFile(path + ".generations.json")
	require.NoError(t, err)
	var state struct {
		Version int    `json:"version"`
		OwnerID string `json:"owner_id"`
	}
	require.NoError(t, json.Unmarshal(stateData, &state))
	require.Equal(t, 2, state.Version)
	require.Len(t, state.OwnerID, 32)

	reopened, err := kvpebble.NewRotating(path, rotatingTestOptions())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reopened.Close()) })
	for _, key := range []string{"writable", "archive"} {
		value, err := reopened.Get([]byte(key))
		require.NoError(t, err)
		require.Equal(t, []byte("value"), value)
	}
}

func TestRotatingStoreLegacyManifestDoesNotTrustUnownedSibling(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "nodes")
	require.NoError(t, os.Mkdir(path, 0o755))
	archiveName := ".nodes-generation-unrelated"
	archivePath := filepath.Join(root, archiveName)
	require.NoError(t, os.Mkdir(archivePath, 0o755))
	sentinelPath := filepath.Join(archivePath, "sentinel")
	sentinel := []byte("must remain unchanged")
	require.NoError(t, os.WriteFile(sentinelPath, sentinel, 0o600))
	writeLegacyRotationState(t, path, filepath.Base(path), archiveName)

	_, err := kvpebble.NewRotating(path, rotatingTestOptions())
	require.ErrorIs(t, err, kvpebble.ErrLegacyRotationState)
	got, err := os.ReadFile(sentinelPath)
	require.NoError(t, err)
	require.Equal(t, sentinel, got)
	_, markerErr := os.Lstat(filepath.Join(archivePath, ".goxrpl-generation.json"))
	require.ErrorIs(t, markerErr, os.ErrNotExist)
}

func TestMigrateLegacyRotationStateRejectsGenerationSymlink(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "nodes")
	writable, err := kvpebble.New(path, legacyTestOptions())
	require.NoError(t, err)
	require.NoError(t, writable.Close())

	archiveName := ".nodes-generation-legacy"
	archivePath := filepath.Join(root, archiveName)
	outside := filepath.Join(t.TempDir(), "outside")
	require.NoError(t, os.Mkdir(outside, 0o755))
	sentinelPath := filepath.Join(outside, "sentinel")
	sentinel := []byte("must remain unchanged")
	require.NoError(t, os.WriteFile(sentinelPath, sentinel, 0o600))
	require.NoError(t, os.Symlink(outside, archivePath))
	writeLegacyRotationState(t, path, filepath.Base(path), archiveName)

	err = kvpebble.MigrateLegacyRotationState(path)
	require.ErrorContains(t, err, "symlink")
	got, err := os.ReadFile(sentinelPath)
	require.NoError(t, err)
	require.Equal(t, sentinel, got)
	_, markerErr := os.Lstat(filepath.Join(path, ".goxrpl-generation.json"))
	require.ErrorIs(t, markerErr, os.ErrNotExist)
}

func TestRotatingStoreRejectsUnrelatedSiblingInManifest(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "nodes")
	store, err := kvpebble.NewRotating(path, rotatingTestOptions())
	require.NoError(t, err)
	require.NoError(t, store.Close())

	siblingPath := filepath.Join(root, ".nodes-generation-unrelated")
	require.NoError(t, os.Mkdir(siblingPath, 0o755))
	sentinelPath := filepath.Join(siblingPath, "sentinel")
	sentinel := []byte("must remain unchanged")
	require.NoError(t, os.WriteFile(sentinelPath, sentinel, 0o600))

	stateData, err := os.ReadFile(path + ".generations.json")
	require.NoError(t, err)
	var state map[string]any
	require.NoError(t, json.Unmarshal(stateData, &state))
	state["archive"] = filepath.Base(siblingPath)
	stateData, err = json.Marshal(state)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path+".generations.json", stateData, 0o600))

	_, err = kvpebble.NewRotating(path, rotatingTestOptions())
	require.ErrorContains(t, err, "not owned")
	got, err := os.ReadFile(sentinelPath)
	require.NoError(t, err)
	require.Equal(t, sentinel, got)
}

func TestRotatingStoreRejectsGenerationSymlink(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "nodes")
	store, err := kvpebble.NewRotating(path, rotatingTestOptions())
	require.NoError(t, err)
	require.NoError(t, store.Close())

	stateData, err := os.ReadFile(path + ".generations.json")
	require.NoError(t, err)
	var state struct {
		Archive string `json:"archive"`
	}
	require.NoError(t, json.Unmarshal(stateData, &state))
	archivePath := filepath.Join(root, state.Archive)
	require.NoError(t, os.RemoveAll(archivePath))

	outside := filepath.Join(t.TempDir(), "outside")
	require.NoError(t, os.Mkdir(outside, 0o755))
	sentinelPath := filepath.Join(outside, "sentinel")
	sentinel := []byte("must remain unchanged")
	require.NoError(t, os.WriteFile(sentinelPath, sentinel, 0o600))
	require.NoError(t, os.Symlink(outside, archivePath))

	_, err = kvpebble.NewRotating(path, rotatingTestOptions())
	require.ErrorContains(t, err, "symlink")
	got, err := os.ReadFile(sentinelPath)
	require.NoError(t, err)
	require.Equal(t, sentinel, got)
}

func TestRotatingStoreLeavesUnmarkedFakePrefixOrphan(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "nodes")
	orphanPath := filepath.Join(root, ".nodes-generation-fake")
	require.NoError(t, os.Mkdir(orphanPath, 0o755))
	sentinelPath := filepath.Join(orphanPath, "sentinel")
	sentinel := []byte("must remain unchanged")
	require.NoError(t, os.WriteFile(sentinelPath, sentinel, 0o600))

	store, err := kvpebble.NewRotating(path, rotatingTestOptions())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	got, err := os.ReadFile(sentinelPath)
	require.NoError(t, err)
	require.Equal(t, sentinel, got)
}

func TestRotatingStoreBatchTargetsWritableAtCommitTime(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nodes")
	store, err := kvpebble.NewRotating(path, rotatingTestOptions())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	batch, err := store.NewBatch()
	require.NoError(t, err)
	defer batch.Close()
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
	store, err := kvpebble.NewRotating(path, rotatingTestOptions())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	it, err := store.NewIterator(nil, nil)
	require.NoError(t, err)
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
	require.NoError(t, it.Close())
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("rotation did not resume after iterator release")
	}
}

func TestRotatingStoreIteratorMergesGenerationsInKeyOrder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nodes")
	store, err := kvpebble.NewRotating(path, rotatingTestOptions())
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

	it, err := store.NewIterator(nil, nil)
	require.NoError(t, err)
	defer it.Close()
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
	store, err := kvpebble.NewRotating(path, rotatingTestOptions())
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

	reopened, err := kvpebble.NewRotating(path, rotatingTestOptions())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reopened.Close()) })
	value, err = reopened.Get([]byte("durable"))
	require.NoError(t, err)
	require.Equal(t, []byte("value"), value)
}

func writeLegacyRotationState(t *testing.T, path, writable, archive string) {
	t.Helper()
	stateData, err := json.Marshal(map[string]any{
		"version":        1,
		"writable":       writable,
		"archive":        archive,
		"last_rotated":   11,
		"minimum_online": 1,
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path+".generations.json", stateData, 0o600))
}
