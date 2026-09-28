package message

import (
	"bytes"
	"testing"

	wire "github.com/LeJamon/go-xrpl/internal/peermanagement/proto"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func nodeDepth(value uint32) *uint32 { return &value }

func TestLedgerNodeReferences(t *testing.T) {
	key := [32]byte{0xab, 0xcd}
	for _, nodeType := range []shamap.NodeType{shamap.NodeTypeAccountState, shamap.NodeTypeTransactionWithMeta, shamap.NodeTypeTransactionNoMeta} {
		t.Run(nodeType.String(), func(t *testing.T) {
			m := shamap.New(shamap.TypeState)
			require.NoError(t, m.PutWithNodeType(key, bytes.Repeat([]byte{1}, 40), nodeType))
			nodes, err := m.WalkWireNodes()
			require.NoError(t, err)
			var leafData, innerData []byte
			var leafKey [32]byte
			for _, node := range nodes {
				decoded, err := shamap.DeserializeFromWire(node.Data)
				require.NoError(t, err)
				if leaf, ok := decoded.(shamap.LeafReader); ok {
					leafData, leafKey = node.Data, leaf.Item().Key()
				} else {
					innerData = node.Data
				}
			}
			require.NotEmpty(t, leafData)
			root := make([]byte, shamap.NodeIDSize)
			for _, depth := range []uint32{0, 1, 63, 64} {
				want, err := shamap.NewNodeID(uint8(depth), leafKey)
				require.NoError(t, err)
				for _, node := range []LedgerNode{
					{NodeData: leafData, Depth: nodeDepth(depth)},
					{NodeData: leafData, NodeID: want.Bytes()},
				} {
					got, err := node.SHAMapNodeID()
					require.NoError(t, err)
					require.Equal(t, want, got)
				}
			}
			wrongKey := leafKey
			wrongKey[0] ^= 0xff
			wrongID, err := shamap.NewNodeID(64, wrongKey)
			require.NoError(t, err)
			cases := map[string]LedgerNode{
				"missing":            {NodeData: leafData},
				"empty legacy":       {NodeData: leafData, NodeID: []byte{}},
				"mixed":              {NodeData: leafData, NodeID: root, Depth: nodeDepth(0)},
				"empty legacy mixed": {NodeData: leafData, NodeID: []byte{}, Depth: nodeDepth(0)},
				"both modern":        {NodeData: leafData, ID: root, Depth: nodeDepth(0)},
				"leaf id":            {NodeData: leafData, ID: root},
				"too deep":           {NodeData: leafData, Depth: nodeDepth(65)},
				"max uint32":         {NodeData: leafData, Depth: nodeDepth(^uint32(0))},
				"wrong prefix":       {NodeData: leafData, NodeID: wrongID.Bytes()},
				"bad data":           {NodeData: []byte{255}, Depth: nodeDepth(1)},
				"empty data":         {Depth: nodeDepth(0)},
				"inner depth":        {NodeData: innerData, Depth: nodeDepth(0)},
				"empty inner id":     {NodeData: innerData, ID: []byte{}},
			}
			for name, node := range cases {
				t.Run(name, func(t *testing.T) { _, err := node.SHAMapNodeID(); require.Error(t, err) })
			}
			for _, node := range []LedgerNode{{NodeData: innerData, ID: root}, {NodeData: innerData, NodeID: root}} {
				got, err := node.SHAMapNodeID()
				require.NoError(t, err)
				require.True(t, got.IsRoot())
			}
		})
	}
}

func TestLedgerNodeReferenceWire(t *testing.T) {
	cases := []struct {
		name string
		node LedgerNode
		wire *wire.TMLedgerNode
	}{
		{"legacy", LedgerNode{NodeData: []byte{1}, NodeID: []byte{2}}, &wire.TMLedgerNode{Nodedata: []byte{1}, Nodeid: []byte{2}}},
		{"inner", LedgerNode{NodeData: []byte{1}, ID: []byte{2}}, &wire.TMLedgerNode{Nodedata: []byte{1}, Reference: &wire.TMLedgerNode_Id{Id: []byte{2}}}},
		{"empty inner", LedgerNode{NodeData: []byte{1}, ID: []byte{}}, &wire.TMLedgerNode{Nodedata: []byte{1}, Reference: &wire.TMLedgerNode_Id{Id: []byte{}}}},
		{"root leaf", LedgerNode{NodeData: []byte{1}, Depth: nodeDepth(0)}, &wire.TMLedgerNode{Nodedata: []byte{1}, Reference: &wire.TMLedgerNode_Depth{Depth: 0}}},
		{"last leaf", LedgerNode{NodeData: []byte{1}, Depth: nodeDepth(64)}, &wire.TMLedgerNode{Nodedata: []byte{1}, Reference: &wire.TMLedgerNode_Depth{Depth: 64}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg := &LedgerData{LedgerHash: make([]byte, 32), LedgerSeq: 1, InfoType: LedgerInfoAsNode, Nodes: []LedgerNode{tc.node}}
			got, err := Encode(msg)
			require.NoError(t, err)
			kind := wire.TMLedgerInfoType_liAS_NODE
			want, err := proto.Marshal(&wire.TMLedgerData{LedgerHash: make([]byte, 32), LedgerSeq: proto.Uint32(1), Type: &kind, Nodes: []*wire.TMLedgerNode{tc.wire}})
			require.NoError(t, err)
			require.Equal(t, want, got)
			decoded, err := Decode(TypeLedgerData, want)
			require.NoError(t, err)
			require.Equal(t, tc.node, decoded.(*LedgerData).Nodes[0])
		})
	}
}
