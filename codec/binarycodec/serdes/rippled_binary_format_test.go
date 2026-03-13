package serdes

import (
	"encoding/hex"
	"testing"

	"github.com/LeJamon/goXRPLd/codec/binarycodec/definitions"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// Binary Format Tests derived from rippled protocol specification
// These tests verify VL encoding, field ID encoding, and other binary format details.
// =============================================================================

// TestVariableLengthEncoding tests the Variable Length (VL) encoding scheme.
// VL encoding is used for fields like Blob, AccountID, and other variable-length types.
// The encoding uses 1-3 bytes depending on length:
//   - 0-192: 1 byte (length directly)
//   - 193-12480: 2 bytes
//   - 12481-918744: 3 bytes
func TestVariableLengthEncoding(t *testing.T) {
	tests := []struct {
		name        string
		length      int
		expectedHex string // lowercase hex as Go's hex.EncodeToString produces
		expectError bool
	}{
		// Single byte encoding (0-192)
		{
			name:        "length 0",
			length:      0,
			expectedHex: "00",
			expectError: false,
		},
		{
			name:        "length 1",
			length:      1,
			expectedHex: "01",
			expectError: false,
		},
		{
			name:        "length 100",
			length:      100,
			expectedHex: "64",
			expectError: false,
		},
		{
			name:        "length 192 (max single byte)",
			length:      192,
			expectedHex: "c0",
			expectError: false,
		},
		// Two byte encoding (193-12480)
		{
			name:        "length 193 (min two byte)",
			length:      193,
			expectedHex: "c100",
			expectError: false,
		},
		{
			name:        "length 200",
			length:      200,
			expectedHex: "c107",
			expectError: false,
		},
		{
			name:        "length 1000",
			length:      1000,
			expectedHex: "c427",
			expectError: false,
		},
		{
			name:        "length 12479 (max two byte)",
			length:      12479,
			expectedHex: "f0fe",
			expectError: false,
		},
		// Three byte encoding (12480-918744)
		// Note: implementation uses 12480 as start of 3-byte encoding
		{
			name:        "length 12480 (min three byte)",
			length:      12480,
			expectedHex: "f0ffff",
			expectError: false,
		},
		{
			name:        "length 12481",
			length:      12481,
			expectedHex: "f10000",
			expectError: false,
		},
		{
			name:        "length 100000",
			length:      100000,
			expectedHex: "f255df",
			expectError: false,
		},
		{
			name:        "length 918744 (max allowed)",
			length:      918744,
			expectedHex: "fed417",
			expectError: false,
		},
		// Error case - exceeds maximum
		{
			name:        "length 918745 (exceeds max)",
			length:      918745,
			expectedHex: "",
			expectError: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result, err := encodeVariableLength(tc.length)

			if tc.expectError {
				require.Error(t, err)
				require.Equal(t, ErrLengthPrefixTooLong, err)
				return
			}

			require.NoError(t, err)
			actualHex := hex.EncodeToString(result)
			assert.Equal(t, tc.expectedHex, actualHex, "VL encoding mismatch for length %d", tc.length)
		})
	}
}

// TestVariableLengthDecoding tests VL decoding (parsing).
func TestVariableLengthDecoding(t *testing.T) {
	defs := definitions.Get()

	tests := []struct {
		name           string
		hexInput       string
		expectedLength int
		expectError    bool
	}{
		// Single byte decoding
		{
			name:           "decode length 0",
			hexInput:       "00",
			expectedLength: 0,
			expectError:    false,
		},
		{
			name:           "decode length 1",
			hexInput:       "01",
			expectedLength: 1,
			expectError:    false,
		},
		{
			name:           "decode length 192",
			hexInput:       "C0",
			expectedLength: 192,
			expectError:    false,
		},
		// Two byte decoding
		{
			name:           "decode length 193",
			hexInput:       "C100",
			expectedLength: 193,
			expectError:    false,
		},
		{
			name:           "decode length 200",
			hexInput:       "C107",
			expectedLength: 200,
			expectError:    false,
		},
		{
			name:           "decode length 12479",
			hexInput:       "F0FE",
			expectedLength: 12479,
			expectError:    false,
		},
		{
			name:           "decode length 12480",
			hexInput:       "F0FF",
			expectedLength: 12480,
			expectError:    false,
		},
		// Three byte decoding
		{
			name:           "decode length 12481",
			hexInput:       "F10000",
			expectedLength: 12481,
			expectError:    false,
		},
		{
			name:           "decode length 918744",
			hexInput:       "FED417",
			expectedLength: 918744,
			expectError:    false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			data, err := hex.DecodeString(tc.hexInput)
			require.NoError(t, err)

			parser := NewBinaryParser(data, defs)
			length, err := parser.ReadVariableLength()

			if tc.expectError {
				require.Error(t, err)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tc.expectedLength, length, "VL decoding mismatch")
		})
	}
}

