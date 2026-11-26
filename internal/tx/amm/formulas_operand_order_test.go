package amm

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/stretchr/testify/require"
)

// Without fixAMMv1_3 the balance is multiplied before dividing: (B*num)/den
// and (T*x)/2. Each vector rounds differently under the other operand order.
func TestPreFixAMMv1_3OperandOrder(t *testing.T) {
	m := numberMath{ctx: state.NewNumberContext(state.MantissaScaleSmall, true)}
	iou := func(v string) tx.Amount {
		a, err := state.NewIssuedAmountFromDecimalString(v, "USD", "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh")
		require.NoError(t, err)
		return a
	}

	t.Run("ammAssetOut", func(t *testing.T) {
		for _, tc := range []struct {
			balance, lpt, tokens string
			tfee                 uint16
			want                 string
		}{
			{"4300.694", "1511.162", "390.728", 445, "1933.744244795292"},
			{"8737.631", "5485.026", "314.09", 433, "970.1117077729896"},
		} {
			got := ammAssetOut(m, iou(tc.balance), iou(tc.lpt), iou(tc.tokens), tc.tfee, false)
			require.Equal(t, iou(tc.want), got)
		}
	})

	t.Run("lpTokensIn", func(t *testing.T) {
		for _, tc := range []struct {
			balance, withdraw, lpt string
			tfee                   uint16
			want                   string
		}{
			{"9237.106", "748.947", "3495.466", 287, "144.9010940950054"},
			{"5888.79", "132.429", "7015.541", 356, "79.4722808670715"},
		} {
			got := calcLPTokensIn(m, iou(tc.balance), iou(tc.withdraw), iou(tc.lpt), tc.tfee, false)
			require.Equal(t, iou(tc.want), got)
		}
	})
}
