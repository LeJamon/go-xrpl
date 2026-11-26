package payment

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
)

func TestWithinRelativeDistanceMeasuresAgainstWorseQuality(t *testing.T) {
	quality := func(mantissa uint64, exponent int) Quality {
		return Quality{Value: uint64(exponent+100)<<56 | mantissa}
	}
	tests := []struct {
		name   string
		q1, q2 Quality
		want   bool
	}{
		{name: "equal", q1: quality(1_000_000_000_000_000, -15), q2: quality(1_000_000_000_000_000, -15), want: true},
		// The gap is 1.00000005e-7 of the better rate but 9.9999995e-8 of the worse.
		{name: "inside only relative to worse rate", q1: quality(1_000_000_000_000_000, -15), q2: quality(1_000_000_100_000_005, -15), want: true},
		{name: "outside", q1: quality(1_000_000_000_000_000, -15), q2: quality(1_000_000_200_000_000, -15), want: false},
		{name: "across exponents", q1: quality(9_999_999_999_999_999, -16), q2: quality(1_000_000_000_000_000, -15), want: true},
	}
	for _, scale := range []state.MantissaScale{state.MantissaScaleSmall, state.MantissaScaleLarge} {
		ctx := state.NewNumberContext(scale, false)
		dist := ctx.Number(1, -7, state.RoundToNearest)
		for _, tt := range tests {
			for _, pair := range [][2]Quality{{tt.q1, tt.q2}, {tt.q2, tt.q1}} {
				if got := WithinRelativeDistance(ctx, pair[0], pair[1], dist); got != tt.want {
					t.Errorf("scale %v %s: WithinRelativeDistance(%x, %x) = %v, want %v", scale, tt.name, pair[0].Value, pair[1].Value, got, tt.want)
				}
			}
		}
	}
}
