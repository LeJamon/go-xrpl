package credential_test

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/credential"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

func TestCredentialDeleteCorruptSubjectDirectoryIsAtomic(t *testing.T) {
	tests := []struct {
		name    string
		corrupt func(*testing.T, *jtx.TestEnv, keylet.Keylet)
	}{
		{
			name: "missing page",
			corrupt: func(t *testing.T, env *jtx.TestEnv, directory keylet.Keylet) {
				t.Helper()
				require.NoError(t, env.Ledger().Erase(directory))
			},
		},
		{
			name: "missing item",
			corrupt: func(t *testing.T, env *jtx.TestEnv, directory keylet.Keylet) {
				t.Helper()
				data, err := env.LedgerEntry(directory)
				require.NoError(t, err)
				node, err := state.ParseDirectoryNode(data)
				require.NoError(t, err)
				node.Indexes = nil
				data, err = state.SerializeDirectoryNode(node, false)
				require.NoError(t, err)
				require.NoError(t, env.Ledger().Update(directory, data))
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			issuer := jtx.NewAccount("issuer")
			subject := jtx.NewAccount("subject")
			env := jtx.NewTestEnv(t)
			env.Fund(issuer, subject)
			env.Close()

			const credentialType = "atomic"
			jtx.RequireTxSuccess(t, env.Submit(
				credential.CredentialCreateText(issuer, subject, credentialType).Build(),
			))
			jtx.RequireTxSuccess(t, env.Submit(
				credential.CredentialAcceptText(subject, issuer, credentialType).Build(),
			))
			env.Close()

			credentialKey := jtx.CredentialKeylet(subject, issuer, credentialType)
			issuerDirectory := keylet.OwnerDir(issuer.ID)
			subjectDirectory := keylet.OwnerDir(subject.ID)
			credentialBefore, err := env.LedgerEntry(credentialKey)
			require.NoError(t, err)
			issuerDirectoryBefore, err := env.LedgerEntry(issuerDirectory)
			require.NoError(t, err)
			test.corrupt(t, env, subjectDirectory)
			subjectDirectoryBefore, err := env.LedgerEntry(subjectDirectory)
			require.NoError(t, err)
			issuerBalance := env.Balance(issuer)
			subjectBalance := env.Balance(subject)
			issuerSequence := env.Seq(issuer)
			subjectSequence := env.Seq(subject)
			issuerOwnerCount := env.OwnerCount(issuer)
			subjectOwnerCount := env.OwnerCount(subject)

			result := env.Submit(
				credential.CredentialDeleteText(subject, subject, issuer, credentialType).Build(),
			)
			jtx.RequireTxFail(t, result, jtx.TefBAD_LEDGER)
			require.Nil(t, result.Metadata)
			jtx.RequireBalance(t, env, issuer, issuerBalance)
			jtx.RequireBalance(t, env, subject, subjectBalance)
			jtx.RequireSequence(t, env, issuer, issuerSequence)
			jtx.RequireSequence(t, env, subject, subjectSequence)
			jtx.RequireOwnerCount(t, env, issuer, issuerOwnerCount)
			jtx.RequireOwnerCount(t, env, subject, subjectOwnerCount)

			credentialAfter, err := env.LedgerEntry(credentialKey)
			require.NoError(t, err)
			require.Equal(t, credentialBefore, credentialAfter)
			issuerDirectoryAfter, err := env.LedgerEntry(issuerDirectory)
			require.NoError(t, err)
			require.Equal(t, issuerDirectoryBefore, issuerDirectoryAfter)
			subjectDirectoryAfter, err := env.LedgerEntry(subjectDirectory)
			require.NoError(t, err)
			require.Equal(t, subjectDirectoryBefore, subjectDirectoryAfter)
		})
	}
}
