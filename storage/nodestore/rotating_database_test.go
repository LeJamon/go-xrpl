package nodestore

import (
	"bytes"
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

func TestRotatingKVDatabaseBatchPromotionDecodesSortedResults(t *testing.T) {
	ctx := context.Background()
	store, err := kvpebble.NewRotating(
		filepath.Join(t.TempDir(), "nodes"),
		kvpebble.Options{BlockCacheBytes: 16 << 20, MaxOpenFiles: 200},
	)
	require.NoError(t, err)
	db, err := NewRotatingKVDatabase(store, positiveCacheConfig(16))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	first := &Node{Type: NodeAccount, Hash: testHash([]byte("first")), Data: []byte("first"), LedgerSeq: 10}
	second := &Node{Type: NodeAccount, Hash: testHash([]byte("second")), Data: []byte("second"), LedgerSeq: 10}
	require.NoError(t, db.Store(ctx, first))
	require.NoError(t, db.Store(ctx, second))
	committed, err := db.RotateGeneration(ctx, 11, 1)
	require.True(t, committed)
	require.NoError(t, err)

	missing := testHash([]byte("missing"))
	nodes, stats, err := db.FetchBatchForPromotion(ctx, []Hash256{second.Hash, missing, first.Hash}, 1<<20)
	require.NoError(t, err)
	require.Len(t, nodes, 3)
	var previous Hash256
	got := make(map[Hash256][]byte)
	missingResults := 0
	for i, node := range nodes {
		var hash Hash256
		if node == nil {
			hash = missing
			missingResults++
		} else {
			hash = node.Hash
			got[hash] = node.Data
		}
		if i > 0 {
			require.LessOrEqual(t, bytes.Compare(previous[:], hash[:]), 0)
		}
		previous = hash
	}
	require.Equal(t, 1, missingResults)
	require.Equal(t, first.Data, got[first.Hash])
	require.Equal(t, second.Data, got[second.Hash])
	require.Equal(t, 3, stats.Consumed)
	require.Equal(t, 2, stats.Promoted)
	require.Equal(t, uint64(3), db.Stats().Reads)
	require.Equal(t, uint64(2), db.Stats().FetchHits)

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	_, _, err = db.FetchBatchForPromotion(cancelled, []Hash256{first.Hash}, 1<<20)
	require.ErrorIs(t, err, context.Canceled)
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

func TestRotatingKVDatabaseRejectsDirectDeleteBefore(t *testing.T) {
	ctx := context.Background()
	store, err := kvpebble.NewRotating(
		filepath.Join(t.TempDir(), "nodes"),
		kvpebble.Options{BlockCacheBytes: 16 << 20, MaxOpenFiles: 200},
	)
	require.NoError(t, err)
	db, err := NewRotatingKVDatabase(store, positiveCacheConfig(16))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	node := &Node{
		Type: NodeAccount, Hash: testHash([]byte("retained-node")),
		Data: []byte("retained-node"), LedgerSeq: 1,
	}
	require.NoError(t, db.Store(ctx, node))
	deleted, err := db.DeleteBefore(ctx, 2, 10)
	require.Zero(t, deleted)
	require.ErrorContains(t, err, "unsupported for rotating stores")
	stored, err := db.Fetch(ctx, node.Hash)
	require.NoError(t, err)
	require.NotNil(t, stored)
}
