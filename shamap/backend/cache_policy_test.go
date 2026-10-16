package backend

import (
	"bytes"
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/membudget"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/LeJamon/go-xrpl/storage/kvstore"
	"github.com/LeJamon/go-xrpl/storage/kvstore/memorydb"
	"github.com/LeJamon/go-xrpl/storage/nodestore"
	"github.com/stretchr/testify/require"
)

type policyCountingStore struct {
	kvstore.KeyValueStore
	reads atomic.Uint64
}

func (s *policyCountingStore) Get(key []byte) ([]byte, error) {
	s.reads.Add(1)
	return s.KeyValueStore.Get(key)
}

func TestPeerScansProtectBackedForegroundAcrossCacheLayers(t *testing.T) {
	for _, order := range []string{"foreground-first", "peer-first"} {
		t.Run(order, func(t *testing.T) {
			pool := membudget.New(128 << 10)
			progress, err := pool.NewAccount("progress", 32<<10, 0)
			require.NoError(t, err)
			lower, err := pool.NewAccount("node_cache", 12<<10, 24<<10)
			require.NoError(t, err)
			upper, err := pool.NewAccount("shamap_cache", 24<<10, 48<<10)
			require.NoError(t, err)
			store := &policyCountingStore{KeyValueStore: memorydb.New()}
			db, err := nodestore.NewKVDatabase(store, nodestore.DatabaseConfig{PositiveCache: nodestore.CacheConfig{Enabled: true, MaxEntries: 1024, TTL: time.Hour, MemoryBudget: lower}})
			require.NoError(t, err)
			family := New(db, NodeStoreOptions{CleanCacheBytes: 48 << 10, CleanMemoryBudget: upper})
			t.Cleanup(func() { require.NoError(t, family.Close()) })
			source := shamap.New(shamap.TypeState)
			hot := make([][32]byte, 8)
			for i := range hot {
				hot[i] = [32]byte{1, byte(i), 1}
				require.NoError(t, source.Put(hot[i], bytes.Repeat([]byte{1}, 24)))
			}
			for i := 0; i < 1024; i++ {
				require.NoError(t, source.Put([32]byte{0x80 + byte(i/16), byte(i%16) << 4, 1}, bytes.Repeat([]byte{2}, 24)))
			}
			var records []shamap.FlushEntry
			require.NoError(t, source.StoreDirty(func(entries []shamap.FlushEntry) error {
				records = append(records, entries...)
				return family.StoreBatch(t.Context(), entries)
			}))
			hash, err := source.Hash()
			require.NoError(t, err)
			tree, err := shamap.NewFromRootHash(shamap.TypeState, hash, family)
			require.NoError(t, err)
			foreground := func() {
				for _, key := range hot {
					item, found, err := tree.Get(key)
					require.NoError(t, err)
					require.True(t, found)
					require.Equal(t, bytes.Repeat([]byte{1}, 24), item.Data())
				}
			}
			lowerWarm := func() {
				for _, record := range records {
					_, err := db.Fetch(t.Context(), nodestore.Hash256(record.Hash))
					require.NoError(t, err)
				}
			}
			if order == "foreground-first" {
				foreground()
				lowerWarm()
			} else {
				lowerWarm()
				foreground()
			}
			initial := family.clean.Stats()
			require.LessOrEqual(t, initial.Foreground.UsedBytes, uint64(18<<10), "fixture must fit protected foreground capacity")
			pressure := progress.Snapshot().Available
			require.True(t, progress.TryReserve(pressure))
			beforeLower := db.Stats()
			for pass := 0; pass < 3; pass++ {
				for peer := 0; peer < 4; peer++ {
					for subtree := 0; subtree < 16; subtree++ {
						nodes, err := tree.GetNodeFatByPath([32]byte{0x80 + byte(peer*16+subtree)}, 2, 1, true)
						require.NoError(t, err)
						require.NotEmpty(t, nodes)
						beforeRead := store.reads.Load()
						foreground()
						require.Equal(t, beforeRead, store.reads.Load(), "peer scans displaced the protected foreground working set")
					}
				}
			}
			after := family.clean.Stats()
			require.Equal(t, initial.Foreground.Admissions, after.Foreground.Admissions)
			require.Equal(t, initial.Foreground.Evictions, after.Foreground.Evictions)
			require.Equal(t, beforeLower.Foreground.Admissions, db.Stats().Foreground.Admissions)
			require.Zero(t, db.Stats().Peer.Admissions)
			require.Positive(t, db.Stats().Peer.Bypasses)
			require.Equal(t, after.UsedBytes, upper.Snapshot().Used)
			require.LessOrEqual(t, pool.Snapshot().Used, pool.Snapshot().Limit)
			progress.Release(pressure)
			beforeRead := store.reads.Load()
			foreground()
			require.Equal(t, beforeRead, store.reads.Load(), "foreground recovery after scans must require no reload")
			wantPath := [32]byte{0x80}
			_, err = tree.GetNodeFatByPath(wantPath, 3, 1, true)
			require.NoError(t, err)
			beforeRead = store.reads.Load()
			_, err = tree.GetNodeFatByPath(wantPath, 3, 1, true)
			require.NoError(t, err)
			require.Equal(t, beforeRead, store.reads.Load(), "peer reuse must remain useful")
			afterHash, err := tree.Hash()
			require.NoError(t, err)
			require.Equal(t, hash, afterHash)
			require.NoError(t, family.Close())
			require.Zero(t, pool.Snapshot().Used)
			require.Zero(t, pool.Snapshot().InvalidReleases)
		})
	}
}

func TestSHAMapFetchAdmissionDoesNotDuplicateNodeStoreResidency(t *testing.T) {
	store := &policyCountingStore{KeyValueStore: memorydb.New()}
	db, err := nodestore.NewKVDatabase(store, nodestore.DefaultDatabaseConfig())
	require.NoError(t, err)
	family := New(db)
	defer family.Close()
	tree, err := shamap.NewBacked(shamap.TypeState, family)
	require.NoError(t, err)
	key := [32]byte{1}
	require.NoError(t, tree.Put(key, bytes.Repeat([]byte{7}, 24)))
	require.NoError(t, tree.StoreDirtyAndRelease(func(entries []shamap.FlushEntry) error { return family.StoreBatch(context.Background(), entries) }))
	before := db.Stats()
	_, found, err := tree.Get(key)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, before.Foreground.Admissions, db.Stats().Foreground.Admissions)
	require.Positive(t, family.clean.Stats().Foreground.Admissions)
}
