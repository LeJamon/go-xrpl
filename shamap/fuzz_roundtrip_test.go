package shamap

import (
	"bytes"
	"testing"
)

// FuzzAccountStateLeafRoundTrip verifies that AccountStateLeafNode survives
// wire and prefix serialization round-trips with identical hashes and data.
func FuzzAccountStateLeafRoundTrip(f *testing.F) {
	key32 := make([]byte, 32)
	for i := range key32 {
		key32[i] = 0x01
	}
	data12 := make([]byte, 12)
	for i := range data12 {
		data12[i] = 0x02
	}
	f.Add(key32, data12)

	// Larger data
	data64 := make([]byte, 64)
	for i := range data64 {
		data64[i] = byte(i)
	}
	f.Add(key32, data64)

	f.Fuzz(func(t *testing.T, keyBytes []byte, data []byte) {
		if len(keyBytes) < 32 || len(data) < 12 {
			return
		}

		var key [32]byte
		copy(key[:], keyBytes[:32])
		if key == [32]byte{} {
			return
		}

		item := NewItem(key, data)
		node, err := newAccountStateLeafNode(item)
		if err != nil {
			return
		}
		origHash := node.Hash()

		// Wire round-trip
		wireData, err := node.SerializeForWire()
		if err != nil {
			t.Fatalf("SerializeForWire failed: %v", err)
		}
		node2, err := newAccountStateLeafFromWire(wireData)
		if err != nil {
			t.Fatalf("newAccountStateLeafFromWire failed: %v", err)
		}
		if origHash != node2.Hash() {
			t.Fatal("wire round-trip: hash mismatch")
		}
		if !bytes.Equal(node.Item().Data(), node2.Item().Data()) {
			t.Fatal("wire round-trip: data mismatch")
		}
		if node.Item().Key() != node2.Item().Key() {
			t.Fatal("wire round-trip: key mismatch")
		}

		// Prefix round-trip
		prefixData, err := node.SerializeWithPrefix()
		if err != nil {
			t.Fatalf("SerializeWithPrefix failed: %v", err)
		}
		node3, err := DeserializeFromPrefix(prefixData)
		if err != nil {
			t.Fatalf("DeserializeFromPrefix failed: %v", err)
		}
		if origHash != node3.Hash() {
			t.Fatal("prefix round-trip: hash mismatch")
		}
	})
}

// FuzzTransactionLeafRoundTrip verifies TransactionLeafNode round-trips.
// Transaction leaf keys are derived by hashing, so only data is fuzzed.
func FuzzTransactionLeafRoundTrip(f *testing.F) {
	data12 := make([]byte, 12)
	for i := range data12 {
		data12[i] = byte(i + 1)
	}
	f.Add(data12)

	data128 := make([]byte, 128)
	for i := range data128 {
		data128[i] = byte(i)
	}
	f.Add(data128)

	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) < 12 {
			return
		}

		// Key is derived from hash, so we need a placeholder
		var key [32]byte
		for i := range key {
			key[i] = 0xFF
		}

		item := NewItem(key, data)
		node, err := newTransactionLeafNode(item)
		if err != nil {
			return
		}
		origHash := node.Hash()

		// Wire round-trip
		wireData, err := node.SerializeForWire()
		if err != nil {
			t.Fatalf("SerializeForWire failed: %v", err)
		}
		node2, err := NewTransactionLeafFromWire(wireData)
		if err != nil {
			t.Fatalf("NewTransactionLeafFromWire failed: %v", err)
		}
		// Note: wire deserialization re-derives the key from the hash,
		// so the hash of the deserialized node should match the original
		// only if the key was correctly derived. The wire format drops
		// the key and recomputes it, so we compare data instead.
		if !bytes.Equal(node.Item().Data(), node2.Item().Data()) {
			t.Fatal("wire round-trip: data mismatch")
		}

		// Prefix round-trip
		prefixData, err := node.SerializeWithPrefix()
		if err != nil {
			t.Fatalf("SerializeWithPrefix failed: %v", err)
		}
		node3, err := DeserializeFromPrefix(prefixData)
		if err != nil {
			t.Fatalf("DeserializeFromPrefix failed: %v", err)
		}
		if origHash != node3.Hash() {
			t.Fatal("prefix round-trip: hash mismatch")
		}
	})
}

