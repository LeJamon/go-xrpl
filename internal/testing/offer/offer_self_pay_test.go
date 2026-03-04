package offer

// Self-pay transfer fee offer tests.
// Reference: rippled/src/test/app/Offer_test.cpp
//   - testSelfPayXferFeeOffer (lines 3995-4148)
//   - testSelfPayUnlimitedFunds (lines 4150-4297)

import (
	"testing"

	"github.com/LeJamon/goXRPLd/internal/core/tx"
	jtx "github.com/LeJamon/goXRPLd/internal/testing"
	"github.com/LeJamon/goXRPLd/internal/testing/payment"
	"github.com/stretchr/testify/require"
)

type selfPayActor struct {
	acct   *jtx.Account
	offers int
	xrp    uint64
	btc    float64
	usd    float64
}

type selfPayTestData struct {
	selfIdx  int
	leg0Idx  int
	leg1Idx  int
	btcStart float64
	actors   []selfPayActor
}

// TestOffer_SelfPayXferFeeOffer tests auto-bridged offer crossing
// where alice may end up paying herself with transfer fees.
// Reference: rippled Offer_test.cpp testSelfPayXferFeeOffer (lines 3995-4148)
func TestOffer_SelfPayXferFeeOffer(t *testing.T) {
	for _, fs := range offerFeatureSets {
		t.Run(fs.name, func(t *testing.T) {
			testSelfPayXferFeeOffer(t, fs.disabled)
		})
	}
}

func testSelfPayXferFeeOffer(t *testing.T, disabledFeatures []string) {
	env := newEnvWithFeatures(t, disabledFeatures)
	baseFee := env.BaseFee()

	gw := jtx.NewAccount("gw")
	BTC := func(amount float64) tx.Amount { return jtx.BTC(gw, amount) }
	USD := func(amount float64) tx.Amount { return jtx.USD(gw, amount) }

	env.FundAmount(gw, uint64(jtx.XRP(4000000)))
	env.Close()

	env.SetTransferRate(gw, 1250000000) // rate 1.25
	env.Close()

	tests := []selfPayTestData{
		// ann crosses her own BTC leg; no BTC xfer fee
		{0, 0, 1, 20, []selfPayActor{
			{jtx.NewAccount("ann"), 0, uint64(jtx.XRP(3900000)) - 4*baseFee, 20.0, 3000},
			{jtx.NewAccount("abe"), 0, uint64(jtx.XRP(4100000)) - 3*baseFee, 0, 750},
		}},
		// bev crosses her own USD leg; no USD xfer fee
		{0, 1, 0, 20, []selfPayActor{
			{jtx.NewAccount("bev"), 0, uint64(jtx.XRP(4100000)) - 4*baseFee, 7.5, 2000},
			{jtx.NewAccount("bob2"), 0, uint64(jtx.XRP(3900000)) - 3*baseFee, 10, 0},
		}},
		// cam crosses both legs; no xfer fee at all
		{0, 0, 0, 20, []selfPayActor{
			{jtx.NewAccount("cam"), 0, uint64(jtx.XRP(4000000)) - 5*baseFee, 20.0, 2000},
		}},
		// deb partially crosses; no USD xfer fee (forward case)
		{0, 1, 0, 5, []selfPayActor{
			{jtx.NewAccount("deb"), 1, uint64(jtx.XRP(4040000)) - 4*baseFee, 0.0, 2000},
			{jtx.NewAccount("dan3"), 1, uint64(jtx.XRP(3960000)) - 3*baseFee, 4, 0},
		}},
	}

	for i, tt := range tests {
		self := tt.actors[tt.selfIdx]
		leg0 := tt.actors[tt.leg0Idx]
		leg1 := tt.actors[tt.leg1Idx]

		t.Run(self.acct.Name, func(t *testing.T) {
			for _, actor := range tt.actors {
				env.FundAmount(actor.acct, uint64(jtx.XRP(4000000)))
				env.Close()
				env.Trust(actor.acct, BTC(40))
				env.Trust(actor.acct, USD(8000))
				env.Close()
			}

			result := env.Submit(payment.PayIssued(gw, self.acct, BTC(tt.btcStart)).Build())
			jtx.RequireTxSuccess(t, result)
			result = env.Submit(payment.PayIssued(gw, self.acct, USD(2000)).Build())
			jtx.RequireTxSuccess(t, result)
			if self.acct.Name != leg1.acct.Name {
				result = env.Submit(payment.PayIssued(gw, leg1.acct, USD(2000)).Build())
				jtx.RequireTxSuccess(t, result)
			}
			env.Close()

			// leg0 offer: passive, wants BTC(10), pays XRP(100000)
			result = env.Submit(OfferCreate(leg0.acct, BTC(10), jtx.XRPTxAmountFromXRP(100000)).Passive().Build())
			jtx.RequireTxSuccess(t, result)
			env.Close()
			leg0OfferSeq := env.Seq(leg0.acct) - 1

			// leg1 offer: passive, wants XRP(100000), pays USD(1000)
			result = env.Submit(OfferCreate(leg1.acct, jtx.XRPTxAmountFromXRP(100000), USD(1000)).Passive().Build())
			jtx.RequireTxSuccess(t, result)
			env.Close()
			leg1OfferSeq := env.Seq(leg1.acct) - 1

			// This is the offer that matters: wants USD(1000), pays BTC(10)
			result = env.Submit(OfferCreate(self.acct, USD(1000), BTC(10)).Build())
			jtx.RequireTxSuccess(t, result)
			env.Close()
			selfOfferSeq := env.Seq(self.acct) - 1

			// Verify results
			for _, actor := range tt.actors {
				// Count non-empty offers
				actorOffers := OffersOnAccount(env, actor.acct)
				nonEmptyCount := 0
				for _, o := range actorOffers {
					if !o.TakerGets.IsZero() {
						nonEmptyCount++
					}
				}
				require.Equal(t, actor.offers, nonEmptyCount,
					"Test %d (%s): expected %d offers, got %d", i, actor.acct.Name, actor.offers, nonEmptyCount)

				jtx.RequireBalance(t, env, actor.acct, actor.xrp)
				jtx.RequireIOUBalance(t, env, actor.acct, gw, "BTC", actor.btc)
				jtx.RequireIOUBalance(t, env, actor.acct, gw, "USD", actor.usd)
			}

			// Cleanup
			env.Submit(OfferCancel(leg0.acct, leg0OfferSeq).Build())
			env.Close()
			env.Submit(OfferCancel(leg1.acct, leg1OfferSeq).Build())
			env.Close()
			env.Submit(OfferCancel(self.acct, selfOfferSeq).Build())
			env.Close()
		})
	}
}

