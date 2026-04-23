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
