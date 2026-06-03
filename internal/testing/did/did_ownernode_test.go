// Regression test for the sfOwnerNode directory-page bug class (see issue #729).
// A created DID must record the owner-directory page from DirInsert in
// sfOwnerNode rather than a hardcoded 0; otherwise the DID SLE diverges from
// rippled once the owner directory paginates, forking account_hash.
// Reference: rippled DID.cpp:105-109.
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

	r := env.Submit(did.DIDSet(alice).URI("4142").Build())
	jtx.RequireTxSuccess(t, r)

	data, err := env.LedgerEntry(keylet.DID(alice.ID))
	require.NoError(t, err)
	d, err := state.ParseDID(data)
	require.NoError(t, err)
	require.Equal(t, uint64(1), d.OwnerNode,
		"DID created after a full page must record owner-dir page 1, not hardcoded 0")
}

// When the DID is the account's first owned object, the owner directory is
// created fresh — and must carry sfOwner (rippled's describeOwnerDir). The old
// code passed a nil setupFunc, omitting sfOwner and diverging the directory SLE.
func TestDIDSet_FirstObject_OwnerDirHasOwner(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	env.FundAmount(alice, uint64(jtx.XRP(5000)))
	env.Close()

	jtx.RequireTxSuccess(t, env.Submit(did.DIDSet(alice).URI("4142").Build()))

	data, err := env.LedgerEntry(keylet.OwnerDir(alice.ID))
	require.NoError(t, err)
	fields, err := binarycodec.Decode(hex.EncodeToString(data))
	require.NoError(t, err)
	require.Equal(t, alice.Address, fields["Owner"],
		"owner directory must record sfOwner when created by DIDSet")
}
