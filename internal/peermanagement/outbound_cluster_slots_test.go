package peermanagement

import (
	"testing"

	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/cluster"
	"github.com/stretchr/testify/require"
)

// An outbound peer that is a cluster member takes a reserved slot in rippled
// (ConnectAttempt activates it with reserved = cluster membership, and
// Counts::adjust keeps reserved slots out of outActive), so it does not use
// up one of the MaxOutbound ordinary slots.
func TestOrdinaryOutboundCount_ExcludesClusterPeers(t *testing.T) {
	clusterID, err := NewIdentity()
	require.NoError(t, err)
	otherID, err := NewIdentity()
	require.NoError(t, err)
	clusterPub, err := addresscodec.EncodeNodePublicKey(clusterID.PublicKey())
	require.NoError(t, err)

	cfg := DefaultConfig()
	o := &Overlay{
		cfg:       cfg,
		cluster:   cluster.New(),
		discovery: NewDiscovery(&cfg, nil),
		peers:     make(map[PeerID]*Peer),
	}
	require.NoError(t, o.cluster.Load([]string{clusterPub}))

	clusterPeer := makeClusterTestPeer(t, clusterID, "192.0.2.10", 51235)
	otherPeer := makeClusterTestPeer(t, otherID, "192.0.2.11", 51235)
	otherPeer.id = PeerID(2)
	o.peers[clusterPeer.id] = clusterPeer
	o.peers[otherPeer.id] = otherPeer

	require.Equal(t, 1, o.ordinaryOutboundCount())
}
