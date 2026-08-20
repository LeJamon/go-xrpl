package adaptor

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/crypto/sha512half"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/ledger/inbound"
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/stretchr/testify/require"
)

type acquisitionStoreTestFamily struct {
	mu         sync.Mutex
	data       map[[32]byte][]byte
	cached     map[[32]byte][]byte
	fetchCalls int
	cacheCalls int
	calls      [][32]byte
	started    chan [32]byte
	blockFirst chan struct{}
	failFirst  bool
	failAll    bool
}

func newAcquisitionStoreTestFamily() *acquisitionStoreTestFamily {
	return &acquisitionStoreTestFamily{
		data:    make(map[[32]byte][]byte),
		cached:  make(map[[32]byte][]byte),
		started: make(chan [32]byte, 16),
	}
}

func (f *acquisitionStoreTestFamily) Fetch(_ context.Context, hash [32]byte) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fetchCalls++
	return bytes.Clone(f.data[hash]), nil
}

func (f *acquisitionStoreTestFamily) FetchCached(_ context.Context, hash [32]byte) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cacheCalls++
	return bytes.Clone(f.cached[hash]), nil
}

func (f *acquisitionStoreTestFamily) clearCached() {
	f.mu.Lock()
	clear(f.cached)
	f.mu.Unlock()
}

