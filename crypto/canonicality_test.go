package crypto

import (
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestECDSACanonicality(t *testing.T) {
	tests := []struct {
		name     string
		sig      string
		expected Canonicality
	}{
		{
			name: "Fully canonical signature",
			// A valid DER signature with low S value
			sig:      "304402206878b5690514437a2342405029426cc2b25b4a03fc396fef845d656cf62bad2c022018610a8d37f65ad02af907c8cb8f72becd0de43de7d5f42fefccb6c2a391a67c",
			expected: CanonicityFullyCanonical,
		},
		{
			name:     "Too short signature",
			sig:      "3006020101020101",
			expected: CanonicityFullyCanonical, // Actually this is minimal valid
		},
		{
			name:     "Invalid sequence tag",
			sig:      "3106020100020100",
			expected: CanonicityNone,
		},
		{
			name:     "Wrong total length",
			sig:      "3007020100020100",
			expected: CanonicityNone,
		},
		{
			name:     "Empty signature",
			sig:      "",
			expected: CanonicityNone,
		},
		{
			name:     "Just sequence tag",
			sig:      "30",
			expected: CanonicityNone,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sig, err := hex.DecodeString(tt.sig)
			if err != nil && tt.expected == CanonicityNone {
				// Invalid hex is also invalid signature
				return
			}
			require.NoError(t, err)
			result := ECDSACanonicality(sig)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestECDSACanonicality_EdgeCases(t *testing.T) {
	// Test with R and S at boundary values
	t.Run("Zero R value should be invalid", func(t *testing.T) {
		// DER signature with R=0
		sig, _ := hex.DecodeString("300602010002010a")
		assert.Equal(t, CanonicityNone, ECDSACanonicality(sig))
	})

	t.Run("Negative R value (high bit set without padding)", func(t *testing.T) {
		// The byte 0x80 has high bit set - should fail
		sig, _ := hex.DecodeString("3006020180020101")
		assert.Equal(t, CanonicityNone, ECDSACanonicality(sig))
	})
}

func TestEd25519Canonical(t *testing.T) {
	tests := []struct {
		name     string
		sig      string
		expected bool
	}{
		{
			name:     "Valid Ed25519 signature with low S",
			sig:      "e5564300c360ac729086e2cc806e828a84877f1eb8e5d974d873e065224901555fb8821590a33bacc61e39701cf9b46bd25bf5f0595bbe24655141438e7a100b",
			expected: true,
		},
		{
			name:     "Wrong length (too short)",
			sig:      "e5564300c360ac729086e2cc806e828a84877f1eb8e5d974d873e06522490155",
			expected: false,
		},
		{
			name:     "Wrong length (too long)",
			sig:      "e5564300c360ac729086e2cc806e828a84877f1eb8e5d974d873e065224901555fb8821590a33bacc61e39701cf9b46bd25bf5f0595bbe24655141438e7a100b00",
			expected: false,
		},
		{
			name:     "Empty signature",
			sig:      "",
			expected: false,
		},
		{
			// S below the boundary: BE = L - 1, last byte 0xEC. Must verify.
			name:     "Boundary: S = L - 1 (just-canonical)",
			sig:      "0000000000000000000000000000000000000000000000000000000000000000" + "ECD3F55C1A631258D69CF7A2DEF9DE1400000000000000000000000000000010",
			expected: true,
		},
		{
			// S exactly at the subgroup order L. rippled uses strict < (lex_compare),
			// so this must be rejected (PublicKey.cpp:188).
			name:     "Boundary: S = L (must reject)",
			sig:      "0000000000000000000000000000000000000000000000000000000000000000" + "EDD3F55C1A631258D69CF7A2DEF9DE1400000000000000000000000000000010",
			expected: false,
		},
		{
			// S = L + 1, above the boundary. Last BE byte 0xEE > 0xED.
			name:     "Above-boundary: S = L + 1 (must reject)",
			sig:      "0000000000000000000000000000000000000000000000000000000000000000" + "EED3F55C1A631258D69CF7A2DEF9DE1400000000000000000000000000000010",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sig, _ := hex.DecodeString(tt.sig)
			result := Ed25519Canonical(sig)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestParseDERInteger(t *testing.T) {
	tests := []struct {
		name        string
		data        string
		expectValue bool
		expectLen   int
	}{
		{
			name:        "Valid single byte integer",
			data:        "020101",
			expectValue: true,
			expectLen:   1,
		},
		{
			name:        "Valid multi-byte integer",
			data:        "02030102ff",
			expectValue: true,
			expectLen:   3,
		},
		{
			name:        "Valid integer with leading zero (high bit set)",
			data:        "020200ff",
			expectValue: true,
			expectLen:   2,
		},
		{
			name:        "Invalid - wrong tag",
			data:        "030101",
			expectValue: false,
			expectLen:   0,
		},
		{
			name:        "Invalid - too short",
			data:        "02",
			expectValue: false,
			expectLen:   0,
		},
		{
			name:        "Invalid - length exceeds data",
			data:        "020501",
			expectValue: false,
			expectLen:   0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, _ := hex.DecodeString(tt.data)
			result, _, ok := parseDERInteger(data)
			assert.Equal(t, tt.expectValue, ok)
			if ok {
				assert.Equal(t, tt.expectLen, len(result))
			}
		})
	}
}
