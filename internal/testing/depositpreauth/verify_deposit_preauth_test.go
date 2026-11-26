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
	"github.com/LeJamon/go-xrpl/internal/testing/offer"
	"github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/internal/testing/trustset"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/keylet"
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
	expiration := env.NowRipple() + 50
	result := env.Submit(
		credential.CredentialCreateText(issuer, alice, credType).
			Expiration(expiration).
			Build(),
	)
	jtx.RequireTxSuccess(t, result)
	env.Close()
	result = env.Submit(credential.CredentialAcceptText(alice, issuer, credType).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	credIdx := dp.CredentialIndexHex(alice, issuer, credType)
	credKey := jtx.CredentialKeylet(alice, issuer, credType)

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
	expiration := env.NowRipple() + 50
	result = env.Submit(
		credential.CredentialCreateText(issuer, alice, credType).
			Expiration(expiration).
			Build(),
	)
	jtx.RequireTxSuccess(t, result)
	env.Close()
	result = env.Submit(credential.CredentialAcceptText(alice, issuer, credType).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	credIdx := dp.CredentialIndexHex(alice, issuer, credType)
	credKey := jtx.CredentialKeylet(alice, issuer, credType)

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
	expiration := env.NowRipple() + 50
	result := env.Submit(
		credential.CredentialCreateText(issuer, bob, credType).
			Expiration(expiration).
			Build(),
	)
	jtx.RequireTxSuccess(t, result)
	env.Close()
	result = env.Submit(credential.CredentialAcceptText(bob, issuer, credType).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	credIdx := dp.CredentialIndexHex(bob, issuer, credType)
	credKey := jtx.CredentialKeylet(bob, issuer, credType)

	// Let the credential expire.
	env.AdvanceTime(60 * time.Second)
	env.Close()

	// Holder-to-holder transfer is rejected by the CanTransfer check before
	// deposit preauth runs, so the expired credential is NOT deleted.
	result = env.Submit(
		payment.PayIssued(bob, cindy, mptAlice.MPTAmount(10)).
			CredentialIDs([]string{credIdx}).
			Build(),
	)
	require.Equal(t, "tecNO_AUTH", result.Code)
	env.Close()
	require.True(t, env.LedgerEntryExists(credKey),
		"credential must be untouched when CanTransfer fails first")
}

// expiredCredential creates a credential issued to holder by issuer, accepted
// by holder, and advances the ledger past its expiration. It returns the
// credential index hex and its keylet. Self-issued credentials are accepted
// on creation.
func expiredCredential(t *testing.T, env *jtx.TestEnv, issuer, holder *jtx.Account, credType string) (string, keylet.Keylet) {
	t.Helper()
	result := env.Submit(
		credential.CredentialCreateText(issuer, holder, credType).
			Expiration(env.NowRipple() + 50).
			Build(),
	)
	jtx.RequireTxSuccess(t, result)
	env.Close()
	if issuer != holder {
		result = env.Submit(credential.CredentialAcceptText(holder, issuer, credType).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
	}

	env.AdvanceTime(60 * time.Second)
	env.Close()
	return dp.CredentialIndexHex(holder, issuer, credType), jtx.CredentialKeylet(holder, issuer, credType)
}

// TestPayment_ExpiredCredentialsNewDestinationXRP verifies that a direct XRP
// payment creating its destination still runs the expired-credential removal
// when the amount exceeds the base reserve: it fails with tecEXPIRED, the
// credential deletion survives the fee-claim reset, and the destination is not
// created. At exactly the base reserve the wedge exemption skips the check.
func TestPayment_ExpiredCredentialsNewDestinationXRP(t *testing.T) {
	const credType = "abcde"
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	carol := jtx.NewAccount("carol")

	env := jtx.NewTestEnv(t)
	env.FundAmount(alice, uint64(jtx.XRP(10000)))
	env.Close()

	credIdx, credKey := expiredCredential(t, env, alice, alice, credType)

	result := env.Submit(
		payment.Pay(alice, carol, reserve(env, 0)).
			CredentialIDs([]string{credIdx}).
			Build(),
	)
	jtx.RequireTxSuccess(t, result)
	env.Close()
	require.True(t, env.Exists(carol))
	require.True(t, env.LedgerEntryExists(credKey),
		"expired credential must survive a payment inside the reserve exemption")

	before := env.Balance(alice)
	result = env.Submit(
		payment.Pay(alice, bob, uint64(jtx.XRP(1000))).
			CredentialIDs([]string{credIdx}).
			Build(),
	)
	require.Equal(t, "tecEXPIRED", result.Code)
	env.Close()

	require.False(t, env.LedgerEntryExists(credKey),
		"expired credential must be deleted despite the fee-claim reset")
	require.False(t, env.Exists(bob), "destination must not be created")
	require.Equal(t, before-env.BaseFee(), env.Balance(alice))
}

// TestPayment_ExpiredCredentialsNewDestinationPath verifies that a
// cross-currency payment delivering XRP to a not-yet-existing destination runs
// the expired-credential removal unconditionally.
func TestPayment_ExpiredCredentialsNewDestinationPath(t *testing.T) {
	const credType = "abcde"
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	gw := jtx.NewAccount("gw")
	mm := jtx.NewAccount("mm")

	env := jtx.NewTestEnv(t)
	env.FundAmount(alice, uint64(jtx.XRP(10000)))
	env.FundAmount(gw, uint64(jtx.XRP(10000)))
	env.FundAmount(mm, uint64(jtx.XRP(10000)))
	env.Close()

	jtx.RequireTxSuccess(t, env.Submit(trustset.TrustLine(alice, "USD", gw, "10000").Build()))
	jtx.RequireTxSuccess(t, env.Submit(trustset.TrustLine(mm, "USD", gw, "10000").Build()))
	env.Close()
	jtx.RequireTxSuccess(t, env.Submit(
		payment.PayIssued(gw, alice, tx.NewIssuedAmountFromFloat64(5000, "USD", gw.Address)).Build()))
	jtx.RequireTxSuccess(t, env.Submit(
		offer.OfferCreate(mm, tx.NewIssuedAmountFromFloat64(1000, "USD", gw.Address), jtx.XRPTxAmountFromXRP(1000)).Build()))
	env.Close()

	credIdx, credKey := expiredCredential(t, env, alice, alice, credType)

	result := env.Submit(
		payment.PayIssued(alice, bob, jtx.XRPTxAmountFromXRP(500)).
			SendMax(tx.NewIssuedAmountFromFloat64(600, "USD", gw.Address)).
			CredentialIDs([]string{credIdx}).
			Build(),
	)
	require.Equal(t, "tecEXPIRED", result.Code)
	env.Close()

	require.False(t, env.LedgerEntryExists(credKey),
		"expired credential must be deleted despite the fee-claim reset")
	require.False(t, env.Exists(bob), "destination must not be created")
	require.InDelta(t, 5000.0, env.BalanceIOU(alice, "USD", gw), 1e-10)
}
