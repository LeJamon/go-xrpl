package peermanagement

import (
	"sync"
	"time"
)

// rollingWindowSeconds matches rippled's circular_buffer<uint64>(30, 0)
// in PeerImp::Metrics (PeerImp.h:219). The bps reading is the mean of
// the last 30 one-second buckets.
const rollingWindowSeconds = 30

// byteMetrics tracks total bytes seen and a rolling per-second average,
// mirroring rippled PeerImp::Metrics (PeerImp.cpp:3514-3551).
//
// The model: every wire-byte arrival increments totalBytes and
// accumBytes. Once at least one second has elapsed since intervalStart,
// we close the bucket — pushing accumBytes/elapsedSeconds onto the
// rolling buffer and recomputing the mean — then reset the interval.
type byteMetrics struct {
	mu sync.Mutex

	clock func() time.Time

	totalBytes      uint64
	accumBytes      uint64
	intervalStart   time.Time
	rollingAvg      [rollingWindowSeconds]uint64
	rollingAvgBytes uint64
}

func newByteMetrics(clock func() time.Time) *byteMetrics {
	if clock == nil {
		clock = time.Now
	}
	return &byteMetrics{
		clock:         clock,
		intervalStart: clock(),
	}
}

// addMessage records bytes transferred on the wire. Mirrors
// PeerImp::Metrics::add_message (PeerImp.cpp:3514).
func (m *byteMetrics) addMessage(bytes uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.totalBytes += bytes
	m.accumBytes += bytes

	elapsed := m.clock().Sub(m.intervalStart)
	elapsedSecs := uint64(elapsed / time.Second)
	if elapsedSecs == 0 {
		return
	}

	avg := m.accumBytes / elapsedSecs

	// Shift the ring left by one and append the new bucket. Matches the
	// behavior of boost::circular_buffer::push_back: drop the oldest,
	// append the newest. Pre-fill (count<30) is handled by initialising
	// the array with zeros, exactly as rippled does
	// (rollingAvg_{30, 0ull}).
	copy(m.rollingAvg[:rollingWindowSeconds-1], m.rollingAvg[1:])
	m.rollingAvg[rollingWindowSeconds-1] = avg

	var sum uint64
	for _, v := range m.rollingAvg {
		sum += v
	}
	m.rollingAvgBytes = sum / rollingWindowSeconds

	m.intervalStart = m.clock()
	m.accumBytes = 0
}

// totalBytesSnapshot returns the cumulative byte count.
func (m *byteMetrics) totalBytesSnapshot() uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.totalBytes
}

// averageBytes returns the latest rolling-window mean (bytes/sec).
func (m *byteMetrics) averageBytes() uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.rollingAvgBytes
}

// peerMetrics groups receive and send byteMetrics for a single peer,
// matching rippled's anonymous metrics_ struct (PeerImp.h:226-230).
type peerMetrics struct {
	recv *byteMetrics
	sent *byteMetrics
}

func newPeerMetrics(clock func() time.Time) *peerMetrics {
	return &peerMetrics{
		recv: newByteMetrics(clock),
		sent: newByteMetrics(clock),
	}
}
