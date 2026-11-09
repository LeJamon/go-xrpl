package accountdelete_test

import (
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	offerbuild "github.com/LeJamon/go-xrpl/internal/testing/offer"
	"github.com/stretchr/testify/require"
)

// The ticket an AccountDelete consumes still counts toward the deletable
// owner-directory entries.
func TestAccountDelete_TicketCountsTowardDeletableEntryLimit(t *testing.T) {
	env := jtx.NewTestEnvBacked(t)
	alice := jtx.NewAccount("alice")
	gw := jtx.NewAccount("gw")
	env.FundAmount(alice, uint64(jtx.XRP(10_000_000)))
	env.Fund(gw)
	env.Close()

	ticket := env.CreateTickets(alice, 1)
	const offerCount = 1000
	for range offerCount {
		jtx.RequireTxSuccess(t, env.Submit(
			offerbuild.OfferCreate(alice, gw.IOU("USD", 1), jtx.XRPTxAmount(jtx.XRP(1))).Build()))
	}
	env.Close()
	require.Equal(t, uint32(offerCount+1), env.OwnerCount(alice))

	env.IncLedgerSeqForAccDel(alice)
	sequenceBefore := env.Seq(alice)
	balanceBefore := env.Balance(alice)
	result := env.Submit(jtx.WithTicketSeq(newAccountDelete(env, alice, gw), ticket))
	jtx.RequireTxFail(t, result, jtx.TefTOO_BIG)
	require.False(t, result.Applied)
	require.Equal(t, sequenceBefore, env.Seq(alice))
	require.Equal(t, balanceBefore, env.Balance(alice))
	require.Equal(t, uint32(offerCount+1), env.OwnerCount(alice))
}
