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

// TestRelay_DeleteIdlePeers_EvictsStaleEntries pins G2: the periodic
// sweep must evict peers whose lastMessage is older than Idled, and
// drop the slot entirely when no peers remain. Without this, r.slots
// only shrinks on explicit RemovePeer and leaks entries for validators
// we no longer see.
//
// Mirrors rippled's Slot::deleteIdlePeer + Slots::deleteIdlePeers
// (Slot.h:262-283, 821-839). Rippled drives the sweep every 4s from
// the once-per-second overlay timer (OverlayImpl.cpp:107-111 +
// Tuning::checkIdlePeers=4).
func TestRelay_DeleteIdlePeers_EvictsStaleEntries(t *testing.T) {
	cfg := &Config{
		EnableReduceRelay: true,
		Clock:             time.Now,
	}
	mock := newMockSquelchCallback()
	relay := NewRelay(cfg, mock.callback)
	relay.startTime = time.Now().Add(-WaitOnBootup - time.Minute)

	validator := []byte("test-validator-idle")

	// Seed a single peer so a slot is created with exactly one entry.
	relay.OnMessage(validator, PeerID(1))

	relay.mu.RLock()
	slot, ok := relay.slots[string(validator)]
	relay.mu.RUnlock()
	if !ok {
		t.Fatalf("slot was not created by OnMessage")
	}

	// Precondition: peer is present.
	slot.mu.RLock()
	_, exists := slot.peers[PeerID(1)]
	slot.mu.RUnlock()
	if !exists {
		t.Fatalf("precondition: PeerID(1) should exist before sweep")
	}

	// Advance the sweep clock past Idled from the peer's lastMessage.
	// The slot stored peer.LastMessage ~= time.Now() inside OnMessage.
	// Passing a now that is Idled+1s in the future triggers eviction.
	sweepNow := time.Now().Add(Idled + time.Second)
	relay.deleteIdlePeers(sweepNow)

	// The slot had exactly one peer, and that peer was idle — the slot
	// should have been deleted from r.slots after the sweep.
	relay.mu.RLock()
	_, stillThere := relay.slots[string(validator)]
	slotCount := len(relay.slots)
	relay.mu.RUnlock()
	if stillThere {
		t.Fatalf("slot should have been deleted after its only peer idled out; %d slots remain", slotCount)
	}
}

// TestSlot_IgnoredSquelch_FiresCallback pins G4: when a peer in
// RelayPeerSquelched state continues to relay a validator's messages,
// the slot must invoke its ignored-squelch callback with the offending
// peer's ID so the overlay can charge reputation. Mirrors rippled's
// Slot::update firing the ignored_squelch_callback at Slot.h:329-331.
//
// Without this the only enforcement of squelch is advisory — a peer
// that ignores our TMSquelch frames keeps wasting bandwidth with zero
// accountability.
func TestSlot_IgnoredSquelch_FiresCallback(t *testing.T) {
	mock := newMockSquelchCallback()
	slot := NewValidatorSlot(3, mock.callback)

	var (
		charges   []PeerID
		chargesMu sync.Mutex
	)
	slot.onIgnoredSquelch = func(peerID PeerID) {
		chargesMu.Lock()
		defer chargesMu.Unlock()
		charges = append(charges, peerID)
	}

	validator := []byte("test-validator-squelch-ignored")

	// Install PeerID(7) directly in RelayPeerSquelched state with an
	// Expire far in the future so Update does NOT treat it as
	// expired-and-return-to-counting. LastMessage set to now so the
	// is-fresh check succeeds.
	now := time.Now()
	slot.mu.Lock()
	slot.peers[PeerID(7)] = &RelayPeerInfo{
		State:       RelayPeerSquelched,
		Expire:      now.Add(10 * time.Minute),
		LastMessage: now,
	}
	slot.mu.Unlock()

	// Feed a message from the squelched peer. Update must fire the
	// ignored-squelch callback exactly once, attributed to PeerID(7).
	slot.Update(validator, PeerID(7))

	chargesMu.Lock()
	defer chargesMu.Unlock()
	if len(charges) != 1 {
		t.Fatalf("expected exactly 1 ignored-squelch charge, got %d: %v", len(charges), charges)
	}
	if charges[0] != PeerID(7) {
		t.Fatalf("charge attributed to wrong peer: got %v, want PeerID(7)", charges[0])
	}
}

