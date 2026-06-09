package peermanagement

import (
	"net"
	"testing"
	"time"
)

func TestNewDiscovery(t *testing.T) {
	cfg := &Config{
		MaxPeers:    50,
		MaxInbound:  25,
		MaxOutbound: 25,
	}
	events := make(chan Event, 10)
	d := NewDiscovery(cfg, events)

	if d == nil {
		t.Fatal("NewDiscovery returned nil")
	}
}

func TestDiscoveryAddPeer(t *testing.T) {
	cfg := &Config{
		MaxPeers:    50,
		MaxInbound:  25,
		MaxOutbound: 25,
	}
	events := make(chan Event, 10)
	d := NewDiscovery(cfg, events)

	d.AddPeer("192.168.1.1:51235", 0, 0)
	d.AddPeer("192.168.1.2:51235", 1, 1)

	d.mu.RLock()
	count := len(d.peers)
	d.mu.RUnlock()

	if count != 2 {
		t.Errorf("PeerCount = %d, want 2", count)
	}
}

func TestDiscoveryAddPeerUpdateHops(t *testing.T) {
	cfg := &Config{
		MaxPeers:    50,
		MaxInbound:  25,
		MaxOutbound: 25,
	}
	events := make(chan Event, 10)
	d := NewDiscovery(cfg, events)

	// Add with high hop count
	d.AddPeer("192.168.1.1:51235", 3, 1)

	d.mu.RLock()
	peer := d.peers["192.168.1.1:51235"]
	initialHops := peer.Hops
	d.mu.RUnlock()

	if initialHops != 3 {
		t.Errorf("Hops = %d, want 3", initialHops)
	}

	// Update with lower hop count
	d.AddPeer("192.168.1.1:51235", 1, 2)

	d.mu.RLock()
	peer = d.peers["192.168.1.1:51235"]
	updatedHops := peer.Hops
	d.mu.RUnlock()

	if updatedHops != 1 {
		t.Errorf("Hops = %d, want 1 after update", updatedHops)
	}
}

func TestDiscoveryMarkConnected(t *testing.T) {
	cfg := &Config{
		MaxPeers:    50,
		MaxInbound:  25,
		MaxOutbound: 25,
	}
	events := make(chan Event, 10)
	d := NewDiscovery(cfg, events)

	d.AddPeer("192.168.1.1:51235", 0, 0)
	d.MarkConnected("192.168.1.1:51235", PeerID(1))

	if d.ConnectedCount() != 1 {
		t.Errorf("ConnectedCount = %d, want 1", d.ConnectedCount())
	}

	d.mu.RLock()
	peer := d.peers["192.168.1.1:51235"]
	connected := peer.Connected
	peerID := peer.PeerID
	d.mu.RUnlock()

	if !connected {
		t.Error("Peer should be marked as connected")
	}
	if peerID != PeerID(1) {
		t.Errorf("PeerID = %d, want 1", peerID)
	}
}

func TestDiscoveryMarkDisconnected(t *testing.T) {
	cfg := &Config{
		MaxPeers:    50,
		MaxInbound:  25,
		MaxOutbound: 25,
	}
	events := make(chan Event, 10)
	d := NewDiscovery(cfg, events)

	d.AddPeer("192.168.1.1:51235", 0, 0)
	d.MarkConnected("192.168.1.1:51235", PeerID(1))
	d.MarkDisconnected(PeerID(1))

	if d.ConnectedCount() != 0 {
		t.Errorf("ConnectedCount = %d, want 0", d.ConnectedCount())
	}

	d.mu.RLock()
	peer := d.peers["192.168.1.1:51235"]
	connected := peer.Connected
	d.mu.RUnlock()

	if connected {
		t.Error("Peer should be marked as disconnected")
	}
}

