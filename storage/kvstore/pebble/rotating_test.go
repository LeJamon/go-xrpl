package pebble_test

import (
	"encoding/json"
	"fmt"
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
	hasState, err := kvpebble.HasRotationState(path)
	require.NoError(t, err)
	require.False(t, hasState)

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

func TestRotatingStoreBatchPromotionPreservesOrderPrecedenceAndBounds(t *testing.T) {
	store, err := kvpebble.NewRotating(filepath.Join(t.TempDir(), "nodes"), rotatingTestOptions())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	empty, emptyStats, err := store.PromoteBatch(nil, 0)
	require.NoError(t, err)
	require.Empty(t, empty)
	require.Zero(t, emptyStats.Consumed)
	_, _, err = store.PromoteBatch([][]byte{[]byte("a")}, 0)
	require.ErrorContains(t, err, "byte limit")

	require.NoError(t, store.Put([]byte("a"), []byte("archive-a")))
	require.NoError(t, store.Put([]byte("b"), []byte("archive-b")))
	committed, err := store.Rotate(11, 1)
	require.True(t, committed)
	require.NoError(t, err)
	require.NoError(t, store.Put([]byte("b"), []byte("writable-b")))
	require.NoError(t, store.Put([]byte("c"), []byte("writable-c")))

	promotions, stats, err := store.PromoteBatch(
		[][]byte{[]byte("c"), []byte("missing"), []byte("b"), []byte("a")},
		1<<20,
	)
	require.NoError(t, err)
	require.Equal(t, []string{"a", "b", "c", "missing"}, []string{
		string(promotions[0].Key),
		string(promotions[1].Key),
		string(promotions[2].Key),
		string(promotions[3].Key),
	})
	require.Equal(t, []byte("archive-a"), promotions[0].Value)
	require.Equal(t, []byte("writable-b"), promotions[1].Value)
	require.Equal(t, []byte("writable-c"), promotions[2].Value)
	require.Nil(t, promotions[3].Value)
	require.Equal(t, 4, stats.Requested)
	require.Equal(t, 4, stats.Consumed)
	require.Equal(t, 2, stats.WritableHits)
	require.Equal(t, 2, stats.WritableMisses)
	require.Equal(t, 2, stats.ArchiveLookups)
	require.Equal(t, 2, stats.ArchiveLookupsAvoided)
	require.Equal(t, 1, stats.ArchiveHits)
	require.Equal(t, 1, stats.ArchiveMisses)
	require.Equal(t, 1, stats.Promoted)
	require.Equal(t, len("archive-a"), stats.PromotedBytes)
	require.Equal(t, 1, stats.Batches)

	promotions[0].Key[0] = 'z'
	promotions[0].Value[0] = 'z'
	value, err := store.Get([]byte("a"))
	require.NoError(t, err)
	require.Equal(t, []byte("archive-a"), value)

	bounded, boundedStats, err := store.PromoteBatch([][]byte{[]byte("a"), []byte("b")}, len("archive-a"))
	require.NoError(t, err)
	require.Len(t, bounded, 1)
	require.Equal(t, "a", string(bounded[0].Key))
	require.Equal(t, len("archive-a"), boundedStats.BufferedBytes)

	oversized, oversizedStats, err := store.PromoteBatch([][]byte{[]byte("b"), []byte("c")}, 1)
	require.NoError(t, err)
	require.Len(t, oversized, 1)
	require.Equal(t, "b", string(oversized[0].Key))
	require.Greater(t, oversizedStats.BufferedBytes, 1)
}

func TestRotatingStoreBatchPromotionIgnoresSupersededArchivePayloads(t *testing.T) {
	store, err := kvpebble.NewRotating(filepath.Join(t.TempDir(), "nodes"), rotatingTestOptions())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	for _, key := range []string{"a", "b", "empty"} {
		require.NoError(t, store.Put([]byte(key), make([]byte, 4096)))
	}
	require.NoError(t, store.Put([]byte("dead"), []byte("obsolete")))
	require.NoError(t, store.Put([]byte("c"), []byte("archive")))
	committed, err := store.Rotate(11, 1)
	require.NoError(t, err)
	require.True(t, committed)
	require.NoError(t, store.Put([]byte("a"), []byte("a")))
	require.NoError(t, store.Put([]byte("b"), []byte("b")))
	require.NoError(t, store.Put([]byte("empty"), nil))

	keys := [][]byte{[]byte("missing"), []byte("empty"), []byte("c"), []byte("b"), []byte("a")}
	promotions, stats, err := store.PromoteBatch(keys, 9)
	require.NoError(t, err)
	require.Len(t, promotions, 5)
	require.Equal(t, 5, stats.Consumed)
	require.Equal(t, 3, stats.ArchiveLookupsAvoided)
	require.Equal(t, 2, stats.ArchiveLookups)
	require.Equal(t, 9, stats.PrefetchBytes)
	require.Equal(t, 9, stats.BufferedBytes)
	require.Equal(t, []byte("a"), promotions[0].Value)
	require.Equal(t, []byte("b"), promotions[1].Value)
	require.Equal(t, []byte("archive"), promotions[2].Value)
	require.True(t, promotions[3].Found)
	require.Empty(t, promotions[3].Value)
	require.False(t, promotions[4].Found)

	committed, err = store.Rotate(21, 12)
	require.NoError(t, err)
	require.True(t, committed)
	iter, err := store.NewIterator(nil, nil)
	require.NoError(t, err)
	var retained []string
	for iter.Next() {
		retained = append(retained, string(iter.Key()))
	}
	require.NoError(t, iter.Error())
	require.NoError(t, iter.Close())
	require.Equal(t, []string{"a", "b", "c", "empty"}, retained)
}

func TestRotatingStoreBatchPromotionDistinguishesEmptyValueFromMissing(t *testing.T) {
	store, err := kvpebble.NewRotating(filepath.Join(t.TempDir(), "nodes"), rotatingTestOptions())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	require.NoError(t, store.Put([]byte("empty"), nil))
	committed, err := store.Rotate(11, 1)
	require.True(t, committed)
	require.NoError(t, err)

	promotions, _, err := store.PromoteBatch([][]byte{[]byte("missing"), []byte("empty")}, 1024)
	require.NoError(t, err)
	require.Len(t, promotions, 2)
	require.Equal(t, "empty", string(promotions[0].Key))
	require.True(t, promotions[0].Found)
	require.Empty(t, promotions[0].Value)
	require.Equal(t, "missing", string(promotions[1].Key))
	require.False(t, promotions[1].Found)

	require.NoError(t, store.Put([]byte("large"), []byte("oversized")))
	promotions, stats, err := store.PromoteBatch([][]byte{[]byte("large"), []byte("missing")}, 1)
	require.NoError(t, err)
	require.Len(t, promotions, 2)
	require.Equal(t, 2, stats.Consumed)
	require.Equal(t, []byte("oversized"), promotions[0].Value)
	require.False(t, promotions[1].Found)
}

func TestRotatingStoreRejectsRetentionFloorRollback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nodes")
	store, err := kvpebble.NewRotating(path, rotatingTestOptions())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	committed, err := store.Rotate(100, 51)
	require.True(t, committed)
	require.NoError(t, err)

	committed, err = store.Rotate(200, 50)
	require.False(t, committed)
	require.ErrorContains(t, err, "precedes committed minimum")
	lastRotated, minimumOnline := store.RotationState()
	require.Equal(t, uint32(100), lastRotated)
	require.Equal(t, uint32(51), minimumOnline)
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
	require.ErrorContains(t, err, "offline rotation-state migration")
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

func TestRotatingStoreRejectsMissingManifestWithoutTouchingGenerations(t *testing.T) {
	for _, rotations := range []int{1, 2} {
		t.Run(fmt.Sprintf("after %d rotations", rotations), func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "nodes")
			store, err := kvpebble.NewRotating(path, rotatingTestOptions())
			require.NoError(t, err)
			require.NoError(t, store.Put([]byte("key-0"), []byte("value-0")))
			for rotation := 1; rotation <= rotations; rotation++ {
				minimumOnline := uint32(1)
				if rotation > 1 {
					minimumOnline = uint32((rotation-1)*10 + 2)
				}
				committed, err := store.Rotate(uint32(rotation*10+1), minimumOnline)
				require.True(t, committed)
				require.NoError(t, err)
				key := fmt.Sprintf("key-%d", rotation)
				require.NoError(t, store.Put([]byte(key), []byte(fmt.Sprintf("value-%d", rotation))))
			}
			require.NoError(t, store.Sync())
			require.NoError(t, store.Close())

			statePath := path + ".generations.json"
			stateData, err := os.ReadFile(statePath)
			require.NoError(t, err)
			var state struct {
				Writable string `json:"writable"`
				Archive  string `json:"archive"`
			}
			require.NoError(t, json.Unmarshal(stateData, &state))
			generationPaths := []string{
				filepath.Join(root, state.Writable),
				filepath.Join(root, state.Archive),
			}
			for _, generationPath := range generationPaths {
				require.NoError(t, os.WriteFile(
					filepath.Join(generationPath, "missing-manifest-sentinel"),
					[]byte(generationPath),
					0o600,
				))
			}
			before, err := filepath.Glob(filepath.Join(root, ".nodes-generation-*"))
			require.NoError(t, err)

			backupPath := statePath + ".backup"
			require.NoError(t, os.Rename(statePath, backupPath))
			hasState, err := kvpebble.HasRotationState(path)
			require.False(t, hasState)
			require.ErrorContains(t, err, "generation state is missing")
			unexpected, err := kvpebble.NewRotating(path, rotatingTestOptions())
			if unexpected != nil {
				require.NoError(t, unexpected.Close())
			}
			require.ErrorContains(t, err, "generation state is missing")
			_, statErr := os.Lstat(statePath)
			require.ErrorIs(t, statErr, os.ErrNotExist)
			backupData, err := os.ReadFile(backupPath)
			require.NoError(t, err)
			require.Equal(t, stateData, backupData)

			after, err := filepath.Glob(filepath.Join(root, ".nodes-generation-*"))
			require.NoError(t, err)
			require.ElementsMatch(t, before, after)
			for _, generationPath := range generationPaths {
				sentinelPath := filepath.Join(generationPath, "missing-manifest-sentinel")
				got, err := os.ReadFile(sentinelPath)
				require.NoError(t, err)
				require.Equal(t, []byte(generationPath), got)
				require.NoError(t, os.Remove(sentinelPath))
			}

			require.NoError(t, os.Rename(backupPath, statePath))
			reopened, err := kvpebble.NewRotating(path, rotatingTestOptions())
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, reopened.Close()) })
			for keyNumber := rotations - 1; keyNumber <= rotations; keyNumber++ {
				key := fmt.Sprintf("key-%d", keyNumber)
				value, err := reopened.Get([]byte(key))
				require.NoError(t, err)
				require.Equal(t, []byte(fmt.Sprintf("value-%d", keyNumber)), value)
			}
		})
	}
}

