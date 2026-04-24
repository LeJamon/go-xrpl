package peermanagement

import (
	"testing"
	"time"

	"github.com/LeJamon/goXRPLd/internal/peermanagement/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSquelchMessageRoundTrip verifies that a TMSquelch built by our send
// path encodes to bytes that decode back to the same fields. This is a
// sanity check on the wire format we emit for mtSQUELCH.
func TestSquelchMessageRoundTrip(t *testing.T) {
	// Use a 33-byte pubkey (typical secp256k1 compressed length) to be
	// representative; the wire format treats it as opaque bytes.
	validator := make([]byte, 33)
	for i := range validator {
		validator[i] = byte(i + 1)
	}

	original := &message.Squelch{
		Squelch:         true,
		ValidatorPubKey: validator,
		SquelchDuration: 600,
	}

	encoded, err := message.Encode(original)
	require.NoError(t, err)

	decoded, err := message.Decode(message.TypeSquelch, encoded)
	require.NoError(t, err)

	got, ok := decoded.(*message.Squelch)
	require.True(t, ok, "decoded value must be *message.Squelch")
	assert.Equal(t, original.Squelch, got.Squelch)
	assert.Equal(t, original.ValidatorPubKey, got.ValidatorPubKey)
	assert.Equal(t, original.SquelchDuration, got.SquelchDuration)
}

// TestSquelchUnsquelchRoundTrip verifies the unsquelch shape: squelch=false
// and the duration field is absent (zero on the wire).
func TestSquelchUnsquelchRoundTrip(t *testing.T) {
	validator := []byte("validator-pubkey-bytes")

	original := &message.Squelch{
		Squelch:         false,
		ValidatorPubKey: validator,
		// SquelchDuration intentionally zero — rippled treats unsquelch as
		// duration-less.
	}

	encoded, err := message.Encode(original)
	require.NoError(t, err)

	decoded, err := message.Decode(message.TypeSquelch, encoded)
	require.NoError(t, err)

	got := decoded.(*message.Squelch)
	assert.False(t, got.Squelch)
	assert.Equal(t, validator, got.ValidatorPubKey)
	assert.Equal(t, uint32(0), got.SquelchDuration)
}

// TestValidatorSlot_SelectsMaxPeers_AndSquelchesRest drives a slot through
// MAX_MESSAGE_THRESHOLD with 20 peers and asserts that exactly
// MaxSelectedPeers end up in Selected and the rest each receive a squelch
// callback whose duration falls within the rippled-spec window.
func TestValidatorSlot_SelectsMaxPeers_AndSquelchesRest(t *testing.T) {
	mock := newMockSquelchCallback()
	slot := NewValidatorSlot(MaxSelectedPeers, mock.callback)

	const numPeers = 20
	validator := []byte("validator-pubkey")

	// Drive each peer past MaxMessageThreshold. Selection fires the moment
	// MaxSelectedPeers peers each cross MaxMessageThreshold+1.
	for round := 0; round <= MaxMessageThreshold+1; round++ {
		for i := 1; i <= numPeers; i++ {
			slot.Update(validator, PeerID(i))
		}
	}

	// State should now be Selected.
	slot.mu.RLock()
	state := slot.state
	slot.mu.RUnlock()
	assert.Equal(t, RelaySlotSelected, state, "slot should have transitioned to Selected")

	selected := slot.GetSelected()
	assert.Equal(t, MaxSelectedPeers, len(selected),
		"expected exactly %d selected peers, got %d", MaxSelectedPeers, len(selected))

	// Inspect the recorded squelch callbacks: there must be exactly
	// numPeers - MaxSelectedPeers of them, each with a duration in
	// [MinUnsquelchExpire, MaxUnsquelchExpirePeers].
	mock.mu.Lock()
	defer mock.mu.Unlock()

	assert.Equal(t, numPeers-MaxSelectedPeers, len(mock.squelched),
		"expected %d squelch callbacks, got %d", numPeers-MaxSelectedPeers, len(mock.squelched))

	for peerID, dur := range mock.squelched {
		assert.GreaterOrEqual(t, dur, MinUnsquelchExpire,
			"peer %d squelch duration %v below MinUnsquelchExpire", peerID, dur)
		assert.LessOrEqual(t, dur, MaxUnsquelchExpirePeers,
			"peer %d squelch duration %v above MaxUnsquelchExpirePeers", peerID, dur)
	}

	// Ensure no overlap between Selected and Squelched sets.
	selectedSet := make(map[PeerID]struct{}, len(selected))
	for _, id := range selected {
		selectedSet[id] = struct{}{}
	}
	for peerID := range mock.squelched {
		_, isSelected := selectedSet[peerID]
		assert.False(t, isSelected, "peer %d cannot be both Selected and Squelched", peerID)
	}
}

// TestPeerSquelchExpire_FiltersThenAllowsAfterExpiry exercises the
// receive-side state: after AddSquelch, ExpireSquelch must report that
// further validator messages are squelched (false). After the deadline
// passes, ExpireSquelch must return true (and clear the entry).
func TestPeerSquelchExpire_FiltersThenAllowsAfterExpiry(t *testing.T) {
	id, err := NewIdentity()
	require.NoError(t, err)

	events := make(chan Event, 1)
	endpoint := Endpoint{Host: "127.0.0.1", Port: 51235}
	peer := NewPeer(PeerID(1), endpoint, false, id, events)

	validator := []byte("V")

	// Without any squelch, sending is allowed.
	assert.True(t, peer.ExpireSquelch(validator),
		"unsquelched validator should be allowed")

	// Add a real squelch. AddSquelch enforces rippled's bounds.
	ok := peer.AddSquelch(validator, MinUnsquelchExpire)
	require.True(t, ok, "AddSquelch with MinUnsquelchExpire must succeed")

	// While squelched, ExpireSquelch returns false (drop the message).
	assert.False(t, peer.ExpireSquelch(validator),
		"squelched validator's messages must be dropped")

	// Force the deadline into the past to simulate time advancing past
	// the squelch expiry without sleeping.
	peer.squelchMu.Lock()
	peer.squelchMap[string(validator)] = time.Now().Add(-time.Second)
	peer.squelchMu.Unlock()

	// Now ExpireSquelch must return true (allow) and clear the entry.
	assert.True(t, peer.ExpireSquelch(validator),
		"expired squelch should allow the message")

	peer.squelchMu.RLock()
	_, stillThere := peer.squelchMap[string(validator)]
	peer.squelchMu.RUnlock()
	assert.False(t, stillThere, "expired entry should have been cleared")
}

// TestRelayFromValidator_SkipsSquelchedPeers verifies the relay-forward
// filter: a peer-originated validator message is delivered to all
// connected peers EXCEPT those that have squelched the originating
// validator. Mirrors rippled PeerImp.cpp:240-256.
func TestRelayFromValidator_SkipsSquelchedPeers(t *testing.T) {
	id, err := NewIdentity()
	require.NoError(t, err)

	o := &Overlay{
		peers:  make(map[PeerID]*Peer),
		events: make(chan Event, 8),
	}

	endpoint := Endpoint{Host: "127.0.0.1", Port: 51235}
	allowed := NewPeer(PeerID(1), endpoint, false, id, make(chan Event, 1))
	squelched := NewPeer(PeerID(2), endpoint, false, id, make(chan Event, 1))

	allowed.setState(PeerStateConnected)
	squelched.setState(PeerStateConnected)

	o.peers[allowed.ID()] = allowed
	o.peers[squelched.ID()] = squelched

	validator := []byte("validator-V")
	require.True(t, squelched.AddSquelch(validator, MinUnsquelchExpire))

	payload := []byte("validation-frame")
	// exceptPeer = 0 means no peer is excluded by origin — this test
	// exercises only the squelch filter, not the origin-exclusion.
	// The suppressionHash is unused by this test (reverse-index is
	// populated but never read), so any sentinel value works.
	var hash [32]byte
	require.NoError(t, o.RelayFromValidator(validator, hash, PeerID(0), payload))

	// `allowed` must have received exactly the payload.
	select {
	case got := <-allowed.send:
		assert.Equal(t, payload, got)
	default:
		t.Fatal("allowed peer did not receive the broadcast")
	}

	// `squelched` must NOT have received anything.
	select {
	case got := <-squelched.send:
		t.Fatalf("squelched peer received a frame it should have been filtered: %q", got)
	default:
	}
}

// TestPeerAddSquelch_RejectsInvalidDuration verifies that AddSquelch
// rejects out-of-range durations and clears any prior squelch (matching
// rippled Squelch::addSquelch semantics). Also verifies that each
// rejection records a bad-data event against the peer — the rejection
// is the moment we know the remote peer sent protocol-invalid data, so
// it is the natural (and only) place to attribute it.
func TestPeerAddSquelch_RejectsInvalidDuration(t *testing.T) {
	id, err := NewIdentity()
	require.NoError(t, err)

	events := make(chan Event, 1)
	endpoint := Endpoint{Host: "127.0.0.1", Port: 51235}
	peer := NewPeer(PeerID(1), endpoint, false, id, events)

	validator := []byte("V2")

	// Set a valid squelch first — no bad-data expected for the happy path.
	require.True(t, peer.AddSquelch(validator, MinUnsquelchExpire))
	require.Equal(t, uint32(0), peer.BadDataCount(),
		"valid AddSquelch must not record a bad-data event")

	// Now try a too-short duration: must return false and remove the entry.
	tooShort := MinUnsquelchExpire - time.Second
	assert.False(t, peer.AddSquelch(validator, tooShort),
		"duration below MinUnsquelchExpire must be rejected")

	assert.True(t, peer.ExpireSquelch(validator),
		"prior squelch should have been cleared by the rejected AddSquelch")

	// BadDataCount is now a weighted balance (see peer.go badDataBalance
	// + overlay.go BadDataWeight). "squelch-duration" is charged at the
	// feeInvalidData tier — one offense adds weightInvalidData (400).
	assert.Equal(t, uint32(weightInvalidData), peer.BadDataCount(),
		"rejected too-short duration must charge feeInvalidData (1 event × 400)")

	// Try a too-long duration.
	tooLong := MaxUnsquelchExpirePeers + time.Second
	assert.False(t, peer.AddSquelch(validator, tooLong),
		"duration above MaxUnsquelchExpirePeers must be rejected")

	assert.Equal(t, uint32(weightInvalidData*2), peer.BadDataCount(),
		"rejected too-long duration must charge a second feeInvalidData (2 events × 400)")
}

// TestOverlay_InboundSquelch_MalformedPubkey_Charges pins R5.8: a
// TMSquelch whose ValidatorPubKey isn't a 33-byte compressed secp256k1
// point must charge feeInvalidData (weightInvalidData = 400) against
// the sending peer. Pre-R5.8 behavior silently dropped these frames,
// letting an attacker spam bogus TMSquelches without penalty.
// Matches rippled PeerImp.cpp:2701-2712.
func TestOverlay_InboundSquelch_MalformedPubkey_Charges(t *testing.T) {
	id, err := NewIdentity()
	require.NoError(t, err)

	o := &Overlay{
		peers:  make(map[PeerID]*Peer),
		events: make(chan Event, 8),
	}

	endpoint := Endpoint{Host: "127.0.0.1", Port: 51235}
	peer := NewPeer(PeerID(77), endpoint, false, id, make(chan Event, 1))
	o.peers[peer.ID()] = peer

	// 32-byte pubkey — wrong length; rippled expects 33-byte compressed.
	badValidator := make([]byte, 32)
	for i := range badValidator {
		badValidator[i] = byte(i)
	}

	sq := &message.Squelch{
		Squelch:         true,
		ValidatorPubKey: badValidator,
		SquelchDuration: uint32(MinUnsquelchExpire / time.Second),
	}
	payload, err := message.Encode(sq)
	require.NoError(t, err)

	require.Equal(t, uint32(0), peer.BadDataCount(),
		"peer must start at zero bad-data")

	o.onMessageReceived(Event{
		PeerID:      peer.ID(),
		MessageType: uint16(message.TypeSquelch),
		Payload:     payload,
	})

	assert.Equal(t, uint32(weightInvalidData), peer.BadDataCount(),
		"malformed TMSquelch pubkey must charge feeInvalidData (400)")

	// Squelch must NOT have been applied.
	validator33 := make([]byte, 33)
	copy(validator33, badValidator)
	assert.True(t, peer.ExpireSquelch(validator33),
		"malformed-pubkey squelch must not have installed a squelch entry")
}

// TestHandleSquelchMessage_DropsSelfTargetingSquelch pins Task 4.2 (G3):
// an inbound TMSquelch whose ValidatorPubKey equals the local validator
// pubkey must be dropped WITHOUT installing a squelch — otherwise a
// hostile peer could silence our own validator's traffic on the
// RelayFromValidator path. Matches rippled PeerImp.cpp:2715-2721.
// The peer is charged "squelch-targets-self" so repeat offenders evict.
func TestHandleSquelchMessage_DropsSelfTargetingSquelch(t *testing.T) {
	id, err := NewIdentity()
	require.NoError(t, err)

	// Configure the local validator pubkey P (33-byte compressed).
	localValidator := make([]byte, 33)
	for i := range localValidator {
		localValidator[i] = byte(0xAA)
	}

	o := &Overlay{
		cfg:    Config{LocalValidatorPubKey: localValidator},
		peers:  make(map[PeerID]*Peer),
		events: make(chan Event, 8),
	}

	endpoint := Endpoint{Host: "127.0.0.1", Port: 51235}
	peer := NewPeer(PeerID(99), endpoint, false, id, make(chan Event, 1))
	o.peers[peer.ID()] = peer

	// Peer sends a TMSquelch targeting OUR validator pubkey.
	sq := &message.Squelch{
		Squelch:         true,
		ValidatorPubKey: localValidator,
		SquelchDuration: uint32(MinUnsquelchExpire / time.Second),
	}
	payload, err := message.Encode(sq)
	require.NoError(t, err)

	require.Equal(t, uint32(0), peer.BadDataCount(),
		"peer must start at zero bad-data")

	o.onMessageReceived(Event{
		PeerID:      peer.ID(),
		MessageType: uint16(message.TypeSquelch),
		Payload:     payload,
	})

	// AddSquelch must NOT have been called — ExpireSquelch returns true
	// for any validator we have not squelched.
	assert.True(t, peer.ExpireSquelch(localValidator),
		"self-targeted squelch must not have installed a squelch entry")

	// Peer is charged for attempting to silence our own validator.
	// "squelch-targets-self" falls through to weightDefaultBadData (100).
	assert.Equal(t, uint32(weightDefaultBadData), peer.BadDataCount(),
		"self-targeting TMSquelch must charge bad-data (squelch-targets-self)")
}

// TestHandleSquelchMessage_AllowsOtherValidatorSquelch is the
// companion regression guard: a TMSquelch targeting a validator pubkey
// Q ≠ P (our local validator) must still be applied normally. Ensures
// the G3 filter does not clobber the common case.
func TestHandleSquelchMessage_AllowsOtherValidatorSquelch(t *testing.T) {
	id, err := NewIdentity()
	require.NoError(t, err)

	// Local validator pubkey P.
	localValidator := make([]byte, 33)
	for i := range localValidator {
		localValidator[i] = byte(0xAA)
	}

	// Other validator pubkey Q, distinct from P.
	otherValidator := make([]byte, 33)
	for i := range otherValidator {
		otherValidator[i] = byte(0xBB)
	}

	o := &Overlay{
		cfg:    Config{LocalValidatorPubKey: localValidator},
		peers:  make(map[PeerID]*Peer),
		events: make(chan Event, 8),
	}

	endpoint := Endpoint{Host: "127.0.0.1", Port: 51235}
	peer := NewPeer(PeerID(100), endpoint, false, id, make(chan Event, 1))
	o.peers[peer.ID()] = peer

	sq := &message.Squelch{
		Squelch:         true,
		ValidatorPubKey: otherValidator,
		SquelchDuration: uint32(MinUnsquelchExpire / time.Second),
	}
	payload, err := message.Encode(sq)
	require.NoError(t, err)

	o.onMessageReceived(Event{
		PeerID:      peer.ID(),
		MessageType: uint16(message.TypeSquelch),
		Payload:     payload,
	})

	// AddSquelch must have run — ExpireSquelch returns false while the
	// squelch is active.
	assert.False(t, peer.ExpireSquelch(otherValidator),
		"other-validator squelch must have been installed")

	// No bad-data charge: applying a squelch for another validator is
	// the normal, valid case.
	assert.Equal(t, uint32(0), peer.BadDataCount(),
		"applying a squelch for a different validator must not charge bad-data")
}

// TestOverlay_InboundSquelch_FromUnnegotiatedPeer verifies that an
// inbound TMSquelch from a peer that did NOT negotiate reduce-relay is
// still applied (parity with rippled PeerImp.cpp:2691-2732). Feature
// negotiation governs what we SEND, not what we accept — rejecting
// inbound squelches would create a not-actually-rippled divergence.
// Regression guard against the historic stricter gate that charged
// feeInvalidData (400) and dropped the squelch.
func TestOverlay_InboundSquelch_FromUnnegotiatedPeer(t *testing.T) {
	id, err := NewIdentity()
	require.NoError(t, err)

	o := &Overlay{
		peers:  make(map[PeerID]*Peer),
		events: make(chan Event, 8),
	}

	endpoint := Endpoint{Host: "127.0.0.1", Port: 51235}
	peer := NewPeer(PeerID(42), endpoint, false, id, make(chan Event, 1))
	// NOTE: no capabilities set — simulates a peer that didn't
	// advertise reduce-relay in handshake. PeerSupports(..., Feature
	// ReduceRelay) will therefore return false.
	o.peers[peer.ID()] = peer

	validator := make([]byte, 33)
	for i := range validator {
		validator[i] = byte(i + 1)
	}

	sq := &message.Squelch{
		Squelch:         true,
		ValidatorPubKey: validator,
		SquelchDuration: uint32(MinUnsquelchExpire / time.Second),
	}
	payload, err := message.Encode(sq)
	require.NoError(t, err)

	o.onMessageReceived(Event{
		PeerID:      peer.ID(),
		MessageType: uint16(message.TypeSquelch),
		Payload:     payload,
	})

	// Squelch must have been applied: ExpireSquelch returns false
	// (messages from this validator are dropped).
	assert.False(t, peer.ExpireSquelch(validator),
		"inbound TMSquelch must apply even from an unnegotiated peer (rippled parity)")

	// No bad-data charge: accepting a squelch from an unnegotiated
	// peer is not a protocol violation per rippled.
	assert.Equal(t, uint32(0), peer.BadDataCount(),
		"inbound TMSquelch from unnegotiated peer must NOT charge bad-data (regression guard for historic stricter gate)")
}
