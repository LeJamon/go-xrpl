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

func TestEngine_StallPingCanBeClearedWhileRunning(t *testing.T) {
	adaptor := newMockAdaptor()
	cfg := DefaultConfig()
	cfg.Timing.LedgerMinClose = 10 * time.Millisecond
	engine := NewEngine(adaptor, cfg)

	started := make(chan struct{})
	release := make(chan struct{})
	returned := make(chan struct{})
	var oldPings atomic.Int64
	engine.SetStallPing(func() {
		if oldPings.Add(1) == 1 {
			close(started)
			<-release
			close(returned)
		}
	})
	if err := engine.Start(t.Context()); err != nil {
		t.Fatalf("start engine: %v", err)
	}
	defer func() { _ = engine.Stop() }()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("stall ping did not start")
	}
	engine.SetStallPing(nil)
	close(release)
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("in-flight stall ping did not return")
	}

	replacementPings := make(chan struct{}, 2)
	engine.SetStallPing(func() {
		select {
		case replacementPings <- struct{}{}:
		default:
		}
	})
	for range 2 {
		select {
		case <-replacementPings:
		case <-time.After(2 * time.Second):
			t.Fatal("replacement stall ping did not run")
		}
	}
	if oldPings.Load() != 1 {
		t.Fatalf("cleared stall ping ran %d times", oldPings.Load())
	}
}
