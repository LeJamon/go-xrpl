package peermanagement

import (
	"bytes"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOverlayHandlePingPongCopiesOnlySequence(t *testing.T) {
	peer := newTestPeer(t, 7)
	o := newTestOverlayWithPeers(map[PeerID]*Peer{7: peer})

	tests := []struct {
		name    string
		payload []byte
		seq     uint32
		seqSet  bool
	}{
		{name: "absent", payload: []byte{0x08, 0x00}},
		{
			name:    "explicit zero",
			payload: []byte{0x08, 0x00, 0x10, 0x00},
			seqSet:  true,
		},
		{
			name:    "legacy unknown fields",
			payload: []byte{0x08, 0x00, 0x10, 0x0b, 0x18, 0x16, 0x20, 0x21},
			seq:     11,
			seqSet:  true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.True(t, o.handlePing(Event{PeerID: 7, Payload: tt.payload}))

			frame := requireOutboundFrame(t, peer)
			header, replyPayload, err := readTestFrame(bytes.NewReader(frame))
			require.NoError(t, err)
			require.Equal(t, message.TypePing, header.MessageType)
			decoded, err := message.Decode(message.TypePing, replyPayload)
			require.NoError(t, err)
			pong := decoded.(*message.Ping)

			assert.Equal(t, message.PingTypePong, pong.PType)
			assert.Equal(t, tt.seq, pong.Seq)
			assert.Equal(t, tt.seqSet, pong.HasSeq())
			expectedPayload, err := message.Encode(&message.Ping{
				PType:  message.PingTypePong,
				Seq:    tt.seq,
				SeqSet: tt.seqSet,
			})
			require.NoError(t, err)
			assert.Equal(t, expectedPayload, replyPayload)
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
