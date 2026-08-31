package types

import (
	"math"
	"strconv"
	"testing"

	"github.com/LeJamon/go-xrpl/codec/binarycodec/definitions"
	"github.com/LeJamon/go-xrpl/codec/binarycodec/serdes"
	"github.com/stretchr/testify/require"
)

func TestUInt32FromJSON(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    any
		expected []byte
	}{
		{name: "uint32 zero", input: uint32(0), expected: []byte{0, 0, 0, 0}},
		{name: "uint32 maximum", input: uint32(math.MaxUint32), expected: []byte{255, 255, 255, 255}},
		{name: "int", input: int(1), expected: []byte{0, 0, 0, 1}},
		{name: "int64", input: int64(100), expected: []byte{0, 0, 0, 100}},
		{name: "uint64 maximum", input: uint64(math.MaxUint32), expected: []byte{255, 255, 255, 255}},
		{name: "float64 maximum", input: float64(math.MaxUint32), expected: []byte{255, 255, 255, 255}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			actual, err := (&UInt32{}).FromJSON(tc.input)
			require.NoError(t, err)
			require.Equal(t, tc.expected, actual)
		})
	}
}

func TestUInt32FromJSONRejectsInvalidNumbers(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name  string
		input any
	}
	tests := []testCase{
		{name: "negative int", input: int(-1)},
		{name: "negative int64", input: int64(-1)},
		{name: "overflowing int64", input: int64(math.MaxUint32) + 1},
		{name: "overflowing uint64", input: uint64(math.MaxUint32) + 1},
		{name: "negative float64", input: float64(-1)},
		{name: "overflowing float64", input: float64(math.MaxUint32) + 1},
		{name: "fractional float64", input: float64(1.5)},
		{name: "NaN", input: math.NaN()},
		{name: "positive infinity", input: math.Inf(1)},
		{name: "negative infinity", input: math.Inf(-1)},
	}
	if strconv.IntSize == 64 {
		tests = append(tests, testCase{name: "overflowing int", input: int(uint64(math.MaxUint32) + 1)})
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			actual, err := (&UInt32{}).FromJSON(tc.input)
			require.Error(t, err)
			require.Nil(t, actual)
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
