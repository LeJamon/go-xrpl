package adaptor

import (
	"context"
	"testing"
	"time"

	"github.com/LeJamon/goXRPLd/internal/peermanagement"
)

// TestRouter_IOLatencyProbe_Drained verifies the router select loop drains
// the probe channel and records samples. Without the wiring, LatencyMs
// stays 0 because no goroutine consumes Ch().
func TestRouter_IOLatencyProbe_Drained(t *testing.T) {
	inbox := make(chan *peermanagement.InboundMessage, 1)
	router := NewRouter(nil, nil, nil, inbox)

	probe := NewIOLatencyProbe(nil)
	router.SetIOLatencyProbe(probe)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	probe.Start(ctx, 10*time.Millisecond)
	defer probe.Stop()

	go router.Run(ctx)

	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if probe.LatencyMs() > 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("expected LatencyMs > 0 after router drains probe, got %d", probe.LatencyMs())
}

// TestRouter_NoProbe_NoCrash verifies a router without a probe runs cleanly.
// The nil-channel case must be silently disabled in the select.
func TestRouter_NoProbe_NoCrash(t *testing.T) {
	inbox := make(chan *peermanagement.InboundMessage, 1)
	router := NewRouter(nil, nil, nil, inbox)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		router.Run(ctx)
		close(done)
	}()

	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("router did not exit after context cancel")
	}
}