func TestDiscoveryNeedsMorePeers(t *testing.T) {
	cfg := &Config{
		MaxPeers:    50,
		MaxInbound:  25,
		MaxOutbound: 3,
	}
	events := make(chan Event, 10)
	d := NewDiscovery(cfg, events)

	if !d.NeedsMorePeers() {
		t.Error("Should need more peers when none connected")
	}

	d.MarkConnected("192.168.1.1:51235", PeerID(1))
	d.MarkConnected("192.168.1.2:51235", PeerID(2))
	d.MarkConnected("192.168.1.3:51235", PeerID(3))

	if d.NeedsMorePeers() {
		t.Error("Should not need more peers when at max outbound")
	}
}

func TestDiscoverySelectPeersToConnect(t *testing.T) {
	cfg := &Config{
		MaxPeers:    50,
		MaxInbound:  25,
		MaxOutbound: 25,
	}
	events := make(chan Event, 10)
	d := NewDiscovery(cfg, events)

	// Add some peers
	d.AddPeer("192.168.1.1:51235", 0, 0)
	d.AddPeer("192.168.1.2:51235", 1, 0)
	d.AddPeer("192.168.1.3:51235", 2, 0)
	d.AddPeer("192.168.1.4:51235", 10, 0) // Too many hops

	// Mark one as connected
	d.MarkConnected("192.168.1.1:51235", PeerID(1))

	candidates := d.SelectPeersToConnect(3)

	// Should not include connected peer or high-hop peer
	for _, addr := range candidates {
		if addr == "192.168.1.1:51235" {
			t.Error("Should not select already connected peer")
		}
		if addr == "192.168.1.4:51235" {
			t.Error("Should not select peer with too many hops")
		}
	}
}

func TestDiscoveryBootstrapPeers(t *testing.T) {
	cfg := &Config{
		MaxPeers:       50,
		MaxInbound:     25,
		MaxOutbound:    25,
		BootstrapPeers: []string{"192.168.1.1:51235", "192.168.1.2:51235"},
	}
	events := make(chan Event, 10)
	d := NewDiscovery(cfg, events)

	ctx := t.Context()

	err := d.Start(ctx)
	if err != nil {
		t.Fatalf("Start error: %v", err)
	}

	// Bootstrap peers should be added
	d.mu.RLock()
	count := len(d.peers)
	d.mu.RUnlock()

	if count != 2 {
		t.Errorf("PeerCount = %d, want 2", count)
	}

	d.Stop()
}

func TestNewInboundSlot(t *testing.T) {
	localAddr := &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 51235}
	remoteAddr := &net.TCPAddr{IP: net.ParseIP("192.168.1.1"), Port: 51235}

	slot := NewInboundSlot(localAddr, remoteAddr, false)

	if !slot.Inbound() {
		t.Error("Slot should be inbound")
	}

	if slot.Fixed() {
		t.Error("Slot should not be fixed")
	}

	if slot.State() != SlotStateAccept {
		t.Errorf("Expected SlotStateAccept, got %v", slot.State())
	}
}

func TestNewOutboundSlot(t *testing.T) {
	remoteAddr := &net.TCPAddr{IP: net.ParseIP("192.168.1.1"), Port: 51235}

	slot := NewOutboundSlot(remoteAddr, true)

	if slot.Inbound() {
		t.Error("Slot should not be inbound")
	}

	if !slot.Fixed() {
		t.Error("Slot should be fixed")
	}

	if slot.State() != SlotStateConnect {
		t.Errorf("Expected SlotStateConnect, got %v", slot.State())
	}
}

