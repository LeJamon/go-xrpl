package did_test

import (
	"encoding/hex"
	"testing"

	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/did"
	"github.com/LeJamon/go-xrpl/internal/testing/ticket"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

func TestDIDSet_OwnerNode_Pagination(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	env.FundAmount(alice, uint64(jtx.XRP(100000)))
	env.Close()

	// Fill owner-dir page 0 with 32 tickets, so the DID lands on page 1.
	jtx.RequireTxSuccess(t, env.Submit(ticket.TicketCreate(alice, 32).Build()))
	env.Close()

	r := env.Submit(did.DIDSet(alice).URIHex("4142").Build())
	jtx.RequireTxSuccess(t, r)

	data, err := env.LedgerEntry(keylet.DID(alice.ID))
	require.NoError(t, err)
	d, err := state.ParseDID(data)
	require.NoError(t, err)
	require.Equal(t, uint64(1), d.OwnerNode,
		"DID created after a full page must record owner-dir page 1, not hardcoded 0")
}

func TestDIDSet_FirstObject_OwnerDirHasOwner(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	env.FundAmount(alice, uint64(jtx.XRP(5000)))
	env.Close()

	jtx.RequireTxSuccess(t, env.Submit(did.DIDSet(alice).URIHex("4142").Build()))

	data, err := env.LedgerEntry(keylet.OwnerDir(alice.ID))
	require.NoError(t, err)
	fields, err := binarycodec.Decode(hex.EncodeToString(data))
	require.NoError(t, err)
	require.Equal(t, alice.Address, fields["Owner"],
		"owner directory must record sfOwner when created by DIDSet")
}

func TestDIDDelete_OwnerNode_Pagination(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	env.FundAmount(alice, uint64(jtx.XRP(100000)))
	env.Close()

	// Fill owner-dir page 0 with 32 tickets, so the DID lands on page 1.
	jtx.RequireTxSuccess(t, env.Submit(ticket.TicketCreate(alice, 32).Build()))
	env.Close()

	jtx.RequireTxSuccess(t, env.Submit(did.DIDSet(alice).URIHex("4142").Build()))
	env.Close()

	data, err := env.LedgerEntry(keylet.DID(alice.ID))
	require.NoError(t, err)
	d, err := state.ParseDID(data)
	require.NoError(t, err)
	require.Equal(t, uint64(1), d.OwnerNode, "DID must land on owner-dir page 1")
	page1, err := env.LedgerEntry(keylet.OwnerDirPage(alice.ID, 1))
	require.NoError(t, err)
	require.NotNil(t, page1)
	page1Node, err := state.ParseDirectoryNode(page1)
	require.NoError(t, err)
	require.Equal(t, [][32]byte{keylet.DID(alice.ID).Key}, page1Node.Indexes)
	require.Equal(t, uint32(33), env.OwnerCount(alice))

	jtx.RequireTxSuccess(t, env.Submit(did.DIDDelete(alice).Build()))
	env.Close()

	gone, err := env.LedgerEntry(keylet.DID(alice.ID))
	require.NoError(t, err)
	require.Nil(t, gone, "DID SLE must be erased after delete")

	page1, err = env.LedgerEntry(keylet.OwnerDirPage(alice.ID, 1))
	require.NoError(t, err)
	require.Nil(t, page1)
	root, err := env.LedgerEntry(keylet.OwnerDir(alice.ID))
	require.NoError(t, err)
	require.NotNil(t, root)
	rootNode, err := state.ParseDirectoryNode(root)
	require.NoError(t, err)
	require.Len(t, rootNode.Indexes, 32)
	require.Zero(t, rootNode.IndexNext)
	require.Equal(t, uint32(32), env.OwnerCount(alice))
}
