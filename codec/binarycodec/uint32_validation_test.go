package binarycodec_test

import (
	"math"
	"testing"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/stretchr/testify/require"
)

func TestEncodeBytesValidatesUInt32Values(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		value    any
		expected []byte
	}{
		{name: "zero", value: uint32(0), expected: []byte{0x24, 0, 0, 0, 0}},
		{name: "maximum", value: uint64(math.MaxUint32), expected: []byte{0x24, 255, 255, 255, 255}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			actual, err := binarycodec.EncodeBytes(map[string]any{"Sequence": tc.value})
			require.NoError(t, err)
			require.Equal(t, tc.expected, actual)
		})
	}

	for _, tc := range []struct {
		name  string
		value any
	}{
		{name: "negative", value: int64(-1)},
		{name: "overflow", value: uint64(math.MaxUint32) + 1},
		{name: "fractional", value: float64(1.5)},
		{name: "NaN", value: math.NaN()},
		{name: "positive infinity", value: math.Inf(1)},
		{name: "negative infinity", value: math.Inf(-1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			actual, err := binarycodec.EncodeBytes(map[string]any{"Sequence": tc.value})
			require.Error(t, err)
			require.Nil(t, actual)
		})
	}
}
