package adaptor

import (
	"sync"
	"testing"
	"time"

	"github.com/LeJamon/goXRPLd/internal/consensus"
	"github.com/LeJamon/goXRPLd/internal/peermanagement"
	"github.com/LeJamon/goXRPLd/internal/peermanagement/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// retryRecordingSender captures the per-call peer-exclusion set passed
// to RequestTxSetMissingNodes so tests can pin issue #420's throttle,
// max-attempts, and de-prioritization behavior. Other NetworkSender
// methods inherit from noopSender.
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
}

func (s *retryRecordingSender) RequestTxSetMissingNodes(id consensus.TxSetID, nodeIDs [][]byte, excluded map[uint64]bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	copyExcluded := map[uint64]bool{}
	for k, v := range excluded {
		copyExcluded[k] = v
	}
	copyIDs := make([][]byte, len(nodeIDs))
	for i, n := range nodeIDs {
		copyIDs[i] = append([]byte(nil), n...)
	}
	s.calls = append(s.calls, retryRecordedCall{
		txSetID:  id,
		nodeIDs:  copyIDs,
		excluded: copyExcluded,
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
	router := NewRouter(&mockEngine{}, a, nil, make(chan *peermanagement.InboundMessage, 1))
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

// withRetryKnobs overrides this router's issue-#420 retry knobs for
// the duration of fn so tests run instantly instead of waiting for the
// production 250 ms throttle window. Restores prior values on return.
func withRetryKnobs(router *Router, minInterval time.Duration, maxAttempts, peerThreshold int, fn func()) {
	prev := router.txSetRetryKnobs
	router.SetTxSetRetryKnobsForTest(txSetRetryKnobs{
		MinInterval:              minInterval,
		MaxAttempts:              maxAttempts,
		PeerNonProgressThreshold: peerThreshold,
	})
	defer router.SetTxSetRetryKnobsForTest(prev)
	fn()
}

// TestTxSetRetry_ThrottleSkipsRapidRetries pins the rate limiter
// (issue #420 item 2a). A peer that replies twice in quick succession
// must not produce two broadcasts — the second falls inside the
// minimum-interval window and is dropped. Without the throttle, every
// non-progressing TMLedgerData spawns an immediate re-broadcast,
// driving the 100+ retries/sec storm captured in the issue.
func TestTxSetRetry_ThrottleSkipsRapidRetries(t *testing.T) {
	router, rs := newRetryRouter(t)
	withRetryKnobs(router, time.Hour, 100, 100, func() {
		ld, _ := rootOnlyTxSetLedgerData(t, 4)

		router.handleTxSetData(ld, 1)
		require.Equal(t, 1, rs.calledN(),
			"first reply must trigger exactly one missing-nodes broadcast")

		router.handleTxSetData(ld, 1)
		assert.Equal(t, 1, rs.calledN(),
			"second reply inside the throttle window must NOT trigger another broadcast — "+
				"issue #420: without this, every non-progressing reply re-broadcasts immediately")
	})
}

// TestTxSetRetry_MaxAttemptsCapDropsAcquire pins the give-up condition
// (issue #420 item 2b). After MaxAttempts broadcasts the acquisition
// is dropped: the cap-hit reply does NOT broadcast and the entry is
// deleted. A later reply for the same tx-set ID re-arms a fresh
// acquire — mirrors rippled's stillNeed reset path
// (TransactionAcquire.cpp:256-264) so consensus oscillating back to
// the same set isn't silenced for the full TTL window.
func TestTxSetRetry_MaxAttemptsCapDropsAcquire(t *testing.T) {
	router, rs := newRetryRouter(t)
	const maxAttempts = 3
	withRetryKnobs(router, 0, maxAttempts, 1_000_000, func() {
		ld, _ := rootOnlyTxSetLedgerData(t, 4)

		for i := 0; i < maxAttempts; i++ {
			router.handleTxSetData(ld, uint64(i+1))
		}
		require.Equal(t, maxAttempts, rs.calledN(),
			"each of the first maxAttempts replies must broadcast")

		// Reply at the cap: hits the delete-on-cap branch — no broadcast.
		router.handleTxSetData(ld, 99)
		require.Equal(t, maxAttempts, rs.calledN(),
			"reply that hits the cap must NOT broadcast (delete-on-cap)")

		// Subsequent reply re-creates state and broadcasts a fresh attempt.
		router.handleTxSetData(ld, 100)
		assert.Equal(t, maxAttempts+1, rs.calledN(),
			"after the entry was dropped, the next reply must start a fresh acquire "+
				"and broadcast again — matches rippled's stillNeed re-arm path")
	})
}

// TestTxSetRetry_DeprioritizesNonProgressingPeer pins per-peer
// exclusion (issue #420 item 2c). A peer that returns
// PeerNonProgressThreshold non-progressing replies in a row is
// dropped from the next missing-nodes broadcast via the excluded
// map. Non-progress matches rippled's takeNodes invalid() branch:
// a reply that adds neither root nor non-root nodes.
func TestTxSetRetry_DeprioritizesNonProgressingPeer(t *testing.T) {
	router, rs := newRetryRouter(t)
	const threshold = 2
	withRetryKnobs(router, 0, 1_000_000, threshold, func() {
		ld, txSetID := rootOnlyTxSetLedgerData(t, 4)
		noProgressLD := emptyTxSetLedgerData(txSetID)
		const badPeer = uint64(7)

		// Setup: root-only reply creates state. Root-add counts as
		// progress (TransactionAcquire.cpp:194-226 useful() branch),
		// so the per-peer counter stays at 0.
		router.handleTxSetData(ld, badPeer)
		require.Equal(t, 1, rs.calledN())
		assert.Empty(t, rs.lastCall().excluded,
			"first broadcast must carry no exclusions")

		// Non-progress reply 1 — counter[badPeer] = 1 (< threshold).
		router.handleTxSetData(noProgressLD, badPeer)
		require.Equal(t, 2, rs.calledN())
		assert.Empty(t, rs.lastCall().excluded,
			"counter below threshold must not yet exclude the peer")

		// Non-progress reply 2 — counter[badPeer] = 2 (== threshold) →
		// the broadcast for this retry must exclude badPeer.
		router.handleTxSetData(noProgressLD, badPeer)
		require.Equal(t, 3, rs.calledN())
		require.NotNil(t, rs.lastCall().excluded)
		assert.Truef(t, rs.lastCall().excluded[badPeer],
			"peer %d should be excluded once non-progress count reaches threshold (%d)",
			badPeer, threshold)
	})
}

// TestTxSetRetry_ProgressResetsNonProgressCounter pins that a peer
// reply that DOES extend the SHAMap resets that peer's non-progress
// counter, so a transient stretch of empty replies doesn't permanently
// banish a recovered peer.
func TestTxSetRetry_ProgressResetsNonProgressCounter(t *testing.T) {
	router, rs := newRetryRouter(t)
	const threshold = 2
	withRetryKnobs(router, 0, 1_000_000, threshold, func() {
		ld, txSetID := rootOnlyTxSetLedgerData(t, 4)
		noProgressLD := emptyTxSetLedgerData(txSetID)
		const peer = uint64(11)

		// Setup: root reply creates state (counts as progress).
		router.handleTxSetData(ld, peer)
		require.Equal(t, 1, rs.calledN())

		// Non-progress reply — counter[peer] = 1.
		router.handleTxSetData(noProgressLD, peer)
		require.Equal(t, 2, rs.calledN())

		// Progress reply: same root (ErrRootAlreadySet still counts as
		// rootAccepted=true per takeNodes useful() semantics) — resets
		// counter[peer] to 0.
		router.handleTxSetData(ld, peer)
		require.Equal(t, 3, rs.calledN())

		// One further non-progress reply — counter[peer] = 1 again,
		// still < threshold=2, peer must NOT be excluded.
		router.handleTxSetData(noProgressLD, peer)
		last := rs.lastCall()
		assert.Falsef(t, last.excluded[peer],
			"progress reset the non-progress counter; one further empty reply "+
				"should not be enough to exclude the peer")
	})
}
