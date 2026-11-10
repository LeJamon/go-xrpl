package peermanagement

import (
	"testing"
	"time"
)

// newSelectedRelaySlot drives a fresh Relay through one selection round for
// validator with peers 1..n (n > MaxSelectedPeers) and returns the relay, its
// slot and the IDs left Squelched by the round.
func newSelectedRelaySlot(t *testing.T, validator []byte, n int, mock *mockSquelchCallback) (*Relay, *ValidatorSlot, []PeerID) {
	t.Helper()
	relay := NewRelay(&Config{EnableVPReduceRelay: true, Clock: time.Now}, mock.callback, nil)
	relay.startTime = time.Now().Add(-WaitOnBootup - time.Minute)

	// The first message of each peer resets every count (initCounting), so
	// register all peers before counting.
	for id := 1; id <= n; id++ {
		relay.OnMessage(validator, PeerID(id))
	}
	for range MaxMessageThreshold + 1 {
		for id := 1; id <= n; id++ {
			relay.OnMessage(validator, PeerID(id))
		}
	}

	relay.mu.RLock()
	slot := relay.slots[string(validator)]
	relay.mu.RUnlock()
	if slot == nil {
		t.Fatal("no slot created")
	}

	slot.mu.RLock()
	defer slot.mu.RUnlock()
	if slot.state != RelaySlotSelected {
		t.Fatalf("slot state = %v, want selected", slot.state)
	}
	var squelched []PeerID
	for id, p := range slot.peers {
		if p.State == RelayPeerSquelched {
			squelched = append(squelched, id)
		}
	}
	if len(squelched) != n-MaxSelectedPeers {
		t.Fatalf("squelched %d peers, want %d", len(squelched), n-MaxSelectedPeers)
	}
	return relay, slot, squelched
}

// rippled 3.4.1 Slots::deleteIdlePeers (Slot.h:842-860) erases a slot once
// its last selection is older than kMaxUnsquelchExpireDefault, even while its
// peers keep relaying. The next message then starts a new slot whose
// lastSelected_ is fresh. Without that, a slot that fell back to Counting
// more than 2*kMaxUnsquelchExpireDefault after its last selection resets its
// counts on every message (Slot.h:349-355) and never selects again.
func TestRelay_DeleteIdlePeers_ErasesSlotAfterLastSelectionAges(t *testing.T) {
	validator := []byte("validator-slot-aging")
	mock := newMockSquelchCallback()
	relay, slot, _ := newSelectedRelaySlot(t, validator, MaxSelectedPeers+1, mock)

	// Every peer is still active; only the last selection is old.
	slot.mu.Lock()
	slot.lastSelected = time.Now().Add(-2*MaxUnsquelchExpireDefault - time.Second)
	var selected PeerID
	for id, p := range slot.peers {
		if p.State == RelayPeerSelected {
			selected = id
			break
		}
	}
	slot.mu.Unlock()

	// A selected peer leaves: the slot falls back to Counting.
	relay.RemovePeer(selected)
	relay.deleteIdlePeers(time.Now())

	relay.mu.RLock()
	_, kept := relay.slots[string(validator)]
	relay.mu.RUnlock()
	if kept {
		t.Fatal("slot whose last selection is older than MaxUnsquelchExpireDefault was kept")
	}

	// The remaining peers and a newcomer keep relaying: a new selection
	// round must happen and squelch the peer that is not selected.
	mock.mu.Lock()
	mock.squelched = make(map[PeerID]time.Duration)
	mock.mu.Unlock()
	peers := []PeerID{}
	for id := 1; id <= MaxSelectedPeers+2; id++ {
		if PeerID(id) != selected {
			peers = append(peers, PeerID(id))
		}
	}
	for _, id := range peers {
		relay.OnMessage(validator, id)
	}
	for range MaxMessageThreshold + 1 {
		for _, id := range peers {
			relay.OnMessage(validator, id)
		}
	}
	mock.mu.Lock()
	got := len(mock.squelched)
	mock.mu.Unlock()
	if got != len(peers)-MaxSelectedPeers {
		t.Fatalf("new selection squelched %d peers, want %d", got, len(peers)-MaxSelectedPeers)
	}
}

// rippled 3.4.1 Slot::deleteIdlePeer (Slot.h:276-295) calls
// deletePeer(validator, id, false): an idle peer is reset but kept. A peer
// that honours our squelch goes quiet, so it stays in the slot as Squelched,
// and when a selected peer later disconnects, Slot::deletePeer
// (Slot.h:446-490) unsquelches it.
func TestRelay_DeleteIdlePeers_KeepsIdleSquelchedPeerForUnsquelch(t *testing.T) {
	validator := []byte("validator-idle-squelched")
	mock := newMockSquelchCallback()
	relay, slot, squelched := newSelectedRelaySlot(t, validator, MaxSelectedPeers+1, mock)
	quiet := squelched[0]

	// The squelched peer honours the squelch and stops relaying; the
	// selected peers keep relaying.
	sweep := time.Now().Add(Idled + time.Second)
	slot.mu.Lock()
	var selected PeerID
	for id, p := range slot.peers {
		if p.State == RelayPeerSelected {
			p.LastMessage = sweep
			selected = id
		}
	}
	slot.mu.Unlock()

	relay.deleteIdlePeers(sweep)

	slot.mu.RLock()
	p, ok := slot.peers[quiet]
	slot.mu.RUnlock()
	if !ok {
		t.Fatal("idle squelched peer was erased from the slot")
	}
	if p.State != RelayPeerSquelched {
		t.Fatalf("idle squelched peer state = %v, want squelched", p.State)
	}

	relay.RemovePeer(selected)

	mock.mu.Lock()
	defer mock.mu.Unlock()
	if len(mock.unsquelched) != 1 || mock.unsquelched[0] != quiet {
		t.Fatalf("unsquelched %v, want [%v]", mock.unsquelched, quiet)
	}
}
