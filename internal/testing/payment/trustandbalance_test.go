// Package payment contains integration tests for payment behavior.
// Tests ported from rippled's TrustAndBalance_test.cpp
package payment

import (
	"testing"

	"github.com/LeJamon/goXRPLd/internal/tx"
	xrplgoTesting "github.com/LeJamon/goXRPLd/internal/testing"
	"github.com/LeJamon/goXRPLd/internal/testing/trustset"
	"github.com/stretchr/testify/require"
)

// TestDirectRipple tests direct IOU payments between two accounts with mutual trust lines.
// This tests direct issuance (sender is issuer) and redemption (dest is issuer).
// Note: Cross-issuer payments (alice sends bob/USD to bob) require Flow and are tested separately.
// From rippled: testDirectRipple (partial)
func TestDirectRipple(t *testing.T) {
	env := xrplgoTesting.NewTestEnv(t)

	alice := xrplgoTesting.NewAccount("alice")
	bob := xrplgoTesting.NewAccount("bob")

	env.FundAmount(alice, uint64(xrplgoTesting.XRP(10000)))
	env.FundAmount(bob, uint64(xrplgoTesting.XRP(10000)))
	env.Close()

	// Set up mutual trust lines
	// alice trusts bob for 600 USD
	result := env.Submit(trustset.TrustLine(alice, "USD", bob, "600").Build())
	xrplgoTesting.RequireTxSuccess(t, result)

	// bob trusts alice for 700 USD
	result = env.Submit(trustset.TrustLine(bob, "USD", alice, "700").Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// Verify trust lines exist
	require.True(t, env.TrustLineExists(alice, bob, "USD"), "Trust line should exist")

	// alice sends bob 24 USD with alice as issuer
	// alice issues alice/USD to bob
	usd24 := tx.NewIssuedAmountFromFloat64(24, "USD", alice.Address)
	result = env.Submit(PayIssued(alice, bob, usd24).Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// Check bob's balance: bob should have 24 alice/USD
	bobBalance := env.IOUBalance(bob, alice, "USD")
	require.NotNil(t, bobBalance, "bob's balance should not be nil")
	t.Logf("bob's alice/USD balance after first payment: %v", bobBalance)

	// Verify bob has 24 alice/USD
	expectedFloat := 24.0
	actualFloat := bobBalance.Float64()
	require.InDelta(t, expectedFloat, actualFloat, 0.0001,
		"bob should have 24 alice/USD, got %v", actualFloat)

	// alice sends bob more alice/USD (alice as issuer)
	usd33 := tx.NewIssuedAmountFromFloat64(33, "USD", alice.Address)
	result = env.Submit(PayIssued(alice, bob, usd33).Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// Check bob's balance: should now be 24 + 33 = 57 alice/USD
	bobBalance = env.IOUBalance(bob, alice, "USD")
	require.NotNil(t, bobBalance, "bob's balance should not be nil")
	t.Logf("bob's alice/USD balance after second payment: %v", bobBalance)

	expectedFloat = 57.0
	actualFloat = bobBalance.Float64()
	require.InDelta(t, expectedFloat, actualFloat, 0.0001,
		"bob should have 57 alice/USD, got %v", actualFloat)

	// bob sends back alice/USD to alice (bob redeems alice's IOUs)
	usd40 := tx.NewIssuedAmountFromFloat64(40, "USD", alice.Address)
	result = env.Submit(PayIssued(bob, alice, usd40).Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// bob's balance should be 57 - 40 = 17 alice/USD
	bobBalance = env.IOUBalance(bob, alice, "USD")
	require.NotNil(t, bobBalance, "bob's balance should not be nil")
	t.Logf("bob's alice/USD balance after redemption: %v", bobBalance)

	expectedFloat = 17.0
	actualFloat = bobBalance.Float64()
	require.InDelta(t, expectedFloat, actualFloat, 0.0001,
		"bob should have 17 alice/USD after redemption, got %v", actualFloat)

	// alice issues to bob's trust limit (700)
	// Need to issue 700 - 17 = 683 more
	usd683 := tx.NewIssuedAmountFromFloat64(683, "USD", alice.Address)
	result = env.Submit(PayIssued(alice, bob, usd683).Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// bob's balance should be 700 (at bob's trust limit)
	bobBalance = env.IOUBalance(bob, alice, "USD")
	require.NotNil(t, bobBalance, "bob's balance should not be nil")
	t.Logf("bob's alice/USD balance at limit: %v", bobBalance)

	expectedFloat = 700.0
	actualFloat = bobBalance.Float64()
	require.InDelta(t, expectedFloat, actualFloat, 0.0001,
		"bob should have 700 alice/USD (at limit), got %v", actualFloat)

	// alice tries to issue past bob's trust limit - should fail
	usd1 := tx.NewIssuedAmountFromFloat64(1, "USD", alice.Address)
	result = env.Submit(PayIssued(alice, bob, usd1).Build())
	require.False(t, result.Success, "Issuance past trust limit should fail")
	t.Logf("Issuance past limit result: %s", result.Code)

	// Balance should remain unchanged
	bobBalance = env.IOUBalance(bob, alice, "USD")
	expectedFloat = 700.0
	actualFloat = bobBalance.Float64()
	require.InDelta(t, expectedFloat, actualFloat, 0.0001,
		"bob's balance should remain 700, got %v", actualFloat)
}

// TestGatewayPayment tests IOU payments via a gateway (issuer).
// Gateway issues tokens to alice, alice sends to bob, bob sends back.
// From rippled: testWithTransferFee (without transfer fee case)
func TestGatewayPayment(t *testing.T) {
	env := xrplgoTesting.NewTestEnv(t)

	gw := xrplgoTesting.NewAccount("gateway")
	alice := xrplgoTesting.NewAccount("alice")
	bob := xrplgoTesting.NewAccount("bob")

	env.FundAmount(gw, uint64(xrplgoTesting.XRP(10000)))
	env.FundAmount(alice, uint64(xrplgoTesting.XRP(10000)))
	env.FundAmount(bob, uint64(xrplgoTesting.XRP(10000)))
	env.Close()

	// Set up trust lines: both alice and bob trust gateway
	result := env.Submit(trustset.TrustLine(alice, "AUD", gw, "100").Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	result = env.Submit(trustset.TrustLine(bob, "AUD", gw, "100").Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// Gateway issues 1 AUD to alice
	aud1 := tx.NewIssuedAmountFromFloat64(1, "AUD", gw.Address)
	result = env.Submit(PayIssued(gw, alice, aud1).Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// Verify alice has 1 gw/AUD
	aliceBalance := env.IOUBalance(alice, gw, "AUD")
	require.NotNil(t, aliceBalance, "alice's balance should not be nil")
	t.Logf("alice's gw/AUD balance after gateway issue: %v", aliceBalance)
	require.InDelta(t, 1.0, aliceBalance.Float64(), 0.0001,
		"alice should have 1 gw/AUD")

	// alice sends bob 1 gw/AUD
	result = env.Submit(PayIssued(alice, bob, aud1).Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// Verify balances
	aliceBalance = env.IOUBalance(alice, gw, "AUD")
	bobBalance := env.IOUBalance(bob, gw, "AUD")
	t.Logf("alice's gw/AUD after transfer: %v", aliceBalance)
	t.Logf("bob's gw/AUD after transfer: %v", bobBalance)

	require.InDelta(t, 0.0, aliceBalance.Float64(), 0.0001,
		"alice should have 0 gw/AUD after transfer")
	require.InDelta(t, 1.0, bobBalance.Float64(), 0.0001,
		"bob should have 1 gw/AUD after transfer")

	// bob sends alice 0.5 gw/AUD
	audHalf := tx.NewIssuedAmountFromFloat64(0.5, "AUD", gw.Address)
	result = env.Submit(PayIssued(bob, alice, audHalf).Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// Verify final balances
	aliceBalance = env.IOUBalance(alice, gw, "AUD")
	bobBalance = env.IOUBalance(bob, gw, "AUD")
	t.Logf("alice's gw/AUD after partial return: %v", aliceBalance)
	t.Logf("bob's gw/AUD after partial return: %v", bobBalance)

	require.InDelta(t, 0.5, aliceBalance.Float64(), 0.0001,
		"alice should have 0.5 gw/AUD")
	require.InDelta(t, 0.5, bobBalance.Float64(), 0.0001,
		"bob should have 0.5 gw/AUD")
}

// TestCreditLimit tests trust line creation and credit limit behavior.
// From rippled: testCreditLimit
func TestCreditLimit(t *testing.T) {
	env := xrplgoTesting.NewTestEnv(t)

	gw := xrplgoTesting.NewAccount("gateway")
	alice := xrplgoTesting.NewAccount("alice")
	bob := xrplgoTesting.NewAccount("bob")

	env.FundAmount(gw, uint64(xrplgoTesting.XRP(10000)))
	env.FundAmount(alice, uint64(xrplgoTesting.XRP(10000)))
	env.FundAmount(bob, uint64(xrplgoTesting.XRP(10000)))
	env.Close()

	// Verify trust line doesn't exist yet
	require.False(t, env.TrustLineExists(gw, alice, "USD"),
		"Trust line should not exist initially")

	// Create a trust line: alice trusts gw for 800 USD
	result := env.Submit(trustset.TrustLine(alice, "USD", gw, "800").Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// Verify trust line exists
	require.True(t, env.TrustLineExists(alice, gw, "USD"),
		"Trust line should exist after creation")

	// Modify the trust line: alice changes limit to 700 USD
	result = env.Submit(trustset.TrustLine(alice, "USD", gw, "700").Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// Set negative limit - should fail
	result = env.Submit(trustset.TrustLine(alice, "USD", gw, "-1").Build())
	require.False(t, result.Success, "Negative trust limit should fail")
	t.Logf("Negative limit result: %s", result.Code)

	// Set zero limit - should delete the trust line
	// This works because the test environment enables DefaultRipple on all accounts
	// (matching rippled's behavior), which allows the trust line to be deleted
	// when both limits are 0 and balance is 0.
	result = env.Submit(trustset.TrustLine(alice, "USD", gw, "0").Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// Verify trust line was deleted
	require.False(t, env.TrustLineExists(alice, gw, "USD"),
		"Trust line should be deleted when limit is set to 0")

	// Set up bidirectional trust between alice and bob
	result = env.Submit(trustset.TrustLine(alice, "USD", bob, "600").Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	result = env.Submit(trustset.TrustLine(bob, "USD", alice, "500").Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// Verify both trust lines are represented by a single RippleState entry
	require.True(t, env.TrustLineExists(alice, bob, "USD"),
		"Bidirectional trust line should exist")
}

// TestIndirectPayment tests payment through a common gateway.
// Alice and bob both trust gateway, alice pays bob via gateway.
// From rippled: testIndirect
func TestIndirectPayment(t *testing.T) {
	env := xrplgoTesting.NewTestEnv(t)

	gw := xrplgoTesting.NewAccount("gateway")
	alice := xrplgoTesting.NewAccount("alice")
	bob := xrplgoTesting.NewAccount("bob")

	env.FundAmount(gw, uint64(xrplgoTesting.XRP(10000)))
	env.FundAmount(alice, uint64(xrplgoTesting.XRP(10000)))
	env.FundAmount(bob, uint64(xrplgoTesting.XRP(10000)))
	env.Close()

	// Set up trust lines
	result := env.Submit(trustset.TrustLine(alice, "USD", gw, "600").Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	result = env.Submit(trustset.TrustLine(bob, "USD", gw, "700").Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// Gateway issues USD to both alice and bob
	usd70 := tx.NewIssuedAmountFromFloat64(70, "USD", gw.Address)
	result = env.Submit(PayIssued(gw, alice, usd70).Build())
	xrplgoTesting.RequireTxSuccess(t, result)

	usd50 := tx.NewIssuedAmountFromFloat64(50, "USD", gw.Address)
	result = env.Submit(PayIssued(gw, bob, usd50).Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// Verify initial balances
	aliceBalance := env.IOUBalance(alice, gw, "USD")
	bobBalance := env.IOUBalance(bob, gw, "USD")
	require.InDelta(t, 70.0, aliceBalance.Float64(), 0.0001,
		"alice should have 70 gw/USD")
	require.InDelta(t, 50.0, bobBalance.Float64(), 0.0001,
		"bob should have 50 gw/USD")

	// alice tries to send more than she has to issuer - should fail
	usd100 := tx.NewIssuedAmountFromFloat64(100, "USD", gw.Address)
	result = env.Submit(PayIssued(alice, gw, usd100).Build())
	require.False(t, result.Success, "Payment of more than balance should fail")
	t.Logf("Overpay to issuer result: %s", result.Code)

	// alice tries to send more than she has to bob - should fail
	result = env.Submit(PayIssued(alice, bob, usd100).Build())
	require.False(t, result.Success, "Payment of more than balance should fail")
	t.Logf("Overpay to bob result: %s", result.Code)
	env.Close()

	// Verify balances unchanged
	aliceBalance = env.IOUBalance(alice, gw, "USD")
	bobBalance = env.IOUBalance(bob, gw, "USD")
	require.InDelta(t, 70.0, aliceBalance.Float64(), 0.0001,
		"alice should still have 70 gw/USD")
	require.InDelta(t, 50.0, bobBalance.Float64(), 0.0001,
		"bob should still have 50 gw/USD")

	// alice sends bob 5 gw/USD (indirect payment through gateway)
	usd5 := tx.NewIssuedAmountFromFloat64(5, "USD", gw.Address)
	result = env.Submit(PayIssued(alice, bob, usd5).Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// Verify final balances
	aliceBalance = env.IOUBalance(alice, gw, "USD")
	bobBalance = env.IOUBalance(bob, gw, "USD")
	t.Logf("alice's balance after indirect payment: %v", aliceBalance)
	t.Logf("bob's balance after indirect payment: %v", bobBalance)

	require.InDelta(t, 65.0, aliceBalance.Float64(), 0.0001,
		"alice should have 65 gw/USD")
	require.InDelta(t, 55.0, bobBalance.Float64(), 0.0001,
		"bob should have 55 gw/USD")
}

// TestIssuerRedemption tests that an account can send tokens back to the issuer.
// This is the basic "redeem" scenario where tokens are returned to the issuer.
func TestIssuerRedemption(t *testing.T) {
	env := xrplgoTesting.NewTestEnv(t)

	gw := xrplgoTesting.NewAccount("gateway")
	alice := xrplgoTesting.NewAccount("alice")

	env.FundAmount(gw, uint64(xrplgoTesting.XRP(10000)))
	env.FundAmount(alice, uint64(xrplgoTesting.XRP(10000)))
	env.Close()

	// alice trusts gateway
	result := env.Submit(trustset.TrustLine(alice, "USD", gw, "1000").Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// Gateway issues 100 USD to alice
	usd100 := tx.NewIssuedAmountFromFloat64(100, "USD", gw.Address)
	result = env.Submit(PayIssued(gw, alice, usd100).Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// Verify alice has 100 gw/USD
	aliceBalance := env.IOUBalance(alice, gw, "USD")
	require.InDelta(t, 100.0, aliceBalance.Float64(), 0.0001,
		"alice should have 100 gw/USD")

	// alice redeems 50 USD by sending back to gateway
	usd50 := tx.NewIssuedAmountFromFloat64(50, "USD", gw.Address)
	result = env.Submit(PayIssued(alice, gw, usd50).Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// Verify alice has 50 gw/USD remaining
	aliceBalance = env.IOUBalance(alice, gw, "USD")
	t.Logf("alice's balance after redemption: %v", aliceBalance)
	require.InDelta(t, 50.0, aliceBalance.Float64(), 0.0001,
		"alice should have 50 gw/USD after redemption")

	// alice redeems remaining 50 USD
	result = env.Submit(PayIssued(alice, gw, usd50).Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// Verify alice has 0 gw/USD
	aliceBalance = env.IOUBalance(alice, gw, "USD")
	t.Logf("alice's balance after full redemption: %v", aliceBalance)
	require.InDelta(t, 0.0, aliceBalance.Float64(), 0.0001,
		"alice should have 0 gw/USD after full redemption")
}

// TestSelfIssuance tests that an account can issue its own currency.
// The issuer can send their own IOUs to anyone who trusts them.
func TestSelfIssuance(t *testing.T) {
	env := xrplgoTesting.NewTestEnv(t)

	issuer := xrplgoTesting.NewAccount("issuer")
	holder := xrplgoTesting.NewAccount("holder")

	env.FundAmount(issuer, uint64(xrplgoTesting.XRP(10000)))
	env.FundAmount(holder, uint64(xrplgoTesting.XRP(10000)))
	env.Close()

	// holder trusts issuer for 1000 USD
	result := env.Submit(trustset.TrustLine(holder, "USD", issuer, "1000").Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// issuer can create tokens by sending to holder
	usd500 := tx.NewIssuedAmountFromFloat64(500, "USD", issuer.Address)
	result = env.Submit(PayIssued(issuer, holder, usd500).Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// Verify holder has 500 issuer/USD
	holderBalance := env.IOUBalance(holder, issuer, "USD")
	t.Logf("holder's balance after issuance: %v", holderBalance)
	require.InDelta(t, 500.0, holderBalance.Float64(), 0.0001,
		"holder should have 500 issuer/USD")

	// issuer can issue more up to the trust limit
	usd500more := tx.NewIssuedAmountFromFloat64(500, "USD", issuer.Address)
	result = env.Submit(PayIssued(issuer, holder, usd500more).Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// Verify holder is at limit
	holderBalance = env.IOUBalance(holder, issuer, "USD")
	t.Logf("holder's balance at limit: %v", holderBalance)
	require.InDelta(t, 1000.0, holderBalance.Float64(), 0.0001,
		"holder should have 1000 issuer/USD (at limit)")

	// issuer cannot issue beyond holder's trust limit
	usd1 := tx.NewIssuedAmountFromFloat64(1, "USD", issuer.Address)
	result = env.Submit(PayIssued(issuer, holder, usd1).Build())
	require.False(t, result.Success, "Issuance beyond trust limit should fail")
	t.Logf("Over-limit issuance result: %s", result.Code)
}
