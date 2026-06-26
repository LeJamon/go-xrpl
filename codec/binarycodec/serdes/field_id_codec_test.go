package serdes

import (
	"testing"

	"github.com/LeJamon/go-xrpl/codec/binarycodec/definitions"
	"github.com/stretchr/testify/require"
)

func TestFieldIDCodec_Encode(t *testing.T) {
	tt := []struct {
		description string
		input       string
		expected    []byte
		expectedErr error
	}{
		{
			description: "Type Code and Field Code < 16",
			input:       "Sequence",
			expected:    []byte{36},
			expectedErr: nil,
		},
		{
			description: "Additional Type Code and Field Code < 16",
			input:       "Flags",
			expected:    []byte{34},
			expectedErr: nil,
		},
		{
			description: "Additional Type Code and Field Code < 16",
			input:       "DestinationTag",
			expected:    []byte{46},
			expectedErr: nil,
		},
		{
			description: "Type Code >= 16 and Field Code < 16",
			input:       "Paths",
			expected:    []byte{1, 18},
			expectedErr: nil,
		},
		{
			description: "Additional Type Code >= 16 and Field Code < 16",
			input:       "CloseResolution",
			expected:    []byte{1, 16},
			expectedErr: nil,
		},
		{
			description: "Type Code < 16 and Field Code >= 16",
			input:       "SetFlag",
			expected:    []byte{32, 33},
			expectedErr: nil,
		},
		{
			description: "Additional Type Code < 16 and Field Code >= 16",
			input:       "Nickname",
			expected:    []byte{80, 18},
			expectedErr: nil,
		},
		{
			description: "Type Code and Field Code >= 16",
			input:       "TickSize",
			expected:    []byte{0, 16, 16},
			expectedErr: nil,
		},
		{
			description: "Additional Type Code and Field Code >= 16",
			input:       "UNLModifyDisabling",
			expected:    []byte{0, 16, 17},
			expectedErr: nil,
		},
		{
			description: "Non existent field name",
			input:       "yurt",
			expected:    nil,
			expectedErr: &definitions.NotFoundError{Instance: "FieldName", Input: "yurt"},
		},
	}

	for _, tc := range tt {
		t.Run(tc.description, func(t *testing.T) {
			got, err := NewFieldIDCodec(definitions.Get()).Encode(tc.input)

			if tc.expectedErr != nil {
				require.EqualError(t, err, tc.expectedErr.Error())
				require.Nil(t, got)
			} else {
				require.NoError(t, err)
				require.Equal(t, tc.expected, got)
			}
		})
	}
}
