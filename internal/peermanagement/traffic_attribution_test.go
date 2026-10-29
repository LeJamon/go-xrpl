package peermanagement

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
)

// Based on rippled traffic_attribution_test.cpp testInboundCluster
// (XRPLF/rippled#8319): a cluster message counts as cluster traffic only from
// a configured member, judged per message, and as unknown from anyone else.
func TestInboundClusterTrafficAttribution(t *testing.T) {
	memberID, err := GenerateIdentity()
	require.NoError(t, err)
	outsiderID, err := GenerateIdentity()
	require.NoError(t, err)

	o, err := New(WithDataDir(t.TempDir()))
	require.NoError(t, err)
	member := newPeerWithRemoteKey(t, o, 1, Endpoint{Host: "192.0.2.40", Port: 51235}, memberID)
	outsider := newPeerWithRemoteKey(t, o, 2, Endpoint{Host: "192.0.2.41", Port: 51235}, outsiderID)
	require.NoError(t, o.addPeer(outsider))
	require.NoError(t, o.addPeer(member))

	assert.Equal(t, CategoryUnknown, outsider.inboundCategory(message.TypeCluster))
	assert.Equal(t, CategoryUnknown, member.inboundCategory(message.TypeCluster), "not yet a member")

	pub, err := addresscodec.EncodeNodePublicKey(memberID.PublicKey())
	require.NoError(t, err)
	require.NoError(t, o.cluster.Load([]string{pub + " test-member"}))
	assert.Equal(t, CategoryCluster, member.inboundCategory(message.TypeCluster))
	assert.Equal(t, CategoryUnknown, outsider.inboundCategory(message.TypeCluster))

	// Every other category is left as the message type says, member or not.
	for _, msgType := range []message.MessageType{message.TypePing, message.TypeValidation, message.TypeGetLedger} {
		assert.Equal(t, CategorizeMessage(msgType), outsider.inboundCategory(msgType))
		assert.Equal(t, CategorizeMessage(msgType), member.inboundCategory(msgType))
	}

	// A peer the overlay never admitted has no membership check.
	assert.Equal(t, CategoryUnknown, (&Peer{}).inboundCategory(message.TypeCluster))
}
