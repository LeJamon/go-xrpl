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

func TestSnapshot_ExcludesReleasedEntries(t *testing.T) {
	m, _ := newTestManager()

	c := m.NewInboundEndpoint("203.0.113.9:40000")
	c.Charge(NewCharge(WarningThreshold*DecayWindowSeconds, "load"), "")

	// While the Consumer is held the endpoint is active and listed.
	if got := m.Snapshot(0); len(got) != 1 {
		t.Fatalf("active endpoint: expected 1 entry, got %d", len(got))
	}

	// Releasing the last Consumer drops the refcount to 0. The entry stays
	// resident (so a reconnect inherits its balance) but, like rippled moving
	// it into inactive_, it must no longer appear in the black_list snapshot.
	c.Release()
	if got := m.Snapshot(0); len(got) != 0 {
		t.Errorf("released endpoint should be excluded, got %d entries", len(got))
	}
}

func TestSnapshot_AdminEndpointKeyedWithPortOne(t *testing.T) {
	m, _ := newTestManager()

	// An unlimited (admin/cluster) endpoint is keyed at port 1, matching
	// rippled's at_port(1), so it never collides with the port-0 inbound key
	// for the same host. It surfaces at threshold 0 even with a zero balance.
	c := m.NewUnlimitedEndpoint("198.51.100.7:51235")
	defer c.Release()

	got := m.Snapshot(0)
	if len(got) != 1 {
		t.Fatalf("expected 1 admin entry, got %d", len(got))
	}
	if got[0].Address != "198.51.100.7:1" {
		t.Errorf("address = %q, want host with admin port :1", got[0].Address)
	}
	if got[0].Type != "admin" {
		t.Errorf("type = %q, want admin", got[0].Type)
	}
}
