package adaptor

import (
	"errors"

	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/LeJamon/go-xrpl/shamap"
)

func useLedgerNodeDepth(nodes []message.LedgerNode) error {
	for i := range nodes {
		node := &nodes[i]
		id, err := shamap.ParseNodeID(node.NodeID)
		if err != nil {
			return err
		}
		if len(node.NodeData) == 0 {
			return errors.New("empty ledger node data")
		}
		switch protocol.WireType(node.NodeData[len(node.NodeData)-1]) {
		case protocol.WireTypeInner, protocol.WireTypeCompressedInner:
			node.ID = node.NodeID
		case protocol.WireTypeAccountState, protocol.WireTypeTransaction, protocol.WireTypeTransactionWithMeta:
			depth := uint32(id.Depth())
			node.Depth = &depth
		default:
			return errors.New("invalid ledger node type")
		}
		node.NodeID = nil
	}
	return nil
}

func relayLedgerNodes(data *message.LedgerData, useDepth bool) ([]message.LedgerNode, error) {
	nodes := append([]message.LedgerNode(nil), data.Nodes...)
	legacy := false
	for i := range nodes {
		node := &nodes[i]
		if len(node.NodeData) == 0 {
			return nil, errors.New("empty ledger node data")
		}
		if data.InfoType == message.LedgerInfoBase {
			if node.NodeID != nil || node.ID != nil || node.Depth != nil {
				return nil, errors.New("ledger base has a node reference")
			}
			continue
		}
		if i == 0 {
			legacy = node.NodeID != nil
		} else if legacy != (node.NodeID != nil) {
			return nil, errors.New("mixed ledger node reference formats")
		}
		if useDepth || legacy {
			continue
		}
		switch {
		case node.ID != nil:
			node.NodeID = node.ID
			node.ID = nil
		case node.Depth != nil:
			id, err := node.SHAMapNodeID()
			if err != nil {
				return nil, err
			}
			node.NodeID = id.Bytes()
			node.Depth = nil
		default:
			return nil, errors.New("missing ledger node reference")
		}
	}
	return nodes, nil
}
