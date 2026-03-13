package secp256k1

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"github.com/stretchr/testify/require"
)

// secp256k1 curve order n
var curveOrderN, _ = new(big.Int).SetString("FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFEBAAEDCE6AF48A03BBFD25E8CD0364141", 16)

// halfN is n/2, used for Bitcoin-style signature malleability check
var halfN = new(big.Int).Rsh(curveOrderN, 1)

// DER parsing errors
var (
	errInvalidDERSignature     = errors.New("invalid DER signature")
	errInvalidDERIntegerTag    = errors.New("invalid DER: expected integer tag")
	errInvalidDERNotEnoughData = errors.New("invalid DER: not enough data")
	errLeftoverBytes           = errors.New("invalid signature: leftover bytes after parsing")
)

// parseStrictDERSignature parses a DER-encoded ECDSA signature with strict validation.
// It returns r and s as byte slices, or an error if the encoding is invalid.
// This implements Bitcoin's strict DER signature checking (BIP 66).
func parseStrictDERSignature(sig []byte) ([]byte, []byte, error) {
	// Minimum DER signature: 30 06 02 01 XX 02 01 XX
	if len(sig) < 8 {
		return nil, nil, errInvalidDERSignature
	}

	// Maximum signature length (typically 72-73 bytes for secp256k1)
	if len(sig) > 73 {
		return nil, nil, errInvalidDERSignature
	}

	// Check sequence tag
	if sig[0] != 0x30 {
		return nil, nil, errInvalidDERSignature
	}

	// Check that the length byte covers the entire signature
	seqLen := int(sig[1])
	if seqLen != len(sig)-2 {
		return nil, nil, errInvalidDERSignature
	}

	// Parse r
	pos := 2
	if pos >= len(sig) || sig[pos] != 0x02 {
		return nil, nil, errInvalidDERIntegerTag
	}
	pos++

	if pos >= len(sig) {
		return nil, nil, errInvalidDERNotEnoughData
	}
	rLen := int(sig[pos])
	pos++

	// r length validation
	if rLen == 0 || pos+rLen > len(sig) {
		return nil, nil, errInvalidDERNotEnoughData
	}

	r := sig[pos : pos+rLen]
	pos += rLen

	// Strict DER: r must not have unnecessary leading zeros
	// A leading zero is only allowed if the next byte has the high bit set
	if len(r) > 1 && r[0] == 0x00 && (r[1]&0x80) == 0 {
		return nil, nil, errInvalidDERSignature
	}

	// Strict DER: r must not be negative (high bit of first byte must be 0, or have leading 0x00)
	if len(r) > 0 && (r[0]&0x80) != 0 {
		return nil, nil, errInvalidDERSignature
	}

	// Parse s
	if pos >= len(sig) || sig[pos] != 0x02 {
		return nil, nil, errInvalidDERIntegerTag
	}
	pos++

	if pos >= len(sig) {
		return nil, nil, errInvalidDERNotEnoughData
	}
	sLen := int(sig[pos])
	pos++

	// s length validation
	if sLen == 0 || pos+sLen > len(sig) {
		return nil, nil, errInvalidDERNotEnoughData
	}

	s := sig[pos : pos+sLen]
	pos += sLen

	// Strict DER: s must not have unnecessary leading zeros
	if len(s) > 1 && s[0] == 0x00 && (s[1]&0x80) == 0 {
		return nil, nil, errInvalidDERSignature
	}

	// Strict DER: s must not be negative
	if len(s) > 0 && (s[0]&0x80) != 0 {
		return nil, nil, errInvalidDERSignature
	}

	// Check that we've consumed all bytes
	if pos != len(sig) {
		return nil, nil, errLeftoverBytes
	}

	// Strip leading zeros from r and s for processing
	for len(r) > 1 && r[0] == 0x00 {
		r = r[1:]
	}
	for len(s) > 1 && s[0] == 0x00 {
		s = s[1:]
	}

	return r, s, nil
}

// WycheproofTestVectors represents the root structure of the Wycheproof test vectors JSON.
type WycheproofTestVectors struct {
	Algorithm     string            `json:"algorithm"`
	NumberOfTests int               `json:"numberOfTests"`
	TestGroups    []WycheproofGroup `json:"testGroups"`
}

// WycheproofGroup represents a group of tests with a common public key.
type WycheproofGroup struct {
	Type      string              `json:"type"`
	PublicKey WycheproofPublicKey `json:"publicKey"`
	SHA       string              `json:"sha"`
	Tests     []WycheproofTest    `json:"tests"`
}

// WycheproofPublicKey represents the public key in the test vectors.
type WycheproofPublicKey struct {
	Type         string `json:"type"`
	Curve        string `json:"curve"`
	KeySize      int    `json:"keySize"`
	Uncompressed string `json:"uncompressed"`
	Wx           string `json:"wx"`
	Wy           string `json:"wy"`
}