// TestOffer_SelfPayUnlimitedFunds tests the "unlimited funds" optimization
// in Taker offer crossing where alice pays herself.
// Reference: rippled Offer_test.cpp testSelfPayUnlimitedFunds (lines 4150-4297)
func TestOffer_SelfPayUnlimitedFunds(t *testing.T) {
	for _, fs := range offerFeatureSets {
		t.Run(fs.name, func(t *testing.T) {
			testSelfPayUnlimitedFunds(t, fs.disabled)
		})
	}
}

func testSelfPayUnlimitedFunds(t *testing.T, disabledFeatures []string) {
	env := newEnvWithFeatures(t, disabledFeatures)
	baseFee := env.BaseFee()

	gw := jtx.NewAccount("gw")
	BTC := func(amount float64) tx.Amount { return jtx.BTC(gw, amount) }
	USD := func(amount float64) tx.Amount { return jtx.USD(gw, amount) }

	env.FundAmount(gw, uint64(jtx.XRP(4000000)))
	env.Close()

	env.SetTransferRate(gw, 1250000000) // rate 1.25
	env.Close()

	tests := []selfPayTestData{
		// gay crosses her own BTC leg; no BTC xfer fee (forward case)
		{0, 0, 1, 5, []selfPayActor{
			{jtx.NewAccount("gay"), 1, uint64(jtx.XRP(3950000)) - 4*baseFee, 5, 2500},
			{jtx.NewAccount("gar"), 1, uint64(jtx.XRP(4050000)) - 3*baseFee, 0, 1375},
		}},
		// hye crosses both legs; no xfer fee (forward case)
		{0, 0, 0, 5, []selfPayActor{
			{jtx.NewAccount("hye"), 2, uint64(jtx.XRP(4000000)) - 5*baseFee, 5, 2000},
		}},
	}

	for i, tt := range tests {
		self := tt.actors[tt.selfIdx]
		leg0 := tt.actors[tt.leg0Idx]
		leg1 := tt.actors[tt.leg1Idx]

		t.Run(self.acct.Name, func(t *testing.T) {
			for _, actor := range tt.actors {
				env.FundAmount(actor.acct, uint64(jtx.XRP(4000000)))
				env.Close()
				env.Trust(actor.acct, BTC(40))
				env.Trust(actor.acct, USD(8000))
				env.Close()
			}

			result := env.Submit(payment.PayIssued(gw, self.acct, BTC(tt.btcStart)).Build())
			jtx.RequireTxSuccess(t, result)
			result = env.Submit(payment.PayIssued(gw, self.acct, USD(2000)).Build())
			jtx.RequireTxSuccess(t, result)
			if self.acct.Name != leg1.acct.Name {
				result = env.Submit(payment.PayIssued(gw, leg1.acct, USD(2000)).Build())
				jtx.RequireTxSuccess(t, result)
			}
			env.Close()

			result = env.Submit(OfferCreate(leg0.acct, BTC(10), jtx.XRPTxAmountFromXRP(100000)).Passive().Build())
			jtx.RequireTxSuccess(t, result)
			env.Close()
			leg0OfferSeq := env.Seq(leg0.acct) - 1

			result = env.Submit(OfferCreate(leg1.acct, jtx.XRPTxAmountFromXRP(100000), USD(1000)).Passive().Build())
			jtx.RequireTxSuccess(t, result)
			env.Close()
			leg1OfferSeq := env.Seq(leg1.acct) - 1

			result = env.Submit(OfferCreate(self.acct, USD(1000), BTC(10)).Build())
			jtx.RequireTxSuccess(t, result)
			env.Close()
			selfOfferSeq := env.Seq(self.acct) - 1

			for _, actor := range tt.actors {
				actorOffers := OffersOnAccount(env, actor.acct)
				nonEmptyCount := 0
				for _, o := range actorOffers {
					if !o.TakerGets.IsZero() {
						nonEmptyCount++
					}
				}
				require.Equal(t, actor.offers, nonEmptyCount,
					"Test %d (%s): expected %d offers, got %d", i, actor.acct.Name, actor.offers, nonEmptyCount)

				jtx.RequireBalance(t, env, actor.acct, actor.xrp)
				jtx.RequireIOUBalance(t, env, actor.acct, gw, "BTC", actor.btc)
				jtx.RequireIOUBalance(t, env, actor.acct, gw, "USD", actor.usd)
			}

			env.Submit(OfferCancel(leg0.acct, leg0OfferSeq).Build())
			env.Close()
			env.Submit(OfferCancel(leg1.acct, leg1OfferSeq).Build())
			env.Close()
			env.Submit(OfferCancel(self.acct, selfOfferSeq).Build())
			env.Close()
		})
	}
}
