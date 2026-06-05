package inbound

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/shamap"
)

// buildSourceStateMap builds a multi-level state tree and returns it together
// with its root hash and serialized root.
func buildSourceStateMap(t *testing.T) (*shamap.SHAMap, [32]byte, []byte) {
	t.Helper()
	source, err := shamap.New(shamap.TypeState)
	if err != nil {
		t.Fatalf("new source: %v", err)
	}
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

// TestFetchPackRequested_OneShot pins the one-shot escalation flag and its
// deadline extension.
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
	// Extending the deadline must clear a timed-out state at the moment of marking.
	if il.IsTimedOut() {
		t.Error("acquisition still reports timed-out immediately after deadline extension")
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
