package offer_test

import (
	"encoding/hex"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	offerbuild "github.com/LeJamon/go-xrpl/internal/testing/offer"
	"github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

// An auto-bridged offer that crosses the account's own offer moves BBB from the
// account through the issuer and back. The holder's line reaches its default
// state mid-flow and is deleted, then re-created by the incoming leg. The
// re-created line is a new object: it is threaded to the transaction but its
// ModifiedNode carries no previous transaction pointer.
func TestOfferCreate_SelfCrossRecreatedTrustLineHasNoPreviousTxn(t *testing.T) {
	env := jtx.NewTestEnv(t)
	env.EnableOpenLedgerReplay()

	gwA := jtx.NewAccount("gwA")
	gwB := jtx.NewAccount("gwB")
	alice := jtx.NewAccount("alice")
	env.FundAmount(gwA, uint64(jtx.XRP(1000)))
	env.FundAmount(gwB, uint64(jtx.XRP(1000)))
	env.FundAmount(alice, uint64(jtx.XRP(1000)))
	env.Close()

	env.Trust(alice, jtx.IssuedCurrency(gwA, "AAA", 100))
	env.Trust(alice, jtx.IssuedCurrency(gwB, "BBB", 100))
	env.Close()
	jtx.RequireTxSuccess(t, env.Submit(payment.PayIssued(gwA, alice, jtx.IssuedCurrency(gwA, "AAA", 5)).Build()))
	jtx.RequireTxSuccess(t, env.Submit(payment.PayIssued(gwB, alice, jtx.IssuedCurrency(gwB, "BBB", 5)).Build()))
	env.Close()
	env.Trust(alice, jtx.IssuedCurrency(gwB, "BBB", 0))
	env.Close()

	jtx.RequireTxSuccess(t, env.Submit(offerbuild.OfferCreate(gwA, jtx.IssuedCurrency(gwA, "AAA", 10), jtx.XRPTxAmountFromXRP(10)).Build()))
	aliceOfferSeq := env.Seq(alice)
	jtx.RequireTxSuccess(t, env.Submit(offerbuild.OfferCreate(alice, jtx.XRPTxAmountFromXRP(5), jtx.IssuedCurrency(gwB, "BBB", 5)).Build()))
	env.Close()

	lineKL := keylet.Line(alice.ID, gwB.ID, "BBB")
	beforeData, err := env.LedgerEntry(lineKL)
	require.NoError(t, err)
	before, err := state.ParseRippleState(beforeData)
	require.NoError(t, err)
	require.NotZero(t, before.PreviousTxnID, "premise: the line carries a previous transaction pointer")

	env.SetOpenLedger(false)
	cross := env.Submit(offerbuild.OfferCreate(alice, jtx.IssuedCurrency(gwB, "BBB", 10), jtx.IssuedCurrency(gwA, "AAA", 10)).Build())
	jtx.RequireTxSuccess(t, cross)
	require.Nil(t, offerbuild.GetOffer(env, alice, aliceOfferSeq), "premise: alice's own offer is consumed")
	jtx.RequireIOUBalance(t, env, alice, gwB, "BBB", 5)

	afterData, err := env.LedgerEntry(lineKL)
	require.NoError(t, err)
	after, err := state.ParseRippleState(afterData)
	require.NoError(t, err)
	require.NotEqual(t, before.PreviousTxnID, after.PreviousTxnID, "re-created line is threaded to the crossing")

	require.NotNil(t, cross.Metadata)
	lineIndex := strings.ToUpper(hex.EncodeToString(lineKL.Key[:]))
	found := false
	for _, n := range cross.Metadata.AffectedNodes {
		if n.LedgerIndex != lineIndex {
			continue
		}
		found = true
		require.Equal(t, "ModifiedNode", n.NodeType)
		require.Empty(t, n.PreviousTxnID)
		require.Zero(t, n.PreviousTxnLgrSeq)
		require.Empty(t, n.PreviousFields)
	}
	require.True(t, found, "re-created trust line must appear in metadata")
}
