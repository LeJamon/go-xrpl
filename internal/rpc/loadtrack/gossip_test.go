package loadtrack

import (
	"testing"
	"time"
)

// TestGossip_ExportFiltersBelowMinimum verifies Export only emits
// consumers whose decayed local balance >= MinimumGossipBalance, matching
// rippled Logic.h:270 (`if (item.balance >= minimumGossipBalance)`).
func TestGossip_ExportFiltersBelowMinimum(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	tr := newWithClock(func() time.Time { return now })

	// chatty client: 1 heavy (3000) is above MinimumGossipBalance(1000)
	tr.Charge("loud", LoadHeavy)
	// quiet client: 1 reference (20) is below
	tr.Charge("quiet", LoadReference)

	g := tr.Export()
	if len(g.Items) != 1 {
		t.Fatalf("expected 1 gossiped item, got %d (%v)", len(g.Items), g.Items)
	}
	if g.Items[0].Key != "loud" {
		t.Errorf("expected key=loud, got %q", g.Items[0].Key)
	}
	if g.Items[0].Balance < MinimumGossipBalance {
		t.Errorf("emitted item below threshold: balance=%d", g.Items[0].Balance)
	}
}

// TestGossip_ImportRaisesThreshold checks that an imported remote
// balance pushes a previously-OK client over the Warn threshold without
// any local charges, matching rippled Entry.h:74 balance(now) =
// local_balance.value(now) + remote_balance.
func TestGossip_ImportRaisesThreshold(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	tr := newWithClock(func() time.Time { return now })

	// Without import, a single Reference charge is fine.
	if got := tr.Charge("1.2.3.4", LoadReference); got != OutcomeOK {
		t.Fatalf("baseline charge: expected OK, got %v", got)
	}

	tr.Import("peerA", Gossip{Items: []GossipItem{{Key: "1.2.3.4", Balance: WarningThreshold}}})

	if got := tr.Charge("1.2.3.4", LoadReference); got != OutcomeWarn {
		t.Fatalf("after import: expected Warn, got %v (balance=%v)", got, tr.Balance("1.2.3.4"))
	}
}

// TestGossip_ReImportReplacesPriorContribution checks that a second
// Import from the same origin subtracts the previous contribution
// before adding the new one, matching rippled Logic.h:312-333.
func TestGossip_ReImportReplacesPriorContribution(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	tr := newWithClock(func() time.Time { return now })

	tr.Import("peerA", Gossip{Items: []GossipItem{{Key: "1.2.3.4", Balance: 10_000}}})
	if got := tr.Balance("1.2.3.4"); got != 10_000 {
		t.Fatalf("after first import: balance=%v, want 10000", got)
	}

	tr.Import("peerA", Gossip{Items: []GossipItem{{Key: "1.2.3.4", Balance: 2_000}}})
	if got := tr.Balance("1.2.3.4"); got != 2_000 {
		t.Fatalf("after second import: balance=%v, want 2000 (not 12000)", got)
	}
}

// TestGossip_ImportsFromMultipleOriginsAccumulate ensures distinct
// origins contribute independently — matches rippled's keyed import
// table semantics.
func TestGossip_ImportsFromMultipleOriginsAccumulate(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	tr := newWithClock(func() time.Time { return now })

	tr.Import("peerA", Gossip{Items: []GossipItem{{Key: "1.2.3.4", Balance: 3_000}}})
	tr.Import("peerB", Gossip{Items: []GossipItem{{Key: "1.2.3.4", Balance: 4_000}}})

	if got := tr.Balance("1.2.3.4"); got != 7_000 {
		t.Fatalf("two-peer import: balance=%v, want 7000", got)
	}
}

// TestGossip_ImportExpires verifies imports are refunded after
// GossipExpiration elapses without a refresh.
func TestGossip_ImportExpires(t *testing.T) {
	clock := time.Unix(1_000_000, 0)
	tr := newWithClock(func() time.Time { return clock })

	tr.Import("peerA", Gossip{Items: []GossipItem{{Key: "1.2.3.4", Balance: 8_000}}})
	if got := tr.Balance("1.2.3.4"); got != 8_000 {
		t.Fatalf("pre-expiry balance=%v, want 8000", got)
	}

	clock = clock.Add(GossipExpiration + time.Second)
	tr.Charge("trigger-sweep", LoadReference)

	if got := tr.Balance("1.2.3.4"); got != 0 {
		t.Fatalf("post-expiry balance=%v, want 0", got)
	}
}

// TestGossip_EmptyOriginIgnored asserts that Import("", ...) is a no-op
// rather than colliding with other empty-origin imports.
func TestGossip_EmptyOriginIgnored(t *testing.T) {
	tr := New()
	tr.Import("", Gossip{Items: []GossipItem{{Key: "1.2.3.4", Balance: 5_000}}})
	if got := tr.Balance("1.2.3.4"); got != 0 {
		t.Errorf("empty-origin import should be ignored, got balance=%v", got)
	}
}

// TestGossip_RoundTrip exercises Export → Import across two trackers,
// confirming the wire format is self-consistent.
func TestGossip_RoundTrip(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	a := newWithClock(func() time.Time { return now })
	b := newWithClock(func() time.Time { return now })

	// Node A sees a chatty client.
	a.Charge("loud", LoadHeavy) // 3000

	g := a.Export()
	if len(g.Items) == 0 {
		t.Fatal("expected non-empty export from node A")
	}

	// Node B has no local record yet — importing should raise its
	// remote balance for the same key.
	b.Import("node-a", g)
	if got := b.Balance("loud"); got < MinimumGossipBalance {
		t.Errorf("round-trip: B balance for 'loud'=%v, want >=%d", got, MinimumGossipBalance)
	}
}
