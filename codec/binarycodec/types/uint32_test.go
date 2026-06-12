package types

import (
	"bytes"
	"testing"

	"github.com/LeJamon/go-xrpl/codec/binarycodec/definitions"
	"github.com/LeJamon/go-xrpl/codec/binarycodec/serdes"
	"github.com/stretchr/testify/require"
)

func TestUint32_FromJson(t *testing.T) {
	tt := []struct {
		name        string
		input       any
		expected    []byte
		expectedErr error
	}{
		{
			name:        "Valid uint32",
			input:       uint32(1),
			expected:    []byte{0, 0, 0, 1},
			expectedErr: nil,
		},
		{
			name:        "Valid uint32 (2)",
			input:       uint32(100),
			expected:    []byte{0, 0, 0, 100},
			expectedErr: nil,
		},
		{
			name:        "Valid uint32 (3)",
			input:       uint32(255),
			expected:    []byte{0, 0, 0, 255},
			expectedErr: nil,
		},
		// TODO: Add test for overflow case
	}

	for _, tc := range tt {
		t.Run(tc.name, func(t *testing.T) {
			uint32 := &UInt32{}
			actual, err := uint32.FromJSON(tc.input)
			if err != tc.expectedErr {
				t.Errorf("Expected error %v, got %v", tc.expectedErr, err)
			}
			if !bytes.Equal(actual, tc.expected) {
				t.Errorf("Expected %v, got %v", tc.expected, actual)
			}
		})
	}
}

func TestUint32_ToJson(t *testing.T) {
	defs := definitions.Get()

	tt := []struct {
		name        string
		input       []byte
		malleate    func(t *testing.T) *serdes.BinaryParser
		expected    uint32
		expectedErr error
	}{
		{
			name:  "fail - not enough data",
			input: []byte{0, 0},
			malleate: func(t *testing.T) *serdes.BinaryParser {
				return serdes.NewBinaryParser([]byte{0, 0}, defs)
			},
			expected:    0,
			expectedErr: serdes.ErrParserOutOfBound,
		},
		{
			name:  "pass - valid uint32",
			input: []byte{0, 0, 0, 1},
			malleate: func(t *testing.T) *serdes.BinaryParser {
				return serdes.NewBinaryParser([]byte{0, 0, 0, 1}, defs)
			},
			expected:    1,
			expectedErr: nil,
		},
		{
			name:  "pass - valid uint32 (2)",
			input: []byte{0, 0, 0, 100},
			malleate: func(t *testing.T) *serdes.BinaryParser {
				return serdes.NewBinaryParser([]byte{0, 0, 0, 100}, defs)
			},
			expected:    100,
			expectedErr: nil,
		},
		{
			name:  "pass - valid uint32 (3)",
			input: []byte{0, 0, 0, 255},
			malleate: func(t *testing.T) *serdes.BinaryParser {
				return serdes.NewBinaryParser([]byte{0, 0, 0, 255}, defs)
			},
			expected:    255,
			expectedErr: nil,
		},
	}

	for _, tc := range tt {
		t.Run(tc.name, func(t *testing.T) {
			class := &UInt32{}
			parser := tc.malleate(t)
			actual, err := class.ToJSON(parser)
			if tc.expectedErr != nil {
				require.EqualError(t, err, tc.expectedErr.Error())
			} else {
				require.Equal(t, tc.expected, actual)
			}
		})
	}
}
