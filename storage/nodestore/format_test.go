package nodestore

import (
	"encoding/binary"
	"errors"
	"path/filepath"
	"testing"

	"github.com/LeJamon/go-xrpl/storage/kvstore"
	"github.com/LeJamon/go-xrpl/storage/kvstore/memorydb"
	kvpebble "github.com/LeJamon/go-xrpl/storage/kvstore/pebble"
	"github.com/stretchr/testify/require"
)

var formatTestPebbleOptions = kvpebble.Options{BlockCacheBytes: 16 << 20, MaxOpenFiles: 200}

func requireFormatMarker(t *testing.T, store kvstore.KeyValueStore) {
	t.Helper()
	value, err := store.Get(formatMarkerKey)
	require.NoError(t, err)
	require.Equal(t, binary.BigEndian.AppendUint32(nil, FormatVersion), value)
}

func putLegacyNode(t *testing.T, store kvstore.KeyValueStore) *Node {
	t.Helper()
	node := testNode(NodeAccount, []byte("legacy-node"), 7)
	require.NoError(t, store.Put(node.Hash[:], []byte("\x03\x00\x00\x00\x07legacy-node")))
	return node
}

func TestFormatMarkerStampedOnNewStore(t *testing.T) {
	store := memorydb.New()
	testDatabase(t, store, noCacheConfig())
	requireFormatMarker(t, store)
}

func TestFormatMarkerAdoptsLegacyStoreAndSurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nodes")
	legacy, err := kvpebble.New(path, formatTestPebbleOptions)
	require.NoError(t, err)
	node := putLegacyNode(t, legacy)
	_, err = legacy.Get(formatMarkerKey)
	require.ErrorIs(t, err, kvstore.ErrNotFound)
	require.NoError(t, legacy.Close())

	for range 2 {
		store, err := kvpebble.New(path, formatTestPebbleOptions)
		require.NoError(t, err)
		db, err := NewKVDatabase(store, noCacheConfig())
		require.NoError(t, err)
		requireFormatMarker(t, store)
		fetched, err := db.Fetch(t.Context(), node.Hash)
		require.NoError(t, err)
		require.Equal(t, node.Data, fetched.Data)
		require.Equal(t, node.LedgerSeq, fetched.LedgerSeq)
		require.NoError(t, db.Close())
	}
}

func TestFormatMarkerRejectsOtherVersions(t *testing.T) {
	tests := []struct {
		name   string
		marker []byte
		want   error
	}{
		{"newer", binary.BigEndian.AppendUint32(nil, FormatVersion+1), ErrUnsupportedFormat},
		{"zero", binary.BigEndian.AppendUint32(nil, 0), ErrUnsupportedFormat},
		{"truncated", []byte{1}, ErrDataCorrupt},
		{"oversized", []byte{0, 0, 0, 1, 0}, ErrDataCorrupt},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := memorydb.New()
			require.NoError(t, store.Put(formatMarkerKey, test.marker))
			db, err := NewKVDatabase(store, noCacheConfig())
			require.ErrorIs(t, err, test.want)
			require.Nil(t, db)
			value, err := store.Get(formatMarkerKey)
			require.NoError(t, err)
			require.Equal(t, test.marker, value, "a rejected marker is left untouched")
		})
	}
}

func TestFormatMarkerOpenPropagatesBackendErrors(t *testing.T) {
	getErr := errors.New("get failed")
	_, err := NewKVDatabase(&databaseErrorStore{KeyValueStore: memorydb.New(), getErr: getErr}, noCacheConfig())
	require.ErrorIs(t, err, getErr)

	putErr := errors.New("put failed")
	_, err = NewKVDatabase(&databaseErrorStore{KeyValueStore: memorydb.New(), putErr: putErr}, noCacheConfig())
	require.ErrorIs(t, err, putErr)
}

func TestFormatMarkerReadOnlyOpenDoesNotStamp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nodes")
	legacy, err := kvpebble.New(path, formatTestPebbleOptions)
	require.NoError(t, err)
	node := putLegacyNode(t, legacy)
	require.NoError(t, legacy.Close())

	store, err := kvpebble.NewReadOnly(path, formatTestPebbleOptions)
	require.NoError(t, err)
	db, err := NewKVDatabase(store, noCacheConfig())
	require.NoError(t, err)
	fetched, err := db.Fetch(t.Context(), node.Hash)
	require.NoError(t, err)
	require.Equal(t, node.Data, fetched.Data)
	_, err = store.Get(formatMarkerKey)
	require.ErrorIs(t, err, kvstore.ErrNotFound)
	require.NoError(t, db.Close())

	writable, err := kvpebble.New(path, formatTestPebbleOptions)
	require.NoError(t, err)
	require.NoError(t, writable.Put(formatMarkerKey, binary.BigEndian.AppendUint32(nil, FormatVersion+1)))
	require.NoError(t, writable.Close())

	store, err = kvpebble.NewReadOnly(path, formatTestPebbleOptions)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	_, err = NewKVDatabase(store, noCacheConfig())
	require.ErrorIs(t, err, ErrUnsupportedFormat)
}

