package credential_test

// Tests for the credential deletion / DeleteSLE consolidation and the
// prior-balance reserve check (issue #891 audit, H3 + M4).

import (
	"encoding/hex"
	"testing"

	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/credential"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

// dirContains reports whether an owner directory contains the given key on any
// of its pages. It walks pages until a page is missing.
func dirContains(t *testing.T, env *jtx.TestEnv, owner *jtx.Account, key keylet.Keylet) bool {
	t.Helper()
	wantHex := hex.EncodeToString(key.Key[:])
	for page := uint64(0); ; page++ {
		data, err := env.LedgerEntry(keylet.OwnerDirPage(owner.ID, page))
		if err != nil || data == nil {
			return false
		}
		fields, err := binarycodec.Decode(hex.EncodeToString(data))
		require.NoError(t, err)
		if idxs, ok := fields["Indexes"].([]string); ok {
			for _, s := range idxs {
				if hexEqualFold(s, wantHex) {
					return true
				}
			}
		}
		if page > 64 {
			return false
		}
	}
}

func hexEqualFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		ca, cb := a[i], b[i]
		if ca >= 'A' && ca <= 'F' {
			ca += 'a' - 'A'
		}
		if cb >= 'A' && cb <= 'F' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}

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

	credKey := credentialKeylet(subject, issuer, credType)

	// Issuer creates a credential for subject with a short expiration.
	now := rippleTime(env)
	r := env.Submit(credential.CredentialCreate(issuer, subject, credType).
		Expiration(now + 20).Build())
	jtx.RequireTxSuccess(t, r)
	env.Close()

	// Before expiry: credential listed in both owner directories.
	require.True(t, dirContains(t, env, issuer, credKey),
		"credential must be in issuer directory after create")
	require.True(t, dirContains(t, env, subject, credKey),
		"un-accepted credential must be in subject directory after create")
	require.Equal(t, uint32(1), env.OwnerCount(issuer), "issuer owns un-accepted credential")
	require.Equal(t, uint32(0), env.OwnerCount(subject), "subject does not own un-accepted credential")

	// Advance time past expiry, then attempt to accept.
	for rippleTime(env) <= now+20 {
		env.Close()
	}

	r = env.Submit(credential.CredentialAccept(subject, issuer, credType).Build())
	require.Equal(t, "tecEXPIRED", r.Code, "accepting an expired credential must return tecEXPIRED")
	env.Close()

	// After: credential erased and removed from BOTH directories; issuer count back to 0.
	require.False(t, env.LedgerEntryExists(credKey), "expired credential must be erased")
	require.False(t, dirContains(t, env, issuer, credKey),
		"expired credential must be removed from issuer directory")
	require.False(t, dirContains(t, env, subject, credKey),
		"expired credential must be removed from subject directory")
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

	credKey := credentialKeylet(subject, issuer, credType)

	r := env.Submit(credential.CredentialCreate(issuer, subject, credType).Build())
	jtx.RequireTxSuccess(t, r)
	env.Close()
	r = env.Submit(credential.CredentialAccept(subject, issuer, credType).Build())
	jtx.RequireTxSuccess(t, r)
	env.Close()

	// Accepted: subject owns it, listed in both directories.
	require.Equal(t, uint32(0), env.OwnerCount(issuer))
	require.Equal(t, uint32(1), env.OwnerCount(subject))
	require.True(t, dirContains(t, env, issuer, credKey))
	require.True(t, dirContains(t, env, subject, credKey))

	// Subject (the owner) deletes the credential.
	r = env.Submit(credential.CredentialDelete(subject, subject, issuer, credType).Build())
	jtx.RequireTxSuccess(t, r)
	env.Close()

	require.False(t, env.LedgerEntryExists(credKey), "credential must be erased")
	require.False(t, dirContains(t, env, issuer, credKey),
		"credential must be removed from issuer directory")
	require.False(t, dirContains(t, env, subject, credKey),
		"credential must be removed from subject directory")
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

	credKey := credentialKeylet(subject, issuer, credType)

	r := env.Submit(credential.CredentialCreate(issuer, subject, credType).Build())
	jtx.RequireTxSuccess(t, r)
	env.Close()

	// Un-accepted: issuer owns it, listed in both directories.
	require.Equal(t, uint32(1), env.OwnerCount(issuer))
	require.Equal(t, uint32(0), env.OwnerCount(subject))
	require.True(t, dirContains(t, env, issuer, credKey))
	require.True(t, dirContains(t, env, subject, credKey))

	// Issuer (the owner) deletes the un-accepted credential.
	r = env.Submit(credential.CredentialDelete(issuer, subject, issuer, credType).Build())
	jtx.RequireTxSuccess(t, r)
	env.Close()

	require.False(t, env.LedgerEntryExists(credKey), "credential must be erased")
	require.False(t, dirContains(t, env, issuer, credKey),
		"credential must be removed from issuer directory")
	require.False(t, dirContains(t, env, subject, credKey),
		"credential must be removed from subject directory")
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

	// FundAmount may leave the issuer slightly under `reserve` after its internal
	// AccountSet fee; top up to land exactly on the boundary.
	if bal := env.Balance(issuer); bal < reserve {
		env.Pay(issuer, reserve-bal)
		env.Close()
	}
	require.GreaterOrEqual(t, env.Balance(issuer), reserve, "issuer must hold at least the reserve")

	r := env.Submit(credential.CredentialCreate(issuer, subject, credType).
		Fee(bigFee).Build())
	jtx.RequireTxSuccess(t, r)
	env.Close()

	require.Equal(t, uint32(1), env.OwnerCount(issuer),
		"credential create must succeed when prior balance (incl. actual fee) covers the reserve")
}
