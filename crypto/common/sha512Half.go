package common

import (
	"crypto/sha512"
	"hash"
	"sync"
)

// sha512Pool reuses sha512.Hash instances across calls. Sha512Half is on the
// hash path of nearly every ledger and consensus operation, so amortising the
// ~180-byte hasher allocation matters.
var sha512Pool = sync.Pool{
	New: func() any {
		return sha512.New()
	},
}

// AcquireSHA512 returns a reset sha512.Hash from the pool. The caller must
// call ReleaseSHA512 when done. The hasher is not safe for concurrent use.
func AcquireSHA512() hash.Hash {
	h := sha512Pool.Get().(hash.Hash)
	h.Reset()
	return h
}

// ReleaseSHA512 returns a sha512.Hash to the pool.
func ReleaseSHA512(h hash.Hash) {
	sha512Pool.Put(h)
}

// Sha512Half Returns the first 32 bytes of a sha512 hash of a byte[]
func Sha512Half(args ...[]byte) [32]byte {
	hasher := AcquireSHA512()
	for _, arg := range args {
		hasher.Write(arg)
	}
	var buf [sha512.Size]byte
	sum := hasher.Sum(buf[:0])
	ReleaseSHA512(hasher)
	var out [32]byte
	copy(out[:], sum[:32])
	return out
}
