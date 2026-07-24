package inbound

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/stretchr/testify/require"
)

type failingStoreFamily struct {
	base shamap.Family
	err  error
}

type blockingPersistenceFamily struct {
	shamap.Family
	store func([]shamap.FlushEntry) error
}

type checkpointFamily struct {
	shamap.Family
	flushes int
	err     error
}

func (f blockingPersistenceFamily) StoreBatch(_ context.Context, entries []shamap.FlushEntry) error {
	return f.store(entries)
}

func (f *checkpointFamily) Flush(context.Context) error {
	f.flushes++
	return f.err
}

func (f failingStoreFamily) Fetch(ctx context.Context, hash [32]byte) ([]byte, error) {
	return f.base.Fetch(ctx, hash)
}

func (f failingStoreFamily) StoreBatch(context.Context, []shamap.FlushEntry) error {
	return f.err
}

// buildBackedTestState builds a multi-level state SHAMap from a fixed base set
// plus `extra` items placed under an otherwise-unused top branch. Two trees
// built with different `extra` share byte-identical base subtrees (only the
// added branch and the root re-hash), modeling a fork: canonical and divergent
// ledgers share the bulk of state, differing only in the touched accounts.
func buildBackedTestState(t *testing.T, extra int) (sm *shamap.SHAMap, rootHash [32]byte, rootData []byte) {
	t.Helper()
	sm = shamap.New(shamap.TypeState)
	put := func(k0, k1, k2 byte) {
		var key [32]byte
		key[0] = k0
		key[1] = k1
		key[2] = k2
		key[31] = 0xA5
		if err := sm.Put(key, []byte{k0, k1, k2, 0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88, 0x99}); err != nil {
			t.Fatalf("put: %v", err)
		}
	}
	// Base set shared by every tree: four top branches, multi-level.
	for a := range byte(4) {
		for b := range byte(4) {
			for c := range byte(4) {
				put((a<<4)|b, c<<4, 0)
			}
		}
	}
	// Fork delta: brand-new subtree under top branch 0xE, untouched by the base.
	for i := range extra {
		put(0xE0, byte(i)<<4, 0)
	}

	var err error
	if rootHash, err = sm.Hash(); err != nil {
		t.Fatalf("hash: %v", err)
	}
	if rootData, err = sm.SerializeRoot(); err != nil {
		t.Fatalf("serialize root: %v", err)
	}
	return sm, rootHash, rootData
}

// seedFamilyFrom stores every node of sm into a fresh in-memory node-store
// family, modeling the nodes a node already holds in its local store.
func seedFamilyFrom(t *testing.T, sm *shamap.SHAMap) *shamap.NodeStoreFamily {
	t.Helper()
	family := shamap.NewMemoryNodeStoreFamily()
	nodes, err := sm.WalkFetchPackNodes(1 << 20)
	if err != nil {
		t.Fatalf("walk fetch-pack nodes: %v", err)
	}
	entries := make([]shamap.FlushEntry, 0, len(nodes))
	for _, n := range nodes {
		entries = append(entries, shamap.FlushEntry{Hash: n.Hash, Data: n.Data})
	}
	if err := family.StoreBatch(context.Background(), entries); err != nil {
		t.Fatalf("store batch: %v", err)
	}
	return family
}

func toLedgerNodes(wire []shamap.WireNode) []message.LedgerNode {
	out := make([]message.LedgerNode, 0, len(wire))
	for _, w := range wire {
		out = append(out, message.LedgerNode{NodeID: w.NodeID, NodeData: w.Data})
	}
	return out
}

func TestCheckpointPersistenceUsesBoundedUsefulNodeBatches(t *testing.T) {
	family := &checkpointFamily{Family: shamap.NewMemoryNodeStoreFamily()}
	ledger := New([32]byte{1}, 1, 1, discardLogger(), WithFamily(family))

	require.NoError(t, ledger.CheckpointPersistence(t.Context(), persistenceCheckpointNodes-1))
	require.Equal(t, 0, family.flushes)
	require.NoError(t, ledger.CheckpointPersistence(t.Context(), 1))
	require.Equal(t, 1, family.flushes)

	wantErr := errors.New("checkpoint failed")
	family.err = wantErr
	require.ErrorIs(t, ledger.CheckpointPersistence(t.Context(), persistenceCheckpointNodes), wantErr)
	require.Equal(t, 2, family.flushes)
}

