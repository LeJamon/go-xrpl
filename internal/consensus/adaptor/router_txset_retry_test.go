package adaptor

import (
	"maps"
	"sync"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// retryRecordingSender captures every tx-set missing-nodes request — the
// broadcast RequestTxSetMissingNodes (timer path) AND the unicast
// RequestTxSetMissingNodesFromPeer (inbound pipeline) — into a single ordered
// slice so tests can pin throttle, give-up, de-prioritization, and the
// unicast-inline / broadcast-timer split. peerID is 0 for a broadcast and the
// target peer for a unicast; excluded is populated only on broadcasts. Other
// NetworkSender methods inherit from noopSender.
type retryRecordingSender struct {
	noopSender
	mu        sync.Mutex
	calls     []retryRecordedCall
	returnErr error
}

type retryRecordedCall struct {
	txSetID  consensus.TxSetID
	nodeIDs  [][]byte
	excluded map[uint64]bool
	indirect bool
	peerID   uint64
}

func (s *retryRecordingSender) RequestTxSetMissingNodes(id consensus.TxSetID, nodeIDs [][]byte, excluded map[uint64]bool, indirect bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	copyExcluded := map[uint64]bool{}
	maps.Copy(copyExcluded, excluded)
	copyIDs := make([][]byte, len(nodeIDs))
	for i, n := range nodeIDs {
		copyIDs[i] = append([]byte(nil), n...)
	}
	s.calls = append(s.calls, retryRecordedCall{
		txSetID:  id,
		nodeIDs:  copyIDs,
		excluded: copyExcluded,
		indirect: indirect,
	})
	return s.returnErr
}

func (s *retryRecordingSender) RequestTxSetMissingNodesFromPeer(id consensus.TxSetID, nodeIDs [][]byte, peerID uint64, indirect bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	copyIDs := make([][]byte, len(nodeIDs))
	for i, n := range nodeIDs {
		copyIDs[i] = append([]byte(nil), n...)
	}
	s.calls = append(s.calls, retryRecordedCall{
		txSetID:  id,
		nodeIDs:  copyIDs,
		indirect: indirect,
		peerID:   peerID,
	})
	return s.returnErr
}

func (s *retryRecordingSender) calledN() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

func (s *retryRecordingSender) lastCall() retryRecordedCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.calls) == 0 {
		return retryRecordedCall{}
	}
	return s.calls[len(s.calls)-1]
}

func (s *retryRecordingSender) callAt(i int) retryRecordedCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls[i]
}

// newRetryRouter wires a Router whose NetworkSender records every
// RequestTxSetMissingNodes call. The router is NOT started — tests
// invoke handleTxSetData directly so timings are deterministic.
func newRetryRouter(t *testing.T) (*Router, *retryRecordingSender) {
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
	router := NewRouter(&mockEngine{}, a, make(chan *peermanagement.InboundMessage, 1))
	return router, rs
}

// rootOnlyTxSetLedgerData returns a TMLedgerData carrying ONLY the
// SHAMap root node of a tx-set whose remaining nodes are NOT included.
// Feeding this to handleTxSetData adds the root and leaves the SHAMap
// incomplete, which forces the FinishSync-fails-then-retry branch —
// the exact code path issue #420 targets. AddRootNode succeeds so this
// reply counts as "progress" per the rippled-aligned takeNodes
// semantics (TransactionAcquire.cpp:194-226).
func rootOnlyTxSetLedgerData(t *testing.T, leafCount int) (*message.LedgerData, consensus.TxSetID) {
	t.Helper()
	_, txSetID, wireNodes := buildTxSetForTest(t, leafCount)
	require.Greater(t, len(wireNodes), 1, "tx-set must have non-root nodes so the consumer enters the retry branch")
	rootNode := wireNodes[0]
	ld := &message.LedgerData{
		InfoType:   message.LedgerInfoTsCandidate,
		LedgerHash: txSetID[:],
		Nodes: []message.LedgerNode{
			{NodeID: rootNode.NodeID, NodeData: rootNode.Data},
		},
	}
	return ld, consensus.TxSetID(txSetID)
}

