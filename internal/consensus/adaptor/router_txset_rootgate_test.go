package adaptor

import (
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/stretchr/testify/require"
)

// ldFromWire builds a liTS_CANDIDATE reply carrying the given SHAMap wire nodes.
func ldFromWire(txSetID [32]byte, nodes []shamap.WireNode) *message.LedgerData {
	lns := make([]message.LedgerNode, 0, len(nodes))
	for _, w := range nodes {
		lns = append(lns, message.LedgerNode{NodeID: w.NodeID, NodeData: w.Data})
	}
	return &message.LedgerData{
		InfoType:   message.LedgerInfoTsCandidate,
		LedgerHash: txSetID[:],
		Nodes:      lns,
	}
}

// TestTxSetAcquire_RootlessReplyDoesNotCompleteEmpty pins the root-gate fix: a
// reply carrying only NON-root nodes for a set we're not yet tracking must NOT
// complete-empty (the bug: a fresh shamap.New has an empty root, FinishSync on
// it "succeeds" with zero leaves → a bogus tx_count=0 completion + delete →
// recreate storm). Instead the acquire is created rootless, the root is
// requested (unicast to the replying peer), and the entry is RETAINED. Then
// delivering the root + nodes completes it normally with the right tx_count.
func TestTxSetAcquire_RootlessReplyDoesNotCompleteEmpty(t *testing.T) {
	router, rs, eng := newPipelineRouter(t)
	withRetryKnobs(router, time.Hour, 1_000_000, 1_000_000, func() {
		_, rawID, wireNodes := buildTxSetForTest(t, 8)
		require.Greater(t, len(wireNodes), 1, "fixture must have non-root nodes")
		txSetID := consensus.TxSetID(rawID)

		// Feed ONLY the non-root nodes (a missing-nodes reply for a set with no
		// live acquire) from peer 7.
		router.handleTxSetData(ldFromWire(rawID, wireNodes[1:]), 7)

		// No completion: the engine must not have been fed an empty set.
		eng.mu.Lock()
		fedAfterRootless := len(eng.txSets)
		eng.mu.Unlock()
		require.Zero(t, fedAfterRootless,
			"a rootless reply must not complete-empty and feed the engine")

		// Acquire retained, rootless, not done, and the root was requested once
		// (unicast to the replying peer).
		router.txSetAcquireMu.Lock()
		state, tracked := router.txSetAcquire[txSetID]
		var haveRoot, done bool
		if tracked {
			haveRoot, done = state.haveRoot, state.done
		}
		router.txSetAcquireMu.Unlock()
		require.True(t, tracked, "the acquire must be retained, not deleted")
		require.False(t, haveRoot, "no root installed from a non-root-only reply")
		require.False(t, done, "a rootless acquire must not be latched done")
		require.Equal(t, 1, rs.calledN(), "the root must be requested exactly once")
		require.Equal(t, uint64(7), rs.lastCall().peerID,
			"the root request must be unicast to the replying peer")

		// Now deliver root + all nodes → completes normally with the right count.
		router.handleTxSetData(ldFromWire(rawID, wireNodes), 7)

		eng.mu.Lock()
		gotSets := append([]consensus.TxSetID(nil), eng.txSets...)
		eng.mu.Unlock()
		require.Len(t, gotSets, 1, "delivering the root + nodes must complete the acquire")
		require.Equal(t, txSetID, gotSets[0], "the completed set ID must match")

		router.txSetAcquireMu.Lock()
		state, tracked = router.txSetAcquire[txSetID]
		doneAfter := tracked && state.done
		router.txSetAcquireMu.Unlock()
		require.True(t, tracked, "a completed acquire is retained (done), not deleted")
		require.True(t, doneAfter, "a completed acquire is latched done")
	})
}

// TestTxSetAcquire_StragglerAfterDoneIsDropped pins the done-latch anti-storm
// guard: once an acquire completes it is retained as done, and any further
// straggler reply for that set is DROPPED — no engine re-feed, no re-request,
// no fresh acquire. This is the fix for the 860×/25ms recreate storm.
func TestTxSetAcquire_StragglerAfterDoneIsDropped(t *testing.T) {
	router, rs, eng := newPipelineRouter(t)
	withRetryKnobs(router, time.Hour, 1_000_000, 1_000_000, func() {
		_, rawID, wireNodes := buildTxSetForTest(t, 8)
		txSetID := consensus.TxSetID(rawID)
		completeLD := ldFromWire(rawID, wireNodes)

		// Complete the acquire in one reply.
		router.handleTxSetData(completeLD, 1)
		eng.mu.Lock()
		require.Len(t, eng.txSets, 1, "the set must complete and feed the engine once")
		eng.mu.Unlock()

		nAfterComplete := rs.calledN()
		router.txSetAcquireMu.Lock()
		state, tracked := router.txSetAcquire[txSetID]
		done := tracked && state.done
		router.txSetAcquireMu.Unlock()
		require.True(t, tracked, "a completed acquire is retained as done")
		require.True(t, done)

		// A burst of straggler replies (both a full re-send and empty replies)
		// for the finished set must all be dropped.
		for range 50 {
			router.handleTxSetData(completeLD, 2)
			router.handleTxSetData(emptyTxSetLedgerData(txSetID), 3)
		}
		eng.mu.Lock()
		require.Len(t, eng.txSets, 1, "stragglers must not re-feed the engine")
		eng.mu.Unlock()
		require.Equal(t, nAfterComplete, rs.calledN(),
			"stragglers for a done acquire must not trigger re-requests")

		// The entry is the same one (not recreated).
		router.txSetAcquireMu.Lock()
		state2, tracked2 := router.txSetAcquire[txSetID]
		router.txSetAcquireMu.Unlock()
		require.True(t, tracked2)
		require.Same(t, state, state2, "no fresh acquire was created for the finished set")
	})
}

// TestTxSetAcquire_DuplicateOnlyReplyDoesNotReRequest pins that only a fresh
// attach (NodeUseful) counts as progress: a reply that re-sends nodes the map
// already holds (all NodeDuplicate, no root) makes no progress and must NOT
// pipeline a re-request — otherwise a peer replaying fat nodes keeps firing
// requests (the 70f35035 regression).
func TestTxSetAcquire_DuplicateOnlyReplyDoesNotReRequest(t *testing.T) {
	router, rs, _ := newPipelineRouter(t)
	withRetryKnobs(router, time.Hour, 1_000_000, 1_000_000, func() {
		_, rawID, wireNodes := buildTxSetForTest(t, 16)
		require.Greater(t, len(wireNodes), 2, "fixture must have a root and >=2 non-root nodes")
		root := wireNodes[0]
		firstNonRoot := wireNodes[1]

		// Root reply → progress (installs the root) → pipelines one request.
		router.handleTxSetData(ldFromWire(rawID, []shamap.WireNode{root}), 1)
		require.Equal(t, 1, rs.calledN(), "a root reply pipelines one request")

		// A reply carrying one fresh non-root node → progress → one more request.
		router.handleTxSetData(ldFromWire(rawID, []shamap.WireNode{firstNonRoot}), 1)
		require.Equal(t, 2, rs.calledN(), "a fresh non-root node pipelines one more request")

		// Re-deliver the SAME non-root node (all NodeDuplicate, no root): nothing
		// is added, so it is not progress and must NOT re-request.
		router.handleTxSetData(ldFromWire(rawID, []shamap.WireNode{firstNonRoot}), 1)
		require.Equal(t, 2, rs.calledN(),
			"a duplicate-only reply makes no progress and must not re-request")
	})
}
