package did_test

import (
	"encoding/hex"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/did"
	"github.com/LeJamon/go-xrpl/internal/testing/metadata"
	"github.com/LeJamon/go-xrpl/keylet"
)

func TestDIDMetadataLifecycle(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	env.Fund(alice)
	env.Close()

	create := env.Submit(did.DIDSet(alice).URI("uri1").Document("doc1").Build())
	jtx.RequireTxSuccess(t, create)
	require.NotNil(t, create.Metadata)
	createDID := metadata.FindNode(create.Metadata, "CreatedNode", "DID")
	require.NotNil(t, createDID)
	require.Equal(t, didLedgerIndex(alice), createDID.LedgerIndex)
	require.Equal(t, alice.Address, createDID.NewFields["Account"])
	require.Equal(t, hex.EncodeToString([]byte("uri1")), strings.ToLower(createDID.NewFields["URI"].(string)))
	require.Equal(t, hex.EncodeToString([]byte("doc1")), strings.ToLower(createDID.NewFields["DIDDocument"].(string)))
	require.NotContains(t, createDID.NewFields, "OwnerNode")
	require.NotNil(t, metadata.FindNode(create.Metadata, "CreatedNode", "DirectoryNode"))
	createAccount := metadata.FindNode(create.Metadata, "ModifiedNode", "AccountRoot")
	require.NotNil(t, createAccount)
	require.Equal(t, uint32(0), createAccount.PreviousFields["OwnerCount"])
	require.Equal(t, uint32(1), createAccount.FinalFields["OwnerCount"])
	env.Close()
	require.NotNil(t, getDIDEntry(t, env, alice))

	update := env.Submit(did.DIDSet(alice).URI("uri2").Document("").Build())
	jtx.RequireTxSuccess(t, update)
	require.NotNil(t, update.Metadata)
	updateDID := metadata.FindNode(update.Metadata, "ModifiedNode", "DID")
	require.NotNil(t, updateDID)
	require.Equal(t, didLedgerIndex(alice), updateDID.LedgerIndex)
	require.Equal(t, hex.EncodeToString([]byte("uri1")), strings.ToLower(updateDID.PreviousFields["URI"].(string)))
	require.Equal(t, hex.EncodeToString([]byte("doc1")), strings.ToLower(updateDID.PreviousFields["DIDDocument"].(string)))
	require.Equal(t, hex.EncodeToString([]byte("uri2")), strings.ToLower(updateDID.FinalFields["URI"].(string)))
	require.NotContains(t, updateDID.FinalFields, "DIDDocument")
	require.Nil(t, metadata.FindNode(update.Metadata, "ModifiedNode", "DirectoryNode"))
	env.Close()
	updated := getDIDEntry(t, env, alice)
	require.NotNil(t, updated)
	checkVL(t, "URI", updated.URI, "uri2")
	require.Empty(t, updated.DIDDocument)

	deleted := env.Submit(did.DIDDelete(alice).Build())
	jtx.RequireTxSuccess(t, deleted)
	require.NotNil(t, deleted.Metadata)
	deletedDID := metadata.FindNode(deleted.Metadata, "DeletedNode", "DID")
	require.NotNil(t, deletedDID)
	require.Equal(t, didLedgerIndex(alice), deletedDID.LedgerIndex)
	require.Equal(t, alice.Address, deletedDID.FinalFields["Account"])
	require.Equal(t, hex.EncodeToString([]byte("uri2")), strings.ToLower(deletedDID.FinalFields["URI"].(string)))
	require.NotNil(t, metadata.FindNode(deleted.Metadata, "ModifiedNode", "DirectoryNode"))
	deleteAccount := metadata.FindNode(deleted.Metadata, "ModifiedNode", "AccountRoot")
	require.NotNil(t, deleteAccount)
	require.Equal(t, uint32(1), deleteAccount.PreviousFields["OwnerCount"])
	require.Equal(t, uint32(0), deleteAccount.FinalFields["OwnerCount"])
	env.Close()
	requireDIDAbsent(t, env, alice)
}

func didLedgerIndex(account *jtx.Account) string {
	key := keylet.DID(account.ID).Key
	return strings.ToUpper(hex.EncodeToString(key[:]))
}
