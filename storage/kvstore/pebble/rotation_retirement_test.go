package pebble

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/storage/kvstore"
	"github.com/stretchr/testify/require"
)

var _ kvstore.RetiringRotatingStore = (*RotatingStore)(nil)

func TestDetachedRetirementLeavesPublishedPairAvailable(t *testing.T) {
	store, err := NewRotating(filepath.Join(t.TempDir(), "nodes"), Options{BlockCacheBytes: 16 << 20, MaxOpenFiles: 200})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	require.NoError(t, store.Put([]byte("retained"), []byte("value")))
	oldArchive := store.archive
	oldPath := store.archivePath
	committed, retire, err := store.RotateWithRetirement(11, 1)
	require.NoError(t, err)
	require.True(t, committed)
	require.NotNil(t, retire)
	defer retire()
	require.False(t, oldArchive.closed, "retired archive closed under caller's guards")
	_, err = os.Stat(oldPath)
	require.NoError(t, err)
	if store.rotateMu.TryLock() {
		store.rotateMu.Unlock()
		t.Fatal("rotation/Close not serialized with retirement")
	}
	closed := make(chan error, 1)
	go func() { closed <- store.Close() }()
	select {
	case err := <-closed:
		t.Fatalf("Close completed before retirement: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	value, err := store.Get([]byte("retained"))
	require.NoError(t, err)
	require.Equal(t, []byte("value"), value)
	require.NoError(t, store.Put([]byte("new"), []byte("data")))
	require.NoError(t, retire())
	require.NoError(t, retire(), "retirement must be idempotent")
	select {
	case err := <-closed:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not complete")
	}
	_, err = os.Stat(oldPath)
	require.ErrorIs(t, err, os.ErrNotExist)
	reopened, err := NewRotating(store.basePath, Options{BlockCacheBytes: 16 << 20, MaxOpenFiles: 200})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reopened.Close()) })
	for key, want := range map[string]string{"retained": "value", "new": "data"} {
		got, err := reopened.Get([]byte(key))
		require.NoError(t, err)
		require.Equal(t, want, string(got))
	}
}

func TestDetachedRetirementPreservesOldArchiveOnUncertainManifestDurability(t *testing.T) {
	store, err := NewRotating(filepath.Join(t.TempDir(), "nodes"), Options{BlockCacheBytes: 16 << 20, MaxOpenFiles: 200})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	oldPath := store.archivePath
	syncErr := errors.New("directory sync failed")
	store.syncDir = func(string) error { return syncErr }
	committed, retire, err := store.RotateWithRetirement(11, 1)
	require.True(t, committed)
	require.ErrorIs(t, err, syncErr)
	require.NotNil(t, retire)
	require.NoError(t, retire())
	_, err = os.Stat(oldPath)
	require.NoError(t, err, "uncertain publication must preserve old manifest's archive")
}

func TestDetachedRetirementFailedAndNoOpRotationsReleaseAdmission(t *testing.T) {
	store, err := NewRotating(filepath.Join(t.TempDir(), "nodes"), Options{BlockCacheBytes: 16 << 20, MaxOpenFiles: 200})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	require.NoError(t, store.Put([]byte("kept"), []byte("data")))
	oldWritable, oldArchive := store.writable, store.archive
	statePath := store.statePath
	store.statePath = filepath.Join(t.TempDir(), "absent", "manifest.json")
	committed, retire, err := store.RotateWithRetirement(11, 1)
	require.Error(t, err)
	require.False(t, committed)
	require.Nil(t, retire)
	require.Same(t, oldWritable, store.writable)
	require.Same(t, oldArchive, store.archive)
	if !store.rotateMu.TryLock() {
		t.Fatal("failed publication leaked rotation admission")
	}
	store.rotateMu.Unlock()
	store.statePath = statePath
	committed, err = store.Rotate(11, 1)
	require.NoError(t, err)
	require.True(t, committed)
	committed, retire, err = store.RotateWithRetirement(11, 1)
	require.NoError(t, err)
	require.True(t, committed)
	require.Nil(t, retire)
	if !store.rotateMu.TryLock() {
		t.Fatal("no-op rotation leaked rotation admission")
	}
	store.rotateMu.Unlock()
}