// emptyTxSetLedgerData returns a TMLedgerData for txSetID carrying no
// nodes at all. Used to drive the non-progress branch once state
// already exists from a prior reply: rootAccepted stays false and
// added stays 0, so the peer's non-progress counter increments.
func emptyTxSetLedgerData(txSetID consensus.TxSetID) *message.LedgerData {
	return &message.LedgerData{
		InfoType:   message.LedgerInfoTsCandidate,
		LedgerHash: txSetID[:],
		Nodes:      nil,
	}
}

// withRetryKnobs overrides this router's tx-set acquire retry knobs for
// the duration of fn so tests run instantly instead of waiting for the
// production 250 ms cadence window. Restores prior values on return.
func withRetryKnobs(router *Router, minInterval time.Duration, maxStallTicks, peerThreshold int, fn func()) {
	prev := router.txSetRetryKnobs
	router.SetTxSetRetryKnobsForTest(txSetRetryKnobs{
		MinInterval:              minInterval,
		MaxStallTicks:            maxStallTicks,
		PeerNonProgressThreshold: peerThreshold,
	})
	defer router.SetTxSetRetryKnobsForTest(prev)
	fn()
}

// TestTxSetRetry_NoProgressReplyDefersToTimer pins the pipelining-model
// anti-storm guard. A PROGRESSING reply re-requests immediately (RTT is
// the rate limiter), but a NON-progress reply must NOT re-request inline —
// otherwise a junk/empty-reply peer amplifies into a broadcast storm. The
// 250ms stall timer, not the inbound path, drives re-requests once a peer
// goes quiet.
func TestTxSetRetry_NoProgressReplyDefersToTimer(t *testing.T) {
	router, rs := newRetryRouter(t)
	withRetryKnobs(router, 0, 1_000_000, 1_000_000, func() {
		ld, txSetID := rootOnlyTxSetLedgerData(t, 4)

		// A progressing (root) reply pipelines exactly one request.
		router.handleTxSetData(ld, 1)
		require.Equal(t, 1, rs.calledN(),
			"a progressing reply must pipeline exactly one missing-nodes request")

		// An empty reply makes no progress → must NOT re-request inline.
		router.handleTxSetData(emptyTxSetLedgerData(txSetID), 1)
		assert.Equal(t, 1, rs.calledN(),
			"a non-progress reply must defer to the stall timer, not re-request inline")
	})
}

// TestTxSetRetry_InboundProgressNeverGivesUp pins that the inbound path
// has NO give-up cap: give-up now lives solely on the stall timer, keyed
// on consecutive no-progress ticks. Many progressing replies in a row each
// pipeline a fresh request and never delete the acquire nor accrue stall
// ticks — the pre-pipelining inbound MaxAttempts delete-on-cap is gone.
func TestTxSetRetry_InboundProgressNeverGivesUp(t *testing.T) {
	router, rs := newRetryRouter(t)
	// MaxStallTicks deliberately small: if the inbound path fed stall
	// telemetry it would trip and delete the acquire.
	withRetryKnobs(router, 0, 3, 1_000_000, func() {
		ld, txSetID := rootOnlyTxSetLedgerData(t, 4)

		const replies = 25
		for i := range replies {
			router.handleTxSetData(ld, uint64(i+1))
		}
		require.Equal(t, replies, rs.calledN(),
			"every progressing inbound reply must pipeline a request — no inbound give-up cap")

		router.txSetAcquireMu.Lock()
		state, tracked := router.txSetAcquire[txSetID]
		stall, dormant := 0, false
		if tracked {
			stall, dormant = state.stallTicks, state.dormant
		}
		router.txSetAcquireMu.Unlock()
		require.True(t, tracked, "a progressing acquire is never deleted by the inbound path")
		assert.Equal(t, 0, stall, "progressing replies keep stallTicks pinned at 0")
		assert.False(t, dormant, "a progressing acquire never goes dormant")
	})
}

