package rcl

import (
	"sync/atomic"
	"testing"
	"time"
)

// The consensus run loop must ping the installed stall watchdog callback on
// every heartbeat tick, so an out-of-band watchdog can observe loop liveness.
func TestEngine_StallPingFiresFromRunLoop(t *testing.T) {
	adaptor := newMockAdaptor()
	cfg := DefaultConfig()
	// Shrink the heartbeat so the test does not wait whole seconds.
	cfg.Timing.LedgerMinClose = 10 * time.Millisecond
	engine := NewEngine(adaptor, cfg)

	var pings atomic.Int64
	engine.SetStallPing(func() { pings.Add(1) })

	if err := engine.Start(t.Context()); err != nil {
		t.Fatalf("start engine: %v", err)
	}
	defer func() { _ = engine.Stop() }()

	deadline := time.After(2 * time.Second)
	for pings.Load() < 2 {
		select {
		case <-deadline:
			t.Fatalf("stall ping fired %d times, expected the run loop to ping repeatedly", pings.Load())
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// A nil ping disables the callback without panicking the loop.
func TestEngine_StallPingNilIsSafe(t *testing.T) {
	adaptor := newMockAdaptor()
	cfg := DefaultConfig()
	cfg.Timing.LedgerMinClose = 10 * time.Millisecond
	engine := NewEngine(adaptor, cfg)

	engine.SetStallPing(func() {})
	engine.SetStallPing(nil) // clear

	if err := engine.Start(t.Context()); err != nil {
		t.Fatalf("start engine: %v", err)
	}
	defer func() { _ = engine.Stop() }()

	time.Sleep(50 * time.Millisecond) // a few ticks; must not panic
}
