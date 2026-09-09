package pebble_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/LeJamon/go-xrpl/storage/kvstore"
	kvpebble "github.com/LeJamon/go-xrpl/storage/kvstore/pebble"
	"github.com/stretchr/testify/require"
)

var _ kvstore.BatchReadingStore = (*kvpebble.Store)(nil)
var _ kvstore.BatchReadingStore = (*kvpebble.RotatingStore)(nil)

func TestStoreGetBatchOrderingAndOwnership(t *testing.T) {
	store, err := kvpebble.New(t.TempDir(), kvpebble.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	for key, value := range map[string][]byte{
		"a":     []byte("alpha"),
		"d":     []byte("delta"),
		"empty": {},
	} {
		require.NoError(t, store.Put([]byte(key), value))
	}
	keys := [][]byte{
		[]byte("d"),
		[]byte("missing"),
		[]byte("a"),
		[]byte("a"),
		[]byte("empty"),
	}
	original := [][]byte{
		append([]byte(nil), keys[0]...),
		append([]byte(nil), keys[1]...),
		append([]byte(nil), keys[2]...),
		append([]byte(nil), keys[3]...),
		append([]byte(nil), keys[4]...),
	}

	results, err := store.GetBatch(t.Context(), keys, 10, 1024)
	require.NoError(t, err)
	require.Equal(t, []string{"a", "a", "d", "empty", "missing"}, resultKeys(results))
	require.True(t, results[0].Found)
	require.Equal(t, []byte("alpha"), results[0].Value)
	require.True(t, results[1].Found)
	require.Equal(t, []byte("alpha"), results[1].Value)
	require.True(t, results[2].Found)
	require.Equal(t, []byte("delta"), results[2].Value)
	require.True(t, results[3].Found)
	require.Empty(t, results[3].Value)
	require.False(t, results[4].Found)
	require.Nil(t, results[4].Value)
	for index := range keys {
		require.Equal(t, original[index], keys[index])
	}

	results[0].Key[0] = 'z'
	results[0].Value[0] = 'z'
	got, err := store.Get([]byte("a"))
	require.NoError(t, err)
	require.Equal(t, []byte("alpha"), got)
	require.Equal(t, []byte("a"), keys[2])
}

func TestStoreGetBatchLimits(t *testing.T) {
	store, err := kvpebble.New(t.TempDir(), kvpebble.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	for key, value := range map[string][]byte{
		"a":     []byte("12"),
		"big":   []byte("12345"),
		"c":     []byte("6"),
		"empty": {},
	} {
		require.NoError(t, store.Put([]byte(key), value))
	}

	results, err := store.GetBatch(t.Context(), [][]byte{[]byte("c"), []byte("a"), []byte("big")}, 2, 1024)
	require.NoError(t, err)
	require.Equal(t, []string{"a", "big"}, resultKeys(results))

	results, err = store.GetBatch(t.Context(), [][]byte{[]byte("c"), []byte("a"), []byte("big")}, 10, 2)
	require.NoError(t, err)
	require.Equal(t, []string{"a"}, resultKeys(results))

	results, err = store.GetBatch(t.Context(), [][]byte{[]byte("big"), []byte("c")}, 10, 1)
	require.NoError(t, err)
	require.Equal(t, []string{"big"}, resultKeys(results))
	require.Equal(t, []byte("12345"), results[0].Value)

	results, err = store.GetBatch(t.Context(), [][]byte{[]byte("empty"), []byte("c")}, 10, 1)
	require.NoError(t, err)
	require.Equal(t, []string{"c"}, resultKeys(results))

	results, err = store.GetBatch(t.Context(), [][]byte{[]byte("a"), []byte("missing")}, 10, 2)
	require.NoError(t, err)
	require.Equal(t, []string{"a"}, resultKeys(results))

	results, err = store.GetBatch(t.Context(), [][]byte{[]byte("a"), []byte("big")}, 1, 1024)
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Equal(t, "a", string(results[0].Key))
}

func TestStoreGetBatchValidationCancellationAndClose(t *testing.T) {
	store, err := kvpebble.New(t.TempDir(), kvpebble.Options{})
	require.NoError(t, err)
	require.NoError(t, store.Put([]byte("key"), []byte("value")))
	t.Cleanup(func() { _ = store.Close() })

	_, err = store.GetBatch(t.Context(), [][]byte{[]byte("key")}, 0, 1)
	require.Error(t, err)
	_, err = store.GetBatch(t.Context(), [][]byte{[]byte("key")}, 1, 0)
	require.Error(t, err)
	results, err := store.GetBatch(t.Context(), nil, 0, 0)
	require.NoError(t, err)
	require.Empty(t, results)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = store.GetBatch(ctx, [][]byte{[]byte("key")}, 1, 1024)
	require.ErrorIs(t, err, context.Canceled)

	require.NoError(t, store.Close())
	_, err = store.GetBatch(t.Context(), [][]byte{[]byte("key")}, 1, 1024)
	require.ErrorIs(t, err, kvstore.ErrClosed)
}

func TestRotatingStoreGetBatchUsesWritablePrecedenceWithoutPromotion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nodes")
	store, err := kvpebble.NewRotating(path, kvpebble.Options{BlockCacheBytes: 16 << 20, MaxOpenFiles: 200})
	require.NoError(t, err)

	require.NoError(t, store.Put([]byte("archive-only"), []byte("archive")))
	require.NoError(t, store.Put([]byte("shared"), []byte("archive-shared")))
	committed, err := store.Rotate(11, 1)
	require.True(t, committed)
	require.NoError(t, err)
	require.NoError(t, store.Put([]byte("shared"), []byte("writable-shared")))
	require.NoError(t, store.Put([]byte("writable-only"), []byte("writable")))
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	results, err := store.GetBatch(t.Context(), [][]byte{
		[]byte("writable-only"),
		[]byte("missing"),
		[]byte("shared"),
		[]byte("archive-only"),
		[]byte("shared"),
	}, 10, 1024)
	require.NoError(t, err)
	require.Equal(t, []string{"archive-only", "missing", "shared", "shared", "writable-only"}, resultKeys(results))
	require.True(t, results[0].Found)
	require.Equal(t, []byte("archive"), results[0].Value)
	require.False(t, results[1].Found)
	require.True(t, results[2].Found)
	require.Equal(t, []byte("writable-shared"), results[2].Value)
	require.Equal(t, []byte("writable-shared"), results[3].Value)
	require.Equal(t, []byte("writable"), results[4].Value)

	committed, err = store.Rotate(21, 12)
	require.True(t, committed)
	require.NoError(t, err)
	_, err = store.Get([]byte("archive-only"))
	require.ErrorIs(t, err, kvstore.ErrNotFound)
}

func TestRotatingStoreGetBatchCancellationAndClose(t *testing.T) {
	store, err := kvpebble.NewRotating(filepath.Join(t.TempDir(), "nodes"), kvpebble.Options{BlockCacheBytes: 16 << 20, MaxOpenFiles: 200})
	require.NoError(t, err)
	require.NoError(t, store.Put([]byte("key"), []byte("value")))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = store.GetBatch(ctx, [][]byte{[]byte("key")}, 1, 1024)
	require.ErrorIs(t, err, context.Canceled)
	require.NoError(t, store.Close())
	_, err = store.GetBatch(t.Context(), [][]byte{[]byte("key")}, 1, 1024)
	require.ErrorIs(t, err, kvstore.ErrClosed)
}

func resultKeys(results []kvstore.ReadResult) []string {
	keys := make([]string, len(results))
	for index, result := range results {
		keys[index] = string(result.Key)
	}
	return keys
}
