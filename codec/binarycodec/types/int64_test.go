package types

import (
	"errors"
	"testing"

	"github.com/LeJamon/goXRPLd/codec/binarycodec/definitions"
	"github.com/LeJamon/goXRPLd/codec/binarycodec/serdes"
	"github.com/LeJamon/goXRPLd/codec/binarycodec/types/interfaces"
	"github.com/LeJamon/goXRPLd/codec/binarycodec/types/testutil"
	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/require"
)

func TestInt64_FromJson(t *testing.T) {
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
			expectedErr: ErrInvalidInt64,
		},
		{
			name:        "fail - invalid numeric string",
			input:       "invalid",
			expected:    nil,
			expectedErr: ErrInvalidInt64,
		},
		{
			name:        "pass - valid decimal string",
			input:       "256",
			expected:    []byte{0, 0, 0, 0, 0, 0, 1, 0},
			expectedErr: nil,
		},
		{
			name:        "pass - negative decimal string",
			input:       "-1",
			expected:    []byte{255, 255, 255, 255, 255, 255, 255, 255},
			expectedErr: nil,
		},
		{
			name:        "pass - int value",
			input:       int(1),
			expected:    []byte{0, 0, 0, 0, 0, 0, 0, 1},
			expectedErr: nil,
		},
		{
			name:        "pass - int64 value",
			input:       int64(256),
			expected:    []byte{0, 0, 0, 0, 0, 0, 1, 0},
			expectedErr: nil,
		},
		{
			name:        "pass - float64 value (from JSON unmarshal)",
			input:       float64(740),
			expected:    []byte{0, 0, 0, 0, 0, 0, 0x02, 0xE4},
			expectedErr: nil,
		},
		{
			name:        "pass - float64 at positive bound (2^32-1)",
			input:       float64(4294967295),
			expected:    []byte{0, 0, 0, 0, 0xFF, 0xFF, 0xFF, 0xFF},
			expectedErr: nil,
		},
		{
			name:        "pass - float64 at negative bound (-2^31)",
			input:       float64(-2147483648),
			expected:    []byte{0xFF, 0xFF, 0xFF, 0xFF, 0x80, 0, 0, 0},
			expectedErr: nil,
		},
		{
			name:        "fail - float64 just above positive bound (2^32)",
			input:       float64(4294967296),
			expected:    nil,
			expectedErr: ErrInvalidInt64,
		},
		{
			name:        "fail - float64 just below negative bound (-2^31-1)",
			input:       float64(-2147483649),
			expected:    nil,
			expectedErr: ErrInvalidInt64,
		},
		{
			name:        "fail - float64 far above range loses precision",
			input:       float64(1e18),
			expected:    nil,
			expectedErr: ErrInvalidInt64,
		},
		{
			name:        "fail - float64 far below range loses precision",
			input:       float64(-1e18),
			expected:    nil,
			expectedErr: ErrInvalidInt64,
		},
		{
			name:        "fail - non-integral float64",
			input:       float64(1.5),
			expected:    nil,
			expectedErr: ErrInvalidInt64,
		},
	}

	for _, tc := range tt {
		t.Run(tc.name, func(t *testing.T) {
			class := &Int64Type{}
			actual, err := class.FromJSON(tc.input)
			if tc.expectedErr != nil {
				require.EqualError(t, err, tc.expectedErr.Error())
			} else {
				require.Equal(t, tc.expected, actual)
			}
		})
	}
}

func TestInt64_ToJson(t *testing.T) {
	defs := definitions.Get()

	tt := []struct {
		name        string
		malleate    func(t *testing.T) interfaces.BinaryParser
		expected    string
		expectedErr error
	}{
		{
			name: "fail - binary parser has no data",
			malleate: func(t *testing.T) interfaces.BinaryParser {
				parserMock := testutil.NewMockBinaryParser(gomock.NewController(t))
				parserMock.EXPECT().ReadBytes(gomock.Any()).Return([]byte{}, errors.New("binary parser has no data"))
				return parserMock
			},
			expected:    "",
			expectedErr: errors.New("binary parser has no data"),
		},
		{
			name: "pass - valid int64",
			malleate: func(t *testing.T) interfaces.BinaryParser {
				return serdes.NewBinaryParser([]byte{0, 0, 0, 0, 0, 0, 1, 0}, defs)
			},
			expected:    "256",
			expectedErr: nil,
		},
		{
			name: "pass - negative int64",
			malleate: func(t *testing.T) interfaces.BinaryParser {
				return serdes.NewBinaryParser([]byte{255, 255, 255, 255, 255, 255, 255, 255}, defs)
			},
			expected:    "-1",
			expectedErr: nil,
		},
	}

	for _, tc := range tt {
		t.Run(tc.name, func(t *testing.T) {
			class := &Int64Type{}
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
