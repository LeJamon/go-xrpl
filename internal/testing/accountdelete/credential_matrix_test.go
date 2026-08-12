package accountdelete_test

import (
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	credentialtest "github.com/LeJamon/go-xrpl/internal/testing/credential"
	"github.com/LeJamon/go-xrpl/internal/testing/depositpreauth"
	"github.com/LeJamon/go-xrpl/keylet"
)

func TestAccountDelete_CredentialAuthorization(t *testing.T) {
	const credentialType = "account-delete"

	t.Run("credential preauthorization", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		destination := jtx.NewAccount("destination")
		subject := jtx.NewAccount("subject")
		issuer := jtx.NewAccount("issuer")
		env.Fund(destination, subject, issuer)
		env.Close()
		env.EnableDepositAuth(destination)
		env.Close()

		credentialKey := keylet.Credential(subject.ID, issuer.ID, []byte(credentialType))
		credentialID := depositpreauth.CredentialIndexHex(subject, issuer, credentialType)
		jtx.RequireTxSuccess(t, env.Submit(
			credentialtest.CredentialCreateText(issuer, subject, credentialType).Build()))
		env.Close()

		env.IncLedgerSeqForAccDel(subject)
		unaccepted := newAccountDelete(env, subject, destination)
		unaccepted.CredentialIDs = []string{credentialID}
		jtx.RequireTxFail(t, env.Submit(unaccepted), "tecBAD_CREDENTIALS")
		env.Close()

		jtx.RequireTxSuccess(t, env.Submit(
			credentialtest.CredentialAcceptText(subject, issuer, credentialType).Build()))
		env.Close()
		env.IncLedgerSeqForAccDel(issuer)
		wrongSubject := newAccountDelete(env, issuer, destination)
		wrongSubject.CredentialIDs = []string{credentialID}
		jtx.RequireTxFail(t, env.Submit(wrongSubject), "tecBAD_CREDENTIALS")

		env.IncLedgerSeqForAccDel(subject)
		unauthorized := newAccountDelete(env, subject, destination)
		unauthorized.CredentialIDs = []string{credentialID}
		jtx.RequireTxFail(t, env.Submit(unauthorized), jtx.TecNO_PERMISSION)
		env.Close()

		jtx.RequireTxSuccess(t, env.Submit(depositpreauth.AuthCredentials(destination,
			[]depositpreauth.AuthorizeCredentials{{Issuer: issuer, CredTypeText: credentialType}}).Build()))
		env.Close()
		env.IncLedgerSeqForAccDel(subject)
		before := captureDeleteBalances(env, subject, destination)
		d := newAccountDelete(env, subject, destination)
		d.CredentialIDs = []string{credentialID}
		result := env.Submit(d)
		requireAccountDeleteSuccess(t, env, result, subject, destination, before, credentialKey)
	})

	t.Run("direct account preauthorization", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		destination := jtx.NewAccount("destination")
		subject := jtx.NewAccount("subject")
		issuer := jtx.NewAccount("issuer")
		env.Fund(destination, subject, issuer)
		env.Close()
		env.EnableDepositAuth(destination)
		env.Preauthorize(destination, subject)
		env.Close()

		credentialKey := keylet.Credential(subject.ID, issuer.ID, []byte(credentialType))
		credentialID := depositpreauth.CredentialIndexHex(subject, issuer, credentialType)
		jtx.RequireTxSuccess(t, env.Submit(
			credentialtest.CredentialCreateText(issuer, subject, credentialType).Build()))
		env.Close()
		jtx.RequireTxSuccess(t, env.Submit(
			credentialtest.CredentialAcceptText(subject, issuer, credentialType).Build()))
		env.Close()

		env.IncLedgerSeqForAccDel(subject)
		before := captureDeleteBalances(env, subject, destination)
		d := newAccountDelete(env, subject, destination)
		d.CredentialIDs = []string{credentialID}
		result := env.Submit(d)
		requireAccountDeleteSuccess(t, env, result, subject, destination, before, credentialKey)
	})

	t.Run("credentials not required", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		destination := jtx.NewAccount("destination")
		subject := jtx.NewAccount("subject")
		issuer := jtx.NewAccount("issuer")
		env.Fund(destination, subject, issuer)
		env.Close()

		credentialKey := keylet.Credential(subject.ID, issuer.ID, []byte(credentialType))
		credentialID := depositpreauth.CredentialIndexHex(subject, issuer, credentialType)
		jtx.RequireTxSuccess(t, env.Submit(
			credentialtest.CredentialCreateText(issuer, subject, credentialType).Build()))
		env.Close()
		jtx.RequireTxSuccess(t, env.Submit(
			credentialtest.CredentialAcceptText(subject, issuer, credentialType).Build()))
		env.Close()

		env.IncLedgerSeqForAccDel(subject)
		before := captureDeleteBalances(env, subject, destination)
		d := newAccountDelete(env, subject, destination)
		d.CredentialIDs = []string{credentialID}
		result := env.Submit(d)
		requireAccountDeleteSuccess(t, env, result, subject, destination, before, credentialKey)
	})

	t.Run("unknown credential", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		alice := jtx.NewAccount("alice")
		destination := jtx.NewAccount("destination")
		env.Fund(alice, destination)
		env.Close()
		env.IncLedgerSeqForAccDel(alice)

		d := newAccountDelete(env, alice, destination)
		d.CredentialIDs = []string{credID}
		jtx.RequireTxFail(t, env.Submit(d), "tecBAD_CREDENTIALS")
	})
}