func TestRotatingStoreRejectsCorruptUnmanifestedMarker(t *testing.T) {
	for _, markerLocation := range []string{"base", "sibling"} {
		t.Run(markerLocation, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "nodes")
			markerDirectory := path
			if markerLocation == "sibling" {
				markerDirectory = filepath.Join(root, ".nodes-generation-corrupt")
			}
			require.NoError(t, os.Mkdir(markerDirectory, 0o755))
			markerPath := filepath.Join(markerDirectory, ".goxrpl-generation.json")
			markerData := []byte("not-json")
			require.NoError(t, os.WriteFile(markerPath, markerData, 0o600))

			hasState, err := kvpebble.HasRotationState(path)
			require.False(t, hasState)
			require.ErrorContains(t, err, "generation marker")
			unexpected, err := kvpebble.NewRotating(path, rotatingTestOptions())
			if unexpected != nil {
				require.NoError(t, unexpected.Close())
			}
			require.ErrorContains(t, err, "generation marker")
			got, err := os.ReadFile(markerPath)
			require.NoError(t, err)
			require.Equal(t, markerData, got)
			_, statErr := os.Lstat(path + ".generations.json")
			require.ErrorIs(t, statErr, os.ErrNotExist)
		})
	}
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

func TestRotatingStoreBatchPromotionSkipsSupersededArchivePayloads(t *testing.T) {
	store, err := kvpebble.NewRotating(filepath.Join(t.TempDir(), "nodes"), rotatingTestOptions())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	for _, key := range []string{"a", "b", "c"} {
		require.NoError(t, store.Put([]byte(key), []byte("oversized archive value")))
	}
	committed, err := store.Rotate(11, 1)
	require.True(t, committed)
	require.NoError(t, err)
	require.NoError(t, store.Put([]byte("a"), []byte("a")))
	require.NoError(t, store.Put([]byte("b"), nil))
	require.NoError(t, store.Put([]byte("c"), []byte("c")))

	promotions, stats, err := store.PromoteBatch([][]byte{[]byte("c"), []byte("b"), []byte("a")}, 2)
	require.NoError(t, err)
	require.Len(t, promotions, 3)
	require.Equal(t, []byte("a"), promotions[0].Value)
	require.True(t, promotions[1].Found)
	require.Empty(t, promotions[1].Value)
	require.Equal(t, []byte("c"), promotions[2].Value)
	require.Equal(t, 2, stats.BufferedBytes)
	require.Equal(t, 3, stats.ArchiveLookupsAvoided)
	require.Zero(t, stats.ArchiveLookups)
	require.Zero(t, stats.Promoted)
}
