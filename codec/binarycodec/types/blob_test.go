package types

import (
	"bytes"
	"encoding/hex"
	"testing"

	"github.com/LeJamon/go-xrpl/codec/binarycodec/serdes"
)

func TestBlob_FromJson(t *testing.T) {
	tt := []struct {
		name        string
		input       string
		expected    []byte
		expectedErr error
	}{
		{
			name:        "Valid Blob",
			input:       "00",
			expected:    []byte{0},
			expectedErr: nil,
		},
		{
			name:        "Valid Blob",
			input:       "000102030405060708090A0B0C0D0E0F",
			expected:    []byte{0x00, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f},
			expectedErr: nil,
		},
		{
			name:        "Invalid hex string",
			input:       "000102030405060708090A0B0C0D0E0G",
			expected:    nil,
			expectedErr: hex.InvalidByteError('G'),
		},
	}

	for _, tc := range tt {
		t.Run(tc.name, func(t *testing.T) {
			blob := &Blob{}
			actual, err := blob.FromJSON(tc.input)
			if err != tc.expectedErr {
				t.Errorf("Expected error %v, got %v", tc.expectedErr, err)
			}
			if !bytes.Equal(actual, tc.expected) {
				t.Errorf("Expected %v, got %v", tc.expected, actual)
			}
		})
	}
}

func TestBlob_ToJson(t *testing.T) {
	tt := []struct {
		name        string
		input       []byte
		expected    any
		opts        []int
		expectedErr error
	}{
		{
			name:        "Valid Blob",
			input:       []byte{0x00, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f},
			expected:    "000102030405060708090A0B0C0D0E0F",
			opts:        []int{16},
			expectedErr: nil,
		},
		{
			name:        "ReadBytes error - truncated data",
			input:       []byte{0x00, 0x01},
			expected:    nil,
			opts:        []int{16},
			expectedErr: serdes.ErrParserOutOfBound,
		},
		{
			name:        "No length prefix",
			input:       []byte{0x00, 0x01, 0x02, 0x03},
			expected:    nil,
			opts:        nil,
			expectedErr: ErrNoLengthPrefix,
		},
	}

	for _, tc := range tt {
		t.Run(tc.name, func(t *testing.T) {
			blob := &Blob{}
			actual, err := blob.ToJSON(testParser(tc.input), tc.opts...)
			if err != nil && err.Error() != tc.expectedErr.Error() {
				t.Errorf("Expected error %v, got %v", tc.expectedErr, err)
			}
			if actual != tc.expected {
				t.Errorf("Expected %v, got %v", tc.expected, actual)
			}
		})
	}
}
