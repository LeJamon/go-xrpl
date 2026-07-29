package peermanagement

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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

	o := &Overlay{
		cfg:    cfg,
		peers:  make(map[PeerID]*Peer),
		events: make(chan Event, 256),
	}
	o.relay = NewRelay(&cfg, nil, nil)
	o.relay.onSquelch = o.handleSquelch

	clockNS.Store(startAt.Add(WaitOnBootup + time.Minute).UnixNano())

	const numPeers = 10
	identity, err := NewIdentity()
	require.NoError(t, err)
	endpoint := Endpoint{Host: "127.0.0.1", Port: 51235}
	peers := make(map[PeerID]*Peer, numPeers)
	for i := 1; i <= numPeers; i++ {
		p := NewPeer(PeerID(i), endpoint, false, identity, make(chan Event, 1))
		p.setState(PeerStateConnected)
		peers[PeerID(i)] = p
		o.peers[PeerID(i)] = p
	}

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

	selected := slot.Selected()
	require.Equal(t, MaxSelectedPeers, len(selected),
		"exactly MaxSelectedPeers must be picked as the source set")

	selectedSet := make(map[PeerID]struct{}, len(selected))
	for _, id := range selected {
		selectedSet[id] = struct{}{}
	}

	squelchesSeen := 0
	for id, p := range peers {
		if _, isSelected := selectedSet[id]; isSelected {
			if frame, ok := takeOutboundFrame(p); ok {
				t.Errorf("selected peer %d unexpectedly received a frame (%d bytes)", id, len(frame))
			}
			continue
		}
		if frame, ok := takeOutboundFrame(p); ok {
			require.GreaterOrEqual(t, len(frame), message.HeaderSizeUncompressed,
				"squelched peer %d received an undersized frame", id)
			// The 4th and 5th bytes (big-endian) carry the type.
			msgType := (uint16(frame[4]) << 8) | uint16(frame[5])
			assert.Equal(t, uint16(message.TypeSquelch), msgType,
				"non-selected peer %d should receive a TMSquelch frame, got type %d", id, msgType)
			squelchesSeen++
		} else {
			t.Errorf("non-selected peer %d never received a TMSquelch frame", id)
		}
	}

	assert.Equal(t, numPeers-MaxSelectedPeers, squelchesSeen,
		"exactly %d non-selected peers must have received a TMSquelch",
		numPeers-MaxSelectedPeers)
}
