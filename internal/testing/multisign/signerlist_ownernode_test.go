// Regression test for the sfOwnerNode directory-page bug class (see issue #729).
// A created SignerList must record the owner-directory page from DirInsert in
// sfOwnerNode rather than a hardcoded 0; otherwise the SignerList SLE diverges
// from rippled once the owner directory paginates, forking account_hash.
// Reference: rippled SetSignerList.cpp:384-393.
package multisign_test

import (
	"encoding/hex"
	"strconv"
	"testing"

	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/ticket"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

func TestSignerListSet_OwnerNode_Pagination(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	bogie := jtx.NewAccount("bogie")
	env.FundAmount(alice, uint64(jtx.XRP(100000)))
	env.Fund(bogie)
	env.Close()

	// Fill owner-dir page 0 with 32 tickets, so the signer list lands on page 1.
	jtx.RequireTxSuccess(t, env.Submit(ticket.TicketCreate(alice, 32).Build()))
	env.Close()

	env.SetSignerList(alice, 1, []jtx.TestSigner{{Account: bogie, Weight: 1}})
	env.Close()

	data, err := env.LedgerEntry(keylet.SignerList(alice.ID))
	require.NoError(t, err)
	fields, err := binarycodec.Decode(hex.EncodeToString(data))
	require.NoError(t, err)
	s, ok := fields["OwnerNode"].(string)
	require.True(t, ok, "OwnerNode must be present in SignerList SLE")
	page, err := strconv.ParseUint(s, 16, 64)
	require.NoError(t, err)
	require.Equal(t, uint64(1), page,
		"signer list created after a full page must record owner-dir page 1, not hardcoded 0")
}

// Removing a signer list that lives on a paginated owner directory (page > 0)
// must unlink it from its recorded sfOwnerNode page, not a hardcoded page 0.
// With the bug the signer list's index dangles on page 1, forking account_hash.
// Reference: rippled SetSignerList.cpp:226-228.
func TestSignerListSet_Delete_OwnerNode_Pagination(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	bogie := jtx.NewAccount("bogie")
	env.FundAmount(alice, uint64(jtx.XRP(100000)))
	env.Fund(bogie)
	env.Close()

	// Fill owner-dir page 0 with 32 tickets, so the signer list lands on page 1.
	jtx.RequireTxSuccess(t, env.Submit(ticket.TicketCreate(alice, 32).Build()))
	env.Close()

	env.SetSignerList(alice, 1, []jtx.TestSigner{{Account: bogie, Weight: 1}})
	env.Close()

	data, err := env.LedgerEntry(keylet.SignerList(alice.ID))
	require.NoError(t, err)
	require.NotEmpty(t, data)

	env.RemoveSignerList(alice)
	env.Close()

	gone, _ := env.LedgerEntry(keylet.SignerList(alice.ID))
	require.Empty(t, gone, "signer list SLE must be erased after removal")

	// Page 1 held only the signer list; once unlinked the empty non-root page is erased.
	page1, err := env.LedgerEntry(keylet.OwnerDirPage(alice.ID, 1))
	require.True(t, err != nil || len(page1) == 0,
		"owner-dir page 1 must be erased after removing the signer list it held, not left dangling")
}

// When the signer list is the account's only owned object, removing it empties
// the owner-directory root. rippled passes keepRoot=false and erases the empty
// root SLE (SetSignerList.cpp:228); the old code passed keepRoot=true and left
// an empty root behind, forking account_hash.
func TestSignerListSet_Delete_LastObject_ErasesOwnerDirRoot(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	bogie := jtx.NewAccount("bogie")
	env.Fund(alice)
	env.Fund(bogie)
	env.Close()

	env.SetSignerList(alice, 1, []jtx.TestSigner{{Account: bogie, Weight: 1}})
	env.Close()

	root, err := env.LedgerEntry(keylet.OwnerDir(alice.ID))
	require.NoError(t, err)
	require.NotEmpty(t, root, "owner-dir root must exist while the signer list is owned")

	env.RemoveSignerList(alice)
	env.Close()

	gone, err := env.LedgerEntry(keylet.OwnerDir(alice.ID))
	require.True(t, err != nil || len(gone) == 0,
		"empty owner-directory root must be erased when the last owned object (signer list) is removed")
}
