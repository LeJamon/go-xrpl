// Package payment contains integration tests for payment behavior.
// Tests ported from rippled's DepositAuth_test.cpp
package payment

import (
	"testing"

	"github.com/LeJamon/goXRPLd/internal/core/tx"
	"github.com/LeJamon/goXRPLd/internal/core/tx/depositpreauth"
	xrplgoTesting "github.com/LeJamon/goXRPLd/internal/testing"
	"github.com/LeJamon/goXRPLd/internal/testing/trustset"
	"github.com/stretchr/testify/require"
)

// TestDepositAuth_Enable tests enabling and disabling DepositAuth flag.
// From rippled: testEnable
func TestDepositAuth_Enable(t *testing.T) {
	env := xrplgoTesting.NewTestEnv(t)

	alice := xrplgoTesting.NewAccount("alice")

	env.FundAmount(alice, uint64(xrplgoTesting.XRP(10000)))
	env.Close()

	// Get initial account flags
	initialBalance := env.Balance(alice)

	// alice enables DepositAuth via AccountSet
	env.EnableDepositAuth(alice)
	env.Close()

	// Verify account still exists and balance reduced by fee
	balanceAfterEnable := env.Balance(alice)
	require.Less(t, balanceAfterEnable, initialBalance,
		"Balance should decrease due to AccountSet fee")

	// alice disables DepositAuth via AccountSet
	env.DisableDepositAuth(alice)
	env.Close()

	// Verify account still exists and balance reduced again
	finalBalance := env.Balance(alice)
	require.Less(t, finalBalance, balanceAfterEnable,
		"Balance should decrease due to second AccountSet fee")

	t.Log("DepositAuth enable/disable test passed")
}

