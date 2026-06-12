package types

import (
	"bytes"
	"reflect"
	"testing"

	"github.com/LeJamon/go-xrpl/codec/binarycodec/definitions"
	"github.com/LeJamon/go-xrpl/codec/binarycodec/serdes"
)

func TestUint16_FromJson(t *testing.T) {
	tt := []struct {
		name        string
		input       any
		expected    []byte
		expectedErr error
	}{
		{
			name:     "fail - invalid ledger entry type",
			input:    "invalid",
			expected: nil,
			expectedErr: &definitions.NotFoundError{
				Instance: "LedgerEntryTypeName",
				Input:    "invalid",
			},
		},
		{
			name:        "pass - valid uint16 from uint16",
			input:       1,
			expected:    []byte{0, 1},
			expectedErr: nil,
		},
		{
			name:        "pass - valid uint16 from uint16 (2)",
			input:       100,
			expected:    []byte{0, 100},
			expectedErr: nil,
		},
		{
			name:        "pass - valid uint16 from uint16 (3)",
			input:       255,
			expected:    []byte{0, 255},
			expectedErr: nil,
		},
		{
			name:        "pass - valid uint16 from TransactionType",
			input:       "Payment",
			expected:    []byte{0, 0},
			expectedErr: nil,
		},
		{
			name:        "pass - valid uint16 from TransactionType (2)",
			input:       "EscrowCreate",
			expected:    []byte{0, 1},
			expectedErr: nil,
		},
		// TODO: Add test for overflow case
	}

	for _, tc := range tt {
		t.Run(tc.name, func(t *testing.T) {
			class := &UInt16{}
			actual, err := class.FromJSON(tc.input)
			if !reflect.DeepEqual(err, tc.expectedErr) {
				t.Errorf("Expected error %v, got %v", tc.expectedErr, err)
			}
			if !bytes.Equal(actual, tc.expected) {
				t.Errorf("Expected %v, got %v", tc.expected, actual)
			}
		})
	}
}

func TestUint16_ToJson(t *testing.T) {
	tt := []struct {
		name        string
		input       []byte
		expected    any
		expectedErr error
	}{
		{
			name:        "Valid uint16",
			input:       []byte{0, 1},
			expected:    1,
			expectedErr: nil,
		},
		{
			name:        "Valid uint16 (2)",
			input:       []byte{0, 100},
			expected:    100,
			expectedErr: nil,
		},
		{
			name:        "Valid uint16 (3)",
			input:       []byte{0, 255},
			expected:    255,
			expectedErr: nil,
		},
		{
			name:        "Invalid ReadBytes - not enough data",
			input:       []byte{1},
			expected:    nil,
			expectedErr: serdes.ErrParserOutOfBound,
		},
	}

	for _, tc := range tt {
		t.Run(tc.name, func(t *testing.T) {
			class := &UInt16{}
			actual, err := class.ToJSON(testParser(tc.input))
			if !reflect.DeepEqual(err, tc.expectedErr) {
				t.Errorf("Expected error %v, got %v", tc.expectedErr, err)
			}
			if actual != tc.expected {
				t.Errorf("Expected %v, got %v", tc.expected, actual)
			}
		})
	}
}
