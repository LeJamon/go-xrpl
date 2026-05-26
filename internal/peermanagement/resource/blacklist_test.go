package resource

import "testing"

func TestSnapshot_FiltersByThreshold(t *testing.T) {
	m, _ := newTestManager()

	c := m.NewInboundEndpoint("192.0.2.50:51000")
	defer c.Release()
	// The decaying sample scales a charge down by the decay window, so a
	// burst sized by DecayWindowSeconds yields a steady-state value near
	// WarningThreshold (mirrors the burst pattern in manager_test.go).
	c.Charge(NewCharge(WarningThreshold*DecayWindowSeconds, "load"), "")

	// At threshold 0 the loaded endpoint is listed, keyed by host (the
	// inbound port is normalized away) with the inbound type label.
	low := m.Snapshot(0)
	if len(low) != 1 {
		t.Fatalf("expected 1 entry at threshold 0, got %d", len(low))
	}
	got := low[0]
	if got.Address != "192.0.2.50" {
		t.Errorf("address = %q, want host without port", got.Address)
	}
	if got.Type != "inbound" {
		t.Errorf("type = %q, want inbound", got.Type)
	}
	if got.Local <= 0 {
		t.Errorf("local = %d, want positive balance", got.Local)
	}

	// A threshold above the combined balance filters the endpoint out.
	if hi := m.Snapshot(got.Local + got.Remote + 1); len(hi) != 0 {
		t.Errorf("expected 0 entries above balance, got %d", len(hi))
	}
}
