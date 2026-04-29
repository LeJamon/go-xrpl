package secp256k1

import (
	"errors"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	ecdsa "github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
)

// SignDigestBytes signs a pre-hashed 32-byte digest with raw private key
// bytes. The result is DER-encoded. No re-hashing — mirrors rippled's
// signDigest() which feeds the hash directly to secp256k1.
//
// Use this from contexts that already hold raw key/digest bytes (e.g.
// the peer handshake's session-signature path) to avoid hex round-trips.
func SignDigestBytes(digest, privKey []byte) ([]byte, error) {
	if len(digest) != 32 {
		return nil, errors.New("secp256k1: digest must be 32 bytes")
	}
	if len(privKey) != 32 {
		return nil, ErrInvalidPrivateKey
	}
	sk := secp256k1.PrivKeyFromBytes(privKey)
	if sk == nil {
		return nil, ErrInvalidPrivateKey
	}
	return ecdsa.Sign(sk, digest).Serialize(), nil
}

// VerifyDigestBytes verifies a DER-encoded signature against a 32-byte
// digest with a compressed-form public key. Mirrors rippled's
// verifyDigest(..., mustBeFullyCanonical=false): rejects only signatures
// that fail the minimum canonicality screen, accepts non-low-S forms.
//
// Returns false on any parse or verification failure.
func VerifyDigestBytes(digest, pubKey, sig []byte) bool {
	if len(digest) != 32 {
		return false
	}
	var d [32]byte
	copy(d[:], digest)
	return SECP256K1().ValidateDigest(d, pubKey, sig)
}
