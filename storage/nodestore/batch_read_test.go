package nodestore

import (
	"bytes"
	"context"
	"slices"
	"testing"

	"github.com/LeJamon/go-xrpl/storage/kvstore"
	"github.com/LeJamon/go-xrpl/storage/kvstore/memorydb"
	kvpebble "github.com/LeJamon/go-xrpl/storage/kvstore/pebble"
	"github.com/stretchr/testify/require"
)

func TestFetchBatchUncached(t *testing.T) {
	for _, backend := range []string{"scalar", "pebble", "rotating"} {
		t.Run(backend, func(t *testing.T) {
			var store kvstore.KeyValueStore
			var err error
			switch backend {
			case "scalar":
				store = memorydb.New()
			case "pebble":
				store, err = kvpebble.New(t.TempDir(), kvpebble.Options{})
			case "rotating":
				store, err = kvpebble.NewRotating(t.TempDir(), kvpebble.Options{})
			}
			require.NoError(t, err)
			db, err := NewKVDatabase(store, DefaultDatabaseConfig())
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, db.Close()) })
			first := &Node{Type: NodeAccount, Hash: Hash256{1}, Data: []byte("archive"), LedgerSeq: 1}
			second := &Node{Type: NodeTransaction, Hash: Hash256{3}, Data: []byte("writable"), LedgerSeq: 2}
			require.NoError(t, db.Store(t.Context(), first))
			if rotating, ok := store.(kvstore.RotatingStore); ok {
				committed, err := rotating.Rotate(2, 1)
				require.NoError(t, err)
				require.True(t, committed)
			}
			require.NoError(t, db.Store(t.Context(), second))
			db.cache.Clear()
			missing := Hash256{2}
			// A stale negative entry must not hide a durable record during verification.
			db.negativeCache.MarkMissing(first.Hash)
			hashes := []Hash256{second.Hash, missing, first.Hash, first.Hash}
			original := slices.Clone(hashes)
			nodes, err := db.FetchBatchUncached(t.Context(), hashes, 8, 1024)
			require.NoError(t, err)
			require.Equal(t, []*Node{first, first, nil, second}, nodes)
			require.Equal(t, original, hashes)
			require.Zero(t, db.cache.Size())
			require.False(t, db.negativeCache.IsMissing(missing))
			db.negativeCache.Remove(first.Hash)
			for _, node := range []*Node{first, second} {
				data, err := db.FetchDataUncached(t.Context(), node.Hash)
				require.NoError(t, err)
				require.Equal(t, node.Data, data)
			}
			for _, limits := range [][3]int{{1, 1024, 1}, {8, 1, 1}, {8, 2 * (nodeEncodingHeaderSize + len(first.Data)), 2}} {
				nodes, err := db.FetchBatchUncached(t.Context(), hashes, limits[0], limits[1])
				require.NoError(t, err)
				require.Len(t, nodes, limits[2])
			}
			nodes[0].Data[0] ^= 1
			again, err := db.FetchBatchUncached(t.Context(), []Hash256{first.Hash}, 1, 1024)
			require.NoError(t, err)
			require.Equal(t, first, again[0])
			if rotating, ok := store.(kvstore.RotatingStore); ok {
				committed, err := rotating.Rotate(3, 2)
				require.NoError(t, err)
				require.True(t, committed)
				nodes, err := db.FetchBatchUncached(t.Context(), []Hash256{first.Hash, second.Hash}, 2, 1024)
				require.NoError(t, err)
				require.Equal(t, []*Node{nil, second}, nodes, "archive reads must not promote")
			}
		})
	}
}

func TestFetchBatchUncachedInvalidRecords(t *testing.T) {
	for _, batch := range []bool{false, true} {
		for _, data := range [][]byte{nil, {1}, {255, 0, 0, 0, 1, 42}, {byte(NodeAccount), 0, 0, 0, 1}} {
			var store kvstore.KeyValueStore = memorydb.New()
			if batch {
				var err error
				store, err = kvpebble.New(t.TempDir(), kvpebble.Options{})
				require.NoError(t, err)
			}
			db, err := NewKVDatabase(store, DatabaseConfig{})
			require.NoError(t, err)
			hash := Hash256{1}
			require.NoError(t, store.Put(hash[:], data))
			_, scalarErr := db.FetchDataUncached(t.Context(), hash)
			_, batchErr := db.FetchBatchUncached(t.Context(), []Hash256{hash}, 1, 1024)
			require.ErrorIs(t, scalarErr, ErrDataCorrupt)
			require.EqualError(t, batchErr, scalarErr.Error())
			require.NoError(t, db.Close())
		}
	}
}

func TestFetchBatchUncachedLifecycleAndLimits(t *testing.T) {
	db, err := NewKVDatabase(memorydb.New(), DatabaseConfig{})
	require.NoError(t, err)
	hashes := []Hash256{{1}}
	for _, limits := range [][2]int{{0, 1}, {1, 0}, {-1, 1}} {
		_, err := db.FetchBatchUncached(t.Context(), hashes, limits[0], limits[1])
		require.Error(t, err)
	}
	nodes, err := db.FetchBatchUncached(t.Context(), nil, 0, 0)
	require.NoError(t, err)
	require.Empty(t, nodes)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = db.FetchBatchUncached(ctx, hashes, 1, 1)
	require.ErrorIs(t, err, context.Canceled)
	require.NoError(t, db.Close())
	_, err = db.FetchBatchUncached(t.Context(), hashes, 1, 1)
	require.ErrorIs(t, err, ErrClosed)
}

type invalidBatchReadStore struct {
	kvstore.KeyValueStore
	results []kvstore.ReadResult
	read    func()
}

func (s invalidBatchReadStore) GetBatch(context.Context, [][]byte, int, int) ([]kvstore.ReadResult, error) {
	if s.read != nil {
		s.read()
	}
	return s.results, nil
}

func TestFetchBatchUncachedRejectsInvalidMapping(t *testing.T) {
	for _, results := range [][]kvstore.ReadResult{nil, {{Key: bytes.Repeat([]byte{2}, 32)}}, {{}, {}}} {
		db, err := NewKVDatabase(invalidBatchReadStore{KeyValueStore: memorydb.New(), results: results}, DatabaseConfig{})
		require.NoError(t, err)
		_, err = db.FetchBatchUncached(t.Context(), []Hash256{{1}}, 1, 1024)
		require.Error(t, err)
		require.NoError(t, db.Close())
	}
}

func TestFetchBatchUncachedCancellationAfterRead(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	hash := Hash256{1}
	db, err := NewKVDatabase(invalidBatchReadStore{
		KeyValueStore: memorydb.New(),
		results:       []kvstore.ReadResult{{Key: hash[:]}},
		read:          cancel,
	}, DatabaseConfig{})
	require.NoError(t, err)
	defer db.Close()
	nodes, err := db.FetchBatchUncached(ctx, []Hash256{hash}, 1, 1024)
	require.ErrorIs(t, err, context.Canceled)
	require.Nil(t, nodes)
}
