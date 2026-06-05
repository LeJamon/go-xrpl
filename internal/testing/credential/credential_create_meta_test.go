// Confirmation test that CredentialCreate produces exactly the CreatedNode
// NewFields rippled does. Investigated as a candidate meta-completeness
// divergence; the goXRPL created SLE matches rippled (Credentials.cpp:158-197):
// for a non-self-issued credential the NewFields are {Subject, Issuer,
// CredentialType}. sfIssuerNode/sfSubjectNode are 0 (page 0) and default, so
// excluded from NewFields; sfFlags is only set (lsfAccepted) for self-issued.
package credential_test

import (
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/credential"
	"github.com/stretchr/testify/require"
)

func TestCredentialCreate_Meta_NewFields(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	env.FundAmount(alice, uint64(jtx.XRP(5000)))
	env.FundAmount(bob, uint64(jtx.XRP(5000)))
	env.Close()

	const credTypeHex = "6162636465" // "abcde"
	res := env.Submit(credential.CredentialCreate(alice, bob, credTypeHex).Build())
	jtx.RequireTxSuccess(t, res)
	require.NotNil(t, res.Metadata)

	var nf map[string]any
	for _, n := range res.Metadata.AffectedNodes {
		if n.NodeType == "CreatedNode" && n.LedgerEntryType == "Credential" {
			nf = n.NewFields
		}
	}
	require.NotNil(t, nf, "Credential CreatedNode expected")
	require.Equal(t, bob.Address, nf["Subject"])
	require.Equal(t, alice.Address, nf["Issuer"])
	require.Equal(t, credTypeHex, nf["CredentialType"])
	// Defaulted required node fields excluded; no Flags for non-self-issued.
	require.NotContains(t, nf, "IssuerNode", "IssuerNode=0 is default; excluded from NewFields")
	require.NotContains(t, nf, "SubjectNode", "SubjectNode=0 is default; excluded from NewFields")
	require.NotContains(t, nf, "Flags", "non-self-issued credential has no Flags")
}
