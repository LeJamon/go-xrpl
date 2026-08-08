package credential_test

import (
	"fmt"
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/credential"
	metatesting "github.com/LeJamon/go-xrpl/internal/testing/metadata"
	"github.com/stretchr/testify/require"
)

func TestCredentialLifecycleMetadata(t *testing.T) {
	issuer := jtx.NewAccount("issuer")
	subject := jtx.NewAccount("subject")
	env := jtx.NewTestEnv(t)
	env.Fund(issuer, subject)
	env.Close()

	const credentialType = "meta"
	const credentialTypeHex = "6D657461"
	credentialKey := jtx.CredentialKeylet(subject, issuer, credentialType)
	credentialIndex := fmt.Sprintf("%X", credentialKey.Key)

	issuerBalance := env.Balance(issuer)
	issuerSequence := env.Seq(issuer)
	create := env.Submit(credential.CredentialCreateText(issuer, subject, credentialType).Build())
	jtx.RequireTxSuccess(t, create)
	require.NotNil(t, create.Metadata)
	metatesting.CheckNodeCount(t, create, 4)

	createdCredential := metatesting.FindNode(create.Metadata, "CreatedNode", "Credential")
	require.NotNil(t, createdCredential)
	require.Equal(t, credentialIndex, createdCredential.LedgerIndex)
	require.Equal(t, map[string]any{
		"CredentialType": credentialTypeHex,
		"Issuer":         issuer.Address,
		"Subject":        subject.Address,
	}, createdCredential.NewFields)

	createdDirectories := metatesting.FindNodes(create.Metadata, "CreatedNode", "DirectoryNode")
	require.Len(t, createdDirectories, 2)
	owners := make(map[string]bool)
	for _, directory := range createdDirectories {
		require.Equal(t, map[string]any{
			"Owner":     directory.NewFields["Owner"],
			"RootIndex": directory.LedgerIndex,
		}, directory.NewFields)
		owner, ok := directory.NewFields["Owner"].(string)
		require.True(t, ok)
		owners[owner] = true
	}
	require.Equal(t, map[string]bool{issuer.Address: true, subject.Address: true}, owners)

	issuerCreateNode := metatesting.FindNodeByAccount(create.Metadata, issuer.Address)
	require.NotNil(t, issuerCreateNode)
	require.Equal(t, map[string]any{
		"Balance":    fmt.Sprintf("%d", issuerBalance),
		"OwnerCount": uint32(0),
		"Sequence":   issuerSequence,
	}, issuerCreateNode.PreviousFields)
	require.Equal(t, uint32(1), metatesting.ToUint32(issuerCreateNode.FinalFields["OwnerCount"]))

	env.Close()
	subjectBalance := env.Balance(subject)
	subjectSequence := env.Seq(subject)
	accept := env.Submit(credential.CredentialAcceptText(subject, issuer, credentialType).Build())
	jtx.RequireTxSuccess(t, accept)
	require.NotNil(t, accept.Metadata)
	metatesting.CheckNodeCount(t, accept, 3)

	modifiedCredential := metatesting.FindNode(accept.Metadata, "ModifiedNode", "Credential")
	require.NotNil(t, modifiedCredential)
	require.Equal(t, credentialIndex, modifiedCredential.LedgerIndex)
	require.Equal(t, map[string]any{"Flags": uint32(0)}, modifiedCredential.PreviousFields)
	require.Equal(t, map[string]any{
		"CredentialType": credentialTypeHex,
		"Flags":          uint32(0x00010000),
		"Issuer":         issuer.Address,
		"IssuerNode":     "0",
		"Subject":        subject.Address,
		"SubjectNode":    "0",
	}, modifiedCredential.FinalFields)

	issuerAcceptNode := metatesting.FindNodeByAccount(accept.Metadata, issuer.Address)
	subjectAcceptNode := metatesting.FindNodeByAccount(accept.Metadata, subject.Address)
	require.Equal(t, map[string]any{"OwnerCount": uint32(1)}, issuerAcceptNode.PreviousFields)
	require.Equal(t, uint32(0), metatesting.ToUint32(issuerAcceptNode.FinalFields["OwnerCount"]))
	require.Equal(t, map[string]any{
		"Balance":    fmt.Sprintf("%d", subjectBalance),
		"OwnerCount": uint32(0),
		"Sequence":   subjectSequence,
	}, subjectAcceptNode.PreviousFields)
	require.Equal(t, uint32(1), metatesting.ToUint32(subjectAcceptNode.FinalFields["OwnerCount"]))

	env.Close()
	subjectBalance = env.Balance(subject)
	subjectSequence = env.Seq(subject)
	deleteResult := env.Submit(
		credential.CredentialDeleteText(subject, subject, issuer, credentialType).Build(),
	)
	jtx.RequireTxSuccess(t, deleteResult)
	require.NotNil(t, deleteResult.Metadata)
	metatesting.CheckNodeCount(t, deleteResult, 4)

	deletedCredential := metatesting.FindNode(deleteResult.Metadata, "DeletedNode", "Credential")
	require.NotNil(t, deletedCredential)
	require.Equal(t, credentialIndex, deletedCredential.LedgerIndex)
	require.Equal(t, credentialTypeHex, deletedCredential.FinalFields["CredentialType"])
	require.Equal(t, uint32(0x00010000), deletedCredential.FinalFields["Flags"])
	require.Equal(t, issuer.Address, deletedCredential.FinalFields["Issuer"])
	require.Equal(t, subject.Address, deletedCredential.FinalFields["Subject"])
	require.Equal(t, "0", deletedCredential.FinalFields["IssuerNode"])
	require.Equal(t, "0", deletedCredential.FinalFields["SubjectNode"])
	require.NotEmpty(t, deletedCredential.FinalFields["PreviousTxnID"])
	require.NotZero(t, metatesting.ToUint32(deletedCredential.FinalFields["PreviousTxnLgrSeq"]))
	require.Nil(t, deletedCredential.PreviousFields)

	deletedDirectories := metatesting.FindNodes(deleteResult.Metadata, "DeletedNode", "DirectoryNode")
	require.Len(t, deletedDirectories, 2)
	for _, directory := range deletedDirectories {
		require.Equal(t, uint32(0), directory.FinalFields["Flags"])
		require.Equal(t, directory.LedgerIndex, directory.FinalFields["RootIndex"])
		require.NotEmpty(t, directory.FinalFields["PreviousTxnID"])
		require.NotZero(t, metatesting.ToUint32(directory.FinalFields["PreviousTxnLgrSeq"]))
		require.Nil(t, directory.PreviousFields)
	}

	subjectDeleteNode := metatesting.FindNodeByAccount(deleteResult.Metadata, subject.Address)
	require.NotNil(t, subjectDeleteNode)
	require.Equal(t, map[string]any{
		"Balance":    fmt.Sprintf("%d", subjectBalance),
		"OwnerCount": uint32(1),
		"Sequence":   subjectSequence,
	}, subjectDeleteNode.PreviousFields)
	require.Equal(t, uint32(0), metatesting.ToUint32(subjectDeleteNode.FinalFields["OwnerCount"]))
}
