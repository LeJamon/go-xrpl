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

func TestPermissionedDomainDeleteUsesRecordedOwnerDirectoryPage(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	issuer := jtx.NewAccount("issuer")
	env.Fund(alice, issuer)
	env.Close()

	var pageOneDomain keylet.Keylet
	for i := range 33 {
		seq := env.Seq(alice)
		domainKey := domainKeylet(alice, seq)
		jtx.RequireTxSuccess(t, env.Submit(pd.DomainSet(alice).Credential(issuer, credTypeHex(i+1)).Build()))
		if i == 32 {
			pageOneDomain = domainKey
		}
	}
	env.Close()

	domainData, err := env.LedgerEntry(pageOneDomain)
	require.NoError(t, err)
	domain, err := state.ParsePermissionedDomain(domainData)
	require.NoError(t, err)
	require.Equal(t, uint64(1), domain.OwnerNode)

	ownerDirKey := keylet.OwnerDir(alice.ID)
	root := readOwnerDirectory(t, env, alice)
	require.Len(t, root.Indexes, 32)
	require.Equal(t, uint64(1), root.IndexNext)
	require.Equal(t, uint64(1), root.IndexPrevious)

	pageOneKey := keylet.DirPage(ownerDirKey.Key, 1)
	pageOneData, err := env.LedgerEntry(pageOneKey)
	require.NoError(t, err)
	pageOne, err := state.ParseDirectoryNode(pageOneData)
	require.NoError(t, err)
	require.Equal(t, [][32]byte{pageOneDomain.Key}, pageOne.Indexes)

	result := env.Submit(pd.DomainDelete(alice, domainIDHex(pageOneDomain)).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	root = readOwnerDirectory(t, env, alice)
	require.Len(t, root.Indexes, 32)
	require.Zero(t, root.IndexNext)
	require.Zero(t, root.IndexPrevious)
	pageOneData, err = env.LedgerEntry(pageOneKey)
	require.NoError(t, err)
	require.Nil(t, pageOneData)
	require.Nil(t, getDomainEntry(t, env, pageOneDomain))
	require.Equal(t, uint32(32), env.OwnerCount(alice))
}

func TestPermissionedDomainDeleteRejectsCorruptOwnerDirectory(t *testing.T) {
	tests := []struct {
		name    string
		corrupt func(*testing.T, *jtx.TestEnv, keylet.Keylet)
	}{
		{
			name: "missing page",
			corrupt: func(t *testing.T, env *jtx.TestEnv, ownerDirKey keylet.Keylet) {
				t.Helper()
				require.NoError(t, env.Ledger().Erase(ownerDirKey))
			},
		},
		{
			name: "missing item",
			corrupt: func(t *testing.T, env *jtx.TestEnv, ownerDirKey keylet.Keylet) {
				t.Helper()
				data, err := env.LedgerEntry(ownerDirKey)
				require.NoError(t, err)
				dir, err := state.ParseDirectoryNode(data)
				require.NoError(t, err)
				dir.Indexes = nil
				data, err = state.SerializeDirectoryNode(dir, false)
				require.NoError(t, err)
				require.NoError(t, env.Ledger().Update(ownerDirKey, data))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := jtx.NewTestEnv(t)
			alice := jtx.NewAccount("alice")
			issuer := jtx.NewAccount("issuer")
			env.Fund(alice, issuer)
			env.Close()

			domainSeq := env.Seq(alice)
			jtx.RequireTxSuccess(t, env.Submit(pd.DomainSet(alice).Credential(issuer, credTypeHex(10)).Build()))
			env.Close()

			domainKey := domainKeylet(alice, domainSeq)
			ownerDirKey := keylet.OwnerDir(alice.ID)
			domainBefore, err := env.LedgerEntry(domainKey)
			require.NoError(t, err)
			tt.corrupt(t, env, ownerDirKey)
			dirBefore, err := env.LedgerEntry(ownerDirKey)
			require.NoError(t, err)
			balanceBefore := env.Balance(alice)
			sequenceBefore := env.Seq(alice)
			ownerCountBefore := env.OwnerCount(alice)

			result := env.Submit(pd.DomainDelete(alice, domainIDHex(domainKey)).Build())
			jtx.RequireTxFail(t, result, jtx.TefBAD_LEDGER)
			require.Nil(t, result.Metadata)
			require.Equal(t, balanceBefore, env.Balance(alice))
			require.Equal(t, sequenceBefore, env.Seq(alice))
			require.Equal(t, ownerCountBefore, env.OwnerCount(alice))

			domainAfter, err := env.LedgerEntry(domainKey)
			require.NoError(t, err)
			require.Equal(t, domainBefore, domainAfter)
			dirAfter, err := env.LedgerEntry(ownerDirKey)
			require.NoError(t, err)
			require.Equal(t, dirBefore, dirAfter)
		})
	}
}