func TestSlotStateTransitions(t *testing.T) {
	remoteAddr := &net.TCPAddr{IP: net.ParseIP("192.168.1.1"), Port: 51235}
	slot := NewOutboundSlot(remoteAddr, false)

	// Initial state
	if slot.State() != SlotStateConnect {
		t.Errorf("Expected SlotStateConnect, got %v", slot.State())
	}

	// Transition to connected
	slot.SetState(SlotStateConnected)
	if slot.State() != SlotStateConnected {
		t.Errorf("Expected SlotStateConnected, got %v", slot.State())
	}

	// Activate
	slot.Activate()
	if slot.State() != SlotStateActive {
		t.Errorf("Expected SlotStateActive, got %v", slot.State())
	}

	if !slot.IsActive() {
		t.Error("Slot should be active")
	}

	// Transition to closing
	slot.SetState(SlotStateClosing)
	if slot.State() != SlotStateClosing {
		t.Errorf("Expected SlotStateClosing, got %v", slot.State())
	}
}

func TestSlotStateString(t *testing.T) {
	tests := []struct {
		state    SlotState
		expected string
	}{
		{SlotStateAccept, "accept"},
		{SlotStateConnect, "connect"},
		{SlotStateConnected, "connected"},
		{SlotStateActive, "active"},
		{SlotStateClosing, "closing"},
		{SlotState(99), "unknown"},
	}

	for _, tc := range tests {
		if tc.state.String() != tc.expected {
			t.Errorf("Expected %s for state %d, got %s", tc.expected, tc.state, tc.state.String())
		}
	}
}

func TestRecentEndpoints(t *testing.T) {
	recent := NewRecentEndpoints()

	// Insert an endpoint
	recent.Insert("192.168.1.1:51235", 1)

	// Should filter the same endpoint with same or higher hops
	if !recent.Filter("192.168.1.1:51235", 1) {
		t.Error("Should filter endpoint with same hops")
	}

	if !recent.Filter("192.168.1.1:51235", 2) {
		t.Error("Should filter endpoint with higher hops")
	}

	// Should not filter with lower hops
	if recent.Filter("192.168.1.1:51235", 0) {
		t.Error("Should not filter endpoint with lower hops")
	}

	// Should not filter unknown endpoint
	if recent.Filter("192.168.1.2:51235", 1) {
		t.Error("Should not filter unknown endpoint")
	}
}

func TestRecentEndpointsExpire(t *testing.T) {
	recent := NewRecentEndpoints()

	// Insert endpoint
	recent.Insert("192.168.1.1:51235", 1)

	// Set last seen to past (beyond TTL)
	recent.mu.Lock()
	recent.cache["192.168.1.1:51235"].LastSeen = time.Now().Add(-RecentEndpointTTL - time.Minute)
	recent.mu.Unlock()

	// Expire should remove it
	recent.Expire()

	recent.mu.RLock()
	_, exists := recent.cache["192.168.1.1:51235"]
	recent.mu.RUnlock()

	if exists {
		t.Error("Expired endpoint should be removed")
	}
}

func TestBootCache(t *testing.T) {
	// Use temp directory for test
	bc := NewBootCache("")

	// Insert endpoints
	bc.Insert("192.168.1.1", 51235)
	bc.Insert("192.168.1.2", 51235)

	endpoints := bc.GetEndpoints(10)
	if len(endpoints) != 2 {
		t.Errorf("Expected 2 endpoints, got %d", len(endpoints))
	}

	// Mark success increases valence
	bc.Insert("192.168.1.1", 51235) // Initial valence = 1
	bc.MarkSuccess("192.168.1.1")   // valence = 2

	bc.mu.RLock()
	entry := bc.cache["192.168.1.1"]
	valence := entry.Valence
	bc.mu.RUnlock()

	if valence != 3 { // 1 initial + 1 from second insert + 1 from MarkSuccess
		t.Errorf("Expected valence 3, got %d", valence)
	}

	// Mark failed decreases valence
	bc.MarkFailed("192.168.1.1")

	bc.mu.RLock()
	entry = bc.cache["192.168.1.1"]
	valence = entry.Valence
	bc.mu.RUnlock()

	if valence != 2 {
		t.Errorf("Expected valence 2 after fail, got %d", valence)
	}
}

