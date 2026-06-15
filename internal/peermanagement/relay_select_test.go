package peermanagement

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newRelayTestPeer(id PeerID) *Peer {
	return &Peer{id: id, state: PeerStateConnected}
}

func hash32(b byte) [32]byte {
	var h [32]byte
	for i := range h {
		h[i] = b
	}
	return h
}

// TestPeer_TxSetRing pins the per-peer tx-set advertisement ring: HasTxSet
// reflects AddTxSet, duplicates are ignored, and the ring evicts the oldest
// entry once it exceeds maxRecentTxSets (mirrors rippled's
// circular_buffer<uint256>{128}).
func TestPeer_TxSetRing(t *testing.T) {
	p := newRelayTestPeer(1)

	a, b := hash32(0x01), hash32(0x02)
	assert.False(t, p.HasTxSet(a))
	p.AddTxSet(a)
	p.AddTxSet(a) // duplicate ignored
	p.AddTxSet(b)
	assert.True(t, p.HasTxSet(a))
	assert.True(t, p.HasTxSet(b))

	p.mu.RLock()
	require.Len(t, p.recentTxSets, 2, "duplicate must not grow the ring")
	p.mu.RUnlock()

	// Overflow the ring: the first-added entry (a) is evicted.
	for i := range maxRecentTxSets {
		p.AddTxSet(hash32(byte(0x80 + i)))
	}
	assert.False(t, p.HasTxSet(a), "oldest entry evicted past the cap")
	p.mu.RLock()
	require.Len(t, p.recentTxSets, maxRecentTxSets)
	p.mu.RUnlock()
}

// TestOverlay_PeerWithLedger pins getPeerWithLedger selection: a connected
// peer advertising the ledger as its closed OR previous hash is chosen, the
// excluded peer is skipped, and ties resolve to the lowest peer id.
func TestOverlay_PeerWithLedger(t *testing.T) {
	target := hash32(0xAA)
	o := &Overlay{peers: make(map[PeerID]*Peer)}

	// p3 and p5 both advertise target as their closed ledger; p4 advertises
	// it as its previous ledger; p9 advertises something else.
	p3 := newRelayTestPeer(3)
	p3.closedLedger, p3.hasClosedLedger = target, true
	p5 := newRelayTestPeer(5)
	p5.closedLedger, p5.hasClosedLedger = target, true
	p4 := newRelayTestPeer(4)
	p4.previousLedger, p4.hasPreviousLedger = target, true
	p9 := newRelayTestPeer(9)
	p9.closedLedger, p9.hasClosedLedger = hash32(0xBB), true
	for _, p := range []*Peer{p3, p4, p5, p9} {
		o.peers[p.id] = p
	}

	got, ok := o.PeerWithLedger(target, 0)
	require.True(t, ok)
	assert.Equal(t, PeerID(3), got, "lowest-id candidate selected")

	got, ok = o.PeerWithLedger(target, 3)
	require.True(t, ok)
	assert.Equal(t, PeerID(4), got, "excluded peer skipped; previous-ledger match counts")

	_, ok = o.PeerWithLedger(hash32(0xCC), 0)
	assert.False(t, ok, "no peer advertises this ledger")
}

// TestOverlay_PeerWithLedger_SkipsDisconnected pins that a peer holding the
// ledger but not in PeerStateConnected is not a relay target.
func TestOverlay_PeerWithLedger_SkipsDisconnected(t *testing.T) {
	target := hash32(0xAA)
	o := &Overlay{peers: make(map[PeerID]*Peer)}

	p := newRelayTestPeer(2)
	p.state = PeerStateConnecting
	p.closedLedger, p.hasClosedLedger = target, true
	o.peers[p.id] = p

	_, ok := o.PeerWithLedger(target, 0)
	assert.False(t, ok, "a non-connected peer must not be selected")
}

// TestOverlay_PeerWithTxSet pins getPeerWithTree selection over the per-peer
// tx-set advertisement ring, honouring the exclude argument.
func TestOverlay_PeerWithTxSet(t *testing.T) {
	root := hash32(0x77)
	o := &Overlay{peers: make(map[PeerID]*Peer)}

	p2 := newRelayTestPeer(2)
	p2.AddTxSet(root)
	p6 := newRelayTestPeer(6)
	p6.AddTxSet(root)
	p8 := newRelayTestPeer(8)
	p8.AddTxSet(hash32(0x11))
	for _, p := range []*Peer{p2, p6, p8} {
		o.peers[p.id] = p
	}

	got, ok := o.PeerWithTxSet(root, 0)
	require.True(t, ok)
	assert.Equal(t, PeerID(2), got)

	got, ok = o.PeerWithTxSet(root, 2)
	require.True(t, ok)
	assert.Equal(t, PeerID(6), got)

	_, ok = o.PeerWithTxSet(hash32(0x22), 0)
	assert.False(t, ok)
}

// TestOverlay_NotePeerHasTxSet pins that recording an advertisement reaches
// the peer's ring and that an unknown peer id is a safe no-op.
func TestOverlay_NotePeerHasTxSet(t *testing.T) {
	root := hash32(0x77)
	o := &Overlay{peers: make(map[PeerID]*Peer)}
	p := newRelayTestPeer(1)
	o.peers[p.id] = p

	o.NotePeerHasTxSet(1, root)
	assert.True(t, p.HasTxSet(root))

	// Unknown peer: must not panic and must record nothing.
	o.NotePeerHasTxSet(999, root)

	got, ok := o.PeerWithTxSet(root, 0)
	require.True(t, ok)
	assert.Equal(t, PeerID(1), got)
}
