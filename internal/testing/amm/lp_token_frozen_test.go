// Assertive tests for the fixFrozenLPTokenTransfer amendment.
//
// Reference: rippled/src/test/app/LPTokenTransfer_test.cpp
//
// Each scenario is exercised with the amendment enabled (frozen underlying AMM
// assets must zero LP-token funds / fail the payment step) and disabled (legacy
// behaviour, frozen underlying assets are ignored for LP-token movement). The
// test env enables every SupportedYes amendment by default, so the enabled arm
// is the default; the disabled arm calls DisableFeature before Close().
package amm_test

import (
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/amm"
	"github.com/LeJamon/go-xrpl/internal/testing/check"
	offerbuild "github.com/LeJamon/go-xrpl/internal/testing/offer"
	"github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/internal/testing/trustset"
)

const fixFrozenLPTokenTransfer = "fixFrozenLPTokenTransfer"

// lpFrozenEnv builds an XRP/USD AMM with bob and carol as liquidity providers,
// each holding 1,000,000 LP tokens, plus LP-token trust lines so LP tokens can
// move by direct payment and offer. Mirrors the setup in rippled's
// LPTokenTransfer_test.cpp testDirectStep/testBookStep.
func lpFrozenEnv(t *testing.T, amendmentEnabled bool) *amm.AMMTestEnv {
	t.Helper()
	env := amm.NewAMMTestEnv(t)
	if !amendmentEnabled {
		env.DisableFeature(fixFrozenLPTokenTransfer)
	}
	env.FundWithIOUs(30000, 0)
	env.TestEnv.FundAmount(env.Bob, uint64(jtx.XRP(30000)))
	env.Trust(env.Bob, env.GW, "USD", 100000)
	env.Close()
	env.PayIOU(env.GW, env.Bob, "USD", 30000)
	env.Close()

	createTx := amm.AMMCreate(env.Alice, amm.XRPAmount(10000), amm.IOUAmount(env.GW, "USD", 10000)).Build()
	if r := env.Submit(createTx); !r.Success {
		t.Fatalf("AMM create failed: %s - %s", r.Code, r.Message)
	}
	env.Close()

	for _, lp := range []*jtx.Account{env.Carol, env.Bob} {
		dep := amm.AMMDeposit(lp, amm.XRP(), env.USD).
			LPTokenOut(amm.LPTokenAmount(env, amm.XRP(), env.USD, 1000000)).
			LPToken().
			Build()
		if r := env.Submit(dep); !r.Success {
			t.Fatalf("%s deposit failed: %s - %s", lp.Address, r.Code, r.Message)
		}
		env.Close()
	}

	// LP-token trust lines so the LP token can be received/sent by payment & offer.
	for _, lp := range []*jtx.Account{env.Alice, env.Bob, env.Carol} {
		tt := trustset.TrustSet(lp, env.LPTokenAmountFromLedger(amm.XRP(), env.USD, 2000000)).Build()
		if r := env.Submit(tt); !r.Success {
			t.Fatalf("%s LP-token trust set failed: %s - %s", lp.Address, r.Code, r.Message)
		}
	}
	env.Close()
	return env
}

// TestFrozenLP_DirectStep mirrors rippled LPTokenTransfer_test.cpp testDirectStep.
//   - A frozen account can always RECEIVE LP tokens.
//   - With the amendment, a frozen account can NOT SEND LP tokens (tecPATH_DRY);
//     without it the send succeeds.
func TestFrozenLP_DirectStep(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		t.Run(label(enabled), func(t *testing.T) {
			env := lpFrozenEnv(t, enabled)

			// Gateway freezes carol's USD.
			env.FreezeTrustLine(env.GW, env.Carol, "USD")
			env.Close()

			lpAmt := env.LPTokenAmountFromLedger(amm.XRP(), env.USD, 5)

			// bob can always send LP tokens to carol even though carol's USD is frozen.
			if r := env.Submit(payment.PayIssued(env.Bob, env.Carol, lpAmt).Build()); !r.Success {
				t.Fatalf("bob->carol (frozen receiver) should succeed, got %s", r.Code)
			}
			env.Close()

			// carol sends LP tokens to bob while her USD is frozen.
			r := env.Submit(payment.PayIssued(env.Carol, env.Bob, lpAmt).Build())
			if enabled {
				if r.Code != "tecPATH_DRY" {
					t.Fatalf("with fix, frozen carol->bob must be tecPATH_DRY, got %s", r.Code)
				}
			} else {
				if !r.Success {
					t.Fatalf("without fix, frozen carol->bob must succeed, got %s", r.Code)
				}
			}
		})
	}
}

