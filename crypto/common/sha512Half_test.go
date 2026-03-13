package common

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSha512Half(t *testing.T) {
	tt := []struct {
		description string
		input       []byte
		expected    [32]uint8
	}{
		{
			description: "hash of fakeRandomString",
			input:       []byte{102, 97, 107, 101, 82, 97, 110, 100, 111, 109, 83, 116, 114, 105, 110, 103},
			expected:    [32]uint8{0xbb, 0x3e, 0xca, 0x89, 0x85, 0xe1, 0x48, 0x4f, 0xa6, 0xa2, 0x8c, 0x4b, 0x30, 0xfb, 0x0, 0x42, 0xa2, 0xcc, 0x5d, 0xf3, 0xec, 0x8d, 0xc3, 0x7b, 0x5f, 0x3d, 0x12, 0x6d, 0xdf, 0xd3, 0xca, 0x14},
		},
	}

	for _, tc := range tt {
		t.Run(tc.description, func(t *testing.T) {
			got := Sha512Half(tc.input)
			require.Equal(t, tc.expected, got)
		})
	}
}
