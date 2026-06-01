package secp256k1

import (
	"encoding/hex"
	"math/big"
	"strings"
	"testing"

	rootcrypto "github.com/LeJamon/go-xrpl/crypto"

	"github.com/stretchr/testify/require"
)

// TestValidateWithCanonicality_HighS locks in the relaxed-verify
// contract: with mustBeFullyCanonical=false, a high-S signature must
// verify. Both backends must agree:
//   - cgo: shim normalizes high-S to low-S before secp256k1_ecdsa_verify
//   - !cgo: decred's Verify accepts arbitrary-S
//
// The manifest path itself runs strict (mustBeFullyCanonical=true) per
// rippled PublicKey.h:256 — this test only guards the low-level relaxed
// branch.
func TestValidateWithCanonicality_HighS(t *testing.T) {
	t.Parallel()

	const (
		msg     = "Hello World"
		pubHex  = "02950F4710101A25073BF37086D73FBBD00C7A6B0F91097D8F0BC6D268C400D56E"
		lowSDER = "3045022100E1617F1A3C85B5BC8FA6224F893FE9068BEA8F8D075EE144F6F9D255C829761802206FD9B361CDE83A0C3D5654232F1D7CFB1A614E9A8F9B1A861564029065516E64"
	)

	algo := SECP256K1()

	require.True(t, algo.Validate(msg, pubHex, lowSDER),
		"baseline: low-S DER must verify under strict (mustBeFullyCanonical=true)")

	highSDER := flipSToHighS(t, lowSDER)

	lowSBytes, err := hex.DecodeString(lowSDER)
	require.NoError(t, err)
	highSBytes, err := hex.DecodeString(highSDER)
	require.NoError(t, err)
	require.Equal(t, rootcrypto.CanonicityFullyCanonical, rootcrypto.ECDSACanonicality(lowSBytes))
	require.Equal(t, rootcrypto.CanonicityCanonical, rootcrypto.ECDSACanonicality(highSBytes))

	require.False(t, algo.Validate(msg, pubHex, highSDER),
		"high-S DER must be rejected under strict (mustBeFullyCanonical=true)")

	require.True(t, algo.ValidateWithCanonicality(msg, pubHex, highSDER, false),
		"high-S DER must verify under mustBeFullyCanonical=false")
}

// flipSToHighS rewrites a DER ECDSA signature so its s value becomes
// N-s, converting low-S → high-S (or vice versa). Mathematically
// equivalent for verification.
func flipSToHighS(t *testing.T, sigHex string) string {
	t.Helper()
	sigBytes, err := hex.DecodeString(sigHex)
	require.NoError(t, err)
	r, s, err := rootcrypto.DERSigToRS(sigBytes)
	require.NoError(t, err)
	sBig := new(big.Int).SetBytes(s)
	flipped := new(big.Int).Sub(curveOrderN, sBig)
	return strings.ToUpper(hex.EncodeToString(
		rootcrypto.EncodeDERSignature(new(big.Int).SetBytes(r), flipped),
	))
}