func (f *acquisitionStoreTestFamily) StoreBatch(ctx context.Context, entries []shamap.FlushEntry) error {
	f.mu.Lock()
	call := len(f.calls)
	if len(entries) > 0 {
		f.calls = append(f.calls, entries[0].Hash)
	}
	f.mu.Unlock()
	if len(entries) > 0 {
		f.started <- entries[0].Hash
	}
	if call == 0 && f.blockFirst != nil {
		select {
		case <-f.blockFirst:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if call == 0 && f.failFirst {
		return errors.New("store failed")
	}
	f.mu.Lock()
	failAll := f.failAll
	f.mu.Unlock()
	if failAll {
		return errors.New("store failed")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range entries {
		f.data[entries[i].Hash] = bytes.Clone(entries[i].Data)
		f.cached[entries[i].Hash] = bytes.Clone(entries[i].Data)
	}
	return nil
}

func acquisitionEntry(id byte) shamap.FlushEntry {
	return shamap.FlushEntry{
		Hash:      [32]byte{id},
		Data:      []byte{id, id + 1},
		LedgerSeq: uint32(id),
		MapType:   shamap.TypeState,
	}
}

func TestAcquisitionStoreScopeDurableReadsDoNotExhaust(t *testing.T) {
	base := newAcquisitionStoreTestFamily()
	const reads = 16*1024 + 1
	entries := make([]shamap.FlushEntry, 0, reads)
	for i := range reads {
		hash := [32]byte{byte(i), byte(i >> 8), byte(i >> 16)}
		entries = append(entries, shamap.FlushEntry{Hash: hash, Data: []byte{byte(i)}})
	}
	require.NoError(t, base.StoreBatch(t.Context(), entries))
	base.clearCached()

	lane := newAcquisitionStoreLane(base, slog.Default(), 1)
	scope := lane.scope().(*acquisitionStoreScope)

	for i := range reads {
		data, err := scope.FetchDurable(t.Context(), entries[i].Hash)
		require.NoError(t, err)
		require.Equal(t, entries[i].Data, data)
	}

	base.mu.Lock()
	fetchCalls := base.fetchCalls
	base.mu.Unlock()
	require.Equal(t, reads, fetchCalls)
}

func TestAcquisitionStoreScopeCheckpointPreservesDurableReads(t *testing.T) {
	base := newAcquisitionStoreTestFamily()
	first := acquisitionEntry(1)
	second := acquisitionEntry(2)
	require.NoError(t, base.StoreBatch(t.Context(), []shamap.FlushEntry{first, second}))
	base.clearCached()

	lane := newAcquisitionStoreLane(base, slog.Default(), 1)
	lane.start(context.Background())
	defer lane.stopDrain()

	scope := lane.scope().(*acquisitionStoreScope)
	data, err := scope.FetchDurable(t.Context(), first.Hash)
	require.NoError(t, err)
	require.Equal(t, first.Data, data)
	data, err = scope.FetchDurable(t.Context(), second.Hash)
	require.NoError(t, err)
	require.Equal(t, second.Data, data)

	checkpoint := acquisitionEntry(3)
	require.NoError(t, scope.StoreBatch(t.Context(), []shamap.FlushEntry{checkpoint}))
	require.NoError(t, scope.Flush(t.Context()))

	base.clearCached()
	data, err = scope.FetchDurable(t.Context(), second.Hash)
	require.NoError(t, err)
	require.Equal(t, second.Data, data)
}

func TestAcquisitionStoreScopeRepeatedCheckpointsPreserveDurableReads(t *testing.T) {
	base := newAcquisitionStoreTestFamily()
	first := acquisitionEntry(1)
	second := acquisitionEntry(2)
	third := acquisitionEntry(3)
	require.NoError(t, base.StoreBatch(t.Context(), []shamap.FlushEntry{first, second, third}))
	base.clearCached()

	lane := newAcquisitionStoreLane(base, slog.Default(), 1)
	lane.start(context.Background())
	defer lane.stopDrain()

	scope := lane.scope().(*acquisitionStoreScope)
	data, err := scope.FetchDurable(t.Context(), first.Hash)
	require.NoError(t, err)
	require.Equal(t, first.Data, data)

	require.NoError(t, scope.StoreBatch(t.Context(), []shamap.FlushEntry{acquisitionEntry(4)}))
	require.NoError(t, scope.Flush(t.Context()))

	base.clearCached()
	data, err = scope.FetchDurable(t.Context(), second.Hash)
	require.NoError(t, err)
	require.Equal(t, second.Data, data)
	data, err = scope.FetchDurable(t.Context(), third.Hash)
	require.NoError(t, err)
	require.Equal(t, third.Data, data)
}

func TestAcquisitionStoreScopeFailedCheckpointKeepsDurableReads(t *testing.T) {
	base := newAcquisitionStoreTestFamily()
	persisted := acquisitionEntry(1)
	require.NoError(t, base.StoreBatch(t.Context(), []shamap.FlushEntry{persisted}))
	base.clearCached()
	base.failAll = true

	lane := newAcquisitionStoreLane(base, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), 1)
	lane.start(context.Background())
	defer lane.stopDrain()

	scope := lane.scope().(*acquisitionStoreScope)
	data, err := scope.FetchDurable(t.Context(), persisted.Hash)
	require.NoError(t, err)
	require.Equal(t, persisted.Data, data)

	require.NoError(t, scope.StoreBatch(t.Context(), []shamap.FlushEntry{acquisitionEntry(2)}))
	require.EqualError(t, scope.Flush(t.Context()), "store failed")

	base.clearCached()
	data, err = scope.FetchDurable(t.Context(), persisted.Hash)
	require.NoError(t, err)
	require.Equal(t, persisted.Data, data)
}

func TestAcquisitionStoreScopeCanceledCheckpointRetainsPersistenceFailure(t *testing.T) {
	base := newAcquisitionStoreTestFamily()
	base.blockFirst = make(chan struct{})
	base.failFirst = true
	lane := newAcquisitionStoreLane(base, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), 1)
	lane.start(context.Background())
	defer lane.stopDrain()

	scope := lane.scope().(*acquisitionStoreScope)
	_, err := scope.FetchDurable(t.Context(), [32]byte{0xff})
	require.NoError(t, err)

	require.NoError(t, scope.StoreBatch(t.Context(), []shamap.FlushEntry{acquisitionEntry(3)}))
	require.Equal(t, [32]byte{3}, <-base.started)

	ctx, cancel := context.WithCancel(t.Context())
	checkpointDone := make(chan error, 1)
	go func() {
		checkpointDone <- scope.Flush(ctx)
	}()
	cancel()
	require.ErrorIs(t, <-checkpointDone, context.Canceled)

	close(base.blockFirst)
	require.EqualError(t, scope.Flush(t.Context()), "store failed")

	require.NoError(t, scope.Retire(t.Context()))
	require.NoError(t, scope.Flush(t.Context()))
}

func TestAcquisitionStoreScopeCheckpointReprovesFromDurableStore(t *testing.T) {
	base := newAcquisitionStoreTestFamily()
	lane := newAcquisitionStoreLane(base, slog.Default(), 1)
	lane.start(context.Background())
	defer lane.stopDrain()

	scope := lane.scope().(*acquisitionStoreScope)
	_, err := scope.FetchDurable(t.Context(), [32]byte{0xff})
	require.NoError(t, err)

	rootHash, rootData, wire := buildSelfHealSourceState(t)
	dest, err := shamap.NewBacked(shamap.TypeState, scope)
	require.NoError(t, err)
	require.NoError(t, dest.AddRootNode(rootHash, rootData))
	require.NotEmpty(t, dest.GetMissingNodes(1024, nil))

	entries := make([]shamap.FlushEntry, 0, len(wire))
	for _, node := range wire {
		nodeID, parseErr := shamap.ParseNodeID(node.NodeID)
		require.NoError(t, parseErr)
		if nodeID.IsRoot() {
			continue
		}
		result, entry, addErr := dest.AddKnownNodeByIDWithEntryContext(t.Context(), nodeID, node.NodeData)
		require.NoError(t, addErr)
		if result == shamap.NodeUseful {
			entries = append(entries, entry)
		}
	}
	require.NotEmpty(t, entries)
	require.Empty(t, dest.GetMissingNodes(1024, nil))

	require.NoError(t, scope.StoreBatch(t.Context(), entries))
	require.NoError(t, scope.Flush(t.Context()))
	require.NoError(t, dest.AcknowledgePersistedContext(t.Context()))

	base.mu.Lock()
	storedBeforeReload := len(base.data)
	fetchesBeforeReload := base.fetchCalls
	base.mu.Unlock()

	base.clearCached()
	require.Empty(t, dest.GetMissingNodes(1024, nil))

	scope.FullBelowCache().Bump()
	require.Empty(t, dest.GetMissingNodes(1024, nil))

	base.mu.Lock()
	require.Equal(t, storedBeforeReload, len(base.data))
	require.Greater(t, base.fetchCalls, fetchesBeforeReload)
	base.mu.Unlock()

	for _, node := range wire {
		nodeID, parseErr := shamap.ParseNodeID(node.NodeID)
		require.NoError(t, parseErr)
		if nodeID.IsRoot() {
			continue
		}
		_, _, addErr := dest.AddKnownNodeByIDWithEntryContext(t.Context(), nodeID, node.NodeData)
		require.NoError(t, addErr)
	}
	require.Empty(t, dest.GetMissingNodes(1024, nil))
}

func TestAcquisitionStoreScopeNodePlacementLoadsDurableAncestors(t *testing.T) {
	base := newAcquisitionStoreTestFamily()
	rootHash, rootData, wire := buildSelfHealSourceState(t)

	entries := make([]shamap.FlushEntry, 0, len(wire))
	var target message.LedgerNode
	var targetID shamap.NodeID
	for i := range wire {
		entry, err := shamap.FlushEntryFromWire(wire[i].NodeData, 88, shamap.TypeState)
		require.NoError(t, err)
		entries = append(entries, entry)

		nodeID, err := shamap.ParseNodeID(wire[i].NodeID)
		require.NoError(t, err)
		if targetID.Depth() < 2 && nodeID.Depth() >= 2 {
			target = wire[i]
			targetID = nodeID
		}
	}
	require.GreaterOrEqual(t, targetID.Depth(), uint8(2))
	require.NoError(t, base.StoreBatch(t.Context(), entries))
	base.clearCached()

	lane := newAcquisitionStoreLane(base, slog.Default(), 1)
	scope := lane.scope().(*acquisitionStoreScope)

	dest, err := shamap.NewBacked(shamap.TypeState, scope)
	require.NoError(t, err)
	require.NoError(t, dest.AddRootNode(rootHash, rootData))

	base.mu.Lock()
	fetchesBeforePlacement := base.fetchCalls
	base.mu.Unlock()
	result, entry, err := dest.AddKnownNodeByIDWithEntryContext(t.Context(), targetID, target.NodeData)
	require.NoError(t, err)
	require.Equal(t, shamap.NodeUseful, result)
	require.NotEqual(t, [32]byte{}, entry.Hash)

	base.mu.Lock()
	require.Greater(t, base.fetchCalls, fetchesBeforePlacement)
	base.mu.Unlock()
}

func TestAcquisitionStoreScopeNodePlacementSeesOnlySameScopePendingAncestors(t *testing.T) {
	base := newAcquisitionStoreTestFamily()
	base.blockFirst = make(chan struct{})
	lane := newAcquisitionStoreLane(base, slog.Default(), 1)
	lane.start(context.Background())
	defer lane.stopDrain()

	rootHash, rootData, wire := buildSelfHealSourceState(t)
	var target message.LedgerNode
	var targetID shamap.NodeID
	for i := range wire {
		nodeID, err := shamap.ParseNodeID(wire[i].NodeID)
		require.NoError(t, err)
		if targetID.Depth() < 2 && nodeID.Depth() >= 2 {
			target = wire[i]
			targetID = nodeID
		}
	}
	require.GreaterOrEqual(t, targetID.Depth(), uint8(2))

	entries := make([]shamap.FlushEntry, 0, len(wire)-1)
	for i := range wire {
		if bytes.Equal(wire[i].NodeID, target.NodeID) {
			continue
		}
		entry, err := shamap.FlushEntryFromWire(wire[i].NodeData, 88, shamap.TypeState)
		require.NoError(t, err)
		entries = append(entries, entry)
	}

	owner := lane.scope().(*acquisitionStoreScope)
	require.NoError(t, owner.StoreBatch(t.Context(), entries))
	require.Equal(t, entries[0].Hash, <-base.started)

	ownerMap, err := shamap.NewBacked(shamap.TypeState, owner)
	require.NoError(t, err)
	require.NoError(t, ownerMap.AddRootNode(rootHash, rootData))
	result, _, err := ownerMap.AddKnownNodeByIDWithEntryContext(t.Context(), targetID, target.NodeData)
	require.NoError(t, err)
	require.Equal(t, shamap.NodeUseful, result)

	other := lane.scope().(*acquisitionStoreScope)
	otherMap, err := shamap.NewBacked(shamap.TypeState, other)
	require.NoError(t, err)
	require.NoError(t, otherMap.AddRootNode(rootHash, rootData))
	result, _, err = otherMap.AddKnownNodeByIDWithEntryContext(t.Context(), targetID, target.NodeData)
	require.NoError(t, err)
	require.Equal(t, shamap.NodeReRequest, result)

	close(base.blockFirst)
	require.NoError(t, owner.Flush(t.Context()))
}

func TestAcquisitionStoreScopeSharesFreshCachedNodes(t *testing.T) {
	base := newAcquisitionStoreTestFamily()
	lane := newAcquisitionStoreLane(base, slog.Default(), 1)
	lane.start(context.Background())
	defer lane.stopDrain()

	consumer := lane.scope().(*acquisitionStoreScope)
	missing := [32]byte{0xff}
	data, err := consumer.FetchDurable(t.Context(), missing)
	require.NoError(t, err)
	require.Nil(t, data)

	producer := lane.scope().(*acquisitionStoreScope)
	entry := acquisitionEntry(8)
	require.NoError(t, producer.StoreBatch(t.Context(), []shamap.FlushEntry{entry}))
	require.NoError(t, producer.Flush(t.Context()))

	base.mu.Lock()
	beforeDurable := base.fetchCalls
	base.mu.Unlock()
	data, err = consumer.FetchDurable(t.Context(), entry.Hash)
	require.NoError(t, err)
	require.Equal(t, entry.Data, data)
	base.mu.Lock()
	require.Equal(t, beforeDurable, base.fetchCalls)
	base.mu.Unlock()
}

func TestAcquisitionStoreScopeKeepsPendingWritesReadable(t *testing.T) {
	base := newAcquisitionStoreTestFamily()
	base.blockFirst = make(chan struct{})
	lane := newAcquisitionStoreLane(base, slog.Default(), 1)
	lane.start(context.Background())
	defer lane.stopDrain()

	scope := lane.scope().(*acquisitionStoreScope)
	missing := [32]byte{0xff}
	data, err := scope.FetchDurable(t.Context(), missing)
	require.NoError(t, err)
	require.Nil(t, data)

	entry := acquisitionEntry(7)
	require.NoError(t, scope.StoreBatch(t.Context(), []shamap.FlushEntry{entry}))
	require.Equal(t, entry.Hash, <-base.started)
	data, err = scope.Fetch(t.Context(), entry.Hash)
	require.NoError(t, err)
	require.Equal(t, entry.Data, data)

	close(base.blockFirst)
	require.NoError(t, scope.Flush(t.Context()))
	stored, err := base.Fetch(t.Context(), entry.Hash)
	require.NoError(t, err)
	require.Equal(t, entry.Data, stored)
}

func TestAcquisitionStoreScopePromotionKeepsDurableReads(t *testing.T) {
	base := newAcquisitionStoreTestFamily()
	first := acquisitionEntry(5)
	second := acquisitionEntry(6)
	require.NoError(t, base.StoreBatch(t.Context(), []shamap.FlushEntry{first, second}))
	base.clearCached()

	lane := newAcquisitionStoreLane(base, slog.Default(), 1)
	scope := lane.scope().(*acquisitionStoreScope)
	data, err := scope.FetchDurable(t.Context(), first.Hash)
	require.NoError(t, err)
	require.Equal(t, first.Data, data)
	data, err = scope.FetchDurable(t.Context(), second.Hash)
	require.NoError(t, err)
	require.Equal(t, second.Data, data)

	require.NoError(t, scope.Promote(t.Context()))
	data, err = scope.FetchDurable(t.Context(), second.Hash)
	require.NoError(t, err)
	require.Equal(t, second.Data, data)
}

func TestRouterAcquisitionOptionsUseScopedDurableStore(t *testing.T) {
	base := newAcquisitionStoreTestFamily()
	rootHash, rootData, wire := buildSelfHealSourceState(t)
	entries := make([]shamap.FlushEntry, 0, len(wire))
	for i := range wire {
		entry, err := shamap.FlushEntryFromWire(wire[i].NodeData, 88, shamap.TypeState)
		require.NoError(t, err)
		entries = append(entries, entry)
	}
	require.NoError(t, base.StoreBatch(t.Context(), entries))
	base.clearCached()

	router := newTestRouter(nil, nil, nil)
	router.SetAcquisitionFamily(base)
	headerData := header.AddRaw(header.LedgerHeader{LedgerIndex: 88, AccountHash: rootHash}, false)
	ledgerHash := sha512half.Sum(protocol.HashPrefixLedgerMaster().Bytes(), headerData)

	acquired := inbound.New(ledgerHash, 88, 7, serveTestLogger(), router.acquisitionOpts()...)
	require.NoError(t, acquired.GotBase([]message.LedgerNode{{NodeData: headerData}, {NodeData: rootData}}))
	for !acquired.IsComplete() {
		base.mu.Lock()
		before := base.fetchCalls
		base.mu.Unlock()
		state, tx, complete, err := acquired.CollectMissingRequestContext(
			shamap.WithTraversalBudget(t.Context(), 1), false)
		base.mu.Lock()
		passFetches := base.fetchCalls - before
		base.mu.Unlock()
		require.LessOrEqual(t, passFetches, 1)
		if errors.Is(err, shamap.ErrTraversalBudget) {
			require.Empty(t, state)
			require.Empty(t, tx)
			continue
		}
		require.NoError(t, err)
		require.True(t, complete)
		require.Empty(t, state)
		require.Empty(t, tx)
	}
	base.mu.Lock()
	fetches := base.fetchCalls
	base.mu.Unlock()
	require.Greater(t, fetches, 1)
}

func TestAcquisitionStoreLaneSynchronousWhenStopped(t *testing.T) {
	base := newAcquisitionStoreTestFamily()
	lane := newAcquisitionStoreLane(base, slog.Default(), 1)
	entry := acquisitionEntry(1)

	require.NoError(t, lane.StoreBatch(t.Context(), []shamap.FlushEntry{entry}))
	require.Equal(t, entry.Hash, <-base.started)
	stored, err := lane.Fetch(t.Context(), entry.Hash)
	require.NoError(t, err)
	require.Equal(t, entry.Data, stored)
	require.NoError(t, lane.flush(t.Context()))
}

func TestAcquisitionStoreLanePendingReadThroughAndBarrier(t *testing.T) {
	base := newAcquisitionStoreTestFamily()
	base.blockFirst = make(chan struct{})
	lane := newAcquisitionStoreLane(base, slog.Default(), 1)
	lane.start(context.Background())
	entry := acquisitionEntry(2)

	require.NoError(t, lane.StoreBatch(t.Context(), []shamap.FlushEntry{entry}))
	require.Equal(t, entry.Hash, <-base.started)
	baseData, err := base.Fetch(t.Context(), entry.Hash)
	require.NoError(t, err)
	require.Nil(t, baseData)
	pending, err := lane.Fetch(t.Context(), entry.Hash)
	require.NoError(t, err)
	require.Equal(t, entry.Data, pending)
	durable, err := lane.FetchDurable(t.Context(), entry.Hash)
	require.NoError(t, err)
	require.Nil(t, durable)

	barrierDone := make(chan error, 1)
	go func() { barrierDone <- lane.flush(t.Context()) }()
	select {
	case err := <-barrierDone:
		t.Fatalf("barrier returned before prior write completed: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	close(base.blockFirst)
	require.NoError(t, <-barrierDone)

	stored, err := base.Fetch(t.Context(), entry.Hash)
	require.NoError(t, err)
	require.Equal(t, entry.Data, stored)
	durable, err = lane.FetchDurable(t.Context(), entry.Hash)
	require.NoError(t, err)
	require.Equal(t, entry.Data, durable)
	lane.stopDrain()
}

func TestAcquisitionStoreLaneBoundedFIFOBackpressure(t *testing.T) {
	base := newAcquisitionStoreTestFamily()
	base.blockFirst = make(chan struct{})
	lane := newAcquisitionStoreLane(base, slog.Default(), 1)
	lane.start(context.Background())

	first := acquisitionEntry(1)
	second := acquisitionEntry(2)
	third := acquisitionEntry(3)
	require.NoError(t, lane.StoreBatch(t.Context(), []shamap.FlushEntry{first}))
	require.Equal(t, first.Hash, <-base.started)
	require.NoError(t, lane.StoreBatch(t.Context(), []shamap.FlushEntry{second}))

	thirdDone := make(chan error, 1)
	go func() { thirdDone <- lane.StoreBatch(t.Context(), []shamap.FlushEntry{third}) }()
	select {
	case err := <-thirdDone:
		t.Fatalf("third enqueue bypassed the bounded queue: %v", err)
	case <-time.After(25 * time.Millisecond):
	}

	close(base.blockFirst)
	require.NoError(t, <-thirdDone)
	require.NoError(t, lane.flush(t.Context()))
	lane.stopDrain()

	base.mu.Lock()
	calls := append([][32]byte(nil), base.calls...)
	base.mu.Unlock()
	require.Equal(t, [][32]byte{first.Hash, second.Hash, third.Hash}, calls)
}

func TestAcquisitionStoreLaneFailureDoesNotStopFIFO(t *testing.T) {
	base := newAcquisitionStoreTestFamily()
	base.failFirst = true
	lane := newAcquisitionStoreLane(base, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), 2)
	lane.start(context.Background())
	first := acquisitionEntry(1)
	second := acquisitionEntry(2)

	require.NoError(t, lane.StoreBatch(t.Context(), []shamap.FlushEntry{first}))
	require.NoError(t, lane.StoreBatch(t.Context(), []shamap.FlushEntry{second}))
	require.EqualError(t, lane.flush(t.Context()), "store failed")
	lane.stopDrain()

	firstData, err := lane.Fetch(t.Context(), first.Hash)
	require.NoError(t, err)
	require.Nil(t, firstData)
	secondData, err := lane.Fetch(t.Context(), second.Hash)
	require.NoError(t, err)
	require.Equal(t, second.Data, secondData)
}

func TestAcquisitionStoreLaneAttributesFailuresToAcquisition(t *testing.T) {
	base := newAcquisitionStoreTestFamily()
	base.failFirst = true
	lane := newAcquisitionStoreLane(base, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), 2)
	lane.start(context.Background())
	defer lane.stopDrain()

	first := acquisitionEntry(1)
	second := acquisitionEntry(2)
	firstScope := lane.scope().(*acquisitionStoreScope)
	secondScope := lane.scope().(*acquisitionStoreScope)
	require.NoError(t, firstScope.StoreBatch(t.Context(), []shamap.FlushEntry{first}))
	require.NoError(t, secondScope.StoreBatch(t.Context(), []shamap.FlushEntry{second}))

	require.NoError(t, secondScope.Flush(t.Context()))
	require.EqualError(t, firstScope.Flush(t.Context()), "store failed")

	retryScope := lane.scope().(*acquisitionStoreScope)
	require.NoError(t, retryScope.StoreBatch(t.Context(), []shamap.FlushEntry{first}))
	require.NoError(t, retryScope.Flush(t.Context()))
}

func TestAcquisitionStoreScopePendingVisibilityIsPrivate(t *testing.T) {
	base := newAcquisitionStoreTestFamily()
	base.blockFirst = make(chan struct{})
	base.failFirst = true
	lane := newAcquisitionStoreLane(base, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), 2)
	lane.start(context.Background())
	defer lane.stopDrain()

	entry := acquisitionEntry(7)
	producer := lane.scope().(*acquisitionStoreScope)
	consumer := lane.scope().(*acquisitionStoreScope)
	require.NoError(t, producer.StoreBatch(t.Context(), []shamap.FlushEntry{entry}))
	require.Equal(t, entry.Hash, <-base.started)

	producerData, err := producer.Fetch(t.Context(), entry.Hash)
	require.NoError(t, err)
	require.Equal(t, entry.Data, producerData)
	consumerData, err := consumer.Fetch(t.Context(), entry.Hash)
	require.NoError(t, err)
	require.Nil(t, consumerData)

	close(base.blockFirst)
	require.EqualError(t, producer.Flush(t.Context()), "store failed")
	require.NoError(t, consumer.Flush(t.Context()))
	consumerData, err = consumer.Fetch(t.Context(), entry.Hash)
	require.NoError(t, err)
	require.Nil(t, consumerData)
}

