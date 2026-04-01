package nodestore

import (
	"sync"
	"time"
)

// IOLatencyTracker tracks IO latency using an exponentially weighted moving average (EWMA).
// This provides a smoothed estimate of recent IO latency, matching how rippled
// reports io_latency_ms in server_info.
type IOLatencyTracker struct {
	mu      sync.Mutex
	ewma    time.Duration // current EWMA value
	alpha   float64       // smoothing factor (0 < alpha <= 1)
	seeded  bool          // whether we have at least one sample
}

// NewIOLatencyTracker creates a new tracker with the given EWMA smoothing factor.
// Alpha controls how quickly the average responds to new samples:
//   - alpha close to 1.0 = very responsive (recent samples dominate)
//   - alpha close to 0.0 = very smooth (old values persist longer)
//
// A typical value is 0.1 (gives ~10-sample effective window).
func NewIOLatencyTracker(alpha float64) *IOLatencyTracker {
	if alpha <= 0 || alpha > 1 {
		alpha = 0.1
	}
	return &IOLatencyTracker{alpha: alpha}
}

// Record records a new IO latency sample.
func (t *IOLatencyTracker) Record(d time.Duration) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if !t.seeded {
		t.ewma = d
		t.seeded = true
		return
	}

	// EWMA: new = alpha * sample + (1 - alpha) * old
	t.ewma = time.Duration(t.alpha*float64(d) + (1-t.alpha)*float64(t.ewma))
}

// Latency returns the current smoothed IO latency estimate.
// Returns 0 if no samples have been recorded.
func (t *IOLatencyTracker) Latency() time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.ewma
}

// LatencyMs returns the current smoothed IO latency in milliseconds.
func (t *IOLatencyTracker) LatencyMs() int {
	return int(t.Latency() / time.Millisecond)
}
