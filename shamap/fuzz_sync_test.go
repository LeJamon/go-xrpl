package shamap

import (
	"testing"

	"github.com/LeJamon/go-xrpl/protocol"
)

// FuzzAddRootNode fuzzes the sync entry point that receives a root node from a peer.
// A malicious peer could send arbitrary data claiming to be a root node.
func FuzzAddRootNode(f *testing.F) {
	f.Add(makeHashSlice(0x00), []byte{})

	// Single wire type byte
	f.Add(makeHashSlice(0x01), []byte{protocol.WireTypeInner})

	// Valid full inner node
	validRoot := make([]byte, 513)
	for i := range 32 {
		validRoot[i] = 0xAA
	}
	validRoot[512] = protocol.WireTypeInner

	// Compute expected hash: construct the node first to get its hash
	node, err := NewInnerNodeFromWire(validRoot)
	if err == nil {
		h := node.Hash()
		f.Add(h[:], validRoot)
	}

	// Hash mismatch — valid data but wrong hash
	f.Add(makeHashSlice(0xFF), validRoot)

	// Leaf node (should be rejected as root)
	leafData := make([]byte, 45)
	for i := range 12 {
		leafData[i] = byte(i + 1)
	}
	for i := 12; i < 44; i++ {
		leafData[i] = 0x01
	}
	leafData[44] = protocol.WireTypeAccountState
	f.Add(makeHashSlice(0x00), leafData)

	// Compressed inner node
	compressed := make([]byte, 34)
	for i := range 32 {
		compressed[i] = 0xBB
	}
	compressed[32] = 0x00
	compressed[33] = protocol.WireTypeCompressedInner
	f.Add(makeHashSlice(0x00), compressed)

	f.Fuzz(func(t *testing.T, hashBytes []byte, data []byte) {
		if len(hashBytes) < 32 {
			return
		}
		var hash [32]byte
		copy(hash[:], hashBytes[:32])

		sm, err := New(TypeState)
		if err != nil {
			t.Fatal(err)
		}
		if err := sm.StartSync(); err != nil {
			t.Fatal(err)
		}

		// Must not panic — errors are expected for malformed data
		_ = sm.AddRootNode(hash, data)
	})
}

// FuzzAddKnownNode fuzzes the sync entry point that receives child nodes from peers.
// After setting up a root with known missing branches, we feed arbitrary data
// claiming to fill those branches.
func FuzzAddKnownNode(f *testing.F) {
	f.Add(makeHashSlice(0x00), []byte{})

	// Valid account state leaf
	leafData := make([]byte, 45)
	for i := range 12 {
		leafData[i] = byte(i + 1)
	}
	for i := 12; i < 44; i++ {
		leafData[i] = 0x01
	}
	leafData[44] = protocol.WireTypeAccountState
	f.Add(makeHashSlice(0xAA), leafData)

	// Valid inner node
	innerData := make([]byte, 513)
	innerData[512] = protocol.WireTypeInner
	f.Add(makeHashSlice(0xBB), innerData)

	// Invalid wire type
	f.Add(makeHashSlice(0xCC), []byte{0xFF})

	f.Fuzz(func(t *testing.T, hashBytes []byte, data []byte) {
		if len(hashBytes) < 32 {
			return
		}
		var hash [32]byte
		copy(hash[:], hashBytes[:32])

		// Build a syncing tree with a root that has missing children
		sm, err := New(TypeState)
		if err != nil {
			t.Fatal(err)
		}
		if err := sm.StartSync(); err != nil {
			t.Fatal(err)
		}

		// Create a root with some branches pointing to hashes (missing children)
		root := NewInnerNode()
		root.hashes[0] = makeHash(0xAA)
		root.isBranch |= 1 << 0
		root.hashes[5] = makeHash(0xBB)
		root.isBranch |= 1 << 5
		if err := root.UpdateHash(); err != nil {
			t.Fatal(err)
		}

		// Manually set the root on the syncing map
		sm.mu.Lock()
		sm.root = root
		sm.mu.Unlock()

		// Must not panic — errors are expected
		_ = sm.AddKnownNode(hash, data)
	})
}

// FuzzSyncSequence fuzzes a complete sync sequence: set root, then add nodes.
// This exercises the full sync path with arbitrary peer data.
func FuzzSyncSequence(f *testing.F) {
	// Seed: a sequence of (hash + data) pairs encoded as a byte stream
	// Format: [32-byte hash][2-byte data_len][data_bytes]...
	seed := make([]byte, 0, 600)
	// One entry: hash + small inner node
	h := makeHash(0x01)
	seed = append(seed, h[:]...)
	innerData := make([]byte, 513)
	innerData[512] = protocol.WireTypeInner
	seed = append(seed, byte(len(innerData)>>8), byte(len(innerData)&0xFF))
	seed = append(seed, innerData...)
	f.Add(seed)

	// Empty seed
	f.Add([]byte{})

	f.Fuzz(func(t *testing.T, data []byte) {
		sm, err := New(TypeState)
		if err != nil {
			t.Fatal(err)
		}
		if err := sm.StartSync(); err != nil {
			t.Fatal(err)
		}

		// Parse stream of (hash, node_data) pairs
		i := 0
		const maxOps = 50
		ops := 0

		for i < len(data) && ops < maxOps {
			// Need at least 32 bytes for hash + 2 bytes for length
			if i+34 > len(data) {
				break
			}

			var hash [32]byte
			copy(hash[:], data[i:i+32])
			i += 32

			dataLen := int(data[i])<<8 | int(data[i+1])
			i += 2

			// Cap data length to prevent huge allocations
			if dataLen > 1024 {
				dataLen = 1024
			}
			if i+dataLen > len(data) {
				break
			}

			nodeData := data[i : i+dataLen]
			i += dataLen

			// First valid-looking entry becomes root, rest are child nodes
			if ops == 0 {
				_ = sm.AddRootNode(hash, nodeData)
			} else {
				_ = sm.AddKnownNode(hash, nodeData)
			}
			ops++
		}

		// Check sync state — must not panic
		_ = sm.IsSyncing()
		_ = sm.IsComplete()
		sm.GetMissingNodes(10, nil)

		// Attempt to finish — will likely fail but must not panic
		_ = sm.FinishSync()
	})
}
