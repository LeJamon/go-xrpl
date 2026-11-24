package payment

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
)

// swapAssetIn rounds every step to favor the AMM: the pool product upward, the
// grown input pool downward, the ratio upward and the output downward. The
// expected values were computed independently from the rc1 formula with
// 16-digit directed decimal rounding; rounding the product downward instead
// changes every one of them.
func TestSwapAssetInDirectedRounding(t *testing.T) {
	for _, test := range []struct {
		poolIn, poolOut, assetIn string
		tfee                     uint16
		want                     string
	}{
		{"1234.567890123457", "9876.543210987654", "1.111111111111111", 0, "8.880896163262"},
		{"1234.567890123457", "9876.543210987654", "1.111111111111111", 1000, "8.792166259966"},
		{"3.333333333333333", "7.777777777777777", "0.1234567890123456", 500, "0.276438250463092"},
		{"100000.0000000001", "99999.99999999989", "12.34567890123457", 0, "12.34415493146"},
	} {
		amount := func(value string) state.Amount {
			parsed, err := state.NewIssuedAmountFromDecimalString(value, "USD", "rG1QQv2nh2gr7RCZ1P8YYcBUKCCN633jCn")
			require.NoError(t, err)
			return parsed
		}
		got := SwapAssetIn(amount(test.poolIn), amount(test.poolOut), amount(test.assetIn), test.tfee)
		require.Equal(t, test.want, got.Value(), "swapAssetIn(%s, %s, %s, %d)", test.poolIn, test.poolOut, test.assetIn, test.tfee)
	}
}
