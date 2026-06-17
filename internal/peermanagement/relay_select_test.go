package peermanagement

import (
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
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

// TestPeer_HasLedger pins rippled PeerImp::hasLedger: the hash arm matches a
// ledger the peer advertised (recentLedgers ring), and the seq-range arm
// qualifies only a converged peer whose advertised [first,last] covers seq.
func TestPeer_HasLedger(t *testing.T) {
	p := newRelayTestPeer(1)
	h := hash32(0x42)

	assert.False(t, p.HasLedger(h, 0), "nothing advertised yet")

	// Hash arm via the ring.
	p.AddLedger(h)
	p.AddLedger(h) // duplicate ignored
	assert.True(t, p.HasLedger(h, 0))
	assert.False(t, p.HasLedger(hash32(0x43), 0), "different hash not matched")
	p.mu.RLock()
	require.Len(t, p.recentLedgers, 1, "duplicate must not grow the ring")
	p.mu.RUnlock()

	// Seq-range arm: only when converged and in range.
	p.firstLedgerSeq, p.lastLedgerSeq = 100, 200
	p.setTracking(PeerTrackingDiverged)
	assert.False(t, p.HasLedger([32]byte{}, 150), "diverged peer fails the range arm")
	p.setTracking(PeerTrackingConverged)
	assert.True(t, p.HasLedger([32]byte{}, 150))
	assert.False(t, p.HasLedger([32]byte{}, 250), "seq out of range")
	assert.False(t, p.HasLedger([32]byte{}, 0), "seq 0 disables the range arm")
}

// TestPeer_StatusChangeFeedsLedgerRing pins that a status change advertising a
// closed (and previous) ledger feeds the recentLedgers ring, and that a later
// LostSync clears the single hints but never the ring — mirroring rippled
// addLedger (PeerImp.cpp:1846/1857), which is never cleared.
func TestPeer_StatusChangeFeedsLedgerRing(t *testing.T) {
	p := newRelayTestPeer(1)
	closed := hash32(0xC1)
	prev := hash32(0xB1)

	p.applyStatusChange(&message.StatusChange{
		NewEvent:           message.NodeEventAcceptedLedger,
		LedgerHash:         closed[:],
		LedgerHashPrevious: prev[:],
	})
	assert.True(t, p.HasLedger(closed, 0), "closed ledger recorded in the ring")
	assert.True(t, p.HasLedger(prev, 0), "previous ledger recorded in the ring")

	// LostSync clears the closed/previous hints but must not clear the ring.
	p.applyStatusChange(&message.StatusChange{NewEvent: message.NodeEventLostSync})
	if _, ok := p.ClosedLedger(); ok {
		t.Error("LostSync must clear the closed-ledger hint")
	}
	assert.True(t, p.HasLedger(closed, 0), "ring survives LostSync")
}

// TestOverlay_PeerWithLedger pins getPeerWithLedger selection: a connected
// peer that advertised the ledger (via its recentLedgers ring) is chosen, a
// peer advertising a different ledger is never chosen, and the excluded peer
// is skipped.
func TestOverlay_PeerWithLedger(t *testing.T) {
	target := hash32(0xAA)
	o := &Overlay{peers: make(map[PeerID]*Peer)}

	// p3, p4, p5 advertised target among their recent ledgers; p9 advertised
	// a different ledger.
	p3 := newRelayTestPeer(3)
	p3.AddLedger(target)
	p4 := newRelayTestPeer(4)
	p4.AddLedger(target)
	p5 := newRelayTestPeer(5)
	p5.AddLedger(target)
	p9 := newRelayTestPeer(9)
	p9.AddLedger(hash32(0xBB))
	for _, p := range []*Peer{p3, p4, p5, p9} {
		o.peers[p.id] = p
	}

	got, ok := o.PeerWithLedger(target, 0, 0)
	require.True(t, ok)
	assert.Contains(t, []PeerID{3, 4, 5}, got, "an advertiser is selected, never the non-advertiser p9")

	got, ok = o.PeerWithLedger(target, 0, 3)
	require.True(t, ok)
	assert.Contains(t, []PeerID{4, 5}, got, "excluded peer skipped")

	_, ok = o.PeerWithLedger(hash32(0xCC), 0, 0)
	assert.False(t, ok, "no peer advertises this ledger")
}

// TestOverlay_PeersWithLedger pins the multi-peer broaden selector
// (InboundLedger::addPeers): up to max advertisers are returned, the excluded
// set is skipped, non-advertisers are never chosen, and max<=0 / no-advertiser
// both yield nil.
func TestOverlay_PeersWithLedger(t *testing.T) {
	target := hash32(0xAA)
	o := &Overlay{peers: make(map[PeerID]*Peer)}

	p3 := newRelayTestPeer(3)
	p3.AddLedger(target)
	p4 := newRelayTestPeer(4)
	p4.AddLedger(target)
	p5 := newRelayTestPeer(5)
	p5.AddLedger(target)
	p9 := newRelayTestPeer(9)
	p9.AddLedger(hash32(0xBB))
	for _, p := range []*Peer{p3, p4, p5, p9} {
		o.peers[p.id] = p
	}

	got := o.PeersWithLedger(target, 0, nil, 2)
	require.Len(t, got, 2, "capped at max")
	for _, id := range got {
		assert.Contains(t, []PeerID{3, 4, 5}, id, "only advertisers, never p9")
	}

	got = o.PeersWithLedger(target, 0, nil, 10)
	assert.ElementsMatch(t, []PeerID{3, 4, 5}, got, "all advertisers when max exceeds the pool")

	got = o.PeersWithLedger(target, 0, []PeerID{3, 4}, 10)
	assert.Equal(t, []PeerID{5}, got, "excluded set skipped")

	assert.Nil(t, o.PeersWithLedger(target, 0, nil, 0), "max<=0 yields nil")
	assert.Nil(t, o.PeersWithLedger(hash32(0xCC), 0, nil, 5), "no advertiser yields nil")
}

// TestOverlay_PeerWithLedger_PrefersLowerLatency pins the rippled getScore
// weighting: among advertisers, the low-latency peer wins every draw despite
// its higher id, because the latency gap dominates the random jitter.
func TestOverlay_PeerWithLedger_PrefersLowerLatency(t *testing.T) {
	target := hash32(0xAA)
	o := &Overlay{peers: make(map[PeerID]*Peer)}

	// fast has the higher id but a far lower latency; slow is the opposite.
	fast := newRelayTestPeer(7)
	fast.AddLedger(target)
	fast.latency, fast.hasLatency = 1*time.Millisecond, true
	slow := newRelayTestPeer(2)
	slow.AddLedger(target)
	slow.latency, slow.hasLatency = 600*time.Millisecond, true
	o.peers[fast.id], o.peers[slow.id] = fast, slow

	// The >333ms latency gap dominates the [0,9999] random jitter, so the
	// low-latency peer wins every draw despite its higher id.
	for range 50 {
		got, ok := o.PeerWithLedger(target, 0, 0)
		require.True(t, ok)
		assert.Equal(t, PeerID(7), got)
	}
}

// TestOverlay_PeerWithLedger_SeqRange pins the rippled hasLedger(hash, seq)
// range arm: a converged peer whose advertised [first,last] range covers seq
// qualifies even without a hash match; a diverged peer or out-of-range seq
// does not.
func TestOverlay_PeerWithLedger_SeqRange(t *testing.T) {
	o := &Overlay{peers: make(map[PeerID]*Peer)}

	// p2 converged, range [100,200] — covers seq 150.
	p2 := newRelayTestPeer(2)
	p2.firstLedgerSeq, p2.lastLedgerSeq = 100, 200
	p2.setTracking(PeerTrackingConverged)
	// p3 has the same range but is NOT converged — must be skipped.
	p3 := newRelayTestPeer(3)
	p3.firstLedgerSeq, p3.lastLedgerSeq = 100, 200
	p3.setTracking(PeerTrackingDiverged)
	for _, p := range []*Peer{p2, p3} {
		o.peers[p.id] = p
	}

	got, ok := o.PeerWithLedger([32]byte{}, 150, 0)
	require.True(t, ok, "converged peer covering the seq qualifies without a hash")
	assert.Equal(t, PeerID(2), got)

	_, ok = o.PeerWithLedger([32]byte{}, 250, 0)
	assert.False(t, ok, "seq outside every advertised range")

	_, ok = o.PeerWithLedger([32]byte{}, 150, 2)
	assert.False(t, ok, "only the diverged peer remains after excluding p2")
}

// TestOverlay_PeerWithLedger_SkipsDisconnected pins that a peer holding the
// ledger but not in PeerStateConnected is not a relay target.
func TestOverlay_PeerWithLedger_SkipsDisconnected(t *testing.T) {
	target := hash32(0xAA)
	o := &Overlay{peers: make(map[PeerID]*Peer)}

	p := newRelayTestPeer(2)
	p.state = PeerStateConnecting
	p.AddLedger(target)
	o.peers[p.id] = p

	_, ok := o.PeerWithLedger(target, 0, 0)
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
	assert.Contains(t, []PeerID{2, 6}, got, "an advertiser of root is selected, never p8")

	got, ok = o.PeerWithTxSet(root, 2)
	require.True(t, ok)
	assert.Equal(t, PeerID(6), got, "excluding p2 leaves p6 as the only advertiser")

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
