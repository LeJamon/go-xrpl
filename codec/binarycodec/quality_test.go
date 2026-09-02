package binarycodec

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestQualityCodec_Encode(t *testing.T) {
	testcases := []struct {
		name        string
		input       string
		expected    string
		expectedErr error
	}{
		{
			name:        "fail - invalid quality - empty string",
			input:       "",
			expectedErr: ErrInvalidQuality,
		},
		{
			name:        "fail - invalid quality - invalid character",
			input:       "invalid",
			expectedErr: ErrInvalidQuality,
		},
		{
			name:        "fail - invalid quality - leading zero",
			input:       "0195796912.5171664",
			expectedErr: ErrInvalidQuality,
		},
		{
			name:        "fail - invalid quality - embedded number",
			input:       "junk195796912junk",
			expectedErr: ErrInvalidQuality,
		},
		{
			name:        "fail - invalid quality - overflow",
			input:       "195796912.51716641",
			expectedErr: ErrInvalidQuality,
		},
		{
			name:        "fail - invalid quality - overflow",
			input:       "1195796912.5171664",
			expectedErr: ErrInvalidQuality,
		},
		{
			name:     "pass - valid zero quality",
			input:    "0",
			expected: "5500000000000000",
		},
		{
			name:     "pass - valid quality with decimal",
			input:    "0.0",
			expected: "5500000000000000",
		},
		{
			name:     "pass - valid quality with decimal - leading dot",
			input:    "0.",
			expected: "5500000000000000",
		},
		{
			name:     "pass - valid quality with decimal - trailing dot",
			input:    ".0",
			expected: "5500000000000000",
		},
		{
			name:     "pass - signed scientific zero",
			input:    "-0e80",
			expected: "5500000000000000",
		},
		{
			name:     "pass - valid negative quality",
			input:    "-195796912.5171664",
			expected: "5D06F4C3362FE1D0",
		},
		{
			name:     "pass - valid quality - non decimal",
			input:    "195796912",
			expected: "640000000BAB9FB0",
		},
		{
			name:     "pass - valid quality",
			input:    "195796912.5171664",
			expected: "5D06F4C3362FE1D0",
		},
		{
			name:     "pass - explicit positive sign",
			input:    "+195796912.5171664",
			expected: "5D06F4C3362FE1D0",
		},
		{
			name:     "pass - minimum exponent",
			input:    "1e-96",
			expected: "0400000000000001",
		},
		{
			name:     "pass - maximum exponent",
			input:    "1e80",
			expected: "B400000000000001",
		},
		{
			name:        "fail - exponent below minimum",
			input:       "1e-97",
			expectedErr: ErrInvalidQuality,
		},
		{
			name:        "fail - exponent above maximum",
			input:       "1e81",
			expectedErr: ErrInvalidQuality,
		},
	}
	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			encoded, err := EncodeQuality(tc.input)
			if tc.expectedErr != nil {
				require.ErrorIs(t, err, tc.expectedErr)
			} else {
				require.NoError(t, err)
				require.Equal(t, tc.expected, encoded)
			}
		})
	}
}

func TestQualityCodec_Decode(t *testing.T) {
	testcases := []struct {
		name        string
		input       string
		expected    string
		expectedErr error
	}{
		{
			name:        "fail - invalid quality - empty string",
			input:       "",
			expectedErr: ErrInvalidQuality,
		},
		{
			name:     "pass - valid zero quality",
			input:    "5500000000000000",
			expected: "0",
		},
		{
			name:     "pass - valid quality",
			input:    "5D06F4C3362FE1D0",
			expected: "195796912.5171664",
		},
		{
			name:     "pass - valid quality - non decimal",
			input:    "640000000BAB9FB0",
			expected: "195796912",
		},
	}
	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			decoded, err := DecodeQuality(tc.input)
			if tc.expectedErr != nil {
				require.Error(t, err)
				require.Equal(t, tc.expectedErr, err)
			} else {
				require.NoError(t, err)
				require.Equal(t, tc.expected, decoded)
			}
		})
	}
}

func BenchmarkEncodeQuality(b *testing.B) {
	for b.Loop() {
		if _, err := EncodeQuality("195796912.5171664"); err != nil {
			b.Fatal(err)
		}
	}
}
