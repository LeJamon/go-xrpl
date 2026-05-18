//go:build cgo

package shim

import "crypto/sha512"

// Local mirror of common.Sha512Half so the shim has no non-stdlib deps.
func sha512HalfBytes(b []byte) [32]byte {
	sum := sha512.Sum512(b)
	var out [32]byte
	copy(out[:], sum[:32])
	return out
}
