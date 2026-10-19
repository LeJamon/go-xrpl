package nodestore

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/storage/kvstore"
	"github.com/LeJamon/go-xrpl/storage/kvstore/memorydb"
	"github.com/stretchr/testify/require"
)

type boundedReadStore struct {
	*memorydb.MemDatabase
	limits []int
}

func (s *boundedReadStore) Get([]byte) ([]byte, error) {
	panic("bounded read fell back to an unbounded copy")
}

func (s *boundedReadStore) GetBounded(ctx context.Context, key []byte, maxBytes int) ([]byte, error) {
	s.limits = append(s.limits, maxBytes)
	return s.MemDatabase.GetBounded(ctx, key, maxBytes)
}

func TestFetchPeerBoundedColdAndCached(t *testing.T) {
	store := &boundedReadStore{MemDatabase: memorydb.New()}
	db, err := NewKVDatabase(store, DatabaseConfig{})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	node := testNode(NodeAccount, bytes.Repeat([]byte{42}, 4096), 0)
	require.NoError(t, db.Store(t.Context(), node))

	got, err := db.FetchPeerBounded(t.Context(), node.Hash, 4095)
	require.ErrorIs(t, err, kvstore.ErrValueTooLarge)
	require.Nil(t, got)
	require.Equal(t, []int{4095 + nodeEncodingHeaderSize}, store.limits)
	got, err = db.FetchPeerBounded(t.Context(), node.Hash, 4096)
	require.NoError(t, err)
	require.Equal(t, node.Data, got.Data)

	db.cache = newNodeCache(64, time.Hour)
	db.cache.Put(node)
	store.limits = nil
	got, err = db.FetchPeerBounded(t.Context(), node.Hash, 4095)
	require.ErrorIs(t, err, kvstore.ErrValueTooLarge)
	require.Nil(t, got)
	got, err = db.FetchPeerBounded(t.Context(), node.Hash, 4096)
	require.NoError(t, err)
	require.Equal(t, node.Data, got.Data)
	require.Empty(t, store.limits, "cached reads do not fetch backend records")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = db.FetchPeerBounded(ctx, node.Hash, 4096)
	require.ErrorIs(t, err, context.Canceled)
}

type unboundedOnlyStore struct{ kvstore.KeyValueStore }

func TestFetchPeerBoundedRequiresStrictBackend(t *testing.T) {
	db, err := NewKVDatabase(unboundedOnlyStore{memorydb.New()}, DatabaseConfig{})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	_, err = db.FetchPeerBounded(t.Context(), Hash256{1}, 1024)
	require.ErrorIs(t, err, errors.ErrUnsupported)
}
