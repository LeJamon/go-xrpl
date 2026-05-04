package peermanagement

import (
	"errors"
	"fmt"
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

// TestPeer_OnPong_ClearsOlderInFlight pins the single-cycle semantics
// adopted to match rippled (PeerImp.h:115's lone std::optional<uint32>
// lastPingSeq_): a matching pong evicts every in-flight entry sent
// at-or-before the matched send-time, so a peer that responds to the
// most recent ping is never disconnected by a still-pending older
// one. Without this, goXRPL's 15s tick + 60s timeout would evict
// peers under partial pong loss that rippled keeps.
func TestPeer_OnPong_ClearsOlderInFlight(t *testing.T) {
	p := newLatencyTestPeer(t)
	base := time.Now()
	p.recordPingSent(1, base)                    // older, will be lost
	p.recordPingSent(2, base.Add(time.Second))   // older, will be lost
	p.recordPingSent(3, base.Add(2*time.Second)) // matched
	p.recordPingSent(4, base.Add(3*time.Second)) // newer than the matched, must survive

	p.OnPong(3, base.Add(2*time.Second+10*time.Millisecond))

	p.latencyMu.RLock()
	_, has1 := p.pingsInFlight[1]
	_, has2 := p.pingsInFlight[2]
	_, has3 := p.pingsInFlight[3]
	_, has4 := p.pingsInFlight[4]
	p.latencyMu.RUnlock()

	assert.False(t, has1, "older ping must be cleared by matching pong")
	assert.False(t, has2, "older ping must be cleared by matching pong")
	assert.False(t, has3, "matched ping must be cleared")
	assert.True(t, has4, "ping sent after the matched one must survive")

	// At the moment seq=1 would have aged out (had it not been cleared),
	// the surviving seq=4 is only pingTimeout-3s old — still fresh.
	_, _, stale := p.staleInFlightPing(base.Add(pingTimeout), pingTimeout)
	assert.False(t, stale, "no stale candidate after responsive pong sweeps older entries")
}

// Pongs whose seq was already swept by a more-recent matching pong
// are silently dropped. Mirrors rippled PeerImp.cpp:1099-1118 (pong
// with seq != lastPingSeq_ ignored).
func TestPeer_OnPong_DelayedPongForSweptSeqIsNoOp(t *testing.T) {
	p := newLatencyTestPeer(t)
	base := time.Now()
	p.recordPingSent(1, base)
	p.recordPingSent(2, base.Add(time.Second))

	p.OnPong(2, base.Add(time.Second+5*time.Millisecond))

	latencyAfterFirst, hadFirst := p.Latency()
	require.True(t, hadFirst, "first pong must seed latency")

	p.OnPong(1, base.Add(2*time.Second))

	latencyAfter, ok := p.Latency()
	require.True(t, ok)
	assert.Equal(t, latencyAfterFirst, latencyAfter,
		"delayed pong for swept seq must not update latency")

	p.latencyMu.RLock()
	inFlight := len(p.pingsInFlight)
	p.latencyMu.RUnlock()
	assert.Equal(t, 0, inFlight, "delayed pong must not resurrect entries")
}

func TestOverlay_NotePeerRunEnded_IncrementsPingTimeoutCounter(t *testing.T) {
	o := &Overlay{}

	require.Equal(t, uint64(0), o.PingTimeoutDisconnects())

	o.notePeerRunEnded(ErrPingTimeout)
	assert.Equal(t, uint64(1), o.PingTimeoutDisconnects())

	// errors.Is must follow %w wrappers so a future Run() that wraps
	// the sentinel still bumps the counter.
	o.notePeerRunEnded(fmt.Errorf("peer dropped: %w", ErrPingTimeout))
	assert.Equal(t, uint64(2), o.PingTimeoutDisconnects())

	o.notePeerRunEnded(errors.New("connection reset"))
	o.notePeerRunEnded(ErrConnectionClosed)
	assert.Equal(t, uint64(2), o.PingTimeoutDisconnects(),
		"only ErrPingTimeout (and wrappers) bump the counter")

	o.notePeerRunEnded(nil)
	assert.Equal(t, uint64(2), o.PingTimeoutDisconnects())
}

// TestPeer_RunPingTick_HappyPathRecordsAndSends pins the non-timeout
// branch: with no stale ping, runPingTick records the freshly-issued
// seq in pingsInFlight and queues exactly one wire message on
// p.send. Guards against silent regressions that would drop the
// recordPingSent call or short-circuit the Send.
func TestPeer_RunPingTick_HappyPathRecordsAndSends(t *testing.T) {
	p := newLatencyTestPeer(t)
	now := time.Now()

	require.NoError(t, p.runPingTick(now))

	p.latencyMu.RLock()
	inFlight := len(p.pingsInFlight)
	p.latencyMu.RUnlock()
	assert.Equal(t, 1, inFlight, "happy path records exactly one in-flight ping")

	select {
	case msg := <-p.send:
		assert.NotEmpty(t, msg, "ping must be queued on the send channel")
	default:
		t.Fatal("expected a wire message on p.send after a successful tick")
	}
}
