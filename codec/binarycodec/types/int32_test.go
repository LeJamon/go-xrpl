package types

import (
	"errors"
	"math"
	"testing"

	"github.com/LeJamon/go-xrpl/codec/binarycodec/definitions"
	"github.com/LeJamon/go-xrpl/codec/binarycodec/serdes"
	"github.com/LeJamon/go-xrpl/codec/binarycodec/types/interfaces"
	"github.com/LeJamon/go-xrpl/codec/binarycodec/types/testutil"
	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/require"
)

func TestInt32_FromJson(t *testing.T) {
	tt := []struct {
		name        string
		input       any
		expected    []byte
		expectedErr error
	}{
		{
			name:        "fail - value is unsupported type",
			input:       true,
			expected:    nil,
			expectedErr: ErrInvalidInt32,
		},
		{
			name:        "pass - int value",
			input:       int(1),
			expected:    []byte{0, 0, 0, 1},
			expectedErr: nil,
		},
		{
			name:        "pass - int32 value",
			input:       int32(256),
			expected:    []byte{0, 0, 1, 0},
			expectedErr: nil,
		},
		{
			name:        "pass - negative int value",
			input:       int(-1),
			expected:    []byte{0xFF, 0xFF, 0xFF, 0xFF},
			expectedErr: nil,
		},
		{
			name:        "pass - int64 value",
			input:       int64(740),
			expected:    []byte{0, 0, 0x02, 0xE4},
			expectedErr: nil,
		},
		{
			name:        "pass - float64 value (from JSON unmarshal)",
			input:       float64(740),
			expected:    []byte{0, 0, 0x02, 0xE4},
			expectedErr: nil,
		},
		{
			name:        "pass - max int32",
			input:       int(math.MaxInt32),
			expected:    []byte{0x7F, 0xFF, 0xFF, 0xFF},
			expectedErr: nil,
		},
		{
			name:        "pass - min int32",
			input:       int(math.MinInt32),
			expected:    []byte{0x80, 0, 0, 0},
			expectedErr: nil,
		},
		{
			name:        "pass - float64 at min int32",
			input:       float64(math.MinInt32),
			expected:    []byte{0x80, 0, 0, 0},
			expectedErr: nil,
		},
		{
			name:        "fail - int64 above int32 range",
			input:       int64(math.MaxInt32) + 1,
			expected:    nil,
			expectedErr: ErrInvalidInt32,
		},
		{
			name:        "fail - int64 below int32 range",
			input:       int64(math.MinInt32) - 1,
			expected:    nil,
			expectedErr: ErrInvalidInt32,
		},
		{
			name:        "fail - float64 above int32 range",
			input:       float64(math.MaxInt32) + 1,
			expected:    nil,
			expectedErr: ErrInvalidInt32,
		},
		{
			name:        "fail - float64 below int32 range",
			input:       float64(math.MinInt32) - 1,
			expected:    nil,
			expectedErr: ErrInvalidInt32,
		},
		{
			name:        "fail - non-integral float64",
			input:       float64(1.5),
			expected:    nil,
			expectedErr: ErrInvalidInt32,
		},
	}

	for _, tc := range tt {
		t.Run(tc.name, func(t *testing.T) {
			class := &Int32{}
			actual, err := class.FromJSON(tc.input)
			if tc.expectedErr != nil {
				require.EqualError(t, err, tc.expectedErr.Error())
			} else {
				require.Equal(t, tc.expected, actual)
			}
		})
	}
}

func TestInt32_ToJson(t *testing.T) {
	defs := definitions.Get()

	tt := []struct {
		name        string
		malleate    func(t *testing.T) interfaces.BinaryParser
		expected    int
		expectedErr error
	}{
		{
			name: "fail - binary parser has no data",
			malleate: func(t *testing.T) interfaces.BinaryParser {
				parserMock := testutil.NewMockBinaryParser(gomock.NewController(t))
				parserMock.EXPECT().ReadBytes(gomock.Any()).Return([]byte{}, errors.New("binary parser has no data"))
				return parserMock
			},
			expected:    0,
			expectedErr: errors.New("binary parser has no data"),
		},
		{
			name: "pass - valid int32",
			malleate: func(t *testing.T) interfaces.BinaryParser {
				return serdes.NewBinaryParser([]byte{0, 0, 1, 0}, defs)
			},
			expected:    256,
			expectedErr: nil,
		},
		{
			name: "pass - negative int32",
			malleate: func(t *testing.T) interfaces.BinaryParser {
				return serdes.NewBinaryParser([]byte{0xFF, 0xFF, 0xFF, 0xFF}, defs)
			},
			expected:    -1,
			expectedErr: nil,
		},
		{
			name: "pass - min int32",
			malleate: func(t *testing.T) interfaces.BinaryParser {
				return serdes.NewBinaryParser([]byte{0x80, 0, 0, 0}, defs)
			},
			expected:    math.MinInt32,
			expectedErr: nil,
		},
	}

	for _, tc := range tt {
		t.Run(tc.name, func(t *testing.T) {
			class := &Int32{}
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

// FuzzInt32RoundTrip checks that every int32 value survives FromJSON → ToJSON
// unchanged, covering negatives and the min/max boundaries (issue #275 task).
func FuzzInt32RoundTrip(f *testing.F) {
	for _, v := range []int32{0, 1, -1, 256, -256, 740, -740, math.MaxInt32, math.MinInt32} {
		f.Add(v)
	}

	f.Fuzz(func(t *testing.T, v int32) {
		class := &Int32{}
		b, err := class.FromJSON(int(v))
		if err != nil {
			t.Fatalf("FromJSON(%d): %v", v, err)
		}
		if len(b) != 4 {
			t.Fatalf("FromJSON(%d) returned %d bytes, want 4", v, len(b))
		}

		js, err := class.ToJSON(serdes.NewBinaryParser(b, definitions.Get()))
		if err != nil {
			t.Fatalf("ToJSON after FromJSON(%d): %v", v, err)
		}
		got, ok := js.(int)
		if !ok {
			t.Fatalf("ToJSON returned %T, want int", js)
		}
		if int32(got) != v {
			t.Fatalf("round-trip not stable: %d -> %x -> %d", v, b, got)
		}
	})
}
