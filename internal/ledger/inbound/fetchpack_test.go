package inbound

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/LeJamon/go-xrpl/shamap/backend"
)

// buildSourceStateMap builds a multi-level state tree and returns it together
// with its root hash and serialized root.
func buildSourceStateMap(t *testing.T) (*shamap.SHAMap, [32]byte, []byte) {
	t.Helper()
	source := shamap.New(shamap.TypeState)
	for branch := byte(0); branch < 4; branch++ {
		for sub := byte(0); sub < 4; sub++ {
			for i := byte(0); i < 4; i++ {
				var key [32]byte
				key[0] = (branch << 4) | sub
				key[1] = i << 4
				key[31] = 0xA5
				if err := source.Put(key, []byte{branch, sub, i, 0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88, 0x99}); err != nil {
					t.Fatalf("put: %v", err)
				}
			}
		}
	}
	rootHash, err := source.Hash()
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	rootData, err := source.SerializeRoot()
	if err != nil {
		t.Fatalf("serialize root: %v", err)
	}
	return source, rootHash, rootData
}

// TestCheckLocal_CompletesStateFromCache drives an acquisition to WantState
// with only the header + state root, then completes it purely from a local
// node source (the fetch-pack cache analogue) via CheckLocal.
func TestCheckLocal_CompletesStateFromCache(t *testing.T) {
	t.Parallel()
	source, rootHash, rootData := buildSourceStateMap(t)

	packNodes, err := source.WalkFetchPackNodes(1 << 20)
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	cache := make(map[[32]byte][]byte, len(packNodes))
	for _, n := range packNodes {
		cache[n.Hash] = n.Data
	}
	fetch := func(h [32]byte) ([]byte, bool) { d, ok := cache[h]; return d, ok }

	hdr := header.LedgerHeader{LedgerIndex: 321, AccountHash: rootHash}
	hdrBytes, ledgerHash := encodeHeader(hdr)
	il := New(ledgerHash, 321, 7, discardLogger())
	if err := il.GotBase([]message.LedgerNode{{NodeData: hdrBytes}, {NodeData: rootData}}); err != nil {
		t.Fatalf("GotBase: %v", err)
	}
	if il.State() == StateComplete {
		t.Fatal("setup: tree completed from root alone; need a multi-level tree")
	}

	if !il.CheckLocal(fetch) {
		t.Fatal("CheckLocal reported no progress")
	}
	if !il.IsComplete() {
		t.Fatalf("acquisition not complete after CheckLocal: state=%d", il.State())
	}
	gotHash, err := il.stateMap.Hash()
	if err != nil {
		t.Fatalf("dest hash: %v", err)
	}
	if gotHash != rootHash {
		t.Errorf("reconstructed state hash mismatch: want %x got %x", rootHash[:8], gotHash[:8])
	}
}