func TestBootCacheGetEndpointsSorted(t *testing.T) {
	bc := NewBootCache("")

	// Insert with different valences
	bc.Insert("192.168.1.1", 51235)
	bc.Insert("192.168.1.2", 51235)
	bc.Insert("192.168.1.3", 51235)

	// Increase valence for peer 2
	for range 5 {
		bc.MarkSuccess("192.168.1.2")
	}

	// Increase valence for peer 3 even more
	for range 10 {
		bc.MarkSuccess("192.168.1.3")
	}

	endpoints := bc.GetEndpoints(10)

	// Should be sorted by valence descending
	if len(endpoints) < 2 {
		t.Fatal("Need at least 2 endpoints")
	}

	if endpoints[0].Address != "192.168.1.3" {
		t.Errorf("Highest valence peer should be first, got %s", endpoints[0].Address)
	}
}

// Reference: rippled src/test/peerfinder/PeerFinder_test.cpp

// TestBackoffValenceDecrease tests that repeated failures decrease valence
// Reference: rippled PeerFinder_test.cpp test_backoff1() - verifies backoff behavior
func TestBackoffValenceDecrease(t *testing.T) {
	bc := NewBootCache("")

	// Insert an endpoint
	bc.Insert("65.0.0.1", 5)

	// Initial valence should be 1
	bc.mu.RLock()
	initialValence := bc.cache["65.0.0.1"].Valence
	bc.mu.RUnlock()

	if initialValence != 1 {
		t.Errorf("Initial valence = %d, want 1", initialValence)
	}

	// Simulate repeated connection failures
	for range 10 {
		bc.MarkFailed("65.0.0.1")
	}

	bc.mu.RLock()
	finalValence := bc.cache["65.0.0.1"].Valence
	failCount := bc.cache["65.0.0.1"].FailCount
	bc.mu.RUnlock()

	// Valence should be at minimum (0)
	if finalValence != 0 {
		t.Errorf("Final valence = %d, want 0 (minimum)", finalValence)
	}

	// Fail count should reflect all failures
	if failCount != 10 {
		t.Errorf("FailCount = %d, want 10", failCount)
	}
}

// TestBackoffPeerPrioritization tests that failed peers are deprioritized
// Reference: rippled PeerFinder_test.cpp - backoff causes fewer connection attempts
func TestBackoffPeerPrioritization(t *testing.T) {
	bc := NewBootCache("")

	// Insert multiple endpoints
	bc.Insert("192.168.1.1", 51235)
	bc.Insert("192.168.1.2", 51235)
	bc.Insert("192.168.1.3", 51235)

	// Mark peer 1 as failed multiple times
	for range 5 {
		bc.MarkFailed("192.168.1.1")
	}

	// Mark peer 2 as successful
	bc.MarkSuccess("192.168.1.2")
	bc.MarkSuccess("192.168.1.2")

	endpoints := bc.GetEndpoints(10)

	// Peer 2 should be prioritized (higher valence)
	// Peer 1 should be last (lowest valence)
	if len(endpoints) < 3 {
		t.Fatal("Expected 3 endpoints")
	}

	// Find positions
	var peer1Pos, peer2Pos int = -1, -1
	for i, ep := range endpoints {
		if ep.Address == "192.168.1.1" {
			peer1Pos = i
		}
		if ep.Address == "192.168.1.2" {
			peer2Pos = i
		}
	}

	// Peer 2 (successful) should come before Peer 1 (failed)
	if peer2Pos >= peer1Pos {
		t.Errorf("Successful peer should be prioritized over failed peer. peer2Pos=%d, peer1Pos=%d", peer2Pos, peer1Pos)
	}
}

