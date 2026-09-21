package peermanagement

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"testing"
	"time"

	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/cluster"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	peerproto "github.com/LeJamon/go-xrpl/internal/peermanagement/proto"
	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protowire"
	pb "google.golang.org/protobuf/proto"
)

func pingPayloadWithUnknownFields() []byte {
	payload := []byte{0x08, 0x00, 0x10, 0x00}
	for i := 0; i < 8; i++ {
		payload = protowire.AppendTag(payload, 100, protowire.BytesType)
		payload = protowire.AppendBytes(payload, bytes.Repeat([]byte{0x55}, 64))
	}
	return payload
}

func pingWireFrame(t *testing.T, compressed bool, payload []byte) []byte {
	t.Helper()
	if !compressed {
		frame, err := message.BuildWireMessage(message.TypePing, payload)
		require.NoError(t, err)
		return frame
	}

	compressedPayload, err := message.CompressLZ4(payload)
	require.NoError(t, err)
	require.NotEmpty(t, compressedPayload)
	return rawTestWireMessage(
		message.TypePing,
		compressedPayload,
		message.AlgorithmLZ4,
		uint32(len(payload)),
	)
}

func clusterPayloadWithUnknowns(t *testing.T, publicKey string, reportTime uint32) ([]byte, []byte) {
	t.Helper()
	canonical := &peerproto.TMCluster{ClusterNodes: []*peerproto.TMClusterNode{{
		PublicKey:  pb.String(publicKey),
		ReportTime: pb.Uint32(reportTime),
		NodeLoad:   pb.Uint32(321),
		NodeName:   pb.String("updated"),
		Address:    pb.String("192.0.2.10:51235"),
	}}}
	known, err := pb.Marshal(canonical)
	require.NoError(t, err)
	node, err := pb.Marshal(canonical.ClusterNodes[0])
	require.NoError(t, err)
	node = protowire.AppendTag(node, 100, protowire.VarintType)
	node = protowire.AppendVarint(node, 999)
	wire := protowire.AppendTag(nil, 1, protowire.BytesType)
	wire = protowire.AppendBytes(wire, node)
	wire = protowire.AppendTag(wire, 100, protowire.BytesType)
	wire = protowire.AppendBytes(wire, []byte("unknown root field"))
	for i := 0; i < 8; i++ {
		wire = protowire.AppendTag(wire, 101, protowire.BytesType)
		wire = protowire.AppendBytes(wire, bytes.Repeat([]byte{0x55}, 64))
	}
	return known, wire
}

func clusterWireFrame(t *testing.T, compressed bool, payload []byte) []byte {
	t.Helper()
	if !compressed {
		frame, err := message.BuildWireMessage(message.TypeCluster, payload)
		require.NoError(t, err)
		return frame
	}
	compressedPayload, err := message.CompressLZ4(payload)
	require.NoError(t, err)
	require.NotEmpty(t, compressedPayload)
	return rawTestWireMessage(
		message.TypeCluster,
		compressedPayload,
		message.AlgorithmLZ4,
		uint32(len(payload)),
	)
}

