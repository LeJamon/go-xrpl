package shamap

import (
	"testing"

	"github.com/LeJamon/go-xrpl/protocol"
)

// FuzzDeserializeNodeFromWire fuzzes the main network deserialization entry point.
// DeserializeNodeFromWire dispatches based on the last byte (wire type) to one of
// five node parsers. Any crafted input must not cause a panic.
func FuzzDeserializeNodeFromWire(f *testing.F) {
	// Empty input
	f.Add([]byte{})

	// Single wire type bytes (each triggers a different parser with insufficient data)
	f.Add([]byte{protocol.WireTypeTransaction})
	f.Add([]byte{protocol.WireTypeAccountState})
	f.Add([]byte{protocol.WireTypeInner})
	f.Add([]byte{protocol.WireTypeCompressedInner})
	f.Add([]byte{protocol.WireTypeTransactionWithMeta})

	// Invalid wire type
	f.Add([]byte{0xFF})

	// Valid full inner node: 512 zero-bytes + wire type (all branches empty)
	fullInner := make([]byte, 513)
	fullInner[512] = protocol.WireTypeInner
	f.Add(fullInner)

	// Valid full inner node with one non-zero hash at branch 0
	fullInnerWithHash := make([]byte, 513)
	for i := 0; i < 32; i++ {
		fullInnerWithHash[i] = 0xAB
	}
	fullInnerWithHash[512] = protocol.WireTypeInner
	f.Add(fullInnerWithHash)

	// Valid compressed inner node: one chunk (32-byte hash + position 0)
	compressed := make([]byte, 34)
	for i := 0; i < 32; i++ {
		compressed[i] = 0xCD
	}
	compressed[32] = 0x00 // position 0
	compressed[33] = protocol.WireTypeCompressedInner
	f.Add(compressed)

	// Valid account state leaf: 12-byte data + 32-byte non-zero key + wire type
	acctState := make([]byte, 45)
	for i := 0; i < 12; i++ {
		acctState[i] = byte(i + 1)
	}
	for i := 12; i < 44; i++ {
		acctState[i] = 0x01
	}
	acctState[44] = protocol.WireTypeAccountState
	f.Add(acctState)

	// Valid transaction leaf: 12-byte data + wire type
	txLeaf := make([]byte, 13)
	for i := 0; i < 12; i++ {
		txLeaf[i] = byte(i + 1)
	}
	txLeaf[12] = protocol.WireTypeTransaction
	f.Add(txLeaf)

	// Valid transaction+meta leaf: 12-byte data + 32-byte non-zero key + wire type
	txMeta := make([]byte, 45)
	for i := 0; i < 12; i++ {
		txMeta[i] = byte(i + 1)
	}
	for i := 12; i < 44; i++ {
		txMeta[i] = 0x02
	}
	txMeta[44] = protocol.WireTypeTransactionWithMeta
	f.Add(txMeta)

	f.Fuzz(func(t *testing.T, data []byte) {
		node, err := DeserializeNodeFromWire(data)
		if err != nil {
			return
		}
		// Exercise the returned node — must not panic
		_ = node.Hash()
		_ = node.Type()
		_ = node.IsLeaf()
		_ = node.IsInner()
		_ = node.Invariants(true)
	})
}

