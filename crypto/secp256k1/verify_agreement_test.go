package secp256k1

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/LeJamon/go-xrpl/crypto/sha512half"

	"github.com/LeJamon/go-xrpl/crypto/secp256k1/shim"
	"github.com/stretchr/testify/require"
)

func TestVerifyDigestRaw_GoldenVectors(t *testing.T) {
	t.Parallel()

	const (
		msg      = "Hello World"
		pubHex   = "02950F4710101A25073BF37086D73FBBD00C7A6B0F91097D8F0BC6D268C400D56E"
		otherPub = "031FBCFDD2EC6C2EDFBBA3866BDBAC28E5253C6A01FE9EFF8CAAE01871F009E837"
		lowSDER  = "3045022100E1617F1A3C85B5BC8FA6224F893FE9068BEA8F8D075EE144F6F9D255C829761802206FD9B361CDE83A0C3D5654232F1D7CFB1A614E9A8F9B1A861564029065516E64"
	)

	pub := mustDecodeHex(t, pubHex)
	wrongPub := mustDecodeHex(t, otherPub)
	lowS := mustDecodeHex(t, lowSDER)
	highS := mustDecodeHex(t, flipSToHighS(t, lowSDER))
	uncompressed, ok := shim.ParsePublicKey(pub, false)
	require.True(t, ok)
	invalidPrefix := append([]byte(nil), pub...)
	invalidPrefix[0] = 0x04
	invalidPoint := append([]byte{0x02}, make([]byte, 32)...)
	for i := 1; i < len(invalidPoint); i++ {
		invalidPoint[i] = 0xFF
	}

	digest := sha512half.Sum([]byte(msg))
	wrongDigest := sha512half.Sum([]byte("Goodbye World"))

	cases := []struct {
		name   string
		digest []byte
		pubkey []byte
		sig    []byte
		want   bool
	}{
		{"low-S valid", digest[:], pub, lowS, true},
		{"high-S valid (relaxed)", digest[:], pub, highS, true},
		{"valid sig, wrong digest", wrongDigest[:], pub, lowS, false},
		{"valid sig, wrong key", digest[:], wrongPub, lowS, false},
		{"malformed DER", digest[:], pub, []byte{0x30, 0x00}, false},
		{"garbage sig", digest[:], pub, []byte("not a der signature"), false},
		{"empty sig", digest[:], pub, nil, false},
		{"invalid digest length", digest[:len(digest)-1], pub, lowS, false},
		{"uncompressed key", digest[:], uncompressed, lowS, false},
		{"invalid key prefix", digest[:], invalidPrefix, lowS, false},
		{"invalid key length", digest[:], pub[:len(pub)-1], lowS, false},
		{"invalid key point", digest[:], invalidPoint, lowS, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, verifyDigestRaw(tc.digest, tc.pubkey, tc.sig))
		})
	}
}

func TestVerifyDigestRaw_WycheproofValidCorpus(t *testing.T) {
	vectors := loadWycheproofTestVectors(t)

	checked := 0
	for _, group := range vectors.TestGroups {
		pub := parsePublicKey(t, group.PublicKey.Wx, group.PublicKey.Wy)
		for _, tc := range group.Tests {
			if tc.Result != "valid" {
				continue
			}
			msg, err := hex.DecodeString(tc.Msg)
			require.NoError(t, err, "tcId %d: decode msg", tc.TcId)
			sig, err := hex.DecodeString(tc.Sig)
			require.NoError(t, err, "tcId %d: decode sig", tc.TcId)

			digest := sha256.Sum256(msg)
			require.Truef(t, verifyDigestRaw(digest[:], pub, sig),
				"tcId %d (%s): valid signature must verify on the active backend",
				tc.TcId, tc.Comment)
			checked++
		}
	}
	require.Positive(t, checked, "expected at least one valid Wycheproof vector")
	t.Logf("verified %d Wycheproof valid vectors through verifyDigestRaw", checked)
}

func mustDecodeHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	require.NoError(t, err)
	return b
}
