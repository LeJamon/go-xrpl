// Package payment contains integration tests for payment behavior.
// Tests ported from rippled's DeliverMin_test.cpp
package payment

import (
	"testing"

	"github.com/LeJamon/goXRPLd/internal/tx"
	xrplgoTesting "github.com/LeJamon/goXRPLd/internal/testing"
	"github.com/LeJamon/goXRPLd/internal/testing/trustset"
	"github.com/stretchr/testify/require"
)

// TestDeliverMin_WithoutPartialPayment tests that delivermin requires tfPartialPayment flag.
// From rippled: delivermin without tfPartialPayment should fail with temBAD_AMOUNT
func TestDeliverMin_WithoutPartialPayment(t *testing.T) {
	env := xrplgoTesting.NewTestEnv(t)

	gw := xrplgoTesting.NewAccount("gateway")
	alice := xrplgoTesting.NewAccount("alice")
	bob := xrplgoTesting.NewAccount("bob")

	env.FundAmount(gw, uint64(xrplgoTesting.XRP(10000)))
	env.FundAmount(alice, uint64(xrplgoTesting.XRP(10000)))
	env.FundAmount(bob, uint64(xrplgoTesting.XRP(10000)))
	env.Close()

	// Create trust lines
	result := env.Submit(trustset.TrustLine(alice, "USD", gw, "100").Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	result = env.Submit(trustset.TrustLine(bob, "USD", gw, "100").Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// DeliverMin without tfPartialPayment should fail with temBAD_AMOUNT
	usd10 := tx.NewIssuedAmountFromFloat64(10, "USD", gw.Address)
	result = env.Submit(PayIssued(alice, bob, usd10).
		DeliverMin(usd10).
		Build())
	require.Equal(t, "temBAD_AMOUNT", result.Code,
		"DeliverMin without tfPartialPayment should fail with temBAD_AMOUNT")

	t.Log("DeliverMin without partial payment test passed")
}

// TestDeliverMin_NegativeAmount tests that negative delivermin fails.
// From rippled: negative delivermin should fail with temBAD_AMOUNT
func TestDeliverMin_NegativeAmount(t *testing.T) {
	env := xrplgoTesting.NewTestEnv(t)

	gw := xrplgoTesting.NewAccount("gateway")
	alice := xrplgoTesting.NewAccount("alice")
	bob := xrplgoTesting.NewAccount("bob")

	env.FundAmount(gw, uint64(xrplgoTesting.XRP(10000)))
	env.FundAmount(alice, uint64(xrplgoTesting.XRP(10000)))
	env.FundAmount(bob, uint64(xrplgoTesting.XRP(10000)))
	env.Close()

	// Create trust lines
	result := env.Submit(trustset.TrustLine(alice, "USD", gw, "100").Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	result = env.Submit(trustset.TrustLine(bob, "USD", gw, "100").Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// Negative DeliverMin should fail with temBAD_AMOUNT
	usd10 := tx.NewIssuedAmountFromFloat64(10, "USD", gw.Address)
	negativeUsd5 := tx.NewIssuedAmountFromFloat64(-5, "USD", gw.Address)
	result = env.Submit(PayIssued(alice, bob, usd10).
		DeliverMin(negativeUsd5).
		PartialPayment().
		Build())
	require.Equal(t, "temBAD_AMOUNT", result.Code,
		"Negative DeliverMin should fail with temBAD_AMOUNT")

	t.Log("DeliverMin negative amount test passed")
}

// TestDeliverMin_CurrencyMismatch tests that delivermin currency must match amount currency.
// From rippled: delivermin with different currency should fail with temBAD_AMOUNT
func TestDeliverMin_CurrencyMismatch(t *testing.T) {
	env := xrplgoTesting.NewTestEnv(t)

	gw := xrplgoTesting.NewAccount("gateway")
	alice := xrplgoTesting.NewAccount("alice")
	bob := xrplgoTesting.NewAccount("bob")

	env.FundAmount(gw, uint64(xrplgoTesting.XRP(10000)))
	env.FundAmount(alice, uint64(xrplgoTesting.XRP(10000)))
	env.FundAmount(bob, uint64(xrplgoTesting.XRP(10000)))
	env.Close()

	// Create trust lines
	result := env.Submit(trustset.TrustLine(alice, "USD", gw, "100").Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	result = env.Submit(trustset.TrustLine(bob, "USD", gw, "100").Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// DeliverMin with different currency (XRP vs USD) should fail with temBAD_AMOUNT
	usd10 := tx.NewIssuedAmountFromFloat64(10, "USD", gw.Address)
	xrp5 := tx.NewXRPAmount(5_000_000)
	result = env.Submit(PayIssued(alice, bob, usd10).
		DeliverMin(xrp5).
		PartialPayment().
		Build())
	require.Equal(t, "temBAD_AMOUNT", result.Code,
		"DeliverMin with different currency should fail with temBAD_AMOUNT")

	t.Log("DeliverMin currency mismatch test passed")
}

// TestDeliverMin_IssuerMismatch tests that delivermin issuer must match amount issuer.
// From rippled: delivermin with different issuer should fail with temBAD_AMOUNT
func TestDeliverMin_IssuerMismatch(t *testing.T) {
	env := xrplgoTesting.NewTestEnv(t)

	gw := xrplgoTesting.NewAccount("gateway")
	carol := xrplgoTesting.NewAccount("carol")
	alice := xrplgoTesting.NewAccount("alice")
	bob := xrplgoTesting.NewAccount("bob")

	env.FundAmount(gw, uint64(xrplgoTesting.XRP(10000)))
	env.FundAmount(carol, uint64(xrplgoTesting.XRP(10000)))
	env.FundAmount(alice, uint64(xrplgoTesting.XRP(10000)))
	env.FundAmount(bob, uint64(xrplgoTesting.XRP(10000)))
	env.Close()

	// Create trust lines
	result := env.Submit(trustset.TrustLine(alice, "USD", gw, "100").Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	result = env.Submit(trustset.TrustLine(bob, "USD", gw, "100").Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// DeliverMin with different issuer should fail with temBAD_AMOUNT
	usd10 := tx.NewIssuedAmountFromFloat64(10, "USD", gw.Address)
	carolUsd5 := tx.NewIssuedAmountFromFloat64(5, "USD", carol.Address)
	result = env.Submit(PayIssued(alice, bob, usd10).
		DeliverMin(carolUsd5).
		PartialPayment().
		Build())
	require.Equal(t, "temBAD_AMOUNT", result.Code,
		"DeliverMin with different issuer should fail with temBAD_AMOUNT")

	t.Log("DeliverMin issuer mismatch test passed")
}

// TestDeliverMin_ExceedsAmount tests that delivermin cannot exceed amount.
// From rippled: delivermin greater than amount should fail with temBAD_AMOUNT
func TestDeliverMin_ExceedsAmount(t *testing.T) {
	env := xrplgoTesting.NewTestEnv(t)

	gw := xrplgoTesting.NewAccount("gateway")
	alice := xrplgoTesting.NewAccount("alice")
	bob := xrplgoTesting.NewAccount("bob")

	env.FundAmount(gw, uint64(xrplgoTesting.XRP(10000)))
	env.FundAmount(alice, uint64(xrplgoTesting.XRP(10000)))
	env.FundAmount(bob, uint64(xrplgoTesting.XRP(10000)))
	env.Close()

	// Create trust lines
	result := env.Submit(trustset.TrustLine(alice, "USD", gw, "100").Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	result = env.Submit(trustset.TrustLine(bob, "USD", gw, "100").Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// DeliverMin > Amount should fail with temBAD_AMOUNT
	usd10 := tx.NewIssuedAmountFromFloat64(10, "USD", gw.Address)
	usd15 := tx.NewIssuedAmountFromFloat64(15, "USD", gw.Address)
	result = env.Submit(PayIssued(alice, bob, usd10).
		DeliverMin(usd15).
		PartialPayment().
		Build())
	require.Equal(t, "temBAD_AMOUNT", result.Code,
		"DeliverMin > Amount should fail with temBAD_AMOUNT")

	t.Log("DeliverMin exceeds amount test passed")
}

// TestDeliverMin_PathPartial tests partial payment via path with delivermin.
// From rippled: partial payment that doesn't meet delivermin should fail with tecPATH_PARTIAL
func TestDeliverMin_PathPartial(t *testing.T) {
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

	// Create trust lines
	result := env.Submit(trustset.TrustLine(alice, "USD", gw, "100").Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	result = env.Submit(trustset.TrustLine(bob, "USD", gw, "100").Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	result = env.Submit(trustset.TrustLine(carol, "USD", gw, "100").Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// Pay carol some USD
	usd50 := tx.NewIssuedAmountFromFloat64(50, "USD", gw.Address)
	result = env.Submit(PayIssued(gw, carol, usd50).Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// Carol creates offer: sell 5 USD for 5 XRP
	usd5 := tx.NewIssuedAmountFromFloat64(5, "USD", gw.Address)
	xrp5 := tx.NewXRPAmount(5_000_000)
	result = env.CreateOffer(carol, usd5, xrp5) // TakerGets=USD, TakerPays=XRP
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// Alice tries to pay bob 10 USD via XRP path with delivermin of 7 USD
	// SendMax is 5 XRP, but the offer can only provide 5 USD
	// This should fail with tecPATH_PARTIAL
	usd10 := tx.NewIssuedAmountFromFloat64(10, "USD", gw.Address)
	usd7 := tx.NewIssuedAmountFromFloat64(7, "USD", gw.Address)
	result = env.Submit(PayIssued(alice, bob, usd10).
		PathsXRP().
		DeliverMin(usd7).
		PartialPayment().
		SendMax(xrp5).
		Build())
	require.Equal(t, "tecPATH_PARTIAL", result.Code,
		"Payment with insufficient path liquidity should fail with tecPATH_PARTIAL")

	t.Log("DeliverMin path partial test passed")
}

// TestDeliverMin_SelfPayment tests self-payment with delivermin.
// From rippled: alice can pay herself via offer, converting all available liquidity
func TestDeliverMin_SelfPayment(t *testing.T) {

	env := xrplgoTesting.NewTestEnv(t)

	gw := xrplgoTesting.NewAccount("gateway")
	alice := xrplgoTesting.NewAccount("alice")
	bob := xrplgoTesting.NewAccount("bob")

	env.FundAmount(gw, uint64(xrplgoTesting.XRP(10000)))
	env.FundAmount(alice, uint64(xrplgoTesting.XRP(10000)))
	env.FundAmount(bob, uint64(xrplgoTesting.XRP(10000)))
	env.Close()

	// Create trust lines
	result := env.Submit(trustset.TrustLine(alice, "USD", gw, "1000").Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	result = env.Submit(trustset.TrustLine(bob, "USD", gw, "1000").Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// Fund bob with USD
	usd100 := tx.NewIssuedAmountFromFloat64(100, "USD", gw.Address)
	result = env.Submit(PayIssued(gw, bob, usd100).Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// Bob creates an offer: sell 100 USD for 100 XRP
	xrp100 := tx.NewXRPAmount(100_000_000)
	result = env.CreateOffer(bob, usd100, xrp100) // TakerGets=USD, TakerPays=XRP
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// Alice pays herself USD through XRP path (cross-currency self-payment)
	// This acquires USD from bob's offer
	usd10000 := tx.NewIssuedAmountFromFloat64(10000, "USD", gw.Address)
	result = env.Submit(PayIssued(alice, alice, usd10000).
		PathsXRP().
		DeliverMin(usd100).
		PartialPayment().
		SendMax(xrp100).
		Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// alice should now have 100 USD
	aliceUsdBalance := env.BalanceIOU(alice, "USD", gw)
	require.Equal(t, 100.0, aliceUsdBalance, "Alice should have 100 USD after self-payment")

	t.Log("DeliverMin self-payment test passed")
}

// TestDeliverMin_MultipleOffers tests delivermin with multiple offers.
// From rippled: payment should consume multiple offers to meet delivermin
func TestDeliverMin_MultipleOffers(t *testing.T) {

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

	// Create trust lines
	result := env.Submit(trustset.TrustLine(bob, "USD", gw, "1000").Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	result = env.Submit(trustset.TrustLine(carol, "USD", gw, "1000").Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// Fund bob with USD
	usd200 := tx.NewIssuedAmountFromFloat64(200, "USD", gw.Address)
	result = env.Submit(PayIssued(gw, bob, usd200).Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// Bob creates multiple offers at different rates
	// Offer 1: sell 100 USD for 100 XRP (1:1)
	usd100 := tx.NewIssuedAmountFromFloat64(100, "USD", gw.Address)
	xrp100 := tx.NewXRPAmount(100_000_000)
	result = env.CreateOffer(bob, usd100, xrp100)
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// Offer 2: sell 100 USD for 1000 XRP (10:1)
	xrp1000 := tx.NewXRPAmount(1000_000_000)
	result = env.CreateOffer(bob, usd100, xrp1000)
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// Offer 3: sell 100 USD for 10000 XRP (100:1) - bob doesn't have this much USD left
	xrp10000 := tx.NewXRPAmount(10000_000_000)
	result = env.CreateOffer(bob, usd100, xrp10000)
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// Alice tries to pay carol USD via XRP path with delivermin of 200 USD
	// With sendmax(1000 XRP), should fail - first offer gives 100 USD for 100 XRP,
	// leaving 900 XRP which isn't enough for second offer (1000 XRP needed)
	usd10000 := tx.NewIssuedAmountFromFloat64(10000, "USD", gw.Address)
	result = env.Submit(PayIssued(alice, carol, usd10000).
		PathsXRP().
		DeliverMin(usd200).
		PartialPayment().
		SendMax(xrp1000).
		Build())
	require.Equal(t, "tecPATH_PARTIAL", result.Code,
		"Payment with insufficient sendmax should fail with tecPATH_PARTIAL")

	// With sendmax(1100 XRP), should succeed - consuming both first and second offers
	xrp1100 := tx.NewXRPAmount(1100_000_000)
	result = env.Submit(PayIssued(alice, carol, usd10000).
		PathsXRP().
		DeliverMin(usd200).
		PartialPayment().
		SendMax(xrp1100).
		Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// Bob should have 0 USD (all sold through offers)
	bobUsdBalance := env.BalanceIOU(bob, "USD", gw)
	require.Equal(t, 0.0, bobUsdBalance, "Bob should have 0 USD after offers consumed")

	// Carol should have 200 USD
	carolUsdBalance := env.BalanceIOU(carol, "USD", gw)
	require.Equal(t, 200.0, carolUsdBalance, "Carol should have 200 USD")

	t.Log("DeliverMin multiple offers test passed")
}

// TestDeliverMin_MultipleProviders tests delivermin with multiple liquidity providers.
// From rippled: payment should consume offers from multiple providers
func TestDeliverMin_MultipleProviders(t *testing.T) {

	env := xrplgoTesting.NewTestEnv(t)

	gw := xrplgoTesting.NewAccount("gateway")
	alice := xrplgoTesting.NewAccount("alice")
	bob := xrplgoTesting.NewAccount("bob")
	carol := xrplgoTesting.NewAccount("carol")
	dan := xrplgoTesting.NewAccount("dan")

	env.FundAmount(gw, uint64(xrplgoTesting.XRP(10000)))
	env.FundAmount(alice, uint64(xrplgoTesting.XRP(10000)))
	env.FundAmount(bob, uint64(xrplgoTesting.XRP(10000)))
	env.FundAmount(carol, uint64(xrplgoTesting.XRP(10000)))
	env.FundAmount(dan, uint64(xrplgoTesting.XRP(10000)))
	env.Close()

	// Create trust lines
	result := env.Submit(trustset.TrustLine(bob, "USD", gw, "1000").Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	result = env.Submit(trustset.TrustLine(carol, "USD", gw, "1000").Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	result = env.Submit(trustset.TrustLine(dan, "USD", gw, "1000").Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// Fund bob and dan with USD
	usd100 := tx.NewIssuedAmountFromFloat64(100, "USD", gw.Address)
	result = env.Submit(PayIssued(gw, bob, usd100).Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	result = env.Submit(PayIssued(gw, dan, usd100).Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// Bob creates offers
	xrp100 := tx.NewXRPAmount(100_000_000)
	xrp1000 := tx.NewXRPAmount(1000_000_000)
	result = env.CreateOffer(bob, usd100, xrp100) // Sell 100 USD for 100 XRP
	xrplgoTesting.RequireTxSuccess(t, result)
	result = env.CreateOffer(bob, usd100, xrp1000) // This won't be used (bob only has 100 USD)
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// Dan creates an offer
	result = env.CreateOffer(dan, usd100, xrp100) // Sell 100 USD for 100 XRP
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// Alice pays carol USD with sendmax of 200 XRP
	// Should consume both bob's and dan's offers (100 + 100 = 200 USD)
	xrp200 := tx.NewXRPAmount(200_000_000)
	usd200 := tx.NewIssuedAmountFromFloat64(200, "USD", gw.Address)
	usd10000 := tx.NewIssuedAmountFromFloat64(10000, "USD", gw.Address)
	result = env.Submit(PayIssued(alice, carol, usd10000).
		PathsXRP().
		DeliverMin(usd200).
		PartialPayment().
		SendMax(xrp200).
		Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// Bob should have 0 USD
	bobUsdBalance := env.BalanceIOU(bob, "USD", gw)
	require.Equal(t, 0.0, bobUsdBalance, "Bob should have 0 USD")

	// Carol should have 200 USD
	carolUsdBalance := env.BalanceIOU(carol, "USD", gw)
	require.Equal(t, 200.0, carolUsdBalance, "Carol should have 200 USD")

	// Dan should have 0 USD
	danUsdBalance := env.BalanceIOU(dan, "USD", gw)
	require.Equal(t, 0.0, danUsdBalance, "Dan should have 0 USD")

	t.Log("DeliverMin multiple providers test passed")
}