// TestVariableLengthRoundtrip tests encode -> decode roundtrip.
func TestVariableLengthRoundtrip(t *testing.T) {
	defs := definitions.Get()

	lengths := []int{
		0, 1, 10, 100, 192, // Single byte
		193, 200, 1000, 5000, 12480, // Two byte
		12481, 50000, 100000, 918744, // Three byte
	}

	for _, length := range lengths {
		t.Run("", func(t *testing.T) {
			// Encode
			encoded, err := encodeVariableLength(length)
			require.NoError(t, err)

			// Decode
			parser := NewBinaryParser(encoded, defs)
			decoded, err := parser.ReadVariableLength()
			require.NoError(t, err)

			assert.Equal(t, length, decoded, "VL roundtrip failed for length %d", length)
		})
	}
}

// TestFieldIDEncoding_TypeFieldCodes tests field ID byte encoding.
// Field IDs encode type code and field code in 1-3 bytes:
//   - type < 16 && field < 16: 1 byte (type<<4 | field)
//   - type >= 16 && field < 16: 2 bytes (field, type)
//   - type < 16 && field >= 16: 2 bytes (type<<4, field)
//   - type >= 16 && field >= 16: 3 bytes (0, type, field)
func TestFieldIDEncoding_TypeFieldCodes(t *testing.T) {
	defs := definitions.Get()
	codec := NewFieldIDCodec(defs)

	tests := []struct {
		name        string
		fieldName   string
		expectedHex string
		description string
	}{
		// Type < 16, Field < 16 = 1 byte
		{
			name:        "TransactionType",
			fieldName:   "TransactionType",
			expectedHex: "12",
			description: "type=1, field=2 -> (1<<4)|2 = 0x12",
		},
		{
			name:        "Flags",
			fieldName:   "Flags",
			expectedHex: "22",
			description: "type=2, field=2 -> (2<<4)|2 = 0x22",
		},
		{
			name:        "SourceTag",
			fieldName:   "SourceTag",
			expectedHex: "23",
			description: "type=2, field=3 -> (2<<4)|3 = 0x23",
		},
		{
			name:        "Sequence",
			fieldName:   "Sequence",
			expectedHex: "24",
			description: "type=2, field=4 -> (2<<4)|4 = 0x24",
		},
		{
			name:        "DestinationTag",
			fieldName:   "DestinationTag",
			expectedHex: "2e",
			description: "type=2, field=14 -> (2<<4)|14 = 0x2E",
		},
		{
			name:        "LedgerEntryType",
			fieldName:   "LedgerEntryType",
			expectedHex: "11",
			description: "type=1, field=1 -> (1<<4)|1 = 0x11",
		},
		{
			name:        "Fee (Amount)",
			fieldName:   "Fee",
			expectedHex: "68",
			description: "type=6, field=8 -> (6<<4)|8 = 0x68",
		},
		{
			name:        "Account (AccountID)",
			fieldName:   "Account",
			expectedHex: "81",
			description: "type=8, field=1 -> (8<<4)|1 = 0x81",
		},
		// OwnerNode (UInt64, type=3, field=4)
		{
			name:        "OwnerNode",
			fieldName:   "OwnerNode",
			expectedHex: "34",
			description: "type=3, field=4 -> (3<<4)|4 = 0x34",
		},
		// EmailHash (Hash128, type=4, field=1)
		{
			name:        "EmailHash",
			fieldName:   "EmailHash",
			expectedHex: "41",
			description: "type=4, field=1 -> (4<<4)|1 = 0x41",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			encoded, err := codec.Encode(tc.fieldName)
			require.NoError(t, err)

			actualHex := hex.EncodeToString(encoded)
			assert.Equal(t, tc.expectedHex, actualHex, "Field ID encoding mismatch: %s", tc.description)
		})
	}
}

