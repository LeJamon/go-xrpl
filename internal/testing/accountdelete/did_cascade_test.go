package accountdelete_test

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/did"
	"github.com/LeJamon/go-xrpl/internal/testing/metadata"
	"github.com/LeJamon/go-xrpl/internal/testing/ticket"
	"github.com/LeJamon/go-xrpl/keylet"
)

func TestAccountDeleteCascadesDIDFromNonRootOwnerPage(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	becky := jtx.NewAccount("becky")
	env.FundAmount(alice, uint64(jtx.XRP(100_000)))
	env.Fund(becky)
	env.Close()

	jtx.RequireTxSuccess(t, env.Submit(ticket.TicketCreate(alice, 32).Build()))
	env.Close()
	jtx.RequireTxSuccess(t, env.Submit(did.DIDSet(alice).URI("uri").Build()))
	env.Close()

	didKey := keylet.DID(alice.ID)
	didData, err := env.LedgerEntry(didKey)
	require.NoError(t, err)
	require.NotNil(t, didData)
	didEntry, err := state.ParseDID(didData)
	require.NoError(t, err)
	require.Equal(t, uint64(1), didEntry.OwnerNode)
	require.True(t, env.LedgerEntryExists(keylet.OwnerDir(alice.ID)))
	require.True(t, env.LedgerEntryExists(keylet.OwnerDirPage(alice.ID, 1)))

	env.IncLedgerSeqForAccDel(alice)
	aliceBefore := env.Balance(alice)
	beckyBefore := env.Balance(becky)
	result := env.Submit(newAccountDelete(alice, becky))
	jtx.RequireTxSuccess(t, result)

	delivered := aliceBefore - acctDelFee
	require.Equal(t, beckyBefore+delivered, env.Balance(becky))
	require.NotNil(t, result.Metadata)
	require.NotNil(t, result.Metadata.DeliveredAmount)
	require.Equal(t, fmt.Sprintf("%d", delivered), result.Metadata.DeliveredAmount.Value())
	require.NotNil(t, metadata.FindNode(result.Metadata, "DeletedNode", "DID"))
	require.NotNil(t, metadata.FindNode(result.Metadata, "DeletedNode", "AccountRoot"))

	jtx.RequireAccountNotExists(t, env, alice)
	require.False(t, env.LedgerEntryExists(didKey))
	require.False(t, env.LedgerEntryExists(keylet.OwnerDir(alice.ID)))
	require.False(t, env.LedgerEntryExists(keylet.OwnerDirPage(alice.ID, 1)))
	env.Close()
	jtx.RequireAccountNotExists(t, env, alice)
	require.False(t, env.LedgerEntryExists(didKey))
}