func TestBackedAcquisitionDoesNotCompleteFromAnotherScopesFailedPendingWrite(t *testing.T) {
	source := shamap.New(shamap.TypeState)
	for i := byte(1); i <= 8; i++ {
		var key [32]byte
		key[0] = i << 4
		key[31] = i
		require.NoError(t, source.Put(key, []byte{i, i + 1, i + 2, i + 3, 5, 6, 7, 8, 9, 10, 11, 12}))
	}
	rootHash, err := source.Hash()
	require.NoError(t, err)
	rootData, err := source.SerializeRoot()
	require.NoError(t, err)
	var entries []shamap.FlushEntry
	require.NoError(t, source.StoreDirty(func(batch []shamap.FlushEntry) error {
		entries = append(entries, batch...)
		return nil
	}))
	require.NotEmpty(t, entries)

	var target shamap.FlushEntry
	for _, entry := range entries {
		if entry.Hash != rootHash {
			target = entry
			break
		}
	}
	require.NotEqual(t, [32]byte{}, target.Hash)

	base := newAcquisitionStoreTestFamily()
	base.mu.Lock()
	for _, entry := range entries {
		if entry.Hash != target.Hash {
			base.data[entry.Hash] = bytes.Clone(entry.Data)
		}
	}
	base.mu.Unlock()
	base.blockFirst = make(chan struct{})
	base.failFirst = true
	lane := newAcquisitionStoreLane(base, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), 2)
	lane.start(context.Background())
	defer lane.stopDrain()

	producer := lane.scope().(*acquisitionStoreScope)
	consumer := lane.scope().(*acquisitionStoreScope)
	require.NoError(t, producer.StoreBatch(t.Context(), []shamap.FlushEntry{target}))
	require.Equal(t, target.Hash, <-base.started)

	dest, err := shamap.NewBacked(shamap.TypeState, consumer)
	require.NoError(t, err)
	require.NoError(t, dest.AddRootNode(rootHash, rootData))
	missing, err := dest.GetMissingNodesContext(t.Context(), 1024, nil)
	require.NoError(t, err)
	require.Contains(t, missingHashesForTest(missing), target.Hash)

	close(base.blockFirst)
	require.EqualError(t, producer.Flush(t.Context()), "store failed")
	missing, err = dest.GetMissingNodesContext(t.Context(), 1024, nil)
	require.NoError(t, err)
	require.Contains(t, missingHashesForTest(missing), target.Hash)
}

