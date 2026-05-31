//go:build !cgo

package secp256k1

import (
	rootcrypto "github.com/LeJamon/go-xrpl/crypto"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	ecdsa "github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
)

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
