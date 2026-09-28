package state

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGetRateLargeIntegralNumerator(t *testing.T) {
	contexts := []struct {
		name string
		ctx  NumberContext
	}{
		{"legacy", NewNumberContext(MantissaScaleSmall, false)},
		{"universal", NewNumberContext(MantissaScaleSmall, true)},
		{"large", NewNumberContext(MantissaScaleLargeLegacy, true)},
		{"cleanup320", NewNumberContext(MantissaScaleLarge320, true)},
		{"cleanup330", NewNumberContext(MantissaScaleLarge330, true)},
	}
	tests := []struct {
		name      string
		value     int64
		mantissa  uint64
		truncated uint64
	}{
		{"below signed quotient boundary", 92_233_720_368_547_758, 9_223_372_036_854_776, 9_223_372_036_854_775},
		{"above signed quotient boundary", 92_233_720_368_547_759, 9_223_372_036_854_776, 9_223_372_036_854_775},
		{"testnet ledger 21068881", 95_720_761_450_600_688, 8_874_667_928_649_483, 8_874_667_928_649_482},
		{"maximum XRP", 100_000_000_000_000_000, 8_446_744_073_709_552, 8_446_744_073_709_551},
	}
	out := NewIssuedAmountFromValue(MinMantissa, -15, "USD", "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh")
	for _, context := range contexts {
		t.Run(context.name, func(t *testing.T) {
			for _, test := range tests {
				t.Run(test.name, func(t *testing.T) {
					mantissa := test.mantissa
					if !context.ctx.UniversalNumberEnabled() {
						mantissa = test.truncated
					}
					want := uint64(101)<<56 | mantissa
					for _, in := range []Amount{
						NewXRPAmountFromInt(test.value),
						NewMPTAmountWithIssuanceID(test.value, "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh", "000000010000000000000000000000000000000000000001"),
					} {
						require.Equal(t, want, GetRateWithNumberContext(out, in, context.ctx))
					}
				})
			}
		})
	}
}