// TestTxSetRetry_DeprioritizesNonProgressingPeer pins per-peer
// exclusion. A peer that returns PeerNonProgressThreshold
// non-progressing replies in a row is dropped from the next
// missing-nodes broadcast via the excluded map. Under the pipelining
// model, non-progress replies no longer re-request inline (they defer
// to the timer), so the exclusion surfaces on the timer's re-request.
// Non-progress matches rippled's takeNodes invalid() branch: a reply
// that adds neither root nor non-root nodes.
func TestTxSetRetry_DeprioritizesNonProgressingPeer(t *testing.T) {
	router, rs := newRetryRouter(t)
	const threshold = 2
	withRetryKnobs(router, 0, 1_000_000, threshold, func() {
		ld, txSetID := rootOnlyTxSetLedgerData(t, 4)
		noProgressLD := emptyTxSetLedgerData(txSetID)
		const badPeer = uint64(7)

		// Root-only reply creates state and pipelines one request (root add
		// counts as progress, so the per-peer counter stays at 0).
		router.handleTxSetData(ld, badPeer)
		require.Equal(t, 1, rs.calledN())
		assert.Empty(t, rs.lastCall().excluded,
			"first broadcast must carry no exclusions")

		// Two non-progress replies bump counter[badPeer] to threshold.
		// Neither re-requests inline (deferred to the timer).
		router.handleTxSetData(noProgressLD, badPeer)
		router.handleTxSetData(noProgressLD, badPeer)
		require.Equal(t, 1, rs.calledN(),
			"non-progress replies must not re-request inline")

		// The stall timer now re-requests and must route around badPeer.
		router.retryStalledTxSetAcquires()
		require.Equal(t, 2, rs.calledN(), "timer re-requests once the peer goes quiet")
		require.NotNil(t, rs.lastCall().excluded)
		assert.Truef(t, rs.lastCall().excluded[badPeer],
			"peer %d should be excluded once non-progress count reaches threshold (%d)",
			badPeer, threshold)
	})
}

// TestTxSetRetry_ProgressResetsNonProgressCounter pins that a peer
// reply that DOES extend the SHAMap resets that peer's non-progress
// counter, so a transient stretch of empty replies doesn't permanently
// banish a recovered peer. The exclusion decision is observed on the
// timer's re-request (the inbound non-progress path no longer broadcasts).
func TestTxSetRetry_ProgressResetsNonProgressCounter(t *testing.T) {
	router, rs := newRetryRouter(t)
	const threshold = 2
	withRetryKnobs(router, 0, 1_000_000, threshold, func() {
		ld, txSetID := rootOnlyTxSetLedgerData(t, 4)
		noProgressLD := emptyTxSetLedgerData(txSetID)
		const peer = uint64(11)

		// Root reply creates state (progress).
		router.handleTxSetData(ld, peer)
		require.Equal(t, 1, rs.calledN())

		// Non-progress reply — counter[peer] = 1.
		router.handleTxSetData(noProgressLD, peer)

		// Progress reply: same root (ErrRootAlreadySet still counts as
		// rootAccepted=true) — resets counter[peer] to 0 and pipelines
		// another request.
		router.handleTxSetData(ld, peer)
		require.Equal(t, 2, rs.calledN())

		// One further non-progress reply — counter[peer] = 1 again (< threshold).
		router.handleTxSetData(noProgressLD, peer)

		// The timer re-requests; peer must NOT be excluded (1 < 2), proving
		// the intervening progress reply reset the counter.
		router.retryStalledTxSetAcquires()
		require.Equal(t, 3, rs.calledN())
		assert.Falsef(t, rs.lastCall().excluded[peer],
			"progress reset the non-progress counter; one further empty reply "+
				"should not be enough to exclude the peer")
	})
}

