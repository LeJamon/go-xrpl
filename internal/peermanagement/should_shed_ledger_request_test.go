package peermanagement

import (
	"testing"

	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/cluster"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestShouldShedLedgerRequest mirrors rippled
// PeerImp::processLedgerRequest's load gate (PeerImp.cpp:3322-3332) for
// ledger-BODY requests. Each subtest pins one branch. tx-set candidate
// requests never reach this gate (handled separately in the router), so
// they are not exercised here.
func TestShouldShedLedgerRequest(t *testing.T) {
	t.Run("unknown peer is never shed", func(t *testing.T) {
		o := &Overlay{peers: make(map[PeerID]*Peer)}
		assert.False(t, o.ShouldShedLedgerRequest(PeerID(99), true))
	})

	t.Run("not loaded + drained queue → serve", func(t *testing.T) {
		ident, err := NewIdentity()
		require.NoError(t, err)
		o := &Overlay{peers: make(map[PeerID]*Peer)}
		o.peers[1] = relayTestPeer(t, ident, 1, true)
		assert.False(t, o.ShouldShedLedgerRequest(1, false))
	})

	t.Run("fee-loaded + non-cluster peer → shed", func(t *testing.T) {
		ident, err := NewIdentity()
		require.NoError(t, err)
		o := &Overlay{peers: make(map[PeerID]*Peer)}
		o.peers[1] = relayTestPeer(t, ident, 1, true)
		assert.True(t, o.ShouldShedLedgerRequest(1, true),
			"isLoadedLocal() && !cluster() → drop (PeerImp.cpp:3328)")
	})

	t.Run("saturated send queue → shed even when not loaded", func(t *testing.T) {
		ident, err := NewIdentity()
		require.NoError(t, err)
		o := &Overlay{peers: make(map[PeerID]*Peer)}
		p := relayTestPeer(t, ident, 1, true)
		o.peers[1] = p
		for i := 0; i < peerSendQueueDropThreshold; i++ {
			p.send <- []byte{0x00}
		}
		assert.True(t, o.ShouldShedLedgerRequest(1, false),
			"send_queue_ >= dropSendQueue → drop (PeerImp.cpp:3322)")
	})

	t.Run("cluster member is exempt from the load gate", func(t *testing.T) {
		id, err := NewIdentity()
		require.NoError(t, err)
		pub, err := addresscodec.EncodeNodePublicKey(id.PublicKey())
		require.NoError(t, err)
		o := &Overlay{cluster: cluster.New(), peers: make(map[PeerID]*Peer)}
		require.NoError(t, o.cluster.Load([]string{pub}))
		p := makeClusterTestPeer(t, id, "192.0.2.30", 51235)
		o.peers[p.id] = p
		assert.False(t, o.ShouldShedLedgerRequest(p.id, true),
			"cluster peers are exempt from the load gate (PeerImp.cpp:3328 !cluster())")
	})
}
