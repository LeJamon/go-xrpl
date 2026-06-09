// Tests pinning rippled's verifyDepositPreauth() call-site gating: when the
// deposit-authorization check (and the expired-credential removal inside it)
// runs for each Payment variant.
// Reference: rippled Payment.cpp doApply() + CredentialHelpers.cpp verifyDepositPreauth()
package depositpreauth_test

import (
	"testing"
	"time"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/credential"
	dp "github.com/LeJamon/go-xrpl/internal/testing/depositpreauth"
	"github.com/LeJamon/go-xrpl/internal/testing/mpt"
	"github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/internal/testing/trustset"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/stretchr/testify/require"
)

// TestDepositAuth_ExpiredCredentialsReserveExemption verifies that for direct
// XRP payments the deposit-authorization check — including expired-credential
// removal — only runs when the payment amount or the destination balance
// exceeds the base reserve. Within the wedge exemption an expired credential
// is ignored and left on ledger.
// Reference: rippled Payment.cpp:641-678
func TestDepositAuth_ExpiredCredentialsReserveExemption(t *testing.T) {
	credType := "abcde"
	issuer := jtx.NewAccount("issuer")
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")

	env := jtx.NewTestEnv(t)
	env.FundAmount(issuer, uint64(jtx.XRP(10000)))
	env.FundAmount(alice, uint64(jtx.XRP(10000)))
	env.FundAmount(bob, uint64(jtx.XRP(10000)))
	env.Close()

	// Issuer creates a soon-to-expire credential for alice; alice accepts.
	expiration := rippleTime(env) + 50
	result := env.Submit(
		credential.CredentialCreate(issuer, alice, credType).
			Expiration(expiration).
			Build(),
	)
	jtx.RequireTxSuccess(t, result)
	env.Close()
	result = env.Submit(credential.CredentialAccept(alice, issuer, credType).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	credIdx := dp.CredentialIndex(alice, issuer, credType)
	credKey := credentialKeylet(alice, issuer, credType)

	// Bring bob's XRP balance down to exactly the base reserve.
	// bob does NOT have lsfDepositAuth set.
	{
		bobPaysXRP := env.Balance(bob) - reserve(env, 1)
		bobPaysFee := reserve(env, 1) - reserve(env, 0)
		result = env.Submit(payment.Pay(bob, alice, bobPaysXRP).Fee(bobPaysFee).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
	}
	require.Equal(t, reserve(env, 0), env.Balance(bob))

	// Let the credential expire.
	env.AdvanceTime(60 * time.Second)
	env.Close()

	// Payment amount and destination balance are both <= base reserve, so
	// deposit preauth is not checked at all: the payment succeeds and the
	// expired credential is left untouched.
	result = env.Submit(
		payment.Pay(alice, bob, 1).
			CredentialIDs([]string{credIdx}).
			Build(),
	)
	jtx.RequireTxSuccess(t, result)
	env.Close()
	require.True(t, env.LedgerEntryExists(credKey),
		"expired credential must survive a payment inside the reserve exemption")

	// bob is now above the base reserve, so the check runs: the expired
	// credential fails the payment with tecEXPIRED and is deleted.
	require.Equal(t, reserve(env, 0)+1, env.Balance(bob))
	result = env.Submit(
		payment.Pay(alice, bob, 1).
			CredentialIDs([]string{credIdx}).
			Build(),
	)
	require.Equal(t, "tecEXPIRED", result.Code)
	env.Close()
	require.False(t, env.LedgerEntryExists(credKey),
		"expired credential must be deleted once the check runs")
	require.Equal(t, reserve(env, 0)+1, env.Balance(bob))
}

// TestPayment_ExpiredCredentialsNoDepositAuthDestination verifies that an IOU
// payment carrying expired credentials fails with tecEXPIRED (deleting the
// credential) even when the destination does NOT have lsfDepositAuth set:
// rippled runs verifyDepositPreauth for every ripple payment when the
// DepositPreauth and DepositAuth amendments are enabled, and removeExpired
// fires before the destination flags are consulted.
// Reference: rippled Payment.cpp:448-464, CredentialHelpers.cpp verifyDepositPreauth()
func TestPayment_ExpiredCredentialsNoDepositAuthDestination(t *testing.T) {
	credType := "abcde"
	issuer := jtx.NewAccount("issuer")
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	gw := jtx.NewAccount("gw")

	env := jtx.NewTestEnv(t)
	env.FundAmount(issuer, uint64(jtx.XRP(10000)))
	env.FundAmount(alice, uint64(jtx.XRP(10000)))
	env.FundAmount(bob, uint64(jtx.XRP(10000)))
	env.FundAmount(gw, uint64(jtx.XRP(10000)))
	env.Close()

	result := env.Submit(trustset.TrustLine(alice, "USD", gw, "1000").Build())
	jtx.RequireTxSuccess(t, result)
	result = env.Submit(trustset.TrustLine(bob, "USD", gw, "1000").Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	usd150 := tx.NewIssuedAmountFromFloat64(150, "USD", gw.Address)
	result = env.Submit(payment.PayIssued(gw, alice, usd150).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// Issuer creates a soon-to-expire credential for alice; alice accepts.
	expiration := rippleTime(env) + 50
	result = env.Submit(
		credential.CredentialCreate(issuer, alice, credType).
			Expiration(expiration).
			Build(),
	)
	jtx.RequireTxSuccess(t, result)
	env.Close()
	result = env.Submit(credential.CredentialAccept(alice, issuer, credType).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	credIdx := dp.CredentialIndex(alice, issuer, credType)
	credKey := credentialKeylet(alice, issuer, credType)

	// Let the credential expire.
	env.AdvanceTime(60 * time.Second)
	env.Close()

	// bob does NOT have lsfDepositAuth, yet the expired credential still
	// fails the payment and is removed from the ledger.
	usd50 := tx.NewIssuedAmountFromFloat64(50, "USD", gw.Address)
	result = env.Submit(
		payment.PayIssued(alice, bob, usd50).
			CredentialIDs([]string{credIdx}).
			Build(),
	)
	require.Equal(t, "tecEXPIRED", result.Code)
	env.Close()

	require.False(t, env.LedgerEntryExists(credKey),
		"expired credential must be deleted even when the destination has no DepositAuth")
	require.InDelta(t, 0.0, env.BalanceIOU(bob, "USD", gw), 1e-10)
}

// TestMPTPayment_CanTransferCheckedBeforeDepositPreauth verifies that for MPT
// direct payments the CanTransfer check precedes verifyDepositPreauth: a
// holder-to-holder payment of a non-transferable MPT fails with tecNO_AUTH and
// an attached expired credential is left untouched.
// Reference: rippled Payment.cpp:526-539
func TestMPTPayment_CanTransferCheckedBeforeDepositPreauth(t *testing.T) {
	credType := "abcde"
	issuer := jtx.NewAccount("issuer")
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	cindy := jtx.NewAccount("cindy")

	env := jtx.NewTestEnv(t)
	env.Fund(alice)
	env.Fund(bob)
	env.Fund(cindy)
	env.FundAmount(issuer, uint64(jtx.XRP(10000)))
	env.Close()

	// alice creates a non-transferable MPT (no CanTransfer flag).
	mptAlice := mpt.NewMPTTester(t, env, alice, mpt.MPTInit{Holders: []*jtx.Account{bob, cindy}})
	mptAlice.Create(mpt.CreateOpts{OwnerCount: mpt.PtrUint32(1), HolderCount: mpt.PtrUint32(0)})
	mptAlice.Authorize(mpt.AuthorizeOpts{Account: bob})
	mptAlice.Authorize(mpt.AuthorizeOpts{Account: cindy})
	mptAlice.Pay(alice, bob, 100)

	// Issuer creates a soon-to-expire credential for bob; bob accepts.
	expiration := rippleTime(env) + 50
	result := env.Submit(
		credential.CredentialCreate(issuer, bob, credType).
			Expiration(expiration).
			Build(),
	)
	jtx.RequireTxSuccess(t, result)
	env.Close()
	result = env.Submit(credential.CredentialAccept(bob, issuer, credType).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	credIdx := dp.CredentialIndex(bob, issuer, credType)
	credKey := credentialKeylet(bob, issuer, credType)

	// Let the credential expire.
	env.AdvanceTime(60 * time.Second)
	env.Close()

	// Holder-to-holder transfer is rejected by the CanTransfer check before
	// deposit preauth runs, so the expired credential is NOT deleted.
	result = env.Submit(
		payment.PayIssued(bob, cindy, mptAlice.MPTAmount(10)).
			MPTIssuanceID(mptAlice.IssuanceID()).
			CredentialIDs([]string{credIdx}).
			Build(),
	)
	require.Equal(t, "tecNO_AUTH", result.Code)
	env.Close()
	require.True(t, env.LedgerEntryExists(credKey),
		"credential must be untouched when CanTransfer fails first")
}

// TestPayment_SelfRipplePaymentDepositPreauthDisabled verifies that without
// the DepositPreauth amendment, a ripple (cross-currency) payment to a
// destination with lsfDepositAuth set fails with tecNO_PERMISSION even when
// source == destination — the bug the DepositPreauth amendment later fixed.
// Reference: rippled Payment.cpp:440-441
func TestPayment_SelfRipplePaymentDepositPreauthDisabled(t *testing.T) {
	alice := jtx.NewAccount("alice")
	gw := jtx.NewAccount("gw")

	env := jtx.NewTestEnv(t)
	env.DisableFeature("DepositPreauth")

	env.FundAmount(alice, uint64(jtx.XRP(10000)))
	env.FundAmount(gw, uint64(jtx.XRP(10000)))
	env.Close()

	env.EnableDepositAuth(alice)
	env.Close()

	usd1 := tx.NewIssuedAmountFromFloat64(1, "USD", gw.Address)
	result := env.Submit(payment.Pay(alice, alice, 1).SendMax(usd1).Build())
	require.Equal(t, "tecNO_PERMISSION", result.Code)
}
