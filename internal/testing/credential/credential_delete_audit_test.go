package credential_test

import (
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/credential"
	"github.com/stretchr/testify/require"
)

// TestCredentialAccept_ExpiredRemovesFromBothDirectories verifies that when
// CredentialAccept hits an expired (un-accepted) credential, the credential is
// removed from BOTH the issuer's and the subject's owner directories, not just
// erased. An un-accepted credential is owned by the issuer but still listed in
// the subject's directory.
func TestCredentialAccept_ExpiredRemovesFromBothDirectories(t *testing.T) {
	credType := "abcde"

	issuer := jtx.NewAccount("issuer")
	subject := jtx.NewAccount("subject")

	env := jtx.NewTestEnv(t)
	env.Fund(issuer, subject)
	env.Close()

	credKey := jtx.CredentialKeylet(subject, issuer, credType)

	// Issuer creates a credential for subject with a short expiration.
	now := env.NowRipple()
	r := env.Submit(credential.CredentialCreateText(issuer, subject, credType).
		Expiration(now + 20).Build())
	jtx.RequireTxSuccess(t, r)
	env.Close()

	// Before expiry: credential listed in both owner directories.
	jtx.RequireOwnerDirectoryContains(t, env, issuer, credKey.Key, true)
	jtx.RequireOwnerDirectoryContains(t, env, subject, credKey.Key, true)
	require.Equal(t, uint32(1), env.OwnerCount(issuer), "issuer owns un-accepted credential")
	require.Equal(t, uint32(0), env.OwnerCount(subject), "subject does not own un-accepted credential")

	// Advance time past expiry, then attempt to accept.
	for env.NowRipple() <= now+20 {
		env.Close()
	}

	r = env.Submit(credential.CredentialAcceptText(subject, issuer, credType).Build())
	jtx.RequireTxClaimed(t, r, jtx.TecEXPIRED)
	env.Close()

	// After: credential erased and removed from BOTH directories; issuer count back to 0.
	require.False(t, env.LedgerEntryExists(credKey), "expired credential must be erased")
	jtx.RequireOwnerDirectoryContains(t, env, issuer, credKey.Key, false)
	jtx.RequireOwnerDirectoryContains(t, env, subject, credKey.Key, false)
	require.Equal(t, uint32(0), env.OwnerCount(issuer), "issuer owner count must drop to 0")
	require.Equal(t, uint32(0), env.OwnerCount(subject), "subject owner count stays 0")
}

// TestCredentialDelete_ViaDeleteSLE_AcceptedBySubject verifies that deleting an
// accepted credential (owned by the subject) via CredentialDelete removes it
// from both directories and decrements the subject's (sender's) owner count.
func TestCredentialDelete_ViaDeleteSLE_AcceptedBySubject(t *testing.T) {
	credType := "abcde"

	issuer := jtx.NewAccount("issuer")
	subject := jtx.NewAccount("subject")

	env := jtx.NewTestEnv(t)
	env.Fund(issuer, subject)
	env.Close()

	credKey := jtx.CredentialKeylet(subject, issuer, credType)

	r := env.Submit(credential.CredentialCreateText(issuer, subject, credType).Build())
	jtx.RequireTxSuccess(t, r)
	env.Close()
	r = env.Submit(credential.CredentialAcceptText(subject, issuer, credType).Build())
	jtx.RequireTxSuccess(t, r)
	env.Close()

	// Accepted: subject owns it, listed in both directories.
	require.Equal(t, uint32(0), env.OwnerCount(issuer))
	require.Equal(t, uint32(1), env.OwnerCount(subject))
	jtx.RequireOwnerDirectoryContains(t, env, issuer, credKey.Key, true)
	jtx.RequireOwnerDirectoryContains(t, env, subject, credKey.Key, true)

	// Subject (the owner) deletes the credential.
	r = env.Submit(credential.CredentialDeleteText(subject, subject, issuer, credType).Build())
	jtx.RequireTxSuccess(t, r)
	env.Close()

	require.False(t, env.LedgerEntryExists(credKey), "credential must be erased")
	jtx.RequireOwnerDirectoryContains(t, env, issuer, credKey.Key, false)
	jtx.RequireOwnerDirectoryContains(t, env, subject, credKey.Key, false)
	require.Equal(t, uint32(0), env.OwnerCount(subject), "subject owner count must drop to 0")
	require.Equal(t, uint32(0), env.OwnerCount(issuer))
}

