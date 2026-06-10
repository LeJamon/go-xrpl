package inbound

import (
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/crypto/common"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/LeJamon/go-xrpl/shamap"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// encodeHeader serializes a header for the wire and returns the hash a peer
// answering GetLedger must produce. GotBase recomputes the hash from these exact
// bytes and rejects a mismatch (mirroring rippled's takeHeader), so tests
// driving a real acquisition must request the header's true byte-level hash.
func encodeHeader(h header.LedgerHeader) (data []byte, hash [32]byte) {
	data = header.AddRaw(h, false)
	return data, common.Sha512Half(protocol.HashPrefixLedgerMaster.Bytes(), data)
}

// buildSourceState returns a multi-level state SHAMap plus its root hash,
// serialized root, and wire nodes — enough to drive a real acquisition.
func buildSourceState(t *testing.T) (rootHash [32]byte, rootData []byte, wire []message.LedgerNode) {
	t.Helper()
	return buildSourceMap(t, shamap.TypeState)
}

// buildSourceMap builds a multi-level SHAMap of the given type and returns its
// root hash, serialized root, and wire nodes — enough to drive a real
// state-tree or transaction-tree acquisition.
func buildSourceMap(t *testing.T, mapType shamap.Type) (rootHash [32]byte, rootData []byte, wire []message.LedgerNode) {
	t.Helper()
	source := shamap.New(mapType)
	for branch := range byte(4) {
		for sub := range byte(4) {
			for i := range byte(4) {
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
func newAcquiring(t *testing.T, seq uint32) *Ledger {
	t.Helper()
	rootHash, rootData, _ := buildSourceState(t)
	hdrBytes, hash := encodeHeader(header.LedgerHeader{LedgerIndex: seq, AccountHash: rootHash})
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
	il := newAcquiring(t, 200)

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
	rootHash, rootData, wire := buildSourceState(t)
	hdrBytes, hash := encodeHeader(header.LedgerHeader{LedgerIndex: 300, AccountHash: rootHash})
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
	var failHash [32]byte
	failHash[0] = 0x33

	// A prior attempt at seq 600 (failHash) failed and is remembered.
	failed := New(failHash, 600, 3, discardLogger())
	if err := failed.GotBase([]message.LedgerNode{{NodeData: []byte{0x00}}}); err == nil {
		t.Fatal("expected GotBase to fail")
	}
	// A fresh attempt at the same seq is now in flight.
	live := newAcquiring(t, 600)

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

// TestInbound_FullAcquisitionWithTransactions drives a ledger with both a
// non-empty state tree and a non-empty transaction tree through the full
// acquisition. fetch_info reports have_transactions + needed_transaction_hashes
// while the tx tree is outstanding, and the acquisition is complete only once
// both trees are in hand.
func TestInbound_FullAcquisitionWithTransactions(t *testing.T) {
	t.Parallel()
	stateRootHash, stateRoot, stateWire := buildSourceMap(t, shamap.TypeState)
	txRootHash, txRoot, txWire := buildSourceMap(t, shamap.TypeTransaction)

	hdr, hash := encodeHeader(header.LedgerHeader{LedgerIndex: 700, AccountHash: stateRootHash, TxHash: txRootHash})
	il := New(hash, 700, 7, discardLogger())
	if err := il.GotBase([]message.LedgerNode{{NodeData: hdr}, {NodeData: stateRoot}, {NodeData: txRoot}}); err != nil {
		t.Fatalf("GotBase: %v", err)
	}
	if il.State() != StateWantState {
		t.Fatalf("state = %d, want StateWantState", il.State())
	}
	if il.NeedsMissingTxNodeIDs() == nil {
		t.Fatal("expected outstanding tx nodes to request")
	}

	tr := NewTracker()
	tr.Track(il)

	entry := tr.Info()["700"].(map[string]any)
	if entry["have_transactions"] != false {
		t.Errorf("have_transactions = %v, want false", entry["have_transactions"])
	}
	if needed, ok := entry["needed_transaction_hashes"].([]any); !ok || len(needed) == 0 {
		t.Errorf("needed_transaction_hashes = %#v, want non-empty", entry["needed_transaction_hashes"])
	}

	// State completes first; the acquisition must still wait for the tx tree.
	if err := il.GotStateNodes(stateWire); err != nil {
		t.Fatalf("GotStateNodes: %v", err)
	}
	if il.IsComplete() {
		t.Fatal("acquisition complete before tx tree fetched")
	}

	// Tx completes; the acquisition is now complete.
	if err := il.GotTransactionNodes(txWire); err != nil {
		t.Fatalf("GotTransactionNodes: %v", err)
	}
	if !il.IsComplete() {
		t.Fatal("acquisition not complete after both trees fetched")
	}
	if _, _, gotTx, err := il.Result(); err != nil || gotTx == nil {
		t.Fatalf("Result tx map = %v (err %v), want the acquired tree", gotTx, err)
	}

	entry = tr.Info()["700"].(map[string]any)
	if entry["complete"] != true {
		t.Errorf("complete = %v, want true", entry["complete"])
	}
	if entry["have_transactions"] != true {
		t.Errorf("have_transactions = %v, want true", entry["have_transactions"])
	}
	if _, has := entry["needed_transaction_hashes"]; has {
		t.Errorf("needed_transaction_hashes must be absent once tx acquired, got %#v", entry["needed_transaction_hashes"])
	}
}

// TestInbound_EmptyTxTreeImmediatelyComplete confirms a ledger with no
// transactions (zero TxHash) reports have_transactions:true on arrival and
// completes on the state tree alone, with no tx round-trip and a nil tx map.
func TestInbound_EmptyTxTreeImmediatelyComplete(t *testing.T) {
	t.Parallel()
	stateRootHash, stateRoot, stateWire := buildSourceMap(t, shamap.TypeState)

	hdr, hash := encodeHeader(header.LedgerHeader{LedgerIndex: 800, AccountHash: stateRootHash}) // TxHash zero
	il := New(hash, 800, 7, discardLogger())
	if err := il.GotBase([]message.LedgerNode{{NodeData: hdr}, {NodeData: stateRoot}}); err != nil {
		t.Fatalf("GotBase: %v", err)
	}
	if il.NeedsMissingTxNodeIDs() != nil {
		t.Error("empty tx tree must not request tx nodes")
	}

	tr := NewTracker()
	tr.Track(il)
	entry := tr.Info()["800"].(map[string]any)
	if entry["have_transactions"] != true {
		t.Errorf("have_transactions = %v, want true (empty tx tree)", entry["have_transactions"])
	}
	if _, has := entry["needed_transaction_hashes"]; has {
		t.Error("needed_transaction_hashes must be absent for an empty tx tree")
	}

	if err := il.GotStateNodes(stateWire); err != nil {
		t.Fatalf("GotStateNodes: %v", err)
	}
	if !il.IsComplete() {
		t.Fatal("acquisition with empty tx tree should complete on state")
	}
	if _, _, gotTx, err := il.Result(); err != nil || gotTx != nil {
		t.Fatalf("Result tx map = %v (err %v), want nil for empty tx tree", gotTx, err)
	}
}

// TestTracker_FailedEntryCarriesRichShape confirms a failed/timed-out
// acquisition reports the full per-tree getJson shape (failed:true plus
// have_header, no peers), mirroring rippled's still-in-mLedgers failed ledger
// rather than a bare {failed:true}.
func TestTracker_FailedEntryCarriesRichShape(t *testing.T) {
	t.Parallel()
	stateRootHash, stateRoot, _ := buildSourceMap(t, shamap.TypeState)
	txRootHash, txRoot, _ := buildSourceMap(t, shamap.TypeTransaction)

	hdr, hash := encodeHeader(header.LedgerHeader{LedgerIndex: 950, AccountHash: stateRootHash, TxHash: txRootHash})
	il := New(hash, 950, 7, discardLogger())
	if err := il.GotBase([]message.LedgerNode{{NodeData: hdr}, {NodeData: stateRoot}, {NodeData: txRoot}}); err != nil {
		t.Fatalf("GotBase: %v", err)
	}
	il.created = time.Now().Add(-2 * acquisitionTimeout) // white-box: force timeout

	tr := NewTracker()
	tr.Track(il)
	entry := tr.Info()["950"].(map[string]any)
	if entry["failed"] != true {
		t.Errorf("failed = %v, want true", entry["failed"])
	}
	if entry["have_header"] != true {
		t.Errorf("failed entry should carry have_header, got %#v", entry)
	}
	if _, hasPeers := entry["peers"]; hasPeers {
		t.Errorf("failed entry must not report peers, got %#v", entry)
	}
}
