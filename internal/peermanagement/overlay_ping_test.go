package peermanagement

import (
	"bytes"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOverlayHandlePingPongEchoesOptionalFields(t *testing.T) {
	peer := newTestPeer(t, 7)
	o := newTestOverlayWithPeers(map[PeerID]*Peer{7: peer})

	tests := []struct {
		name string
		ping *message.Ping
	}{
		{name: "absent", ping: &message.Ping{PType: message.PingTypePing}},
		{
			name: "explicit zero",
			ping: &message.Ping{
				PType:       message.PingTypePing,
				SeqSet:      true,
				PingTimeSet: true,
				NetTimeSet:  true,
			},
		},
		{
			name: "nonzero",
			ping: &message.Ping{
				PType:    message.PingTypePing,
				Seq:      11,
				PingTime: 22,
				NetTime:  33,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload, err := message.Encode(tt.ping)
			require.NoError(t, err)
			require.True(t, o.handlePing(Event{PeerID: 7, Payload: payload}))

			frame := requireOutboundFrame(t, peer)
			header, replyPayload, err := message.ReadMessage(bytes.NewReader(frame))
			require.NoError(t, err)
			require.Equal(t, message.TypePing, header.MessageType)
			decoded, err := message.Decode(message.TypePing, replyPayload)
			require.NoError(t, err)
			pong := decoded.(*message.Ping)

			assert.Equal(t, message.PingTypePong, pong.PType)
			assert.Equal(t, tt.ping.Seq, pong.Seq)
			assert.Equal(t, tt.ping.HasSeq(), pong.HasSeq())
			assert.Equal(t, tt.ping.PingTime, pong.PingTime)
			assert.Equal(t, tt.ping.HasPingTime(), pong.HasPingTime())
			assert.Equal(t, tt.ping.NetTime, pong.NetTime)
			assert.Equal(t, tt.ping.HasNetTime(), pong.HasNetTime())
		})
	}
}

func TestOverlayHandlePongRequiresSeqPresence(t *testing.T) {
	peer := newTestPeer(t, 7)
	o := newTestOverlayWithPeers(map[PeerID]*Peer{7: peer})
	peer.recordPingSent(0, time.Now().Add(-time.Millisecond))

	absent, err := message.Encode(&message.Ping{PType: message.PingTypePong})
	require.NoError(t, err)
	require.True(t, o.handlePing(Event{PeerID: 7, Payload: absent}))
	_, measured := peer.Latency()
	assert.False(t, measured)

	explicitZero, err := message.Encode(&message.Ping{
		PType:  message.PingTypePong,
		SeqSet: true,
	})
	require.NoError(t, err)
	require.True(t, o.handlePing(Event{PeerID: 7, Payload: explicitZero}))
	_, measured = peer.Latency()
	assert.True(t, measured)
}
