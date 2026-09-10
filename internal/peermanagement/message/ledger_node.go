package message

import (
	"errors"

	"github.com/LeJamon/go-xrpl/shamap"
)

// SHAMapNodeID validates a node reference against its serialized node.
func (n LedgerNode) SHAMapNodeID() (shamap.NodeID, error) {
	treeNode, err := shamap.DeserializeFromWire(n.NodeData)
	if err != nil {
		return shamap.NodeID{}, err
	}
	modern := n.ID != nil || n.Depth != nil
	if modern && n.NodeID != nil || n.ID != nil && n.Depth != nil {
		return shamap.NodeID{}, errors.New("ambiguous ledger node reference")
	}
	leaf, isLeaf := treeNode.(shamap.LeafReader)
	if modern {
		if isLeaf {
			if n.Depth == nil || *n.Depth > 64 {
				return shamap.NodeID{}, errors.New("invalid ledger leaf depth")
			}
			return shamap.NewNodeID(uint8(*n.Depth), leaf.Item().Key())
		}
		if n.ID == nil {
			return shamap.NodeID{}, errors.New("missing ledger inner node id")
		}
		return shamap.ParseNodeID(n.ID)
	}
	id, err := shamap.ParseNodeID(n.NodeID)
	if err != nil {
		return shamap.NodeID{}, err
	}
	if isLeaf {
		prefix, err := shamap.NewNodeID(id.Depth(), leaf.Item().Key())
		if err != nil || prefix != id {
			return shamap.NodeID{}, errors.New("ledger leaf key does not match node id")
		}
	}
	return id, nil
}
