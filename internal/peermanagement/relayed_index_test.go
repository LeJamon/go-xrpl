package peermanagement

import (
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestOverlay_PeersThatHave_PopulatedByRelayForward is the core B3
// contract: when the overlay forwards a validator message to peers,
// Overlay.PeersThatHave(suppressionHash) must return the set of
// recipients so a later duplicate arrival from a DIFFERENT peer can
// feed the reduce-relay slot with every known-haver. Mirrors
// rippled's haveMessage return from overlay_.relay
// (PeerImp.cpp:3010-3017 / 3044-3054).
func TestOverlay_PeersThatHave_PopulatedByRelayForward(t *testing.T) {
	id, err := NewIdentity()
	require.NoError(t, err)

	o := &Overlay{
		peers:         make(map[PeerID]*Peer),
		events:        make(chan Event, 8),
		relayedIndex:  make(map[[32]byte]*relayedEntry),
		clockForIndex: time.Now,
	}

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

	// Relay from peer C (origin): A and B must receive the payload,
	// and the reverse index must record {A, B} against `hash`. C is
	// excluded as origin and must NOT appear in the index.
	require.NoError(t, o.RelayFromValidator(validator, hash, peerC.ID(), payload))

	got := o.PeersThatHave(hash)
	require.NotNil(t, got, "PeersThatHave must return a non-nil set after a relay-forward")
	sort.Slice(got, func(i, j int) bool { return got[i] < got[j] })
	assert.Equal(t, []PeerID{peerA.ID(), peerB.ID()}, got,
		"reverse index must contain exactly the peers we forwarded to (A, B); origin C must be excluded")

	// A second forward for the SAME hash but to a disjoint set (say
	// we learn about a new peer D later) must UNION in, not replace.
	peerD := NewPeer(PeerID(4), endpoint, false, id, make(chan Event, 1))
	peerD.setState(PeerStateConnected)
	o.peers[peerD.ID()] = peerD
	// Exclude A, B, C this time so only D is the recipient.
	require.NoError(t, o.RelayFromValidator(validator, hash, peerA.ID(), payload))

	got2 := o.PeersThatHave(hash)
	gotSet := make(map[PeerID]struct{}, len(got2))
	for _, p := range got2 {
		gotSet[p] = struct{}{}
	}
	for _, want := range []PeerID{peerB.ID(), peerC.ID(), peerD.ID()} {
		_, ok := gotSet[want]
		assert.True(t, ok, "reverse index must retain peer %d after a second relay-forward with a different exceptPeer", want)
	}
}

// TestOverlay_PeersThatHave_TTLExpiry pins the lazy TTL: once the
// bucket is older than RelayedIndexTTL, PeersThatHave returns nil and
// the bucket is dropped. This must match the consensus router's
// messageDedupTTL so the index doesn't outlive the dedup cache — a
// stale entry would feed the slot with counters for peers the rest of
// the network has already aged out.
func TestOverlay_PeersThatHave_TTLExpiry(t *testing.T) {
	var nowVal time.Time
	o := &Overlay{
		peers:        make(map[PeerID]*Peer),
		events:       make(chan Event, 8),
		relayedIndex: make(map[[32]byte]*relayedEntry),
	}
	o.clockForIndex = func() time.Time { return nowVal }

	// Seed the index directly so we don't need a real peer map.
	hash := [32]byte{0x01}
	nowVal = time.Unix(1_700_000_000, 0)
	o.recordRelayedPeers(hash, []PeerID{PeerID(7)})

	// Immediate query returns the set.
	got := o.PeersThatHave(hash)
	require.Len(t, got, 1)
	assert.Equal(t, PeerID(7), got[0])

	// Advance past TTL: query must return nil (lazy expiry).
	nowVal = nowVal.Add(RelayedIndexTTL + time.Second)
	got = o.PeersThatHave(hash)
	assert.Nil(t, got, "bucket older than RelayedIndexTTL must be reaped on query")

	// Bucket must also be physically removed from the map so the
	// next query path is O(1) on the cold-miss.
	o.relayedIndexMu.Lock()
	_, present := o.relayedIndex[hash]
	o.relayedIndexMu.Unlock()
	assert.False(t, present, "expired bucket must be deleted from the index, not just hidden")
}
