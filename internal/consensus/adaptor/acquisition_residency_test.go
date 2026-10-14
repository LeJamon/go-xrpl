package adaptor

import (
	"crypto/sha256"
	"encoding/binary"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/genesis"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/ledger/inbound"
	"github.com/LeJamon/go-xrpl/internal/ledger/service"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/LeJamon/go-xrpl/shamap/backend"
	"github.com/LeJamon/go-xrpl/storage/kvstore/memorydb"
	"github.com/LeJamon/go-xrpl/storage/nodestore"
	"github.com/stretchr/testify/require"
)

func TestAcquiredLedgerSuccessorsReleaseCleanResidency(t *testing.T) {
	ctx := t.Context()
	const cleanCacheLimit = 16 << 10
	db, err := nodestore.NewKVDatabase(memorydb.New(), nodestore.DatabaseConfig{})
	require.NoError(t, err)
	family := backend.New(db, backend.NodeStoreOptions{CleanCacheBytes: cleanCacheLimit})
	t.Cleanup(func() { require.NoError(t, family.Close()) })
	svc, err := service.New(service.Config{
		Standalone:      true,
		GenesisConfig:   genesis.DefaultConfig(),
		NodeStore:       db,
		SHAMapFamily:    family,
		LedgerCacheSize: 8,
	})
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)
	a, _ := newRecordingAdaptor(t, svc)
	router := newTestRouter(nil, a, nil)
	router.SetAcquisitionFamily(family)
	c := router.catchupReplay
	c.acquisitionStore.start(ctx)
	t.Cleanup(c.acquisitionStore.stopDrain)
	t.Cleanup(func() { router.StopAcquisitions() })
	scope := c.acquisitionStore.scope().(*acquisitionStoreScope)
	closeTime := svc.GetClosedLedger().CloseTime().Add(time.Second)
	source, err := ledger.NewOpen(svc.GetClosedLedger(), closeTime)
	require.NoError(t, err)
	require.NoError(t, source.Close(closeTime, 0))
	sourceState, err := source.StateMapSnapshot()
	require.NoError(t, err)
	rootData, err := sourceState.SerializeRoot()
	require.NoError(t, err)
	wireNodes, err := sourceState.WalkWireNodes()
	require.NoError(t, err)
	wire := make([]message.LedgerNode, 0, len(wireNodes))
	for _, node := range wireNodes {
		wire = append(wire, message.LedgerNode{NodeID: node.NodeID, NodeData: node.Data})
	}
	h := source.Header()
	acquired := inbound.NewGeneric(h.Hash, h.LedgerIndex, 7, serveTestLogger(), inbound.WithFamily(scope))
	require.NoError(t, acquired.GotBase([]message.LedgerNode{
		{NodeData: header.AddRaw(h, false)}, {NodeData: rootData},
	}))
	require.NoError(t, acquired.GotStateNodes(wire))
	require.NoError(t, acquired.CheckpointMemory(ctx))
	acquired.CollectMissingRequest(false)
	require.True(t, acquired.IsComplete())
	c.fetchTracker.Track(acquired)
	c.completeInboundLedger(acquired)
	require.Nil(t, c.fetchTracker.Find(h.Hash))
	stored, err := svc.GetLedgerByHash(h.Hash)
	require.NoError(t, err)
	require.NoError(t, svc.SwitchToPreferredLedger(stored))
	svc.FlushPersists()

	type stateEntry struct {
		key   [32]byte
		value []byte
	}
	type retainedState struct {
		state   *shamap.SHAMap
		root    [32]byte
		entries []stateEntry
	}
	history := make([]retainedState, 0, 32)
	for round := range 32 {
		open := svc.GetOpenLedger()
		var key [32]byte
		var value []byte
		entries := make([]stateEntry, 0, 32)
		for index := range 32 {
			value = make([]byte, 64)
			binary.BigEndian.PutUint32(value, uint32(round))
			binary.BigEndian.PutUint32(value[4:], uint32(index))
			key = sha256.Sum256(value)
			require.NoError(t, open.Insert(keylet.Keylet{Key: key}, value))
			entries = append(entries, stateEntry{key: key, value: value})
		}
		_, err := svc.AcceptLedger(ctx)
		require.NoError(t, err)
		svc.FlushPersists()
		state, err := svc.GetClosedLedger().StateMapSnapshot()
		require.NoError(t, err)
		retained, err := state.RetainedMemoryBytes()
		require.NoError(t, err)
		require.LessOrEqual(t, retained, uint64(4<<10), "close %d retained persisted descendants", round)
		stateRoot, err := state.Hash()
		require.NoError(t, err)
		history = append(history, retainedState{state: state, root: stateRoot, entries: entries})
		current, err := svc.GetOpenLedger().StateMapSnapshot()
		require.NoError(t, err)
		retained, err = current.RetainedMemoryBytes()
		require.NoError(t, err)
		require.LessOrEqual(t, retained, uint64(4<<10), "successor %d retained persisted descendants", round)
		hits := family.SHAMapCleanResidency().Stats().Hits
		for range 2 {
			item, found, err := current.GetContext(ctx, key)
			require.NoError(t, err)
			require.True(t, found)
			require.Equal(t, value, item.Data())
		}
		require.Greater(t, family.SHAMapCleanResidency().Stats().Hits, hits)
		require.LessOrEqual(t, family.SHAMapCleanResidency().Stats().UsedBytes, uint64(cleanCacheLimit))
	}
	for _, record := range history {
		got, err := record.state.Hash()
		require.NoError(t, err)
		require.Equal(t, record.root, got)
		cold, err := shamap.NewFromRootHashContext(ctx, shamap.TypeState, record.root, family)
		require.NoError(t, err)
		for _, state := range []*shamap.SHAMap{record.state, cold} {
			for _, entry := range record.entries {
				item, found, err := state.GetContext(ctx, entry.key)
				require.NoError(t, err)
				require.True(t, found)
				require.Equal(t, entry.value, item.Data())
			}
		}
	}
}

func TestAcquisitionPromotionFailureKeepsScopedMaps(t *testing.T) {
	base := newAcquisitionStoreTestFamily()
	lane := newAcquisitionStoreLane(base, serveTestLogger(), 1)
	lane.start(t.Context())
	t.Cleanup(lane.stopDrain)
	scope := lane.scope().(*acquisitionStoreScope)
	acquired, _ := completedScopedAcquisition(t, scope, 45)
	t.Cleanup(acquired.ReleaseAcquisitionMemory)
	_, state, _, err := acquired.Result()
	require.NoError(t, err)
	wantRoot, err := state.Hash()
	require.NoError(t, err)
	base.mu.Lock()
	base.failAll = true
	base.mu.Unlock()
	require.NoError(t, scope.StoreBatch(t.Context(), []shamap.FlushEntry{acquisitionEntry(91)}))
	require.ErrorContains(t, acquired.PromotePersistence(t.Context()), "store failed")
	snapshot, err := state.SnapshotImmutable()
	require.NoError(t, err)
	token := snapshot.PrepareCleanResidencyRelease()
	require.NotNil(t, token)
	require.Same(t, scope, token.Family())
	gotRoot, err := snapshot.Hash()
	require.NoError(t, err)
	require.Equal(t, wantRoot, gotRoot)
	promoted, err := scope.Promote(t.Context())
	require.ErrorContains(t, err, "store failed")
	require.Nil(t, promoted)
}
