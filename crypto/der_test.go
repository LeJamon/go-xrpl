package crypto

import (
	"encoding/hex"
	"math/big"
	"testing"

	"github.com/stretchr/testify/require"
)

func mustDecodeHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	require.NoError(t, err)
	return b
}

func BenchmarkDERSigToRS(b *testing.B) {
	sig, _ := hex.DecodeString("3045022100E1617F1A3C85B5BC8FA6224F893FE9068BEA8F8D075EE144F6F9D255C829761802206FD9B361CDE83A0C3D5654232F1D7CFB1A614E9A8F9B1A861564029065516E64")
	for b.Loop() {
		DERSigToRS(sig)
	}
}

func TestDERSigToRS(t *testing.T) {
	testCases := []struct {
		name        string
		sigHex      string
		expectedR   string
		expectedS   string
		expectError error
	}{
		{
			name:        "fail - invalid signature tag",
			sigHex:      "3145022100E1617F1A3C85B5BC8FA6224F893FE9068BEA8F8D075EE144F6F9D255C829761802206FD9B361CDE83A0C3D5654232F1D7CFB1A614E9A8F9B1A861564029065516E64",
			expectError: ErrInvalidDERSignature,
		},
		{
			name:        "fail - invalid length",
			sigHex:      "3044022100E1617F1A3C85B5BC8FA6224F893FE9068BEA8F8D075EE144F6F9D255C829761802206FD9B361CDE83A0C3D5654232F1D7CFB1A614E9A8F9B1A861564029065516E64",
			expectError: ErrInvalidDERSignature,
		},
		{
			name:        "fail - truncated r integer",
			sigHex:      "3003020301",
			expectError: ErrInvalidDERSignature,
		},
		{
			name:        "fail - truncated s integer",
			sigHex:      "3006020101020301",
			expectError: ErrInvalidDERSignature,
		},
		{
			name:        "fail - leftover bytes",
			sigHex:      "300702010102010101",
			expectError: ErrLeftoverBytes,
		},
		{
			// Single zero-byte integers are non-minimal DER, rejected by the
			// strict parser before the range check.
			name:        "fail - zero r and s",
			sigHex:      "3006020100020100",
			expectError: ErrInvalidDERSignature,
		},
		{
			name:        "fail - r equal to curve order",
			sigHex:      "3026022100FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFEBAAEDCE6AF48A03BBFD25E8CD0364141020101",
			expectError: ErrInvalidDERSignatureValue,
		},
		{
			name:      "pass - valid DER signature",
			sigHex:    "3045022100E1617F1A3C85B5BC8FA6224F893FE9068BEA8F8D075EE144F6F9D255C829761802206FD9B361CDE83A0C3D5654232F1D7CFB1A614E9A8F9B1A861564029065516E64",
			expectedR: "e1617f1a3c85b5bc8fa6224f893fe9068bea8f8d075ee144f6f9d255c8297618",
			expectedS: "6fd9b361cde83a0c3d5654232f1d7cfb1a614e9a8f9b1a861564029065516e64",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			r, s, err := DERSigToRS(mustDecodeHex(t, tc.sigHex))

			if tc.expectError != nil {
				require.Equal(t, tc.expectError, err)
			} else {
				require.NoError(t, err)
				require.Equal(t, tc.expectedR, hex.EncodeToString(r))
				require.Equal(t, tc.expectedS, hex.EncodeToString(s))
			}
		})
	}
}

func BenchmarkEncodeDERSignature(b *testing.B) {
	r, _ := new(big.Int).SetString("E1617F1A3C85B5BC8FA6224F893FE9068BEA8F8D075EE144F6F9D255C8297618", 16)
	s, _ := new(big.Int).SetString("6FD9B361CDE83A0C3D5654232F1D7CFB1A614E9A8F9B1A861564029065516E64", 16)
	for b.Loop() {
		EncodeDERSignature(r, s)
	}
}

func TestEncodeDERSignature(t *testing.T) {
	testCases := []struct {
		name        string
		rHex        string
		sHex        string
		expectedDER string
	}{
		{
			name:        "pass - valid r and s values",
			rHex:        "e1617f1a3c85b5bc8fa6224f893fe9068bea8f8d075ee144f6f9d255c8297618",
			sHex:        "6fd9b361cde83a0c3d5654232f1d7cfb1a614e9a8f9b1a861564029065516e64",
			expectedDER: "3045022100e1617f1a3c85b5bc8fa6224f893fe9068bea8f8d075ee144f6f9d255c829761802206fd9b361cde83a0c3d5654232f1d7cfb1a614e9a8f9b1a861564029065516e64",
		},
		{
			name:        "pass - r value with leading zero",
			rHex:        "00e1617f1a3c85b5bc8fa6224f893fe9068bea8f8d075ee144f6f9d255c8297618",
			sHex:        "6fd9b361cde83a0c3d5654232f1d7cfb1a614e9a8f9b1a861564029065516e64",
			expectedDER: "3045022100e1617f1a3c85b5bc8fa6224f893fe9068bea8f8d075ee144f6f9d255c829761802206fd9b361cde83a0c3d5654232f1d7cfb1a614e9a8f9b1a861564029065516e64",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			r, ok := new(big.Int).SetString(tc.rHex, 16)
			require.True(t, ok)
			s, ok := new(big.Int).SetString(tc.sHex, 16)
			require.True(t, ok)

			result := EncodeDERSignature(r, s)
			require.Equal(t, tc.expectedDER, hex.EncodeToString(result))

			// The encoded signature must parse back to the same r/s.
			gotR, gotS, err := DERSigToRS(result)
			require.NoError(t, err)
			require.Equal(t, new(big.Int).SetBytes(gotR), r)
			require.Equal(t, new(big.Int).SetBytes(gotS), s)
		})
	}
}
