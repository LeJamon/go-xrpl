package peermanagement

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newLatencyTestPeer returns a Peer wired only enough to exercise the
// latency / pings-in-flight machinery (no transport).
func newLatencyTestPeer(t *testing.T) *Peer {
	t.Helper()
	id, err := NewIdentity()
	require.NoError(t, err)
	events := make(chan Event, 1)
	return NewPeer(PeerID(1), Endpoint{Host: "192.0.2.1", Port: 51235}, false, id, events)
}

func TestPeer_Latency_UnsetByDefault(t *testing.T) {
	p := newLatencyTestPeer(t)
	d, ok := p.Latency()
	assert.False(t, ok, "no measurement → second return is false (matches std::optional<>)")
	assert.Equal(t, time.Duration(0), d)
}

func TestPeer_OnPong_FirstMeasurementSeedsLatency(t *testing.T) {
	p := newLatencyTestPeer(t)
	now := time.Now()

	p.recordPingSent(42, now)
	p.OnPong(42, now.Add(120*time.Millisecond))

	d, ok := p.Latency()
	require.True(t, ok)
	assert.Equal(t, 120*time.Millisecond, d, "first sample seeds latency directly (no smoothing)")
}

func TestPeer_OnPong_EWMASmoothingMatchesRippled(t *testing.T) {
	// Rippled formula (PeerImp.cpp:1115):
	//   latency_ = (latency_ * 7 + rtt) / 8
	p := newLatencyTestPeer(t)
	base := time.Now()

	p.recordPingSent(1, base)
	p.OnPong(1, base.Add(100*time.Millisecond))

	p.recordPingSent(2, base.Add(time.Second))
	p.OnPong(2, base.Add(time.Second+200*time.Millisecond))

	want := (100*time.Millisecond*7 + 200*time.Millisecond) / 8
	got, ok := p.Latency()
	require.True(t, ok)
	assert.Equal(t, want, got)
}

func TestPeer_OnPong_UnknownSeqIgnored(t *testing.T) {
	p := newLatencyTestPeer(t)
	p.OnPong(99, time.Now())

	_, ok := p.Latency()
	assert.False(t, ok, "Pong with no matching ping leaves latency unset")
}

func TestPeer_OnPong_WrongSeqDoesNotConsumeOther(t *testing.T) {
	p := newLatencyTestPeer(t)
	now := time.Now()
	p.recordPingSent(1, now)

	p.OnPong(2, now.Add(50*time.Millisecond))
	_, ok := p.Latency()
	assert.False(t, ok, "wrong cookie cannot update latency")

	p.OnPong(1, now.Add(80*time.Millisecond))
	d, ok := p.Latency()
	require.True(t, ok)
	assert.Equal(t, 80*time.Millisecond, d, "matching seq still works after a mismatched pong")
}

func TestPeer_OnPong_ConsumedSeqDoesNotReapply(t *testing.T) {
	p := newLatencyTestPeer(t)
	now := time.Now()
	p.recordPingSent(7, now)
	p.OnPong(7, now.Add(40*time.Millisecond))

	// Second OnPong with same seq must be a no-op (the entry was deleted).
	p.OnPong(7, now.Add(400*time.Millisecond))

	d, _ := p.Latency()
	assert.Equal(t, 40*time.Millisecond, d, "duplicate pong does not re-smooth latency")
}

func TestPeer_OnPong_NegativeRTTClamped(t *testing.T) {
	p := newLatencyTestPeer(t)
	now := time.Now()
	p.recordPingSent(1, now.Add(time.Second)) // recv earlier than send (pathological)
	p.OnPong(1, now)

	d, ok := p.Latency()
	require.True(t, ok)
	assert.Equal(t, time.Duration(0), d, "monotonic-clock skew can't push RTT negative")
}

func TestPeer_RecordPingSent_TrimsExpired(t *testing.T) {
	p := newLatencyTestPeer(t)
	old := time.Now().Add(-pingInFlightTTL - 5*time.Second)
	p.recordPingSent(1, old)

	now := time.Now()
	p.recordPingSent(2, now)

	p.latencyMu.RLock()
	_, oldStill := p.pingsInFlight[1]
	_, freshKept := p.pingsInFlight[2]
	p.latencyMu.RUnlock()

	assert.False(t, oldStill, "entry past pingInFlightTTL should be trimmed on next send")
	assert.True(t, freshKept)
}

func TestPeer_RecordPingSent_CapsMapSize(t *testing.T) {
	p := newLatencyTestPeer(t)
	base := time.Now()

	// Insert pingsInFlightCap+1 entries, all within TTL so trimming
	// alone wouldn't bound the map. Each subsequent send must evict
	// the oldest entry to keep the cap.
	for i := 0; i < pingsInFlightCap+1; i++ {
		p.recordPingSent(uint32(100+i), base.Add(time.Duration(i)*time.Millisecond))
	}

	p.latencyMu.RLock()
	size := len(p.pingsInFlight)
	_, oldestStill := p.pingsInFlight[100]
	_, newestKept := p.pingsInFlight[uint32(100+pingsInFlightCap)]
	p.latencyMu.RUnlock()

	assert.LessOrEqual(t, size, pingsInFlightCap, "cap bounds memory under adversarial input")
	assert.False(t, oldestStill, "oldest in-flight entry is evicted when cap is hit")
	assert.True(t, newestKept)
}

func TestPeerInfo_LatencySnapshot(t *testing.T) {
	p := newLatencyTestPeer(t)
	now := time.Now()
	p.recordPingSent(1, now)
	p.OnPong(1, now.Add(75*time.Millisecond))

	info := p.Info()
	assert.True(t, info.HasLatency)
	assert.Equal(t, 75*time.Millisecond, info.Latency)
}

func TestOverlay_PeersJSON_OmitsLatencyWhenUnmeasured(t *testing.T) {
	p := newLatencyTestPeer(t)
	o := newTestOverlayWithPeers(map[PeerID]*Peer{p.ID(): p})

	out := o.PeersJSON()
	require.Len(t, out, 1)
	assert.NotContains(t, out[0], "latency",
		"matches rippled PeerImp::json: latency emitted only when measured")
}

func TestOverlay_PeersJSON_EmitsLatencyMillisWhenMeasured(t *testing.T) {
	p := newLatencyTestPeer(t)
	now := time.Now()
	p.recordPingSent(1, now)
	p.OnPong(1, now.Add(123*time.Millisecond))

	o := newTestOverlayWithPeers(map[PeerID]*Peer{p.ID(): p})

	out := o.PeersJSON()
	require.Len(t, out, 1)
	assert.Equal(t, uint32(123), out[0]["latency"],
		"rippled emits Json::UInt milliseconds (PeerImp.cpp:421-425)")
}

func TestOverlay_PeersJSON_LatencyRoundedToMillis(t *testing.T) {
	// rippled uses std::chrono::round<milliseconds> on the rtt sample
	// (PeerImp.cpp:1109). Sub-ms residuals must round, not truncate.
	p := newLatencyTestPeer(t)
	now := time.Now()
	p.recordPingSent(1, now)
	p.OnPong(1, now.Add(1500*time.Microsecond)) // 1.5ms → rounds to 2

	o := newTestOverlayWithPeers(map[PeerID]*Peer{p.ID(): p})
	out := o.PeersJSON()
	require.Len(t, out, 1)
	assert.Equal(t, uint32(2), out[0]["latency"])
}
