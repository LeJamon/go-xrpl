package node

import (
	"context"
	"encoding/binary"
	"path/filepath"
	"testing"

	"github.com/LeJamon/go-xrpl/config"
	xrpllog "github.com/LeJamon/go-xrpl/log"
	kvpebble "github.com/LeJamon/go-xrpl/storage/kvstore/pebble"
	"github.com/LeJamon/go-xrpl/storage/nodestore"
	"github.com/stretchr/testify/require"
)

func TestSetupStorageRejectsFormatBeforeConvertingPlainStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nodes")
	options := kvpebble.Options{BlockCacheBytes: 16 << 20, MaxOpenFiles: 200}
	plain, err := kvpebble.New(path, options)
	require.NoError(t, err)
	key := []byte("goxrpl:nodestore:format")
	value := binary.BigEndian.AppendUint32(nil, nodestore.FormatVersion+1)
	require.NoError(t, plain.Put(key, value))
	require.NoError(t, plain.Close())

	cfg := &config.Config{NodeDB: config.NodeDBConfig{Path: path, OnlineDelete: 256}}
	_, _, err = setupStorage(t.Context(), cfg, xrpllog.Discard(), nil)
	require.ErrorIs(t, err, nodestore.ErrUnsupportedFormat)
	hasState, err := kvpebble.HasRotationState(path)
	require.NoError(t, err)
	require.False(t, hasState)

	plain, err = kvpebble.New(path, options)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, plain.Close()) })
	stored, err := plain.Get(key)
	require.NoError(t, err)
	require.Equal(t, value, stored)
}

func TestSetupStorageKeepsGenerationManifestWhenOnlineDeleteIsDisabled(t *testing.T) {
	cfg := &config.Config{
		NodeSize: "small",
		NodeDB: config.NodeDBConfig{
			Path:         filepath.Join(t.TempDir(), "nodes"),
			OnlineDelete: 256,
		},
	}
	db, _, err := setupStorage(context.Background(), cfg, xrpllog.Discard(), nil)
	require.NoError(t, err)
	_, ok := db.(nodestore.GenerationDatabase)
	require.True(t, ok)
	require.NoError(t, db.Close())

	cfg.NodeDB.OnlineDelete = 0
	reopened, _, err := setupStorage(context.Background(), cfg, xrpllog.Discard(), nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reopened.Close()) })
	_, ok = reopened.(nodestore.GenerationDatabase)
	require.True(t, ok, "existing generation state must remain authoritative")
}

func TestSetupStorageRejectsUnsupportedArchiveFormat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nodes")
	store, err := kvpebble.NewRotating(path, kvpebble.Options{BlockCacheBytes: 16 << 20, MaxOpenFiles: 200})
	require.NoError(t, err)
	key := []byte("goxrpl:nodestore:format")
	require.NoError(t, store.Put(key, binary.BigEndian.AppendUint32(nil, nodestore.FormatVersion+1)))
	committed, err := store.Rotate(10, 1)
	require.NoError(t, err)
	require.True(t, committed)
	require.NoError(t, store.Put(key, binary.BigEndian.AppendUint32(nil, nodestore.FormatVersion)))
	require.NoError(t, store.Close())

	cfg := &config.Config{NodeDB: config.NodeDBConfig{Path: path}}
	_, _, err = setupStorage(t.Context(), cfg, xrpllog.Discard(), nil)
	require.ErrorIs(t, err, nodestore.ErrUnsupportedFormat)
}
