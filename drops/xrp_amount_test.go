package drops

import (
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestXRPAmount_String(t *testing.T) {
	require.Equal(t, "123456", NewXRPAmount(123456).String())
	require.Equal(t, "-1", NewXRPAmount(-1).String())
	require.Equal(t, "0", NewXRPAmount(0).String())
}

func TestXRPAmount_JSONClipped(t *testing.T) {
	tests := []struct {
		value int64
		want  int32
	}{
		{math.MinInt64, math.MinInt32},
		{math.MinInt32 - 1, math.MinInt32},
		{math.MinInt32, math.MinInt32},
		{-1, -1},
		{0, 0},
		{math.MaxInt32, math.MaxInt32},
		{math.MaxInt32 + 1, math.MaxInt32},
		{int64(MaxDrops), math.MaxInt32},
	}
	for _, test := range tests {
		require.Equal(t, test.want, XRPAmount(test.value).JSONClipped(), "value %d", test.value)
	}
	negative := XRPAmount(-15)
	wrapped := uint64(negative)
	require.Equal(t, int32(-15), XRPAmount(int64(wrapped)).JSONClipped())
}

// Add/Sub/Mul mirror rippled's plain int64 arithmetic and wrap silently on
// overflow rather than erroring.
func TestXRPAmount_UncheckedWrap(t *testing.T) {
	max := XRPAmount(math.MaxInt64)
	require.Equal(t, XRPAmount(math.MinInt64), max.Add(1))

	min := XRPAmount(math.MinInt64)
	require.Equal(t, XRPAmount(math.MaxInt64), min.Sub(1))

	a, b := int64(1e10), int64(1e10)
	require.Equal(t, XRPAmount(a*b), XRPAmount(a).Mul(b))
}