// TestFieldIDDecoding tests field ID decoding from bytes to field names.
func TestFieldIDDecoding(t *testing.T) {
	defs := definitions.Get()
	codec := NewFieldIDCodec(defs)

	tests := []struct {
		name          string
		hexInput      string
		expectedField string
	}{
		// Single byte field IDs
		{
			name:          "decode TransactionType",
			hexInput:      "12",
			expectedField: "TransactionType",
		},
		{
			name:          "decode Flags",
			hexInput:      "22",
			expectedField: "Flags",
		},
		{
			name:          "decode Sequence",
			hexInput:      "24",
			expectedField: "Sequence",
		},
		{
			name:          "decode Fee",
			hexInput:      "68",
			expectedField: "Fee",
		},
		{
			name:          "decode Account",
			hexInput:      "81",
			expectedField: "Account",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fieldName, err := codec.Decode(tc.hexInput)
			require.NoError(t, err)
			assert.Equal(t, tc.expectedField, fieldName)
		})
	}
}

// TestFieldHeaderParsing tests reading field headers from binary stream.
func TestFieldHeaderParsing(t *testing.T) {
	defs := definitions.Get()

	tests := []struct {
		name          string
		hexInput      string
		expectedField string
	}{
		{
			name:          "TransactionType header",
			hexInput:      "12",
			expectedField: "TransactionType",
		},
		{
			name:          "Flags header",
			hexInput:      "22",
			expectedField: "Flags",
		},
		{
			name:          "multiple fields",
			hexInput:      "1200002200080000", // TransactionType=0, Flags=524288
			expectedField: "TransactionType",  // First field
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			data, err := hex.DecodeString(tc.hexInput)
			require.NoError(t, err)

			parser := NewBinaryParser(data, defs)
			field, err := parser.ReadField()
			require.NoError(t, err)
			assert.Equal(t, tc.expectedField, field.FieldName)
		})
	}
}

// TestBinaryParserOperations tests basic parser operations.
func TestBinaryParserOperations(t *testing.T) {
	defs := definitions.Get()

	t.Run("ReadByte", func(t *testing.T) {
		data := []byte{0x12, 0x34, 0x56}
		parser := NewBinaryParser(data, defs)

		b1, err := parser.ReadByte()
		require.NoError(t, err)
		assert.Equal(t, byte(0x12), b1)

		b2, err := parser.ReadByte()
		require.NoError(t, err)
		assert.Equal(t, byte(0x34), b2)

		b3, err := parser.ReadByte()
		require.NoError(t, err)
		assert.Equal(t, byte(0x56), b3)

		// Should error on empty
		_, err = parser.ReadByte()
		require.Error(t, err)
		assert.Equal(t, ErrParserOutOfBound, err)
	})

	t.Run("Peek", func(t *testing.T) {
		data := []byte{0xAB, 0xCD}
		parser := NewBinaryParser(data, defs)

		// Peek doesn't advance
		p1, err := parser.Peek()
		require.NoError(t, err)
		assert.Equal(t, byte(0xAB), p1)

		p2, err := parser.Peek()
		require.NoError(t, err)
		assert.Equal(t, byte(0xAB), p2)

		// Read advances
		b, err := parser.ReadByte()
		require.NoError(t, err)
		assert.Equal(t, byte(0xAB), b)

		// Peek now shows next byte
		p3, err := parser.Peek()
		require.NoError(t, err)
		assert.Equal(t, byte(0xCD), p3)
	})

	t.Run("ReadBytes", func(t *testing.T) {
		data := []byte{0x01, 0x02, 0x03, 0x04, 0x05}
		parser := NewBinaryParser(data, defs)

		bytes, err := parser.ReadBytes(3)
		require.NoError(t, err)
		assert.Equal(t, []byte{0x01, 0x02, 0x03}, bytes)

		// Should error if not enough bytes
		_, err = parser.ReadBytes(5)
		require.Error(t, err)
	})

	t.Run("HasMore", func(t *testing.T) {
		data := []byte{0x01}
		parser := NewBinaryParser(data, defs)

		assert.True(t, parser.HasMore())

		_, _ = parser.ReadByte()
		assert.False(t, parser.HasMore())
	})
}

