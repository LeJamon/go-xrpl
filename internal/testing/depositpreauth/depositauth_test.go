// Tests for DepositAuth flag behaviour.
// Reference: rippled/src/test/app/DepositAuth_test.cpp – struct DepositAuth_test
package depositpreauth_test

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/accountset"
	"github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/internal/testing/trustset"
	"github.com/LeJamon/go-xrpl/internal/tx"
	accounttx "github.com/LeJamon/go-xrpl/internal/tx/account"
	paymentPkg "github.com/LeJamon/go-xrpl/internal/tx/payment"
	"github.com/stretchr/testify/require"
)

// reserve returns the account reserve for the given owner count.
// Reference: rippled reserve(env, count)
func reserve(env *jtx.TestEnv, count uint32) uint64 {
	return env.ReserveBase() + uint64(count)*env.ReserveIncrement()
}

// --------------------------------------------------------------------------
// testEnable
// Reference: rippled DepositAuth_test::testEnable (lines 47-81)
// --------------------------------------------------------------------------

func TestDepositAuth_Enable(t *testing.T) {
	alice := jtx.NewAccount("alice")

	env := jtx.NewTestEnv(t)

	env.FundAmount(alice, uint64(jtx.XRP(10000)))
	env.Close()

	env.EnableDepositAuth(alice)
	env.Close()
	jtx.RequireFlagSet(t, env, alice, state.LsfDepositAuth)

	env.DisableDepositAuth(alice)
	env.Close()
	jtx.RequireFlagNotSet(t, env, alice, state.LsfDepositAuth)
}

// --------------------------------------------------------------------------
// testPayIOU
// Reference: rippled DepositAuth_test::testPayIOU (lines 83-177)
// --------------------------------------------------------------------------