// TestTxSetRetry_BadNonRootInvalidatesWholeReply pins the
// rippled-faithful takeNodes useful() semantics (M1 from PR #454
// review). A reply that adds a valid root but ALSO carries any
// non-root node that fails to parse / be added must NOT count as
// progress for the peer that sent it — mirrors rippled's
// TransactionAcquire.cpp:217-220 where one bad non-root short-circuits
// the whole reply to invalid() and the caller charges feeUselessData
// (InboundTransactions.cpp:177-178). Without this, a peer trickling
// one valid node alongside junk pins its non-progress counter at zero
// forever and is never de-prioritized by the per-peer threshold.
func TestTxSetRetry_BadNonRootInvalidatesWholeReply(t *testing.T) {
	router, rs := newRetryRouter(t)
	const threshold = 2
	withRetryKnobs(router, 0, 1_000_000, threshold, func() {
		_, txSetID, wireNodes := buildTxSetForTest(t, 4)
		rootNode := wireNodes[0]
		require.Greater(t, len(wireNodes), 1)

		// rootPlusJunkLD: valid root + a non-root NodeID with garbage
		// data so AddKnownNodeByID fails. The non-root NodeID itself is
		// well-formed (33 bytes, non-zero depth) so it survives the
		// UnmarshalBinary check; the failure is on the data side.
		nonRootID := wireNodes[1].NodeID
		rootPlusJunkLD := &message.LedgerData{
			InfoType:   message.LedgerInfoTsCandidate,
			LedgerHash: txSetID[:],
			Nodes: []message.LedgerNode{
				{NodeID: rootNode.NodeID, NodeData: rootNode.Data},
				{NodeID: nonRootID, NodeData: []byte{0xde, 0xad, 0xbe, 0xef}},
			},
		}
		const badPeer = uint64(42)

		// Reply 1: root accepted but junk non-root → replyValid=false → the
		// whole reply is non-progress. State is created (root added) but no
		// inline re-request; counter[badPeer] = 1.
		router.handleTxSetData(rootPlusJunkLD, badPeer)
		require.Equal(t, 0, rs.calledN(),
			"a junk-non-root reply is non-progress → no inline re-request")

		// Reply 2: same shape → counter[badPeer] = 2 (== threshold).
		router.handleTxSetData(rootPlusJunkLD, badPeer)
		require.Equal(t, 0, rs.calledN())

		// The stall timer now re-requests and must exclude badPeer: the junk
		// non-root invalidated the whole reply, so the root-add alone did NOT
		// keep the counter pinned at zero.
		router.retryStalledTxSetAcquires()
		require.Equal(t, 1, rs.calledN())
		require.NotNil(t, rs.lastCall().excluded,
			"timer re-request after two bad-non-root replies must exclude the peer")
		assert.Truef(t, rs.lastCall().excluded[badPeer],
			"peer %d should be excluded — junk non-root must invalidate the whole reply, "+
				"NOT count as progress just because the root was accepted",
			badPeer)
	})
}

// TestTxSetRetry_StillNeededReArmsDormantAcquire pins the stillNeed
// re-arm at the new give-up boundary. Once the stall timer drives an
// acquire dormant (MaxStallTicks consecutive no-progress ticks), it stops
// re-requesting but KEEPS its partial map. A consensus re-ask
// (Adaptor.RequestTxSet → MarkTxSetStillNeeded) must clear the dormant
// latch so the very next timer tick resumes requesting instead of waiting
// out the 60s TTL — mirrors rippled's stillNeed reset.
func TestTxSetRetry_StillNeededReArmsDormantAcquire(t *testing.T) {
	router, rs := newRetryRouter(t)
	const maxStall = 2
	withRetryKnobs(router, 0, maxStall, 1_000_000, func() {
		ld, txSetID := rootOnlyTxSetLedgerData(t, 4)

		// Progress reply creates the acquire and pipelines one request.
		router.handleTxSetData(ld, 1)
		require.Equal(t, 1, rs.calledN())

		// Drive consecutive no-progress timer ticks to dormancy.
		for range maxStall {
			router.retryStalledTxSetAcquires()
		}
		router.txSetAcquireMu.Lock()
		state, tracked := router.txSetAcquire[txSetID]
		dormant := tracked && state.dormant
		router.txSetAcquireMu.Unlock()
		require.True(t, tracked, "dormant acquire keeps its partial map (not deleted)")
		require.True(t, dormant, "acquire must be dormant after MaxStallTicks stall ticks")

		nAtDormant := rs.calledN()
		// While dormant a further timer tick must not re-request.
		router.retryStalledTxSetAcquires()
		require.Equal(t, nAtDormant, rs.calledN(), "dormant acquire must not re-request")

		// stillNeed re-arm via consensus re-ask, then a timer tick resumes.
		require.NoError(t, router.adaptor.RequestTxSet(txSetID))
		router.retryStalledTxSetAcquires()
		assert.Greater(t, rs.calledN(), nAtDormant,
			"after stillNeed re-arm the next timer tick must resume requesting "+
				"instead of being silenced until the TTL sweep")
	})
}

