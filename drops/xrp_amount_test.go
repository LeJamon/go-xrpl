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

func TestXRPAmount_AddOverflow(t *testing.T) {
	max := XRPAmount(math.MaxInt64)
	_, err := max.AddChecked(1)
	require.ErrorIs(t, err, ErrXRPAmountOverflow)
	// Unchecked Add mirrors rippled's plain int64 +: silent two's-complement wrap.
	require.Equal(t, XRPAmount(math.MinInt64), max.Add(1))

	min := XRPAmount(math.MinInt64)
	_, err = min.AddChecked(-1)
	require.ErrorIs(t, err, ErrXRPAmountOverflow)
	require.Equal(t, XRPAmount(math.MaxInt64), min.Add(-1))
}

func TestXRPAmount_SubOverflow(t *testing.T) {
	min := XRPAmount(math.MinInt64)
	_, err := min.SubChecked(1)
	require.ErrorIs(t, err, ErrXRPAmountOverflow)
	// Unchecked Sub mirrors rippled's plain int64 -: silent two's-complement wrap.
	require.Equal(t, XRPAmount(math.MaxInt64), min.Sub(1))

	max := XRPAmount(math.MaxInt64)
	_, err = max.SubChecked(-1)
	require.ErrorIs(t, err, ErrXRPAmountOverflow)
	require.Equal(t, XRPAmount(math.MinInt64), max.Sub(-1))
}

func TestXRPAmount_MulOverflow(t *testing.T) {
	// 1e10 * 1e10 = 1e20 overflows int64 (~9.2e18).
	_, err := XRPAmount(1e10).MulChecked(1e10)
	require.ErrorIs(t, err, ErrXRPAmountOverflow)
	// Unchecked Mul mirrors rippled's plain int64 *: silent two's-complement wrap.
	a, b := int64(1e10), int64(1e10)
	require.Equal(t, XRPAmount(a*b), XRPAmount(a).Mul(b))

	// Sign handling: MinInt64 * 1 must work.
	got, err := XRPAmount(math.MinInt64).MulChecked(1)
	require.NoError(t, err)
	require.Equal(t, XRPAmount(math.MinInt64), got)

	// MinInt64 * -1 would equal MaxInt64+1 → must overflow.
	_, err = XRPAmount(math.MinInt64).MulChecked(-1)
	require.ErrorIs(t, err, ErrXRPAmountOverflow)
}

func TestXRPAmount_MulZero(t *testing.T) {
	got, err := XRPAmount(123).MulChecked(0)
	require.NoError(t, err)
	require.Zero(t, got)
}

func TestFromDecimalXRP_Bounds(t *testing.T) {
	_, err := FromDecimalXRP(math.NaN())
	require.ErrorIs(t, err, ErrInvalidDecimalXRP)
	_, err = FromDecimalXRP(math.Inf(1))
	require.ErrorIs(t, err, ErrInvalidDecimalXRP)
	_, err = FromDecimalXRP(math.MaxFloat64)
	require.ErrorIs(t, err, ErrXRPAmountOverflow)

	got, err := FromDecimalXRP(1.5)
	require.NoError(t, err)
	require.Equal(t, XRPAmount(1_500_000), got)
}
