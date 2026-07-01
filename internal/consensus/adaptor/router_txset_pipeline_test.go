package adaptor

import (
	"encoding/binary"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/stretchr/testify/require"
)

// newPipelineRouter is newRetryRouter plus a handle on the mockEngine so
// the acquisition test can assert the completed set reached the engine.
func newPipelineRouter(t *testing.T) (*Router, *retryRecordingSender, *mockEngine) {
	t.Helper()
	svc := newTestLedgerService(t)
	identity, err := NewValidatorIdentity("snoPBrXtMeMyMHUVTgbuqAfg1SUTb")
	require.NoError(t, err)
	rs := &retryRecordingSender{}
	a := New(Config{
		LedgerService: svc,
		Sender:        rs,
		Identity:      identity,
		Validators:    []consensus.NodeID{identity.NodeID},
	})
	eng := &mockEngine{}
	router := NewRouter(eng, a, make(chan *peermanagement.InboundMessage, 1))
	return router, rs, eng
}

// buildLargeTxSet builds a tx-set of n distinct synthetic tx blobs and
// returns its canonical ID plus a NodeID→wire-data index used to serve
// missing-node requests back to the requestor (the server half of an
// acquisition round-trip).
func buildLargeTxSet(t *testing.T, n int) (consensus.TxSetID, map[string][]byte) {
	t.Helper()
	blobs := make([][]byte, n)
	for i := range blobs {
		b := make([]byte, 16)
		binary.BigEndian.PutUint64(b[:8], uint64(i)+1)
		binary.BigEndian.PutUint64(b[8:], uint64(i)*2654435761+0x9e3779b9)
		blobs[i] = b
	}
	ts, err := NewTxSet(blobs)
	require.NoError(t, err)
	require.Equal(t, n, ts.Size(), "synthetic blobs must all be distinct")
	wire, err := ts.shamap().WalkWireNodes()
	require.NoError(t, err)
	byID := make(map[string][]byte, len(wire))
	for _, w := range wire {
		byID[string(w.NodeID)] = w.Data
	}
	return ts.ID(), byID
}

// TestTxSetAcquire_PipelinesOnProgressAtRTT is the core throughput fix: a
// large tx-set fetched in 256-node batches must re-request the next batch
// IMMEDIATELY on each progressing reply (rate-limited only by the RTT), not
// throttled to one request per MinInterval. It pins:
//
//	(a) each progressing batch pipelines exactly one more request, so the
//	    acquire completes in ~#batches requests despite a huge MinInterval;
//	(b) stallTicks stays 0 across a steadily-progressing acquire;
//	(c) a retryStalledTxSetAcquires tick mid-acquire does NOT delete the
//	    partial map (the timer skips an actively-progressing acquire).
//
// Mirrors rippled's TransactionAcquire::takeNodes → trigger(peer) on every
// onLedgerData.
func TestTxSetAcquire_PipelinesOnProgressAtRTT(t *testing.T) {
	router, rs, eng := newPipelineRouter(t)

	const leaves = 4000
	txSetID, byID := buildLargeTxSet(t, leaves)
	require.Greater(t, len(byID), 256, "fixture must need multiple 256-node batches")

	// serveMissing returns a liTS_CANDIDATE reply carrying up to max of the
	// requested node IDs, sourced from the canonical tx-set — the server
	// half of an acquisition round-trip.
	serveMissing := func(ids [][]byte, max int) *message.LedgerData {
		if len(ids) > max {
			ids = ids[:max]
		}
		nodes := make([]message.LedgerNode, 0, len(ids))
		for _, id := range ids {
			data, ok := byID[string(id)]
			require.Truef(t, ok, "requested node %x must exist in the source tx-set", id)
			nodes = append(nodes, message.LedgerNode{
				NodeID:   append([]byte(nil), id...),
				NodeData: append([]byte(nil), data...),
			})
		}
		return &message.LedgerData{
			InfoType:   message.LedgerInfoTsCandidate,
			LedgerHash: txSetID[:],
			Nodes:      nodes,
		}
	}

	missingIDs := func() [][]byte {
		router.txSetAcquireMu.Lock()
		defer router.txSetAcquireMu.Unlock()
		state, ok := router.txSetAcquire[txSetID]
		if !ok {
			return nil
		}
		return missingNodeIDs(state.txMap.GetMissingNodes(256, nil))
	}

	// stateSnapshot reads (stallTicks, tracked) under the lock.
	stateSnapshot := func() (int, bool) {
		router.txSetAcquireMu.Lock()
		defer router.txSetAcquireMu.Unlock()
		state, ok := router.txSetAcquire[txSetID]
		if !ok {
			return 0, false
		}
		return state.stallTicks, true
	}

	// doneSnapshot reports whether the acquire has completed. Completion now
	// latches the entry done and RETAINS it (stragglers are dropped) rather
	// than deleting it, so completion is observed via the done flag.
	doneSnapshot := func() bool {
		router.txSetAcquireMu.Lock()
		defer router.txSetAcquireMu.Unlock()
		state, ok := router.txSetAcquire[txSetID]
		return ok && state.done
	}

	// MinInterval is deliberately HUGE: if the inbound path still consulted
	// it, only the first batch would ever re-request and the acquire would
	// never complete. The pipelining model ignores it for progressing
	// replies. MaxStallTicks is small so any regression to timer-driven
	// give-up would surface as a premature dormancy.
	withRetryKnobs(router, time.Hour, 3, 1_000_000, func() {
		// Batch 0: the root. A progressing root reply pipelines exactly one
		// missing-nodes request immediately.
		rootID := make([]byte, 33)
		router.handleTxSetData(serveMissing([][]byte{rootID}, 256), 1)
		require.Equal(t, 1, rs.calledN(),
			"root reply must pipeline exactly one missing-nodes request")

		requestsAtLastProgress := 1
		firedMidTick := false
		for step := 0; step < 10*leaves; step++ {
			if doneSnapshot() { // acquire completed (retained as done)
				break
			}
			ids := missingIDs()
			require.NotEmpty(t, ids, "an incomplete tracked acquire must expose missing nodes")

			// (b) A steadily-progressing acquire never accrues stall ticks.
			st, tracked := stateSnapshot()
			require.True(t, tracked)
			require.Equalf(t, 0, st,
				"stallTicks must stay 0 while the acquire is steadily progressing (step %d)", step)

			// (c) A timer tick mid-acquire must NOT delete the partial map.
			// lastRequest is fresh (< MinInterval) so the timer skips this
			// actively-progressing acquire and the entry survives untouched.
			if !firedMidTick && step == 2 {
				before := rs.calledN()
				router.retryStalledTxSetAcquires()
				require.Equal(t, before, rs.calledN(),
					"timer must skip an actively-progressing acquire (fresh lastRequest)")
				stAfter, stillTracked := stateSnapshot()
				require.True(t, stillTracked,
					"the partial map must SURVIVE a retryStalledTxSetAcquires tick mid-acquire")
				require.Equal(t, 0, stAfter,
					"a skipped timer tick must not accrue stall ticks on a progressing acquire")
				firedMidTick = true
			}

			prev := rs.calledN()
			router.handleTxSetData(serveMissing(ids, 256), 1)

			// (a) Each progressing batch pipelines exactly one more request
			// IMMEDIATELY, unless it completed the tree (then no re-request).
			if !doneSnapshot() {
				require.Equal(t, prev+1, rs.calledN(),
					"each progressing batch must pipeline exactly one more request (no throttle)")
				requestsAtLastProgress = rs.calledN()
			}
		}

		// Completed: entry retained (done-latched), engine fed the full set once.
		require.True(t, doneSnapshot(), "acquire must complete (done-latched, entry retained)")
		require.True(t, firedMidTick, "the mid-acquire timer tick must have fired")

		eng.mu.Lock()
		gotSets := append([]consensus.TxSetID(nil), eng.txSets...)
		eng.mu.Unlock()
		require.Len(t, gotSets, 1, "the completed tx-set must reach the engine exactly once")
		require.Equal(t, txSetID, gotSets[0], "the completed set ID must match")

		// The acquire completed in ~#batches pipelined requests — far more
		// than the single request a MinInterval throttle would have allowed.
		require.Greater(t, requestsAtLastProgress, 5,
			"pipelining must issue one request per progressing batch, not throttle to one")
	})
}

