package peermanagement

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPeer_StaleInFlightPing_NoneInFlight(t *testing.T) {
	p := newLatencyTestPeer(t)
	_, _, ok := p.staleInFlightPing(time.Now(), pingTimeout)
	assert.False(t, ok)
}

func TestPeer_StaleInFlightPing_FreshPingNotStale(t *testing.T) {
	p := newLatencyTestPeer(t)
	now := time.Now()
	p.recordPingSent(1, now)

	_, _, ok := p.staleInFlightPing(now.Add(pingTimeout-time.Second), pingTimeout)
	assert.False(t, ok, "ping younger than threshold is not stale")
}

func TestPeer_StaleInFlightPing_AtThresholdReportsStale(t *testing.T) {
	p := newLatencyTestPeer(t)
	now := time.Now()
	p.recordPingSent(7, now)

	seq, age, ok := p.staleInFlightPing(now.Add(pingTimeout), pingTimeout)
	require.True(t, ok, "age == threshold disconnects (one cycle of grace consumed)")
	assert.Equal(t, uint32(7), seq)
	assert.Equal(t, pingTimeout, age)
}

func TestPeer_StaleInFlightPing_PicksOldest(t *testing.T) {
	p := newLatencyTestPeer(t)
	base := time.Now()
	p.recordPingSent(1, base)                   // oldest
	p.recordPingSent(2, base.Add(time.Second))  // newer
	p.recordPingSent(3, base.Add(2*time.Second)) // newest

	seq, _, ok := p.staleInFlightPing(base.Add(pingTimeout), pingTimeout)
	require.True(t, ok)
	assert.Equal(t, uint32(1), seq, "oldest in-flight ping drives the timeout decision")
}

func TestPeer_StaleInFlightPing_PongClearsStale(t *testing.T) {
	p := newLatencyTestPeer(t)
	now := time.Now()
	p.recordPingSent(42, now)

	p.OnPong(42, now.Add(50*time.Millisecond))

	_, _, ok := p.staleInFlightPing(now.Add(pingTimeout+time.Hour), pingTimeout)
	assert.False(t, ok, "answered ping is removed from in-flight map and cannot trigger timeout")
}

// TestPeer_RunPingTick_StalePingReturnsErrPingTimeout pins the
// integration the predicate tests above only cover in pieces:
// runPingTick (the per-tick body of pingLoop, reached via Run())
// must surface ErrPingTimeout when an in-flight ping has aged past
// the threshold. Pre-populating with age = 2*pingTimeout means a
// future regression that swapped the stale-check and the
// recordPingSent GC sweep would evict the entry before it could be
// flagged — and this test would fail.
func TestPeer_RunPingTick_StalePingReturnsErrPingTimeout(t *testing.T) {
	p := newLatencyTestPeer(t)
	now := time.Now()
	p.recordPingSent(7, now.Add(-2*pingTimeout))

	err := p.runPingTick(now)
	require.ErrorIs(t, err, ErrPingTimeout)
}
