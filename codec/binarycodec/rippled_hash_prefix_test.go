package binarycodec

import (
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// Hash Prefix Tests derived from rippled include/xrpl/protocol/HashPrefix.h
// These tests verify hash prefixes match rippled exactly for protocol compatibility.
// =============================================================================

// Hash prefixes from rippled - these are critical for signing and hashing.
// Each prefix is computed as: (char1 << 24) + (char2 << 16) + (char3 << 8)
const (
	// HashPrefixTransactionID: "TXN" - transaction ID hashing
	HashPrefixTransactionID = 0x54584E00

	// HashPrefixTxSign: "STX" - transaction signing
	HashPrefixTxSign = 0x53545800

	// HashPrefixTxMultiSign: "SMT" - multi-signature signing
	HashPrefixTxMultiSign = 0x534D5400

	// HashPrefixPaymentChannelClaim: "CLM" - payment channel claim
	HashPrefixPaymentChannelClaim = 0x434C4D00

	// HashPrefixBatch: "BCH" - batch transaction
	HashPrefixBatch = 0x42434800

	// HashPrefixValidation: "VAL" - validation signing
	HashPrefixValidation = 0x56414C00

	// HashPrefixProposal: "PRP" - proposal signing
	HashPrefixProposal = 0x50525000

	// HashPrefixManifest: "MAN" - manifest
	HashPrefixManifest = 0x4D414E00

	// HashPrefixCredential: "CRD" - credentials signature
	HashPrefixCredential = 0x43524400

	// HashPrefixLedgerMaster: "LWR" - ledger master data
	HashPrefixLedgerMaster = 0x4C575200

	// HashPrefixTxNode: "SND" - transaction node
	HashPrefixTxNode = 0x534E4400

	// HashPrefixLeafNode: "MLN" - account state leaf node
	HashPrefixLeafNode = 0x4D4C4E00

	// HashPrefixInnerNode: "MIN" - inner node in V1 tree
	HashPrefixInnerNode = 0x4D494E00
)

// TestHashPrefixValues verifies hash prefix constants match rippled.
// From include/xrpl/protocol/HashPrefix.h
func TestHashPrefixValues(t *testing.T) {
	tests := []struct {
		name     string
		prefix   uint32
		chars    string // The 3-char string that creates this prefix
		expected string // Expected hex (big-endian, lowercase as Go's hex.EncodeToString produces)
	}{
		{
			name:     "transactionID (TXN)",
			prefix:   HashPrefixTransactionID,
			chars:    "TXN",
			expected: "54584e00",
		},
		{
			name:     "txSign (STX)",
			prefix:   HashPrefixTxSign,
			chars:    "STX",
			expected: "53545800",
		},
		{
			name:     "txMultiSign (SMT)",
			prefix:   HashPrefixTxMultiSign,
			chars:    "SMT",
			expected: "534d5400",
		},
		{
			name:     "paymentChannelClaim (CLM)",
			prefix:   HashPrefixPaymentChannelClaim,
			chars:    "CLM",
			expected: "434c4d00",
		},
		{
			name:     "batch (BCH)",
			prefix:   HashPrefixBatch,
			chars:    "BCH",
			expected: "42434800",
		},
		{
			name:     "validation (VAL)",
			prefix:   HashPrefixValidation,
			chars:    "VAL",
			expected: "56414c00",
		},
		{
			name:     "proposal (PRP)",
			prefix:   HashPrefixProposal,
			chars:    "PRP",
			expected: "50525000",
		},
		{
			name:     "manifest (MAN)",
			prefix:   HashPrefixManifest,
			chars:    "MAN",
			expected: "4d414e00",
		},
		{
			name:     "credential (CRD)",
			prefix:   HashPrefixCredential,
			chars:    "CRD",
			expected: "43524400",
		},
		{
			name:     "ledgerMaster (LWR)",
			prefix:   HashPrefixLedgerMaster,
			chars:    "LWR",
			expected: "4c575200",
		},
		{
			name:     "txNode (SND)",
			prefix:   HashPrefixTxNode,
			chars:    "SND",
			expected: "534e4400",
		},
		{
			name:     "leafNode (MLN)",
			prefix:   HashPrefixLeafNode,
			chars:    "MLN",
			expected: "4d4c4e00",
		},
		{
			name:     "innerNode (MIN)",
			prefix:   HashPrefixInnerNode,
			chars:    "MIN",
			expected: "4d494e00",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Verify the constant matches expected hex
			actualHex := make([]byte, 4)
			actualHex[0] = byte(tc.prefix >> 24)
			actualHex[1] = byte(tc.prefix >> 16)
			actualHex[2] = byte(tc.prefix >> 8)
			actualHex[3] = byte(tc.prefix)

			assert.Equal(t, tc.expected, hex.EncodeToString(actualHex),
				"Hash prefix %s mismatch", tc.name)

			// Verify the char computation matches rippled's make_hash_prefix()
			computed := makeHashPrefix(tc.chars[0], tc.chars[1], tc.chars[2])
			assert.Equal(t, tc.prefix, computed,
				"Computed prefix for %s doesn't match constant", tc.chars)
		})
	}
}

