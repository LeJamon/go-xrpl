package types

import (
	"errors"
	"testing"

	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
	"github.com/LeJamon/go-xrpl/codec/binarycodec/serdes"
	"github.com/stretchr/testify/require"
)

func TestAccountID_FromJson(t *testing.T) {
	tt := []struct {
		name        string
		input       any
		expected    []byte
		expectedErr error
	}{
		{
			name:  "Valid AccountID",
			input: "r3e7qTG44Mg8pHXgxPtyRx286Re5Urtx2p",
			expected: []byte{
				83, 223, 129, 195, 127, 70,
				21, 146, 66, 247, 202, 145,
				99, 224, 159, 4, 64, 41,
				204, 18,
			},
			expectedErr: nil,
		},
		{
			name:        "Invalid AccountID",
			input:       "r3e7qTG44Mg8pHXgxPtyRx286Re5Urtx2p2",
			expected:    nil,
			expectedErr: addresscodec.ErrInvalidClassicAddress,
		},
		{
			name:  "Valid AccountID with XAddress",
			input: "XVYRdEocC28DRx94ZFGP3qNJ1D5Ln7ecXFMd3vREB5Pesju", // rLJ9FwQ3opJZBMsTjhqhHrbhRNALqAQJ5U
			expected: []byte{
				211, 168, 209, 109, 176,
				55, 12, 60, 93, 57, 103,
				89, 62, 51, 191, 128,
				222, 149, 106, 66},
			expectedErr: nil,
		},
		{
			name:  "Valid AccountID with XAddress and tag",
			input: "XVYRdEocC28DRx94ZFGP3qNJ1D5Ln7kXKTG5X57UCKzEwYx", // rLJ9FwQ3opJZBMsTjhqhHrbhRNALqAQJ5U:12345
			expected: []byte{
				211, 168, 209, 109, 176,
				55, 12, 60, 93, 57, 103,
				89, 62, 51, 191, 128,
				222, 149, 106, 66},
			expectedErr: nil,
		},
		{
			name:        "Invalid AccountID with invalid XAddress",
			input:       "XVYRdEocC28DRx94ZFGP3qNJ1D5Ln7ecXFMd3vREB5PesjuA",
			expected:    nil,
			expectedErr: addresscodec.ErrChecksum,
		},
		{
			name:        "Invalid XRPL address",
			input:       "abcde",
			expected:    nil,
			expectedErr: addresscodec.ErrInvalidAddressFormat,
		},
		{
			name:        "Invalid input type",
			input:       1, // should be a string
			expected:    nil,
			expectedErr: errors.New("expected a string but got int"),
		},
	}

	for _, tc := range tt {
		t.Run(tc.name, func(t *testing.T) {
			accountID := &AccountID{}
			actual, err := accountID.FromJSON(tc.input)
			require.Equal(t, tc.expected, actual)
			require.Equal(t, tc.expectedErr, err)
		})
	}
}

func TestAccountID_ToJson(t *testing.T) {
	tt := []struct {
		name     string
		input    []byte
		expected string
		opts     []int
		err      error
	}{
		{
			name: "Valid AccountID",
			input: []byte{
				83, 223, 129, 195, 127, 70,
				21, 146, 66, 247, 202, 145,
				99, 224, 159, 4, 64, 41,
				204, 18,
			},
			expected: "r3e7qTG44Mg8pHXgxPtyRx286Re5Urtx2p",
			opts:     []int{20},
			err:      nil,
		},
		{
			name:     "No length prefix",
			input:    []byte{},
			expected: "",
			opts:     nil,
			err:      ErrNoLengthPrefix,
		},
		{
			name:     "ReadBytes error - truncated data",
			input:    []byte{83, 223, 129},
			expected: "",
			opts:     []int{20},
			err:      serdes.ErrParserOutOfBound,
		},
	}

	for _, tc := range tt {
		t.Run(tc.name, func(t *testing.T) {
			accountID := &AccountID{}
			actual, err := accountID.ToJSON(testParser(tc.input), tc.opts...)

			if tc.err != nil {
				require.Error(t, err)
				require.Equal(t, tc.err, err)
			} else {
				require.NoError(t, err)
				require.Equal(t, tc.expected, actual)
			}
		})
	}
}
