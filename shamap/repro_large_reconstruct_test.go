package shamap

import (
	"crypto/rand"
	"crypto/sha512"
	"math/bits"
	"testing"
)

func bitsOnesCount16(x uint16) int { return bits.OnesCount16(x) }

// TestReproLargeTxSetReconstructFatLeaves builds a SHAMap of N tx blobs,
// extracts the wire nodes WITHOUT leaf blobs (fatLeaves=false — the
// rippled liTS_CANDIDATE serve behaviour at PeerImp.cpp:3318), and
// reconstructs from those inner-only nodes plus the original blobs
// (sourced "locally" like goxrpl's pending-pool fill). Asserts that the
// reconstructed root matches the original, then re-walks leaves into
// blobs and rebuilds a third SHAMap via the same Put path used in
// adaptor.NewTxSet — that third hash MUST also match. Reproduces the
// iter4 seq 28 / seq 257 stall pattern where goxrpl receives a 130-tx
// peer tx_set, "reconstructs" it, but the computed root differs from
// the expected.
func TestReproLargeTxSetReconstructFatLeaves(t *testing.T) {
	const N = 130

	source, err := New(TypeTransaction)
	if err != nil {
		t.Fatalf("New source: %v", err)
	}

	blobs := make([][]byte, N)
	for i := 0; i < N; i++ {
		blob := make([]byte, 64)
		if _, err := rand.Read(blob); err != nil {
			t.Fatalf("rand: %v", err)
		}
		blobs[i] = blob
		key := computeReproKey(blob)
		if err := source.PutWithNodeType(key, blob, NodeTypeTransactionNoMeta); err != nil {
			t.Fatalf("Put %d: %v", i, err)
		}
	}

	sourceHash, err := source.Hash()
	if err != nil {
		t.Fatalf("source hash: %v", err)
	}
	t.Logf("source root: %x", sourceHash[:8])

	rootData, err := source.SerializeRoot()
	if err != nil {
		t.Fatalf("SerializeRoot: %v", err)
	}
	wireNodes, err := source.WalkWireNodes()
	if err != nil {
		t.Fatalf("WalkWireNodes: %v", err)
	}
	t.Logf("source wire nodes: %d", len(wireNodes))

	// Reconstruct using ONLY inner nodes from peer + local blob pool.
	// Mirrors what handleTxSetData does when rippled sends fatLeaves=false.
	dest, err := New(TypeTransaction)
	if err != nil {
		t.Fatalf("New dest: %v", err)
	}
	if err := dest.StartSync(); err != nil {
		t.Fatalf("StartSync: %v", err)
	}
	if err := dest.AddRootNode(sourceHash, rootData); err != nil {
		t.Fatalf("AddRootNode: %v", err)
	}

	innerCount := 0
	for i, w := range wireNodes {
		nid, err := UnmarshalBinary(w.NodeID)
		if err != nil {
			t.Fatalf("UnmarshalBinary[%d]: %v", i, err)
		}
		if nid.IsRoot() {
			continue
		}
		node, err := DeserializeNodeFromWire(w.Data)
		if err != nil {
			continue
		}
		if node.IsLeaf() {
			continue
		}
		if err := dest.AddKnownNodeByID(nid, w.Data); err != nil {
			t.Logf("AddKnownNodeByID[%d] depth=%d nodeID=%x dataLen=%d wireType=0x%02x", i, nid.Depth(), w.NodeID, len(w.Data), w.Data[len(w.Data)-1])
			t.Logf("  data: %x", w.Data)
			parsedNode, _ := DeserializeNodeFromWire(w.Data)
			if parsedNode != nil {
				parsedNode.UpdateHash()
				ph := parsedNode.Hash()
				t.Logf("  parsed.Hash() = %x", ph[:])
				if pInner, ok := parsedNode.(*InnerNode); ok {
					t.Logf("  parsed.isBranch=0x%04x branchCount=%d", pInner.isBranch, bitsOnesCount16(pInner.isBranch))
					for b := 0; b < 16; b++ {
						if pInner.isBranch&(1<<uint(b)) != 0 {
							t.Logf("    branch[%d]: hash=%x", b, pInner.hashes[b][:])
						}
					}
				}
			}
			// Walk source to this NodeID, dump the actual node
			var srcNode Node = source.root
			for d := 0; d < int(nid.Depth()); d++ {
				srcInner, ok := srcNode.(*InnerNode)
				if !ok {
					break
				}
				br := selectBranchForPath(nid.ID(), d)
				srcInner.mu.RLock()
				storedHash := srcInner.hashes[br]
				child := srcInner.children[br]
				srcInner.mu.RUnlock()
				t.Logf("  source@d%d branch%d storedHash=%x childIsNil=%v", d, br, storedHash[:], child == nil)
				if child != nil {
					actualHash := child.Hash()
					t.Logf("            child.Hash()=%x  matches=%v", actualHash[:], actualHash == storedHash)
				}
				srcNode = child
			}
			if srcNode != nil {
				srcHash := srcNode.Hash()
				t.Logf("  source-at-target.Hash()=%x", srcHash[:])
				if srcInner, ok := srcNode.(*InnerNode); ok {
					t.Logf("  source isBranch=0x%04x", srcInner.isBranch)
					for b := 0; b < 16; b++ {
						if srcInner.isBranch&(1<<uint(b)) != 0 {
							t.Logf("    src branch[%d]: hash=%x childNil=%v", b, srcInner.hashes[b][:], srcInner.children[b] == nil)
						}
					}
				}
			}
			t.Fatalf("AddKnownNodeByID[%d] depth=%d: %v", i, nid.Depth(), err)
		}
		innerCount++
	}
	t.Logf("inner nodes loaded: %d", innerCount)

	if err := dest.FinishSync(); err == nil {
		t.Logf("FinishSync succeeded already (no leaves needed?)")
	}
	missing := dest.GetMissingNodes(N+10, nil)
	t.Logf("missing leaves: %d", len(missing))

	blobByHash := make(map[[32]byte][]byte, N)
	for _, b := range blobs {
		blobByHash[computeReproKey(b)] = b
	}
	filled := 0
	for _, m := range missing {
		blob, ok := blobByHash[m.Hash]
		if !ok {
			t.Logf("blob missing from pool for hash %x", m.Hash[:8])
			continue
		}
		wire := make([]byte, len(blob)+1)
		copy(wire, blob)
		wire[len(blob)] = 0x00 // protocol.WireTypeTransaction = 0
		if err := dest.AddKnownNode(m.Hash, wire); err != nil {
			t.Fatalf("AddKnownNode for leaf %x: %v", m.Hash[:8], err)
		}
		filled++
	}
	t.Logf("filled leaves: %d/%d", filled, len(missing))

	if err := dest.FinishSync(); err != nil {
		t.Fatalf("FinishSync after local fill: %v", err)
	}

	destHash, err := dest.Hash()
	if err != nil {
		t.Fatalf("dest hash: %v", err)
	}
	t.Logf("dest root after reconstruct: %x", destHash[:8])
	if destHash != sourceHash {
		t.Errorf("CHECKPOINT 1 FAIL — reconstructed root differs: want %x got %x", sourceHash[:8], destHash[:8])
	}

	var extractedBlobs [][]byte
	_ = dest.ForEach(func(item *Item) bool {
		extractedBlobs = append(extractedBlobs, item.Data())
		return true
	})
	t.Logf("extracted blobs: %d", len(extractedBlobs))

	rebuilt, err := New(TypeTransaction)
	if err != nil {
		t.Fatalf("New rebuilt: %v", err)
	}
	for i, b := range extractedBlobs {
		key := computeReproKey(b)
		if err := rebuilt.PutWithNodeType(key, b, NodeTypeTransactionNoMeta); err != nil {
			t.Fatalf("rebuilt Put %d: %v", i, err)
		}
	}
	rebuiltHash, err := rebuilt.Hash()
	if err != nil {
		t.Fatalf("rebuilt hash: %v", err)
	}
	t.Logf("rebuilt root: %x", rebuiltHash[:8])
	if rebuiltHash != sourceHash {
		t.Errorf("CHECKPOINT 2 FAIL — rebuild from extracted blobs differs: want %x got %x", sourceHash[:8], rebuiltHash[:8])
	}
}

func computeReproKey(blob []byte) [32]byte {
	prefix := [4]byte{'T', 'X', 'N', 0x00}
	h := sha512.New()
	h.Write(prefix[:])
	h.Write(blob)
	sum := h.Sum(nil)
	var out [32]byte
	copy(out[:], sum[:32])
	return out
}