// TestDepositAuth_PayIOU tests IOU payments with DepositAuth.
// From rippled: testPayIOU
func TestDepositAuth_PayIOU(t *testing.T) {
	env := xrplgoTesting.NewTestEnv(t)

	alice := xrplgoTesting.NewAccount("alice")
	bob := xrplgoTesting.NewAccount("bob")
	gw := xrplgoTesting.NewAccount("gateway")

	env.FundAmount(alice, uint64(xrplgoTesting.XRP(10000)))
	env.FundAmount(bob, uint64(xrplgoTesting.XRP(10000)))
	env.FundAmount(gw, uint64(xrplgoTesting.XRP(10000)))
	env.Close()

	// Set up trust lines
	result := env.Submit(trustset.TrustLine(alice, "USD", gw, "1000").Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	result = env.Submit(trustset.TrustLine(bob, "USD", gw, "1000").Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// Fund alice with USD
	usd150 := tx.NewIssuedAmountFromFloat64(150, "USD", gw.Address)
	result = env.Submit(PayIssued(gw, alice, usd150).Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// alice pays bob some USD to set up initial balance
	usd50 := tx.NewIssuedAmountFromFloat64(50, "USD", gw.Address)
	result = env.Submit(PayIssued(alice, bob, usd50).Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// bob enables DepositAuth
	env.EnableDepositAuth(bob)
	env.Close()

	// alice tries to pay bob USD - should fail with tecNO_PERMISSION
	result = env.Submit(PayIssued(alice, bob, usd50).Build())
	require.Equal(t, "tecNO_PERMISSION", result.Code,
		"IOU payment to DepositAuth account should fail with tecNO_PERMISSION")

	// bob can still make payments to others while DepositAuth is set
	usd25 := tx.NewIssuedAmountFromFloat64(25, "USD", gw.Address)
	result = env.Submit(PayIssued(bob, alice, usd25).Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// bob clears DepositAuth
	env.DisableDepositAuth(bob)
	env.Close()

	// alice can now pay bob
	result = env.Submit(PayIssued(alice, bob, usd50).Build())
	xrplgoTesting.RequireTxSuccess(t, result)

	t.Log("DepositAuth IOU test passed")
}

// TestDepositAuth_PayXRP tests XRP payments with DepositAuth.
// From rippled: testPayXRP
func TestDepositAuth_PayXRP(t *testing.T) {
	env := xrplgoTesting.NewTestEnv(t)

	alice := xrplgoTesting.NewAccount("alice")
	bob := xrplgoTesting.NewAccount("bob")

	env.FundAmount(alice, uint64(xrplgoTesting.XRP(10000)))
	env.FundAmount(bob, uint64(xrplgoTesting.XRP(10000)))
	env.Close()

	// bob enables DepositAuth
	env.EnableDepositAuth(bob)
	env.Close()

	// bob has more XRP than base reserve - any payment should fail
	result := env.Submit(Pay(alice, bob, 1_000_000).Build())
	require.Equal(t, "tecNO_PERMISSION", result.Code,
		"XRP payment to DepositAuth account above reserve should fail")

	// bob clears DepositAuth
	env.DisableDepositAuth(bob)
	env.Close()

	// alice can now pay bob
	result = env.Submit(Pay(alice, bob, 1_000_000).Build())
	xrplgoTesting.RequireTxSuccess(t, result)

	t.Log("DepositAuth XRP test passed")
}

// TestDepositAuth_PayXRP_AtReserve tests the special XRP payment allowance at reserve.
// From rippled: testPayXRP (reserve edge cases)
// When an account with DepositAuth is at or below the base reserve, small payments are allowed
// to prevent the account from becoming permanently locked.
func TestDepositAuth_PayXRP_AtReserve(t *testing.T) {
	env := xrplgoTesting.NewTestEnv(t)

	alice := xrplgoTesting.NewAccount("alice")
	bob := xrplgoTesting.NewAccount("bob")

	// Fund alice with plenty of XRP
	env.FundAmount(alice, uint64(xrplgoTesting.XRP(10000)))
	// Fund bob with just the minimum reserve (200 XRP base reserve)
	env.FundAmount(bob, uint64(xrplgoTesting.XRP(200)))
	env.Close()

	// bob enables DepositAuth
	env.EnableDepositAuth(bob)
	env.Close()

	// bob now has balance <= reserve (after AccountSet fee)
	bobBalance := env.Balance(bob)
	t.Logf("Bob's balance after enabling DepositAuth: %d drops", bobBalance)

	// Small XRP payments should succeed because bob is at/below reserve
	// This is the special exception in rippled to prevent account wedging
	result := env.Submit(Pay(alice, bob, 1_000_000).Build())
	xrplgoTesting.RequireTxSuccess(t, result)

	t.Log("DepositAuth XRP at reserve test passed - small payment succeeded")
}

// TestDepositAuth_NoRipple tests DepositAuth interaction with NoRipple flag.
// From rippled: testNoRipple
func TestDepositAuth_NoRipple(t *testing.T) {
	t.Skip("TODO: DepositAuth+NoRipple requires path payment support")

	// Test various combinations of NoRipple and DepositAuth flags
	// DepositAuth should not affect rippling behavior
	// NoRipple controls whether funds can ripple through an account

	t.Log("DepositAuth NoRipple test: requires path payment support")
}

// TestDepositPreauth_Enable tests DepositPreauth creation and deletion.
// From rippled: DepositPreauth_test::testEnable
func TestDepositPreauth_Enable(t *testing.T) {
	env := xrplgoTesting.NewTestEnv(t)

	alice := xrplgoTesting.NewAccount("alice")
	becky := xrplgoTesting.NewAccount("becky")

	env.FundAmount(alice, uint64(xrplgoTesting.XRP(10000)))
	env.FundAmount(becky, uint64(xrplgoTesting.XRP(10000)))
	env.Close()

	// alice authorizes becky via DepositPreauth transaction
	env.Preauthorize(alice, becky)
	env.Close()

	// alice's owner count should increase by 1
	// (we don't have a direct way to check owner count in tests yet)

	// alice removes authorization for becky
	env.Unauthorize(alice, becky)
	env.Close()

	t.Log("DepositPreauth enable test: verified")
}

// TestDepositPreauth_Invalid tests invalid DepositPreauth operations.
// From rippled: DepositPreauth_test::testInvalid
func TestDepositPreauth_Invalid(t *testing.T) {
	env := xrplgoTesting.NewTestEnv(t)

	alice := xrplgoTesting.NewAccount("alice")
	becky := xrplgoTesting.NewAccount("becky")
	carol := xrplgoTesting.NewAccount("carol") // unfunded

	env.FundAmount(alice, uint64(xrplgoTesting.XRP(10000)))
	env.FundAmount(becky, uint64(xrplgoTesting.XRP(10000)))
	env.Close()

	// Cannot preauthorize unfunded account
	preauth := depositpreauth.NewDepositPreauth(alice.Address)
	preauth.SetAuthorize(carol.Address)
	preauth.Fee = "10"
	seq := env.Seq(alice)
	preauth.Sequence = &seq

	result := env.Submit(preauth)
	require.Equal(t, "tecNO_TARGET", result.Code,
		"Preauthorizing unfunded account should fail with tecNO_TARGET")

	// alice authorizes becky
	env.Preauthorize(alice, becky)
	env.Close()

	// Duplicate authorization should fail
	preauth2 := depositpreauth.NewDepositPreauth(alice.Address)
	preauth2.SetAuthorize(becky.Address)
	preauth2.Fee = "10"
	seq = env.Seq(alice)
	preauth2.Sequence = &seq

	result = env.Submit(preauth2)
	require.Equal(t, "tecDUPLICATE", result.Code,
		"Duplicate preauthorization should fail with tecDUPLICATE")

	// Remove non-existent authorization should fail
	preauth3 := depositpreauth.NewDepositPreauth(alice.Address)
	preauth3.SetUnauthorize(carol.Address)
	preauth3.Fee = "10"
	seq = env.Seq(alice)
	preauth3.Sequence = &seq

	result = env.Submit(preauth3)
	require.Equal(t, "tecNO_ENTRY", result.Code,
		"Removing non-existent preauth should fail with tecNO_ENTRY")

	t.Log("DepositPreauth invalid test: verified")
}

// TestDepositPreauth_Payment tests payments with DepositPreauth.
// From rippled: DepositPreauth_test::testPayment
func TestDepositPreauth_Payment(t *testing.T) {
	env := xrplgoTesting.NewTestEnv(t)

	alice := xrplgoTesting.NewAccount("alice")
	becky := xrplgoTesting.NewAccount("becky")
	carol := xrplgoTesting.NewAccount("carol")
	gw := xrplgoTesting.NewAccount("gateway")

	env.FundAmount(alice, uint64(xrplgoTesting.XRP(5000)))
	env.FundAmount(becky, uint64(xrplgoTesting.XRP(5000)))
	env.FundAmount(carol, uint64(xrplgoTesting.XRP(5000)))
	env.FundAmount(gw, uint64(xrplgoTesting.XRP(5000)))
	env.Close()

	// Set up trust lines
	result := env.Submit(trustset.TrustLine(alice, "USD", gw, "1000").Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	result = env.Submit(trustset.TrustLine(becky, "USD", gw, "1000").Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	result = env.Submit(trustset.TrustLine(carol, "USD", gw, "1000").Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// Fund alice
	usd1000 := tx.NewIssuedAmountFromFloat64(1000, "USD", gw.Address)
	result = env.Submit(PayIssued(gw, alice, usd1000).Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// alice can pay becky (no restrictions yet)
	usd100 := tx.NewIssuedAmountFromFloat64(100, "USD", gw.Address)
	result = env.Submit(PayIssued(alice, becky, usd100).Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	result = env.Submit(Pay(alice, becky, 100_000000).Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// becky enables DepositAuth
	env.EnableDepositAuth(becky)
	env.Close()

	// alice can no longer pay becky
	result = env.Submit(Pay(alice, becky, 100_000000).Build())
	require.Equal(t, "tecNO_PERMISSION", result.Code,
		"XRP payment should fail with DepositAuth enabled")

	result = env.Submit(PayIssued(alice, becky, usd100).Build())
	require.Equal(t, "tecNO_PERMISSION", result.Code,
		"IOU payment should fail with DepositAuth enabled")

	// becky preauthorizes carol (not alice)
	env.Preauthorize(becky, carol)
	env.Close()

	// alice still cannot pay becky
	result = env.Submit(Pay(alice, becky, 100_000000).Build())
	require.Equal(t, "tecNO_PERMISSION", result.Code,
		"Payment should still fail - alice not preauthorized")

	// becky preauthorizes alice
	env.Preauthorize(becky, alice)
	env.Close()

	// now alice can pay becky
	result = env.Submit(Pay(alice, becky, 100_000000).Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	result = env.Submit(PayIssued(alice, becky, usd100).Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// becky removes carol's preauth (shouldn't affect alice)
	env.Unauthorize(becky, carol)
	env.Close()

	// alice can still pay becky
	result = env.Submit(Pay(alice, becky, 100_000000).Build())
	xrplgoTesting.RequireTxSuccess(t, result)

	// becky removes alice's preauth
	env.Unauthorize(becky, alice)
	env.Close()

	// alice can no longer pay becky
	result = env.Submit(Pay(alice, becky, 100_000000).Build())
	require.Equal(t, "tecNO_PERMISSION", result.Code,
		"Payment should fail after preauth removed")

	// becky clears DepositAuth
	env.DisableDepositAuth(becky)
	env.Close()

	// alice can pay becky again
	result = env.Submit(Pay(alice, becky, 100_000000).Build())
	xrplgoTesting.RequireTxSuccess(t, result)

	t.Log("DepositPreauth payment test: verified")
}

// TestDepositPreauth_SelfPayment tests self-payment with DepositAuth.
// From rippled: DepositPreauth_test::testPayment (self-payment section)
func TestDepositPreauth_SelfPayment(t *testing.T) {
	t.Skip("TODO: DepositPreauth self-payment requires Offers for cross-currency paths")

	t.Log("DepositPreauth self-payment test: requires offer support")
}

// TestDepositPreauth_Credentials tests DepositPreauth with credentials.
// From rippled: DepositPreauth_test with credentials
func TestDepositPreauth_Credentials(t *testing.T) {
	t.Skip("TODO: Credentials require Credentials feature support")

	t.Log("DepositPreauth credentials test: requires Credentials feature")
}

// TestDepositPreauth_ExpiredCredentials tests DepositPreauth with expired credentials.
// From rippled: DepositPreauth_test::testExpiredCreds
func TestDepositPreauth_ExpiredCredentials(t *testing.T) {
	t.Skip("TODO: Expired credentials require Credentials feature support")

	t.Log("DepositPreauth expired credentials test: requires Credentials feature")
}
