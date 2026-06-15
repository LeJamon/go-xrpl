package types

import (
	"bytes"
	"reflect"
	"testing"

	"github.com/LeJamon/go-xrpl/codec/binarycodec/definitions"
	"github.com/LeJamon/go-xrpl/codec/binarycodec/serdes"
)

func TestUint8_FromJson(t *testing.T) {
	tt := []struct {
		name        string
		input       any
		expected    []byte
		expectedErr error
	}{
		{
			name:        "Valid uint8",
			input:       1,
			expected:    []byte{1},
			expectedErr: nil,
		},
		{
			name:        "Valid uint8 (2)",
			input:       100,
			expected:    []byte{100},
			expectedErr: nil,
		},
		{
			name:        "Valid uint8 from int32",
			input:       int32(255),
			expected:    []byte{255},
			expectedErr: nil,
		},
		{
			name:        "Valid uint8 from string",
			input:       "tesSUCCESS",
			expected:    []byte{0},
			expectedErr: nil,
		},
		{
			name:     "Invalid uint8 from string",
			input:    "InvalidUint8",
			expected: nil,
			expectedErr: &definitions.NotFoundError{
				Instance: "TransactionResultName",
				Input:    "InvalidUint8",
			},
		},
		// TODO: Add test for overflow case
	}

	for _, tc := range tt {
		t.Run(tc.name, func(t *testing.T) {
			class := &UInt8{}
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

func TestUint8_ToJson(t *testing.T) {
	tt := []struct {
		name        string
		input       []byte
		expected    any
		expectedErr error
	}{
		{
			name:        "Valid uint8",
			input:       []byte{1},
			expected:    1,
			expectedErr: nil,
		},
		{
			name:        "Valid uint8 (2)",
			input:       []byte{100},
			expected:    100,
			expectedErr: nil,
		},
		{
			name:        "Valid uint8 (3)",
			input:       []byte{255},
			expected:    255,
			expectedErr: nil,
		},
		{
			name:        "Invalid uint8 - no data",
			input:       []byte{},
			expected:    nil,
			expectedErr: serdes.ErrParserOutOfBound,
		},
	}

	for _, tc := range tt {
		t.Run(tc.name, func(t *testing.T) {
			class := &UInt8{}
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