// FuzzNewInnerNodeFromWire fuzzes inner node wire parsing directly.
// Covers both full format (512 bytes + type) and compressed format (33-byte chunks + type).
func FuzzNewInnerNodeFromWire(f *testing.F) {
	f.Add([]byte{})

	// Full format: 512 bytes + wire type
	full := make([]byte, 513)
	full[512] = protocol.WireTypeInner
	f.Add(full)

	// Full format with hashes at positions 0 and 15
	fullWithHashes := make([]byte, 513)
	for i := 0; i < 32; i++ {
		fullWithHashes[i] = 0xAA     // branch 0
		fullWithHashes[480+i] = 0xBB // branch 15
	}
	fullWithHashes[512] = protocol.WireTypeInner
	f.Add(fullWithHashes)

	// Compressed: 1 chunk
	comp1 := make([]byte, 34)
	for i := 0; i < 32; i++ {
		comp1[i] = 0xCC
	}
	comp1[32] = 0x05 // position 5
	comp1[33] = protocol.WireTypeCompressedInner
	f.Add(comp1)

	// Compressed: max 16 chunks (528 bytes + wire type)
	comp16 := make([]byte, 529)
	for i := 0; i < 16; i++ {
		offset := i * 33
		for j := 0; j < 32; j++ {
			comp16[offset+j] = byte(i + 1)
		}
		comp16[offset+32] = byte(i) // position = branch index
	}
	comp16[528] = protocol.WireTypeCompressedInner
	f.Add(comp16)

	// Compressed with invalid position (16 >= BranchFactor)
	compBad := make([]byte, 34)
	for i := 0; i < 32; i++ {
		compBad[i] = 0xDD
	}
	compBad[32] = 16 // invalid position
	compBad[33] = protocol.WireTypeCompressedInner
	f.Add(compBad)

	// Invalid wire type
	f.Add([]byte{0x00, 0x00, 0xFF})

	f.Fuzz(func(t *testing.T, data []byte) {
		node, err := NewInnerNodeFromWire(data)
		if err != nil {
			return
		}
		_ = node.Hash()
		_ = node.BranchCount()
		_ = node.Invariants(true)

		// Hash must be deterministic
		if err := node.UpdateHash(); err != nil {
			t.Fatalf("UpdateHash failed on valid node: %v", err)
		}
	})
}

// FuzzNewAccountStateLeafFromWire fuzzes account state leaf wire parsing.
// Wire format: [state_data][32-byte key][0x01]
func FuzzNewAccountStateLeafFromWire(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{protocol.WireTypeAccountState})

	// Minimum valid: 12 data + 32 non-zero key + wire type = 45 bytes
	minValid := make([]byte, 45)
	for i := 0; i < 12; i++ {
		minValid[i] = byte(i + 1)
	}
	for i := 12; i < 44; i++ {
		minValid[i] = 0x01
	}
	minValid[44] = protocol.WireTypeAccountState
	f.Add(minValid)

	// Zero key (should fail)
	zeroKey := make([]byte, 45)
	for i := 0; i < 12; i++ {
		zeroKey[i] = byte(i + 1)
	}
	// key bytes 12..43 are zero
	zeroKey[44] = protocol.WireTypeAccountState
	f.Add(zeroKey)

	// Data too short (4 bytes + 32 key + wire type)
	tooShort := make([]byte, 37)
	for i := 0; i < 4; i++ {
		tooShort[i] = byte(i + 1)
	}
	for i := 4; i < 36; i++ {
		tooShort[i] = 0x01
	}
	tooShort[36] = protocol.WireTypeAccountState
	f.Add(tooShort)

	f.Fuzz(func(t *testing.T, data []byte) {
		node, err := NewAccountStateLeafFromWire(data)
		if err != nil {
			return
		}
		_ = node.Hash()
		_ = node.Type()
		_ = node.Item()
		_ = node.Invariants(true)
	})
}

// FuzzNewTransactionLeafFromWire fuzzes transaction leaf wire parsing.
// Wire format: [tx_data][0x00] — key derived by hashing.
func FuzzNewTransactionLeafFromWire(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{protocol.WireTypeTransaction})

	// Minimum valid: 12 data + wire type = 13 bytes
	minValid := make([]byte, 13)
	for i := 0; i < 12; i++ {
		minValid[i] = byte(i + 1)
	}
	minValid[12] = protocol.WireTypeTransaction
	f.Add(minValid)

	// Larger payload
	larger := make([]byte, 65)
	for i := 0; i < 64; i++ {
		larger[i] = byte(i)
	}
	larger[64] = protocol.WireTypeTransaction
	f.Add(larger)

	f.Fuzz(func(t *testing.T, data []byte) {
		node, err := NewTransactionLeafFromWire(data)
		if err != nil {
			return
		}
		_ = node.Hash()
		_ = node.Type()
		_ = node.Item()
		_ = node.Invariants(true)
	})
}

