package escrow_test

import (
	"testing"
	"time"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/credential"
	dp "github.com/LeJamon/go-xrpl/internal/testing/depositpreauth"
	"github.com/LeJamon/go-xrpl/internal/testing/escrow"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

// TestEscrowFinish_ExpiredCredentialsDepositAuthDisabled verifies that with
// the DepositAuth amendment disabled, EscrowFinish never runs the deposit
// authorization check, so an attached expired credential is ignored: the
// finish succeeds and the credential is left on ledger. With the amendment
// enabled the same finish would fail with tecEXPIRED and delete the credential.
// Reference: rippled Escrow.cpp doApply() — verifyDepositPreauth is gated on
// featureDepositAuth.
func TestEscrowFinish_ExpiredCredentialsDepositAuthDisabled(t *testing.T) {
	env := jtx.NewTestEnv(t)
	env.DisableFeature("DepositAuth")

	issuer := jtx.NewAccount("issuer")
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	carol := jtx.NewAccount("carol")
	fund5000(env, issuer, alice, bob, carol)
	env.Close()

	credType := "abcde"

	// Issuer creates a soon-to-expire credential for carol; carol accepts.
	expiration := escrow.ToRippleTime(env.Now()) + 50
	result := env.Submit(
		credential.CredentialCreate(issuer, carol, credType).
			Expiration(expiration).
			Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()
	result = env.Submit(credential.CredentialAccept(carol, issuer, credType).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	credIdx := dp.CredentialIndex(carol, issuer, credType)
	credKey := keylet.Credential(carol.ID, issuer.ID, []byte(credType))

	seq := env.Seq(alice)
	result = env.Submit(
		escrow.EscrowCreate(alice, bob, xrp(1000)).
			FinishTime(env.Now().Add(1 * time.Second)).
			Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	bobBalance := env.Balance(bob)

	// Let the credential expire.
	env.AdvanceTime(60 * time.Second)
	env.Close()

	// Without featureDepositAuth the expired credential is ignored.
	result = env.Submit(
		escrow.EscrowFinish(carol, alice, seq).
			CredentialIDs([]string{credIdx}).
			Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	require.True(t, env.LedgerEntryExists(credKey),
		"expired credential must be untouched when DepositAuth is disabled")
	require.Equal(t, bobBalance+uint64(xrp(1000)), env.Balance(bob))
}