func missingHashesForTest(missing []shamap.MissingNode) [][32]byte {
	hashes := make([][32]byte, len(missing))
	for i := range missing {
		hashes[i] = missing[i].Hash
	}
	return hashes
}

func TestAcquisitionStoreScopeSharesNodeAfterDurableWrite(t *testing.T) {
	base := newAcquisitionStoreTestFamily()
	base.blockFirst = make(chan struct{})
	lane := newAcquisitionStoreLane(base, slog.Default(), 2)
	lane.start(context.Background())
	defer lane.stopDrain()

	entry := acquisitionEntry(8)
	producer := lane.scope().(*acquisitionStoreScope)
	consumer := lane.scope().(*acquisitionStoreScope)
	require.NoError(t, producer.StoreBatch(t.Context(), []shamap.FlushEntry{entry}))
	require.Equal(t, entry.Hash, <-base.started)

	consumerData, err := consumer.Fetch(t.Context(), entry.Hash)
	require.NoError(t, err)
	require.Nil(t, consumerData)

	close(base.blockFirst)
	require.NoError(t, producer.Flush(t.Context()))
	consumerData, err = consumer.Fetch(t.Context(), entry.Hash)
	require.NoError(t, err)
	require.Equal(t, entry.Data, consumerData)
}

func TestAcquisitionStoreScopeRetireConsumesAbandonedFailure(t *testing.T) {
	base := newAcquisitionStoreTestFamily()
	base.failFirst = true
	lane := newAcquisitionStoreLane(base, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), 1)
	lane.start(context.Background())
	defer lane.stopDrain()

	scope := lane.scope().(*acquisitionStoreScope)
	require.NoError(t, scope.StoreBatch(t.Context(), []shamap.FlushEntry{acquisitionEntry(9)}))
	require.NoError(t, scope.Retire(t.Context()))
	require.NoError(t, scope.Flush(t.Context()))
	require.NoError(t, scope.Retire(t.Context()))
}

