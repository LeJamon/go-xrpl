// Package payment contains integration tests for payment behavior.
// Tests ported from rippled's Flow_test.cpp
package payment

import (
	"testing"

	"github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/internal/tx/payment"
	xrplgoTesting "github.com/LeJamon/goXRPLd/internal/testing"
	"github.com/LeJamon/goXRPLd/internal/testing/trustset"
	"github.com/stretchr/testify/require"
)

// TestFlow_DirectStep tests direct payment paths.
// From rippled: Flow_test::testDirectStep
func TestFlow_DirectStep(t *testing.T) {
	t.Run("XRP transfer", func(t *testing.T) {
		env := xrplgoTesting.NewTestEnv(t)

		alice := xrplgoTesting.NewAccount("alice")
		bob := xrplgoTesting.NewAccount("bob")

		env.FundAmount(alice, uint64(xrplgoTesting.XRP(10000)))
		env.FundAmount(bob, uint64(xrplgoTesting.XRP(10000)))
		env.Close()

		// Pay XRP directly
		result := env.Submit(Pay(alice, bob, 100_000_000).Build())
		xrplgoTesting.RequireTxSuccess(t, result)
		env.Close()

		// Verify bob received 100 XRP
		bobBalance := env.Balance(bob)
		require.GreaterOrEqual(t, bobBalance, uint64(xrplgoTesting.XRP(10100)),
			"Bob should have at least 10100 XRP")
	})

	t.Run("USD trivial path", func(t *testing.T) {
		env := xrplgoTesting.NewTestEnv(t)

		gw := xrplgoTesting.NewAccount("gateway")
		alice := xrplgoTesting.NewAccount("alice")
		bob := xrplgoTesting.NewAccount("bob")

		env.FundAmount(gw, uint64(xrplgoTesting.XRP(10000)))
		env.FundAmount(alice, uint64(xrplgoTesting.XRP(10000)))
		env.FundAmount(bob, uint64(xrplgoTesting.XRP(10000)))
		env.Close()

		// Set up trust lines
		result := env.Submit(trustset.TrustLine(alice, "USD", gw, "1000").Build())
		xrplgoTesting.RequireTxSuccess(t, result)
		result = env.Submit(trustset.TrustLine(bob, "USD", gw, "1000").Build())
		xrplgoTesting.RequireTxSuccess(t, result)
		env.Close()

		// Fund alice with USD
		usd100 := tx.NewIssuedAmountFromFloat64(100, "USD", gw.Address)
		result = env.Submit(PayIssued(gw, alice, usd100).Build())
		xrplgoTesting.RequireTxSuccess(t, result)
		env.Close()

		// Pay USD from alice to bob
		usd10 := tx.NewIssuedAmountFromFloat64(10, "USD", gw.Address)
		result = env.Submit(PayIssued(alice, bob, usd10).Build())
		xrplgoTesting.RequireTxSuccess(t, result)
		env.Close()

		// Verify bob has 10 USD
		bobUsd := env.BalanceIOU(bob, "USD", gw)
		require.Equal(t, 10.0, bobUsd, "Bob should have 10 USD")

		t.Log("USD trivial path test passed")
	})
}

