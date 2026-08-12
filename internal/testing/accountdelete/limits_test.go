package accountdelete_test

import (
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	offerbuild "github.com/LeJamon/go-xrpl/internal/testing/offer"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

func TestAccountDelete_DeletableEntryLimitAndDirectoryPages(t *testing.T) {
	env := jtx.NewTestEnvBacked(t)
	alice := jtx.NewAccount("alice")
	gw := jtx.NewAccount("gw")
	env.FundAmount(alice, uint64(jtx.XRP(10_000_000)))
	env.Fund(gw)
	env.Close()

	const offerCount = 1001
	offerKeys := make([]keylet.Keylet, 0, offerCount)
	firstOfferSequence := env.Seq(alice)
	for range offerCount {
		offerKey := keylet.Offer(alice.ID, env.Seq(alice))
		jtx.RequireTxSuccess(t, env.Submit(
			offerbuild.OfferCreate(alice, gw.IOU("USD", 1), jtx.XRPTxAmount(jtx.XRP(1))).Build()))
		offerKeys = append(offerKeys, offerKey)
	}
	env.Close()
	require.Equal(t, uint32(offerCount), env.OwnerCount(alice))
	for page := uint64(0); page <= offerCount/32; page++ {
		jtx.RequireLedgerEntryExists(t, env, keylet.OwnerDirPage(alice.ID, page))
	}

	env.IncLedgerSeqForAccDel(alice)
	stateBefore, err := env.Ledger().StateMapHash()
	require.NoError(t, err)
	sequenceBefore := env.Seq(alice)
	balanceBefore := env.Balance(alice)
	result := env.Submit(newAccountDelete(env, alice, gw))
	jtx.RequireTxFail(t, result, jtx.TefTOO_BIG)
	require.False(t, result.Applied)
	require.Zero(t, result.Fee)
	require.Nil(t, result.Metadata)
	stateAfter, err := env.Ledger().StateMapHash()
	require.NoError(t, err)
	require.Equal(t, stateBefore, stateAfter)
	require.Equal(t, sequenceBefore, env.Seq(alice))
	require.Equal(t, balanceBefore, env.Balance(alice))

	jtx.RequireTxSuccess(t, env.Submit(offerbuild.OfferCancel(alice, firstOfferSequence).Build()))
	env.Close()
	require.Equal(t, uint32(offerCount-1), env.OwnerCount(alice))
	env.IncLedgerSeqForAccDel(alice)
	before := captureDeleteBalances(env, alice, gw)
	result = env.Submit(newAccountDelete(env, alice, gw))
	requireAccountDeleteSuccess(t, env, result, alice, gw, before, offerKeys...)
	for page := uint64(0); page <= offerCount/32; page++ {
		jtx.RequireLedgerEntryNotExists(t, env, keylet.OwnerDirPage(alice.ID, page))
	}
}