func TestAcquisitionStoreScopePromoteWaitsForAdmittedWrite(t *testing.T) {
	base := newAcquisitionStoreTestFamily()
	base.blockFirst = make(chan struct{})
	lane := newAcquisitionStoreLane(base, slog.Default(), 0)
	lane.start(context.Background())
	defer lane.stopDrain()

	scope := lane.scope().(*acquisitionStoreScope)
	first := acquisitionEntry(40)
	second := acquisitionEntry(41)
	require.NoError(t, scope.StoreBatch(t.Context(), []shamap.FlushEntry{first}))
	require.Equal(t, first.Hash, <-base.started)

	secondDone := make(chan error, 1)
	go func() {
		secondDone <- scope.StoreBatch(t.Context(), []shamap.FlushEntry{second})
	}()
	require.Eventually(t, func() bool {
		lane.pendingMu.RLock()
		defer lane.pendingMu.RUnlock()
		_, ok := lane.pending[pendingAcquisitionKey{scope: scope.id, hash: second.Hash}]
		return ok
	}, time.Second, time.Millisecond)
	promoteDone := make(chan error, 1)
	go func() {
		promoteDone <- scope.Promote(t.Context())
	}()
	select {
	case err := <-promoteDone:
		t.Fatalf("promotion passed an admitted write: %v", err)
	case <-time.After(25 * time.Millisecond):
	}

	close(base.blockFirst)
	require.NoError(t, <-secondDone)
	require.Equal(t, second.Hash, <-base.started)
	require.NoError(t, <-promoteDone)

	stored, err := base.Fetch(t.Context(), second.Hash)
	require.NoError(t, err)
	require.Equal(t, second.Data, stored)
}

