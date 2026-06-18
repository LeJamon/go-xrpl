package amm_test

import (
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/amm"
	offerbuild "github.com/LeJamon/go-xrpl/internal/testing/offer"
	"github.com/LeJamon/go-xrpl/internal/tx"
)

// TestIssue1018_AMMMarginalCrossBehindWorseLOB reproduces mainnet ledger 99226376.
//
// An XRP/NAUTi AMM pool (fee=0, XRP=10526371 drops, NAUTi=2089.989295377329)
// sits behind resting CLOB offers whose quality (best ~0.00019888 NAUTi/XRP) is
// WORSE than both the AMM spot price and the incoming offer's limit quality
// (368.83 NAUTi / 1857611 drops ~0.00019855). The AMM is marginally better than
// the limit, so a tiny sliver must cross the AMM while the worse LOB tip is left
// untouched.
//
// Before the fix, forEachOffer's tryAMM passed the raw LOB tip quality to the AMM
// offer generator, sizing the synthetic AMM offer down to the worse LOB tier so
// it failed the taker's quality limit and nothing crossed. rippled passes nullopt
// in this single-path case (BookOfferCrossingStep::qualityThreshold) so the AMM
// generates its maximum offer, which the strand then limits to the taker quality.
func TestIssue1018_AMMMarginalCrossBehindWorseLOB(t *testing.T) {
	const cur = "NAU"
	poolXRP := tx.NewXRPAmount(10526371)
	poolNAU := tx.NewIssuedAmount(2089989295377329, -12, cur, "")

	pool := [2]tx.Amount{poolXRP, poolNAU}
	amm.TestAMM(t, &pool, 0, func(env *amm.AMMTestEnv, ammAcc *jtx.Account) {
		// Resting CLOB offer selling XRP for NAUTi at a quality worse than the
		// incoming offer's limit: best mainnet tier was 603360 drops for 120 NAUTi.
		env.FundBob(30000, 0)
		env.Trust(env.Bob, env.GW, cur, 100000)
		env.Close()
		env.PayIOU(env.GW, env.Bob, cur, 50000)
		env.Close()
		bobOffer := offerbuild.OfferCreate(env.Bob,
			tx.NewIssuedAmount(120, 0, cur, env.GW.Address), // TakerPays = 120 NAUTi
			tx.NewXRPAmount(603360),                         // TakerGets = 603360 drops XRP
		).Build()
		jtx.RequireTxSuccess(t, env.Submit(bobOffer))
		env.Close()

		xrpBefore := env.AMMPoolXRP(ammAcc)

		// Carol sells 368.83 NAUTi, wants 1857611 drops XRP (Flags=0).
		takerPays := tx.NewXRPAmount(1857611)
		takerGets := tx.NewIssuedAmount(36883, -2, cur, env.GW.Address)
		offerTx := offerbuild.OfferCreate(env.Carol, takerPays, takerGets).Build()
		jtx.RequireTxSuccess(t, env.Submit(offerTx))
		env.Close()

		// The AMM must have crossed exactly the 147-drop sliver mainnet saw.
		if consumed := int64(xrpBefore) - int64(env.AMMPoolXRP(ammAcc)); consumed != 147 {
			t.Fatalf("AMM XRP consumed = %d drops, want 147 (offer crossed nothing => bug #1018)", consumed)
		}

		// Carol's remainder offer matches mainnet: TakerPays=1857464, TakerGets=368.8008130442811.
		carolOffers := env.AccountOffers(env.Carol)
		if len(carolOffers) != 1 {
			t.Fatalf("Carol offers = %d, want 1", len(carolOffers))
		}
		if got := carolOffers[0].TakerPays.Value(); got != "1857464" {
			t.Errorf("Carol remainder TakerPays = %s, want 1857464", got)
		}
		if got := carolOffers[0].TakerGets.Value(); got != "368.8008130442811" {
			t.Errorf("Carol remainder TakerGets = %s, want 368.8008130442811", got)
		}

		// The worse LOB tip must be left fully untouched.
		if n := env.OfferCount(env.Bob); n != 1 {
			t.Errorf("Bob LOB offers = %d, want 1 (worse-quality tip must not cross)", n)
		}
	})
}
