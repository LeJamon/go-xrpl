package peermanagement

import (
	"sync"
	"testing"
	"time"
)

// mockSquelchCallback tracks squelch/unsquelch calls for testing
type mockSquelchCallback struct {
	mu          sync.Mutex
	squelched   map[PeerID]time.Duration
	unsquelched []PeerID
}

func newMockSquelchCallback() *mockSquelchCallback {
	return &mockSquelchCallback{
		squelched: make(map[PeerID]time.Duration),
	}
}

func (m *mockSquelchCallback) callback(validator []byte, peerID PeerID, squelch bool, duration time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if squelch {
		m.squelched[peerID] = duration
	} else {
		m.unsquelched = append(m.unsquelched, peerID)
	}
}

func TestValidatorSlotUpdate(t *testing.T) {
	mock := newMockSquelchCallback()
	slot := NewValidatorSlot(3, mock.callback)

	validator := []byte("test-validator")

	// Add first peer
	slot.Update(validator, PeerID(1))

	// Verify peer was added
	peers := slot.GetSelected()
	// Initially no peers selected (still in counting state)
	if len(peers) != 0 {
		t.Errorf("Expected 0 selected peers initially, got %d", len(peers))
	}
}

func TestValidatorSlotDeletePeer(t *testing.T) {
	mock := newMockSquelchCallback()
	slot := NewValidatorSlot(3, mock.callback)

	validator := []byte("test-validator")

	// Add peers
	for i := 1; i <= 5; i++ {
		slot.Update(validator, PeerID(i))
	}

	// Delete a peer
	slot.DeletePeer(validator, PeerID(1), true)

	// Peer 1 should be gone
	slot.mu.RLock()
	_, exists := slot.peers[PeerID(1)]
	slot.mu.RUnlock()

	if exists {
		t.Error("Peer 1 should have been deleted")
	}
}

func TestRelayOnMessage(t *testing.T) {
	cfg := &Config{
		EnableReduceRelay: true,
		Clock:             time.Now,
	}

	mock := newMockSquelchCallback()
	relay := NewRelay(cfg, mock.callback)

	// Set start time to past so we're past bootup wait
	relay.startTime = time.Now().Add(-WaitOnBootup - time.Minute)

	validator := []byte("test-validator")

	// Send messages from multiple peers
	for i := 1; i <= 10; i++ {
		relay.OnMessage(validator, PeerID(i))
	}

	// Check that a slot was created
	relay.mu.RLock()
	slotCount := len(relay.slots)
	relay.mu.RUnlock()

	if slotCount != 1 {
		t.Errorf("Expected 1 slot, got %d", slotCount)
	}
}

func TestRelayDisabled(t *testing.T) {
	cfg := &Config{
		EnableReduceRelay: false, // Disabled
		Clock:             time.Now,
	}

	mock := newMockSquelchCallback()
	relay := NewRelay(cfg, mock.callback)

	validator := []byte("test-validator")

	// Messages should be ignored when disabled
	relay.OnMessage(validator, PeerID(1))

	relay.mu.RLock()
	slotCount := len(relay.slots)
	relay.mu.RUnlock()

	if slotCount != 0 {
		t.Errorf("Expected 0 slots when disabled, got %d", slotCount)
	}
}

// TestRelay_VPRROnly_Activates pins R6.3: when only the specific
// VPRR flag is set (legacy EnableReduceRelay left false, e.g. an
// operator who wants VPRR without TXRR), the Relay engine must still
// activate. Pre-R6.3 the OnMessage gate checked only the legacy
// flag, so an advertised-VPRR configuration would silently skip
// selection entirely.
func TestRelay_VPRROnly_Activates(t *testing.T) {
	// Advanceable clock: Relay captures startTime at construction,
	// so OnMessage's WaitOnBootup gate needs clock() - startTime to
	// exceed the bootup wait. We advance AFTER NewRelay returns.
	var now time.Time
	start := time.Now()
	now = start
	clock := func() time.Time { return now }

	cfg := &Config{
		// Legacy omnibus flag OFF; only specific VPRR flag ON.
		EnableReduceRelay:   false,
		EnableVPReduceRelay: true,
		Clock:               clock,
	}
	mock := newMockSquelchCallback()
	relay := NewRelay(cfg, mock.callback)

	// Advance past WaitOnBootup so OnMessage accepts the traffic.
	now = start.Add(WaitOnBootup + time.Minute)

	validator := []byte("test-validator-vprr-only")
	relay.OnMessage(validator, PeerID(1))

	relay.mu.RLock()
	slotCount := len(relay.slots)
	relay.mu.RUnlock()

	if slotCount != 1 {
		t.Fatalf("VPRR-only config must activate Relay: expected 1 slot, got %d", slotCount)
	}
}

