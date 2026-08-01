package nodestore

import (
	"context"
	"path/filepath"
	"testing"

	kvpebble "github.com/LeJamon/go-xrpl/storage/kvstore/pebble"
	"github.com/stretchr/testify/require"
)

func TestRotatingKVDatabasePromotionBypassesDecodedCache(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "nodes")
	store, err := kvpebble.NewRotating(path, kvpebble.Options{BlockCacheBytes: 16 << 20, MaxOpenFiles: 200})
	require.NoError(t, err)
	db, err := NewRotatingKVDatabase(store, positiveCacheConfig(16))
	require.NoError(t, err)

	node := &Node{
		Type:      NodeAccount,
		Hash:      testHash([]byte("live-node")),
		Data:      []byte("live-node"),
		LedgerSeq: 10,
	}
	require.NoError(t, db.Store(ctx, node))
	_, cached := db.cache.Get(node.Hash)
	require.True(t, cached)

	committed, err := db.RotateGeneration(ctx, 11, 1)
	require.True(t, committed)
	require.NoError(t, err)
	_, cached = db.cache.Get(node.Hash)
	require.False(t, cached)

	promoted, err := db.FetchForPromotion(ctx, node.Hash)
	require.NoError(t, err)
	require.Equal(t, node.Data, promoted.Data)
	_, cached = db.cache.Get(node.Hash)
	require.False(t, cached)
	require.Equal(t, uint64(1), db.Stats().Writes)

	committed, err = db.RotateGeneration(ctx, 21, 12)
	require.True(t, committed)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	reopenedStore, err := kvpebble.NewRotating(path, kvpebble.Options{BlockCacheBytes: 16 << 20, MaxOpenFiles: 200})
	require.NoError(t, err)
	reopened, err := NewRotatingKVDatabase(reopenedStore, positiveCacheConfig(16))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reopened.Close()) })
	fetched, err := reopened.Fetch(ctx, node.Hash)
	require.NoError(t, err)
	require.Equal(t, node.Data, fetched.Data)
}

func TestRotatingKVDatabaseCanRotateWithoutRefresh(t *testing.T) {
	ctx := context.Background()
	store, err := kvpebble.NewRotating(
		filepath.Join(t.TempDir(), "nodes"),
		kvpebble.Options{BlockCacheBytes: 16 << 20, MaxOpenFiles: 200},
	)
	require.NoError(t, err)
	db, err := NewRotatingKVDatabase(store, positiveCacheConfig(16))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	canSkip, err := db.CanRotateWithoutRefresh(ctx)
	require.NoError(t, err)
	require.True(t, canSkip)

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	_, err = db.CanRotateWithoutRefresh(cancelled)
	require.ErrorIs(t, err, context.Canceled)

	node := &Node{
		Type:      NodeAccount,
		Hash:      testHash([]byte("live-node")),
		Data:      []byte("live-node"),
		LedgerSeq: 10,
	}
	require.NoError(t, db.Store(ctx, node))
	committed, err := db.RotateGeneration(ctx, 11, 1)
	require.True(t, committed)
	require.NoError(t, err)
	canSkip, err = db.CanRotateWithoutRefresh(ctx)
	require.NoError(t, err)
	require.False(t, canSkip)
}