// TestGotBase_BackedCompletesEntirelyFromStore proves a node-store-backed
// acquisition self-heals with zero peer fetches when the whole canonical tree
// is already in the local store — rippled's tryDB (InboundLedger.cpp:340).
func TestGotBase_BackedCompletesEntirelyFromStore(t *testing.T) {
	t.Parallel()
	source, rootHash, rootData := buildBackedTestState(t, 0)
	family := seedFamilyFrom(t, source)

	hdrBytes, ledgerHash := encodeHeader(header.LedgerHeader{LedgerIndex: 100, AccountHash: rootHash})
	il := New(ledgerHash, 100, 7, discardLogger(), WithFamily(family))
	if err := il.GotBase([]message.LedgerNode{{NodeData: hdrBytes}, {NodeData: rootData}}); err != nil {
		t.Fatalf("GotBase: %v", err)
	}

	if !il.IsComplete() {
		t.Fatalf("backed acquisition with the whole tree in the store must complete in GotBase; state=%d", il.State())
	}
	if ids := il.NeedsMissingNodeIDs(); ids != nil {
		t.Fatalf("expected no missing nodes to request, got %d", len(ids))
	}
}

// TestGotBase_ColdStoreMatchesUnbacked proves no regression for forward
// catch-up of a brand-new ledger: a backed acquisition over an empty store
// reports the same missing set as an unbacked one, so it still fetches the tree
// over the wire when the store can't help.
func TestGotBase_ColdStoreMatchesUnbacked(t *testing.T) {
	t.Parallel()
	_, rootHash, rootData := buildBackedTestState(t, 0)
	hdrBytes, ledgerHash := encodeHeader(header.LedgerHeader{LedgerIndex: 100, AccountHash: rootHash})
	base := []message.LedgerNode{{NodeData: hdrBytes}, {NodeData: rootData}}

	unbacked := New(ledgerHash, 100, 7, discardLogger())
	if err := unbacked.GotBase(base); err != nil {
		t.Fatalf("unbacked GotBase: %v", err)
	}
	backedCold := New(ledgerHash, 100, 7, discardLogger(), WithFamily(shamap.NewMemoryNodeStoreFamily()))
	if err := backedCold.GotBase(base); err != nil {
		t.Fatalf("backed-cold GotBase: %v", err)
	}

	want := len(unbacked.stateMap.GetMissingNodes(0, nil))
	got := len(backedCold.stateMap.GetMissingNodes(0, nil))
	if want == 0 {
		t.Fatal("test setup: unbacked map should have missing nodes after GotBase")
	}
	if got != want {
		t.Fatalf("backed map over an empty store must report the same missing set as unbacked: got %d want %d", got, want)
	}
}

// TestGotBase_BackedFetchesOnlyForkDelta is the core fork self-heal case: the
// shared pre-fork state is in the local store; the canonical post-fork tree adds
// a touched subtree. A backed acquisition only needs the fork delta from peers
// (far fewer nodes than the whole tree), and completes once the peer supplies it.
func TestGotBase_BackedFetchesOnlyForkDelta(t *testing.T) {
	t.Parallel()
	// Pre-fork (shared) state already in our local store.
	shared, _, _ := buildBackedTestState(t, 0)
	family := seedFamilyFrom(t, shared)

	// Canonical post-fork state: shared base plus a touched (new) subtree.
	canonical, canonRoot, canonRootData := buildBackedTestState(t, 2)
	hdrBytes, ledgerHash := encodeHeader(header.LedgerHeader{LedgerIndex: 101, AccountHash: canonRoot})
	base := []message.LedgerNode{{NodeData: hdrBytes}, {NodeData: canonRootData}}

	il := New(ledgerHash, 101, 7, discardLogger(), WithFamily(family))
	if err := il.GotBase(base); err != nil {
		t.Fatalf("GotBase: %v", err)
	}

	backedMissing := len(il.stateMap.GetMissingNodes(0, nil))
	if backedMissing == 0 {
		t.Fatal("expected a non-empty fork delta to fetch from peers")
	}

	unbacked := New(ledgerHash, 101, 7, discardLogger())
	if err := unbacked.GotBase(base); err != nil {
		t.Fatalf("unbacked GotBase: %v", err)
	}
	unbackedMissing := len(unbacked.stateMap.GetMissingNodes(0, nil))
	if backedMissing >= unbackedMissing {
		t.Fatalf("backed acquisition must request fewer nodes than unbacked: backed=%d unbacked=%d", backedMissing, unbackedMissing)
	}

	// The peer supplies the canonical tree; store-resident nodes are duplicates,
	// the fork delta attaches, and the acquisition completes.
	wire, err := canonical.WalkWireNodes()
	if err != nil {
		t.Fatalf("walk wire nodes: %v", err)
	}
	if err := il.GotStateNodes(toLedgerNodes(wire)); err != nil {
		t.Fatalf("GotStateNodes: %v", err)
	}
	il.CollectMissingRequest(false)
	if !il.IsComplete() {
		t.Fatalf("acquisition should complete after the peer supplies the fork delta; state=%d", il.State())
	}
	called := false
	if err := il.stateMap.StoreDirty(func([]shamap.FlushEntry) error {
		called = true
		return nil
	}); err != nil {
		t.Fatalf("check acquired delta dirtiness: %v", err)
	}
	if called {
		t.Fatal("incrementally persisted fork delta remained dirty")
	}
	canonicalNodes, err := canonical.WalkFetchPackNodes(1 << 20)
	if err != nil {
		t.Fatalf("walk canonical nodes: %v", err)
	}
	for _, node := range canonicalNodes {
		stored, fetchErr := family.Fetch(t.Context(), node.Hash)
		if fetchErr != nil {
			t.Fatalf("fetch canonical node %x: %v", node.Hash[:8], fetchErr)
		}
		if stored == nil {
			t.Fatalf("canonical node %x was not persisted", node.Hash[:8])
		}
	}
}

