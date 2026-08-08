package credential_test

import (
	"encoding/hex"
	"testing"

	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/credential"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

func TestCredentialCreate_SelfIssued_OmitsSubjectNode(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	env.FundAmount(alice, uint64(jtx.XRP(5000)))
	env.Close()

	credType := "abcde"
	r := env.Submit(credential.CredentialCreateText(alice, alice, credType).Build())
	jtx.RequireTxSuccess(t, r)

	key := keylet.Credential(alice.ID, alice.ID, []byte(credType))
	data, err := env.LedgerEntry(key)
	require.NoError(t, err)
	fields, err := binarycodec.Decode(hex.EncodeToString(data))
	require.NoError(t, err)

	_, hasIssuerNode := fields["IssuerNode"]
	require.True(t, hasIssuerNode, "IssuerNode must be present")
	_, hasSubjectNode := fields["SubjectNode"]
	require.False(t, hasSubjectNode, "self-issued credential must omit SubjectNode")
}

func TestCredentialCreate_CrossAccount_EmitsZeroSubjectNode(t *testing.T) {
	env := jtx.NewTestEnv(t)
	issuer := jtx.NewAccount("issuer")
	subject := jtx.NewAccount("subject")
	env.Fund(issuer, subject)
	env.Close()

	credType := "abcde"
	r := env.Submit(credential.CredentialCreateText(issuer, subject, credType).Build())
	jtx.RequireTxSuccess(t, r)

	key := keylet.Credential(subject.ID, issuer.ID, []byte(credType))
	data, err := env.LedgerEntry(key)
	require.NoError(t, err)
	fields, err := binarycodec.Decode(hex.EncodeToString(data))
	require.NoError(t, err)

	_, hasIssuerNode := fields["IssuerNode"]
	require.True(t, hasIssuerNode, "IssuerNode must be present")
	subjectNode, hasSubjectNode := fields["SubjectNode"]
	require.True(t, hasSubjectNode, "cross-account credential must include SubjectNode")
	require.Equal(t, "0", subjectNode, "the first subject directory page is zero")
}
