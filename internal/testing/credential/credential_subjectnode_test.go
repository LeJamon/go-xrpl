// Regression test for the directory-node bug class (see issue #729), Credential
// variant. sfIssuerNode and sfSubjectNode are soeREQUIRED on ltCREDENTIAL, so
// rippled always serializes them — including SubjectNode:0 for a self-issued
// credential (subject == issuer), where doApply leaves it at the template
// default instead of inserting into the subject's directory. goXRPL previously
// omitted SubjectNode in that case, diverging the Credential SLE → account_hash
// fork. Reference: rippled Credentials.cpp:175,180-195; ledger_entries.macro.
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

func TestCredentialCreate_SelfIssued_EmitsSubjectNode(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	env.FundAmount(alice, uint64(jtx.XRP(5000)))
	env.Close()

	credType := "abcde"
	// Self-issued: issuer == subject == alice.
	r := env.Submit(credential.CredentialCreate(alice, alice, credType).Build())
	jtx.RequireTxSuccess(t, r)

	key := keylet.Credential(alice.ID, alice.ID, []byte(credType))
	data, err := env.LedgerEntry(key)
	require.NoError(t, err)
	fields, err := binarycodec.Decode(hex.EncodeToString(data))
	require.NoError(t, err)

	// Both node fields must be present in the serialized state, even at 0.
	_, hasIssuerNode := fields["IssuerNode"]
	require.True(t, hasIssuerNode, "IssuerNode must be present")
	subjectNode, hasSubjectNode := fields["SubjectNode"]
	require.True(t, hasSubjectNode, "self-issued credential must still serialize SubjectNode (rippled emits 0)")
	require.Equal(t, "0", subjectNode, "self-issued SubjectNode is page 0")
}
