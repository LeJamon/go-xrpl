package peermanagement

import (
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestByteMetrics_TotalBytesAccumulates pins the cumulative semantics
// of metrics_.recv.total_bytes() / metrics_.sent.total_bytes()
// (PeerImp.cpp:3547-3551): every byte ever passed in must be counted,
// regardless of interval boundaries.
func TestByteMetrics_TotalBytesAccumulates(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	m := newByteMetrics(func() time.Time { return now })

	m.addMessage(100)
	m.addMessage(50)

	assert.EqualValues(t, 150, m.totalBytesSnapshot())
}

// TestByteMetrics_AverageBytesIsZeroBeforeBoundary mirrors rippled
// (PeerImp.cpp:3525): without one full second elapsed, the rolling
// average remains at its initial value (0) — the bucket is not flushed.
func TestByteMetrics_AverageBytesIsZeroBeforeBoundary(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }

	m := newByteMetrics(clock)
	m.addMessage(1024)

	assert.EqualValues(t, 0, m.averageBytes(),
		"sub-second activity must not flush the rolling bucket")
}

// TestByteMetrics_RollingAveragePartialFill reproduces rippled's
// pre-filled buffer (rollingAvg_{30, 0ull}, PeerImp.h:219): one bucket
// of N bps in a 30-slot zero-filled ring averages to N/30.
func TestByteMetrics_RollingAveragePartialFill(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clockNow := now
	m := newByteMetrics(func() time.Time { return clockNow })

	// One second of activity at 3000 B/s.
	m.addMessage(3000)
	clockNow = clockNow.Add(1 * time.Second)
	m.tick() // flush the bucket at the boundary

	// avg = 3000 / 30 (29 zero buckets pre-filled).
	assert.EqualValues(t, 3000/rollingWindowSeconds, m.averageBytes())
}

// TestByteMetrics_RollingAverageSteadyState fills the ring with
// identical samples and checks the mean equals that sample. This is
// the steady-state behavior reported by avg_bps_recv / avg_bps_sent.
func TestByteMetrics_RollingAverageSteadyState(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clockNow := now
	m := newByteMetrics(func() time.Time { return clockNow })

	const bps uint64 = 1500
	for i := 0; i < rollingWindowSeconds; i++ {
		m.addMessage(bps)
		clockNow = clockNow.Add(1 * time.Second)
		m.tick() // close out the bucket
	}

	assert.EqualValues(t, bps, m.averageBytes(),
		"30 identical buckets must average to the per-bucket value")
}

// TestByteMetrics_RollingAverageDropsOldest verifies the circular
// buffer eviction matches boost::circular_buffer::push_back semantics
// (PeerImp.cpp:3528): after rollingWindowSeconds+1 fills, the very
// first bucket is forgotten.
func TestByteMetrics_RollingAverageDropsOldest(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clockNow := now
	m := newByteMetrics(func() time.Time { return clockNow })

	step := func(bytes uint64) {
		m.addMessage(bytes)
		clockNow = clockNow.Add(1 * time.Second)
		m.tick()
	}

	// Seed bucket 0 with a distinctive large value.
	step(60_000)
	// Fill the remaining 29 buckets with a steady value.
	for i := 0; i < rollingWindowSeconds-1; i++ {
		step(1000)
	}
	// One more step should evict bucket 0; the steady value must now
	// dominate the mean.
	step(1000)

	assert.EqualValues(t, 1000, m.averageBytes(),
		"after rollingWindowSeconds+1 fills, the seed bucket must be evicted")
}

// TestPeerMetrics_RecvAndSentIndependent sanity-checks that the recv
// and sent counters do not bleed into each other — rippled's
// metrics_.recv and metrics_.sent are independent Metrics instances.
func TestPeerMetrics_RecvAndSentIndependent(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	pm := newPeerMetrics(func() time.Time { return now })

	pm.recv.addMessage(100)
	pm.sent.addMessage(7)

	assert.EqualValues(t, 100, pm.recv.totalBytesSnapshot())
	assert.EqualValues(t, 7, pm.sent.totalBytesSnapshot())
}

// TestOverlay_PeersJSON_EmitsMetricsObject pins the strict-parity
// contract from rippled PeerImp::json (PeerImp.cpp:493-501): a
// `metrics` object with the four byte-counter fields, all rendered as
// decimal strings (rippled uses std::to_string).
func TestOverlay_PeersJSON_EmitsMetricsObject(t *testing.T) {
	id, err := NewIdentity()
	require.NoError(t, err)

	p := NewPeer(1, Endpoint{Host: "10.0.0.1", Port: 51235}, false, id, nil)
	p.setState(PeerStateConnected)
	// Frozen clock so the avg_bps_* assertions don't race a real
	// 1-second boundary on slow CI.
	frozen := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	p.metrics = newPeerMetrics(func() time.Time { return frozen })

	// Drive the per-peer counters through the same surface PeerImp's
	// async read/write callbacks would (PeerImp.cpp:911, 970).
	p.metrics.recv.addMessage(2048)
	p.metrics.sent.addMessage(512)

	o := newTestOverlayWithPeers(map[PeerID]*Peer{1: p})
	entries := o.PeersJSON()
	require.Len(t, entries, 1)

	metrics, ok := entries[0]["metrics"].(map[string]any)
	require.True(t, ok, "PeersJSON must emit `metrics` as a JSON object")

	for _, key := range []string{
		"total_bytes_recv",
		"total_bytes_sent",
		"avg_bps_recv",
		"avg_bps_sent",
	} {
		val, present := metrics[key]
		require.True(t, present, "metrics.%s missing", key)
		s, isString := val.(string)
		require.True(t, isString,
			"metrics.%s must be a decimal string (rippled std::to_string), got %T", key, val)
		_, parseErr := strconv.ParseUint(s, 10, 64)
		require.NoError(t, parseErr,
			"metrics.%s must parse as a uint64 decimal, got %q", key, s)
	}

	assert.Equal(t, "2048", metrics["total_bytes_recv"])
	assert.Equal(t, "512", metrics["total_bytes_sent"])
	// avg_bps_* are 0 here because no second boundary has elapsed; the
	// schema is what we're pinning, not the value.
	assert.Equal(t, "0", metrics["avg_bps_recv"])
	assert.Equal(t, "0", metrics["avg_bps_sent"])
}

// TestOverlay_PeersJSON_EmitsMetricsForEveryPeer guards against a
// regression where the metrics block is conditional. rippled emits it
// unconditionally (PeerImp.cpp:493-501).
func TestOverlay_PeersJSON_EmitsMetricsForEveryPeer(t *testing.T) {
	id, err := NewIdentity()
	require.NoError(t, err)

	mk := func(pid PeerID, host string) *Peer {
		p := NewPeer(pid, Endpoint{Host: host, Port: 51235}, false, id, nil)
		p.setState(PeerStateConnected)
		return p
	}

	o := newTestOverlayWithPeers(map[PeerID]*Peer{
		1: mk(1, "10.0.0.1"),
		2: mk(2, "10.0.0.2"),
		3: mk(3, "10.0.0.3"),
	})

	entries := o.PeersJSON()
	require.Len(t, entries, 3)
	for _, e := range entries {
		_, ok := e["metrics"].(map[string]any)
		require.True(t, ok, "every peer entry must carry a metrics object")
	}
}
