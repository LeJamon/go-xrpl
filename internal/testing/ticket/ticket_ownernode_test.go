// Regression test for the sfOwnerNode directory-page bug class (see issue #729 /
// EscrowCreate). A created ledger object must record the owner-directory page
// returned by DirInsert in sfOwnerNode, not a hardcoded 0 — otherwise the SLE
// serializes differently from rippled once the directory paginates, diverging
// account_hash → consensus fork. Ticket is the most exposed: one TicketCreate
// can mint up to 250 tickets, paginating the owner directory within a single tx.
// Reference: rippled CreateTicket.cpp:126-137.
package ticket_test

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

// ownerNodeOfSLE reads the ledger object at ledgerIndexHex and returns its
// sfOwnerNode as a uint64 (decoded from the on-ledger state bytes).
func ownerNodeOfSLE(t *testing.T, env *jtx.TestEnv, ledgerIndexHex string) uint64 {
	t.Helper()
	raw, err := hex.DecodeString(ledgerIndexHex)
	require.NoError(t, err)
	var k [32]byte
	copy(k[:], raw)
	data, err := env.LedgerEntry(keylet.Keylet{Key: k})
	require.NoError(t, err)
	fields, err := binarycodec.Decode(hex.EncodeToString(data))
	require.NoError(t, err)
	s, ok := fields["OwnerNode"].(string)
	require.True(t, ok, "OwnerNode must be present in SLE")
	v, err := strconv.ParseUint(s, 16, 64)
	require.NoError(t, err)
	return v
}

func TestTicketCreate_OwnerNode_Pagination(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	env.FundAmount(alice, uint64(jtx.XRP(100000)))
	env.Close()

	// 33 tickets: 32 fill page 0, the 33rd lands on page 1 of the owner dir.
	r := env.Submit(ticket.TicketCreate(alice, 33).Build())
	jtx.RequireTxSuccess(t, r)

	page0, page1 := 0, 0
	for _, n := range r.Metadata.AffectedNodes {
		if n.NodeType != "CreatedNode" || n.LedgerEntryType != "Ticket" {
			continue
		}
		switch ownerNodeOfSLE(t, env, n.LedgerIndex) {
		case 0:
			page0++
		case 1:
			page1++
		default:
			t.Fatalf("unexpected ticket OwnerNode page")
		}
	}
	require.Equal(t, 32, page0, "32 tickets on owner-dir page 0")
	require.Equal(t, 1, page1, "1 ticket on owner-dir page 1 (proves page captured, not hardcoded 0)")
}
