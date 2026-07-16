package permissioneddomain_test

import (
	"encoding/hex"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/metadata"
	pd "github.com/LeJamon/go-xrpl/internal/testing/permissioneddomain"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

func readOwnerDirectory(t *testing.T, env *jtx.TestEnv, owner *jtx.Account) *state.DirectoryNode {
	t.Helper()

	data, err := env.LedgerEntry(keylet.OwnerDir(owner.ID))
	require.NoError(t, err)
	require.NotNil(t, data)

	dir, err := state.ParseDirectoryNode(data)
	require.NoError(t, err)
	return dir
}

func TestPermissionedDomainDeleteKeepsEmptyOwnerDirectory(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	issuer := jtx.NewAccount("issuer")
	env.Fund(alice, issuer)
	env.Close()

	createLedgerSeq := env.LedgerSeq()
	domainSeq := env.Seq(alice)
	create := pd.DomainSet(alice).Credential(issuer, credTypeHex(10)).Build()
	createResult := env.Submit(create)
	jtx.RequireTxSuccess(t, createResult)
	createHash, err := tx.ComputeTransactionHash(create)
	require.NoError(t, err)
	env.Close()

	ownerDirKey := keylet.OwnerDir(alice.ID)
	domainKey := domainKeylet(alice, domainSeq)
	dirBefore := readOwnerDirectory(t, env, alice)
	require.Equal(t, [][32]byte{domainKey.Key}, dirBefore.Indexes)
	require.Equal(t, createHash, dirBefore.PreviousTxnID)
	require.Equal(t, createLedgerSeq, dirBefore.PreviousTxnLgrSeq)

	deleteLedgerSeq := env.LedgerSeq()
	deleteTx := pd.DomainDelete(alice, domainIDHex(domainKey)).Build()
	deleteResult := env.Submit(deleteTx)
	jtx.RequireTxSuccess(t, deleteResult)
	deleteHash, err := tx.ComputeTransactionHash(deleteTx)
	require.NoError(t, err)
	env.Close()

	dirAfter := readOwnerDirectory(t, env, alice)
	require.Empty(t, dirAfter.Indexes)
	require.Equal(t, alice.ID, dirAfter.Owner)
	require.Equal(t, ownerDirKey.Key, dirAfter.RootIndex)
	require.Equal(t, deleteHash, dirAfter.PreviousTxnID)
	require.Equal(t, deleteLedgerSeq, dirAfter.PreviousTxnLgrSeq)
	require.Zero(t, env.OwnerCount(alice))
	require.Nil(t, getDomainEntry(t, env, domainKey))

	require.NotNil(t, deleteResult.Metadata)
	dirNode := metadata.FindNode(deleteResult.Metadata, "ModifiedNode", "DirectoryNode")
	require.NotNil(t, dirNode)
	require.Equal(t, strings.ToUpper(hex.EncodeToString(ownerDirKey.Key[:])), dirNode.LedgerIndex)
	require.Equal(t, strings.ToUpper(hex.EncodeToString(createHash[:])), dirNode.PreviousTxnID)
	require.Equal(t, createLedgerSeq, dirNode.PreviousTxnLgrSeq)
	require.NotContains(t, dirNode.FinalFields, "Indexes")
	require.NotContains(t, dirNode.PreviousFields, "Indexes")
	require.Nil(t, metadata.FindNode(deleteResult.Metadata, "DeletedNode", "DirectoryNode"))
}

func TestPermissionedDomainDeleteKeepsRemainingOwnerDirectoryEntries(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	issuer := jtx.NewAccount("issuer")
	env.Fund(alice, issuer)
	env.Close()

	firstSeq := env.Seq(alice)
	jtx.RequireTxSuccess(t, env.Submit(pd.DomainSet(alice).Credential(issuer, credTypeHex(1)).Build()))
	env.Close()
	secondSeq := env.Seq(alice)
	jtx.RequireTxSuccess(t, env.Submit(pd.DomainSet(alice).Credential(issuer, credTypeHex(2)).Build()))
	env.Close()

	firstDomain := domainKeylet(alice, firstSeq)
	secondDomain := domainKeylet(alice, secondSeq)
	require.Len(t, readOwnerDirectory(t, env, alice).Indexes, 2)

	result := env.Submit(pd.DomainDelete(alice, domainIDHex(firstDomain)).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	dir := readOwnerDirectory(t, env, alice)
	require.Equal(t, [][32]byte{secondDomain.Key}, dir.Indexes)
	require.Equal(t, uint32(1), env.OwnerCount(alice))
	require.Nil(t, getDomainEntry(t, env, firstDomain))
	require.NotNil(t, getDomainEntry(t, env, secondDomain))

	require.NotNil(t, result.Metadata)
	require.NotNil(t, metadata.FindNode(result.Metadata, "ModifiedNode", "DirectoryNode"))
	require.Nil(t, metadata.FindNode(result.Metadata, "DeletedNode", "DirectoryNode"))
}

func TestPermissionedDomainDeleteRejectsMissingOwnerDirectoryEntry(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	issuer := jtx.NewAccount("issuer")
	env.Fund(alice, issuer)
	env.Close()

	domainSeq := env.Seq(alice)
	jtx.RequireTxSuccess(t, env.Submit(pd.DomainSet(alice).Credential(issuer, credTypeHex(10)).Build()))
	env.Close()

	domainKey := domainKeylet(alice, domainSeq)
	require.NoError(t, env.Ledger().Erase(keylet.OwnerDir(alice.ID)))

	result := env.Submit(pd.DomainDelete(alice, domainIDHex(domainKey)).Build())
	jtx.RequireTxFail(t, result, jtx.TefBAD_LEDGER)
	require.Equal(t, uint32(1), env.OwnerCount(alice))
	require.NotNil(t, getDomainEntry(t, env, domainKey))
}
