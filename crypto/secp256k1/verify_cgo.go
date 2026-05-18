//go:build cgo

package secp256k1

import "github.com/LeJamon/goXRPLd/crypto/secp256k1/shim"

// verifyDigestRaw verifies a DER-encoded ECDSA signature against a
// 32-byte hash and a SEC1-encoded public key using libsecp256k1.
// Callers must have already rejected CanonicityNone signatures; high-S
// (canonical-but-not-fully-canonical) signatures are normalized by the
// shim so behavior matches the pure-Go backend.
func verifyDigestRaw(hash32, pubkey, sigDER []byte) bool {
	return shim.VerifyDigest(hash32, pubkey, sigDER)
}