// WycheproofTest represents an individual test case.
type WycheproofTest struct {
	TcId    int      `json:"tcId"`
	Comment string   `json:"comment"`
	Msg     string   `json:"msg"`
	Sig     string   `json:"sig"`
	Result  string   `json:"result"`
	Flags   []string `json:"flags"`
}

// getTestDataPath returns the path to the testdata directory.
func getTestDataPath() string {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		panic("unable to get caller information")
	}
	// Navigate from crypto/secp256k1 to project root
	dir := filepath.Dir(filename)
	return filepath.Join(dir, "..", "..", "testdata", "wycheproof", "ecdsa_secp256k1_sha256_bitcoin_test.json")
}

// loadWycheproofTestVectors loads the test vectors from the JSON file.
func loadWycheproofTestVectors(t *testing.T) *WycheproofTestVectors {
	t.Helper()

	path := getTestDataPath()
	data, err := os.ReadFile(path)
	require.NoError(t, err, "failed to read test vectors file: %s", path)

	var vectors WycheproofTestVectors
	err = json.Unmarshal(data, &vectors)
	require.NoError(t, err, "failed to parse test vectors JSON")

	return &vectors
}

// parsePublicKey parses the public key from wx and wy hex strings.
func parsePublicKey(t *testing.T, wxHex, wyHex string) *secp256k1.PublicKey {
	t.Helper()

	wx, err := hex.DecodeString(wxHex)
	require.NoError(t, err, "failed to decode wx")

	wy, err := hex.DecodeString(wyHex)
	require.NoError(t, err, "failed to decode wy")

	// Pad to 32 bytes if necessary
	if len(wx) < 32 {
		padded := make([]byte, 32)
		copy(padded[32-len(wx):], wx)
		wx = padded
	}
	if len(wy) < 32 {
		padded := make([]byte, 32)
		copy(padded[32-len(wy):], wy)
		wy = padded
	}

	// If wx or wy is longer than 32 bytes (leading zero for positive number indication),
	// take the last 32 bytes
	if len(wx) > 32 {
		wx = wx[len(wx)-32:]
	}
	if len(wy) > 32 {
		wy = wy[len(wy)-32:]
	}

	var xField, yField secp256k1.FieldVal
	xField.SetByteSlice(wx)
	yField.SetByteSlice(wy)

	return secp256k1.NewPublicKey(&xField, &yField)
}

// verifySignatureSHA256 verifies an ECDSA signature using SHA-256 hash.
// This is the Bitcoin-style verification used by Wycheproof tests.
// It includes the Bitcoin-specific malleability check (s must be <= n/2).
func verifySignatureSHA256(msg []byte, sig []byte, pubKey *secp256k1.PublicKey) bool {
	// Parse the DER signature with strict validation (BIP 66)
	r, s, err := parseStrictDERSignature(sig)
	if err != nil {
		return false
	}

	// Check for empty r or s (invalid signatures)
	if len(r) == 0 || len(s) == 0 {
		return false
	}

	// Check for excessively large r or s values (more than 32 bytes)
	if len(r) > 32 || len(s) > 32 {
		return false
	}

	// Convert r and s to big.Int for malleability check
	rInt := new(big.Int).SetBytes(r)
	sInt := new(big.Int).SetBytes(s)

	// Bitcoin-style signature malleability check: s must be <= n/2
	// This prevents transaction malleability attacks
	if sInt.Cmp(halfN) > 0 {
		return false
	}

	// Check that r and s are within valid range (< curve order)
	if rInt.Cmp(curveOrderN) >= 0 || sInt.Cmp(curveOrderN) >= 0 {
		return false
	}

	// Convert r and s to [32]byte arrays
	var rBytes, sBytes [32]byte
	copy(rBytes[32-len(r):], r)
	copy(sBytes[32-len(s):], s)

	ecdsaR := &secp256k1.ModNScalar{}
	ecdsaS := &secp256k1.ModNScalar{}

	// Check for overflow when setting R and S values
	if ecdsaR.SetBytes(&rBytes) != 0 {
		return false
	}
	if ecdsaS.SetBytes(&sBytes) != 0 {
		return false
	}

	// Check that r and s are not zero
	if ecdsaR.IsZero() || ecdsaS.IsZero() {
		return false
	}

	parsedSig := ecdsa.NewSignature(ecdsaR, ecdsaS)

	// Hash the message with SHA-256 (Bitcoin style)
	hash := sha256.Sum256(msg)

	return parsedSig.Verify(hash[:], pubKey)
}

