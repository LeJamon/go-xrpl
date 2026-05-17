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
// to RequestTxSetMissingNodesExcept so tests can pin issue #420's
// throttle, max-attempts, and de-prioritization behavior. Other
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
}

func (s *retryRecordingSender) RequestTxSetMissingNodes(id consensus.TxSetID, nodeIDs [][]byte) error {
	return s.RequestTxSetMissingNodesExcept(id, nodeIDs, nil)
}

func (s *retryRecordingSender) RequestTxSetMissingNodesExcept(id consensus.TxSetID, nodeIDs [][]byte, excluded map[uint64]bool) error {
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
// RequestTxSetMissingNodesExcept call. The router is NOT started — tests
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

// partialTxSetLedgerData returns a TMLedgerData carrying ONLY the
// SHAMap root node of a tx-set whose remaining nodes are NOT included.
// Feeding this to handleTxSetData adds the root and leaves the SHAMap
// incomplete, which forces the FinishSync-fails-then-retry branch —
// the exact code path issue #420 targets.
func partialTxSetLedgerData(t *testing.T, leafCount int) (*message.LedgerData, consensus.TxSetID) {
	t.Helper()
	txMap, txSetID, wireNodes := buildTxSetForTest(t, leafCount)
	_ = txMap
	require.NotEmpty(t, wireNodes, "tx-set must have at least a root")
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

// withRetryKnobs temporarily overrides the issue-#420 retry knobs for
// the duration of fn so tests run instantly instead of waiting for the
// production 250 ms throttle window. Restores prior values on return.
func withRetryKnobs(minInterval time.Duration, maxAttempts, peerThreshold int, fn func()) {
	prevInterval, prevMax, prevThreshold := txSetRetryKnobsForTest()
	setTxSetRetryKnobsForTest(minInterval, maxAttempts, peerThreshold)
	defer setTxSetRetryKnobsForTest(prevInterval, prevMax, prevThreshold)
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
	withRetryKnobs(time.Hour, 100, 100, func() {
		ld, _ := partialTxSetLedgerData(t, 4)

		router.handleTxSetData(ld, 1)
		require.Equal(t, 1, rs.calledN(),
			"first reply must trigger exactly one missing-nodes broadcast")

		router.handleTxSetData(ld, 1)
		assert.Equal(t, 1, rs.calledN(),
			"second reply inside the throttle window must NOT trigger another broadcast — "+
				"issue #420: without this, every non-progressing reply re-broadcasts immediately")
	})
}

// TestTxSetRetry_MaxAttemptsCapMarksFailed pins the give-up condition
// (issue #420 item 2b). After txSetMaxAttempts broadcasts the
// acquisition is marked failed; further replies are ignored and no
// additional broadcast fires. Without this cap, a stuck acquisition
// loops forever until the 60s TTL sweep clears it.
func TestTxSetRetry_MaxAttemptsCapMarksFailed(t *testing.T) {
	router, rs := newRetryRouter(t)
	const maxAttempts = 3
	withRetryKnobs(0, maxAttempts, 1_000_000, func() {
		ld, _ := partialTxSetLedgerData(t, 4)

		for i := 0; i < maxAttempts; i++ {
			router.handleTxSetData(ld, uint64(i+1))
		}
		require.Equal(t, maxAttempts, rs.calledN(),
			"each of the first maxAttempts replies must broadcast")

		router.handleTxSetData(ld, 99)
		assert.Equal(t, maxAttempts, rs.calledN(),
			"reply past the cap must NOT broadcast — acquisition is now failed")

		router.handleTxSetData(ld, 100)
		assert.Equal(t, maxAttempts, rs.calledN(),
			"failed acquisitions stay quiescent across further replies")
	})
}

// TestTxSetRetry_DeprioritizesNonProgressingPeer pins per-peer
// exclusion (issue #420 item 2c). A peer that returns
// txSetPeerNonProgressThreshold non-progressing replies in a row
// is dropped from the next missing-nodes broadcast via the excluded
// map. The first broadcast carries an empty exclusion set; once the
// per-peer counter crosses the threshold the next broadcast excludes
// that peer.
func TestTxSetRetry_DeprioritizesNonProgressingPeer(t *testing.T) {
	router, rs := newRetryRouter(t)
	const threshold = 2
	withRetryKnobs(0, 1_000_000, threshold, func() {
		ld, _ := partialTxSetLedgerData(t, 4)
		const badPeer = uint64(7)

		// Reply 1 — non-progress count for badPeer becomes 1 (< threshold).
		router.handleTxSetData(ld, badPeer)
		require.Equal(t, 1, rs.calledN())
		first := rs.lastCall()
		assert.Empty(t, first.excluded,
			"first non-progress reply must not yet exclude the peer (count < threshold)")

		// Reply 2 — non-progress count for badPeer becomes 2 (== threshold) →
		// the broadcast for this retry must exclude badPeer.
		router.handleTxSetData(ld, badPeer)
		require.Equal(t, 2, rs.calledN())
		second := rs.lastCall()
		require.NotNil(t, second.excluded)
		assert.Truef(t, second.excluded[badPeer],
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
	withRetryKnobs(0, 1_000_000, threshold, func() {
		ldEmpty, txSetID := partialTxSetLedgerData(t, 4)
		const peer = uint64(11)

		// One non-progress reply.
		router.handleTxSetData(ldEmpty, peer)
		require.Equal(t, 1, rs.calledN())

		// A progressing reply: include a non-root inner node so `added` > 0.
		_, _, wireNodes := buildTxSetForTest(t, 4)
		require.GreaterOrEqual(t, len(wireNodes), 2)
		progressLD := &message.LedgerData{
			InfoType:   message.LedgerInfoTsCandidate,
			LedgerHash: txSetID[:],
			Nodes: []message.LedgerNode{
				{NodeID: wireNodes[0].NodeID, NodeData: wireNodes[0].Data},
				{NodeID: wireNodes[1].NodeID, NodeData: wireNodes[1].Data},
			},
		}
		router.handleTxSetData(progressLD, peer)

		// Now a single more non-progress reply: counter is at 1, NOT yet
		// at threshold → peer must NOT appear in the exclusion set.
		router.handleTxSetData(ldEmpty, peer)
		last := rs.lastCall()
		assert.Falsef(t, last.excluded[peer],
			"progress reset the non-progress counter; one further empty reply "+
				"should not be enough to exclude the peer")
	})
}
