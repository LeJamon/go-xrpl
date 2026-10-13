package service

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/LeJamon/go-xrpl/shamap"
	shamapbackend "github.com/LeJamon/go-xrpl/shamap/backend"
	kvpebble "github.com/LeJamon/go-xrpl/storage/kvstore/pebble"
	"github.com/LeJamon/go-xrpl/storage/nodestore"
	"github.com/stretchr/testify/require"
)

type residencyFaultDatabase struct {
	nodestore.Database
	storeErr error
	syncErr  error
}

func (d *residencyFaultDatabase) Store(ctx context.Context, node *nodestore.Node) error {
	if d.storeErr != nil {
		return d.storeErr
	}
	return d.Database.Store(ctx, node)
}

func (d *residencyFaultDatabase) Sync(ctx context.Context) error {
	if d.syncErr != nil {
		return d.syncErr
	}
	return d.Database.Sync(ctx)
}

func TestService_PersistenceFailureRetainsResidencyUntilSync(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  func(*residencyFaultDatabase, error)
	}{
		{name: "header", set: func(db *residencyFaultDatabase, err error) { db.storeErr = err }},
		{name: "sync", set: func(db *residencyFaultDatabase, err error) { db.syncErr = err }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			path := filepath.Join(t.TempDir(), "nodes")
			store, err := kvpebble.NewRotating(path, kvpebble.Options{BlockCacheBytes: 8 << 20, MaxOpenFiles: 64})
			require.NoError(t, err)
			base, err := nodestore.NewRotatingKVDatabase(store, nodestore.DatabaseConfig{})
			require.NoError(t, err)
			db := &residencyFaultDatabase{Database: base}
			cleanBudget := &preferredMemoryBudget{limit: 16 << 10}
			family := shamapbackend.New(db, shamapbackend.NodeStoreOptions{
				CleanCacheBytes:   16 << 10,
				CleanMemoryBudget: cleanBudget,
			})
			familyClosed := false
			t.Cleanup(func() {
				if !familyClosed {
					require.NoError(t, family.Close())
				}
				require.Zero(t, family.SHAMapCleanResidency().Stats().UsedBytes)
				require.Zero(t, cleanBudget.Used())
			})

			state, err := shamap.NewBacked(shamap.TypeState, family)
			require.NoError(t, err)
			entries := residencyEntries(7, 0, 64)
			for _, entry := range entries {
				require.NoError(t, state.Put(entry.key, entry.value))
			}
			ledger := retentionTestLedger(t, state, 7)
			svc := &Service{
				nodeStore:    db,
				shamapFamily: family,
				config: Config{
					PersistenceMemory: &relayTestMemory{limit: shamap.DirtyWorkspaceBytes},
				},
			}
			before, err := state.RetainedMemoryBytes()
			require.NoError(t, err)
			require.NotZero(t, before)

			failure := errors.New("injected " + tc.name + " failure")
			tc.set(db, failure)
			err = svc.persistToNodeStore(ctx, ledger, ledger.Sequence())
			require.ErrorIs(t, err, failure)
			failed, err := state.RetainedMemoryBytes()
			require.NoError(t, err)
			require.GreaterOrEqual(t, failed, before,
				"failed %s persistence detached clean descendants before durability", tc.name)
			require.NotZero(t, failed)
			item, found, err := state.GetContext(ctx, entries[0].key)
			require.NoError(t, err)
			require.True(t, found)
			require.Equal(t, entries[0].value, item.Data())

			db.storeErr = nil
			db.syncErr = nil
			require.NoError(t, svc.persistToNodeStore(ctx, ledger, ledger.Sequence()))
			after, err := state.RetainedMemoryBytes()
			require.NoError(t, err)
			require.Less(t, after, failed,
				"successful %s retry did not release the persisted map's clean descendants", tc.name)

			root, err := state.Hash()
			require.NoError(t, err)
			require.NoError(t, family.Close())
			familyClosed = true
			require.Zero(t, cleanBudget.Used())

			reopenedStore, err := kvpebble.NewRotating(path, kvpebble.Options{BlockCacheBytes: 8 << 20, MaxOpenFiles: 64})
			require.NoError(t, err)
			reopenedBase, err := nodestore.NewRotatingKVDatabase(reopenedStore, nodestore.DatabaseConfig{})
			require.NoError(t, err)
			reopenedDB := &residencyFaultDatabase{Database: reopenedBase}
			reopenedBudget := &preferredMemoryBudget{limit: 16 << 10}
			reopenedFamily := shamapbackend.New(reopenedDB, shamapbackend.NodeStoreOptions{
				CleanCacheBytes:   16 << 10,
				CleanMemoryBudget: reopenedBudget,
			})
			t.Cleanup(func() {
				require.NoError(t, reopenedFamily.Close())
				require.Zero(t, reopenedFamily.SHAMapCleanResidency().Stats().UsedBytes)
				require.Zero(t, reopenedBudget.Used())
			})
			cold, err := shamap.NewFromRootHashContext(ctx, shamap.TypeState, root, reopenedFamily)
			require.NoError(t, err)
			for _, entry := range entries {
				item, found, err := cold.GetContext(ctx, entry.key)
				require.NoError(t, err)
				require.True(t, found)
				require.Equal(t, entry.value, item.Data())
			}
		})
	}
}

func TestService_PersistenceReleaseRejectsReplacedFamily(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "nodes")
	store, err := kvpebble.NewRotating(path, kvpebble.Options{BlockCacheBytes: 8 << 20, MaxOpenFiles: 64})
	require.NoError(t, err)
	base, err := nodestore.NewRotatingKVDatabase(store, nodestore.DatabaseConfig{})
	require.NoError(t, err)
	db := &residencyFaultDatabase{Database: base}
	family := shamapbackend.New(db, shamapbackend.NodeStoreOptions{CleanCacheBytes: 16 << 10})
	replaced := shamapbackend.New(db, shamapbackend.NodeStoreOptions{CleanCacheBytes: 16 << 10})
	t.Cleanup(func() { require.NoError(t, family.Close()) })

	state, err := shamap.NewBacked(shamap.TypeState, family)
	require.NoError(t, err)
	entries := residencyEntries(8, 0, 64)
	for _, entry := range entries {
		require.NoError(t, state.Put(entry.key, entry.value))
	}
	ledger := retentionTestLedger(t, state, 8)
	svc := &Service{
		nodeStore:    db,
		shamapFamily: replaced,
		config:       Config{PersistenceMemory: &relayTestMemory{limit: shamap.DirtyWorkspaceBytes}},
	}
	require.NoError(t, svc.persistToNodeStore(ctx, ledger, ledger.Sequence()))
	retained, err := state.RetainedMemoryBytes()
	require.NoError(t, err)
	require.NotZero(t, retained, "a replaced family must not release the map it did not bind")

	svc.shamapFamily = family
	require.NoError(t, svc.persistToNodeStore(ctx, ledger, ledger.Sequence()))
	released, err := state.RetainedMemoryBytes()
	require.NoError(t, err)
	require.Less(t, released, retained)
}

var _ nodestore.Database = (*residencyFaultDatabase)(nil)
