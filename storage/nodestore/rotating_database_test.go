package nodestore

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	kvpebble "github.com/LeJamon/go-xrpl/storage/kvstore/pebble"
	"github.com/stretchr/testify/require"
)

func TestRotatingKVDatabasePromotionBypassesDecodedCache(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "nodes")
	store, err := kvpebble.NewRotating(path, 16<<20, 128)
	require.NoError(t, err)
	db := NewRotatingKVDatabase(store, "rotating", &DatabaseConfig{
		CacheSize: 16,
		CacheTTL:  time.Hour,
	})

	node := &Node{
		Type:      NodeAccount,
		Hash:      ComputeHash256([]byte("live-node")),
		Data:      []byte("live-node"),
		LedgerSeq: 10,
	}
	require.NoError(t, db.Store(ctx, node))
	require.Equal(t, uint64(1), db.Stats().CacheSize)

	committed, err := db.RotateGeneration(ctx, 11, 1)
	require.True(t, committed)
	require.NoError(t, err)
	require.Zero(t, db.Stats().CacheSize)

	promoted, err := db.FetchForPromotion(ctx, node.Hash)
	require.NoError(t, err)
	require.Equal(t, node.Data, promoted.Data)
	stats := db.Stats()
	require.Zero(t, stats.CacheSize)
	require.Equal(t, uint64(1), stats.Writes)

	committed, err = db.RotateGeneration(ctx, 21, 12)
	require.True(t, committed)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	reopenedStore, err := kvpebble.NewRotating(path, 16<<20, 128)
	require.NoError(t, err)
	reopened := NewRotatingKVDatabase(reopenedStore, "rotating", &DatabaseConfig{
		CacheSize: 16,
		CacheTTL:  time.Hour,
	})
	t.Cleanup(func() { require.NoError(t, reopened.Close()) })
	fetched, err := reopened.Fetch(ctx, node.Hash)
	require.NoError(t, err)
	require.Equal(t, node.Data, fetched.Data)
}

func TestRotatingKVDatabaseCanRotateWithoutRefresh(t *testing.T) {
	ctx := context.Background()
	store, err := kvpebble.NewRotating(filepath.Join(t.TempDir(), "nodes"), 16<<20, 128)
	require.NoError(t, err)
	db := NewRotatingKVDatabase(store, "rotating", &DatabaseConfig{
		CacheSize: 16,
		CacheTTL:  time.Hour,
	})
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
		Hash:      ComputeHash256([]byte("live-node")),
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
