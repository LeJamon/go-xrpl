package resource

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeClock is a deterministic clock for driving decay windows in
// tests. Mirrors the TestStopwatch pattern used by rippled's
// Logic_test.cpp — advance one second per ++clock.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Unix(0, 0)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func newTestManager() (*Manager, *fakeClock) {
	clk := newFakeClock()
	return NewManager(clk.Now, nil), clk
}

func TestCharge_WarnThenDrop(t *testing.T) {
	m, clk := newTestManager()

	c := m.NewInboundEndpoint("192.0.2.10:51000")
	defer c.Release()

	// Mirrors testDrop(limited=true) from Logic_test.cpp:96-156:
	// sustained over-budget charges escalate through Warn to Drop.
	fee := NewCharge(DropThreshold+1, "synthetic")

	gotWarn := false
	for i := 0; i < 10000; i++ {
		d := c.Charge(fee, "")
		if d == Warn {
			gotWarn = true
			break
		}
		clk.Advance(time.Second)
	}
	if !gotWarn {
		t.Fatalf("Warn never reached under sustained over-budget charge")
	}

	gotDrop := false
	for i := 0; i < 10000; i++ {
		d := c.Charge(fee, "")
		if d == Drop {
			gotDrop = true
			break
		}
		clk.Advance(time.Second)
	}
	if !gotDrop {
		t.Fatalf("Drop never reached after Warn")
	}
	if !c.Disconnect() {
		t.Fatalf("Disconnect()=false after Drop")
	}
}

func TestCharge_AccumulatesToDrop(t *testing.T) {
	m, clk := newTestManager()
	c := m.NewInboundEndpoint("192.0.2.11")
	defer c.Release()

	// Steady-state under cost C charged every second is approximately
	// C balance — so we need cost above DropThreshold to escalate.
	saw := false
	for i := 0; i < 1000 && !saw; i++ {
		if c.Charge(NewCharge(DropThreshold+500, "iter"), "") == Drop {
			saw = true
		}
		clk.Advance(time.Second)
	}
	if !saw {
		t.Fatalf("Drop never reached under sustained over-threshold load")
	}
}

func TestCharge_DecayKeepsHonestPeerBelowDrop(t *testing.T) {
	m, clk := newTestManager()
	c := m.NewInboundEndpoint("192.0.2.12")
	defer c.Release()

	// One feeInvalidData (400) per decay window — well below the
	// drop threshold. Should never escalate to Drop or even Warn.
	for i := 0; i < 200; i++ {
		if d := c.Charge(FeeInvalidData, "low-freq"); d != Ok {
			t.Fatalf("iter %d: disposition=%v want Ok (balance=%d)", i, d, c.Balance())
		}
		clk.Advance(time.Duration(DecayWindowSeconds) * time.Second)
	}
}

func TestUnlimited_NeverDrops(t *testing.T) {
	m, clk := newTestManager()
	c := m.NewUnlimitedEndpoint("10.0.0.1")
	defer c.Release()

	// Even at synthetic over-budget cost, an unlimited consumer
	// returns Ok and never asks to disconnect.
	for i := 0; i < 50; i++ {
		if d := c.Charge(NewCharge(DropThreshold+1, "huge"), ""); d != Ok {
			t.Fatalf("unlimited returned %v, want Ok", d)
		}
		if c.Disconnect() {
			t.Fatalf("unlimited Disconnect()=true")
		}
		clk.Advance(time.Second)
	}
}

func TestInbound_KeyShareAcrossReconnect(t *testing.T) {
	m, _ := newTestManager()

	c1 := m.NewInboundEndpoint("192.0.2.20:51000")
	// Pump enough cost that the post-decay normalized value is
	// non-trivial even after Release ages the entry briefly.
	c1.Charge(NewCharge(WarningThreshold*DecayWindowSeconds, "burst"), "")
	prior := c1.Balance()
	c1.Release()
	if prior <= 0 {
		t.Fatalf("c1 balance after burst = %d", prior)
	}

	// Same IP, different ephemeral port — must inherit the prior
	// balance (this is what makes the system robust to flap-and-retry
	// abuse where a peer reconnects from a new ephemeral port).
	c2 := m.NewInboundEndpoint("192.0.2.20:60000")
	defer c2.Release()
	if c2.Balance() < prior/2 {
		t.Fatalf("reconnect balance=%d want >= %d (prior=%d)", c2.Balance(), prior/2, prior)
	}
}

func TestOutbound_KeyIncludesPort(t *testing.T) {
	m, _ := newTestManager()

	c1 := m.NewOutboundEndpoint("192.0.2.30:51235")
	c1.Charge(NewCharge(WarningThreshold*DecayWindowSeconds, "burst"), "")
	c1.Release()

	// Different outbound port → distinct keying.
	c2 := m.NewOutboundEndpoint("192.0.2.30:51236")
	defer c2.Release()
	if c2.Balance() != 0 {
		t.Fatalf("fresh outbound port carried balance=%d, want 0", c2.Balance())
	}
}

func TestPeriodicActivity_ExpiresInactiveEntries(t *testing.T) {
	m, clk := newTestManager()
	c := m.NewInboundEndpoint("192.0.2.40")
	c.Charge(FeeInvalidData, "")
	c.Release()

	if m.EntryCount() == 0 {
		t.Fatalf("entry erased before periodic activity")
	}
	clk.Advance(SecondsUntilExpiration + time.Second)
	m.PeriodicActivity()
	if m.EntryCount() != 0 {
		t.Fatalf("entry count after expiry = %d, want 0", m.EntryCount())
	}
}

func TestGossip_ExportImport(t *testing.T) {
	m, _ := newTestManager()

	// Build a Consumer with balance over MinimumGossipBalance.
	c := m.NewInboundEndpoint("192.0.2.50")
	c.Charge(NewCharge(MinimumGossipBalance*DecayWindowSeconds*2, "seed"), "")
	defer c.Release()

	g := m.ExportConsumers()
	if len(g.Items) == 0 {
		t.Fatalf("no items exported")
	}

	other, _ := newTestManager()
	other.ImportConsumers("origin-a", g)
	// Imported balance must show up in the remote half of the
	// consumer it targets.
	c2 := other.NewInboundEndpoint("192.0.2.50")
	defer c2.Release()
	if c2.Balance() < MinimumGossipBalance {
		t.Fatalf("imported balance=%d want >= %d", c2.Balance(), MinimumGossipBalance)
	}
}

func TestConcurrent_ChargesAreSafe(t *testing.T) {
	m, _ := newTestManager()
	c := m.NewInboundEndpoint("192.0.2.60")
	defer c.Release()

	var wg sync.WaitGroup
	var charges atomic.Uint64
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				c.Charge(FeeInvalidData, "")
				charges.Add(1)
			}
		}()
	}
	wg.Wait()
	if charges.Load() != 1600 {
		t.Fatalf("charges=%d want 1600", charges.Load())
	}
	// Balance must be positive; exact value is decay-dependent.
	if c.Balance() <= 0 {
		t.Fatalf("balance after concurrent load = %d", c.Balance())
	}
}

func TestStartStop_Idempotent(t *testing.T) {
	m, _ := newTestManager()
	m.Start()
	m.Start() // second call is a no-op
	m.Stop()
	m.Stop() // second call is a no-op
}
