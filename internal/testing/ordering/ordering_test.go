// Package ordering_test contains transaction ordering tests ported from rippled.
// Reference: rippled/src/test/app/Transaction_ordering_test.cpp
package ordering_test

import (
	"strconv"
	"testing"

	jtx "github.com/LeJamon/goXRPLd/internal/testing"
	"github.com/LeJamon/goXRPLd/internal/tx/account"
	"github.com/LeJamon/goXRPLd/internal/txq"
	"github.com/stretchr/testify/require"
)

// TestOrdering_CorrectOrder tests that transactions submitted in sequence order
// are applied correctly.
// Reference: rippled Transaction_ordering_test.cpp testCorrectOrder()
func TestOrdering_CorrectOrder(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	env.Fund(alice)
	env.Close()

	aliceSeq := env.Seq(alice)

	// Submit tx1 at sequence N
	env.Noop(alice)
	env.Close()
	require.Equal(t, aliceSeq+1, env.Seq(alice))

	// Submit tx2 at sequence N+1
	env.Noop(alice)
	env.Close()
	require.Equal(t, aliceSeq+2, env.Seq(alice))
}

// TestOrdering_IncorrectOrder tests that submitting transactions out of order
// still results in correct application after the transaction queue processes them.
// Reference: rippled Transaction_ordering_test.cpp testIncorrectOrder()
//
// When a transaction has a future sequence, it gets terPRE_SEQ and is held.
// When the gap-filling transaction succeeds, held transactions are retried.
func TestOrdering_IncorrectOrder(t *testing.T) {
	// Create env with TxQ enabled using standalone config.
	// The standalone config has a high MinimumTxnInLedgerStandalone (1000)
	// so all transactions can go directly into the open ledger without fee
	// escalation - the TxQ only holds due to sequence gaps (terPRE_SEQ).
	cfg := txq.StandaloneConfig()
	env := jtx.NewTestEnvWithTxQ(t, cfg)
	alice := jtx.NewAccount("alice")
	env.Fund(alice)
	env.Close()

	aliceSeq := env.Seq(alice)
	fee := strconv.FormatUint(env.BaseFee(), 10)

	// Build tx1 at sequence N (current sequence)
	tx1 := account.NewAccountSet(alice.Address)
	tx1.Fee = fee
	seq1 := aliceSeq
	tx1.Sequence = &seq1

	// Build tx2 at sequence N+1 (future sequence)
	tx2 := account.NewAccountSet(alice.Address)
	tx2.Fee = fee
	seq2 := aliceSeq + 1
	tx2.Sequence = &seq2
	lastLedger := uint32(7)
	tx2.LastLedgerSequence = &lastLedger

	// Submit tx2 first (out of order) - should get terPRE_SEQ (held, not TxQ-queued)
	// Reference: rippled env(tx2, ter(terPRE_SEQ))
	result2 := env.Submit(tx2)
	jtx.RequireTxFail(t, result2, "terPRE_SEQ")
	require.Equal(t, aliceSeq, env.Seq(alice),
		"sequence should not advance for held tx")

	// Submit tx1 (fills the gap) - both should be applied after rendezvous
	// Reference: rippled env(tx1); env.app().getJobQueue().rendezvous();
	result1 := env.Submit(tx1)
	jtx.RequireTxSuccess(t, result1)

	// After tx1 fills the gap, retryHeldTransactions should automatically apply tx2.
	require.Equal(t, aliceSeq+2, env.Seq(alice),
		"both transactions should be applied after gap fill")

	env.Close()
}

// TestOrdering_IncorrectOrderMultipleIntermediaries tests that submitting
// multiple future-sequence transactions results in correct application
// once the first transaction fills the gap.
// Reference: rippled Transaction_ordering_test.cpp testIncorrectOrderMultipleIntermediaries()
func TestOrdering_IncorrectOrderMultipleIntermediaries(t *testing.T) {
	cfg := txq.StandaloneConfig()
	env := jtx.NewTestEnvWithTxQ(t, cfg)
	alice := jtx.NewAccount("alice")
	env.Fund(alice)
	env.Close()

	aliceSeq := env.Seq(alice)
	fee := strconv.FormatUint(env.BaseFee(), 10)

	// Build 5 transactions at sequences N, N+1, N+2, N+3, N+4
	txns := make([]*account.AccountSet, 5)
	for i := 0; i < 5; i++ {
		txns[i] = account.NewAccountSet(alice.Address)
		txns[i].Fee = fee
		seq := aliceSeq + uint32(i)
		txns[i].Sequence = &seq
		lastLedger := uint32(7)
		txns[i].LastLedgerSequence = &lastLedger
	}

	// Submit tx[1] through tx[4] first (out of order) - all should get terPRE_SEQ
	for i := 1; i < 5; i++ {
		result := env.Submit(txns[i])
		jtx.RequireTxFail(t, result, "terPRE_SEQ")
		require.Equal(t, aliceSeq, env.Seq(alice),
			"sequence should not advance for held txs")
	}

	// Submit tx[0] (fills the gap) - all 5 should be applied
	result0 := env.Submit(txns[0])
	jtx.RequireTxSuccess(t, result0)

	// After tx[0] fills the gap, retryHeldTransactions should apply tx[1..4].
	require.Equal(t, aliceSeq+5, env.Seq(alice),
		"all 5 transactions should be applied after gap fill")

	env.Close()
}
