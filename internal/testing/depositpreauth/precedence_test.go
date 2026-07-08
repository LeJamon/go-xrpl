package depositpreauth_test

import (
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	dp "github.com/LeJamon/go-xrpl/internal/testing/depositpreauth"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/depositpreauth"
)

// TestDepositPreauth_Precedence_FullyCanonicalSigAllowed pins finding #1: a
// DepositPreauth carrying tfFullyCanonicalSig (set by default by many client
// libraries, and by every inner-batch transaction via tfInnerBatchTxn) is valid.
// The flags mask permits the tfUniversal bits, so the transaction succeeds rather
// than being rejected temINVALID_FLAG (a tes-vs-tem consensus fork before the fix).
// Reference: rippled DepositPreauth has no getFlagsMask override → tfUniversalMask.
func TestDepositPreauth_Precedence_FullyCanonicalSigAllowed(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	becky := jtx.NewAccount("becky")
	env.Fund(alice, becky)
	env.Close()

	result := env.Submit(dp.Auth(alice, becky).Flags(tx.TfFullyCanonicalSig).Build())
	jtx.RequireTxSuccess(t, result)
	jtx.RequireLedgerEntryExists(t, env, dp.DepositPreauthKeylet(alice, becky))
}

// TestDepositPreauth_Precedence_EmptyCredentialsDisabledAmendment pins finding #6:
// with featureCredentials disabled, a present-but-empty AuthorizeCredentials array
// is temDISABLED (the checkExtraFeatures field-presence gate), not temARRAY_EMPTY.
// The amendment gate is keyed on field presence and precedes the preflight body.
// Reference: rippled DepositPreauth.cpp checkExtraFeatures (isFieldPresent gate).
func TestDepositPreauth_Precedence_EmptyCredentialsDisabledAmendment(t *testing.T) {
	env := jtx.NewTestEnv(t)
	env.DisableFeature("Credentials")
	alice := jtx.NewAccount("alice")
	env.Fund(alice)
	env.Close()

	raw := dp.Raw(alice.Address).
		AuthorizeCredentials([]depositpreauth.CredentialWrapper{}).
		Fee("10").
		Sequence(env.Seq(alice))
	jtx.RequireTxFail(t, env.Submit(raw.Build()), "temDISABLED")
}
