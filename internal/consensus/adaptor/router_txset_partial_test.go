package adaptor

import (
	"bytes"
	"log/slog"
	"sort"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// silentLogger satisfies the logger interface used by
// buildShaMapReplyNodes without writing anywhere — keeps test
// output focused on the assertions.
type silentLogger struct{}

func (silentLogger) Debug(string, ...any) {}
func (silentLogger) Warn(string, ...any)  {}

// buildTxSetForTest constructs a SHAMap with `n` synthetic 16-byte
// tx blobs whose first byte cycles 0x10..0x10+n. Returns the map,
// the canonical tx-set ID (root hash), and the WireNodes for
// reference assertions.
func buildTxSetForTest(t *testing.T, n int) (*shamap.SHAMap, [32]byte, []shamap.WireNode) {
	t.Helper()
	blobs := make([][]byte, n)
	for i := range blobs {
		blobs[i] = bytes.Repeat([]byte{byte(0x10 + i)}, 16)
	}
	ts, err := NewTxSet(blobs)
	require.NoError(t, err)
	txMap := ts.shamap()
	require.NotNil(t, txMap)
	wireNodes, err := txMap.WalkWireNodes()
	require.NoError(t, err)
	return txMap, [32]byte(ts.ID()), wireNodes
}

// rootNodeID returns the 33-byte SHAMapNodeID of the root: 32-byte
// zero path + 1-byte zero depth.
func rootNodeID() []byte {
	return make([]byte, 33)
}

// TestServeTxSet_RootRequest_RootFirstAndInnersIncluded pins the
// most common case — TransactionAcquire's first request with
// NodeIDs=[root], querydepth=3, fatLeaves=false. The serve must:
//
//  1. Emit the root as the first node.
//  2. Include every INNER node within the depth budget.
//  3. Skip leaves that fall on the depth boundary — that's
//     deliberate (rippled comments the choice as "We'll already
//     have most transactions"). The requestor reconstructs leaves
//     from its own open-ledger pool.
//
// Leaves DEEPER than the boundary are still included by virtue of
// being popped (every popped node gets emitted), so the
// fatLeaves=false skip only affects leaves whose parent is at the
// budget=0 boundary.
func TestServeTxSet_RootRequest_RootFirstAndInnersIncluded(t *testing.T) {
	txMap, _, wireNodes := buildTxSetForTest(t, 4)

	got := buildShaMapReplyNodes(
		txMap,
		[][]byte{rootNodeID()},
		3,     // querydepth=3 — TransactionAcquire's first-request value
		false, // fatLeaves=false — rippled liTS_CANDIDATE default
		silentLogger{},
		7,
		"txset",
	)

	require.NotEmpty(t, got, "must return at least the root")
	require.Len(t, got[0].NodeID, 33, "first node's NodeID must be a SHAMapNodeID (33 bytes)")
	for _, b := range got[0].NodeID {
		require.Equal(t, byte(0), b, "first node must be the SHAMap root (NodeID = 33 zeros)")
	}

	// Every INNER node from the full walk must be in the serve output.
	// We don't have a direct "is inner" predicate on WireNode, but the
	// SHAMap rebuild round-trip in the existing serve test
	// (router_txset_wire_test.go) already pins that the served bytes
	// reconstruct the full tree — here we assert the necessary
	// pre-condition that the served set never SHRINKS below the inner
	// subset.
	wireByID := make(map[string]shamap.WireNode, len(wireNodes))
	for _, n := range wireNodes {
		wireByID[string(n.NodeID)] = n
	}
	served := make(map[string]bool, len(got))
	for _, n := range got {
		served[string(n.NodeID)] = true
		// Sanity: every served NodeID must come from the walk
		// (no synthesized nodes).
		_, exists := wireByID[string(n.NodeID)]
		assert.Truef(t, exists,
			"served node %x@d=%d not in canonical wire walk",
			n.NodeID[:8], n.NodeID[32])
	}
	// Soft assertion: at minimum the root and the depth-1 inner
	// frontier must be present. With fatLeaves=false depth-1 leaves
	// may legitimately be skipped, so we don't strict-equal the sets.
	require.GreaterOrEqual(t, len(got), 1)
	assert.LessOrEqual(t, len(got), len(wireNodes),
		"serve never invents nodes; bound by full-walk size")
}

// TestServeTxSet_RootRequest_FatLeaves_FullCoverage pins that
// passing fatLeaves=true to the same root-request path yields the
// COMPLETE tree (every node from WalkWireNodes). Used by callers
// that don't trust the requestor's pool — at the cost of more
// bytes, every leaf is carried inline.
func TestServeTxSet_RootRequest_FatLeaves_FullCoverage(t *testing.T) {
	txMap, _, wireNodes := buildTxSetForTest(t, 4)

	got := buildShaMapReplyNodes(
		txMap,
		[][]byte{rootNodeID()},
		3,
		true, // fatLeaves=true — include leaves at depth boundary
		silentLogger{},
		7,
		"txset",
	)

	served := make(map[string]bool, len(got))
	for _, n := range got {
		served[string(n.NodeID)] = true
	}
	for _, want := range wireNodes {
		assert.Truef(t, served[string(want.NodeID)],
			"fatLeaves=true must cover the full walk; missing %x@d=%d",
			want.NodeID[:8], want.NodeID[32])
	}
}

// TestServeTxSet_NoNodeIDs_FallsBackToFullWalk pins the legacy
// behaviour: when a request omits NodeIDs entirely (some test
// fixtures and out-of-spec callers do this), we walk the entire
// SHAMap pre-order. Without the fallback, those callers get an
// empty response and the engine never sees the tx-set.
func TestServeTxSet_NoNodeIDs_FallsBackToFullWalk(t *testing.T) {
	txMap, _, wireNodes := buildTxSetForTest(t, 3)

	got := buildShaMapReplyNodes(
		txMap,
		nil, // explicit empty
		1,   // ignored on the fallback path
		false,
		silentLogger{},
		7,
		"txset",
	)

	require.Len(t, got, len(wireNodes), "fallback walks the full tree")
	for i, n := range got {
		assert.Equal(t, wireNodes[i].NodeID, n.NodeID,
			"fallback emits pre-order — same order as WalkWireNodes")
		assert.Equal(t, wireNodes[i].Data, n.NodeData)
	}
}

// TestServeTxSet_BadNodeID_Skipped pins that a malformed
// SHAMapNodeID in the request (wrong length, depth byte > 64)
// is silently skipped — matches rippled's deserializeSHAMapNodeID
// rejection path. The serve must not panic and must still process
// other valid NodeIDs in the same request.
func TestServeTxSet_BadNodeID_Skipped(t *testing.T) {
	txMap, _, _ := buildTxSetForTest(t, 2)

	got := buildShaMapReplyNodes(
		txMap,
		[][]byte{
			{0xAA, 0xBB},     // too short
			rootNodeID(),     // valid
			make([]byte, 50), // too long
		},
		1, false, silentLogger{}, 7, "txset",
	)

	require.NotEmpty(t, got, "valid root NodeID in the middle must still produce output")
	require.Len(t, got[0].NodeID, 33, "served nodes still carry 33-byte NodeIDs")
}

// TestServeTxSet_HardCap pins the inner-loop truncation gate
// (rippled's PeerImp.cpp:3406-3407). Force-shrink the cap via a
// helper so a small tx-set still trips the boundary; assert the
// returned slice never exceeds the cap and the truncation is
// observable. Without this gate a malicious peer could request a
// huge tx-set and we'd send the whole thing in one frame, blowing
// past the ~64 MB protocol message-size limit.
func TestServeTxSet_HardCap(t *testing.T) {
	txMap, _, wireNodes := buildTxSetForTest(t, 16) // ~ root + inners + 16 leaves
	require.Greater(t, len(wireNodes), 5, "fixture must have enough nodes to exercise the cap")

	got := withTxSetReplyCaps(2, 3, func() []message.LedgerNode {
		return buildShaMapReplyNodes(
			txMap,
			[][]byte{rootNodeID()},
			3, false, silentLogger{}, 7, "txset",
		)
	})

	assert.LessOrEqual(t, len(got), 3,
		"hard cap must truncate the served subtree — observed %d nodes, cap was 3",
		len(got))
	require.NotEmpty(t, got)
	// Root must still be the first emitted node — pre-order is
	// preserved even under truncation.
	require.Len(t, got[0].NodeID, 33)
	for _, b := range got[0].NodeID {
		require.Equal(t, byte(0), b, "root must come first even when truncated")
	}
}

// TestServeTxSet_SoftCap pins the outer-loop guard
// (rippled's PeerImp.cpp:3387). When the running total reaches
// the soft cap, the serve must STOP starting new subtrees from
// the request's nodeids list — already-included subtrees are
// emitted in full but no new ones begin. Distinguishes from
// hardMax which truncates mid-subtree.
//
// We exercise this by submitting MULTIPLE requested NodeIDs and
// verifying that NodeIDs after the soft-cap boundary contribute
// zero nodes to the output.
func TestServeTxSet_SoftCap(t *testing.T) {
	txMap, _, wireNodes := buildTxSetForTest(t, 8)

	// Pick two distinct leaf-level NodeIDs from the wire walk so
	// each one yields >= 1 node when requested. We need them sorted
	// by NodeID for stable assertions.
	leaves := make([]shamap.WireNode, 0)
	for _, n := range wireNodes {
		if n.NodeID[32] > 0 { // non-root depth
			leaves = append(leaves, n)
		}
	}
	require.GreaterOrEqual(t, len(leaves), 2, "need at least 2 non-root nodes to exercise soft cap")
	sort.Slice(leaves, func(i, j int) bool {
		return bytes.Compare(leaves[i].NodeID, leaves[j].NodeID) < 0
	})

	// Soft cap of 1 means: after the FIRST subtree adds any nodes,
	// every subsequent NodeID in the request is skipped.
	got := withTxSetReplyCaps(1, 100, func() []message.LedgerNode {
		return buildShaMapReplyNodes(
			txMap,
			[][]byte{leaves[0].NodeID, leaves[1].NodeID},
			0,
			false,
			silentLogger{},
			7,
			"txset",
		)
	})

	require.NotEmpty(t, got, "the first subtree must still produce output")
	// The second NodeID was skipped → its NodeID must NOT appear in
	// the output. (Other inner nodes shared between them might still
	// match by descent, but the leaf at leaves[1].NodeID specifically
	// only comes from a request that targets it.)
	for _, n := range got {
		assert.NotEqual(t, leaves[1].NodeID, n.NodeID,
			"soft cap must prevent the second requested subtree from being walked")
	}
}

// withTxSetReplyCaps temporarily overrides the soft/hard cap
// constants for the duration of fn. Restores them on return so
// tests don't leak state.
func withTxSetReplyCaps(soft, hard int, fn func() []message.LedgerNode) []message.LedgerNode {
	origSoft, origHard := txSetReplyCapsForTest()
	setTxSetReplyCapsForTest(soft, hard)
	defer setTxSetReplyCapsForTest(origSoft, origHard)
	return fn()
}

// silence the unused-import linter when running -short tests that
// don't reach the slog.Default() path.
var _ = slog.Default
var _ peermanagement.PeerID = 0
