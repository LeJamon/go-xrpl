package inbound

import (
	"context"
	"io"
	"log/slog"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/LeJamon/go-xrpl/shamap/backend"
)

type countingMissingFamily struct {
	base  *backend.NodeStore
	reads atomic.Int64
}

func (f *countingMissingFamily) Fetch(ctx context.Context, hash [32]byte) ([]byte, error) {
	return f.base.Fetch(ctx, hash)
}

func (f *countingMissingFamily) FetchDurable(ctx context.Context, hash [32]byte) ([]byte, error) {
	f.reads.Add(1)
	return f.base.FetchDurable(ctx, hash)
}

func (f *countingMissingFamily) StoreBatch(ctx context.Context, entries []shamap.FlushEntry) error {
	return f.base.StoreBatch(ctx, entries)
}

func (f *countingMissingFamily) FullBelowCache() *shamap.FullBelowCache {
	return f.base.FullBelowCache()
}

// newAcquisitionWithMissingNodes builds an acquisition parked in StateWantState
// with an incomplete state tree, so CollectMissingRequest has outstanding nodes
// to hand out. Modeled on TestNeedsMissingNodeIDs_RequestsActualMissingNodes.
func newAcquisitionWithMissingNodes(t *testing.T) *Ledger {
	return newAcquisitionWithMissingNodesAndOptions(t)
}

func newAcquisitionWithMissingNodesAndOptions(t *testing.T, opts ...Option) *Ledger {
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

	il := New(ledgerHash, 100, 7, slog.New(slog.NewTextHandler(io.Discard, nil)), opts...)
	if err := il.GotBase([]message.LedgerNode{
		{NodeData: hdrBytes},
		{NodeData: rootData},
	}); err != nil {
		t.Fatalf("GotBase: %v", err)
	}
	return il
}

