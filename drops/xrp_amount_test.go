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