// TestTxSetRetry_StillNeededNoOpOnUnknownTxSet pins that the
// stillNeed hook is a safe no-op when no acquisition is in flight —
// matching rippled's getSet path where the stillNeed call is gated on
// `it->second.mAcquire` being live (InboundTransactions.cpp:110-113).
func TestTxSetRetry_StillNeededNoOpOnUnknownTxSet(t *testing.T) {
	router, _ := newRetryRouter(t)
	var unknownID consensus.TxSetID
	unknownID[0] = 0xab

	// Must not panic, must not allocate state, must not broadcast.
	router.MarkTxSetStillNeeded(unknownID)

	router.txSetAcquireMu.Lock()
	_, exists := router.txSetAcquire[unknownID]
	router.txSetAcquireMu.Unlock()
	assert.False(t, exists, "MarkTxSetStillNeeded must not allocate state for unknown tx-sets")
}

// TestTxSetRetry_QueryTypeEscalation pins issue #977's requester-side
// escalation for tx-set acquisition: the first (inbound-driven)
// missing-nodes request is direct (query_type absent, non-relayable),
// while requests issued after the stall timer fires carry indirect=true
// (query_type=qtINDIRECT) so peers relay them on our behalf. Once the
// acquisition has timed out, the latch holds: later inbound-driven
// requests stay indirect too — mirroring rippled's TransactionAcquire
// timeouts_ != 0 gate (TransactionAcquire.cpp:133,164), which is a
// per-acquisition latch, not a per-request flag.
func TestTxSetRetry_QueryTypeEscalation(t *testing.T) {
	router, rs := newRetryRouter(t)
	// MinInterval=0 so neither the inbound throttle nor the timer skip
	// fires; high attempt cap so nothing is dropped mid-test.
	withRetryKnobs(router, 0, 1_000_000, 1_000_000, func() {
		ld, _ := rootOnlyTxSetLedgerData(t, 4)

		// First attempt is inbound-driven, before any timeout → direct.
		router.handleTxSetData(ld, 1)
		require.Equal(t, 1, rs.calledN(), "first reply must broadcast once")
		assert.False(t, rs.callAt(0).indirect,
			"first-attempt request must NOT carry query_type (directly routed)")

		// The stall timer firing IS the timeout signal → indirect, and it
		// latches the acquisition into the aggressive (relayable) mode.
		router.retryStalledTxSetAcquires()
		require.Equal(t, 2, rs.calledN(), "stall timer must re-request missing nodes")
		assert.True(t, rs.callAt(1).indirect,
			"timer re-trigger is post-stall, so its request must carry query_type=qtINDIRECT")

		// Latch: a further inbound-driven request after the timeout stays
		// indirect (rippled re-triggers with query_type set on every reply
		// once timeouts_ != 0, not just on the timeout itself).
		router.handleTxSetData(ld, 1)
		require.Equal(t, 3, rs.calledN())
		assert.True(t, rs.callAt(2).indirect,
			"after the acquisition has timed out once, subsequent requests stay indirect (latch)")
	})
}
