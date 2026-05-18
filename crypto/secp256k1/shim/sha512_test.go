//go:build cgo

package shim

import "crypto/sha512"

// sha512HalfBytes mirrors crypto/common.Sha512Half locally so the test
// file (which lives in the shim package) doesn't need to import
// crypto/common — keeping the shim's dependency surface minimal.
func sha512HalfBytes(b []byte) [32]byte {
	sum := sha512.Sum512(b)
	var out [32]byte
	copy(out[:], sum[:32])
	return out
}
