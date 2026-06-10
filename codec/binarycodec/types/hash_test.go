package types

import (
	"encoding/hex"
	"testing"

	"github.com/LeJamon/go-xrpl/codec/binarycodec/serdes"
	"github.com/stretchr/testify/require"
)

func TestHash_FromJson(t *testing.T) {
	tt := []struct {
		name        string
		json        any
		length      int
		expected    []byte
		expectedErr error
	}{
		{
			name:        "Valid hash of length 32",
			json:        "0316020000000000000000000000000000000000000000000000000000000000",
			length:      32,
			expected:    []byte{0x03, 0x16, 0x02, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00},
			expectedErr: nil,
		},
		{
			name:        "Invalid hex string",
			json:        "031G020000000000000000000000000000000000000000000000000000000000",
			length:      32,
			expected:    nil,
			expectedErr: &ErrInvalidHexString{Err: hex.InvalidByteError('G')},
		},
		{
			name:        "Invalid hash type",
			json:        123,
			length:      32,
			expectedErr: &ErrInvalidHashType{},
		},
		{
			name:        "Invalid hash length",
			json:        "031602000000000000000000000000000000000000000000000000000000000000",
			length:      32,
			expectedErr: &ErrInvalidHashLength{Expected: 32},
		},
	}

	for _, tc := range tt {
		t.Run(tc.name, func(t *testing.T) {
			actual, err := hashFromJSON(tc.json, tc.length)
			require.Equal(t, tc.expected, actual)
			if tc.expectedErr != nil {
				require.Equal(t, tc.expectedErr.Error(), err.Error())
			}
		})
	}
}

func TestHash_ToJson(t *testing.T) {
	tt := []struct {
		name        string
		input       []byte
		length      int
		expected    any
		expectedErr error
	}{
		{
			name:     "Valid hash of length 32",
			input:    []byte{0x03, 0x16, 0x02, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00},
			length:   32,
			expected: "0316020000000000000000000000000000000000000000000000000000000000",
		},
		{
			name:        "ReadBytes error - truncated data",
			input:       []byte{0x03, 0x16},
			length:      32,
			expected:    nil,
			expectedErr: serdes.ErrParserOutOfBound,
		},
	}

	for _, tc := range tt {
		t.Run(tc.name, func(t *testing.T) {
			actual, err := hashToJSON(testParser(tc.input), tc.length)
			require.Equal(t, tc.expectedErr, err)
			if tc.expectedErr == nil {
				require.Equal(t, tc.expected, actual)
			}
		})
	}
}

// TestHash_ZeroValueSafe guards the zero-value safety of the concrete Hash
// types: a Hash256{} (no constructor) must be fully usable.
func TestHash_ZeroValueSafe(t *testing.T) {
	var h Hash256
	b, err := h.FromJSON("0316020000000000000000000000000000000000000000000000000000000000")
	require.NoError(t, err)
	require.Len(t, b, 32)

	v, err := h.ToJSON(testParser(b))
	require.NoError(t, err)
	require.Equal(t, "0316020000000000000000000000000000000000000000000000000000000000", v)
}