// FuzzTransactionWithMetaLeafRoundTrip verifies TransactionWithMetaLeafNode round-trips.
func FuzzTransactionWithMetaLeafRoundTrip(f *testing.F) {
	key32 := make([]byte, 32)
	for i := range key32 {
		key32[i] = 0x03
	}
	data12 := make([]byte, 12)
	for i := range data12 {
		data12[i] = 0x04
	}
	f.Add(key32, data12)

	f.Fuzz(func(t *testing.T, keyBytes []byte, data []byte) {
		if len(keyBytes) < 32 || len(data) < 12 {
			return
		}

		var key [32]byte
		copy(key[:], keyBytes[:32])
		if key == [32]byte{} {
			return
		}

		item := NewItem(key, data)
		node, err := newTransactionWithMetaLeafNode(item)
		if err != nil {
			return
		}
		origHash := node.Hash()

		// Wire round-trip
		wireData, err := node.SerializeForWire()
		if err != nil {
			t.Fatalf("SerializeForWire failed: %v", err)
		}
		node2, err := newTransactionWithMetaLeafFromWire(wireData)
		if err != nil {
			t.Fatalf("newTransactionWithMetaLeafFromWire failed: %v", err)
		}
		if origHash != node2.Hash() {
			t.Fatal("wire round-trip: hash mismatch")
		}
		if !bytes.Equal(node.Item().Data(), node2.Item().Data()) {
			t.Fatal("wire round-trip: data mismatch")
		}
		if node.Item().Key() != node2.Item().Key() {
			t.Fatal("wire round-trip: key mismatch")
		}

		// Prefix round-trip
		prefixData, err := node.SerializeWithPrefix()
		if err != nil {
			t.Fatalf("SerializeWithPrefix failed: %v", err)
		}
		node3, err := DeserializeFromPrefix(prefixData)
		if err != nil {
			t.Fatalf("DeserializeFromPrefix failed: %v", err)
		}
		if origHash != node3.Hash() {
			t.Fatal("prefix round-trip: hash mismatch")
		}
	})
}

// FuzzInnerNodeRoundTrip verifies innerNode wire and prefix round-trips.
// Constructs an innerNode from a branch mask and hash data, then round-trips it.
func FuzzInnerNodeRoundTrip(f *testing.F) {
	// One branch set (branch 0), 32 bytes of hash
	hash32 := make([]byte, 32)
	for i := range hash32 {
		hash32[i] = 0xAA
	}
	f.Add(uint16(0x0001), hash32)

	// All branches set, 512 bytes of hashes
	hash512 := make([]byte, 512)
	for i := range hash512 {
		hash512[i] = byte(i % 256)
	}
	f.Add(uint16(0xFFFF), hash512)

	// Two branches (0 and 1), 64 bytes
	hash64 := make([]byte, 64)
	for i := range hash64 {
		hash64[i] = byte(i + 1)
	}
	f.Add(uint16(0x0003), hash64)

	f.Fuzz(func(t *testing.T, branchMask uint16, hashData []byte) {
		if branchMask == 0 {
			return // empty inner node — nothing to round-trip
		}

		node := newInnerNode()

		// Set hashes for each set bit in branchMask
		hashIdx := 0
		for i := range BranchFactor {
			if branchMask&(1<<i) == 0 {
				continue
			}
			var h [32]byte
			// Fill hash from hashData, cycling if necessary
			for j := range 32 {
				if len(hashData) > 0 {
					h[j] = hashData[hashIdx%len(hashData)]
					hashIdx++
				} else {
					h[j] = byte(i + 1) // fallback
				}
			}
			// Skip zero hashes — they represent empty branches
			if isZeroHash(h) {
				continue
			}
			node.hashes[i] = h
			node.isBranch |= 1 << i
		}

		if node.isBranch == 0 {
			return // all hashes ended up zero
		}

		if err := node.UpdateHash(); err != nil {
			t.Fatalf("UpdateHash failed: %v", err)
		}
		origHash := node.Hash()

		// Wire round-trip
		wireData, err := node.SerializeForWire()
		if err != nil {
			t.Fatalf("SerializeForWire failed: %v", err)
		}
		node2, err := newInnerNodeFromWire(wireData)
		if err != nil {
			t.Fatalf("newInnerNodeFromWire failed: %v", err)
		}
		if origHash != node2.Hash() {
			t.Fatal("wire round-trip: hash mismatch")
		}

		// Prefix round-trip
		prefixData, err := node.SerializeWithPrefix()
		if err != nil {
			t.Fatalf("SerializeWithPrefix failed: %v", err)
		}
		node3, err := DeserializeFromPrefix(prefixData)
		if err != nil {
			t.Fatalf("DeserializeFromPrefix failed: %v", err)
		}
		if origHash != node3.Hash() {
			t.Fatal("prefix round-trip: hash mismatch")
		}
	})
}