// TestCredentialDelete_ViaDeleteSLE_UnacceptedByIssuer verifies that deleting an
// un-accepted credential (owned by the issuer) via CredentialDelete removes it
// from both directories and decrements the issuer's (sender's) owner count.
func TestCredentialDelete_ViaDeleteSLE_UnacceptedByIssuer(t *testing.T) {
	credType := "abcde"

	issuer := jtx.NewAccount("issuer")
	subject := jtx.NewAccount("subject")

	env := jtx.NewTestEnv(t)
	env.Fund(issuer, subject)
	env.Close()

	credKey := jtx.CredentialKeylet(subject, issuer, credType)

	r := env.Submit(credential.CredentialCreateText(issuer, subject, credType).Build())
	jtx.RequireTxSuccess(t, r)
	env.Close()

	// Un-accepted: issuer owns it, listed in both directories.
	require.Equal(t, uint32(1), env.OwnerCount(issuer))
	require.Equal(t, uint32(0), env.OwnerCount(subject))
	jtx.RequireOwnerDirectoryContains(t, env, issuer, credKey.Key, true)
	jtx.RequireOwnerDirectoryContains(t, env, subject, credKey.Key, true)

	// Issuer (the owner) deletes the un-accepted credential.
	r = env.Submit(credential.CredentialDeleteText(issuer, subject, issuer, credType).Build())
	jtx.RequireTxSuccess(t, r)
	env.Close()

	require.False(t, env.LedgerEntryExists(credKey), "credential must be erased")
	jtx.RequireOwnerDirectoryContains(t, env, issuer, credKey.Key, false)
	jtx.RequireOwnerDirectoryContains(t, env, subject, credKey.Key, false)
	require.Equal(t, uint32(0), env.OwnerCount(issuer), "issuer owner count must drop to 0")
	require.Equal(t, uint32(0), env.OwnerCount(subject))
}

// TestCredentialCreate_ReserveUsesActualFee verifies the reserve check compares
// the prior balance (balance + the actual fee paid), not balance + base fee.
// Funded to exactly the new-object reserve and paying a fee far above base, the
// create succeeds; the pre-fix code (balance + baseFee) would have wrongly
// returned tecINSUFFICIENT_RESERVE.
func TestCredentialCreate_ReserveUsesActualFee(t *testing.T) {
	credType := "abcde"

	issuer := jtx.NewAccount("issuer")
	subject := jtx.NewAccount("subject")

	env := jtx.NewTestEnv(t)
	env.Fund(subject)
	env.Close()

	// reserve for the first owned object: base + 1 increment.
	reserve := env.ReserveBase() + env.ReserveIncrement()
	const bigFee = uint64(5_000_000) // 5 XRP, far above the 10-drop base fee

	// Fund the issuer to exactly the reserve. After paying bigFee, the post-fee
	// balance is reserve-bigFee, so priorBalance = (reserve-bigFee)+bigFee =
	// reserve, which exactly covers the reserve under the corrected check. The
	// pre-fix check (post-fee balance + baseFee) would fall short by bigFee.
	env.FundAmount(issuer, reserve)
	env.Close()

	require.Equal(t, reserve, env.Balance(issuer))

	r := env.Submit(credential.CredentialCreateText(issuer, subject, credType).
		Fee(bigFee).Build())
	jtx.RequireTxSuccess(t, r)
	env.Close()

	require.Equal(t, uint32(1), env.OwnerCount(issuer),
		"credential create must succeed when prior balance (incl. actual fee) covers the reserve")
}

func TestCredentialAccept_ReserveUsesActualFee(t *testing.T) {
	issuer := jtx.NewAccount("issuer")
	subject := jtx.NewAccount("subject")
	env := jtx.NewTestEnv(t)
	env.Fund(issuer)
	env.FundAmount(subject, env.ReserveBase()+env.ReserveIncrement())
	env.Close()

	const credentialType = "accept-reserve"
	jtx.RequireTxSuccess(t, env.Submit(
		credential.CredentialCreateText(issuer, subject, credentialType).Build(),
	))
	env.Close()

	requiredReserve := env.ReserveBase() + env.ReserveIncrement()
	require.Equal(t, requiredReserve, env.Balance(subject))
	const highFee = uint64(5_000_000)
	jtx.RequireTxSuccess(t, env.Submit(
		credential.CredentialAcceptText(subject, issuer, credentialType).Fee(highFee).Build(),
	))
	env.Close()

	jtx.RequireOwnerCount(t, env, issuer, 0)
	jtx.RequireOwnerCount(t, env, subject, 1)
	jtx.RequireBalance(t, env, subject, requiredReserve-highFee)
}
