package peermanagement

import "testing"

// TestDefaultConfig_ReduceRelayOptIn pins Task 4.4 (G5): rippled ships
// with reduce-relay disabled by default — `Config.h:248` sets
// `VP_REDUCE_RELAY_BASE_SQUELCH_ENABLE = false`, `Config.h:258` sets
// `TX_REDUCE_RELAY_ENABLE = false`, and `Config.cpp:755-762` preserves
// that default when the .cfg lacks the section. Pre-G5, goXRPL shipped
// with `EnableReduceRelay: true` in DefaultConfig(), which the
// Validate() cascade propagated to both EnableVPReduceRelay and
// EnableTxReduceRelay. A stock goXRPL node joining a stock rippled
// network would therefore advertise vprr+txrr in the handshake and
// engage slot squelching aggressively while every other peer sat
// silent — a deployment-parity hazard.
//
// This test locks in the rippled-matching opt-in posture: all three
// fields must be false out of DefaultConfig(). If a future change
// flips any of them back on, a reviewer sees why that matters via
// this test name.
func TestDefaultConfig_ReduceRelayOptIn(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.EnableReduceRelay {
		t.Errorf("EnableReduceRelay default = true, want false (rippled Config.h:248 defaults reduce-relay off)")
	}
	if cfg.EnableVPReduceRelay {
		t.Errorf("EnableVPReduceRelay default = true, want false (rippled VP_REDUCE_RELAY_BASE_SQUELCH_ENABLE defaults to false)")
	}
	if cfg.EnableTxReduceRelay {
		t.Errorf("EnableTxReduceRelay default = true, want false (rippled TX_REDUCE_RELAY_ENABLE defaults to false)")
	}
}

// TestDefaultConfig_ReduceRelayCascadePreservedForExplicitOptIn pins
// the operator path: once a caller explicitly sets the legacy
// EnableReduceRelay flag, Validate() must still propagate it to both
// specific toggles. The cascade is dead code in the default path
// (since the default is now false) but is load-bearing for anyone
// who opts in via the legacy knob — either a config file using the
// old single-bool or a WithReduceRelay(true) option call.
func TestDefaultConfig_ReduceRelayCascadePreservedForExplicitOptIn(t *testing.T) {
	cfg := DefaultConfig()
	cfg.EnableReduceRelay = true

	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate returned error: %v", err)
	}

	if !cfg.EnableVPReduceRelay {
		t.Errorf("legacy EnableReduceRelay=true did not cascade to EnableVPReduceRelay")
	}
	if !cfg.EnableTxReduceRelay {
		t.Errorf("legacy EnableReduceRelay=true did not cascade to EnableTxReduceRelay")
	}
}