// TestSlot_UnsquelchedRelay_DoesNotCharge pins the negative baseline
// for G4: a peer NOT in RelayPeerSquelched state must never trigger
// the ignored-squelch callback, regardless of slot state or message
// volume. Guards against a latent bug where the charge path fires on
// every incoming message and inflates bad-data balances for well-
// behaved peers.
func TestSlot_UnsquelchedRelay_DoesNotCharge(t *testing.T) {
	mock := newMockSquelchCallback()
	slot := NewValidatorSlot(3, mock.callback)

	var chargeCount int
	slot.onIgnoredSquelch = func(peerID PeerID) {
		chargeCount++
	}

	validator := []byte("test-validator-ok")

	// Drive a handful of messages from a single unsquelched peer —
	// the Update path transitions RelayPeerCounting → considered →
	// (not enough peers to select) but never enters Squelched for
	// this peer. Charge must stay at 0.
	for i := 0; i < 5; i++ {
		slot.Update(validator, PeerID(1))
	}

	if chargeCount != 0 {
		t.Fatalf("unsquelched peer must not trigger ignored-squelch callback; got %d calls", chargeCount)
	}

	// Also verify a Selected peer does not trigger the callback.
	// Install PeerID(2) directly in RelayPeerSelected state.
	now := time.Now()
	slot.mu.Lock()
	slot.peers[PeerID(2)] = &RelayPeerInfo{
		State:       RelayPeerSelected,
		LastMessage: now,
	}
	slot.state = RelaySlotSelected
	slot.mu.Unlock()

	slot.Update(validator, PeerID(2))
	if chargeCount != 0 {
		t.Fatalf("selected peer must not trigger ignored-squelch callback; got %d calls", chargeCount)
	}
}

// TestSlot_IgnoredSquelch_ExpiredDoesNotCharge pins G4's expired-
// squelch behavior matching rippled Slot.h:307-314: when a squelched
// peer's Expire has passed, Update must transition it back to
// Counting and NOT fire the ignored-squelch callback — the squelch
// window itself is over, so the relay is no longer "ignored."
func TestSlot_IgnoredSquelch_ExpiredDoesNotCharge(t *testing.T) {
	mock := newMockSquelchCallback()
	slot := NewValidatorSlot(3, mock.callback)

	var chargeCount int
	slot.onIgnoredSquelch = func(peerID PeerID) {
		chargeCount++
	}

	validator := []byte("test-validator-expired")

	// Squelched with Expire already in the past.
	now := time.Now()
	slot.mu.Lock()
	slot.peers[PeerID(9)] = &RelayPeerInfo{
		State:       RelayPeerSquelched,
		Expire:      now.Add(-time.Second),
		LastMessage: now,
	}
	slot.mu.Unlock()

	slot.Update(validator, PeerID(9))

	if chargeCount != 0 {
		t.Fatalf("expired-squelch relay must not charge; got %d calls", chargeCount)
	}

	// Peer should have transitioned to Counting.
	slot.mu.RLock()
	state := slot.peers[PeerID(9)].State
	slot.mu.RUnlock()
	if state != RelayPeerCounting {
		t.Fatalf("expected peer state Counting after expiry, got %v", state)
	}
}

// TestRelay_SquelchedPeerRelayChargesPeer is the overlay-level G4
// assertion: when the Relay layer sees a message from a peer it has
// already marked Squelched for a given validator, it must propagate
// that event to the overlay-supplied ignored-squelch callback so the
// peer's bad-data balance rises.
func TestRelay_SquelchedPeerRelayChargesPeer(t *testing.T) {
	var now time.Time
	start := time.Now()
	now = start
	clock := func() time.Time { return now }

	cfg := &Config{
		EnableVPReduceRelay: true,
		Clock:               clock,
	}

	var (
		charges   []PeerID
		chargesMu sync.Mutex
	)
	ignoredCb := func(peerID PeerID) {
		chargesMu.Lock()
		defer chargesMu.Unlock()
		charges = append(charges, peerID)
	}

	mock := newMockSquelchCallback()
	relay := NewRelayWithIgnoredCallback(cfg, mock.callback, ignoredCb)

	// Advance past bootup so OnMessage is active.
	now = start.Add(WaitOnBootup + time.Minute)

	validator := []byte("test-validator-charge")

	// Seed a slot with PeerID(42) already in Squelched state with an
	// Expire far in the future so Update's expired-squelch branch
	// doesn't mask the ignored-relay callback.
	relay.OnMessage(validator, PeerID(42))

	relay.mu.RLock()
	slot := relay.slots[string(validator)]
	relay.mu.RUnlock()
	if slot == nil {
		t.Fatalf("slot was not created by OnMessage")
	}
	slot.mu.Lock()
	slot.peers[PeerID(42)] = &RelayPeerInfo{
		State:       RelayPeerSquelched,
		Expire:      now.Add(10 * time.Minute),
		LastMessage: now,
	}
	slot.mu.Unlock()

	// The squelched peer keeps relaying. Expect exactly one charge.
	relay.OnMessage(validator, PeerID(42))

	chargesMu.Lock()
	defer chargesMu.Unlock()
	if len(charges) != 1 || charges[0] != PeerID(42) {
		t.Fatalf("expected one charge for PeerID(42), got: %v", charges)
	}
}

