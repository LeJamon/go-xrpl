//go:build cgo

package secp256k1

import "github.com/LeJamon/goXRPLd/crypto/secp256k1/shim"

// verifyDigestRaw assumes the caller has already rejected CanonicityNone.
// High-S signatures are normalized inside the shim so cgo and purego
// backends agree on accept/reject.
func verifyDigestRaw(hash32, pubkey, sigDER []byte) bool {
	return shim.VerifyDigest(hash32, pubkey, sigDER)
}