func TestGotBase_StoreCompleteTreePersistsOnlyReceivedRoot(t *testing.T) {
	source, rootHash, rootData := buildBackedTestState(t, 0)
	family := shamap.NewMemoryNodeStoreFamily()
	nodes, err := source.WalkFetchPackNodes(1 << 20)
	if err != nil {
		t.Fatalf("walk fetch-pack nodes: %v", err)
	}
	entries := make([]shamap.FlushEntry, 0, len(nodes)-1)
	for _, node := range nodes {
		if node.Hash != rootHash {
			entries = append(entries, shamap.FlushEntry{Hash: node.Hash, Data: node.Data, LedgerSeq: 99, MapType: shamap.TypeState})
		}
	}
	if err := family.StoreBatch(context.Background(), entries); err != nil {
		t.Fatalf("seed descendants: %v", err)
	}

	hdrBytes, ledgerHash := encodeHeader(header.LedgerHeader{LedgerIndex: 100, AccountHash: rootHash})
	il := New(ledgerHash, 100, 7, discardLogger(), WithFamily(family))
	if err := il.GotBase([]message.LedgerNode{{NodeData: hdrBytes}, {NodeData: rootData}}); err != nil {
		t.Fatalf("GotBase: %v", err)
	}
	if !il.IsComplete() {
		t.Fatalf("descendant-backed acquisition did not complete; state=%d", il.State())
	}

	storedRoot, err := family.Fetch(t.Context(), rootHash)
	if err != nil {
		t.Fatalf("fetch received root: %v", err)
	}
	if storedRoot == nil {
		t.Fatalf("received root %x was not persisted", rootHash[:8])
	}
	called := false
	if err := il.stateMap.StoreDirty(func([]shamap.FlushEntry) error {
		called = true
		return nil
	}); err != nil {
		t.Fatalf("check received root dirtiness: %v", err)
	}
	if called {
		t.Fatal("incrementally persisted root remained dirty")
	}
}

func TestCheckLocal_RetainedStoreCompletesAfterBase(t *testing.T) {
	source, rootHash, rootData := buildBackedTestState(t, 0)
	family := shamap.NewMemoryNodeStoreFamily()
	headerData, ledgerHash := encodeHeader(header.LedgerHeader{LedgerIndex: 100, AccountHash: rootHash})
	acquisition := New(ledgerHash, 100, 7, discardLogger(), WithFamily(family))
	require.NoError(t, acquisition.GotBase([]message.LedgerNode{{NodeData: headerData}, {NodeData: rootData}}))
	require.Equal(t, StateWantState, acquisition.State())

	nodes, err := source.WalkFetchPackNodes(1 << 20)
	require.NoError(t, err)
	entries := make([]shamap.FlushEntry, 0, len(nodes))
	for _, node := range nodes {
		if node.Hash != rootHash {
			entries = append(entries, shamap.FlushEntry{Hash: node.Hash, Data: node.Data})
		}
	}
	require.NoError(t, family.StoreBatch(t.Context(), entries))

	progressed, complete, err := acquisition.CheckLocalContext(t.Context(), func([32]byte) ([]byte, bool) {
		return nil, false
	})
	require.NoError(t, err)
	require.True(t, progressed, "nodes materialized from the retained store are acquisition progress")
	require.True(t, complete, "a tree completed by the retained store must be finalized")
	require.Equal(t, StateComplete, acquisition.State())
}

