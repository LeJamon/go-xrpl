package shamap

import (
	"errors"
)

// SerializeRoot serializes the root node for wire transmission.
// This is typically used when sending the tree's root to a peer
// to initiate synchronization.
//
// Returns the serialized wire format of the root node.
func (sm *SHAMap) SerializeRoot() ([]byte, error) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	if sm.root == nil {
		return nil, errors.New("no root node")
	}

	return sm.root.SerializeForWire()
}
