package message

import (
	"strings"
	"testing"

	peerproto "github.com/LeJamon/go-xrpl/internal/peermanagement/proto"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protowire"
	pb "google.golang.org/protobuf/proto"
)

func appendUnknownVarint(wire []byte, field protowire.Number, value uint64) []byte {
	wire = protowire.AppendTag(wire, field, protowire.VarintType)
	return protowire.AppendVarint(wire, value)
}

func appendUnknownBytes(wire []byte, field protowire.Number, value []byte) []byte {
	wire = protowire.AppendTag(wire, field, protowire.BytesType)
	return protowire.AppendBytes(wire, value)
}

func clusterWireWithUnknowns(t *testing.T) ([]byte, []byte) {
	t.Helper()
	canonical := &peerproto.TMCluster{
		ClusterNodes: []*peerproto.TMClusterNode{{
			PublicKey:  pb.String("n9MozjnGB3tpULewtkfeEnFdkn5fXjBeZbCJpyqyBhdNu7tcphmW"),
			ReportTime: pb.Uint32(7),
			NodeLoad:   pb.Uint32(9),
			NodeName:   pb.String("node"),
			Address:    pb.String("192.0.2.1:51235"),
		}},
		LoadSources: []*peerproto.TMLoadSource{{
			Name:  pb.String("source"),
			Cost:  pb.Uint32(11),
			Count: pb.Uint32(13),
		}},
	}

	known, err := pb.Marshal(canonical)
	require.NoError(t, err)
	node, err := pb.Marshal(canonical.ClusterNodes[0])
	require.NoError(t, err)
	node = appendUnknownVarint(node, 100, 101)
	source, err := pb.Marshal(canonical.LoadSources[0])
	require.NoError(t, err)
	source = appendUnknownBytes(source, 101, []byte("nested unknown"))

	wire := make([]byte, 0, len(known)+32)
	wire = protowire.AppendTag(wire, 1, protowire.BytesType)
	wire = protowire.AppendBytes(wire, node)
	wire = protowire.AppendTag(wire, 2, protowire.BytesType)
	wire = protowire.AppendBytes(wire, source)
	wire = appendUnknownVarint(wire, 100, 202)
	return known, wire
}

func decodeCapturingGenerated(t *testing.T, msgType MessageType, wire []byte) (pb.Message, Message) {
	t.Helper()
	codec := codecs[msgType]
	var generated pb.Message
	codecs[msgType] = msgCodec{
		newProto: codec.newProto,
		encode:   codec.encode,
		decode: func(pmsg pb.Message) (Message, error) {
			generated = pmsg
			return codec.decode(pmsg)
		},
	}
	defer func() { codecs[msgType] = codec }()

	decoded, err := Decode(msgType, wire)
	require.NoError(t, err)
	require.NotNil(t, generated)
	return generated, decoded
}

func TestDecodeDiscardsUnknownFieldsRecursively(t *testing.T) {
	knownWire, wire := clusterWireWithUnknowns(t)

	withoutUnknowns, err := Decode(TypeCluster, knownWire)
	require.NoError(t, err)
	generated, withUnknowns := decodeCapturingGenerated(t, TypeCluster, wire)
	cluster := generated.(*peerproto.TMCluster)
	require.Empty(t, cluster.ProtoReflect().GetUnknown())
	require.Empty(t, cluster.ClusterNodes[0].ProtoReflect().GetUnknown())
	require.Empty(t, cluster.LoadSources[0].ProtoReflect().GetUnknown())
	require.Equal(t, withoutUnknowns, withUnknowns)

	reencoded, err := Encode(withUnknowns)
	require.NoError(t, err)
	require.Equal(t, knownWire, reencoded,
		"domain conversion and re-encoding must retain known nested fields only")

	decoded := withUnknowns.(*Cluster)
	require.Len(t, decoded.ClusterNodes, 1)
	require.Equal(t, "n9MozjnGB3tpULewtkfeEnFdkn5fXjBeZbCJpyqyBhdNu7tcphmW", decoded.ClusterNodes[0].PublicKey)
	require.Equal(t, "node", decoded.ClusterNodes[0].NodeName)
	require.Equal(t, "192.0.2.1:51235", decoded.ClusterNodes[0].Address)
	require.Len(t, decoded.LoadSources, 1)
	require.Equal(t, uint32(13), decoded.LoadSources[0].Count)
}

func TestDecodePreservesOptionalPresenceWhileDroppingUnknowns(t *testing.T) {
	known := []byte{0x08, 0x00, 0x10, 0x00}
	withUnknowns := appendUnknownBytes(append([]byte(nil), known...), 100, []byte("unknown"))

	withoutUnknowns, err := Decode(TypePing, known)
	require.NoError(t, err)
	generated, withUnknownsDecoded := decodeCapturingGenerated(t, TypePing, withUnknowns)
	ping := generated.(*peerproto.TMPing)
	require.Empty(t, ping.ProtoReflect().GetUnknown())
	require.NotNil(t, ping.Seq, "explicit zero sequence presence must survive generated decoding")
	require.True(t, withUnknownsDecoded.(*Ping).HasSeq())
	require.Equal(t, uint32(0), withUnknownsDecoded.(*Ping).Seq)
	require.Equal(t, withoutUnknowns, withUnknownsDecoded)

	reencoded, err := Encode(withUnknownsDecoded)
	require.NoError(t, err)
	require.Equal(t, known, reencoded, "explicit zero presence must survive re-encoding")
}

func TestDecodeUnknownFieldsDoNotSatisfyRequiredFields(t *testing.T) {
	tests := []struct {
		name    string
		msgType MessageType
		wire    []byte
	}{
		{
			name:    "missing top-level required field",
			msgType: TypePing,
			wire:    appendUnknownVarint(nil, 100, 1),
		},
		{
			name:    "missing nested required field",
			msgType: TypeManifests,
			wire:    []byte{0x0a, 0x03, 0xa0, 0x06, 0x01},
		},
		{
			name:    "unknown required enum",
			msgType: TypeTransaction,
			wire:    []byte{0x0a, 0x01, 0x01, 0x10, 0x63, 0xa0, 0x06, 0x01},
		},
		{
			name:    "truncated unknown field",
			msgType: TypePing,
			wire:    []byte{0xa0, 0x06},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Decode(test.msgType, test.wire)
			require.Error(t, err)
			require.True(t,
				strings.Contains(err.Error(), "required") || strings.Contains(err.Error(), "malformed"),
				"error = %q", err)
		})
	}
}
