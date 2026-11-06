package vault

import (
	"math"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/stretchr/testify/require"
)

// rate125 is a 25% transfer fee.
const rate125 uint32 = 1_250_000_000

func transferFeeNumberContext() state.NumberContext {
	return tx.NumberContextForRules(amendment.NewRulesBuilder().FromPreset(amendment.PresetAllSupported).Build())
}

// Reference: rippled src/tests/libxrpl/ledger/TokenHelpers.cpp.
func TestTransferFeeHelpers(t *testing.T) {
	nc := transferFeeNumberContext()
	cases := []struct {
		name     string
		subtract bool
		integral bool
		value    int64
		rate     uint32
		want     int64
	}{
		{"add_iou", false, false, 80, rate125, 100},
		{"add_iou_parity", false, false, 80, tx.TransferRateParity, 80},
		{"add_mpt", false, true, 80, rate125, 100},
		{"add_mpt_rounds_up", false, true, 1, rate125, 2},
		{"add_mpt_parity", false, true, 1, tx.TransferRateParity, 1},
		{"subtract_iou", true, false, 100, rate125, 80},
		{"subtract_iou_parity", true, false, 100, tx.TransferRateParity, 100},
		{"subtract_mpt", true, true, 31, rate125, 24},
		{"subtract_mpt_rounds_to_zero", true, true, 1, rate125, 0},
		{"subtract_mpt_parity", true, true, 1, tx.TransferRateParity, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			n := nc.Int(c.value)
			var got state.XRPLNumber
			if c.subtract {
				got = SubtractTransferFee(n, c.integral, c.rate, nc)
			} else {
				got = addTransferFee(n, c.integral, c.rate, nc)
			}
			require.Zero(t, got.Cmp(nc.Int(c.want)), "got %s, want %d", got.String(), c.want)
		})
	}
}

// Reference: rippled src/tests/libxrpl/protocol/STAmount.cpp mulRatio tests.
func TestMulAssetRatio(t *testing.T) {
	nc := transferFeeNumberContext()
	one := tx.TransferRateParity

	t.Run("iou", func(t *testing.T) {
		require.Zero(t, mulAssetRatio(nc.Int(100), false, one, rate125, false, nc).Cmp(nc.Int(80)))
		require.Zero(t, mulAssetRatio(nc.Int(80), false, rate125, one, true, nc).Cmp(nc.Int(100)))

		// 1 / 1.7 does not fit in a 16-digit mantissa, so the rounding
		// direction decides the last digit.
		down := mulAssetRatio(nc.Int(1), false, one, 1_700_000_000, false, nc)
		up := mulAssetRatio(nc.Int(1), false, one, 1_700_000_000, true, nc)
		require.Negative(t, down.Cmp(up))

		full := nc.Number(9_999_999_999_999_999, -5, state.RoundToNearest)
		require.Zero(t, mulAssetRatio(full, false, one, one, false, nc).Cmp(full))
	})

	t.Run("mpt", func(t *testing.T) {
		for _, c := range []struct {
			value   int64
			roundUp bool
			want    int64
		}{
			{31, false, 24},
			{31, true, 25},
			{1, false, 0},
			{1, true, 1},
		} {
			got := mulAssetRatio(nc.Int(c.value), true, one, rate125, c.roundUp, nc)
			require.Zero(t, got.Cmp(nc.Int(c.want)), "%d roundUp=%v: got %s", c.value, c.roundUp, got.String())
		}

		maximum := nc.Int(math.MaxInt64)
		require.Zero(t, mulAssetRatio(maximum, true, one, one, false, nc).Cmp(maximum))
		require.NotPanics(t, func() { mulAssetRatio(maximum, true, one, rate125, false, nc) })
		require.Panics(t, func() { mulAssetRatio(maximum, true, rate125, one, true, nc) })
	})
}
