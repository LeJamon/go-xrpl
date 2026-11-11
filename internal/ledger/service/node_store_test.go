package service

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/LeJamon/go-xrpl/shamap"
	shamapbackend "github.com/LeJamon/go-xrpl/shamap/backend"
	"github.com/LeJamon/go-xrpl/storage/kvstore/memorydb"
	kvpebble "github.com/LeJamon/go-xrpl/storage/kvstore/pebble"
	"github.com/LeJamon/go-xrpl/storage/nodestore"
	"github.com/stretchr/testify/require"
)

type plainCapabilityDatabase struct{ nodestore.Database }

type durableCapabilityDatabase struct {
	nodestore.Database
	nodestore.DurableDatabase
}

type snapshotCapabilityDatabase struct {
	nodestore.Database
	nodestore.DurableSnapshotDatabase
}

type generationCapabilityDatabase struct{ nodestore.GenerationDatabase }

type uncachedCapabilityDatabase struct {
	nodestore.Database
	uncachedNodeReader
	uncachedBatchNodeReader
}

func TestBindNodeStore(t *testing.T) {
	kv, err := nodestore.NewKVDatabase(memorydb.New(), nodestore.DatabaseConfig{})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, kv.Close()) })
	rotatingStore, err := kvpebble.NewRotating(filepath.Join(t.TempDir(), "nodes"), kvpebble.Options{BlockCacheBytes: 8 << 20, MaxOpenFiles: 200})
	require.NoError(t, err)
	rotating, err := nodestore.NewRotatingKVDatabase(rotatingStore, nodestore.DatabaseConfig{})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, rotating.Close()) })

	type want struct{ durable, snapshot, generations, uncached, uncachedBatch bool }
	for _, tc := range []struct {
		name string
		db   nodestore.Database
		want want
	}{
		{name: "none", db: &plainCapabilityDatabase{}},
		{name: "durable only", db: &durableCapabilityDatabase{}, want: want{durable: true}},
		{name: "durable snapshot", db: &snapshotCapabilityDatabase{}, want: want{durable: true, snapshot: true}},
		{name: "generations only", db: &generationCapabilityDatabase{}, want: want{generations: true}},
		{name: "uncached reads only", db: &uncachedCapabilityDatabase{}, want: want{uncached: true, uncachedBatch: true}},
		{name: "kv database", db: kv, want: want{durable: true, snapshot: true, uncached: true, uncachedBatch: true}},
		{name: "rotating database", db: rotating, want: want{durable: true, snapshot: true, generations: true, uncached: true, uncachedBatch: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bound := bindNodeStore(tc.db)
			require.NotNil(t, bound)
			require.Equal(t, tc.db, bound.database())
			caps := bound.capabilities()
			require.Equal(t, tc.want, want{
				durable:       caps.durable != nil,
				snapshot:      caps.durableSnapshot != nil,
				generations:   caps.generations != nil,
				uncached:      caps.uncached != nil,
				uncachedBatch: caps.uncachedBatch != nil,
			})
			if caps.durable != nil {
				require.Equal(t, tc.db, caps.durable)
			}
			if caps.durableSnapshot != nil {
				require.Equal(t, tc.db, caps.durableSnapshot)
			}
			if caps.generations != nil {
				require.Equal(t, tc.db, caps.generations)
			}
		})
	}

	t.Run("absent store", func(t *testing.T) {
		bound := bindNodeStore(nil)
		require.Nil(t, bound)
		require.Nil(t, bound.database())
		require.Equal(t, nodeStoreCapabilities{}, bound.capabilities())
	})
}

// Passing the wrapper where a raw store is expected would silently break
// identity checks such as UsesDatabase; keep that a compile error.
func TestBoundNodeStoreIsNotADatabase(t *testing.T) {
	database := reflect.TypeOf((*nodestore.Database)(nil)).Elem()
	require.False(t, reflect.TypeOf((*boundNodeStore)(nil)).Implements(database))
	require.False(t, reflect.TypeOf(boundNodeStore{}).Implements(database))
}

func TestNodeStoreFamilyCapability(t *testing.T) {
	var family shamap.Family = shamapbackend.New(nil)
	_, ok := family.(nodeStoreFamily)
	require.True(t, ok)
	_, ok = shamap.Family(&plainPersistFamily{Family: family}).(nodeStoreFamily)
	require.False(t, ok)
}

func TestNodeStoreCapabilityAbsentPaths(t *testing.T) {
	plain := &Service{nodeStore: bindNodeStore(&plainCapabilityDatabase{})}
	empty := &Service{}

	_, _, err := plain.PrepareStateRetention(t.Context(), 1, nil)
	require.EqualError(t, err, "state retention requires durable snapshots")

	for _, svc := range []*Service{plain, empty} {
		release, err := svc.AcquireStateAdmission(t.Context())
		require.NoError(t, err)
		require.NotNil(t, release)
		release()
		require.Nil(t, svc.storedSHAMapVerificationBatchFetch())
	}

	_, _, err = plain.fastLoadDurability()
	require.EqualError(t, err, "NodeStore has no durable checkpoint capability")
	_, err = empty.fetchDurableNodeData(t.Context(), nodestore.Hash256{})
	require.EqualError(t, err, "NodeStore is unavailable")
	require.Nil(t, plain.replaySnapshotProvider())
}

type plainPersistFamily struct {
	shamap.Family
	storeBatches int
}

func (f *plainPersistFamily) StoreBatch(ctx context.Context, entries []shamap.FlushEntry) error {
	f.storeBatches++
	return f.Family.StoreBatch(ctx, entries)
}

func TestPersistWithoutNodeStoreFamilyWritesNodeStore(t *testing.T) {
	ctx := t.Context()
	db, err := nodestore.NewKVDatabase(memorydb.New(), nodestore.DatabaseConfig{})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	family := &plainPersistFamily{Family: shamapbackend.New(db)}

	state, err := shamap.NewBacked(shamap.TypeState, family)
	require.NoError(t, err)
	for _, entry := range residencyEntries(9, 0, 16) {
		require.NoError(t, state.Put(entry.key, entry.value))
	}
	l := retentionTestLedger(t, state, 9)
	svc := &Service{nodeStore: bindNodeStore(db), shamapFamily: family}
	before := family.storeBatches
	require.NoError(t, svc.persistToNodeStore(ctx, l, l.Sequence()))
	require.Equal(t, before, family.storeBatches)

	root, err := state.Hash()
	require.NoError(t, err)
	node, err := db.Fetch(ctx, nodestore.Hash256(root))
	require.NoError(t, err)
	require.NotNil(t, node)
	require.Equal(t, nodestore.NodeAccount, node.Type)
	require.Equal(t, l.Sequence(), node.LedgerSeq)
	header, err := db.Fetch(ctx, nodestore.Hash256(l.Hash()))
	require.NoError(t, err)
	require.NotNil(t, header)
	require.Equal(t, nodestore.NodeLedger, header.Type)
}
