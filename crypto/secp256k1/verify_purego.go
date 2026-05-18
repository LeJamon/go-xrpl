//go:build !cgo

package secp256k1

import (
	rootcrypto "github.com/LeJamon/goXRPLd/crypto"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	ecdsa "github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
)

// verifyDigestRaw verifies a DER-encoded ECDSA signature against a
// 32-byte hash and a SEC1-encoded public key using the pure-Go decred
// implementation. Used when CGO is disabled.
func verifyDigestRaw(hash32, pubkey, sigDER []byte) bool {
	r, s, err := rootcrypto.DERSigToRS(sigDER)
	if err != nil {
		return false
	}
	if len(r) > 32 || len(s) > 32 {
		return false
	}
	var rBytes, sBytes [32]byte
	copy(rBytes[32-len(r):], r)
	copy(sBytes[32-len(s):], s)
	ecdsaR := &secp256k1.ModNScalar{}
	ecdsaS := &secp256k1.ModNScalar{}
	ecdsaR.SetBytes(&rBytes)
	ecdsaS.SetBytes(&sBytes)
	parsedSig := ecdsa.NewSignature(ecdsaR, ecdsaS)
	pubKey, err := secp256k1.ParsePubKey(pubkey)
	if err != nil {
		return false
	}
	return parsedSig.Verify(hash32, pubKey)
}
