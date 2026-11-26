package amm_test

import (
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/amm"
	"github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

// Each deposited asset's funds are checked just before it is sent. When the
// IOU is sent first and its line returns to default, the freed owner reserve
// counts toward the XRP that follows; when XRP is sent first, it does not.
func TestAMMDeposit_XRPSufficiencyFollowsSendOrder(t *testing.T) {
	tests := []struct {
		name     string
		usdFirst bool
		want     ter.Result
	}{
		{name: "IOUSentFirst", usdFirst: true, want: ter.TesSUCCESS},
		{name: "XRPSentFirst", usdFirst: false, want: ter.TecUNFUNDED_AMM},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env := amm.NewAMMTestEnv(t)
			env.Fund()
			env.Trust(env.Alice, env.GW, "USD", 100000)
			env.Trust(env.Carol, env.GW, "USD", 1000)
			env.Close()
			env.PayIOU(env.GW, env.Alice, "USD", 10000)
			env.PayIOU(env.GW, env.Carol, "USD", 100)
			env.Close()
			jtx.RequireTxSuccess(t, env.Submit(
				amm.AMMCreate(env.Alice, amm.XRPAmount(10000), amm.IOUAmount(env.GW, "USD", 10000)).Build()))
			env.Close()

			// A zero limit lets Carol's line return to default once its balance
			// is fully deposited, releasing one owner reserve.
			env.Trust(env.Carol, env.GW, "USD", 0)
			env.Close()
			if got := env.OwnerCount(env.Carol); got != 1 {
				t.Fatalf("Carol owner count = %d, want 1", got)
			}

			// Before the fee Carol's liquid XRP, reserving for the USD line and
			// the LP token line it will gain, is exactly the 100 XRP deposit.
			// After the fee it falls short unless the USD line's reserve has
			// been released by then.
			fee := env.BaseFee()
			target := env.ReserveBase() + 2*env.ReserveIncrement() + uint64(jtx.XRP(100))
			excess := env.Balance(env.Carol) - fee - target
			jtx.RequireTxSuccess(t, env.Submit(payment.Pay(env.Carol, env.Bob, excess).Build()))
			env.Close()
			if got := env.Balance(env.Carol); got != target {
				t.Fatalf("Carol balance = %d, want %d", got, target)
			}

			usd := amm.IOUAmount(env.GW, "USD", 100)
			xrp := amm.XRPAmount(100)
			deposit := amm.AMMDeposit(env.Carol, amm.XRP(), env.USD).TwoAsset()
			if tc.usdFirst {
				deposit.Amount(usd).Amount2(xrp)
			} else {
				deposit.Amount(xrp).Amount2(usd)
			}
			amm.ExpectTER(t, env.Submit(deposit.Build()), tc.want.String())

			if tc.want != ter.TesSUCCESS {
				return
			}
			if env.TrustLineExists(env.Carol, env.GW, "USD") {
				t.Fatal("Carol's USD line should be removed after depositing its full balance")
			}
			if want := target - fee - uint64(jtx.XRP(100)); env.Balance(env.Carol) != want {
				t.Fatalf("Carol balance after deposit = %d, want %d", env.Balance(env.Carol), want)
			}
		})
	}
}
