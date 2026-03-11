// Package payment contains integration tests for payment behavior.
// Tests ported from rippled's DepositAuth_test.cpp
package payment

import (
	"testing"

	"github.com/LeJamon/goXRPLd/keylet"
	"github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/internal/tx/depositpreauth"
	paymentPkg "github.com/LeJamon/goXRPLd/internal/tx/payment"
	xrplgoTesting "github.com/LeJamon/goXRPLd/internal/testing"
	"github.com/LeJamon/goXRPLd/internal/testing/credential"
	dp "github.com/LeJamon/goXRPLd/internal/testing/depositpreauth"
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
// The initial implementation of DepositAuth had a bug where an account with
// the DepositAuth flag set could not make a payment to itself. That bug was
// fixed in the DepositPreauth amendment.
func TestDepositPreauth_SelfPayment(t *testing.T) {
	env := xrplgoTesting.NewTestEnv(t)

	alice := xrplgoTesting.NewAccount("alice")
	becky := xrplgoTesting.NewAccount("becky")
	gw := xrplgoTesting.NewAccount("gateway")

	env.FundAmount(alice, uint64(xrplgoTesting.XRP(5000)))
	env.FundAmount(becky, uint64(xrplgoTesting.XRP(5000)))
	env.FundAmount(gw, uint64(xrplgoTesting.XRP(5000)))
	env.Close()

	// Set up trust lines for USD.
	result := env.Submit(trustset.TrustLine(alice, "USD", gw, "1000").Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	result = env.Submit(trustset.TrustLine(becky, "USD", gw, "1000").Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// Fund alice with USD.
	usd500 := tx.NewIssuedAmountFromFloat64(500, "USD", gw.Address)
	result = env.Submit(PayIssued(gw, alice, usd500).Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// alice creates a passive offer: TakerPays=XRP(100), TakerGets=USD(100).
	// In rippled: offer(alice, XRP(100), USD(100), tfPassive)
	usd100 := tx.NewIssuedAmountFromFloat64(100, "USD", gw.Address)
	xrp100 := tx.NewXRPAmount(int64(xrplgoTesting.XRP(100)))
	env.CreatePassiveOffer(alice, usd100, xrp100)
	env.Close()

	// becky pays herself USD(10) by consuming part of alice's offer.
	// This is a cross-currency self-payment: becky sends XRP and receives USD
	// through alice's offer. path(~USD) = {currency=USD, issuer=gw}.
	usd10 := tx.NewIssuedAmountFromFloat64(10, "USD", gw.Address)
	xrp10 := tx.NewXRPAmount(int64(xrplgoTesting.XRP(10)))
	usdPath := [][]paymentPkg.PathStep{{{Currency: "USD", Issuer: gw.Address}}}
	result = env.Submit(
		PayIssued(becky, becky, usd10).
			SendMax(xrp10).
			Paths(usdPath).
			Build(),
	)
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// becky enables DepositAuth.
	env.EnableDepositAuth(becky)
	env.Close()

	// becky pays herself again. With DepositPreauth enabled, self-payment
	// should succeed (the bug fix). Without DepositPreauth it would fail
	// with tecNO_PERMISSION.
	result = env.Submit(
		PayIssued(becky, becky, usd10).
			SendMax(xrp10).
			Paths(usdPath).
			Build(),
	)
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	t.Log("DepositPreauth self-payment test passed")
}

// TestDepositPreauth_Credentials tests DepositPreauth with credentials.
// From rippled: DepositPreauth_test::testCredentialsPayment
// An account with DepositAuth enabled can accept payments from senders who
// present valid credentials that match a credential-based DepositPreauth
// entry set up by the receiver.
func TestDepositPreauth_Credentials(t *testing.T) {
	credType := "abcde"

	issuer := xrplgoTesting.NewAccount("issuer")
	alice := xrplgoTesting.NewAccount("alice")
	bob := xrplgoTesting.NewAccount("bob")
	john := xrplgoTesting.NewAccount("john")

	env := xrplgoTesting.NewTestEnv(t)

	env.FundAmount(issuer, uint64(xrplgoTesting.XRP(5000)))
	env.FundAmount(alice, uint64(xrplgoTesting.XRP(5000)))
	env.FundAmount(bob, uint64(xrplgoTesting.XRP(5000)))
	env.FundAmount(john, uint64(xrplgoTesting.XRP(5000)))
	env.Close()

	// issuer creates credential for alice, alice hasn't accepted yet.
	result := env.Submit(credential.CredentialCreate(issuer, alice, credType).Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// Get the credential index.
	credIdx := dp.CredentialIndex(alice, issuer, credType)

	// bob requires preauthorization.
	env.EnableDepositAuth(bob)
	env.Close()

	// bob accepts payments from accounts with credentials signed by 'issuer'.
	result = env.Submit(dp.AuthCredentials(bob, []dp.AuthorizeCredentials{
		{Issuer: issuer, CredType: credType},
	}).Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// alice can't pay with empty credentials array.
	result = env.Submit(
		Pay(alice, bob, uint64(xrplgoTesting.XRP(100))).
			CredentialIDs([]string{}).
			Build(),
	)
	require.Equal(t, "temMALFORMED", result.Code,
		"empty credentials array should fail with temMALFORMED")
	env.Close()

	// alice can't pay with unaccepted credentials.
	result = env.Submit(
		Pay(alice, bob, uint64(xrplgoTesting.XRP(100))).
			CredentialIDs([]string{credIdx}).
			Build(),
	)
	require.Equal(t, "tecBAD_CREDENTIALS", result.Code,
		"unaccepted credentials should fail with tecBAD_CREDENTIALS")
	env.Close()

	// alice accepts the credentials.
	result = env.Submit(credential.CredentialAccept(alice, issuer, credType).Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// Now alice can pay bob with valid credentials.
	result = env.Submit(
		Pay(alice, bob, uint64(xrplgoTesting.XRP(100))).
			CredentialIDs([]string{credIdx}).
			Build(),
	)
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// alice can pay maria (unfunded, will be created) without depositPreauth
	// because maria has no deposit restrictions. Valid credentials on a
	// non-restricted destination are simply ignored.
	maria := xrplgoTesting.NewAccount("maria")
	result = env.Submit(
		Pay(alice, maria, uint64(xrplgoTesting.XRP(250))).
			CredentialIDs([]string{credIdx}).
			Build(),
	)
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// john can accept payment with old (account-based) DepositPreauth and
	// valid credentials at the same time.
	env.EnableDepositAuth(john)
	result = env.Submit(dp.Auth(john, alice).Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	result = env.Submit(
		Pay(alice, john, uint64(xrplgoTesting.XRP(100))).
			CredentialIDs([]string{credIdx}).
			Build(),
	)
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// --- Invalid credentials section ---

	t.Run("InvalidCredentials", func(t *testing.T) {
		env2 := xrplgoTesting.NewTestEnv(t)

		issuer2 := xrplgoTesting.NewAccount("issuer2")
		alice2 := xrplgoTesting.NewAccount("alice2")
		bob2 := xrplgoTesting.NewAccount("bob2")
		maria2 := xrplgoTesting.NewAccount("maria2")

		env2.FundAmount(issuer2, uint64(xrplgoTesting.XRP(10000)))
		env2.FundAmount(alice2, uint64(xrplgoTesting.XRP(10000)))
		env2.FundAmount(bob2, uint64(xrplgoTesting.XRP(10000)))
		env2.FundAmount(maria2, uint64(xrplgoTesting.XRP(10000)))
		env2.Close()

		// issuer creates credential for alice, alice accepts.
		result := env2.Submit(credential.CredentialCreate(issuer2, alice2, credType).Build())
		xrplgoTesting.RequireTxSuccess(t, result)
		env2.Close()
		result = env2.Submit(credential.CredentialAccept(alice2, issuer2, credType).Build())
		xrplgoTesting.RequireTxSuccess(t, result)
		env2.Close()

		credIdx := dp.CredentialIndex(alice2, issuer2, credType)

		// Success: destination didn't enable preauthorization, so valid
		// credentials won't fail.
		result = env2.Submit(
			Pay(alice2, bob2, uint64(xrplgoTesting.XRP(100))).
				CredentialIDs([]string{credIdx}).
				Build(),
		)
		xrplgoTesting.RequireTxSuccess(t, result)

		// bob requires preauthorization.
		env2.EnableDepositAuth(bob2)
		env2.Close()

		// Fail: destination didn't set up DepositPreauth object for these credentials.
		result = env2.Submit(
			Pay(alice2, bob2, uint64(xrplgoTesting.XRP(100))).
				CredentialIDs([]string{credIdx}).
				Build(),
		)
		require.Equal(t, "tecNO_PERMISSION", result.Code)

		// bob tries to set up DepositPreauth with duplicates - not allowed.
		result = env2.Submit(dp.AuthCredentials(bob2, []dp.AuthorizeCredentials{
			{Issuer: issuer2, CredType: credType},
			{Issuer: issuer2, CredType: credType},
		}).Build())
		require.Equal(t, "temMALFORMED", result.Code)

		// bob sets up DepositPreauth correctly.
		result = env2.Submit(dp.AuthCredentials(bob2, []dp.AuthorizeCredentials{
			{Issuer: issuer2, CredType: credType},
		}).Build())
		xrplgoTesting.RequireTxSuccess(t, result)
		env2.Close()

		// alice can't pay with non-existing credentials.
		invalidIdx := "0E0B04ED60588A758B67E21FBBE95AC5A63598BA951761DC0EC9C08D7E01E034"
		result = env2.Submit(
			Pay(alice2, bob2, uint64(xrplgoTesting.XRP(100))).
				CredentialIDs([]string{invalidIdx}).
				Build(),
		)
		require.Equal(t, "tecBAD_CREDENTIALS", result.Code)

		// maria can't pay using alice's credentials.
		result = env2.Submit(
			Pay(maria2, bob2, uint64(xrplgoTesting.XRP(100))).
				CredentialIDs([]string{credIdx}).
				Build(),
		)
		require.Equal(t, "tecBAD_CREDENTIALS", result.Code)

		// Create another valid credential for alice with different type.
		credType2 := "fghij"
		result = env2.Submit(credential.CredentialCreate(issuer2, alice2, credType2).Build())
		xrplgoTesting.RequireTxSuccess(t, result)
		env2.Close()
		result = env2.Submit(credential.CredentialAccept(alice2, issuer2, credType2).Build())
		xrplgoTesting.RequireTxSuccess(t, result)
		env2.Close()

		credIdx2 := dp.CredentialIndex(alice2, issuer2, credType2)

		// alice can't pay with invalid set of valid credentials (wrong combination).
		result = env2.Submit(
			Pay(alice2, bob2, uint64(xrplgoTesting.XRP(100))).
				CredentialIDs([]string{credIdx, credIdx2}).
				Build(),
		)
		require.Equal(t, "tecNO_PERMISSION", result.Code)

		// Error: duplicate credentials.
		result = env2.Submit(
			Pay(alice2, bob2, uint64(xrplgoTesting.XRP(100))).
				CredentialIDs([]string{credIdx, credIdx}).
				Build(),
		)
		require.Equal(t, "temMALFORMED", result.Code)

		// alice can pay with the correct single credential.
		result = env2.Submit(
			Pay(alice2, bob2, uint64(xrplgoTesting.XRP(100))).
				CredentialIDs([]string{credIdx}).
				Build(),
		)
		xrplgoTesting.RequireTxSuccess(t, result)
		env2.Close()
	})

	t.Log("DepositPreauth credentials test passed")
}

// rippleEpoch is the XRPL epoch start (2000-01-01 00:00:00 UTC).
const rippleEpoch = 946684800

// rippleTime returns the current Ripple epoch time from the test environment.
func rippleTime(env *xrplgoTesting.TestEnv) uint32 {
	return uint32(env.Now().Unix() - rippleEpoch)
}

// credentialKeylet computes the keylet for a credential given subject, issuer, and raw credential type.
func credentialKeylet(subject, issuer *xrplgoTesting.Account, credType string) keylet.Keylet {
	return keylet.Credential(subject.ID, issuer.ID, []byte(credType))
}

// TestDepositPreauth_ExpiredCredentials tests DepositPreauth with expired credentials.
// From rippled: DepositPreauth_test::testExpiredCreds
// When a payment is attempted with expired credentials, the transaction should
// fail with tecEXPIRED and the expired credential should be deleted from the
// ledger, while non-expired credentials remain.
func TestDepositPreauth_ExpiredCredentials(t *testing.T) {
	credType := "abcde"
	credType2 := "fghijkl"

	issuer := xrplgoTesting.NewAccount("issuer")
	alice := xrplgoTesting.NewAccount("alice")
	bob := xrplgoTesting.NewAccount("bob")

	env := xrplgoTesting.NewTestEnv(t)

	env.FundAmount(issuer, uint64(xrplgoTesting.XRP(10000)))
	env.FundAmount(alice, uint64(xrplgoTesting.XRP(10000)))
	env.FundAmount(bob, uint64(xrplgoTesting.XRP(10000)))
	env.Close()

	// issuer creates credential for alice with short expiration (current time + 60s).
	now := rippleTime(env)
	expiration := now + 60
	result := env.Submit(
		credential.CredentialCreate(issuer, alice, credType).
			Expiration(expiration).
			Build(),
	)
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// alice accepts the credential.
	result = env.Submit(credential.CredentialAccept(alice, issuer, credType).Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// issuer creates a second credential for alice with long expiration.
	now = rippleTime(env)
	result = env.Submit(
		credential.CredentialCreate(issuer, alice, credType2).
			Expiration(now + 1000).
			Build(),
	)
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()
	result = env.Submit(credential.CredentialAccept(alice, issuer, credType2).Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	xrplgoTesting.RequireOwnerCount(t, env, issuer, 0)
	xrplgoTesting.RequireOwnerCount(t, env, alice, 2)

	credIdx := dp.CredentialIndex(alice, issuer, credType)
	credIdx2 := dp.CredentialIndex(alice, issuer, credType2)

	// bob requires preauthorization.
	env.EnableDepositAuth(bob)
	env.Close()

	// bob sets up credential-based preauth for both credential types.
	result = env.Submit(dp.AuthCredentials(bob, []dp.AuthorizeCredentials{
		{Issuer: issuer, CredType: credType},
		{Issuer: issuer, CredType: credType2},
	}).Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// alice can pay (credentials not yet expired).
	result = env.Submit(
		Pay(alice, bob, uint64(xrplgoTesting.XRP(100))).
			CredentialIDs([]string{credIdx, credIdx2}).
			Build(),
	)
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()
	env.Close() // Extra close to advance time past expiration

	// Credentials have now expired (60s expiration, each Close advances 10s).
	// alice can't pay anymore.
	result = env.Submit(
		Pay(alice, bob, uint64(xrplgoTesting.XRP(100))).
			CredentialIDs([]string{credIdx, credIdx2}).
			Build(),
	)
	require.Equal(t, "tecEXPIRED", result.Code,
		"payment with expired credentials should fail with tecEXPIRED")
	env.Close()

	// Expired credential (credType) should be deleted from the ledger.
	credKey := credentialKeylet(alice, issuer, credType)
	require.False(t, env.LedgerEntryExists(credKey),
		"expired credential should be deleted from ledger")

	// Non-expired credential (credType2) should still be present.
	credKey2 := credentialKeylet(alice, issuer, credType2)
	require.True(t, env.LedgerEntryExists(credKey2),
		"non-expired credential should still exist")

	xrplgoTesting.RequireOwnerCount(t, env, issuer, 0)
	xrplgoTesting.RequireOwnerCount(t, env, alice, 1) // only credType2 remains

	t.Log("DepositPreauth expired credentials test passed")
}
