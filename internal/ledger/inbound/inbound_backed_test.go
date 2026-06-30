package inbound

import (
	"context"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/shamap"
)

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
	if !il.IsComplete() {
		t.Fatalf("acquisition should complete after the peer supplies the fork delta; state=%d", il.State())
	}
}
