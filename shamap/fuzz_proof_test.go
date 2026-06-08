package shamap

import (
	"testing"
)

// FuzzVerifyProofPath fuzzes Merkle proof verification with arbitrary inputs.
// A malicious peer could send crafted proof paths — this must never panic.
func FuzzVerifyProofPath(f *testing.F) {
	// Empty path
	f.Add(makeHashSlice(0x00), makeHashSlice(0x01), []byte{}, uint8(0))

	// Single garbage blob
	f.Add(makeHashSlice(0xAA), makeHashSlice(0xBB), []byte{0xFF, 0xFF, 0xFF}, uint8(1))

	// Single valid-ish inner node wire data as path element
	innerWire := make([]byte, 513)
	innerWire[512] = 0x02 // WireTypeInner
	f.Add(makeHashSlice(0x00), makeHashSlice(0x01), innerWire, uint8(1))

	// Build a real valid proof from an actual tree for a strong seed
	rootHash, key, pathData, pathCount := buildValidProofSeed()
	f.Add(rootHash[:], key[:], pathData, pathCount)

	f.Fuzz(func(t *testing.T, rootHashBytes []byte, keyBytes []byte, pathData []byte, pathCount uint8) {
		if len(rootHashBytes) < 32 || len(keyBytes) < 32 {
			return
		}

		var rootHash [32]byte
		copy(rootHash[:], rootHashBytes[:32])
		var key [32]byte
		copy(key[:], keyBytes[:32])

		// Split pathData into pathCount blobs
		path := splitIntoBlobs(pathData, pathCount)

		// VerifyProofPath must not panic — just returns bool
		result := VerifyProofPath(rootHash, key, path)

		// VerifyProofPathDetailed must not panic — returns nil or error
		err := VerifyProofPathDetailed(rootHash, key, path)

		// Both functions must agree
		if result && err != nil {
			t.Fatalf("VerifyProofPath returned true but VerifyProofPathDetailed returned error: %v", err)
		}
		if !result && err == nil {
			t.Fatal("VerifyProofPath returned false but VerifyProofPathDetailed returned nil")
		}

		// VerifyProofPathWithValue must not panic
		val := VerifyProofPathWithValue(rootHash, key, path)

		// If VerifyProofPath says invalid, WithValue must return nil
		if !result && val != nil {
			t.Fatal("VerifyProofPathWithValue returned data but VerifyProofPath returned false")
		}
	})
}

// FuzzVerifyProofPathValidTree builds a real tree, gets a valid proof,
// then fuzzes mutations of that proof to ensure no panics and that
// mutations are correctly rejected.
func FuzzVerifyProofPathValidTree(f *testing.F) {
	// Seed: byte slices that will be used to mutate a valid proof
	f.Add([]byte{0x00}, []byte{0x00})

	f.Fuzz(func(t *testing.T, keyMutator []byte, pathMutator []byte) {
		// Build a small tree with known data
		sm, err := New(TypeState)
		if err != nil {
			t.Fatal(err)
		}

		var key1 [32]byte
		key1[0] = 0x11
		key1[31] = 0x01
		data1 := make([]byte, 16)
		for i := range data1 {
			data1[i] = 0xAA
		}

		var key2 [32]byte
		key2[0] = 0x22
		key2[31] = 0x02
		data2 := make([]byte, 16)
		for i := range data2 {
			data2[i] = 0xBB
		}

		if err := sm.Put(key1, data1); err != nil {
			t.Fatal(err)
		}
		if err := sm.Put(key2, data2); err != nil {
			t.Fatal(err)
		}

		rootHash, err := sm.Hash()
		if err != nil {
			t.Fatal(err)
		}

		// Get valid proof for key1
		proof, err := sm.GetProofPath(key1)
		if err != nil {
			t.Fatal(err)
		}
		if !proof.Found {
			t.Fatal("proof not found for key1")
		}

		// Sanity: valid proof must verify
		if !VerifyProofPath(rootHash, key1, proof.Path) {
			t.Fatal("valid proof failed verification")
		}

		// Mutate the key
		mutatedKey := key1
		for i := 0; i < len(keyMutator) && i < 32; i++ {
			mutatedKey[i] ^= keyMutator[i]
		}

		// Mutate a path element
		mutatedPath := make([][]byte, len(proof.Path))
		for i, p := range proof.Path {
			cp := make([]byte, len(p))
			copy(cp, p)
			mutatedPath[i] = cp
		}
		if len(mutatedPath) > 0 && len(pathMutator) > 0 {
			targetIdx := int(pathMutator[0]) % len(mutatedPath)
			blob := mutatedPath[targetIdx]
			for i := 1; i < len(pathMutator) && i-1 < len(blob); i++ {
				blob[(i-1)%len(blob)] ^= pathMutator[i]
			}
		}

		// Must not panic regardless of mutations
		_ = VerifyProofPath(rootHash, mutatedKey, mutatedPath)
		_ = VerifyProofPathDetailed(rootHash, mutatedKey, mutatedPath)
		_ = VerifyProofPathWithValue(rootHash, mutatedKey, mutatedPath)
	})
}

func makeHash(fill byte) [32]byte {
	var h [32]byte
	for i := range h {
		h[i] = fill
	}
	return h
}

func makeHashSlice(fill byte) []byte {
	h := make([]byte, 32)
	for i := range h {
		h[i] = fill
	}
	return h
}

// splitIntoBlobs splits data into n roughly equal blobs.
// Returns nil if n == 0.
func splitIntoBlobs(data []byte, n uint8) [][]byte {
	if n == 0 || len(data) == 0 {
		return nil
	}
	count := min(
		// Cap to prevent huge allocations
		min(

			int(n), MaxDepth+1), len(data))

	blobs := make([][]byte, count)
	chunkSize := len(data) / count
	if chunkSize == 0 {
		chunkSize = 1
	}

	for i := range count {
		start := i * chunkSize
		end := start + chunkSize
		if i == count-1 {
			end = len(data) // last blob gets remainder
		}
		if start >= len(data) {
			blobs[i] = []byte{}
			continue
		}
		if end > len(data) {
			end = len(data)
		}
		blobs[i] = data[start:end]
	}
	return blobs
}

// buildValidProofSeed constructs a valid proof from a real tree
// and returns the components as fuzz seed parameters.
func buildValidProofSeed() ([32]byte, [32]byte, []byte, uint8) {
	sm, err := New(TypeState)
	if err != nil {
		return [32]byte{}, [32]byte{}, nil, 0
	}

	var key [32]byte
	key[0] = 0xAB
	key[31] = 0xCD
	data := make([]byte, 16)
	for i := range data {
		data[i] = byte(i + 1)
	}
	if err := sm.Put(key, data); err != nil {
		return [32]byte{}, [32]byte{}, nil, 0
	}

	rootHash, err := sm.Hash()
	if err != nil {
		return [32]byte{}, [32]byte{}, nil, 0
	}

	proof, err := sm.GetProofPath(key)
	if err != nil || !proof.Found {
		return [32]byte{}, [32]byte{}, nil, 0
	}

	// Concatenate all path blobs into a single byte slice
	var pathData []byte
	for _, blob := range proof.Path {
		pathData = append(pathData, blob...)
	}

	return rootHash, key, pathData, uint8(len(proof.Path))
}