func TestPeerReadLoopDispatchesNormalizedPingForBothWireModes(t *testing.T) {
	for _, compressed := range []bool{false, true} {
		t.Run(map[bool]string{false: "uncompressed", true: "lz4"}[compressed], func(t *testing.T) {
			identity, err := NewIdentity()
			require.NoError(t, err)
			events := make(chan Event, 1)
			peer := NewPeer(
				PeerID(1906),
				Endpoint{Host: "127.0.0.1", Port: 51235},
				false,
				identity,
				events,
			)
			if compressed {
				peer.handshakeCfg.EnableCompression = true
				peer.capabilities = NewPeerCapabilities()
				peer.capabilities.Features.Enable(FeatureCompression)
			}

			payload := pingPayloadWithUnknownFields()
			peer.bufReader = bufio.NewReader(bytes.NewReader(pingWireFrame(t, compressed, payload)))
			err = peer.readLoop(context.Background())
			require.ErrorIs(t, err, io.EOF)
			require.Len(t, events, 1)
			event := <-events
			require.Equal(t, message.TypePing, event.MessageType)
			require.Equal(t, payload, event.Payload)

			o := &Overlay{
				cfg:            DefaultConfig(),
				peers:          map[PeerID]*Peer{peer.ID(): peer},
				outboundBudget: newOutboundBudget(DefaultOutboundRetainedBytes, 1),
			}
			o.attachOutboundBudget(peer)
			o.handleEvent(event)

			replyFrame := requireOutboundFrame(t, peer)
			header, replyPayload, err := readTestFrame(bytes.NewReader(replyFrame))
			require.NoError(t, err)
			require.Equal(t, message.TypePing, header.MessageType)
			reply, err := message.Decode(message.TypePing, replyPayload)
			require.NoError(t, err)
			pong := reply.(*message.Ping)
			require.Equal(t, message.PingTypePong, pong.PType)
			require.True(t, pong.HasSeq(), "explicit zero sequence presence must reach the handler")
			require.Zero(t, pong.Seq)

			canonical, err := message.Encode(&message.Ping{
				PType:  message.PingTypePong,
				SeqSet: true,
			})
			require.NoError(t, err)
			require.Equal(t, canonical, replyPayload,
				"handler response must contain recognized fields only")
		})
	}
}

func TestPeerReadLoopDispatchesNestedNormalizedClusterForBothWireModes(t *testing.T) {
	for _, compressed := range []bool{false, true} {
		t.Run(map[bool]string{false: "uncompressed", true: "lz4"}[compressed], func(t *testing.T) {
			identity, err := NewIdentity()
			require.NoError(t, err)
			peerIdentity, err := NewIdentity()
			require.NoError(t, err)
			peerToken := NewPublicKeyTokenFromBtcec(peerIdentity.BtcecPublicKey())
			peerPublicKey, err := addresscodec.EncodeNodePublicKey(peerToken.Bytes())
			require.NoError(t, err)
			events := make(chan Event, 1)
			peer := NewPeer(
				PeerID(1965),
				Endpoint{Host: "127.0.0.1", Port: 51235},
				false,
				identity,
				events,
			)
			peer.remotePubKey = peerToken
			if compressed {
				peer.handshakeCfg.EnableCompression = true
				peer.capabilities = NewPeerCapabilities()
				peer.capabilities.Features.Enable(FeatureCompression)
			}

			clusterRegistry := cluster.New()
			require.NoError(t, clusterRegistry.Load([]string{peerPublicKey + " peer"}))
			now := time.Unix(2_000_000_000, 0).UTC()
			known, wirePayload := clusterPayloadWithUnknowns(t, peerPublicKey, protocol.ToRippleTime(now))
			peer.bufReader = bufio.NewReader(bytes.NewReader(clusterWireFrame(t, compressed, wirePayload)))
			err = peer.readLoop(context.Background())
			require.ErrorIs(t, err, io.EOF)
			require.Len(t, events, 1)
			event := <-events
			require.Equal(t, message.TypeCluster, event.MessageType)

			decoded, err := message.Decode(message.TypeCluster, event.Payload)
			require.NoError(t, err)
			reencoded, err := message.Encode(decoded)
			require.NoError(t, err)
			require.Equal(t, known, reencoded,
				"nested ingress conversion must re-encode recognized fields only")

			var fees []uint32
			o := &Overlay{
				cfg:            DefaultConfig(),
				peers:          map[PeerID]*Peer{peer.ID(): peer},
				cluster:        clusterRegistry,
				clock:          func() time.Time { return now },
				clusterFeeSink: func(fee uint32) { fees = append(fees, fee) },
			}
			o.handleEvent(event)

			member, ok := clusterRegistry.Member(peerToken.Bytes())
			require.True(t, ok)
			require.Equal(t, "updated", member.Name)
			require.Equal(t, uint32(321), member.LoadFee)
			require.Equal(t, now, member.ReportTime)
			require.Equal(t, []uint32{321}, fees)
		})
	}
}
