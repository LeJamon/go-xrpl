package peermanagement

import (
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOverlay_RecordMessageSource_AccumulatesInboundOnly(t *testing.T) {
	id, err := NewIdentity()
	require.NoError(t, err)

	o := &Overlay{
		peers:         make(map[PeerID]*Peer),
		events:        make(chan Event, 8),
		relayedIndex:  make(map[[32]byte]*relayedEntry),
		clockForIndex: time.Now,
	}
	cfg := DefaultConfig()
	cfg.EnableVPReduceRelay = true
	o.relay = NewRelay(&cfg, nil, nil)
	o.relay.startTime = time.Now().Add(-WaitOnBootup - time.Minute)

	endpoint := Endpoint{Host: "127.0.0.1", Port: 51235}
	peerA := NewPeer(PeerID(1), endpoint, false, id, make(chan Event, 1))
	peerB := NewPeer(PeerID(2), endpoint, false, id, make(chan Event, 1))
	peerC := NewPeer(PeerID(3), endpoint, false, id, make(chan Event, 1))
	peerA.setState(PeerStateConnected)
	peerB.setState(PeerStateConnected)
	peerC.setState(PeerStateConnected)
	o.peers[peerA.ID()] = peerA
	o.peers[peerB.ID()] = peerB
	o.peers[peerC.ID()] = peerC

	validator := []byte("validator-B3")
	hash := [32]byte{0xAB, 0xCD}
	payload := []byte("signed-proposal-bytes")

	o.RecordMessageSource(hash, peerA.ID())
	o.RecordMessageSource(hash, peerC.ID())

	got := o.PeersThatHave(hash)
	slices.Sort(got)
	assert.Equal(t, []PeerID{peerA.ID(), peerC.ID()}, got)

	require.NoError(t, o.RelayFromValidator(validator, hash, 0, payload))
	assert.True(t, o.MessageRelayedRecently(hash))

	for _, source := range []*Peer{peerA, peerC} {
		if frame, ok := takeOutboundFrame(source); ok {
			t.Fatalf("inbound source %d received relayed frame %q", source.ID(), frame)
		}
	}
	assert.Equal(t, payload, requireOutboundFrame(t, peerB))

	assert.Empty(t, o.PeersThatHave(hash), "relay must release the accumulated source set")
	o.relay.mu.RLock()
	slot := o.relay.slots[string(validator)]
	o.relay.mu.RUnlock()
	require.NotNil(t, slot)
	slot.mu.RLock()
	_, countedA := slot.peers[peerA.ID()]
	_, countedC := slot.peers[peerC.ID()]
	slot.mu.RUnlock()
	assert.True(t, countedA)
	assert.True(t, countedC)

	// Outbound delivery is not evidence that a peer supplied the message.
	// A later arrival begins a fresh source set containing only its sender.
	o.RecordMessageSource(hash, peerC.ID())
	assert.Equal(t, []PeerID{peerC.ID()}, o.PeersThatHave(hash))
}

func TestOverlay_PeersThatHave_TTLExpiry(t *testing.T) {
	var nowVal time.Time
	o := &Overlay{
		peers:        make(map[PeerID]*Peer),
		events:       make(chan Event, 8),
		relayedIndex: make(map[[32]byte]*relayedEntry),
	}
	o.clockForIndex = func() time.Time { return nowVal }

	hash := [32]byte{0x01}
	nowVal = time.Unix(1_700_000_000, 0)
	o.RecordMessageSource(hash, PeerID(7))

	got := o.PeersThatHave(hash)
	require.Len(t, got, 1)
	assert.Equal(t, PeerID(7), got[0])

	// Every arrival refreshes the hold and accumulates its source.
	nowVal = nowVal.Add(RelayedIndexTTL - time.Second)
	o.RecordMessageSource(hash, PeerID(8))
	got = o.PeersThatHave(hash)
	slices.Sort(got)
	assert.Equal(t, []PeerID{7, 8}, got)

	nowVal = nowVal.Add(RelayedIndexTTL + time.Second)
	got = o.PeersThatHave(hash)
	assert.Nil(t, got, "bucket older than RelayedIndexTTL must be reaped on query")

	o.relayedIndexMu.Lock()
	_, present := o.relayedIndex[hash]
	o.relayedIndexMu.Unlock()
	assert.False(t, present, "expired bucket must be deleted from the index, not just hidden")
}

func TestOverlay_MessageRelayedRecentlyWindow(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	o := &Overlay{
		relayedIndex:  make(map[[32]byte]*relayedEntry),
		clockForIndex: func() time.Time { return now },
	}
	hash := [32]byte{0x02}
	o.RecordMessageSource(hash, PeerID(7))
	assert.False(t, o.MessageRelayedRecently(hash))

	assert.Equal(t, []PeerID{7}, o.releaseMessageSources(hash))
	assert.True(t, o.MessageRelayedRecently(hash))

	now = now.Add(Idled)
	assert.False(t, o.MessageRelayedRecently(hash))
}

func TestOverlay_RelayFromValidatorMarksOnlySuccessfulEnqueue(t *testing.T) {
	id, err := NewIdentity()
	require.NoError(t, err)
	o := &Overlay{
		cfg:          Config{},
		peers:        make(map[PeerID]*Peer),
		relayedIndex: make(map[[32]byte]*relayedEntry),
	}
	source := NewPeer(PeerID(1), Endpoint{Host: "127.0.0.1", Port: 51235}, false, id, make(chan Event, 1))
	destination := NewPeer(PeerID(2), Endpoint{Host: "127.0.0.1", Port: 51235}, false, id, make(chan Event, 1))
	source.setState(PeerStateConnected)
	destination.setState(PeerStateConnected)
	o.peers[source.ID()] = source
	o.peers[destination.ID()] = destination

	// Saturate the destination's ordinary lane so RelayFromValidator has no
	// successful enqueue to account for.
	for range ordinarySendMaximum {
		require.NoError(t, destination.Send([]byte{0xAA}))
	}
	hash := [32]byte{0xD1}
	o.RecordMessageSource(hash, source.ID())
	require.ErrorIs(t, o.RelayFromValidator([]byte("validator"), hash, 0, []byte{0xBB}), ErrSendBufferFull)
	assert.False(t, o.MessageRelayedRecently(hash),
		"a failed relay must not enter the duplicate-counting window")
	assert.Equal(t, []PeerID{source.ID()}, o.PeersThatHave(hash),
		"a failed relay must restore the source snapshot")

	// A later relay that is accepted by a peer does enter the window.
	destination = NewPeer(PeerID(3), Endpoint{Host: "127.0.0.1", Port: 51235}, false, id, make(chan Event, 1))
	destination.setState(PeerStateConnected)
	o.peers[destination.ID()] = destination
	hash = [32]byte{0xD2}
	o.RecordMessageSource(hash, source.ID())
	require.ErrorIs(t, o.RelayFromValidator([]byte("validator"), hash, 0, []byte{0xBC}), ErrSendBufferFull)
	assert.True(t, o.MessageRelayedRecently(hash))
	assert.Empty(t, o.PeersThatHave(hash),
		"a partially successful relay consumes the source snapshot")
}