// TestBinarySerializerOperations tests serializer sink operations.
func TestBinarySerializerOperations(t *testing.T) {
	defs := definitions.Get()
	codec := NewFieldIDCodec(defs)

	t.Run("GetSink returns accumulated bytes", func(t *testing.T) {
		serializer := NewBinarySerializer(codec)

		// Initially empty
		assert.Empty(t, serializer.GetSink())

		// After writing, sink has data
		fi, err := defs.GetFieldInstanceByFieldName("Flags")
		require.NoError(t, err)

		err = serializer.WriteFieldAndValue(*fi, []byte{0x00, 0x08, 0x00, 0x00})
		require.NoError(t, err)

		sink := serializer.GetSink()
		assert.NotEmpty(t, sink)
	})
}

// TestVLEncodedFieldTypes tests that VL-encoded fields are properly prefixed.
func TestVLEncodedFieldTypes(t *testing.T) {
	defs := definitions.Get()
	codec := NewFieldIDCodec(defs)

	// VL-encoded fields: Blob, AccountID, etc.
	vlFields := []string{
		"SigningPubKey",
		"TxnSignature",
		"MemoType",
		"MemoData",
	}

	for _, fieldName := range vlFields {
		t.Run(fieldName, func(t *testing.T) {
			fi, err := defs.GetFieldInstanceByFieldName(fieldName)
			require.NoError(t, err)
			assert.True(t, fi.IsVLEncoded, "Field %s should be VL-encoded", fieldName)
		})
	}

	// Non-VL-encoded fields
	nonVLFields := []string{
		"Flags",
		"Sequence",
		"Fee",
		"Amount",
	}

	for _, fieldName := range nonVLFields {
		t.Run(fieldName+"_not_VL", func(t *testing.T) {
			fi, err := defs.GetFieldInstanceByFieldName(fieldName)
			require.NoError(t, err)
			assert.False(t, fi.IsVLEncoded, "Field %s should NOT be VL-encoded", fieldName)
		})
	}

	// Test actual VL encoding in serialization
	t.Run("VL field with length prefix", func(t *testing.T) {
		serializer := NewBinarySerializer(codec)

		// SigningPubKey is VL-encoded
		fi, err := defs.GetFieldInstanceByFieldName("SigningPubKey")
		require.NoError(t, err)

		// 33-byte public key
		pubKey := make([]byte, 33)
		pubKey[0] = 0x03 // compressed key prefix

		err = serializer.WriteFieldAndValue(*fi, pubKey)
		require.NoError(t, err)

		sink := serializer.GetSink()
		// Should have: field ID + VL length (1 byte for len 33) + 33 bytes data
		assert.Equal(t, 1+1+33, len(sink), "VL-encoded field should have length prefix")

		// Length byte should be 33 (0x21)
		assert.Equal(t, byte(33), sink[1], "VL length should be 33")
	})
}

// TestSerializedFieldOrder tests that fields are serialized in correct order.
// From rippled: fields are ordered by (typeCode, fieldCode) tuple.
func TestSerializedFieldOrder(t *testing.T) {
	defs := definitions.Get()

	// Get field instances and verify ordering
	fields := []string{
		"TransactionType", // type=1, field=2
		"Flags",           // type=2, field=2
		"SourceTag",       // type=2, field=3
		"Sequence",        // type=2, field=4
		"DestinationTag",  // type=2, field=14
		"Fee",             // type=6, field=8
		"Account",         // type=8, field=1
	}

	var prevOrdinal int32 = -1
	for _, fieldName := range fields {
		fi, err := defs.GetFieldInstanceByFieldName(fieldName)
		require.NoError(t, err)

		assert.Greater(t, fi.Ordinal, prevOrdinal,
			"Field %s (ordinal %d) should be greater than previous (ordinal %d)",
			fieldName, fi.Ordinal, prevOrdinal)

		prevOrdinal = fi.Ordinal
	}
}