// makeHashPrefix computes a hash prefix from 3 characters.
// Mirrors rippled's detail::make_hash_prefix()
func makeHashPrefix(a, b, c byte) uint32 {
	return (uint32(a) << 24) + (uint32(b) << 16) + (uint32(c) << 8)
}

// TestCodecHashPrefixes verifies the codec uses correct prefixes.
// These constants must match the codec.go file.
func TestCodecHashPrefixes(t *testing.T) {
	// From codec.go
	assert.Equal(t, "534D5400", txMultiSigPrefix, "txMultiSigPrefix (SMT)")
	assert.Equal(t, "434C4D00", paymentChannelClaimPrefix, "paymentChannelClaimPrefix (CLM)")
	assert.Equal(t, "53545800", txSigPrefix, "txSigPrefix (STX)")
	assert.Equal(t, "42434800", batchPrefix, "batchPrefix (BCH)")
}

// TestEncodeForSigning_HashPrefix verifies signing prepends correct prefix.
func TestEncodeForSigning_HashPrefix(t *testing.T) {
	input := map[string]any{
		"TransactionType": "Payment",
		"Fee":             "10",
		"Sequence":        uint32(1),
		"Account":         "rMBzp8CgpE441cp5PVyA9rpVV7oT8hP3ys",
		"Destination":     "rMBzp8CgpE441cp5PVyA9rpVV7oT8hP3ys",
		"Amount":          "1000000",
	}

	result, err := EncodeForSigning(input)
	require.NoError(t, err)

	// Should start with txSigPrefix (STX = 53545800)
	assert.True(t, len(result) >= 8, "Result should have at least prefix")
	prefix := result[:8]
	assert.Equal(t, txSigPrefix, prefix, "Should start with STX prefix")
}

// TestEncodeForMultisigning_HashPrefix verifies multi-signing prepends correct prefix.
func TestEncodeForMultisigning_HashPrefix(t *testing.T) {
	input := map[string]any{
		"TransactionType": "Payment",
		"Fee":             "10",
		"Sequence":        uint32(1),
		"Account":         "rMBzp8CgpE441cp5PVyA9rpVV7oT8hP3ys",
		"Destination":     "rMBzp8CgpE441cp5PVyA9rpVV7oT8hP3ys",
		"Amount":          "1000000",
	}

	result, err := EncodeForMultisigning(input, "rMBzp8CgpE441cp5PVyA9rpVV7oT8hP3ys")
	require.NoError(t, err)

	// Should start with txMultiSigPrefix (SMT = 534D5400)
	assert.True(t, len(result) >= 8, "Result should have at least prefix")
	prefix := result[:8]
	assert.Equal(t, txMultiSigPrefix, prefix, "Should start with SMT prefix")
}

// TestEncodeForSigningClaim_HashPrefix verifies payment channel claim prefix.
func TestEncodeForSigningClaim_HashPrefix(t *testing.T) {
	input := map[string]any{
		"Channel": "43904CBFCDCEC530B4037871F86EE90BF799DF8D2E0EA564BC8A3F332E4F5FB1",
		"Amount":  "1000",
	}

	result, err := EncodeForSigningClaim(input)
	require.NoError(t, err)

	// Should start with paymentChannelClaimPrefix (CLM = 434C4D00)
	assert.True(t, len(result) >= 8, "Result should have at least prefix")
	prefix := result[:8]
	assert.Equal(t, paymentChannelClaimPrefix, prefix, "Should start with CLM prefix")
}

