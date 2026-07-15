package credential_test

import (
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	credentialtest "github.com/LeJamon/go-xrpl/internal/testing/credential"
)

// The Credential family's fixInvalidTxFlags-gated flag mask now lives in
// GetFlagsMask, enforced at preflight0 — before the type's field checks and
// before signature verification — matching rippled. Previously it was the first
// statement of Apply(), so a transaction that was both flag-invalid and
// field-malformed surfaced the field's tem* code instead of temINVALID_FLAG.
//
// Each pin combines an invalid flag (0x1, rejected under fixInvalidTxFlags, which
// the test env enables via PresetAllSupported) with a field that would otherwise
// fail Validate. The expected result is temINVALID_FLAG, proving the mask wins.

func TestPrecedence_CredentialCreateFlagBeforeField(t *testing.T) {
	env := jtx.NewTestEnv(t)
	issuer := jtx.NewAccount("issuer")
	subject := jtx.NewAccount("subject")
	env.Fund(issuer)
	env.Close()

	// Empty CredentialType is temMALFORMED in Validate; the invalid flag must win.
	create := credentialtest.CredentialCreate(issuer, subject, "").
		Flags(0x00000001).
		Build()
	jtx.RequireTxFail(t, env.Submit(create), jtx.TemINVALID_FLAG)
}

func TestPrecedence_CredentialAcceptFlagBeforeField(t *testing.T) {
	env := jtx.NewTestEnv(t)
	subject := jtx.NewAccount("subject")
	issuer := jtx.NewAccount("issuer")
	env.Fund(subject)
	env.Close()

	// Empty CredentialType is temMALFORMED in Validate; the invalid flag must win.
	accept := credentialtest.CredentialAccept(subject, issuer, "").
		Flags(0x00000001).
		Build()
	jtx.RequireTxFail(t, env.Submit(accept), jtx.TemINVALID_FLAG)
}

func TestPrecedence_CredentialDeleteFlagBeforeField(t *testing.T) {
	env := jtx.NewTestEnv(t)
	sender := jtx.NewAccount("sender")
	env.Fund(sender)
	env.Close()

	// Neither Subject nor Issuer present is temMALFORMED in Validate; the invalid
	// flag must win.
	del := credentialtest.CredentialDelete(sender, nil, nil, "credType").
		Flags(0x00000001).
		Build()
	jtx.RequireTxFail(t, env.Submit(del), jtx.TemINVALID_FLAG)
}
