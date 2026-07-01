package inbound

import (
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/shamap"
)

// newAcquisitionWithMissingNodes builds an acquisition parked in StateWantState
// with an incomplete state tree, so CollectMissingRequest has outstanding nodes
// to hand out. Modeled on TestNeedsMissingNodeIDs_RequestsActualMissingNodes.
func newAcquisitionWithMissingNodes(t *testing.T) *Ledger {
	t.Helper()
	source := shamap.New(shamap.TypeState)
	for branch := range byte(8) {
		for i := range byte(4) {
			var key [32]byte
			key[0] = (branch << 4) | i
			if err := source.Put(key, make([]byte, 12)); err != nil {
				t.Fatalf("put: %v", err)
			}
		}
	}
	rootHash, err := source.Hash()
	if err != nil {
		t.Fatalf("source hash: %v", err)
	}
	rootData, err := source.SerializeRoot()
	if err != nil {
		t.Fatalf("serialize root: %v", err)
	}

	hdr := header.LedgerHeader{LedgerIndex: 100, AccountHash: rootHash}
	hdrBytes, ledgerHash := encodeHeader(hdr)

	il := New(ledgerHash, 100, 7, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := il.GotBase([]message.LedgerNode{
		{NodeData: hdrBytes},
		{NodeData: rootData},
	}); err != nil {
		t.Fatalf("GotBase: %v", err)
	}
	return il
}

// TestCollectMissingRequest_ThrottlesReplyReRequests is the anti-spin
// regression: within one timer interval the reply path hands out the missing
// nodes once, then de-dups subsequent reply-driven re-requests to nothing (so a
// peer reply cannot re-request the same outstanding nodes at RTT rate). The
// timeout path bypasses the de-dup, and a timer due-fire clears it so the next
// interval may re-request.
func TestCollectMissingRequest_ThrottlesReplyReRequests(t *testing.T) {
	t.Parallel()
	il := newAcquisitionWithMissingNodes(t)

	first, _ := il.CollectMissingRequest(true)
	if len(first) == 0 {
		t.Fatal("the first reply-path collect must return outstanding state nodes")
	}

	// Every node just requested is now in recentNodes → a second reply-path
	// collect in the same interval sends nothing. This is the spin fix.
	second, _ := il.CollectMissingRequest(true)
	if len(second) != 0 {
		t.Fatalf("reply re-request within an interval must be de-duped to nothing; got %d nodes", len(second))
	}

	// A no-progress timeout fan-out bypasses the de-dup so it still queries peers.
	timeout, _ := il.CollectMissingRequest(false)
	if len(timeout) == 0 {
		t.Fatal("the timeout fan-out must bypass the de-dup filter and re-request")
	}

	// A due timer fire clears recentNodes, pacing re-requests to ~once/interval.
	future := time.Now().Add(2 * acquireTimerInterval)
	il.OnTimer(future)
	third, _ := il.CollectMissingRequest(true)
	if len(third) == 0 {
		t.Fatal("after a timer tick the reply path may re-request the still-missing nodes")
	}
}

// TestCollectMissingRequest_TimeoutStillFiresWhenAllDuplicates isolates the
// timeout-bypass branch: after the reply path has recorded every outstanding
// node this interval, the reply path returns nothing but the timeout path still
// returns the full set.
func TestCollectMissingRequest_TimeoutStillFiresWhenAllDuplicates(t *testing.T) {
	t.Parallel()
	il := newAcquisitionWithMissingNodes(t)

	if state, _ := il.CollectMissingRequest(true); len(state) == 0 {
		t.Fatal("precondition: reply collect must seed recentNodes")
	}
	if state, _ := il.CollectMissingRequest(true); len(state) != 0 {
		t.Fatal("reply path must be all-duplicates now")
	}
	if state, _ := il.CollectMissingRequest(false); len(state) == 0 {
		t.Fatal("timeout path must still fan out even when every node is a duplicate")
	}
}