func TestCheckLocal_PartialRetainedStoreResetsNoProgressInterval(t *testing.T) {
	source, rootHash, rootData := buildBackedTestState(t, 0)
	family := shamap.NewMemoryNodeStoreFamily()
	headerData, ledgerHash := encodeHeader(header.LedgerHeader{LedgerIndex: 100, AccountHash: rootHash})
	acquisition := New(ledgerHash, 100, 7, discardLogger(), WithFamily(family))
	require.NoError(t, acquisition.GotBase([]message.LedgerNode{{NodeData: headerData}, {NodeData: rootData}}))

	base := time.Now()
	require.Equal(t, TimerRefresh, acquisition.OnTimer(base.Add(time.Hour)), "consume base progress")
	wire, err := source.WalkWireNodes()
	require.NoError(t, err)
	var retained shamap.FlushEntry
	for _, node := range wire {
		id, parseErr := shamap.ParseNodeID(node.NodeID)
		if parseErr != nil || id.Depth() != 1 {
			continue
		}
		retained, err = shamap.FlushEntryFromWire(node.Data, 100, shamap.TypeState)
		require.NoError(t, err)
		break
	}
	require.NotEqual(t, [32]byte{}, retained.Hash)
	require.NoError(t, family.StoreBatch(t.Context(), []shamap.FlushEntry{retained}))

	progressed, complete, err := acquisition.CheckLocalContext(t.Context(), func([32]byte) ([]byte, bool) {
		return nil, false
	})
	require.NoError(t, err)
	require.True(t, progressed)
	require.False(t, complete)
	require.Equal(t, TimerRefresh, acquisition.OnTimer(base.Add(2*time.Hour)),
		"retained-store hydration must prevent a no-progress strike")
	require.Zero(t, acquisition.Timeouts())
}

func TestGotStateNodes_PartialWorkSurvivesDifferentRootAcquisition(t *testing.T) {
	first, firstRoot, firstRootData := buildBackedTestState(t, 0)
	second, secondRoot, secondRootData := buildBackedTestState(t, 2)
	firstWire, err := first.WalkWireNodes()
	if err != nil {
		t.Fatalf("walk first tree: %v", err)
	}
	secondWire, err := second.WalkWireNodes()
	if err != nil {
		t.Fatalf("walk second tree: %v", err)
	}
	secondHashes := make(map[[32]byte]struct{}, len(secondWire))
	for _, node := range secondWire {
		entry, ferr := shamap.FlushEntryFromWire(node.Data, 101, shamap.TypeState)
		if ferr != nil {
			t.Fatalf("decode second wire node: %v", ferr)
		}
		secondHashes[entry.Hash] = struct{}{}
	}

	var shared shamap.WireNode
	var sharedEntry shamap.FlushEntry
	for _, node := range firstWire {
		id, perr := shamap.ParseNodeID(node.NodeID)
		if perr != nil || id.IsRoot() || id.Depth() != 1 {
			continue
		}
		entry, ferr := shamap.FlushEntryFromWire(node.Data, 100, shamap.TypeState)
		if ferr != nil {
			t.Fatalf("decode first wire node: %v", ferr)
		}
		if _, ok := secondHashes[entry.Hash]; ok {
			shared = node
			sharedEntry = entry
			break
		}
	}
	if len(shared.Data) == 0 {
		t.Fatal("trees have no shared depth-one node")
	}

	family := shamap.NewMemoryNodeStoreFamily()
	firstHeader, firstHash := encodeHeader(header.LedgerHeader{LedgerIndex: 100, AccountHash: firstRoot})
	acquisition := New(firstHash, 100, 7, discardLogger(), WithFamily(family))
	if err := acquisition.GotBase([]message.LedgerNode{{NodeData: firstHeader}, {NodeData: firstRootData}}); err != nil {
		t.Fatalf("GotBase first: %v", err)
	}
	sharedNodes := toLedgerNodes([]shamap.WireNode{shared})
	useful, err := acquisition.GotStateNodesUseful(sharedNodes)
	if err != nil {
		t.Fatalf("GotStateNodes first: %v", err)
	}
	if useful != 1 {
		t.Fatalf("first shared node useful count = %d, want 1", useful)
	}
	useful, err = acquisition.GotStateNodesUseful(sharedNodes)
	if err != nil {
		t.Fatalf("GotStateNodes duplicate: %v", err)
	}
	if useful != 0 {
		t.Fatalf("duplicate shared node useful count = %d, want 0", useful)
	}
	if acquisition.IsComplete() {
		t.Fatal("single shared subtree unexpectedly completed first acquisition")
	}
	stored, err := family.Fetch(context.Background(), sharedEntry.Hash)
	if err != nil || stored == nil {
		t.Fatalf("accepted partial node not persisted: data=%d err=%v", len(stored), err)
	}

	secondHeader, secondHash := encodeHeader(header.LedgerHeader{LedgerIndex: 101, AccountHash: secondRoot})
	replacement := New(secondHash, 101, 8, discardLogger(), WithFamily(family))
	if err := replacement.GotBase([]message.LedgerNode{{NodeData: secondHeader}, {NodeData: secondRootData}}); err != nil {
		t.Fatalf("GotBase replacement: %v", err)
	}
	for _, missing := range replacement.stateMap.GetMissingNodes(0, nil) {
		if missing.Hash == sharedEntry.Hash {
			t.Fatalf("replacement re-requested persisted shared node %x", sharedEntry.Hash[:8])
		}
	}
}