func TestAcquisitionStoreScopeCheckpointWaitsForAdmittedWrite(t *testing.T) {
	base := newAcquisitionStoreTestFamily()
	base.blockFirst = make(chan struct{})
	lane := newAcquisitionStoreLane(base, slog.Default(), 0)
	lane.start(context.Background())
	defer lane.stopDrain()

	scope := lane.scope().(*acquisitionStoreScope)
	first := acquisitionEntry(42)
	second := acquisitionEntry(43)
	require.NoError(t, scope.StoreBatch(t.Context(), []shamap.FlushEntry{first}))
	require.Equal(t, first.Hash, <-base.started)

	secondDone := make(chan error, 1)
	go func() {
		secondDone <- scope.StoreBatch(t.Context(), []shamap.FlushEntry{second})
	}()
	require.Eventually(t, func() bool {
		lane.pendingMu.RLock()
		defer lane.pendingMu.RUnlock()
		_, ok := lane.pending[pendingAcquisitionKey{scope: scope.id, hash: second.Hash}]
		return ok
	}, time.Second, time.Millisecond)

	checkpointDone := make(chan error, 1)
	go func() {
		checkpointDone <- scope.Flush(t.Context())
	}()
	select {
	case err := <-checkpointDone:
		t.Fatalf("checkpoint passed an admitted write: %v", err)
	case <-time.After(25 * time.Millisecond):
	}

	close(base.blockFirst)
	require.NoError(t, <-secondDone)
	require.Equal(t, second.Hash, <-base.started)
	require.NoError(t, <-checkpointDone)

	stored, err := base.Fetch(t.Context(), second.Hash)
	require.NoError(t, err)
	require.Equal(t, second.Data, stored)
}

func TestAcquisitionStoreLaneStopCancelsBlockedWrites(t *testing.T) {
	base := newAcquisitionStoreTestFamily()
	base.blockFirst = make(chan struct{})
	lane := newAcquisitionStoreLane(base, slog.Default(), 1)
	lane.drainTimeout = 25 * time.Millisecond
	lane.start(context.Background())
	first := acquisitionEntry(1)
	second := acquisitionEntry(2)
	require.NoError(t, lane.StoreBatch(t.Context(), []shamap.FlushEntry{first}))
	require.Equal(t, first.Hash, <-base.started)
	require.NoError(t, lane.StoreBatch(t.Context(), []shamap.FlushEntry{second}))

	stopped := make(chan struct{})
	go func() {
		lane.stopDrain()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("stop did not cancel the blocked write")
	}
}

func TestAcquisitionStoreLaneStopDrainsQueuedWrites(t *testing.T) {
	base := newAcquisitionStoreTestFamily()
	base.blockFirst = make(chan struct{})
	lane := newAcquisitionStoreLane(base, slog.Default(), 1)
	lane.start(context.Background())
	first := acquisitionEntry(1)
	second := acquisitionEntry(2)
	require.NoError(t, lane.StoreBatch(t.Context(), []shamap.FlushEntry{first}))
	require.Equal(t, first.Hash, <-base.started)
	require.NoError(t, lane.StoreBatch(t.Context(), []shamap.FlushEntry{second}))

	stopped := make(chan struct{})
	go func() {
		lane.stopDrain()
		close(stopped)
	}()
	select {
	case <-stopped:
		t.Fatal("stop returned before queued persistence completed")
	case <-time.After(25 * time.Millisecond):
	}
	close(base.blockFirst)
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("stop did not finish after draining queued persistence")
	}

	firstData, err := base.Fetch(t.Context(), first.Hash)
	require.NoError(t, err)
	require.Equal(t, first.Data, firstData)
	secondData, err := base.Fetch(t.Context(), second.Hash)
	require.NoError(t, err)
	require.Equal(t, second.Data, secondData)
}

func TestRouterAcquisitionStoreLifecycle(t *testing.T) {
	inbox := make(chan *peermanagement.InboundMessage)
	router := newTestRouter(nil, nil, inbox)
	base := newAcquisitionStoreTestFamily()
	router.SetAcquisitionFamily(base)
	require.Same(t, router.acquisitionStore, router.acquisitionFamily)

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		router.Run(ctx)
		close(done)
	}()
	require.Eventually(t, func() bool {
		router.acquisitionStore.lifecycleMu.RLock()
		defer router.acquisitionStore.lifecycleMu.RUnlock()
		return router.acquisitionStore.done != nil
	}, time.Second, time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("router did not drain acquisition persistence on shutdown")
	}
	router.acquisitionStore.lifecycleMu.RLock()
	running := router.acquisitionStore.done != nil
	router.acquisitionStore.lifecycleMu.RUnlock()
	require.False(t, running)
}

