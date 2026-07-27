package pebble

import (
	"errors"
	"path/filepath"
	"testing"

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
