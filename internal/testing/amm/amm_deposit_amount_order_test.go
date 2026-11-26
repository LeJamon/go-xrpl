package amm_test

import (
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/amm"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/stretchr/testify/require"
)

// A two-asset deposit holds Amount exact and derives the other side from the
// rounded LP tokens, whichever AMM asset Amount names.
func TestAMMDepositTwoAssetAnchorsOnAmountField(t *testing.T) {
	tests := []struct {
		name      string
		usdAmount bool
		wantUSD   string
	}{
		{name: "AmountXRP", wantUSD: "3.999999999999998"},
		{name: "AmountUSD", usdAmount: true, wantUSD: "4"},
	}

	lpTokens := make(map[string]tx.Amount, len(tests))
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := amm.NewAMMTestEnv(t)
			env.FundWithIOUs(100, 0)
			jtx.RequireTxSuccess(t, env.Submit(amm.AMMCreate(
				env.Alice, amm.XRPAmount(3000), amm.IOUAmount(env.GW, "USD", 3),
			).Build()))
			env.Close()

			xrp, usd := amm.XRPAmount(1000), amm.IOUAmount(env.GW, "USD", 1)
			if tt.usdAmount {
				xrp, usd = usd, xrp
			}
			jtx.RequireTxSuccess(t, env.Submit(amm.AMMDeposit(env.Carol, amm.XRP(), env.USD).
				Amount(xrp).
				Amount2(usd).
				TwoAsset().
				Build()))
			env.Close()

			ammAcc := amm.AMMAccount(t, env, amm.XRP(), env.USD)
			require.Equal(t, uint64(jtx.XRP(4000)), env.AMMPoolXRP(ammAcc))
			requireAMMAmount(t, env.AMMPoolIOUPrecise(ammAcc, env.GW, "USD"), tt.wantUSD)

			lpt, found := env.LookupIOUBalance(env.Carol, ammAcc, env.LPTokenAmountFromLedger(amm.XRP(), env.USD, 0).Currency)
			require.True(t, found)
			lpTokens[tt.name] = *lpt
		})
	}
	require.Equal(t, lpTokens["AmountXRP"], lpTokens["AmountUSD"])
}