func TestGotBase_StoreFailureDoesNotFailAcquisition(t *testing.T) {
	source, rootHash, rootData := buildBackedTestState(t, 0)
	wire, err := source.WalkWireNodes()
	if err != nil {
		t.Fatalf("walk wire nodes: %v", err)
	}
	family := failingStoreFamily{
		base: shamap.NewMemoryNodeStoreFamily(),
		err:  errors.New("store unavailable"),
	}
	headerData, ledgerHash := encodeHeader(header.LedgerHeader{LedgerIndex: 100, AccountHash: rootHash})
	acquisition := New(ledgerHash, 100, 7, discardLogger(), WithFamily(family))
	if err := acquisition.GotBase([]message.LedgerNode{{NodeData: headerData}, {NodeData: rootData}}); err != nil {
		t.Fatalf("GotBase with store failure: %v", err)
	}
	if err := acquisition.GotStateNodes(toLedgerNodes(wire[1:2])); err != nil {
		t.Fatalf("GotStateNodes with store failure: %v", err)
	}
	if acquisition.State() == StateFailed {
		t.Fatal("local store failure must not fail or blame the peer acquisition")
	}
}

func TestGotBase_PersistsStateRootWhileWaitingForTransactionRoot(t *testing.T) {
	_, rootHash, rootData := buildBackedTestState(t, 0)
	family := shamap.NewMemoryNodeStoreFamily()
	headerData, ledgerHash := encodeHeader(header.LedgerHeader{
		LedgerIndex: 100,
		AccountHash: rootHash,
		TxHash:      [32]byte{0xA1},
	})
	acquisition := New(ledgerHash, 100, 7, discardLogger(), WithFamily(family))

	err := acquisition.GotBase([]message.LedgerNode{{NodeData: headerData}, {NodeData: rootData}})
	require.NoError(t, err)
	require.Equal(t, StateWantBase, acquisition.State())

	stored, fetchErr := family.Fetch(context.Background(), rootHash)
	require.NoError(t, fetchErr)
	require.NotNil(t, stored, "verified state root must remain reusable while the transaction root is missing")
}

func TestGotBase_PersistsBeforeReleasingAcquisitionLock(t *testing.T) {
	_, rootHash, rootData := buildBackedTestState(t, 0)
	family := shamap.NewMemoryNodeStoreFamily()
	headerData, ledgerHash := encodeHeader(header.LedgerHeader{LedgerIndex: 100, AccountHash: rootHash})
	entered := make(chan struct{})
	release := make(chan struct{})
	acquisition := New(ledgerHash, 100, 7, discardLogger(),
		WithFamily(blockingPersistenceFamily{
			Family: family,
			store: func(entries []shamap.FlushEntry) error {
				close(entered)
				<-release
				return family.StoreBatch(context.Background(), entries)
			},
		}),
	)
	done := make(chan error, 1)
	go func() {
		done <- acquisition.GotBase([]message.LedgerNode{{NodeData: headerData}, {NodeData: rootData}})
	}()
	<-entered

	stateDone := make(chan State, 1)
	go func() { stateDone <- acquisition.State() }()
	select {
	case <-stateDone:
		t.Fatal("acquisition lock released before persistence admission")
	case <-time.After(25 * time.Millisecond):
	}
	close(release)
	require.NoError(t, <-done)
	select {
	case <-stateDone:
	case <-time.After(time.Second):
		t.Fatal("acquisition lock remained held after persistence admission")
	}
}