func TestDepositAuth_PayIOU(t *testing.T) {
	env := jtx.NewTestEnv(t)

	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	carol := jtx.NewAccount("carol")
	gw := jtx.NewAccount("gw")

	env.FundAmount(alice, uint64(jtx.XRP(10000)))
	env.FundAmount(bob, uint64(jtx.XRP(10000)))
	env.FundAmount(carol, uint64(jtx.XRP(10000)))
	env.FundAmount(gw, uint64(jtx.XRP(10000)))
	env.Close()

	// Set up trust lines
	result := env.Submit(trustset.TrustLine(alice, "USD", gw, "1000").Build())
	jtx.RequireTxSuccess(t, result)
	result = env.Submit(trustset.TrustLine(bob, "USD", gw, "1000").Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	usd150 := tx.NewIssuedAmountFromFloat64(150, "USD", gw.Address)
	result = env.Submit(payment.PayIssued(gw, alice, usd150).Build())
	jtx.RequireTxSuccess(t, result)

	// carol creates an offer: sell XRP(100) for USD(100)
	usd100Offer := tx.NewIssuedAmountFromFloat64(100, "USD", gw.Address)
	xrp100Offer := tx.NewXRPAmount(jtx.XRP(100))
	result = env.CreateOffer(carol, xrp100Offer, usd100Offer)
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// alice pays bob some USD to set up initial balance
	usd50 := tx.NewIssuedAmountFromFloat64(50, "USD", gw.Address)
	result = env.Submit(payment.PayIssued(alice, bob, usd50).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// bob enables DepositAuth
	env.EnableDepositAuth(bob)
	env.Close()
	jtx.RequireFlagSet(t, env, bob, state.LsfDepositAuth)

	// --- failedIouPayments closure ---
	failedIouPayments := func() {
		jtx.RequireFlagSet(t, env, bob, state.LsfDepositAuth)

		bobXRP := env.Balance(bob)
		bobUSD := env.BalanceIOU(bob, "USD", gw)

		// IOU payment should fail
		result = env.Submit(payment.PayIssued(alice, bob, usd50).Build())
		require.Equal(t, "tecNO_PERMISSION", result.Code)
		env.Close()

		// XRP payment through an offer should also fail (it passes through IOU)
		usd1 := tx.NewIssuedAmountFromFloat64(1, "USD", gw.Address)
		result = env.Submit(payment.Pay(alice, bob, 1).SendMax(usd1).Build())
		require.Equal(t, "tecNO_PERMISSION", result.Code)
		env.Close()

		// Confirm bob's balances did not change
		require.Equal(t, bobXRP, env.Balance(bob))
		require.InDelta(t, bobUSD, env.BalanceIOU(bob, "USD", gw), 1e-10)
	}

	// Test when bob has XRP > base reserve.
	failedIouPayments()

	// bob pays alice to reduce balance. Demonstrate bob can make payments.
	usd25 := tx.NewIssuedAmountFromFloat64(25, "USD", gw.Address)
	result = env.Submit(payment.PayIssued(bob, alice, usd25).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// Bring bob's XRP balance down to exactly base reserve.
	{
		bobPaysXRP := env.Balance(bob) - reserve(env, 1)
		bobPaysFee := reserve(env, 1) - reserve(env, 0)
		result = env.Submit(payment.Pay(bob, alice, bobPaysXRP).Fee(bobPaysFee).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
	}

	// bob has exactly the base reserve.
	require.Equal(t, reserve(env, 0), env.Balance(bob))
	require.InDelta(t, 25.0, env.BalanceIOU(bob, "USD", gw), 1e-10)
	failedIouPayments()

	// Test when bob has XRP balance == 0.
	env.NoopWithFee(bob, reserve(env, 0))
	env.Close()
	require.Zero(t, env.Balance(bob))
	failedIouPayments()

	// bob clears DepositAuth and payments succeed again.
	// Give bob enough XRP for the fee to clear DepositAuth.
	result = env.Submit(payment.Pay(alice, bob, env.BaseFee()).Build())
	jtx.RequireTxSuccess(t, result)

	env.DisableDepositAuth(bob)
	env.Close()
	jtx.RequireFlagNotSet(t, env, bob, state.LsfDepositAuth)

	bobUSD := env.BalanceIOU(bob, "USD", gw)
	result = env.Submit(payment.PayIssued(alice, bob, usd50).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()
	require.InDelta(t, bobUSD+50, env.BalanceIOU(bob, "USD", gw), 1e-10)

	bobXRP := env.Balance(bob)
	usd1 := tx.NewIssuedAmountFromFloat64(1, "USD", gw.Address)
	result = env.Submit(payment.Pay(alice, bob, 1).SendMax(usd1).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()
	require.Equal(t, bobXRP+1, env.Balance(bob))
}

// --------------------------------------------------------------------------
// testPayXRP
// Reference: rippled DepositAuth_test::testPayXRP (lines 179-280)
// --------------------------------------------------------------------------

func TestDepositAuth_PayXRP(t *testing.T) {
	env := jtx.NewTestEnv(t)
	env.EnableOpenLedgerReplay()

	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")

	baseFee := env.BaseFee()

	env.FundAmount(alice, uint64(jtx.XRP(10000)))
	env.FundAmount(bob, uint64(jtx.XRP(10000)))
	env.Close()

	// bob enables DepositAuth
	env.EnableDepositAuth(bob)
	env.Close()

	expectedBobBalance := uint64(jtx.XRP(10000)) - baseFee
	require.Equal(t, expectedBobBalance, env.Balance(bob))

	// bob has more XRP than base reserve — any payment should fail.
	result := env.Submit(payment.Pay(alice, bob, 1).Build())
	require.Equal(t, "tecNO_PERMISSION", result.Code)
	env.Close()
	require.Equal(t, expectedBobBalance, env.Balance(bob))

	// Bring bob's XRP balance to exactly the base reserve.
	{
		bobPaysXRP := env.Balance(bob) - reserve(env, 1)
		bobPaysFee := reserve(env, 1) - reserve(env, 0)
		result = env.Submit(payment.Pay(bob, alice, bobPaysXRP).Fee(bobPaysFee).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
	}

	// bob has exactly the base reserve. A small direct XRP payment should succeed.
	require.Equal(t, reserve(env, 0), env.Balance(bob))
	result = env.Submit(payment.Pay(alice, bob, 1).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// bob has base reserve + 1. No payment should succeed.
	require.Equal(t, reserve(env, 0)+1, env.Balance(bob))
	result = env.Submit(payment.Pay(alice, bob, 1).Build())
	require.Equal(t, "tecNO_PERMISSION", result.Code)
	env.Close()

	// Take bob down to 0 XRP.
	// rippled: env(noop(bob), fee(reserve(env, 0) + drops(1)));
	env.NoopWithFee(bob, reserve(env, 0)+1)
	env.Close()

	// We should not be able to pay bob more than the base reserve.
	result = env.Submit(payment.Pay(alice, bob, reserve(env, 0)+1).Build())
	require.Equal(t, "tecNO_PERMISSION", result.Code)
	env.Close()

	// A payment of exactly the base reserve should succeed.
	result = env.Submit(payment.Pay(alice, bob, reserve(env, 0)).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()
	require.Equal(t, reserve(env, 0), env.Balance(bob))

	// We should be able to pay bob the base reserve one more time.
	result = env.Submit(payment.Pay(alice, bob, reserve(env, 0)).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()
	require.Equal(t, reserve(env, 0)+reserve(env, 0), env.Balance(bob))

	// bob's above the threshold again. Any payment should fail.
	result = env.Submit(payment.Pay(alice, bob, 1).Build())
	require.Equal(t, "tecNO_PERMISSION", result.Code)
	env.Close()
	require.Equal(t, reserve(env, 0)+reserve(env, 0), env.Balance(bob))

	// Take bob back to 0 XRP.
	// rippled: env(noop(bob), fee(env.balance(bob, XRP)));
	env.NoopWithFee(bob, env.Balance(bob))
	env.Close()
	require.Equal(t, uint64(0), env.Balance(bob))

	// bob should not be able to clear lsfDepositAuth (terINSUF_FEE_B).
	bobSeq := env.Seq(bob)
	clearDepositAuth := accountset.AccountSet(bob).
		ClearFlag(accounttx.AccountSetFlagDepositAuth).
		Fee(baseFee).
		Build()
	result = env.Submit(clearDepositAuth)
	require.Equal(t, "terINSUF_FEE_B", result.Code)
	env.Close()
	require.Zero(t, env.Balance(bob))
	require.Equal(t, bobSeq, env.Seq(bob))
	jtx.RequireFlagSet(t, env, bob, state.LsfDepositAuth)

	// Pay bob 1 drop – should succeed when balance is at or below reserve.
	result = env.Submit(payment.Pay(alice, bob, 1).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()
	require.Equal(t, uint64(1), env.Balance(bob))
	require.Equal(t, bobSeq, env.Seq(bob))
	jtx.RequireFlagSet(t, env, bob, state.LsfDepositAuth)

	// Funding the remaining fee causes the held AccountSet to retry and clear the flag.
	result = env.Submit(payment.Pay(alice, bob, baseFee-1).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()
	require.Zero(t, env.Balance(bob))
	require.Equal(t, bobSeq+1, env.Seq(bob))
	jtx.RequireFlagNotSet(t, env, bob, state.LsfDepositAuth)

	result = env.Submit(payment.Pay(alice, bob, reserve(env, 0)+1).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()
	require.Equal(t, reserve(env, 0)+1, env.Balance(bob))
}

// --------------------------------------------------------------------------
// testNoRipple
// Reference: rippled DepositAuth_test::testNoRipple (lines 282-380)
// --------------------------------------------------------------------------

func TestDepositAuth_NoRipple(t *testing.T) {
	// DepositAuth does not change any behaviours regarding rippling and NoRipple.
	// This test demonstrates that through all 8 combinations of:
	//   noRipplePrev × noRippleNext × withDepositAuth

	gw1 := jtx.NewAccount("gw1")
	gw2 := jtx.NewAccount("gw2")
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")

	testIssuer := func(t *testing.T, noRipplePrev, noRippleNext, withDepositAuth bool) {
		env := jtx.NewTestEnv(t)

		env.FundAmount(gw1, uint64(jtx.XRP(10000)))
		env.FundAmount(alice, uint64(jtx.XRP(10000)))
		env.FundAmount(bob, uint64(jtx.XRP(10000)))
		env.Close()

		// gw1 trusts alice["USD"] with optional noRipple
		aliceTrust := trustset.TrustLine(gw1, "USD", alice, "10")
		if noRipplePrev {
			aliceTrust = aliceTrust.NoRipple()
		}
		result := env.Submit(aliceTrust.Build())
		jtx.RequireTxSuccess(t, result)

		// gw1 trusts bob["USD"] with optional noRipple
		bobTrust := trustset.TrustLine(gw1, "USD", bob, "10")
		if noRippleNext {
			bobTrust = bobTrust.NoRipple()
		}
		result = env.Submit(bobTrust.Build())
		jtx.RequireTxSuccess(t, result)

		result = env.Submit(trustset.TrustLine(alice, "USD", gw1, "10").Build())
		jtx.RequireTxSuccess(t, result)
		result = env.Submit(trustset.TrustLine(bob, "USD", gw1, "10").Build())
		jtx.RequireTxSuccess(t, result)

		usd10 := tx.NewIssuedAmountFromFloat64(10, "USD", gw1.Address)
		result = env.Submit(payment.PayIssued(gw1, alice, usd10).Build())
		jtx.RequireTxSuccess(t, result)

		if withDepositAuth {
			env.EnableDepositAuth(gw1)
		}
		env.Close()
		if withDepositAuth {
			jtx.RequireFlagSet(t, env, gw1, state.LsfDepositAuth)
		} else {
			jtx.RequireFlagNotSet(t, env, gw1, state.LsfDepositAuth)
		}
		require.InDelta(t, 10, env.BalanceIOU(alice, "USD", gw1), 1e-10)
		require.Zero(t, env.BalanceIOU(bob, "USD", gw1))

		// Expected result: tecPATH_DRY if both noRipple flags are set, tesSUCCESS otherwise.
		expectedCode := "tesSUCCESS"
		if noRippleNext && noRipplePrev {
			expectedCode = "tecPATH_DRY"
		}

		// Use explicit path through gw1 (matching rippled: path(gw1))
		gw1Path := [][]paymentPkg.PathStep{{{Account: gw1.Address}}}
		result = env.Submit(
			payment.PayIssued(alice, bob, usd10).Paths(gw1Path).Build(),
		)
		require.Equal(t, expectedCode, result.Code,
			"noRipplePrev=%v noRippleNext=%v withDepositAuth=%v",
			noRipplePrev, noRippleNext, withDepositAuth)
		env.Close()
		if expectedCode == "tesSUCCESS" {
			require.Zero(t, env.BalanceIOU(alice, "USD", gw1))
			require.InDelta(t, 10, env.BalanceIOU(bob, "USD", gw1), 1e-10)
		} else {
			require.InDelta(t, 10, env.BalanceIOU(alice, "USD", gw1), 1e-10)
			require.Zero(t, env.BalanceIOU(bob, "USD", gw1))
		}
	}

	testNonIssuer := func(t *testing.T, noRipplePrev, noRippleNext, withDepositAuth bool) {
		env := jtx.NewTestEnv(t)

		env.FundAmount(gw1, uint64(jtx.XRP(10000)))
		env.FundAmount(gw2, uint64(jtx.XRP(10000)))
		env.FundAmount(alice, uint64(jtx.XRP(10000)))
		env.Close()

		usd1Trust := trustset.TrustLine(alice, "USD", gw1, "10")
		if noRipplePrev {
			usd1Trust = usd1Trust.NoRipple()
		}
		result := env.Submit(usd1Trust.Build())
		jtx.RequireTxSuccess(t, result)

		usd2Trust := trustset.TrustLine(alice, "USD", gw2, "10")
		if noRippleNext {
			usd2Trust = usd2Trust.NoRipple()
		}
		result = env.Submit(usd2Trust.Build())
		jtx.RequireTxSuccess(t, result)

		usd2_10 := tx.NewIssuedAmountFromFloat64(10, "USD", gw2.Address)
		result = env.Submit(payment.PayIssued(gw2, alice, usd2_10).Build())
		jtx.RequireTxSuccess(t, result)

		if withDepositAuth {
			env.EnableDepositAuth(alice)
		}
		env.Close()
		if withDepositAuth {
			jtx.RequireFlagSet(t, env, alice, state.LsfDepositAuth)
		} else {
			jtx.RequireFlagNotSet(t, env, alice, state.LsfDepositAuth)
		}
		require.Zero(t, env.BalanceIOU(alice, "USD", gw1))
		require.InDelta(t, 10, env.BalanceIOU(alice, "USD", gw2), 1e-10)

		expectedCode := "tesSUCCESS"
		if noRippleNext && noRipplePrev {
			expectedCode = "tecPATH_DRY"
		}

		usd1_10 := tx.NewIssuedAmountFromFloat64(10, "USD", gw1.Address)
		usd2_10_pay := tx.NewIssuedAmountFromFloat64(10, "USD", gw2.Address)
		// Use explicit path through alice (matching rippled: path(alice), sendmax(USD1(10)))
		alicePath := [][]paymentPkg.PathStep{{{Account: alice.Address}}}
		result = env.Submit(
			payment.PayIssued(gw1, gw2, usd2_10_pay).
				SendMax(usd1_10).
				Paths(alicePath).
				Build(),
		)
		require.Equal(t, expectedCode, result.Code,
			"noRipplePrev=%v noRippleNext=%v withDepositAuth=%v",
			noRipplePrev, noRippleNext, withDepositAuth)
		env.Close()
		if expectedCode == "tesSUCCESS" {
			require.InDelta(t, 10, env.BalanceIOU(alice, "USD", gw1), 1e-10)
			require.Zero(t, env.BalanceIOU(alice, "USD", gw2))
		} else {
			require.Zero(t, env.BalanceIOU(alice, "USD", gw1))
			require.InDelta(t, 10, env.BalanceIOU(alice, "USD", gw2), 1e-10)
		}
	}

	// Test every combination of noRipplePrev, noRippleNext, and withDepositAuth.
	for i := range 8 {
		noRipplePrev := (i & 0x1) != 0
		noRippleNext := (i & 0x2) != 0
		withDepositAuth := (i & 0x4) != 0

		name := func(issuer bool) string {
			s := "Issuer"
			if !issuer {
				s = "NonIssuer"
			}
			if noRipplePrev {
				s += "_NRP"
			}
			if noRippleNext {
				s += "_NRN"
			}
			if withDepositAuth {
				s += "_DA"
			}
			return s
		}

		t.Run(name(true), func(t *testing.T) {
			testIssuer(t, noRipplePrev, noRippleNext, withDepositAuth)
		})

		t.Run(name(false), func(t *testing.T) {
			testNonIssuer(t, noRipplePrev, noRippleNext, withDepositAuth)
		})
	}
}
