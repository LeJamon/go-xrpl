package serdes

import (
	"encoding/hex"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/codec/binarycodec/definitions"
	"github.com/stretchr/testify/require"
)

func TestBinarySerializer_EncodeVariableLength(t *testing.T) {
	tt := []struct {
		name        string
		len         int
		expected    []byte
		expectedErr error
	}{
		{
			name:        "length less than 193",
			len:         100,
			expected:    []byte{0x64},
			expectedErr: nil,
		},
		{
			name:        "length more than 193 and less than 12481",
			len:         1000,
			expected:    []byte{0xC4, 0x27},
			expectedErr: nil,
		},
		{
			name:        "length more than 12841 ad less than 918744",
			len:         20000,
			expected:    []byte{0xF1, 0x1D, 0x5F},
			expectedErr: nil,
		},
		{
			name:        "length more than 918744",
			len:         1000000,
			expected:    nil,
			expectedErr: ErrLengthPrefixTooLong,
		},
	}

	for _, tc := range tt {
		t.Run(tc.name, func(t *testing.T) {
			s := strings.Repeat("A2", tc.len)
			b, _ := hex.DecodeString(s)
			require.Equal(t, tc.len, len(b))
			actual, err := encodeVariableLength(len(b))
			if tc.expectedErr != nil {
				require.Error(t, err, tc.expectedErr.Error())
				require.Nil(t, actual)
			} else {
				require.NoError(t, err)
				require.Equal(t, tc.expected, actual)
			}
		})
	}
}

func TestBinarySerializer_Put(t *testing.T) {
	testcases := []struct {
		name     string
		input    []byte
		expected []byte
	}{
		{
			name:     "pass",
			input:    []byte{1, 2, 3, 4, 5},
			expected: []byte{1, 2, 3, 4, 5},
		},
		{
			name:     "pass - empty input",
			input:    []byte{},
			expected: []byte(nil),
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			s := NewBinarySerializer(NewFieldIDCodec(definitions.Get()))
			s.put(tc.input)
			require.Equal(t, tc.expected, s.GetSink())
		})
	}
}

func TestBinarySerializer_GetSink(t *testing.T) {
	testcases := []struct {
		name     string
		input    []byte
		expected []byte
	}{
		{
			name:     "pass",
			input:    []byte{1, 2, 3, 4, 5},
			expected: []byte{1, 2, 3, 4, 5},
		},
		{
			name:     "pass - empty input",
			input:    []byte{},
			expected: []byte(nil),
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			s := NewBinarySerializer(NewFieldIDCodec(definitions.Get()))
			s.put(tc.input)
			require.Equal(t, tc.expected, s.GetSink())
		})
	}
}

func TestBinarySerializer_WriteFieldAndValue(t *testing.T) {
	codec := DefaultFieldIDCodec()
	fieldInstance := func(name string) definitions.FieldInstance {
		fi, err := definitions.Get().GetFieldInstanceByFieldName(name)
		require.NoError(t, err)
		return *fi
	}
	header := func(name string) []byte {
		h, err := codec.Encode(name)
		require.NoError(t, err)
		return h
	}

	testcases := []struct {
		name          string
		fieldInstance definitions.FieldInstance
		value         []byte
		expected      []byte
		expectedErr   error
	}{
		{
			name: "fail - field not found",
			fieldInstance: definitions.FieldInstance{
				FieldName: "NotARealField",
			},
			expectedErr: &definitions.NotFoundError{
				Instance: "FieldName",
				Input:    "NotARealField",
			},
		},
		{
			name:          "fail - vle encoded variable length too long",
			value:         []byte(strings.Repeat("A", 1000000)),
			fieldInstance: fieldInstance("PublicKey"),
			expectedErr:   ErrLengthPrefixTooLong,
		},
		{
			name:          "pass - vle encoded",
			fieldInstance: fieldInstance("PublicKey"),
			value:         []byte{3, 4, 5},
			expected:      append(append([]byte{}, header("PublicKey")...), 3, 3, 4, 5),
			expectedErr:   nil,
		},
		{
			name:          "pass - non-vle encoded",
			fieldInstance: fieldInstance("Flags"),
			value:         []byte{0, 0, 0, 5},
			expected:      append(append([]byte{}, header("Flags")...), 0, 0, 0, 5),
			expectedErr:   nil,
		},
		{
			name:          "pass - type STObject appends end marker",
			fieldInstance: fieldInstance("Majority"),
			value:         []byte{3, 4, 5},
			expected:      append(append([]byte{}, header("Majority")...), 3, 4, 5, 0xE1),
			expectedErr:   nil,
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			s := NewBinarySerializer(codec)
			err := s.WriteFieldAndValue(tc.fieldInstance, tc.value)
			if tc.expectedErr != nil {
				require.Error(t, err, tc.expectedErr.Error())
			} else {
				require.NoError(t, err)
				require.Equal(t, tc.expected, s.GetSink())
			}
		})
	}
}
