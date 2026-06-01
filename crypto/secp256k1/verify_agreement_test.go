package secp256k1

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/LeJamon/go-xrpl/crypto/common"

	"github.com/stretchr/testify/require"
)

// These tests call verifyDigestRaw directly — the build-tagged seam where
// the cgo (libsecp256k1) and purego (decred) backends diverge. Each binary
// compiles exactly one backend, so the cross-backend agreement claim is
// proven by running this suite under both CGO_ENABLED=1 (CI "libs" group)
// and CGO_ENABLED=0 (CI "libs-purego" job): both must produce the accept/
// reject verdicts asserted here for every vector.

// TestVerifyDigestRaw_GoldenVectors pins the low-level relaxed-verify
// contract that verifyDigestRaw exposes (canonicality gating happens in the
// caller, so high-S must verify here). The high-S case is the one the shim
// was written to normalize for — the most likely place for the two backends
// to drift apart.
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

	digest := common.Sha512Half([]byte(msg))
	wrongDigest := common.Sha512Half([]byte("Goodbye World"))

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
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, verifyDigestRaw(tc.digest, tc.pubkey, tc.sig))
		})
	}
}

// TestVerifyDigestRaw_WycheproofValidCorpus routes every Wycheproof vector
// the corpus marks "valid" through the active backend. A fully-canonical,
// in-range, well-formed signature must verify regardless of which backend is
// compiled — so both must accept the entire valid corpus. This is the gap
// the existing Wycheproof tests leave open: they call decred directly and so
// never exercise verifyDigestRaw / the cgo path.
func TestVerifyDigestRaw_WycheproofValidCorpus(t *testing.T) {
	vectors := loadWycheproofTestVectors(t)

	checked := 0
	for _, group := range vectors.TestGroups {
		pub := parsePublicKey(t, group.PublicKey.Wx, group.PublicKey.Wy).SerializeCompressed()
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