func TestCompleteInboundLedgerDiscardsItsOwnPersistenceFailure(t *testing.T) {
	base := newAcquisitionStoreTestFamily()
	base.failFirst = true
	router := newTestRouter(nil, nil, make(chan *peermanagement.InboundMessage))
	router.SetAcquisitionFamily(base)
	router.acquisitionStore.start(t.Context())
	defer router.acquisitionStore.stopDrain()

	hash := [32]byte{0xc3}
	family := router.acquisitionStore.scope()
	ledger := inbound.New(hash, 44, 7, serveTestLogger(), inbound.WithFamily(family))
	router.fetchTracker.Track(ledger)
	require.NoError(t, family.StoreBatch(t.Context(), []shamap.FlushEntry{acquisitionEntry(3)}))

	router.completeInboundLedger(ledger)
	require.Nil(t, router.fetchTracker.Find(hash))
	require.NoError(t, family.(*acquisitionStoreScope).Flush(t.Context()))
}

func TestCompleteInboundLedgerPromotesResultMapPersistence(t *testing.T) {
	base := newAcquisitionStoreTestFamily()
	router := newTestRouter(nil, newTestAdaptor(t), make(chan *peermanagement.InboundMessage))
	router.SetAcquisitionFamily(base)
	router.acquisitionStore.start(t.Context())
	defer router.acquisitionStore.stopDrain()

	scope := router.acquisitionStore.scope().(*acquisitionStoreScope)
	acquired, ledgerHash := completedScopedAcquisition(t, scope, 45)
	router.fetchTracker.Track(acquired)

	router.completeInboundLedger(acquired)
	require.Nil(t, router.fetchTracker.Find(ledgerHash))
	scope.mu.Lock()
	promoted, retired := scope.promoted, scope.retired
	scope.mu.Unlock()
	require.True(t, promoted)
	require.False(t, retired)

	_, stateMap, _, err := acquired.Result()
	require.NoError(t, err)
	stateRoot, err := stateMap.Hash()
	require.NoError(t, err)
	fullBelow := stateMap.FullBelowCache()
	require.True(t, fullBelow.Has(fullBelow.Generation(), stateRoot), "promoted acquisition must publish its durable root proof")
	base.mu.Lock()
	callsAfterCompletion := len(base.calls)
	base.mu.Unlock()
	_, err = stateMap.SnapshotImmutable()
	require.NoError(t, err)
	mutable, err := stateMap.SnapshotMutable()
	require.NoError(t, err)
	base.mu.Lock()
	callsAfterSnapshots := len(base.calls)
	base.mu.Unlock()
	require.Equal(t, callsAfterCompletion, callsAfterSnapshots, "clean adopted map snapshots must not rewrite acquisition nodes")
	var key [32]byte
	key[0] = 0xf1
	key[31] = 0x5a
	require.NoError(t, mutable.Put(key, []byte{0xf2, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11}))
	next, err := mutable.SnapshotMutable()
	require.NoError(t, err)

	base.mu.Lock()
	base.failAll = true
	base.mu.Unlock()
	key[0] = 0xf2
	require.NoError(t, next.Put(key, []byte{0xf3, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11}))
	_, err = next.SnapshotMutable()
	require.ErrorContains(t, err, "store failed")
}

func TestStandardReplayReloadsPromotedTransactionMap(t *testing.T) {
	base := newAcquisitionStoreTestFamily()
	router := newTestRouter(nil, nil, make(chan *peermanagement.InboundMessage))
	router.SetAcquisitionFamily(base)
	router.acquisitionStore.start(t.Context())
	defer router.acquisitionStore.stopDrain()

	txMap := shamap.New(shamap.TypeTransaction)
	blob, txID := txWithMetaBlob(t, []byte{0x10, 0x20, 0x30, 0x40}, 1)
	require.NoError(t, txMap.PutWithNodeType(txID, blob, shamap.NodeTypeTransactionWithMeta))
	txRoot, err := txMap.Hash()
	require.NoError(t, err)
	batch, err := txMap.FlushDirty()
	require.NoError(t, err)

	scope := router.acquisitionStore.scope().(*acquisitionStoreScope)
	require.NoError(t, scope.StoreBatch(t.Context(), batch.Entries))
	require.NoError(t, scope.Promote(t.Context()))
	base.clearCached()

	entry := &standardReplayEntry{
		header:  header.LedgerHeader{TxHash: txRoot},
		durable: true,
	}
	reloaded, err := router.loadStandardReplayTransactionMap(t.Context(), entry)
	require.NoError(t, err)
	reloadedRoot, err := reloaded.Hash()
	require.NoError(t, err)
	require.Equal(t, txRoot, reloadedRoot)
}

func TestCompleteInboundLedgerReadyReleasesUnconsumedScopes(t *testing.T) {
	t.Run("result error", func(t *testing.T) {
		base := newAcquisitionStoreTestFamily()
		router := newTestRouter(nil, &Adaptor{}, make(chan *peermanagement.InboundMessage))
		router.SetAcquisitionFamily(base)
		router.acquisitionStore.start(t.Context())
		defer router.acquisitionStore.stopDrain()

		scope := router.acquisitionStore.scope().(*acquisitionStoreScope)
		hash := [32]byte{0xe2}
		acquired := inbound.New(hash, 46, 7, serveTestLogger(), inbound.WithFamily(scope))
		router.fetchTracker.Track(acquired)
		router.completeInboundLedgerReady(acquired)
		require.Nil(t, router.fetchTracker.Find(hash))
		require.ErrorContains(t, scope.StoreBatch(t.Context(), []shamap.FlushEntry{acquisitionEntry(46)}), "scope retired")
	})

	t.Run("nil service", func(t *testing.T) {
		base := newAcquisitionStoreTestFamily()
		router := newTestRouter(nil, &Adaptor{}, make(chan *peermanagement.InboundMessage))
		router.SetAcquisitionFamily(base)
		router.acquisitionStore.start(t.Context())
		defer router.acquisitionStore.stopDrain()

		scope := router.acquisitionStore.scope().(*acquisitionStoreScope)
		acquired, hash := completedScopedAcquisition(t, scope, 47)
		router.fetchTracker.Track(acquired)
		router.completeInboundLedger(acquired)
		require.Nil(t, router.fetchTracker.Find(hash))
		require.ErrorContains(t, scope.StoreBatch(t.Context(), []shamap.FlushEntry{acquisitionEntry(47)}), "scope retired")
	})

	t.Run("nil adaptor", func(t *testing.T) {
		base := newAcquisitionStoreTestFamily()
		router := newTestRouter(nil, nil, make(chan *peermanagement.InboundMessage))
		router.SetAcquisitionFamily(base)
		router.acquisitionStore.start(t.Context())
		defer router.acquisitionStore.stopDrain()

		scope := router.acquisitionStore.scope().(*acquisitionStoreScope)
		acquired, hash := completedScopedAcquisition(t, scope, 48)
		router.fetchTracker.Track(acquired)
		router.completeInboundLedger(acquired)
		require.Nil(t, router.fetchTracker.Find(hash))
		require.ErrorContains(t, scope.StoreBatch(t.Context(), []shamap.FlushEntry{acquisitionEntry(48)}), "scope retired")
	})
}

