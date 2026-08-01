package adaptor

import (
	"errors"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type proposalAdmissionEngine struct {
	mockEngine
	err   error
	calls int
}

func (e *proposalAdmissionEngine) OnProposal(*consensus.Proposal, uint64) error {
	e.calls++
	return e.err
}

// TestMessageSuppression_ObserveReturnsLastSeen pins R5.7: observe()
// must return both first-seen and the prior observation time so the
// router can gate UpdateRelaySlot on the IDLED window.
func TestMessageSuppression_ObserveReturnsLastSeen(t *testing.T) {
	var clockNS int64
	baseTime := time.Unix(1_700_000_000, 0)
	clockNS = baseTime.UnixNano()

	s := newMessageSuppression(30*time.Second, 64)
	s.now = func() time.Time { return time.Unix(0, clockNS) }

	hash := [32]byte{0xAA}

	// First observation: firstSeen=true, lastSeenAt=zero.
	firstSeen, lastSeen := s.observe(hash)
	assert.True(t, firstSeen, "first observation must be marked first-seen")
	assert.True(t, lastSeen.IsZero(),
		"first observation must return zero lastSeenAt")

	// Advance the clock by 2 seconds and observe again: duplicate,
	// lastSeenAt must reflect the first observation's timestamp.
	clockNS = baseTime.Add(2 * time.Second).UnixNano()
	firstSeen, lastSeen = s.observe(hash)
	assert.False(t, firstSeen, "second observation must be a duplicate")
	assert.Equal(t, baseTime.UnixNano(), lastSeen.UnixNano(),
		"lastSeenAt must be the prior observation's timestamp")

	// The previous duplicate refreshed the entry to t=2s. A third
	// observation at t=3s should see lastSeenAt=2s.
	clockNS = baseTime.Add(3 * time.Second).UnixNano()
	firstSeen, lastSeen = s.observe(hash)
	assert.False(t, firstSeen)
	assert.Equal(t, baseTime.Add(2*time.Second).UnixNano(), lastSeen.UnixNano(),
		"lastSeenAt must be refreshed on each duplicate (sliding window)")

	// Expire the entry by advancing past the TTL: should re-report
	// first-seen.
	clockNS = baseTime.Add(40 * time.Second).UnixNano()
	firstSeen, lastSeen = s.observe(hash)
	assert.True(t, firstSeen, "beyond TTL, observation must be marked first-seen again")
	assert.True(t, lastSeen.IsZero(),
		"TTL-expired re-observation must return zero lastSeenAt")
}

// TestMessageSuppression_RecordPeerAndHasHash pins the per-hash peer
// set semantics that drive validator-list broadcast suppression:
// recordPeer adds the peer to the set, peerHasHash reflects it, and a
// repeated recordPeer for the same (hash, peer) is reported as not
// newly-added. Mirrors rippled HashRouter::addSuppressionPeer's
// peer-set extension behaviour at HashRouter.cpp:51-79.
func TestMessageSuppression_RecordPeerAndHasHash(t *testing.T) {
	s := newMessageSuppression(30*time.Second, 64)

	hash := [32]byte{0xBB}

	// Unknown hash → peerHasHash must be false for any peer.
	assert.False(t, s.peerHasHash(hash, 42),
		"peerHasHash for unknown hash must be false")

	// Recording peer 42 first-time: returns true (newly added), and
	// peerHasHash flips to true for that peer.
	added := s.recordPeer(hash, 42)
	assert.True(t, added, "first recordPeer must return true (newly added)")
	assert.True(t, s.peerHasHash(hash, 42),
		"peerHasHash must be true after recordPeer")

	// Re-recording the same peer: returns false (already in set),
	// peerHasHash still true.
	added = s.recordPeer(hash, 42)
	assert.False(t, added, "duplicate recordPeer must return false")
	assert.True(t, s.peerHasHash(hash, 42),
		"peerHasHash must remain true on duplicate recordPeer")

	// A second peer on the same hash is independent.
	added = s.recordPeer(hash, 43)
	assert.True(t, added, "different peer on same hash must be newly added")
	assert.True(t, s.peerHasHash(hash, 42))
	assert.True(t, s.peerHasHash(hash, 43))
	assert.False(t, s.peerHasHash(hash, 44),
		"unrelated peer must not be reported as having the hash")

	// A different hash is isolated from peer 42's prior entry.
	other := [32]byte{0xCC}
	assert.False(t, s.peerHasHash(other, 42),
		"peer-set must be scoped per hash")
}

func TestMessageSuppression_EvictsLeastRecentlyUsedAtCapacity(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	s := newMessageSuppression(5*time.Minute, 3)
	s.now = func() time.Time { return now }

	first := [32]byte{1}
	second := [32]byte{2}
	third := [32]byte{3}
	fourth := [32]byte{4}
	s.observe(first)
	s.observe(second)
	s.observe(third)
	s.observe(first)
	s.observe(fourth)

	assert.Contains(t, s.entries, first)
	assert.NotContains(t, s.entries, second)
	assert.Contains(t, s.entries, third)
	assert.Contains(t, s.entries, fourth)
	assert.Len(t, s.entries, 3)
}

func TestMessageSuppression_RecordPeerUsesSharedCapacity(t *testing.T) {
	s := newMessageSuppression(5*time.Minute, 2)
	first := [32]byte{1}
	second := [32]byte{2}
	third := [32]byte{3}

	assert.True(t, s.recordPeer(first, 11))
	assert.True(t, s.recordPeer(second, 22))
	assert.False(t, s.recordPeer(first, 11))
	assert.True(t, s.recordPeer(third, 33))

	assert.True(t, s.peerHasHash(first, 11))
	assert.False(t, s.peerHasHash(second, 22))
	assert.True(t, s.peerHasHash(third, 33))
	assert.Len(t, s.entries, 2)
}

func TestMessageSuppression_PeerAssociationsExpireAsOneEntry(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	const ttl = 30 * time.Second
	s := newMessageSuppression(ttl, 4)
	s.now = func() time.Time { return now }
	hash := [32]byte{1}

	assert.True(t, s.recordPeer(hash, 11))
	now = now.Add(ttl)
	assert.False(t, s.peerHasHash(hash, 11))
	assert.NotContains(t, s.entries, hash)

	assert.True(t, s.recordPeer(hash, 22))
	assert.False(t, s.peerHasHash(hash, 11))
	assert.True(t, s.peerHasHash(hash, 22))
}

func TestMessageSuppression_AllowRetryRemovesWholeEntry(t *testing.T) {
	s := newMessageSuppression(time.Minute, 4)
	hash := [32]byte{1}
	s.recordPeer(hash, 11)

	s.allowRetry(hash)

	assert.NotContains(t, s.entries, hash)
	assert.False(t, s.peerHasHash(hash, 11))
}

func TestProposalSuppression_AdmitsOnlyAfterEngineAcceptance(t *testing.T) {
	router, _ := newRetryRouter(t)
	engine := &proposalAdmissionEngine{err: errors.New("invalid proposal")}
	router.engine = engine
	router.messageSeen = newMessageSuppression(time.Minute, 2)

	first := [32]byte{1}
	second := [32]byte{2}
	router.messageSeen.observe(first)
	router.messageSeen.observe(second)

	proposal := &message.ProposeSet{
		ProposeSeq:     1,
		CurrentTxHash:  make([]byte, 32),
		NodePubKey:     make([]byte, 33),
		CloseTime:      timeToXrplEpoch(time.Unix(1_700_000_000, 0)),
		Signature:      make([]byte, signatureMinLen),
		PreviousLedger: make([]byte, 32),
	}
	proposal.NodePubKey[0] = 0x02
	hash := hashProposalSuppression(ProposalFromMessage(proposal))
	inbound := &peermanagement.InboundMessage{
		PeerID:  7,
		Type:    uint16(message.TypeProposeLedger),
		Payload: encodePayload(t, proposal),
	}

	router.handleProposal(inbound)
	assert.NotContains(t, router.messageSeen.entries, hash)
	assert.Contains(t, router.messageSeen.entries, first)
	assert.Contains(t, router.messageSeen.entries, second)

	engine.err = nil
	router.handleProposal(inbound)
	require.Contains(t, router.messageSeen.entries, hash)
	assert.Equal(t, 2, engine.calls)

	router.handleProposal(inbound)
	assert.Equal(t, 2, engine.calls, "accepted duplicate must bypass the engine")
}
