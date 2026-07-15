package sha512half

import (
	"crypto/sha512"
	"hash"
	"sync"
)

// sha512Pool reuses sha512.Hash instances across calls. Sum is on the
// hash path of nearly every ledger and consensus operation, so amortising the
// ~180-byte hasher allocation matters.
var sha512Pool = sync.Pool{
	New: func() any {
		return sha512.New()
	},
}

// Acquire returns a reset sha512.Hash from the pool. The caller must
// call Release when done. The hasher is not safe for concurrent use.
func Acquire() hash.Hash {
	h := sha512Pool.Get().(hash.Hash)
	h.Reset()
	return h
}

// Release returns a hasher obtained from Acquire to the pool. The
// hasher must not be used after it is released.
func Release(h hash.Hash) {
	sha512Pool.Put(h)
}

// Sum returns the first 32 bytes of the SHA-512 hash of the
// concatenated argument slices.
func Sum(args ...[]byte) [32]byte {
	hasher := Acquire()
	defer Release(hasher)
	for _, arg := range args {
		hasher.Write(arg)
	}
	var buf [sha512.Size]byte
	sum := hasher.Sum(buf[:0])
	var out [32]byte
	copy(out[:], sum[:32])
	return out
}
