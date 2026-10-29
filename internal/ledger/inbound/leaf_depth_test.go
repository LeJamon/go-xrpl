package inbound

import (
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/LeJamon/go-xrpl/crypto/sha512half"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/LeJamon/go-xrpl/shamap"
)

// singleBranchInner returns the compressed wire form and hash of an inner node
// whose only child, on branch 0, has the given hash.
func singleBranchInner(child [32]byte) ([]byte, [32]byte) {
	hashes := make([]byte, shamap.BranchFactor*32)
	copy(hashes, child[:])
	hash := sha512half.Sum(protocol.HashPrefixInnerNode().Bytes(), hashes)
	wire := append(append([]byte{}, child[:]...), 0, byte(protocol.WireTypeCompressedInner))
	return wire, hash
}

// Based on rippled SHAMapSync tests for XRPLF/rippled#8084 and #8086: a
// hash-verified inner node at leaf depth fails the acquisition at once.
func TestGotStateNodes_InnerAtLeafDepthFailsAcquisition(t *testing.T) {
	t.Parallel()
	// wires[d] is the inner node at depth d of a single-branch chain whose
	// last inner sits at leaf depth (64).
	const leafDepth = 64
	wires := make([][]byte, leafDepth+1)
	child := [32]byte{1}
	for depth := leafDepth; depth >= 0; depth-- {
		wires[depth], child = singleBranchInner(child)
	}
	rootHash := child

	headerData, ledgerHash := encodeHeader(header.LedgerHeader{LedgerIndex: 700, AccountHash: rootHash})
	acquisition := New(ledgerHash, 700, 16, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := acquisition.GotBase([]message.LedgerNode{{NodeData: headerData}, {NodeData: wires[0]}}); err != nil {
		t.Fatalf("GotBase: %v", err)
	}

	nodes := make([]message.LedgerNode, 0, leafDepth)
	for depth := 1; depth <= leafDepth; depth++ {
		id, err := shamap.NewNodeID(uint8(depth), [32]byte{})
		if err != nil {
			t.Fatal(err)
		}
		nodes = append(nodes, message.LedgerNode{NodeData: wires[depth], NodeID: id.Bytes()})
	}
	_, err := acquisition.GotStateNodesUsefulContext(t.Context(), nodes)
	if !errors.Is(err, ErrInvalidPeerNode) || !errors.Is(err, shamap.ErrMapInvalid) {
		t.Fatalf("GotStateNodes error = %v, want an invalid peer node that invalidates the map", err)
	}
	if acquisition.State() != StateFailed || !errors.Is(acquisition.Err(), shamap.ErrMapInvalid) {
		t.Fatalf("state = %d, err = %v, want a failed acquisition", acquisition.State(), acquisition.Err())
	}
}
