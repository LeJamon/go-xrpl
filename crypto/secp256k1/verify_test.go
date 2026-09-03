package secp256k1

import (
	"encoding/hex"
	"math/big"
	"strings"
	"testing"

	rootcrypto "github.com/LeJamon/go-xrpl/crypto"
	"github.com/LeJamon/go-xrpl/crypto/sha512half"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/stretchr/testify/require"
)

func TestVerificationPublicKeyEncoding(t *testing.T) {
	t.Parallel()

	const (
		msg     = "Hello World"
		pubHex  = "02950F4710101A25073BF37086D73FBBD00C7A6B0F91097D8F0BC6D268C400D56E"
		lowSDER = "3045022100E1617F1A3C85B5BC8FA6224F893FE9068BEA8F8D075EE144F6F9D255C829761802206FD9B361CDE83A0C3D5654232F1D7CFB1A614E9A8F9B1A861564029065516E64"
	)

	pub := mustDecodeHex(t, pubHex)
	sig := mustDecodeHex(t, lowSDER)
	parsedPub, err := btcec.ParsePubKey(pub)
	require.NoError(t, err)
	digest := sha512half.Sum([]byte(msg))
	algo := Algorithm{}

	verifiers := []struct {
		name   string
		verify func([]byte) bool
	}{
		{"Validate", func(key []byte) bool {
			return algo.Validate(msg, hex.EncodeToString(key), lowSDER)
		}},
		{"ValidateWithCanonicality", func(key []byte) bool {
			return algo.ValidateWithCanonicality(msg, hex.EncodeToString(key), lowSDER, false)
		}},
		{"ValidateBytes", func(key []byte) bool {
			return algo.ValidateBytes([]byte(msg), key, sig)
		}},
		{"ValidateDigest", func(key []byte) bool {
			return algo.ValidateDigest(digest, key, sig)
		}},
		{"ValidateDigestWithCanonicality", func(key []byte) bool {
			return algo.ValidateDigestWithCanonicality(digest, key, sig, false)
		}},
		{"VerifyDigestBytes", func(key []byte) bool {
			return VerifyDigestBytes(digest[:], key, sig)
		}},
	}

	invalidPrefix := append([]byte(nil), pub...)
	invalidPrefix[0] = 0x04
	invalidPoint := append([]byte{0x02}, make([]byte, 32)...)
	for i := 1; i < len(invalidPoint); i++ {
		invalidPoint[i] = 0xFF
	}
	keys := []struct {
		name string
		key  []byte
		want bool
	}{
		{"compressed", pub, true},
		{"uncompressed", parsedPub.SerializeUncompressed(), false},
		{"invalid prefix", invalidPrefix, false},
		{"invalid length", pub[:len(pub)-1], false},
		{"invalid point", invalidPoint, false},
	}

	for _, verifier := range verifiers {
		t.Run(verifier.name, func(t *testing.T) {
			t.Parallel()
			for _, key := range keys {
				t.Run(key.name, func(t *testing.T) {
					t.Parallel()
					require.Equal(t, key.want, verifier.verify(key.key))
				})
			}
		})
	}
}

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

	algo := Algorithm{}

	require.True(t, algo.Validate(msg, pubHex, lowSDER),
		"baseline: low-S DER must verify under strict (mustBeFullyCanonical=true)")

	highSDER := flipSToHighS(t, lowSDER)

	lowSBytes, err := hex.DecodeString(lowSDER)
	require.NoError(t, err)
	highSBytes, err := hex.DecodeString(highSDER)
	require.NoError(t, err)
	require.Equal(t, rootcrypto.CanonicalityFullyCanonical, rootcrypto.ECDSACanonicality(lowSBytes))
	require.Equal(t, rootcrypto.CanonicalityCanonical, rootcrypto.ECDSACanonicality(highSBytes))

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
