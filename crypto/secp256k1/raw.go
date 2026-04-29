package secp256k1

import (
	"errors"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	ecdsa "github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
)

// SignDigestBytes signs a pre-hashed 32-byte digest. Result is DER-encoded.
func SignDigestBytes(digest, privKey []byte) ([]byte, error) {
	if len(digest) != 32 {
		return nil, errors.New("secp256k1: digest must be 32 bytes")
	}
	if len(privKey) != 32 {
		return nil, ErrInvalidPrivateKey
	}
	sk := secp256k1.PrivKeyFromBytes(privKey)
	return ecdsa.Sign(sk, digest).Serialize(), nil
}

// VerifyDigestBytes verifies a DER-encoded signature against a 32-byte digest.
// Matches rippled's verifyDigest(..., mustBeFullyCanonical=false).
func VerifyDigestBytes(digest, pubKey, sig []byte) bool {
	if len(digest) != 32 {
		return false
	}
	var d [32]byte
	copy(d[:], digest)
	return SECP256K1().ValidateDigest(d, pubKey, sig)
}
