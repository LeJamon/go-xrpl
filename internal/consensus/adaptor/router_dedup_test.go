package adaptor

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

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