func newWideAcquisition(t *testing.T, opts ...Option) *Ledger {
	t.Helper()
	source := shamap.New(shamap.TypeState)
	for first := byte(0); first < 1; first++ {
		for second := byte(0); second < 16; second++ {
			for third := byte(0); third < 16; third++ {
				for fourth := byte(0); fourth < 8; fourth++ {
					var key [32]byte
					key[0] = first<<4 | second
					key[1] = third<<4 | fourth
					data := make([]byte, 12)
					copy(data, []byte{first, second, third, fourth})
					if err := source.Put(key, data); err != nil {
						t.Fatalf("put: %v", err)
					}
				}
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
	hdrBytes, ledgerHash := encodeHeader(header.LedgerHeader{LedgerIndex: 101, AccountHash: rootHash})
	il := New(ledgerHash, 101, 7, slog.New(slog.NewTextHandler(io.Discard, nil)), opts...)
	if err := il.GotBase([]message.LedgerNode{{NodeData: hdrBytes}, {NodeData: rootData}}); err != nil {
		t.Fatalf("GotBase: %v", err)
	}
	wire, err := source.WalkWireNodes()
	if err != nil {
		t.Fatalf("WalkWireNodes: %v", err)
	}
	ancestors := make([]message.LedgerNode, 0, 273)
	for _, node := range wire {
		depth := node.NodeID[32]
		if depth == 1 || depth == 2 || depth == 3 {
			ancestors = append(ancestors, message.LedgerNode{NodeID: node.NodeID, NodeData: node.Data})
		}
	}
	if added, err := il.GotStateNodesUseful(ancestors); err != nil || added != len(ancestors) {
		t.Fatalf("attach ancestors: added=%d want=%d err=%v", added, len(ancestors), err)
	}
	return il
}

func TestCollectMissingReplyRequests_SixDisjointFrontiers(t *testing.T) {
	for _, test := range []struct {
		name string
		opts []Option
	}{
		{name: "unbacked"},
		{name: "backed", opts: []Option{WithFamily(backend.NewMemory())}},
	} {
		t.Run(test.name, func(t *testing.T) {
			il := newWideAcquisition(t, test.opts...)
			requests, complete := il.CollectMissingReplyRequests([]uint64{11, 12, 13, 14, 15, 16})
			if complete {
				t.Fatal("incomplete tree reported complete")
			}
			if len(requests) != 6 {
				t.Fatalf("requests=%d, want 6", len(requests))
			}
			seen := make(map[string]uint64, missingNodesFind)
			for i, request := range requests {
				if request.PeerID != uint64(11+i) {
					t.Fatalf("request %d peer=%d, want %d", i, request.PeerID, 11+i)
				}
				if request.Transaction || len(request.NodeIDs) == 0 || len(request.NodeIDs) > reqNodesReply {
					t.Fatalf("request %d transaction=%t nodes=%d", i, request.Transaction, len(request.NodeIDs))
				}
				for _, nodeID := range request.NodeIDs {
					key := string(nodeID)
					if owner, duplicate := seen[key]; duplicate {
						t.Fatalf("node assigned to peers %d and %d", owner, request.PeerID)
					}
					seen[key] = request.PeerID
				}
			}
			if len(seen) != missingNodesFind {
				t.Fatalf("assigned nodes=%d, want bounded frontier %d", len(seen), missingNodesFind)
			}

			if next, _ := il.CollectMissingReplyRequests([]uint64{17}); len(next) != 0 {
				t.Fatalf("seventh request escaped the six-peer window: %#v", next)
			}
			il.ReleaseMissingPeer(requests[0].PeerID)
			next, _ := il.CollectMissingReplyRequests([]uint64{17})
			if len(next) != 1 || len(next[0].NodeIDs) == 0 {
				t.Fatalf("replacement frontier=%v, want further disjoint work", next)
			}
			for _, nodeID := range next[0].NodeIDs {
				if slices.ContainsFunc(requests, func(request MissingRequest) bool {
					return slices.ContainsFunc(request.NodeIDs, func(prior []byte) bool { return string(prior) == string(nodeID) })
				}) {
					t.Fatal("next frontier repeated an in-flight node")
				}
			}
		})
	}
}

func TestCollectMissingReplyRequests_OneLeasePerPeer(t *testing.T) {
	il := newWideAcquisition(t)
	first, _ := il.CollectMissingReplyRequests([]uint64{21})
	if len(first) != 1 {
		t.Fatalf("first requests=%d, want 1", len(first))
	}
	if duplicate, _ := il.CollectMissingReplyRequests([]uint64{21}); len(duplicate) != 0 {
		t.Fatalf("busy peer received a second request: %#v", duplicate)
	}

	il.ReleaseMissingPeer(21)
	next, _ := il.CollectMissingReplyRequests([]uint64{21})
	if len(next) != 1 || len(next[0].NodeIDs) == 0 {
		t.Fatalf("released peer did not receive a replacement frontier: %#v", next)
	}
	for _, nodeID := range next[0].NodeIDs {
		if slices.ContainsFunc(first[0].NodeIDs, func(prior []byte) bool {
			return string(prior) == string(nodeID)
		}) {
			t.Fatal("replacement request repeated an in-flight node")
		}
	}
}

func TestCollectMissingReplyRequests_ReleasesFailedAssignment(t *testing.T) {
	il := newAcquisitionWithMissingNodes(t)
	first, _ := il.CollectMissingReplyRequests([]uint64{21})
	if len(first) != 1 {
		t.Fatalf("first requests=%d, want 1", len(first))
	}
	if again, _ := il.CollectMissingReplyRequests([]uint64{22}); len(again) != 0 {
		t.Fatal("in-flight frontier must stay reserved")
	}
	il.ReleaseMissingRequest(first[0].PeerID, first[0].NodeHashes)
	retry, _ := il.CollectMissingReplyRequests([]uint64{22})
	if len(retry) != 1 || len(retry[0].NodeIDs) != len(first[0].NodeIDs) {
		t.Fatalf("released frontier was not reassigned: %#v", retry)
	}
}

func TestCollectMissingReplyRequests_DoesNotRescanReservedFrontier(t *testing.T) {
	family := &countingMissingFamily{base: backend.NewMemory()}
	il := newAcquisitionWithMissingNodesAndOptions(t, WithFamily(family))
	first, complete, err := il.CollectMissingReplyRequestsContext(t.Context(), []uint64{21})
	if err != nil || complete || len(first) != 1 {
		t.Fatalf("first collect = %#v, complete=%t, err=%v", first, complete, err)
	}

	family.reads.Store(0)
	second, complete, err := il.CollectMissingReplyRequestsContext(t.Context(), []uint64{22})
	if err != nil || complete || len(second) != 0 {
		t.Fatalf("reserved collect = %#v, complete=%t, err=%v", second, complete, err)
	}
	if reads := family.reads.Load(); reads != 8 {
		t.Fatalf("durable reads=%d, want one eight-branch filtered scan", reads)
	}
}

func TestCollectMissingReplyRequests_CompletesWhenReservationsArrived(t *testing.T) {
	source := shamap.New(shamap.TypeState)
	var key [32]byte
	key[0] = 0xA0
	if err := source.Put(key, make([]byte, 12)); err != nil {
		t.Fatalf("put: %v", err)
	}
	rootHash, err := source.Hash()
	if err != nil {
		t.Fatalf("source hash: %v", err)
	}
	rootData, err := source.SerializeRoot()
	if err != nil {
		t.Fatalf("serialize root: %v", err)
	}
	hdrBytes, ledgerHash := encodeHeader(header.LedgerHeader{LedgerIndex: 102, AccountHash: rootHash})
	il := New(ledgerHash, 102, 7, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := il.GotBase([]message.LedgerNode{{NodeData: hdrBytes}, {NodeData: rootData}}); err != nil {
		t.Fatalf("GotBase: %v", err)
	}

	first, complete, err := il.CollectMissingReplyRequestsContext(t.Context(), []uint64{21})
	if err != nil || complete || len(first) != 1 || len(first[0].NodeIDs) != 1 {
		t.Fatalf("first collect = %#v, complete=%t, err=%v", first, complete, err)
	}

	wire, err := source.WalkWireNodes()
	if err != nil {
		t.Fatalf("walk source: %v", err)
	}
	byID := make(map[string]message.LedgerNode, len(wire))
	for _, node := range wire {
		byID[string(node.NodeID)] = message.LedgerNode{NodeID: node.NodeID, NodeData: node.Data}
	}
	node, ok := byID[string(first[0].NodeIDs[0])]
	if !ok {
		t.Fatalf("requested node %x absent from source", first[0].NodeIDs[0])
	}
	if added, err := il.GotStateNodesUseful([]message.LedgerNode{node}); err != nil || added != 1 {
		t.Fatalf("add reserved node: added=%d err=%v", added, err)
	}

	requests, complete, err := il.CollectMissingReplyRequestsContext(t.Context(), []uint64{22})
	if err != nil || !complete || len(requests) != 0 {
		t.Fatalf("completion collect = %#v, complete=%t, err=%v", requests, complete, err)
	}
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