func TestCheckLocal_PersistsRecoveredNodesWithoutDirtyingTree(t *testing.T) {
	source, rootHash, rootData := buildSourceStateMap(t)
	packNodes, err := source.WalkFetchPackNodes(1 << 20)
	if err != nil {
		t.Fatal(err)
	}
	cache := make(map[[32]byte][]byte, len(packNodes))
	for _, node := range packNodes {
		cache[node.Hash] = node.Data
	}

	family := backend.NewMemory()
	hdr := header.LedgerHeader{LedgerIndex: 323, AccountHash: rootHash}
	hdrBytes, ledgerHash := encodeHeader(hdr)
	il := New(ledgerHash, 323, 7, discardLogger(), WithFamily(family))
	if err := il.GotBase([]message.LedgerNode{{NodeData: hdrBytes}, {NodeData: rootData}}); err != nil {
		t.Fatal(err)
	}
	if !il.CheckLocal(func(hash [32]byte) ([]byte, bool) {
		data, ok := cache[hash]
		return data, ok
	}) {
		t.Fatal("CheckLocal reported no progress")
	}
	if !il.IsComplete() {
		t.Fatal("CheckLocal did not complete the acquisition")
	}
	for _, node := range packNodes {
		stored, fetchErr := family.Fetch(context.Background(), node.Hash)
		if fetchErr != nil {
			t.Fatal(fetchErr)
		}
		if stored == nil {
			t.Fatalf("locally recovered node %x was not persisted", node.Hash[:8])
		}
	}
	called := false
	if err := il.stateMap.StoreDirty(func([]shamap.FlushEntry) error {
		called = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("verified sync nodes were left dirty after incremental persistence")
	}
}

func TestCheckLocal_PersistsProgressBeforeTraversalYield(t *testing.T) {
	source, rootHash, rootData := buildSourceStateMap(t)
	packNodes, err := source.WalkFetchPackNodes(1 << 20)
	if err != nil {
		t.Fatal(err)
	}
	cache := make(map[[32]byte][]byte, len(packNodes))
	for _, node := range packNodes {
		cache[node.Hash] = node.Data
	}

	family := backend.NewMemory()
	hdr := header.LedgerHeader{LedgerIndex: 324, AccountHash: rootHash}
	hdrBytes, ledgerHash := encodeHeader(hdr)
	il := New(ledgerHash, 324, 7, discardLogger(), WithFamily(family))
	if err := il.GotBase([]message.LedgerNode{{NodeData: hdrBytes}, {NodeData: rootData}}); err != nil {
		t.Fatal(err)
	}
	clock := time.Now()
	il.RearmTimer(clock)

	progressed, complete, err := il.CheckLocalContext(shamap.WithTraversalBudget(t.Context(), 4), func(hash [32]byte) ([]byte, bool) {
		data, ok := cache[hash]
		return data, ok
	})
	if !errors.Is(err, shamap.ErrTraversalBudget) {
		t.Fatalf("CheckLocalContext error = %v, want %v", err, shamap.ErrTraversalBudget)
	}
	if !progressed || complete {
		t.Fatalf("CheckLocalContext = progressed %t, complete %t; want true, false", progressed, complete)
	}

	stored := 0
	for _, node := range packNodes {
		data, fetchErr := family.Fetch(t.Context(), node.Hash)
		if fetchErr != nil {
			t.Fatal(fetchErr)
		}
		if data != nil {
			stored++
		}
	}
	if stored == 0 {
		t.Fatal("traversal yield discarded locally recovered nodes")
	}
	if got := il.OnTimer(clock.Add(4 * time.Second)); got != TimerRefresh {
		t.Fatalf("timer result after yielded progress = %v, want %v", got, TimerRefresh)
	}
	if got := il.Timeouts(); got != 0 {
		t.Fatalf("timeouts after yielded progress = %d, want 0", got)
	}
}

// TestCheckLocal_NoSourceNoProgress confirms an empty source leaves the
// acquisition incomplete and reports no progress (no false completion).
func TestCheckLocal_NoSourceNoProgress(t *testing.T) {
	t.Parallel()
	_, rootHash, rootData := buildSourceStateMap(t)

	hdr := header.LedgerHeader{LedgerIndex: 322, AccountHash: rootHash}
	hdrBytes, ledgerHash := encodeHeader(hdr)
	il := New(ledgerHash, 322, 7, discardLogger())
	if err := il.GotBase([]message.LedgerNode{{NodeData: hdrBytes}, {NodeData: rootData}}); err != nil {
		t.Fatalf("GotBase: %v", err)
	}

	empty := func([32]byte) ([]byte, bool) { return nil, false }
	if il.CheckLocal(empty) {
		t.Error("CheckLocal reported progress from an empty source")
	}
	if il.IsComplete() {
		t.Error("acquisition completed with no nodes supplied")
	}
	if il.CheckLocal(nil) {
		t.Error("CheckLocal with a nil fetch reported progress")
	}
}

// TestFetchPackRequested_OneShot pins the one-shot escalation flag so the
// router escalates a stalled acquisition to a fetch-pack at most once.
func TestFetchPackRequested_OneShot(t *testing.T) {
	t.Parallel()
	il := New([32]byte{1}, 5, 7, discardLogger())
	if il.FetchPackRequested() {
		t.Fatal("a fresh acquisition reports fetch-pack already requested")
	}
	il.MarkFetchPackRequested()
	if !il.FetchPackRequested() {
		t.Fatal("MarkFetchPackRequested was not recorded")
	}
}

// TestTrackerActive_ReturnsInFlight covers the Active iterator used by the
// router to run CheckLocal on every live acquisition.
func TestTrackerActive_ReturnsInFlight(t *testing.T) {
	t.Parallel()
	tr := NewTracker()
	if len(tr.Active()) != 0 {
		t.Fatal("empty tracker should have no active acquisitions")
	}
	il := New([32]byte{9}, 5, 7, discardLogger())
	tr.Track(il)
	active := tr.Active()
	if len(active) != 1 || active[0] != il {
		t.Fatalf("Active did not return the tracked acquisition (got %d)", len(active))
	}
	tr.Remove(il.Hash(), false)
	if len(tr.Active()) != 0 {
		t.Fatal("removed acquisition is still reported active")
	}
}