// TestRelay_BothFlagsOff_StaysDormant guards the negative case: with
// neither flag set, Relay must stay dormant.
func TestRelay_BothFlagsOff_StaysDormant(t *testing.T) {
	var now time.Time
	start := time.Now()
	now = start
	clock := func() time.Time { return now }

	cfg := &Config{
		EnableReduceRelay:   false,
		EnableVPReduceRelay: false,
		Clock:               clock,
	}
	mock := newMockSquelchCallback()
	relay := NewRelay(cfg, mock.callback)

	now = start.Add(WaitOnBootup + time.Minute)
	relay.OnMessage([]byte("validator"), PeerID(1))

	relay.mu.RLock()
	slotCount := len(relay.slots)
	relay.mu.RUnlock()

	if slotCount != 0 {
		t.Fatalf("both flags off must leave Relay dormant: got %d slots", slotCount)
	}
}

func TestRelayRemovePeer(t *testing.T) {
	cfg := &Config{
		EnableReduceRelay: true,
		Clock:             time.Now,
	}

	mock := newMockSquelchCallback()
	relay := NewRelay(cfg, mock.callback)
	relay.startTime = time.Now().Add(-WaitOnBootup - time.Minute)

	validator := []byte("test-validator")

	// Add peer via message
	relay.OnMessage(validator, PeerID(1))

	// Remove peer
	relay.RemovePeer(PeerID(1))

	// Peer should be removed from slot
	selected := relay.GetSelectedPeers(validator)
	for _, p := range selected {
		if p == PeerID(1) {
			t.Error("Peer 1 should have been removed")
		}
	}
}

func TestRelayBootupWait(t *testing.T) {
	cfg := &Config{
		EnableReduceRelay: true,
		Clock:             time.Now,
	}

	mock := newMockSquelchCallback()
	relay := NewRelay(cfg, mock.callback)

	// Start time is now, so we're within bootup wait
	validator := []byte("test-validator")

	relay.OnMessage(validator, PeerID(1))

	// Should not create slot during bootup
	relay.mu.RLock()
	slotCount := len(relay.slots)
	relay.mu.RUnlock()

	if slotCount != 0 {
		t.Errorf("Expected 0 slots during bootup, got %d", slotCount)
	}
}

func TestRelayConstants(t *testing.T) {
	// Verify constants match rippled values
	if MinUnsquelchExpire != 300*time.Second {
		t.Errorf("MinUnsquelchExpire = %v, want 300s", MinUnsquelchExpire)
	}

	if MaxUnsquelchExpireDefault != 600*time.Second {
		t.Errorf("MaxUnsquelchExpireDefault = %v, want 600s", MaxUnsquelchExpireDefault)
	}

	if SquelchPerPeer != 10*time.Second {
		t.Errorf("SquelchPerPeer = %v, want 10s", SquelchPerPeer)
	}

	if MaxSelectedPeers != 5 {
		t.Errorf("MaxSelectedPeers = %d, want 5", MaxSelectedPeers)
	}

	if WaitOnBootup != 10*time.Minute {
		t.Errorf("WaitOnBootup = %v, want 10m", WaitOnBootup)
	}

	if MinMessageThreshold != 19 {
		t.Errorf("MinMessageThreshold = %d, want 19", MinMessageThreshold)
	}

	if MaxMessageThreshold != 20 {
		t.Errorf("MaxMessageThreshold = %d, want 20", MaxMessageThreshold)
	}
}

func TestRelayPeerStateString(t *testing.T) {
	tests := []struct {
		state    RelayPeerState
		expected string
	}{
		{RelayPeerCounting, "counting"},
		{RelayPeerSelected, "selected"},
		{RelayPeerSquelched, "squelched"},
		{RelayPeerState(99), "unknown"},
	}

	for _, tc := range tests {
		if tc.state.String() != tc.expected {
			t.Errorf("RelayPeerState(%d).String() = %s, expected %s", tc.state, tc.state.String(), tc.expected)
		}
	}
}

func TestValidatorSlotGetSelected(t *testing.T) {
	mock := newMockSquelchCallback()
	slot := NewValidatorSlot(3, mock.callback)

	// Initially no peers selected
	selected := slot.GetSelected()
	if len(selected) != 0 {
		t.Errorf("Expected 0 selected peers initially, got %d", len(selected))
	}

	// Manually set a peer as selected for testing
	slot.mu.Lock()
	slot.peers[PeerID(1)] = &RelayPeerInfo{State: RelayPeerSelected}
	slot.peers[PeerID(2)] = &RelayPeerInfo{State: RelayPeerSquelched}
	slot.peers[PeerID(3)] = &RelayPeerInfo{State: RelayPeerSelected}
	slot.mu.Unlock()

	selected = slot.GetSelected()
	if len(selected) != 2 {
		t.Errorf("Expected 2 selected peers, got %d", len(selected))
	}
}