func TestWycheproofECDSA(t *testing.T) {
	vectors := loadWycheproofTestVectors(t)

	require.Equal(t, "ECDSA", vectors.Algorithm, "unexpected algorithm")

	totalTests := 0
	passedTests := 0
	skippedTests := 0

	for _, group := range vectors.TestGroups {
		require.Equal(t, "SHA-256", group.SHA, "test vectors use unexpected hash")
		require.Equal(t, "secp256k1", group.PublicKey.Curve, "test vectors use unexpected curve")

		pubKey := parsePublicKey(t, group.PublicKey.Wx, group.PublicKey.Wy)

		for _, tc := range group.Tests {
			totalTests++
			tcName := tc.Comment
			if tcName == "" {
				tcName = "test"
			}

			t.Run(tcName, func(t *testing.T) {
				// Decode the message and signature
				msg, err := hex.DecodeString(tc.Msg)
				if err != nil {
					if tc.Result == "invalid" {
						// Invalid message encoding is expected to fail
						return
					}
					require.NoError(t, err, "tcId %d: failed to decode message", tc.TcId)
				}

				sig, err := hex.DecodeString(tc.Sig)
				if err != nil {
					if tc.Result == "invalid" {
						// Invalid signature encoding is expected to fail
						return
					}
					require.NoError(t, err, "tcId %d: failed to decode signature", tc.TcId)
				}

				// Verify the signature
				verified := verifySignatureSHA256(msg, sig, pubKey)

				switch tc.Result {
				case "valid":
					require.True(t, verified, "tcId %d (%s): valid signature should verify", tc.TcId, tc.Comment)
					passedTests++
				case "invalid":
					require.False(t, verified, "tcId %d (%s): invalid signature should not verify", tc.TcId, tc.Comment)
					passedTests++
				case "acceptable":
					// Acceptable signatures may or may not verify depending on the implementation.
					// For Bitcoin-style signature verification with malleability checks,
					// we expect these to fail (signatures with s > n/2 should be rejected).
					// Log the result but don't fail the test.
					if verified {
						t.Logf("tcId %d (%s): acceptable signature verified (flags: %v)", tc.TcId, tc.Comment, tc.Flags)
					} else {
						t.Logf("tcId %d (%s): acceptable signature rejected (flags: %v)", tc.TcId, tc.Comment, tc.Flags)
					}
					skippedTests++
				default:
					t.Fatalf("tcId %d: unexpected result type: %s", tc.TcId, tc.Result)
				}
			})
		}
	}

	t.Logf("Wycheproof ECDSA test results: %d total, %d passed, %d acceptable (implementation-dependent)",
		totalTests, passedTests, skippedTests)
}

// TestWycheproofECDSATableDriven runs the Wycheproof test vectors as table-driven tests.
func TestWycheproofECDSATableDriven(t *testing.T) {
	vectors := loadWycheproofTestVectors(t)

	for groupIdx, group := range vectors.TestGroups {
		pubKey := parsePublicKey(t, group.PublicKey.Wx, group.PublicKey.Wy)

		testCases := make([]struct {
			name           string
			tcId           int
			msg            string
			sig            string
			expectedResult string
			flags          []string
		}, len(group.Tests))

		for i, tc := range group.Tests {
			testCases[i] = struct {
				name           string
				tcId           int
				msg            string
				sig            string
				expectedResult string
				flags          []string
			}{
				name:           tc.Comment,
				tcId:           tc.TcId,
				msg:            tc.Msg,
				sig:            tc.Sig,
				expectedResult: tc.Result,
				flags:          tc.Flags,
			}
		}

		t.Run("group_"+string(rune('0'+groupIdx)), func(t *testing.T) {
			for _, tc := range testCases {
				testName := tc.name
				if testName == "" {
					testName = "test"
				}

				t.Run(testName, func(t *testing.T) {
					t.Parallel()

					msg, msgErr := hex.DecodeString(tc.msg)
					sig, sigErr := hex.DecodeString(tc.sig)

					// If we can't decode the test data, check if that's expected
					if msgErr != nil || sigErr != nil {
						if tc.expectedResult == "invalid" {
							// Invalid encoding is expected
							return
						}
						require.NoError(t, msgErr, "tcId %d: failed to decode message", tc.tcId)
						require.NoError(t, sigErr, "tcId %d: failed to decode signature", tc.tcId)
					}

					verified := verifySignatureSHA256(msg, sig, pubKey)

					switch tc.expectedResult {
					case "valid":
						require.True(t, verified, "tcId %d (%s): valid signature should verify", tc.tcId, tc.name)
					case "invalid":
						require.False(t, verified, "tcId %d (%s): invalid signature should not verify", tc.tcId, tc.name)
					case "acceptable":
						// For acceptable tests, we document the behavior but don't fail
						// Bitcoin implementations should reject malleable signatures (s > n/2)
						t.Logf("tcId %d (%s): acceptable signature %s (flags: %v)",
							tc.tcId, tc.name, map[bool]string{true: "accepted", false: "rejected"}[verified], tc.flags)
					}
				})
			}
		})
	}
}
