package inbound

import (
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/LeJamon/goXRPLd/internal/ledger/header"
	"github.com/LeJamon/goXRPLd/internal/peermanagement/message"
	"github.com/LeJamon/goXRPLd/shamap"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// buildSourceState returns a multi-level state SHAMap plus its root hash,
// serialized root, and wire nodes — enough to drive a real acquisition.
func buildSourceState(t *testing.T) (rootHash [32]byte, rootData []byte, wire []message.LedgerNode) {
	t.Helper()
	source, err := shamap.New(shamap.TypeState)
	if err != nil {
		t.Fatalf("new source map: %v", err)
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
	rootHash, err = source.Hash()
	if err != nil {
		t.Fatalf("source hash: %v", err)
	}
	rootData, err = source.SerializeRoot()
	if err != nil {
		t.Fatalf("serialize root: %v", err)
	}
	wireNodes, err := source.WalkWireNodes()
	if err != nil {
		t.Fatalf("walk wire nodes: %v", err)
	}
	wire = make([]message.LedgerNode, 0, len(wireNodes))
	for _, w := range wireNodes {
		wire = append(wire, message.LedgerNode{NodeID: w.NodeID, NodeData: w.Data})
	}
	return rootHash, rootData, wire
}

// newAcquiring returns a Ledger that has received its header + state root and
// is mid-acquisition (StateWantState), with missing state nodes outstanding.
func newAcquiring(t *testing.T, seq uint32, hash [32]byte) *Ledger {
	t.Helper()
	rootHash, rootData, _ := buildSourceState(t)
	hdrBytes := header.AddRaw(header.LedgerHeader{LedgerIndex: seq, AccountHash: rootHash}, false)
	il := New(hash, seq, 7, discardLogger())
	if err := il.GotBase([]message.LedgerNode{{NodeData: hdrBytes}, {NodeData: rootData}}); err != nil {
		t.Fatalf("GotBase: %v", err)
	}
	if il.State() != StateWantState {
		t.Fatalf("state = %d, want StateWantState", il.State())
	}
	return il
}

func TestTracker_ActiveAcquisitionSnapshot(t *testing.T) {
	t.Parallel()
	var hash [32]byte
	hash[0] = 0xAB
	il := newAcquiring(t, 200, hash)

	tr := NewTracker()
	tr.Track(il)

	info := tr.Info()
	entry, ok := info["200"].(map[string]any)
	if !ok {
		t.Fatalf("expected entry keyed by seq %q, got %#v", "200", info)
	}
	if entry["have_header"] != true {
		t.Errorf("have_header = %v, want true", entry["have_header"])
	}
	if entry["have_state"] != false {
		t.Errorf("have_state = %v, want false", entry["have_state"])
	}
	if entry["peers"] != 1 {
		t.Errorf("peers = %v, want 1", entry["peers"])
	}
	needed, ok := entry["needed_state_hashes"].([]any)
	if !ok || len(needed) == 0 {
		t.Errorf("needed_state_hashes = %#v, want non-empty array", entry["needed_state_hashes"])
	}
	if entry["timeouts"] != 0 {
		t.Errorf("timeouts = %v, want 0", entry["timeouts"])
	}
}

func TestTracker_CompletedReportedThenSwept(t *testing.T) {
	t.Parallel()
	var hash [32]byte
	hash[0] = 0xCD
	rootHash, rootData, wire := buildSourceState(t)
	hdrBytes := header.AddRaw(header.LedgerHeader{LedgerIndex: 300, AccountHash: rootHash}, false)
	il := New(hash, 300, 9, discardLogger())
	if err := il.GotBase([]message.LedgerNode{{NodeData: hdrBytes}, {NodeData: rootData}}); err != nil {
		t.Fatalf("GotBase: %v", err)
	}

	tr := NewTracker()
	tr.Track(il)

	if err := il.GotStateNodes(wire); err != nil {
		t.Fatalf("GotStateNodes: %v", err)
	}
	if !il.IsComplete() {
		t.Fatalf("acquisition not complete")
	}

	// rippled keeps a completed acquisition in mLedgers until sweep, so
	// fetch_info reports complete:true for a short window.
	entry, ok := tr.Info()["300"].(map[string]any)
	if !ok {
		t.Fatalf("completed acquisition should be reported, got %#v", tr.Info())
	}
	if entry["complete"] != true {
		t.Errorf("complete = %v, want true", entry["complete"])
	}
	if entry["have_state"] != true {
		t.Errorf("have_state = %v, want true", entry["have_state"])
	}
	if _, hasPeers := entry["peers"]; hasPeers {
		t.Errorf("completed entry must not report peers, got %#v", entry)
	}

	// Once the retention window elapses it is dropped.
	tr.mu.Lock()
	rec := tr.completed[hash]
	rec.at = time.Now().Add(-2 * completedRetention)
	tr.completed[hash] = rec
	tr.mu.Unlock()
	if info := tr.Info(); len(info) != 0 {
		t.Errorf("completed acquisition should be swept after retention, got %#v", info)
	}
}

func TestTracker_LiveAcquisitionOverwritesSameSeqFailure(t *testing.T) {
	t.Parallel()
	var failHash, liveHash [32]byte
	failHash[0] = 0x33
	liveHash[0] = 0x44

	// A prior attempt at seq 600 (failHash) failed and is remembered.
	failed := New(failHash, 600, 3, discardLogger())
	if err := failed.GotBase([]message.LedgerNode{{NodeData: []byte{0x00}}}); err == nil {
		t.Fatal("expected GotBase to fail")
	}
	// A fresh attempt at the same seq (liveHash) is now in flight.
	live := newAcquiring(t, 600, liveHash)

	tr := NewTracker()
	tr.Track(failed)
	tr.Track(live)

	entry, ok := tr.Info()["600"].(map[string]any)
	if !ok {
		t.Fatalf("seq 600 should be present, got %#v", tr.Info())
	}
	if entry["failed"] == true {
		t.Errorf("live re-acquisition must win over a stale same-seq failure, got %#v", entry)
	}
	if entry["have_header"] != true {
		t.Errorf("expected the live acquisition entry, got %#v", entry)
	}
}

func TestTracker_FailedReportedThenCleared(t *testing.T) {
	t.Parallel()
	var hash [32]byte
	hash[0] = 0xEF
	il := New(hash, 400, 3, discardLogger())
	// Too few nodes drives the acquisition to StateFailed.
	if err := il.GotBase([]message.LedgerNode{{NodeData: []byte{0x00}}}); err == nil {
		t.Fatal("expected GotBase to fail with a single node")
	}
	if il.State() != StateFailed {
		t.Fatalf("state = %d, want StateFailed", il.State())
	}

	tr := NewTracker()
	tr.Track(il)

	entry, ok := tr.Info()["400"].(map[string]any)
	if !ok || entry["failed"] != true {
		t.Fatalf("expected {failed:true} for failed acquisition, got %#v", tr.Info())
	}

	tr.Clear()
	if info := tr.Info(); len(info) != 0 {
		t.Errorf("Clear should empty the tracker, got %#v", info)
	}
}

func TestTracker_TimedOutDemotedToFailure(t *testing.T) {
	t.Parallel()
	var hash [32]byte
	hash[0] = 0x11
	il := New(hash, 500, 4, discardLogger())
	il.created = time.Now().Add(-2 * acquisitionTimeout) // white-box: force timeout

	tr := NewTracker()
	tr.Track(il)

	entry, ok := tr.Info()["500"].(map[string]any)
	if !ok || entry["failed"] != true {
		t.Fatalf("timed-out acquisition should report {failed:true}, got %#v", tr.Info())
	}
	// It must have moved out of the active set into the failure history.
	tr.mu.Lock()
	_, stillActive := tr.active[hash]
	tr.mu.Unlock()
	if stillActive {
		t.Error("timed-out acquisition should be removed from the active set")
	}
}

func TestTracker_GenesisKeyedByHash(t *testing.T) {
	t.Parallel()
	var hash [32]byte
	hash[0] = 0x22
	il := New(hash, 1, 5, discardLogger())
	if err := il.GotBase([]message.LedgerNode{{NodeData: []byte{0x00}}}); err == nil {
		t.Fatal("expected GotBase to fail")
	}

	tr := NewTracker()
	tr.Track(il)

	wantKey := acquisitionKey(1, hash)
	if _, ok := tr.Info()[wantKey].(map[string]any); !ok {
		t.Fatalf("seq<=1 should be keyed by hash %q, got %#v", wantKey, tr.Info())
	}
}

func TestTracker_NilSafe(t *testing.T) {
	t.Parallel()
	var tr *Tracker
	tr.Track(New([32]byte{}, 1, 0, discardLogger())) // must not panic
	tr.Clear()
	if info := tr.Info(); len(info) != 0 {
		t.Errorf("nil tracker Info should be empty, got %#v", info)
	}
}
