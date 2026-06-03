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