func completedScopedAcquisition(t *testing.T, scope *acquisitionStoreScope, seq uint32) (*inbound.Ledger, [32]byte) {
	t.Helper()
	rootHash, rootData, wire := buildSelfHealSourceState(t)
	hdr := header.LedgerHeader{LedgerIndex: seq, AccountHash: rootHash}
	headerData := header.AddRaw(hdr, false)
	ledgerHash := sha512half.Sum(protocol.HashPrefixLedgerMaster().Bytes(), headerData)
	acquired := inbound.New(ledgerHash, seq, 7, serveTestLogger(), inbound.WithFamily(scope))
	require.NoError(t, acquired.GotBase([]message.LedgerNode{
		{NodeData: headerData},
		{NodeData: rootData},
	}))
	require.NoError(t, acquired.GotStateNodes(wire))
	acquired.CollectMissingRequest(false)
	require.True(t, acquired.IsComplete())
	return acquired, ledgerHash
}

func TestRouterRetiresPersistenceOnAbandonedAcquisitionPaths(t *testing.T) {
	tests := []struct {
		name   string
		finish func(*testing.T, *Router, *inbound.Ledger)
	}{
		{
			name: "clear fetch info",
			finish: func(_ *testing.T, router *Router, _ *inbound.Ledger) {
				router.ClearFetchInfo()
			},
		},
		{
			name: "stale worker result",
			finish: func(t *testing.T, router *Router, ledger *inbound.Ledger) {
				require.True(t, router.fetchTracker.RemoveExpectedWithSnapshot(ledger, ledger.Snapshot(), false))
				router.handleAcquisitionWorkResult(acquisitionWorkResult{ledger: ledger})
			},
		},
		{
			name: "rejected worker result",
			finish: func(_ *testing.T, router *Router, ledger *inbound.Ledger) {
				router.handleAcquisitionWorkResult(acquisitionWorkResult{
					ledger: ledger, remove: true, haveSnapshot: true, snapshot: ledger.Snapshot(),
				})
			},
		},
		{
			name: "terminal timer result",
			finish: func(_ *testing.T, router *Router, ledger *inbound.Ledger) {
				router.handleAcquisitionWorkResult(acquisitionWorkResult{
					ledger: ledger, remove: true, timerFailure: true, snapshot: ledger.Snapshot(),
				})
			},
		},
		{
			name: "completion persistence failure",
			finish: func(_ *testing.T, router *Router, ledger *inbound.Ledger) {
				router.handleAcquisitionWorkResult(acquisitionWorkResult{
					ledger: ledger, complete: true, persistenceErr: errors.New("persistence failed"),
				})
			},
		},
		{
			name: "legacy malformed base",
			finish: func(t *testing.T, router *Router, ledger *inbound.Ledger) {
				require.True(t, router.handleInboundLedgerData(ledger, &message.LedgerData{
					InfoType: message.LedgerInfoBase,
					Nodes:    []message.LedgerNode{{NodeData: []byte{1}}},
				}, 7))
			},
		},
	}

	for i, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			base := newAcquisitionStoreTestFamily()
			base.failFirst = true
			router := newTestRouter(nil, nil, make(chan *peermanagement.InboundMessage))
			router.SetAcquisitionFamily(base)
			router.acquisitionStore.start(t.Context())
			defer router.acquisitionStore.stopDrain()

			scope := router.acquisitionStore.scope().(*acquisitionStoreScope)
			hash := [32]byte{0xd0, byte(i)}
			ledger := inbound.New(hash, uint32(100+i), 7, serveTestLogger(), inbound.WithFamily(scope))
			router.fetchTracker.Track(ledger)
			require.NoError(t, scope.StoreBatch(t.Context(), []shamap.FlushEntry{acquisitionEntry(byte(20 + i))}))

			test.finish(t, router, ledger)
			require.Nil(t, router.fetchTracker.Find(hash))
			require.NoError(t, scope.Flush(t.Context()))
		})
	}
}

func TestClearThenStaleResultRetiresLaterPersistenceFailure(t *testing.T) {
	base := newAcquisitionStoreTestFamily()
	base.blockFirst = make(chan struct{})
	base.failFirst = true
	router := newTestRouter(nil, nil, make(chan *peermanagement.InboundMessage))
	router.SetAcquisitionFamily(base)
	router.acquisitionStore.start(t.Context())
	defer router.acquisitionStore.stopDrain()

	scope := router.acquisitionStore.scope().(*acquisitionStoreScope)
	hash := [32]byte{0xe1}
	ledger := inbound.New(hash, 201, 7, serveTestLogger(), inbound.WithFamily(scope))
	router.fetchTracker.Track(ledger)
	require.NoError(t, scope.StoreBatch(t.Context(), []shamap.FlushEntry{acquisitionEntry(31)}))
	require.Equal(t, [32]byte{31}, <-base.started)

	cleared := make(chan struct{})
	go func() {
		router.ClearFetchInfo()
		close(cleared)
	}()
	select {
	case <-cleared:
	case <-time.After(time.Second):
		t.Fatal("scope retirement blocked behind persistence")
	}

	close(base.blockFirst)
	router.handleAcquisitionWorkResult(acquisitionWorkResult{ledger: ledger})
	require.NoError(t, scope.Flush(t.Context()))
}
