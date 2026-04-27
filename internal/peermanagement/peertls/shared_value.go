package peertls

import (
	"crypto/sha512"
	"errors"
)

// computeSharedValue mirrors rippled's makeSharedValue:
//
//	sha512Half(sha512(local) XOR sha512(peer))
//
// where sha512Half is the first 32 bytes of SHA-512.
func computeSharedValue(local, peer []byte) ([]byte, error) {
	if len(local) < 12 || len(peer) < 12 {
		return nil, errors.New("peertls: Finished message shorter than 12 bytes")
	}

	h1 := sha512.Sum512(local)
	h2 := sha512.Sum512(peer)

	var xor [64]byte
	allZero := true
	for i := 0; i < 64; i++ {
		xor[i] = h1[i] ^ h2[i]
		if xor[i] != 0 {
			allZero = false
		}
	}
	if allZero {
		return nil, errors.New("peertls: identical local and peer Finished")
	}

	final := sha512.Sum512(xor[:])
	out := make([]byte, 32)
	copy(out, final[:32])
	return out, nil
}