// TestFrozenLP_OfferCreation mirrors rippled testOfferCreation.
//   - With the amendment, a frozen account can NOT create a sell offer for LP
//     tokens (tecUNFUNDED_OFFER); without it the offer is created.
//   - A buy offer for LP tokens can always be created (the account is acquiring,
//     not spending, LP tokens).
func TestFrozenLP_OfferCreation(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		t.Run(label(enabled), func(t *testing.T) {
			env := lpFrozenEnv(t, enabled)

			env.FreezeTrustLine(env.GW, env.Carol, "USD")
			env.Close()

			lpAmt := env.LPTokenAmountFromLedger(amm.XRP(), env.USD, 10)

			// carol tries to create a passive offer SELLING LP tokens for XRP.
			sellOffer := offerbuild.OfferCreate(env.Carol, amm.XRPAmount(10), lpAmt).Passive().Build()
			r := env.Submit(sellOffer)
			if enabled {
				if r.Code != "tecUNFUNDED_OFFER" {
					t.Fatalf("with fix, frozen carol sell offer must be tecUNFUNDED_OFFER, got %s", r.Code)
				}
				if got := len(env.AccountOffers(env.Carol)); got != 0 {
					t.Fatalf("with fix, expected 0 carol offers, got %d", got)
				}
				env.Close()

				// Unfreeze and retry: the offer is now created.
				env.UnfreezeTrustLine(env.GW, env.Carol, "USD")
				env.Close()
				sellOffer2 := offerbuild.OfferCreate(env.Carol, amm.XRPAmount(10), lpAmt).Passive().Build()
				if r := env.Submit(sellOffer2); !r.Success {
					t.Fatalf("after unfreeze, carol sell offer must succeed, got %s", r.Code)
				}
				env.Close()
				if got := len(env.AccountOffers(env.Carol)); got != 1 {
					t.Fatalf("after unfreeze, expected 1 carol offer, got %d", got)
				}
			} else {
				if !r.Success {
					t.Fatalf("without fix, frozen carol sell offer must succeed, got %s", r.Code)
				}
				env.Close()
				if got := len(env.AccountOffers(env.Carol)); got != 1 {
					t.Fatalf("without fix, expected 1 carol offer, got %d", got)
				}
			}
		})
	}
}

// TestFrozenLP_OfferCreation_BuyAlwaysAllowed asserts a buy offer for LP tokens
// is created even with the underlying asset frozen, for both amendment states.
// Reference: rippled testOfferCreation final block.
func TestFrozenLP_OfferCreation_BuyAlwaysAllowed(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		t.Run(label(enabled), func(t *testing.T) {
			env := lpFrozenEnv(t, enabled)
			env.FreezeTrustLine(env.GW, env.Carol, "USD")
			env.Close()

			lpAmt := env.LPTokenAmountFromLedger(amm.XRP(), env.USD, 10)
			// carol BUYS LP tokens (pays XRP, receives LP tokens) — always allowed.
			buyOffer := offerbuild.OfferCreate(env.Carol, lpAmt, amm.XRPAmount(5)).Passive().Build()
			if r := env.Submit(buyOffer); !r.Success {
				t.Fatalf("frozen carol buy offer must succeed, got %s", r.Code)
			}
			env.Close()
			if got := len(env.AccountOffers(env.Carol)); got != 1 {
				t.Fatalf("expected 1 carol buy offer, got %d", got)
			}
		})
	}
}

// TestFrozenLP_Check mirrors rippled testCheck.
//   - carol can always create a check funded with LP tokens whose underlying is frozen.
//   - With the amendment, bob fails to cash that check (tecPATH_PARTIAL); without
//     it the cash succeeds.
//   - carol can always cash a check sent to her even while her USD is frozen.
func TestFrozenLP_Check(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		t.Run(label(enabled), func(t *testing.T) {
			env := lpFrozenEnv(t, enabled)

			env.FreezeTrustLine(env.GW, env.Carol, "USD")
			env.Close()

			lpAmt := env.LPTokenAmountFromLedger(amm.XRP(), env.USD, 10)

			// carol creates a check with LP tokens (her USD is frozen) — always allowed.
			carolChkID := check.GetCheckID(env.Carol, env.TestEnv.Seq(env.Carol))
			if r := env.Submit(check.CheckCreate(env.Carol, env.Bob, lpAmt).Build()); !r.Success {
				t.Fatalf("carol check create must succeed, got %s", r.Code)
			}
			env.Close()

			// bob cashes carol's check.
			rCash := env.Submit(check.CheckCashAmount(env.Bob, carolChkID, lpAmt).Build())
			if enabled {
				if rCash.Code != "tecPATH_PARTIAL" {
					t.Fatalf("with fix, bob cashing frozen-LP check must be tecPATH_PARTIAL, got %s", rCash.Code)
				}
			} else {
				if !rCash.Success {
					t.Fatalf("without fix, bob cashing frozen-LP check must succeed, got %s", rCash.Code)
				}
			}
			env.Close()

			// bob creates a check for carol; carol (frozen) can still RECEIVE LP tokens.
			bobChkID := check.GetCheckID(env.Bob, env.TestEnv.Seq(env.Bob))
			if r := env.Submit(check.CheckCreate(env.Bob, env.Carol, lpAmt).Build()); !r.Success {
				t.Fatalf("bob check create must succeed, got %s", r.Code)
			}
			env.Close()
			if r := env.Submit(check.CheckCashAmount(env.Carol, bobChkID, lpAmt).Build()); !r.Success {
				t.Fatalf("frozen carol cashing a check (receiving LP) must succeed, got %s", r.Code)
			}
		})
	}
}

func label(enabled bool) string {
	if enabled {
		return "amendment_enabled"
	}
	return "amendment_disabled"
}
