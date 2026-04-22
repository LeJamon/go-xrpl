package peermanagement

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/LeJamon/goXRPLd/internal/peermanagement/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestOverlay_ReduceRelay_NaturalSelection_EndToEnd pins the R4.2
// integration path: the full Relay selection → Overlay.handleSquelch
// → Peer.Send wire chain fires when natural MaxMessageThreshold +
// MaxSelectedPeers traffic arrives through Overlay.OnValidatorMessage
// (the production entry point called by adaptor.UpdateRelaySlot).
//
// Without this test the only end-to-end coverage is
// TestTwoOverlay_Squelch_RoundTrip, which short-circuits through
// IssueSquelch directly and skips the selection state machine and the
// onSquelch→handleSquelch callback wiring. A regression that broke
// any of those hops (selection not firing, callback not wired,
// handleSquelch not finding the peer, wire encode failure, send
// queue overflow) would pass existing tests but silently disable
// reduce-relay for the whole fleet.
//
// Approach: build a bare Overlay with a fake clock past WaitOnBootup,
// register several mock peers with real Peer.send channels, feed the
// Relay enough activity to cross selection threshold via the
// production OnValidatorMessage entry point, and assert that at least
// one TMSquelch wire frame materialized in a non-selected peer's
// send queue.
func TestOverlay_ReduceRelay_NaturalSelection_EndToEnd(t *testing.T) {
	// Fake clock: Relay captures startTime at construction, so we
	// initialize the clock at t=0 and advance it past WaitOnBootup
	// AFTER the Relay exists. This mimics production (startTime is
	// set when the overlay boots; reduce-relay activates after the
	// grace period).
	var clockNS atomic.Int64
	startAt := time.Now()
	clockNS.Store(startAt.UnixNano())
	fakeClock := func() time.Time {
		return time.Unix(0, clockNS.Load())
	}

	cfg := DefaultConfig()
	cfg.EnableReduceRelay = true
	cfg.Clock = fakeClock

	// Build Overlay manually — NewFromConfig requires a real data dir
	// and listener. All we need is the peer map + relay + events
	// plumbing.
	o := &Overlay{
		cfg:    cfg,
		peers:  make(map[PeerID]*Peer),
		events: make(chan Event, 256),
	}
	o.relay = NewRelay(&cfg, nil)
	o.relay.onSquelch = o.handleSquelch

	// Advance the clock past WaitOnBootup so Relay.OnMessage starts
	// accepting traffic.
	clockNS.Store(startAt.Add(WaitOnBootup + time.Minute).UnixNano())

	// Build 10 mock peers. MaxSelectedPeers=5 of them will be chosen
	// and the other 5 will each receive a TMSquelch wire frame on
	// their Peer.send channel.
	const numPeers = 10
	identity, err := NewIdentity()
	require.NoError(t, err)
	endpoint := Endpoint{Host: "127.0.0.1", Port: 51235}
	peers := make(map[PeerID]*Peer, numPeers)
	for i := 1; i <= numPeers; i++ {
		p := NewPeer(PeerID(i), endpoint, false, identity, make(chan Event, 1))
		// Mark connected so o.handleSquelch can find + Send() without
		// failing on closed state.
		p.setState(PeerStateConnected)
		peers[PeerID(i)] = p
		o.peers[PeerID(i)] = p
	}

	// 33-byte validator pub-key (compressed secp256k1 shape).
	validator := make([]byte, 33)
	for i := range validator {
		validator[i] = byte(0x10 | i)
	}

	// Drive Relay.OnMessage past selection threshold via the
	// production entry point (OnValidatorMessage — the same method
	// the adaptor's sender shim calls on every duplicate proposal /
	// validation). Selection fires once MaxSelectedPeers peers each
	// cross MaxMessageThreshold duplicates; push a comfortable margin
	// above that so the test isn't racy on any future tuning change.
	for round := 0; round <= MaxMessageThreshold+2; round++ {
		for i := 1; i <= numPeers; i++ {
			o.OnValidatorMessage(validator, PeerID(i))
		}
	}

	// The selection state machine must have transitioned to Selected
	// and chosen MaxSelectedPeers peers.
	keyHex := string(validator)
	o.relay.mu.RLock()
	slot, ok := o.relay.slots[keyHex]
	o.relay.mu.RUnlock()
	require.True(t, ok, "relay must have created a ValidatorSlot for this validator")

	slot.mu.RLock()
	state := slot.state
	slot.mu.RUnlock()
	require.Equal(t, RelaySlotSelected, state,
		"slot must have transitioned to Selected after numPeers × MaxMessageThreshold messages")

	selected := slot.GetSelected()
	require.Equal(t, MaxSelectedPeers, len(selected),
		"exactly MaxSelectedPeers must be picked as the source set")

	// For each non-selected peer, its Peer.send channel must now
	// contain a TMSquelch wire frame. This is the integration gate:
	// selection fired → onSquelch callback invoked → handleSquelch
	// built and sent the wire frame. Any regression in that chain
	// leaves the channel empty.
	selectedSet := make(map[PeerID]struct{}, len(selected))
	for _, id := range selected {
		selectedSet[id] = struct{}{}
	}

	squelchesSeen := 0
	for id, p := range peers {
		if _, isSelected := selectedSet[id]; isSelected {
			// Selected peers MUST NOT have received a squelch.
			select {
			case frame := <-p.send:
				// Sanity: it's possible the overlay sent something
				// else (unlikely in this barebones test, but guard).
				t.Errorf("selected peer %d unexpectedly received a frame (%d bytes)", id, len(frame))
			default:
			}
			continue
		}
		// Non-selected peer — expect exactly one TMSquelch frame in
		// the send queue.
		select {
		case frame := <-p.send:
			// Decode the wire frame header to confirm it's TMSquelch.
			require.GreaterOrEqual(t, len(frame), message.HeaderSizeUncompressed,
				"squelched peer %d received an undersized frame", id)
			// The 4th and 5th bytes (big-endian) carry the type.
			msgType := (uint16(frame[4]) << 8) | uint16(frame[5])
			assert.Equal(t, uint16(message.TypeSquelch), msgType,
				"non-selected peer %d should receive a TMSquelch frame, got type %d", id, msgType)
			squelchesSeen++
		default:
			t.Errorf("non-selected peer %d never received a TMSquelch frame", id)
		}
	}

	assert.Equal(t, numPeers-MaxSelectedPeers, squelchesSeen,
		"exactly %d non-selected peers must have received a TMSquelch",
		numPeers-MaxSelectedPeers)
}