// FuzzNewTransactionWithMetaLeafFromWire fuzzes tx+meta leaf wire parsing.
// Wire format: [tx_data][32-byte key][0x04]
func FuzzNewTransactionWithMetaLeafFromWire(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{protocol.WireTypeTransactionWithMeta})

	// Minimum valid: 12 data + 32 key + wire type = 45 bytes
	minValid := make([]byte, 45)
	for i := 0; i < 12; i++ {
		minValid[i] = byte(i + 1)
	}
	for i := 12; i < 44; i++ {
		minValid[i] = 0x03
	}
	minValid[44] = protocol.WireTypeTransactionWithMeta
	f.Add(minValid)

	// Only key + wire type, no data (should fail — data < 12)
	noData := make([]byte, 33)
	for i := 0; i < 32; i++ {
		noData[i] = 0x01
	}
	noData[32] = protocol.WireTypeTransactionWithMeta
	f.Add(noData)

	f.Fuzz(func(t *testing.T, data []byte) {
		node, err := NewTransactionWithMetaLeafFromWire(data)
		if err != nil {
			return
		}
		_ = node.Hash()
		_ = node.Type()
		_ = node.Item()
		_ = node.Invariants(true)
	})
}

// FuzzDeserializeFromPrefix fuzzes the storage deserialization entry point.
// Dispatches based on 4-byte hash prefix to type-specific parsers.
func FuzzDeserializeFromPrefix(f *testing.F) {
	f.Add([]byte{})

	// Too short for prefix
	f.Add([]byte{0x01, 0x02, 0x03})

	// Unknown prefix
	f.Add([]byte{0xFF, 0xFF, 0xFF, 0xFF, 0x00})

	// Valid inner node prefix: 4 prefix + 512 child hashes = 516 bytes
	innerPrefix := make([]byte, 516)
	copy(innerPrefix[:4], protocol.HashPrefixInnerNode[:])
	f.Add(innerPrefix)

	// Inner node prefix with one non-zero hash
	innerWithHash := make([]byte, 516)
	copy(innerWithHash[:4], protocol.HashPrefixInnerNode[:])
	for i := 4; i < 36; i++ {
		innerWithHash[i] = 0xEE
	}
	f.Add(innerWithHash)

	// Valid account state prefix: 4 prefix + 12 data + 32 non-zero key = 48 bytes
	acctPrefix := make([]byte, 48)
	copy(acctPrefix[:4], protocol.HashPrefixLeafNode[:])
	for i := 4; i < 16; i++ {
		acctPrefix[i] = byte(i)
	}
	for i := 16; i < 48; i++ {
		acctPrefix[i] = 0x01
	}
	f.Add(acctPrefix)

	// Valid transaction prefix: 4 prefix + 12 data = 16 bytes
	txPrefix := make([]byte, 16)
	copy(txPrefix[:4], protocol.HashPrefixTransactionID[:])
	for i := 4; i < 16; i++ {
		txPrefix[i] = byte(i)
	}
	f.Add(txPrefix)

	// Valid tx+meta prefix: 4 prefix + 12 data + 32 key = 48 bytes
	txMetaPrefix := make([]byte, 48)
	copy(txMetaPrefix[:4], protocol.HashPrefixTxNode[:])
	for i := 4; i < 16; i++ {
		txMetaPrefix[i] = byte(i)
	}
	for i := 16; i < 48; i++ {
		txMetaPrefix[i] = 0x02
	}
	f.Add(txMetaPrefix)

	// Wrong size for inner node prefix (too small)
	innerBadSize := make([]byte, 100)
	copy(innerBadSize[:4], protocol.HashPrefixInnerNode[:])
	f.Add(innerBadSize)

	f.Fuzz(func(t *testing.T, data []byte) {
		node, err := DeserializeFromPrefix(data)
		if err != nil {
			return
		}
		_ = node.Hash()
		_ = node.Type()
		_ = node.IsLeaf()
		_ = node.IsInner()
		_ = node.Invariants(true)
	})
}