// TestBackoffRecovery tests that successful connections reset backoff state
// Reference: rippled PeerFinder_test.cpp test_backoff2() - activation resets state
func TestBackoffRecovery(t *testing.T) {
	bc := NewBootCache("")

	// Insert and fail multiple times
	bc.Insert("65.0.0.1", 5)
	for range 5 {
		bc.MarkFailed("65.0.0.1")
	}

	bc.mu.RLock()
	valenceAfterFail := bc.cache["65.0.0.1"].Valence
	failCountAfterFail := bc.cache["65.0.0.1"].FailCount
	bc.mu.RUnlock()

	// Should have minimum valence and high fail count
	if valenceAfterFail != 0 {
		t.Errorf("Valence after failures = %d, want 0", valenceAfterFail)
	}
	if failCountAfterFail != 5 {
		t.Errorf("FailCount after failures = %d, want 5", failCountAfterFail)
	}

	// Now mark as successful (simulates successful connection + activation)
	bc.MarkSuccess("65.0.0.1")

	bc.mu.RLock()
	valenceAfterSuccess := bc.cache["65.0.0.1"].Valence
	failCountAfterSuccess := bc.cache["65.0.0.1"].FailCount
	bc.mu.RUnlock()

	// Valence should increase
	if valenceAfterSuccess <= valenceAfterFail {
		t.Errorf("Valence should increase after success. before=%d, after=%d",
			valenceAfterFail, valenceAfterSuccess)
	}

	// Fail count should be reset to 0
	if failCountAfterSuccess != 0 {
		t.Errorf("FailCount should be reset to 0 after success, got %d", failCountAfterSuccess)
	}
}

// TestFixedPeerHandling tests that fixed peers are handled specially
// Reference: rippled PeerFinder_test.cpp - addFixedPeer behavior
func TestFixedPeerHandling(t *testing.T) {
	cfg := &Config{
		MaxPeers:    50,
		MaxInbound:  25,
		MaxOutbound: 25,
		FixedPeers:  []string{"65.0.0.1:5", "65.0.0.2:5"},
	}
	events := make(chan Event, 10)
	d := NewDiscovery(cfg, events)

	// Fixed peers should be tracked
	d.mu.RLock()
	fixed1 := d.fixedPeers["65.0.0.1:5"]
	fixed2 := d.fixedPeers["65.0.0.2:5"]
	nonFixed := d.fixedPeers["192.168.1.1:51235"]
	d.mu.RUnlock()

	if !fixed1 {
		t.Error("65.0.0.1:5 should be a fixed peer")
	}
	if !fixed2 {
		t.Error("65.0.0.2:5 should be a fixed peer")
	}
	if nonFixed {
		t.Error("192.168.1.1:51235 should not be a fixed peer")
	}
}

// TestSlotDuplicatePrevention tests duplicate connection prevention
// Reference: rippled PeerFinder_test.cpp test_duplicateOutIn() and test_duplicateInOut()
func TestSlotDuplicatePrevention(t *testing.T) {
	cfg := &Config{
		MaxPeers:    50,
		MaxInbound:  25,
		MaxOutbound: 25,
	}
	events := make(chan Event, 10)
	d := NewDiscovery(cfg, events)

	// Add a peer and mark connected
	peerAddr := "65.0.0.1:5"
	d.AddPeer(peerAddr, 0, 0)
	d.MarkConnected(peerAddr, PeerID(1))

	// Verify it's connected
	d.mu.RLock()
	peer := d.peers[peerAddr]
	isConnected := peer.Connected
	d.mu.RUnlock()

	if !isConnected {
		t.Error("Peer should be connected")
	}

	// SelectPeersToConnect should not include already connected peer
	candidates := d.SelectPeersToConnect(10)
	for _, addr := range candidates {
		if addr == peerAddr {
			t.Error("Already connected peer should not be in candidates")
		}
	}
}

