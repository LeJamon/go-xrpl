package peermanagement

import (
	"context"
	"testing"
)

// TestDiscoveryStopIdempotent guards the issue #673 regression: Stop() must be
// idempotent. The original bug closed an unguarded channel, so a second Stop()
// paniced with "close of closed channel".
func TestDiscoveryStopIdempotent(t *testing.T) {
	d := NewDiscovery(&Config{MaxPeers: 50, MaxInbound: 25, MaxOutbound: 25}, make(chan Event, 1))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := d.Start(ctx); err != nil {
		t.Fatalf("Start error: %v", err)
	}

	d.Stop()
	d.Stop() // second call must be a no-op, not a panic
}

// TestOverlayStopIdempotent guards against the non-idempotent Overlay.Stop()
// from issue #673: a second Stop() (defensive cleanup, error-path + deferred
// stop) must not re-run shutdown and must not panic via discovery.Stop().
func TestOverlayStopIdempotent(t *testing.T) {
	o := &Overlay{
		cfg:       Config{},
		peers:     make(map[PeerID]*Peer),
		events:    make(chan Event, 1),
		discovery: NewDiscovery(&Config{}, make(chan Event, 1)),
	}

	if err := o.Stop(); err != nil {
		t.Fatalf("first Stop error: %v", err)
	}
	if err := o.Stop(); err != nil { // must be a no-op, not a panic
		t.Fatalf("second Stop error: %v", err)
	}
}