// TestRelay_UnsquelchedPeerRelayDoesNotCharge is the negative G4
// baseline at the Relay level: routine traffic from an unsquelched
// peer must never trigger the ignored-squelch callback.
func TestRelay_UnsquelchedPeerRelayDoesNotCharge(t *testing.T) {
	var now time.Time
	start := time.Now()
	now = start
	clock := func() time.Time { return now }

	cfg := &Config{
		EnableVPReduceRelay: true,
		Clock:               clock,
	}

	var charges int
	ignoredCb := func(peerID PeerID) { charges++ }

	mock := newMockSquelchCallback()
	relay := NewRelayWithIgnoredCallback(cfg, mock.callback, ignoredCb)

	now = start.Add(WaitOnBootup + time.Minute)

	validator := []byte("test-validator-ok")

	// A handful of messages from one unsquelched peer.
	for i := 0; i < 5; i++ {
		relay.OnMessage(validator, PeerID(1))
	}

	if charges != 0 {
		t.Fatalf("unsquelched peer must not be charged; got %d calls", charges)
	}
}

// TestRelay_DeleteIdlePeers_DemotesSelectedBelowQuorum pins G2's
// safety rule: if a slot was in Selected state and the sweep reduces
// the remaining peer count below MaxSelectedPeers, the slot must be
// demoted back to Counting so future updates can re-select. Without
// this, a slot stays "Selected" pointing at peers that have all gone
// silent, and the relay never retries selection for that validator.
func TestRelay_DeleteIdlePeers_DemotesSelectedBelowQuorum(t *testing.T) {
	mock := newMockSquelchCallback()
	slot := NewValidatorSlot(5, mock.callback)

	// Install 5 peers directly as Selected (bypassing the counting
	// state machine) so the slot starts in RelaySlotSelected. Use a
	// recent lastMessage for 2 of them and a stale lastMessage for 3
	// so the sweep evicts three and leaves two.
	now := time.Now()
	fresh := now.Add(-time.Second)             // within Idled window
	stale := now.Add(-(Idled + 2*time.Second)) // past Idled

	slot.mu.Lock()
	slot.peers[PeerID(1)] = &RelayPeerInfo{State: RelayPeerSelected, LastMessage: fresh}
	slot.peers[PeerID(2)] = &RelayPeerInfo{State: RelayPeerSelected, LastMessage: fresh}
	slot.peers[PeerID(3)] = &RelayPeerInfo{State: RelayPeerSelected, LastMessage: stale}
	slot.peers[PeerID(4)] = &RelayPeerInfo{State: RelayPeerSelected, LastMessage: stale}
	slot.peers[PeerID(5)] = &RelayPeerInfo{State: RelayPeerSelected, LastMessage: stale}
	slot.state = RelaySlotSelected
	slot.mu.Unlock()

	// Register the slot in a Relay so we exercise the aggregator path.
	cfg := &Config{
		EnableReduceRelay: true,
		Clock:             time.Now,
	}
	relay := NewRelay(cfg, mock.callback)
	validator := []byte("test-validator-demote")
	relay.mu.Lock()
	relay.slots[string(validator)] = slot
	relay.mu.Unlock()

	relay.deleteIdlePeers(now)

	// Slot must still exist (two fresh peers remain) but must have
	// been demoted to Counting: 2 < MaxSelectedPeers=5.
	relay.mu.RLock()
	_, stillThere := relay.slots[string(validator)]
	relay.mu.RUnlock()
	if !stillThere {
		t.Fatalf("slot should remain: 2 fresh peers were not evicted")
	}

	slot.mu.RLock()
	state := slot.state
	remaining := len(slot.peers)
	slot.mu.RUnlock()

	if remaining != 2 {
		t.Fatalf("expected 2 remaining peers after sweep, got %d", remaining)
	}
	if state != RelaySlotCounting {
		t.Fatalf("slot state = %v; want RelaySlotCounting after dropping below MaxSelectedPeers", state)
	}
}