// TestDiscoveryPruneOldPeers tests that old disconnected peers are pruned
func TestDiscoveryPruneOldPeers(t *testing.T) {
	cfg := &Config{
		MaxPeers:    50,
		MaxInbound:  25,
		MaxOutbound: 25,
	}
	events := make(chan Event, 10)
	d := NewDiscovery(cfg, events)

	// Add peers
	d.AddPeer("192.168.1.1:51235", 0, 0)
	d.AddPeer("192.168.1.2:51235", 0, 0)

	// Set one peer's LastSeen to very old
	d.mu.Lock()
	d.peers["192.168.1.1:51235"].LastSeen = time.Now().Add(-2 * time.Hour)
	d.mu.Unlock()

	// Run prune
	d.prune()

	// Old peer should be removed
	d.mu.RLock()
	_, exists1 := d.peers["192.168.1.1:51235"]
	_, exists2 := d.peers["192.168.1.2:51235"]
	d.mu.RUnlock()

	if exists1 {
		t.Error("Old disconnected peer should be pruned")
	}
	if !exists2 {
		t.Error("Recent peer should not be pruned")
	}
}

// TestBootCacheFailedLastTime tests that LastFailed time is recorded
func TestBootCacheFailedLastTime(t *testing.T) {
	bc := NewBootCache("")

	bc.Insert("192.168.1.1", 51235)

	// Initially no failure time
	bc.mu.RLock()
	initialLastFailed := bc.cache["192.168.1.1"].LastFailed
	bc.mu.RUnlock()

	if !initialLastFailed.IsZero() {
		t.Error("LastFailed should be zero initially")
	}

	// Mark as failed
	beforeFail := time.Now()
	bc.MarkFailed("192.168.1.1")
	afterFail := time.Now()

	bc.mu.RLock()
	lastFailed := bc.cache["192.168.1.1"].LastFailed
	bc.mu.RUnlock()

	if lastFailed.Before(beforeFail) || lastFailed.After(afterFail) {
		t.Errorf("LastFailed time should be between test bounds")
	}
}

// TestSimulatedBackoffBehavior simulates 10000 seconds of connection attempts
// Reference: rippled PeerFinder_test.cpp test_backoff1()
// This test verifies that with valence-based prioritization, a failing peer
// gets deprioritized over time
func TestSimulatedBackoffBehavior(t *testing.T) {
	bc := NewBootCache("")

	// Add a primary peer and some backup peers
	bc.Insert("primary.peer", 51235)
	bc.Insert("backup1.peer", 51235)
	bc.Insert("backup2.peer", 51235)

	// Boost backup peers' valence
	for range 10 {
		bc.MarkSuccess("backup1.peer")
		bc.MarkSuccess("backup2.peer")
	}

	// Simulate connection attempts over 100 iterations
	primaryAttempts := 0
	for range 100 {
		endpoints := bc.GetEndpoints(1)
		if len(endpoints) > 0 && endpoints[0].Address == "primary.peer" {
			primaryAttempts++
			bc.MarkFailed("primary.peer")
		}
	}

	// Primary peer should be deprioritized after failures
	// It shouldn't be selected many times since it keeps failing
	// while backup peers have higher valence
	t.Logf("Primary peer selected %d times out of 100 iterations", primaryAttempts)

	// After initial selections and failures, primary should rarely be selected
	// because backup peers have much higher valence
	if primaryAttempts > 20 {
		t.Errorf("Primary peer selected too many times (%d). "+
			"Expected backoff to deprioritize it", primaryAttempts)
	}
}

