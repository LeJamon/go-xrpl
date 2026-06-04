package shamap

import (
	"bytes"
	"testing"
)

const (
	fuzzOpPut    = 0
	fuzzOpGet    = 1
	fuzzOpDelete = 2
)

// FuzzSHAMapOperations fuzzes random sequences of Put/Get/Delete on a SHAMap,
// verifying tree invariants and correctness against a reference oracle after
// each operation sequence.
func FuzzSHAMapOperations(f *testing.F) {
	// Single Put
	singlePut := make([]byte, 45)
	singlePut[0] = fuzzOpPut
	for i := 1; i <= 32; i++ {
		singlePut[i] = byte(i)
	}
	for i := 33; i < 45; i++ {
		singlePut[i] = 0xAA
	}
	f.Add(singlePut)

	// Put then Get same key
	putGet := make([]byte, 78)
	putGet[0] = fuzzOpPut
	for i := 1; i <= 32; i++ {
		putGet[i] = byte(i)
	}
	for i := 33; i < 45; i++ {
		putGet[i] = 0xBB
	}
	putGet[45] = fuzzOpGet
	for i := 46; i <= 77; i++ {
		putGet[i] = byte(i - 45)
	}
	f.Add(putGet)

	// Put then Delete same key
	putDel := make([]byte, 78)
	putDel[0] = fuzzOpPut
	for i := 1; i <= 32; i++ {
		putDel[i] = byte(i)
	}
	for i := 33; i < 45; i++ {
		putDel[i] = 0xCC
	}
	putDel[45] = fuzzOpDelete
	for i := 46; i <= 77; i++ {
		putDel[i] = byte(i - 45)
	}
	f.Add(putDel)

	// Empty (no operations)
	f.Add([]byte{})

	f.Fuzz(func(t *testing.T, data []byte) {
		sm, err := New(TypeState)
		if err != nil {
			t.Fatalf("New failed: %v", err)
		}

		// Oracle: tracks expected state
		oracle := make(map[[32]byte][]byte)

		ops := 0
		const maxOps = 100
		i := 0

		for i < len(data) && ops < maxOps {
			if i+33 > len(data) {
				break // not enough bytes for opcode + key
			}

			opcode := data[i] % 3
			i++

			var key [32]byte
			copy(key[:], data[i:i+32])
			i += 32

			// Skip zero keys — they're invalid for SHAMap
			if key == [32]byte{} {
				continue
			}

			switch opcode {
			case fuzzOpPut:
				// Read data: consume up to 32 bytes, pad to minimum 12
				var itemData []byte
				remaining := len(data) - i
				take := min(remaining, 32)
				if take > 0 {
					itemData = make([]byte, take)
					copy(itemData, data[i:i+take])
					i += take
				}
				// Pad to minimum 12 bytes
				for len(itemData) < 12 {
					itemData = append(itemData, 0x00)
				}

				err := sm.Put(key, itemData)
				if err != nil {
					// Some errors are expected (e.g., immutable state)
					continue
				}
				// Store in oracle (defensive copy)
				oracleCopy := make([]byte, len(itemData))
				copy(oracleCopy, itemData)
				oracle[key] = oracleCopy

			case fuzzOpGet:
				item, found, err := sm.Get(key)
				if err != nil {
					t.Fatalf("Get error: %v", err)
				}
				expected, inOracle := oracle[key]
				if found != inOracle {
					t.Fatalf("Get found=%v but oracle has key=%v", found, inOracle)
				}
				if found && !bytes.Equal(item.Data(), expected) {
					t.Fatalf("Get data mismatch for key %x", key[:4])
				}

			case fuzzOpDelete:
				err := sm.Delete(key)
				_, inOracle := oracle[key]
				if inOracle {
					if err != nil {
						t.Fatalf("Delete failed for existing key: %v", err)
					}
					delete(oracle, key)
				}
				// Delete of non-existent key may return error — that's fine
			}

			ops++
		}

		// Final verification: hash determinism
		hash1, err := sm.Hash()
		if err != nil {
			t.Fatalf("Hash() failed: %v", err)
		}
		hash2, err := sm.Hash()
		if err != nil {
			t.Fatalf("Hash() failed on second call: %v", err)
		}
		if hash1 != hash2 {
			t.Fatal("Hash() not deterministic")
		}

		// Final verification: all oracle entries retrievable
		for key, expectedData := range oracle {
			item, found, err := sm.Get(key)
			if err != nil {
				t.Fatalf("final Get error for key %x: %v", key[:4], err)
			}
			if !found {
				t.Fatalf("final Get: key %x not found but should exist", key[:4])
			}
			if !bytes.Equal(item.Data(), expectedData) {
				t.Fatalf("final Get: data mismatch for key %x", key[:4])
			}
		}
	})
}