func TestFormatMarkerSurvivesDeleteBefore(t *testing.T) {
	store := memorydb.New()
	db := testDatabase(t, store, noCacheConfig())
	old := storeNodeAt(t, db, 5, 1)
	kept := storeNodeAt(t, db, 20, 2)

	deleted, err := db.DeleteBefore(t.Context(), 10, 0)
	require.NoError(t, err)
	require.Equal(t, uint64(1), deleted)
	require.False(t, fetchPresent(t, db, old))
	require.True(t, fetchPresent(t, db, kept))
	requireFormatMarker(t, store)
}

func TestFormatMarkerSurvivesRotation(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "nodes")

	legacy, err := kvpebble.New(path, formatTestPebbleOptions)
	require.NoError(t, err)
	putLegacyNode(t, legacy)
	require.NoError(t, legacy.Close())

	store, err := kvpebble.NewRotating(path, formatTestPebbleOptions)
	require.NoError(t, err)
	db, err := NewRotatingKVDatabase(store, noCacheConfig())
	require.NoError(t, err)
	requireFormatMarker(t, store)

	// Three rotations retire every generation that existed at open.
	for i := range uint32(3) {
		committed, err := db.RotateGeneration(ctx, 10*(i+1), 10*i+1)
		require.NoError(t, err)
		require.True(t, committed)
		requireFormatMarker(t, store)
	}
	require.NoError(t, db.Close())

	reopened, err := kvpebble.NewRotating(path, formatTestPebbleOptions)
	require.NoError(t, err)
	requireFormatMarker(t, reopened)
	require.NoError(t, reopened.Put(formatMarkerKey, binary.BigEndian.AppendUint32(nil, FormatVersion+1)))
	_, err = NewRotatingKVDatabase(reopened, noCacheConfig())
	require.ErrorIs(t, err, ErrUnsupportedFormat)
	require.NoError(t, reopened.Close())
}

type failingRotationPut struct {
	*kvpebble.RotatingStore
	putErr error
}

func (s *failingRotationPut) Put(key, value []byte) error {
	if s.putErr != nil {
		return s.putErr
	}
	return s.RotatingStore.Put(key, value)
}

func TestFormatMarkerWriteFailureDuringRotation(t *testing.T) {
	backend, err := kvpebble.NewRotating(filepath.Join(t.TempDir(), "nodes"), formatTestPebbleOptions)
	require.NoError(t, err)
	store := &failingRotationPut{RotatingStore: backend}
	db, err := NewRotatingKVDatabase(store, noCacheConfig())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	ctx := t.Context()
	committed, err := db.RotateGeneration(ctx, 10, 2)
	require.NoError(t, err)
	require.True(t, committed)

	store.putErr = errors.New("marker write failed")
	committed, err = db.RotateGeneration(ctx, 10, 2)
	require.NoError(t, err)
	require.True(t, committed)

	for _, floor := range []uint32{0, 1, 21} {
		committed, err = db.RotateGeneration(ctx, 20, floor)
		require.Error(t, err)
		require.NotErrorIs(t, err, store.putErr)
		require.False(t, committed)
	}

	committed, err = db.RotateGeneration(ctx, 20, 11)
	require.ErrorIs(t, err, store.putErr)
	require.False(t, committed)
	last, minimum := db.GenerationState()
	require.Equal(t, uint32(10), last)
	require.Equal(t, uint32(2), minimum)
}

func TestFormatMarkerChecksShadowedArchive(t *testing.T) {
	store, err := kvpebble.NewRotating(filepath.Join(t.TempDir(), "nodes"), formatTestPebbleOptions)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	require.NoError(t, store.Put(formatMarkerKey, []byte{1}))
	committed, err := store.Rotate(10, 1)
	require.NoError(t, err)
	require.True(t, committed)
	require.NoError(t, writeFormatMarker(store))

	_, err = NewRotatingKVDatabase(store, noCacheConfig())
	require.ErrorIs(t, err, ErrDataCorrupt)
}