func TestDiscoverySyncConnectedState_DropsStalePeers(t *testing.T) {
	cfg := &Config{MaxPeers: 10, MaxInbound: 5, MaxOutbound: 5}
	events := make(chan Event, 10)
	d := NewDiscovery(cfg, events)

	d.AddPeer("10.0.0.1:51235", 0, 0)
	d.AddPeer("10.0.0.2:51235", 0, 0)
	d.AddPeer("10.0.0.3:51235", 0, 0)
	d.MarkConnected("10.0.0.1:51235", 101)
	d.MarkConnected("10.0.0.2:51235", 102)
	d.MarkConnected("10.0.0.3:51235", 103)

	// Overlay reports that only 10.0.0.2 is still connected outbound.
	live := map[string]struct{}{"10.0.0.2:51235": {}}
	d.SyncConnectedState(live)

	d.mu.RLock()
	defer d.mu.RUnlock()

	if d.peers["10.0.0.1:51235"].Connected {
		t.Error("10.0.0.1 should be flipped to Connected=false")
	}
	if d.peers["10.0.0.1:51235"].PeerID != 0 {
		t.Errorf("10.0.0.1 PeerID should be cleared, got %d", d.peers["10.0.0.1:51235"].PeerID)
	}
	if _, present := d.connected[101]; present {
		t.Error("10.0.0.1 should be removed from d.connected map")
	}
	if !d.peers["10.0.0.2:51235"].Connected {
		t.Error("10.0.0.2 must remain Connected=true")
	}
	if _, present := d.connected[102]; !present {
		t.Error("10.0.0.2 must remain in d.connected map")
	}
	if d.peers["10.0.0.3:51235"].Connected {
		t.Error("10.0.0.3 should be flipped to Connected=false")
	}
}

func TestDiscoverySyncConnectedState_NoOpForDisconnectedPeers(t *testing.T) {
	cfg := &Config{MaxPeers: 10, MaxInbound: 5, MaxOutbound: 5}
	events := make(chan Event, 10)
	d := NewDiscovery(cfg, events)

	d.AddPeer("10.0.0.4:51235", 0, 0)
	// Never MarkConnected — peer stays Connected=false.

	d.SyncConnectedState(map[string]struct{}{})

	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.peers["10.0.0.4:51235"].Connected {
		t.Error("disconnected peer should remain Connected=false")
	}
}

func TestDiscoverySyncConnectedHosts_CoversFixedInbound(t *testing.T) {
	cfg := &Config{
		MaxPeers:    10,
		MaxInbound:  5,
		MaxOutbound: 5,
		FixedPeers:  []string{"goxrpl-0:51235"},
	}
	events := make(chan Event, 10)
	d := NewDiscovery(cfg, events)

	d.AddPeer("goxrpl-0:51235", 0, 0)

	d.mu.RLock()
	if d.peers["goxrpl-0:51235"].Connected {
		d.mu.RUnlock()
		t.Fatal("precondition: fixed peer must start disconnected")
	}
	d.mu.RUnlock()

	// Inbound peer has the same host but a different ephemeral source port —
	// SyncConnectedState alone would not match it.
	hosts := map[string]struct{}{"goxrpl-0": {}}
	d.SyncConnectedHosts(hosts)

	d.mu.RLock()
	defer d.mu.RUnlock()
	if !d.peers["goxrpl-0:51235"].Connected {
		t.Error("fixed peer should be marked Connected=true based on host coverage")
	}
}

func TestDiscoverySyncConnectedHosts_EmptyHostsIsNoOp(t *testing.T) {
	cfg := &Config{MaxPeers: 10, MaxInbound: 5, MaxOutbound: 5}
	events := make(chan Event, 10)
	d := NewDiscovery(cfg, events)

	d.AddPeer("10.0.0.5:51235", 0, 0)

	d.SyncConnectedHosts(map[string]struct{}{})

	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.peers["10.0.0.5:51235"].Connected {
		t.Error("empty hosts map must not mutate Connected state")
	}
}

func TestDiscoverySyncConnectedHosts_SkipsMalformedAddress(t *testing.T) {
	cfg := &Config{MaxPeers: 10, MaxInbound: 5, MaxOutbound: 5}
	events := make(chan Event, 10)
	d := NewDiscovery(cfg, events)

	// Address missing the port — net.SplitHostPort returns an error.
	d.AddPeer("badly-formed-no-port", 0, 0)

	d.SyncConnectedHosts(map[string]struct{}{"any": {}})

	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.peers["badly-formed-no-port"].Connected {
		t.Error("malformed address must not be marked connected")
	}
}
