package clawback_test

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/accountset"
	"github.com/LeJamon/go-xrpl/internal/testing/clawback"
	"github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/internal/testing/trustset"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

func TestClawback_DeleteDefaultLineRollbackOnCorruptDirectory(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	env.Fund(alice, bob)
	env.Close()

	jtx.RequireTxSuccess(t, env.Submit(accountset.AccountSet(alice).AllowClawback().Build()))
	env.Trust(bob, tx.NewIssuedAmountFromFloat64(1000, "USD", alice.Address))
	jtx.RequireTxSuccess(t, env.Submit(payment.PayIssued(alice, bob, tx.NewIssuedAmountFromFloat64(1000, "USD", alice.Address)).Build()))
	jtx.RequireTxSuccess(t, env.Submit(trustset.TrustSet(bob, tx.NewIssuedAmountFromFloat64(0, "USD", alice.Address)).Build()))
	env.Close()

	lineKey := keylet.Line(alice.ID, bob.ID, "USD")
	dirKey := keylet.OwnerDir(bob.ID)
	dirBytes, err := env.LedgerEntry(dirKey)
	require.NoError(t, err)
	dir, err := state.ParseDirectoryNode(dirBytes)
	require.NoError(t, err)
	dir.Indexes = nil
	dirBytes, err = state.SerializeDirectoryNode(dir, false)
	require.NoError(t, err)
	require.NoError(t, env.Ledger().Update(dirKey, dirBytes))

	lineBefore, err := env.LedgerEntry(lineKey)
	require.NoError(t, err)
	dirBefore, err := env.LedgerEntry(dirKey)
	require.NoError(t, err)
	aliceBefore, err := env.LedgerEntry(keylet.Account(alice.ID))
	require.NoError(t, err)
	bobBefore, err := env.LedgerEntry(keylet.Account(bob.ID))
	require.NoError(t, err)
	aliceSequence := env.Seq(alice)
	aliceBalance := env.Balance(alice)
	bobBalance := env.Balance(bob)

	result := env.Submit(clawback.Claw(alice, bob, "USD", 1000).Build())
	jtx.RequireTxFail(t, result, jtx.TefBAD_LEDGER)
	require.Nil(t, result.Metadata)
	require.Equal(t, aliceSequence, env.Seq(alice))
	require.Equal(t, aliceBalance, env.Balance(alice))
	require.Equal(t, bobBalance, env.Balance(bob))
	jtx.RequireIOUBalance(t, env, bob, alice, "USD", 1000)
	jtx.RequireIOUBalance(t, env, alice, bob, "USD", -1000)

	lineAfter, err := env.LedgerEntry(lineKey)
	require.NoError(t, err)
	dirAfter, err := env.LedgerEntry(dirKey)
	require.NoError(t, err)
	aliceAfter, err := env.LedgerEntry(keylet.Account(alice.ID))
	require.NoError(t, err)
	bobAfter, err := env.LedgerEntry(keylet.Account(bob.ID))
	require.NoError(t, err)
	require.Equal(t, lineBefore, lineAfter)
	require.Equal(t, dirBefore, dirAfter)
	require.Equal(t, aliceBefore, aliceAfter)
	require.Equal(t, bobBefore, bobAfter)
}

func TestClawback_Tickets(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	env.Fund(alice, bob)
	env.Close()

	result := env.Submit(accountset.AccountSet(alice).AllowClawback().Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()
	jtx.RequireFlagSet(t, env, alice, state.LsfAllowTrustLineClawback)

	env.Trust(bob, tx.NewIssuedAmountFromFloat64(1000, "USD", alice.Address))
	result = env.Submit(payment.PayIssued(alice, bob, tx.NewIssuedAmountFromFloat64(100, "USD", alice.Address)).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()
	jtx.RequireIOUBalance(t, env, bob, alice, "USD", 100)
	jtx.RequireIOUBalance(t, env, alice, bob, "USD", -100)

	ticketCount := uint32(10)
	aliceTicketSeq := env.CreateTickets(alice, ticketCount)
	env.Close()
	aliceSeq := env.Seq(alice)
	jtx.RequireOwnerCount(t, env, alice, ticketCount)

	remaining := ticketCount
	for remaining > 0 {
		clawTx := jtx.WithTicketSeq(clawback.Claw(alice, bob, "USD", 5).Build(), aliceTicketSeq)
		result = env.Submit(clawTx)
		jtx.RequireTxSuccess(t, result)
		env.Close()
		aliceTicketSeq++
		remaining--
		jtx.RequireOwnerCount(t, env, alice, remaining)
	}

	jtx.RequireIOUBalance(t, env, bob, alice, "USD", 50)
	jtx.RequireIOUBalance(t, env, alice, bob, "USD", -50)
	require.Equal(t, aliceSeq, env.Seq(alice))
	jtx.RequireTicketCount(t, env, alice, 0)

	consumed := jtx.WithTicketSeq(clawback.Claw(alice, bob, "USD", 5).Build(), aliceTicketSeq-ticketCount)
	jtx.RequireTxFail(t, env.Submit(consumed), "tefNO_TICKET")
	future := jtx.WithTicketSeq(clawback.Claw(alice, bob, "USD", 5).Build(), aliceSeq)
	jtx.RequireTxFail(t, env.Submit(future), "terPRE_TICKET")
	jtx.RequireIOUBalance(t, env, bob, alice, "USD", 50)
	jtx.RequireIOUBalance(t, env, alice, bob, "USD", -50)
	jtx.RequireOwnerCount(t, env, alice, 0)
	jtx.RequireTicketCount(t, env, alice, 0)
	require.Equal(t, aliceSeq, env.Seq(alice))
}