// TestFlow_PartialPayments tests partial payment scenarios.
// From rippled: Flow_test::testDirectStep (partial payments section)
func TestFlow_PartialPayments(t *testing.T) {
	env := xrplgoTesting.NewTestEnv(t)

	gw := xrplgoTesting.NewAccount("gateway")
	alice := xrplgoTesting.NewAccount("alice")
	bob := xrplgoTesting.NewAccount("bob")

	env.FundAmount(gw, uint64(xrplgoTesting.XRP(10000)))
	env.FundAmount(alice, uint64(xrplgoTesting.XRP(10000)))
	env.FundAmount(bob, uint64(xrplgoTesting.XRP(10000)))
	env.Close()

	// Set up trust lines
	result := env.Submit(trustset.TrustLine(alice, "USD", gw, "1000").Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	result = env.Submit(trustset.TrustLine(bob, "USD", gw, "1000").Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// Fund alice with 100 USD
	usd100 := tx.NewIssuedAmountFromFloat64(100, "USD", gw.Address)
	result = env.Submit(PayIssued(gw, alice, usd100).Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// Try to pay more than alice has - should fail with tecPATH_PARTIAL
	usd110 := tx.NewIssuedAmountFromFloat64(110, "USD", gw.Address)
	result = env.Submit(PayIssued(alice, bob, usd110).Build())
	require.Equal(t, "tecPATH_PARTIAL", result.Code,
		"Payment exceeding balance should fail with tecPATH_PARTIAL")

	t.Log("Flow partial payments test passed")
}

// TestFlow_LineQuality tests line quality/transfer rate.
// From rippled: Flow_test::testLineQuality
func TestFlow_LineQuality(t *testing.T) {
	// Test QualityOut on bob's trust line with alice
	// bob -> alice -> carol; bobAliceQOut varies
	for _, bobAliceQOut := range []uint32{80, 100, 120} {
		t.Run("QualityOut_"+string(rune('0'+bobAliceQOut/10))+string(rune('0'+bobAliceQOut%10)), func(t *testing.T) {
			env := xrplgoTesting.NewTestEnv(t)

			alice := xrplgoTesting.NewAccount("alice")
			bob := xrplgoTesting.NewAccount("bob")
			carol := xrplgoTesting.NewAccount("carol")

			env.FundAmount(alice, uint64(xrplgoTesting.XRP(10000)))
			env.FundAmount(bob, uint64(xrplgoTesting.XRP(10000)))
			env.FundAmount(carol, uint64(xrplgoTesting.XRP(10000)))
			env.Close()

			// bob trusts alice with QualityOut setting
			// QualityOut affects how much bob pays when sending through this line
			qualityOutValue := bobAliceQOut * 10_000_000 // Convert percentage to quality (100% = 1e9)
			bobTrust := trustset.TrustLine(bob, "USD", alice, "10").QualityOut(qualityOutValue)
			result := env.Submit(bobTrust.Build())
			xrplgoTesting.RequireTxSuccess(t, result)

			// carol trusts alice normally
			result = env.Submit(trustset.TrustLine(carol, "USD", alice, "10").Build())
			xrplgoTesting.RequireTxSuccess(t, result)
			env.Close()

			// alice pays bob 10 USD
			usd10 := tx.NewIssuedAmountFromFloat64(10, "USD", alice.Address)
			result = env.Submit(PayIssued(alice, bob, usd10).Build())
			xrplgoTesting.RequireTxSuccess(t, result)
			env.Close()

			// Verify bob has 10 USD
			bobUsd := env.BalanceIOU(bob, "USD", alice)
			require.InDelta(t, 10.0, bobUsd, 0.0001, "Bob should have 10 USD")

			// bob pays carol 5 USD (sendmax 5)
			usd5 := tx.NewIssuedAmountFromFloat64(5, "USD", alice.Address)
			result = env.Submit(PayIssued(bob, carol, usd5).SendMax(usd5).Build())
			xrplgoTesting.RequireTxSuccess(t, result)
			env.Close()

			// Verify carol has 5 USD
			carolUsd := env.BalanceIOU(carol, "USD", alice)
			require.InDelta(t, 5.0, carolUsd, 0.0001, "Carol should have 5 USD")

			// Verify bob has 5 USD remaining (10 - 5)
			bobUsd = env.BalanceIOU(bob, "USD", alice)
			require.InDelta(t, 5.0, bobUsd, 0.0001, "Bob should have 5 USD remaining")
		})
	}

	t.Log("Flow line quality test passed")
}

// TestFlow_BookStep tests book step (offer matching).
// From rippled: Flow_test::testBookStep
func TestFlow_BookStep(t *testing.T) {
	t.Run("IOU/IOU offer", func(t *testing.T) {
		// Simple IOU/IOU offer test from rippled
		env := xrplgoTesting.NewTestEnv(t)

		gw := xrplgoTesting.NewAccount("gateway")
		alice := xrplgoTesting.NewAccount("alice")
		bob := xrplgoTesting.NewAccount("bob")
		carol := xrplgoTesting.NewAccount("carol")

		env.FundAmount(gw, uint64(xrplgoTesting.XRP(10000)))
		env.FundAmount(alice, uint64(xrplgoTesting.XRP(10000)))
		env.FundAmount(bob, uint64(xrplgoTesting.XRP(10000)))
		env.FundAmount(carol, uint64(xrplgoTesting.XRP(10000)))
		env.Close()

		// Setup trust lines for USD and BTC
		result := env.Submit(trustset.TrustLine(alice, "USD", gw, "1000").Build())
		xrplgoTesting.RequireTxSuccess(t, result)
		result = env.Submit(trustset.TrustLine(alice, "BTC", gw, "1000").Build())
		xrplgoTesting.RequireTxSuccess(t, result)
		result = env.Submit(trustset.TrustLine(bob, "USD", gw, "1000").Build())
		xrplgoTesting.RequireTxSuccess(t, result)
		result = env.Submit(trustset.TrustLine(bob, "BTC", gw, "1000").Build())
		xrplgoTesting.RequireTxSuccess(t, result)
		result = env.Submit(trustset.TrustLine(carol, "USD", gw, "1000").Build())
		xrplgoTesting.RequireTxSuccess(t, result)
		result = env.Submit(trustset.TrustLine(carol, "BTC", gw, "1000").Build())
		xrplgoTesting.RequireTxSuccess(t, result)
		env.Close()

		// alice gets 50 BTC, bob gets 50 USD
		btc50 := tx.NewIssuedAmountFromFloat64(50, "BTC", gw.Address)
		usd50 := tx.NewIssuedAmountFromFloat64(50, "USD", gw.Address)
		result = env.Submit(PayIssued(gw, alice, btc50).Build())
		xrplgoTesting.RequireTxSuccess(t, result)
		result = env.Submit(PayIssued(gw, bob, usd50).Build())
		xrplgoTesting.RequireTxSuccess(t, result)
		env.Close()

		// bob creates offer: sell 50 USD for 50 BTC
		result = env.CreateOffer(bob, usd50, btc50) // TakerGets=USD, TakerPays=BTC
		xrplgoTesting.RequireTxSuccess(t, result)
		env.Close()

		// alice pays carol 50 USD using BTC through the offer
		// Use Paths with a path element specifying just the output currency
		// This tells the strand builder to go through the BTC->USD order book
		paymentTx := PayIssued(alice, carol, usd50).
			Paths([][]payment.PathStep{{
				{Currency: "USD", Issuer: gw.Address},
			}}).
			SendMax(btc50).
			Build()
		t.Logf("Payment tx: %+v", paymentTx)
		result = env.Submit(paymentTx)
		xrplgoTesting.RequireTxSuccess(t, result)
		env.Close()

		// Verify balances
		// alice should have 0 BTC (spent 50)
		aliceBtc := env.BalanceIOU(alice, "BTC", gw)
		require.Equal(t, 0.0, aliceBtc, "Alice should have 0 BTC")

		// bob should have 50 BTC (received from offer), 0 USD (sold through offer)
		bobBtc := env.BalanceIOU(bob, "BTC", gw)
		bobUsd := env.BalanceIOU(bob, "USD", gw)
		require.Equal(t, 50.0, bobBtc, "Bob should have 50 BTC from offer")
		require.Equal(t, 0.0, bobUsd, "Bob should have 0 USD after offer consumed")

		// carol should have 50 USD (received from payment)
		carolUsd := env.BalanceIOU(carol, "USD", gw)
		require.Equal(t, 50.0, carolUsd, "Carol should have 50 USD")

		t.Log("IOU/IOU offer test passed")
	})

	t.Run("XRP/IOU offer", func(t *testing.T) {
		// Simple XRP/IOU offer (XRP bridge) test
		env := xrplgoTesting.NewTestEnv(t)

		gw := xrplgoTesting.NewAccount("gateway")
		alice := xrplgoTesting.NewAccount("alice")
		bob := xrplgoTesting.NewAccount("bob")
		carol := xrplgoTesting.NewAccount("carol")

		env.FundAmount(gw, uint64(xrplgoTesting.XRP(10000)))
		env.FundAmount(alice, uint64(xrplgoTesting.XRP(10000)))
		env.FundAmount(bob, uint64(xrplgoTesting.XRP(10000)))
		env.FundAmount(carol, uint64(xrplgoTesting.XRP(10000)))
		env.Close()

		// Setup trust lines for USD
		result := env.Submit(trustset.TrustLine(bob, "USD", gw, "1000").Build())
		xrplgoTesting.RequireTxSuccess(t, result)
		result = env.Submit(trustset.TrustLine(carol, "USD", gw, "1000").Build())
		xrplgoTesting.RequireTxSuccess(t, result)
		env.Close()

		// bob gets 100 USD
		usd100 := tx.NewIssuedAmountFromFloat64(100, "USD", gw.Address)
		result = env.Submit(PayIssued(gw, bob, usd100).Build())
		xrplgoTesting.RequireTxSuccess(t, result)
		env.Close()

		// bob creates offer: sell 100 USD for 100 XRP
		xrp100 := tx.NewXRPAmount(100_000_000)
		result = env.CreateOffer(bob, usd100, xrp100) // TakerGets=USD, TakerPays=XRP
		xrplgoTesting.RequireTxSuccess(t, result)
		env.Close()

		// alice pays carol 100 USD using XRP through the offer
		result = env.Submit(PayIssued(alice, carol, usd100).
			PathsXRP().
			SendMax(xrp100).
			Build())
		xrplgoTesting.RequireTxSuccess(t, result)
		env.Close()

		// Verify bob has 0 USD
		bobUsd := env.BalanceIOU(bob, "USD", gw)
		require.Equal(t, 0.0, bobUsd, "Bob should have 0 USD after offer consumed")

		// carol should have 100 USD
		carolUsd := env.BalanceIOU(carol, "USD", gw)
		require.Equal(t, 100.0, carolUsd, "Carol should have 100 USD")

		t.Log("XRP/IOU offer test passed")
	})
}

// TestFlow_TransferRate tests transfer rate.
// From rippled: Flow_test::testTransferRate
func TestFlow_TransferRate(t *testing.T) {
	env := xrplgoTesting.NewTestEnv(t)

	gw := xrplgoTesting.NewAccount("gateway")
	alice := xrplgoTesting.NewAccount("alice")
	bob := xrplgoTesting.NewAccount("bob")

	env.FundAmount(gw, uint64(xrplgoTesting.XRP(10000)))
	env.FundAmount(alice, uint64(xrplgoTesting.XRP(10000)))
	env.FundAmount(bob, uint64(xrplgoTesting.XRP(10000)))
	env.Close()

	// Set 25% transfer rate on gateway (1.25e9 = 125% means sender pays 25% extra)
	env.SetTransferRate(gw, 1_250_000_000)
	env.Close()

	// Set up trust lines
	result := env.Submit(trustset.TrustLine(alice, "USD", gw, "1000").Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	result = env.Submit(trustset.TrustLine(bob, "USD", gw, "1000").Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// Gateway creates offer: gives USD(125), receives XRP(125)
	// (TakerGets=USD, TakerPays=XRP)
	usd125 := tx.NewIssuedAmountFromFloat64(125, "USD", gw.Address)
	xrp125 := tx.NewXRPAmount(125_000_000) // 125 XRP in drops
	result = env.CreateOffer(gw, usd125, xrp125)
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	aliceBalanceBefore := env.Balance(alice)

	// alice pays bob 100 USD using XRP (sendmax 200 XRP)
	// Due to 25% transfer rate, alice needs to send more XRP
	usd100 := tx.NewIssuedAmountFromFloat64(100, "USD", gw.Address)
	xrp200 := tx.NewXRPAmount(200_000_000)
	paymentTx := PayIssued(alice, bob, usd100).
		PathsXRP().
		SendMax(xrp200).
		Build()
	result = env.Submit(paymentTx)
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// Verify bob received 100 USD
	bobUsd := env.BalanceIOU(bob, "USD", gw)
	require.InDelta(t, 100.0, bobUsd, 0.0001, "Bob should have 100 USD")

	// Verify alice spent 125 XRP (plus tx fee)
	// With 25% transfer rate: to deliver 100 USD, alice needs 125 XRP worth
	aliceBalanceAfter := env.Balance(alice)
	xrpSpent := int64(aliceBalanceBefore) - int64(aliceBalanceAfter)
	// Alice should have spent 125 XRP (125,000,000 drops) + fee (10 drops)
	expectedSpent := int64(125_000_010)
	require.InDelta(t, expectedSpent, xrpSpent, 100, "Alice should have spent ~125 XRP plus fee")

	t.Log("Flow transfer rate test passed")
}

// TestFlow_FalseDryChanges tests false dry changes.
// From rippled: Flow_test::testFalseDryChanges
func TestFlow_FalseDryChanges(t *testing.T) {
	t.Skip("TODO: falseDryChanges requires specific flow engine testing")

	t.Log("Flow false dry changes test")
}

// TestFlow_LimitQuality tests limit quality flag.
// From rippled: Flow_test::testLimitQuality
func TestFlow_LimitQuality(t *testing.T) {
	t.Skip("TODO: limitQuality requires quality limit path finding")

	t.Log("Flow limit quality test")
}

// TestFlow_SelfPayment1 tests first self-payment scenario.
// From rippled: Flow_test::testSelfPayment1
func TestFlow_SelfPayment1(t *testing.T) {
	env := xrplgoTesting.NewTestEnv(t)

	alice := xrplgoTesting.NewAccount("alice")

	env.FundAmount(alice, uint64(xrplgoTesting.XRP(10000)))
	env.Close()

	// Self payment of XRP (without path) should fail with temREDUNDANT
	result := env.Submit(Pay(alice, alice, 100_000_000).Build())
	require.Equal(t, "temREDUNDANT", result.Code,
		"XRP self-payment without path should fail with temREDUNDANT")

	t.Log("Flow self-payment 1 test passed")
}

// TestFlow_SelfPayment2 tests second self-payment scenario with path.
// From rippled: Flow_test::testSelfPayment2
func TestFlow_SelfPayment2(t *testing.T) {
	t.Skip("TODO: Self-payment with path requires path finding")

	t.Log("Flow self-payment 2 test: requires path support")
}

// TestFlow_SelfFundedXRPEndpoint tests self-funded XRP endpoint.
// From rippled: Flow_test::testSelfFundedXRPEndpoint
func TestFlow_SelfFundedXRPEndpoint(t *testing.T) {
	t.Skip("TODO: Self-funded XRP endpoint requires path finding")

	t.Log("Flow self-funded XRP endpoint test")
}

// TestFlow_UnfundedOffer tests unfunded offer scenario.
// From rippled: Flow_test::testUnfundedOffer
func TestFlow_UnfundedOffer(t *testing.T) {
	t.Skip("TODO: Unfunded offer requires DEX offer support")

	t.Log("Flow unfunded offer test")
}

// TestFlow_ReexecuteDirectStep tests re-executing direct step.
// From rippled: Flow_test::testReexecuteDirectStep
func TestFlow_ReexecuteDirectStep(t *testing.T) {
	t.Skip("TODO: ReexecuteDirectStep requires specific flow engine testing")

	t.Log("Flow re-execute direct step test")
}

// TestFlow_RIPD1443 tests RIPD-1443 fix.
// From rippled: Flow_test::testRipd1443
func TestFlow_RIPD1443(t *testing.T) {
	t.Skip("TODO: RIPD-1443 requires specific edge case testing")

	t.Log("Flow RIPD-1443 test")
}

// TestFlow_RIPD1449 tests RIPD-1449 fix.
// From rippled: Flow_test::testRipd1449
func TestFlow_RIPD1449(t *testing.T) {
	t.Skip("TODO: RIPD-1449 requires specific edge case testing")

	t.Log("Flow RIPD-1449 test")
}

// TestFlow_SelfCrossingLowQualityOffer tests self-crossing low quality offer.
// From rippled: Flow_test::testSelfCrossingLowQualityOffer
func TestFlow_SelfCrossingLowQualityOffer(t *testing.T) {
	t.Skip("TODO: Self-crossing offer requires DEX support")

	t.Log("Flow self-crossing low quality offer test")
}

// TestFlow_EmptyStrand tests empty strand scenario.
// From rippled: Flow_test::testEmptyStrand
func TestFlow_EmptyStrand(t *testing.T) {
	t.Skip("TODO: Empty strand requires specific flow engine testing")

	t.Log("Flow empty strand test")
}

// TestFlow_CircularXRP tests circular XRP path.
// From rippled: Flow_test::testCircularXRP
func TestFlow_CircularXRP(t *testing.T) {
	t.Skip("TODO: Circular XRP requires path finding")

	t.Log("Flow circular XRP test")
}

// TestFlow_PaymentWithTicket tests payment using ticket.
// From rippled: Flow_test::testPaymentWithTicket
func TestFlow_PaymentWithTicket(t *testing.T) {
	t.Skip("TODO: Payment with ticket requires Ticket support")

	t.Log("Flow payment with ticket test")
}