// TestEncodeForSigningBatch_HashPrefix verifies batch signing prefix.
func TestEncodeForSigningBatch_HashPrefix(t *testing.T) {
	input := map[string]any{
		"flags": uint32(1),
		"txIDs": []string{
			"ABE4871E9083DF66727045D49DEEDD3A6F166EB7F8D1E92FE868F02E76B2C5CA",
		},
	}

	result, err := EncodeForSigningBatch(input)
	require.NoError(t, err)

	// Should start with batchPrefix (BCH = 42434800)
	assert.True(t, len(result) >= 8, "Result should have at least prefix")
	prefix := result[:8]
	assert.Equal(t, batchPrefix, prefix, "Should start with BCH prefix")
}

// TestHashPrefixEndianness verifies prefixes are big-endian.
// rippled uses big-endian for hash prefixes.
func TestHashPrefixEndianness(t *testing.T) {
	// "STX" should produce bytes: 'S' 'T' 'X' 0x00
	// In big-endian: 0x53 0x54 0x58 0x00
	stxBytes := []byte{0x53, 0x54, 0x58, 0x00}
	stxHex := hex.EncodeToString(stxBytes)

	assert.Equal(t, txSigPrefix, stxHex, "STX prefix should be big-endian")
	assert.Equal(t, "53545800", stxHex)
}

// TestAllHashPrefixesUnique verifies no hash prefix collisions.
func TestAllHashPrefixesUnique(t *testing.T) {
	prefixes := map[uint32]string{
		HashPrefixTransactionID:       "TXN",
		HashPrefixTxSign:              "STX",
		HashPrefixTxMultiSign:         "SMT",
		HashPrefixPaymentChannelClaim: "CLM",
		HashPrefixBatch:               "BCH",
		HashPrefixValidation:          "VAL",
		HashPrefixProposal:            "PRP",
		HashPrefixManifest:            "MAN",
		HashPrefixCredential:          "CRD",
		HashPrefixLedgerMaster:        "LWR",
		HashPrefixTxNode:              "SND",
		HashPrefixLeafNode:            "MLN",
		HashPrefixInnerNode:           "MIN",
	}

	// Verify all values are unique
	seen := make(map[uint32]string)
	for prefix, name := range prefixes {
		if existing, ok := seen[prefix]; ok {
			t.Errorf("Duplicate prefix value 0x%X: %s and %s", prefix, existing, name)
		}
		seen[prefix] = name
	}

	// Verify we have all expected prefixes
	assert.Len(t, prefixes, 13, "Should have all 13 hash prefixes")
}

// TestRippledHashPrefixCompatibility documents the exact hex values from rippled.
// These values are protocol-critical and must never change.
func TestRippledHashPrefixCompatibility(t *testing.T) {
	// Document the exact values for reference
	rippledPrefixes := map[string]string{
		"transactionID":       "54584E00", // 'T' 'X' 'N' 0
		"txNode":              "534E4400", // 'S' 'N' 'D' 0
		"leafNode":            "4D4C4E00", // 'M' 'L' 'N' 0
		"innerNode":           "4D494E00", // 'M' 'I' 'N' 0
		"ledgerMaster":        "4C575200", // 'L' 'W' 'R' 0
		"txSign":              "53545800", // 'S' 'T' 'X' 0
		"txMultiSign":         "534D5400", // 'S' 'M' 'T' 0
		"validation":          "56414C00", // 'V' 'A' 'L' 0
		"proposal":            "50525000", // 'P' 'R' 'P' 0
		"manifest":            "4D414E00", // 'M' 'A' 'N' 0
		"paymentChannelClaim": "434C4D00", // 'C' 'L' 'M' 0
		"credential":          "43524400", // 'C' 'R' 'D' 0
		"batch":               "42434800", // 'B' 'C' 'H' 0
	}

	for name, expected := range rippledPrefixes {
		t.Run(name, func(t *testing.T) {
			// Just document the expected values
			assert.Len(t, expected, 8, "Prefix should be 4 bytes (8 hex chars)")

			// Verify last byte is 0x00
			assert.Equal(t, "00", expected[6:8], "Last byte should be 0x00")
		})
	}
}