// TestTxSetAcquire_DormantRetainsMapAndResumes pins property (d): after
// MaxStallTicks consecutive no-progress timer ticks the acquire goes dormant
// but its partial map is RETAINED (not deleted), and a subsequent
// MarkTxSetStillNeeded + progressing reply resumes it from that partial map.
// Mirrors rippled keeping mMap across retries and stillNeed() re-arming.
func TestTxSetAcquire_DormantRetainsMapAndResumes(t *testing.T) {
	router, rs, _ := newPipelineRouter(t)
	ld, txSetID := rootOnlyTxSetLedgerData(t, 8)

	const maxStall = 3
	withRetryKnobs(router, 0, maxStall, 1_000_000, func() {
		// Progress reply creates the acquire (root only → incomplete) and
		// pipelines one request.
		router.handleTxSetData(ld, 1)
		require.Equal(t, 1, rs.calledN())

		// No inbound progress arrives; consecutive timer ticks accrue stall
		// ticks until the acquire goes dormant at MaxStallTicks.
		for range maxStall {
			router.retryStalledTxSetAcquires()
		}

		router.txSetAcquireMu.Lock()
		state, tracked := router.txSetAcquire[txSetID]
		var dormant, resumable bool
		if tracked {
			dormant = state.dormant
			// Root present, children still missing → a resumable partial map.
			resumable = len(state.txMap.GetMissingNodes(256, nil)) > 0
		}
		router.txSetAcquireMu.Unlock()
		require.True(t, tracked, "dormant acquire must be RETAINED, not deleted")
		require.True(t, dormant, "acquire must be dormant after MaxStallTicks consecutive stall ticks")
		require.True(t, resumable, "the partial SHAMap must be retained for resume")

		// While dormant, further timer ticks must not re-request.
		nAtDormant := rs.calledN()
		router.retryStalledTxSetAcquires()
		require.Equal(t, nAtDormant, rs.calledN(), "a dormant acquire must not re-request")

		// Re-arm: consensus re-asks (MarkTxSetStillNeeded) AND a progressing
		// reply arrives. Together they must wake the acquire and resume
		// pipelining from the retained partial map.
		router.MarkTxSetStillNeeded(txSetID)
		router.handleTxSetData(ld, 1)
		require.Greater(t, rs.calledN(), nAtDormant,
			"a re-armed dormant acquire must resume requesting from its retained partial map")

		router.txSetAcquireMu.Lock()
		state, tracked = router.txSetAcquire[txSetID]
		stillDormant := tracked && state.dormant
		stall := 0
		if tracked {
			stall = state.stallTicks
		}
		router.txSetAcquireMu.Unlock()
		require.True(t, tracked)
		require.False(t, stillDormant, "resuming must clear the dormant latch")
		require.Equal(t, 0, stall, "resuming must reset the stall counter")
	})
}
