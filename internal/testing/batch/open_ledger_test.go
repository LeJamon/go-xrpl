package batch

import (
	"fmt"
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/tx"
	accounttx "github.com/LeJamon/go-xrpl/internal/tx/account"
	batchtx "github.com/LeJamon/go-xrpl/internal/tx/batch"
	"github.com/LeJamon/go-xrpl/internal/tx/check"
	"github.com/LeJamon/go-xrpl/internal/tx/payment"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/internal/txq"
	"github.com/stretchr/testify/require"
)

func TestTickets(t *testing.T) {
	t.Run("tickets outer", func(t *testing.T) {
		// Reference: rippled Batch_test.cpp testTickets() - "tickets outer"
		env := newBatchEnv(t)

		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		env.FundAmount(alice, uint64(jtx.XRP(10000)))
		env.FundAmount(bob, uint64(jtx.XRP(10000)))
		env.Close()

		aliceTicketSeq := env.CreateTickets(alice, 10)
		env.Close()

		aliceSeq := env.Seq(alice)
		preAlice := env.Balance(alice)
		preBob := env.Balance(bob)

		batchFee := CalcBatchFeeFromEnv(env, 0, 2)
		batch := NewBatchBuilderWithTicket(alice, aliceTicketSeq, batchFee, batchtx.BatchFlagAllOrNothing).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 1, aliceSeq+0)).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 2, aliceSeq+1)).
			MustBuild()

		result := env.Submit(batch)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		require.Equal(t, uint32(9), env.OwnerCount(alice), "alice should have 9 owner objects")
		require.Equal(t, uint32(9), env.TicketCount(alice), "alice should have 9 tickets remaining")

		jtx.RequireSequence(t, env, alice, aliceSeq+2)

		jtx.RequireBalance(t, env, alice, preAlice-uint64(jtx.XRP(3))-batchFee)
		jtx.RequireBalance(t, env, bob, preBob+uint64(jtx.XRP(3)))
	})

	t.Run("tickets inner", func(t *testing.T) {
		// Reference: rippled Batch_test.cpp testTickets() - "tickets inner"
		env := newBatchEnv(t)

		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		env.FundAmount(alice, uint64(jtx.XRP(10000)))
		env.FundAmount(bob, uint64(jtx.XRP(10000)))
		env.Close()

		aliceTicketSeq := env.CreateTickets(alice, 10)
		env.Close()

		aliceSeq := env.Seq(alice)
		preAlice := env.Balance(alice)
		preBob := env.Balance(bob)

		batchFee := CalcBatchFeeFromEnv(env, 0, 2)
		batch := NewBatchBuilder(alice, aliceSeq, batchFee, batchtx.BatchFlagAllOrNothing).
			AddInnerTx(MakeInnerPaymentXRPWithTicket(alice, bob, 1, aliceTicketSeq)).
			AddInnerTx(MakeInnerPaymentXRPWithTicket(alice, bob, 2, aliceTicketSeq+1)).
			MustBuild()

		result := env.Submit(batch)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		require.Equal(t, uint32(8), env.OwnerCount(alice), "alice should have 8 owner objects")
		require.Equal(t, uint32(8), env.TicketCount(alice), "alice should have 8 tickets remaining")

		jtx.RequireSequence(t, env, alice, aliceSeq+1)

		jtx.RequireBalance(t, env, alice, preAlice-uint64(jtx.XRP(3))-batchFee)
		jtx.RequireBalance(t, env, bob, preBob+uint64(jtx.XRP(3)))
	})

	t.Run("tickets outer inner", func(t *testing.T) {
		// Reference: rippled Batch_test.cpp testTickets() - "tickets outer inner"
		env := newBatchEnv(t)

		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		env.FundAmount(alice, uint64(jtx.XRP(10000)))
		env.FundAmount(bob, uint64(jtx.XRP(10000)))
		env.Close()

		aliceTicketSeq := env.CreateTickets(alice, 10)
		env.Close()

		aliceSeq := env.Seq(alice)
		preAlice := env.Balance(alice)
		preBob := env.Balance(bob)

		batchFee := CalcBatchFeeFromEnv(env, 0, 2)
		batch := NewBatchBuilderWithTicket(alice, aliceTicketSeq, batchFee, batchtx.BatchFlagAllOrNothing).
			AddInnerTx(MakeInnerPaymentXRPWithTicket(alice, bob, 1, aliceTicketSeq+1)).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 2, aliceSeq)).
			MustBuild()

		result := env.Submit(batch)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		require.Equal(t, uint32(8), env.OwnerCount(alice), "alice should have 8 owner objects")
		require.Equal(t, uint32(8), env.TicketCount(alice), "alice should have 8 tickets remaining")

		jtx.RequireSequence(t, env, alice, aliceSeq+1)

		jtx.RequireBalance(t, env, alice, preAlice-uint64(jtx.XRP(3))-batchFee)
		jtx.RequireBalance(t, env, bob, preBob+uint64(jtx.XRP(3)))
	})
}

func TestTicketsOpenLedger(t *testing.T) {
	// Reference: rippled Batch_test.cpp testTicketsOpenLedger()

	t.Run("before batch txn with same ticket", func(t *testing.T) {
		// Reference: rippled testTicketsOpenLedger() "Before Batch Txn w/ same ticket"
		// The batch is applied first (canonical order), consuming the ticket
		// used by the inner tx. The noop that also uses that ticket is
		// overwritten.
		env := newBatchEnv(t)
		env.EnableOpenLedgerReplay()

		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		env.Fund(alice, bob)
		env.Close()

		aliceTicketSeq := env.CreateTickets(alice, 10)
		env.Close()

		aliceSeq := env.Seq(alice)

		noopTxn := accounttx.NewAccountSet(alice.Address)
		noopTxn.Fee = fmt.Sprintf("%d", env.BaseFee())
		noopTxn.SigningPubKey = alice.PublicKeyHex()
		zero := uint32(0)
		noopTxn.Sequence = &zero
		ticketSeq1 := aliceTicketSeq + 1
		noopTxn.TicketSequence = &ticketSeq1
		result := env.Submit(noopTxn)
		jtx.RequireTxSuccess(t, result)

		batchFee := CalcBatchFeeFromEnv(env, 0, 2)
		batch := NewBatchBuilderWithTicket(alice, aliceTicketSeq, batchFee, batchtx.BatchFlagAllOrNothing).
			AddInnerTx(MakeInnerPaymentXRPWithTicket(alice, bob, 1, aliceTicketSeq+1)).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 2, aliceSeq)).
			MustBuild()
		batchResult := env.Submit(batch)
		jtx.RequireTxSuccess(t, batchResult)
		env.Close()
		requireBatchLedgerData(t, env, batch, batchResult, ter.TesSUCCESS, ter.TesSUCCESS)

		env.Close()
		closed := env.LastClosedLedger()
		require.Zero(t, closed.TxCount())

		require.Equal(t, aliceSeq+1, env.Seq(alice))
	})

	t.Run("after batch txn with same ticket", func(t *testing.T) {
		// Reference: rippled testTicketsOpenLedger() "After Batch Txn w/ same ticket"
		env := newBatchEnv(t)
		env.EnableOpenLedgerReplay()

		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		env.Fund(alice, bob)
		env.Close()

		aliceTicketSeq := env.CreateTickets(alice, 10)
		env.Close()

		aliceSeq := env.Seq(alice)

		batchFee := CalcBatchFeeFromEnv(env, 0, 2)
		batch := NewBatchBuilderWithTicket(alice, aliceTicketSeq, batchFee, batchtx.BatchFlagAllOrNothing).
			AddInnerTx(MakeInnerPaymentXRPWithTicket(alice, bob, 1, aliceTicketSeq+1)).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 2, aliceSeq)).
			MustBuild()
		batchResult := env.Submit(batch)
		jtx.RequireTxSuccess(t, batchResult)

		noopTxn := accounttx.NewAccountSet(alice.Address)
		noopTxn.Fee = fmt.Sprintf("%d", env.BaseFee())
		noopTxn.SigningPubKey = alice.PublicKeyHex()
		zero := uint32(0)
		noopTxn.Sequence = &zero
		ticketSeq1 := aliceTicketSeq + 1
		noopTxn.TicketSequence = &ticketSeq1
		noopResult := env.Submit(noopTxn)
		jtx.RequireTxSuccess(t, noopResult)
		env.Close()
		requireBatchLedgerData(t, env, batch, batchResult, ter.TesSUCCESS, ter.TesSUCCESS)

		env.Close()
		require.Zero(t, env.LastClosedLedger().TxCount())

		require.Equal(t, aliceSeq+1, env.Seq(alice))
	})
}

func TestBatchTxQueue(t *testing.T) {
	t.Run("outer batch txns count towards queue size", func(t *testing.T) {
		// Reference: rippled Batch_test.cpp testBatchTxQueue() first sub-test
		// "only outer batch transactions are counter towards the queue size"
		cfg := makeSmallQueueConfig(2)
		env := jtx.NewTestEnvWithTxQ(t, cfg)
		env.EnableFeatureNow("Batch")

		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		carol := jtx.NewAccount("carol")

		// Fund across several ledgers so the TxQ metrics stay restricted.
		// noripple funds do not enable DefaultRipple (1 tx per account instead of 2).
		env.FundAmountNoRipple(alice, uint64(jtx.XRP(10000)))
		env.FundAmountNoRipple(bob, uint64(jtx.XRP(10000)))
		env.Close()
		env.FundAmountNoRipple(carol, uint64(jtx.XRP(10000)))
		env.Close()

		env.Noop(alice)
		env.Noop(alice)
		env.Noop(alice)
		checkMetrics(t, env, 0, nil, 3, 2)

		result := env.Submit(makeNoopWithFee(carol, env.BaseFee()))
		jtx.RequireTxFail(t, result, "terQUEUED")
		checkMetrics(t, env, 1, nil, 3, 2)

		aliceSeq := env.Seq(alice)
		bobSeq := env.Seq(bob)
		batchFee := CalcBatchFeeFromEnv(env, 1, 2)

		batch := NewBatchBuilder(alice, aliceSeq, batchFee, batchtx.BatchFlagAllOrNothing).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 10, aliceSeq+1)).
			AddInnerTx(MakeInnerPaymentXRP(bob, alice, 5, bobSeq)).
			AddSigner(bob).
			MustBuild()
		result = env.Submit(batch)
		jtx.RequireTxFail(t, result, "terQUEUED")
		checkMetrics(t, env, 2, nil, 3, 2)

		olFee := env.OpenLedgerFee(batchFee)
		batch2 := NewBatchBuilder(alice, aliceSeq, olFee, batchtx.BatchFlagAllOrNothing).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 10, aliceSeq+1)).
			AddInnerTx(MakeInnerPaymentXRP(bob, alice, 5, bobSeq)).
			AddSigner(bob).
			MustBuild()
		result = env.Submit(batch2)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// After close: queue drained (carol's noop applied in new ledger),
		// maxSize = txnsExpected * ledgersInQueue.
		// Closed ledger had: 3 noops + 1 batch outer + 2 inner = 6 txns.
		// With NormalConsensusIncreasePercent=0, txnsExpected = 6.
		// maxSize = 6 * 2 = 12. txInLedger = 1 (carol's noop from queue).
		maxSize := uint64(12)
		checkMetrics(t, env, 0, &maxSize, 1, 6)
		closed := env.LastClosedLedger()
		require.Equal(t, uint32(6), closed.TxCount())
		root, err := closed.TxMapHash()
		require.NoError(t, err)
		require.NotEqual(t, [32]byte{}, root)
		require.Equal(t, root, closed.Header().TxHash)
	})

	t.Run("inner batch txns count towards ledger tx count", func(t *testing.T) {
		// Reference: rippled Batch_test.cpp testBatchTxQueue() second sub-test
		// "inner batch transactions are counter towards the ledger tx count"
		cfg := makeSmallQueueConfig(2)
		env := jtx.NewTestEnvWithTxQ(t, cfg)
		env.EnableFeatureNow("Batch")

		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		carol := jtx.NewAccount("carol")

		// Fund across several ledgers so the TxQ metrics stay restricted.
		env.FundAmountNoRipple(alice, uint64(jtx.XRP(10000)))
		env.FundAmountNoRipple(bob, uint64(jtx.XRP(10000)))
		env.Close()
		env.FundAmountNoRipple(carol, uint64(jtx.XRP(10000)))
		env.Close()

		env.Noop(alice)
		env.Noop(alice)
		checkMetrics(t, env, 0, nil, 2, 2)

		aliceSeq := env.Seq(alice)
		bobSeq := env.Seq(bob)
		batchFee := CalcBatchFeeFromEnv(env, 1, 2)

		batch := NewBatchBuilder(alice, aliceSeq, batchFee, batchtx.BatchFlagAllOrNothing).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 10, aliceSeq+1)).
			AddInnerTx(MakeInnerPaymentXRP(bob, alice, 5, bobSeq)).
			AddSigner(bob).
			MustBuild()
		result := env.Submit(batch)
		jtx.RequireTxSuccess(t, result)
		checkMetrics(t, env, 0, nil, 3, 2)

		result = env.Submit(makeNoopWithFee(carol, env.BaseFee()))
		jtx.RequireTxFail(t, result, "terQUEUED")
		checkMetrics(t, env, 1, nil, 3, 2)
	})
}

// makeSmallQueueConfig creates a TxQ config matching rippled's Batch_test
// makeSmallQueueConfig({{"minimum_txn_in_ledger_standalone", minTxn}}).
// Reference: rippled Batch_test.cpp makeSmallQueueConfig()
func makeSmallQueueConfig(minTxnStandalone uint32) txq.Config {
	return txq.Config{
		LedgersInQueue:                 2,
		QueueSizeMin:                   2,
		RetrySequencePercent:           25,
		MinimumEscalationMultiplier:    txq.BaseLevel * 500,
		MinimumTxnInLedger:             32,
		MinimumTxnInLedgerStandalone:   minTxnStandalone,
		TargetTxnInLedger:              256,
		MaximumTxnInLedger:             0,
		NormalConsensusIncreasePercent: 0,
		SlowConsensusDecreasePercent:   50,
		MaximumTxnPerAccount:           10,
		MinimumLastLedgerBuffer:        2,
		Standalone:                     true,
	}
}

// makeNoopWithFee creates an AccountSet noop with a specific fee.
// Unlike env.Noop() which auto-fills and submits, this returns the raw tx.
func makeNoopWithFee(acc *jtx.Account, fee uint64) *accounttx.AccountSet {
	as := accounttx.NewAccountSet(acc.Address)
	as.Fee = fmt.Sprintf("%d", fee)
	return as
}

// checkMetrics asserts TxQ metrics match expected values.
// maxSize nil means skip that assertion (matches rippled's std::nullopt).
// Reference: rippled test/jtx/TestHelpers.h checkMetrics()
func checkMetrics(t *testing.T, env *jtx.TestEnv, expectedQueueSize uint64, expectedMaxSize *uint64, expectedTxInLedger uint64, expectedTxPerLedger uint64) {
	t.Helper()
	metrics := env.TxQMetrics()

	require.Equal(t, expectedQueueSize, metrics.TxCount,
		"checkMetrics: txCount (queue size) mismatch")

	if expectedMaxSize != nil {
		require.NotNil(t, metrics.TxQMaxSize, "checkMetrics: maxSize should not be nil")
		require.Equal(t, *expectedMaxSize, *metrics.TxQMaxSize,
			"checkMetrics: txQMaxSize mismatch")
	}

	require.Equal(t, expectedTxInLedger, metrics.TxInLedger,
		"checkMetrics: txInLedger mismatch")

	require.Equal(t, expectedTxPerLedger, metrics.TxPerLedger,
		"checkMetrics: txPerLedger mismatch")
}

func TestSequenceOpenLedger(t *testing.T) {
	// Reference: rippled Batch_test.cpp testSequenceOpenLedger()
	// Tests interactions between batch inner transactions advancing sequences
	// and standalone transactions with future sequences or the same sequences.

	t.Run("before batch txn with retry following ledger", func(t *testing.T) {
		// Reference: rippled testSequenceOpenLedger() "Before Batch Txn w/ retry following ledger"
		// A noop at aliceSeq+2 gets terPRE_SEQ. Then a batch with carol as
		// outer submitter and alice as inner signer advances alice's seq.
		// After close: batch succeeds, noop retried in next ledger.
		env := newBatchEnv(t)
		env.EnableOpenLedgerReplay()

		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		carol := jtx.NewAccount("carol")
		env.Fund(alice, bob, carol)
		env.Close()

		aliceSeq := env.Seq(alice)
		carolSeq := env.Seq(carol)
		preAlice := env.Balance(alice)
		preBob := env.Balance(bob)

		noopTxn := accounttx.NewAccountSet(alice.Address)
		noopTxn.Fee = fmt.Sprintf("%d", env.BaseFee())
		noopTxn.SigningPubKey = alice.PublicKeyHex()
		futureSeq := aliceSeq + 2
		noopTxn.Sequence = &futureSeq
		result := env.Submit(noopTxn)
		jtx.RequireTxFail(t, result, "terPRE_SEQ")

		batchFee := CalcBatchFeeFromEnv(env, 1, 2)
		batch := NewBatchBuilder(carol, carolSeq, batchFee, batchtx.BatchFlagAllOrNothing).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 1, aliceSeq)).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 2, aliceSeq+1)).
			AddSigner(alice).
			MustBuild()
		result = env.Submit(batch)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		requireBatchLedgerData(t, env, batch, result, ter.TesSUCCESS, ter.TesSUCCESS)

		env.Close()
		closed := env.LastClosedLedger()
		require.Equal(t, uint32(1), closed.TxCount())
		noopID, err := tx.ComputeTransactionHash(noopTxn)
		require.NoError(t, err)
		noopExists, err := closed.TxExists(noopID)
		require.NoError(t, err)
		require.True(t, noopExists)

		require.Equal(t, aliceSeq+3, env.Seq(alice),
			"alice seq should have advanced by 3 (2 inner payments + 1 noop)")

		require.Equal(t, carolSeq+1, env.Seq(carol),
			"carol seq should have advanced by 1 (batch outer)")

		jtx.RequireBalance(t, env, alice, preAlice-uint64(jtx.XRP(3))-env.BaseFee())
		jtx.RequireBalance(t, env, bob, preBob+uint64(jtx.XRP(3)))
	})

	t.Run("before batch txn with same sequence", func(t *testing.T) {
		// Reference: rippled testSequenceOpenLedger() "Before Batch Txn w/ same sequence"
		// A noop at aliceSeq+1 gets terPRE_SEQ. Then a batch with alice as
		// outer submitter has inner payments consuming aliceSeq+1 and aliceSeq+2.
		// After close: batch wins (canonical order), noop overwritten.
		env := newBatchEnv(t)
		env.EnableOpenLedgerReplay()

		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		env.Fund(alice, bob)
		env.Close()

		aliceSeq := env.Seq(alice)
		preAlice := env.Balance(alice)
		preBob := env.Balance(bob)

		noopTxn := accounttx.NewAccountSet(alice.Address)
		noopTxn.Fee = fmt.Sprintf("%d", env.BaseFee())
		noopTxn.SigningPubKey = alice.PublicKeyHex()
		futureSeq := aliceSeq + 1
		noopTxn.Sequence = &futureSeq
		result := env.Submit(noopTxn)
		jtx.RequireTxFail(t, result, "terPRE_SEQ")

		batchFee := CalcBatchFeeFromEnv(env, 0, 2)
		batch := NewBatchBuilder(alice, aliceSeq, batchFee, batchtx.BatchFlagAllOrNothing).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 1, aliceSeq+1)).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 2, aliceSeq+2)).
			MustBuild()
		result = env.Submit(batch)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		env.Close()

		require.Equal(t, aliceSeq+3, env.Seq(alice),
			"alice seq should have advanced by 3 (outer + 2 inner payments)")

		jtx.RequireBalance(t, env, alice, preAlice-uint64(jtx.XRP(3))-batchFee)
		jtx.RequireBalance(t, env, bob, preBob+uint64(jtx.XRP(3)))
	})

	t.Run("after batch txn with same sequence", func(t *testing.T) {
		// Reference: rippled testSequenceOpenLedger() "After Batch Txn w/ same sequence"
		// Batch submitted first, then noop at aliceSeq+1.
		// After close: batch wins (applied first), noop at same seq overwritten.
		env := newBatchEnv(t)
		env.EnableOpenLedgerReplay()

		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		env.Fund(alice, bob)
		env.Close()

		aliceSeq := env.Seq(alice)
		preAlice := env.Balance(alice)
		preBob := env.Balance(bob)

		batchFee := CalcBatchFeeFromEnv(env, 0, 2)
		batch := NewBatchBuilder(alice, aliceSeq, batchFee, batchtx.BatchFlagAllOrNothing).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 1, aliceSeq+1)).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 2, aliceSeq+2)).
			MustBuild()
		result := env.Submit(batch)
		jtx.RequireTxSuccess(t, result)

		noopTxn := accounttx.NewAccountSet(alice.Address)
		noopTxn.Fee = fmt.Sprintf("%d", env.BaseFee())
		noopTxn.SigningPubKey = alice.PublicKeyHex()
		sameSeq := aliceSeq + 1
		noopTxn.Sequence = &sameSeq
		noopResult := env.Submit(noopTxn)
		jtx.RequireTxSuccess(t, noopResult)
		env.Close()

		env.Close()

		require.Equal(t, aliceSeq+3, env.Seq(alice),
			"alice seq should have advanced by 3 (outer + 2 inner payments)")

		jtx.RequireBalance(t, env, alice, preAlice-uint64(jtx.XRP(3))-batchFee)
		jtx.RequireBalance(t, env, bob, preBob+uint64(jtx.XRP(3)))
	})

	t.Run("outer batch terPRE_SEQ", func(t *testing.T) {
		// Reference: rippled testSequenceOpenLedger() "Outer Batch terPRE_SEQ"
		// Batch outer has a future sequence (carolSeq+1) -> terPRE_SEQ.
		// A noop advances carol's seq. After close: batch succeeds.
		env := newBatchEnv(t)
		env.EnableOpenLedgerReplay()

		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		carol := jtx.NewAccount("carol")
		env.Fund(alice, bob, carol)
		env.Close()

		aliceSeq := env.Seq(alice)
		carolSeq := env.Seq(carol)
		preAlice := env.Balance(alice)
		preBob := env.Balance(bob)

		batchFee := CalcBatchFeeFromEnv(env, 1, 2)
		batch := NewBatchBuilder(carol, carolSeq+1, batchFee, batchtx.BatchFlagAllOrNothing).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 1, aliceSeq)).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 2, aliceSeq+1)).
			AddSigner(alice).
			MustBuild()
		result := env.Submit(batch)
		jtx.RequireTxFail(t, result, "terPRE_SEQ")

		noopTxn := accounttx.NewAccountSet(carol.Address)
		noopTxn.Fee = fmt.Sprintf("%d", env.BaseFee())
		noopTxn.SigningPubKey = carol.PublicKeyHex()
		noopTxn.Sequence = &carolSeq
		result = env.Submit(noopTxn)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		env.Close()

		require.Equal(t, aliceSeq+2, env.Seq(alice),
			"alice seq should advance by 2 (inner payments)")

		require.Equal(t, carolSeq+2, env.Seq(carol),
			"carol seq should advance by 2 (noop + batch outer)")

		jtx.RequireBalance(t, env, alice, preAlice-uint64(jtx.XRP(3)))
		jtx.RequireBalance(t, env, bob, preBob+uint64(jtx.XRP(3)))
	})
}

func TestObjectsOpenLedger(t *testing.T) {
	// Reference: rippled Batch_test.cpp testObjectsOpenLedger()
	// Tests interactions between batch inner transactions creating/consuming
	// ledger objects and standalone transactions that depend on those objects.

	t.Run("consume object before batch txn", func(t *testing.T) {
		// Reference: rippled testObjectsOpenLedger() "Consume Object Before Batch Txn"
		// CheckCash submitted before the batch that creates the check.
		// In rippled, CheckCash gets tecNO_ENTRY initially (inner txns deferred).
		// During consensus replay, batch (alice) is applied first in canonical
		// order, creating the check. Then CheckCash (bob) succeeds.
		//
		// The open ledger cannot see the Check created by the Batch inner until
		// consensus replay closes the ledger.
		env := newBatchEnv(t)
		env.EnableOpenLedgerReplay()

		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		env.Fund(alice, bob)
		env.Close()

		aliceTicketSeq := env.CreateTickets(alice, 10)
		env.Close()

		aliceSeq := env.Seq(alice)
		bobSeq := env.Seq(bob)
		preAlice := env.Balance(alice)
		preBob := env.Balance(bob)

		chkID := GetCheckIndex(alice, aliceSeq)
		cashTxn := check.NewCheckCash(bob.Address, chkID)
		cashTxn.SetExactAmount(tx.NewXRPAmount(jtx.XRP(10)))
		cashTxn.Fee = fmt.Sprintf("%d", env.BaseFee())
		cashTxn.SigningPubKey = bob.PublicKeyHex()
		cashTxn.Sequence = &bobSeq
		cashResult := env.Submit(cashTxn)
		jtx.RequireTxClaimed(t, cashResult, "tecNO_ENTRY")

		batchFee := CalcBatchFeeFromEnv(env, 0, 2)
		batch := NewBatchBuilderWithTicket(alice, aliceTicketSeq, batchFee, batchtx.BatchFlagAllOrNothing).
			AddInnerTx(MakeInnerCheckCreate(alice, bob, tx.NewXRPAmount(jtx.XRP(10)), aliceSeq)).
			AddInnerTx(MakeInnerPaymentXRPWithTicket(alice, bob, 1, aliceTicketSeq+1)).
			MustBuild()
		result := env.Submit(batch)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		env.Close()

		require.Equal(t, bobSeq+1, env.Seq(bob),
			"bob seq should advance by 1 (CheckCash)")
		require.Equal(t, aliceSeq+1, env.Seq(alice),
			"alice seq should advance by 1 (inner CheckCreate)")

		jtx.RequireBalance(t, env, alice, preAlice-uint64(jtx.XRP(11))-batchFee)
		jtx.RequireBalance(t, env, bob, preBob+uint64(jtx.XRP(11))-env.BaseFee())
	})

	t.Run("create object before batch txn", func(t *testing.T) {
		// Reference: rippled testObjectsOpenLedger() "Create Object Before Batch Txn"
		// CheckCreate submitted before the batch. The batch's inner CheckCash
		// consumes the check. The standalone CheckCreate runs first in the
		// open view.
		env := newBatchEnv(t)
		env.EnableOpenLedgerReplay()

		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		env.Fund(alice, bob)
		env.Close()

		aliceTicketSeq := env.CreateTickets(alice, 10)
		env.Close()

		aliceSeq := env.Seq(alice)
		bobSeq := env.Seq(bob)
		preAlice := env.Balance(alice)
		preBob := env.Balance(bob)

		chkID := GetCheckIndex(alice, aliceSeq)
		createTxn := check.NewCheckCreate(alice.Address, bob.Address, tx.NewXRPAmount(jtx.XRP(10)))
		createTxn.Fee = fmt.Sprintf("%d", env.BaseFee())
		createTxn.SigningPubKey = alice.PublicKeyHex()
		createTxn.Sequence = &aliceSeq
		result := env.Submit(createTxn)
		jtx.RequireTxSuccess(t, result)

		batchFee := CalcBatchFeeFromEnv(env, 1, 2)
		batch := NewBatchBuilderWithTicket(alice, aliceTicketSeq, batchFee, batchtx.BatchFlagAllOrNothing).
			AddInnerTx(MakeInnerCheckCash(bob, chkID, tx.NewXRPAmount(jtx.XRP(10)), bobSeq)).
			AddInnerTx(MakeInnerPaymentXRPWithTicket(alice, bob, 1, aliceTicketSeq+1)).
			AddSigner(bob).
			MustBuild()
		result = env.Submit(batch)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		require.Equal(t, bobSeq+1, env.Seq(bob),
			"bob seq should advance by 1 (inner CheckCash)")
		require.Equal(t, aliceSeq+1, env.Seq(alice),
			"alice seq should advance by 1 (standalone CheckCreate)")

		jtx.RequireBalance(t, env, alice, preAlice-uint64(jtx.XRP(11))-batchFee-env.BaseFee())
		jtx.RequireBalance(t, env, bob, preBob+uint64(jtx.XRP(11)))
	})

	t.Run("after batch txn", func(t *testing.T) {
		// Reference: rippled testObjectsOpenLedger() "After Batch Txn"
		// Batch creates a check (inner), then standalone CheckCash tries to cash it.
		// In rippled, the CheckCash gets tecNO_ENTRY because batch inner txns
		// are deferred. During replay, batch applies first, then CheckCash succeeds.
		env := newBatchEnv(t)
		env.EnableOpenLedgerReplay()

		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		env.Fund(alice, bob)
		env.Close()

		aliceTicketSeq := env.CreateTickets(alice, 10)
		env.Close()

		aliceSeq := env.Seq(alice)
		bobSeq := env.Seq(bob)
		preAlice := env.Balance(alice)
		preBob := env.Balance(bob)

		batchFee := CalcBatchFeeFromEnv(env, 0, 2)
		chkID := GetCheckIndex(alice, aliceSeq)
		batch := NewBatchBuilderWithTicket(alice, aliceTicketSeq, batchFee, batchtx.BatchFlagAllOrNothing).
			AddInnerTx(MakeInnerCheckCreate(alice, bob, tx.NewXRPAmount(jtx.XRP(10)), aliceSeq)).
			AddInnerTx(MakeInnerPaymentXRPWithTicket(alice, bob, 1, aliceTicketSeq+1)).
			MustBuild()
		result := env.Submit(batch)
		jtx.RequireTxSuccess(t, result)

		cashTxn := check.NewCheckCash(bob.Address, chkID)
		cashTxn.SetExactAmount(tx.NewXRPAmount(jtx.XRP(10)))
		cashTxn.Fee = fmt.Sprintf("%d", env.BaseFee())
		cashTxn.SigningPubKey = bob.PublicKeyHex()
		cashTxn.Sequence = &bobSeq
		cashResult := env.Submit(cashTxn)
		jtx.RequireTxClaimed(t, cashResult, "tecNO_ENTRY")
		env.Close()

		env.Close()

		require.Equal(t, bobSeq+1, env.Seq(bob),
			"bob seq should advance by 1 (CheckCash)")
		require.Equal(t, aliceSeq+1, env.Seq(alice),
			"alice seq should advance by 1 (inner CheckCreate)")

		jtx.RequireBalance(t, env, alice, preAlice-uint64(jtx.XRP(11))-batchFee)
		jtx.RequireBalance(t, env, bob, preBob+uint64(jtx.XRP(11))-env.BaseFee())
	})
}

func TestOpenLedger(t *testing.T) {
	// Reference: rippled Batch_test.cpp testOpenLedger()
	// Tests a mixed scenario: alice pays bob, then an atomic batch with
	// alice+bob, then bob pays alice with a future sequence (terPRE_SEQ).
	// The canonical ordering during consensus determines transaction placement.
	//
	// In rippled's canonical ordering (salted), alice's payment comes first,
	// then the batch, then bob's payment is retried next ledger.
	//
	// The open ledger cannot see the sequence consumed by the Batch inner until
	// consensus replay closes the ledger.
	env := newBatchEnv(t)
	env.EnableOpenLedgerReplay()

	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	env.Fund(alice, bob)
	env.Close()

	noopBob := accounttx.NewAccountSet(bob.Address)
	noopBob.Fee = fmt.Sprintf("%d", env.BaseFee())
	noopBob.SigningPubKey = bob.PublicKeyHex()
	bobNoopSeq := env.Seq(bob)
	noopBob.Sequence = &bobNoopSeq
	result := env.Submit(noopBob)
	jtx.RequireTxSuccess(t, result)
	env.Close()

	aliceSeq := env.Seq(alice)
	preAlice := env.Balance(alice)
	preBob := env.Balance(bob)
	bobSeq := env.Seq(bob)

	payTxn1 := payment.NewPayment(alice.Address, bob.Address, tx.NewXRPAmount(jtx.XRP(10)))
	payTxn1.Fee = fmt.Sprintf("%d", env.BaseFee())
	payTxn1.SigningPubKey = alice.PublicKeyHex()
	payTxn1.Sequence = &aliceSeq
	result = env.Submit(payTxn1)
	jtx.RequireTxSuccess(t, result)

	batchFee := CalcBatchFeeFromEnv(env, 1, 2)
	batch := NewBatchBuilder(alice, aliceSeq+1, batchFee, batchtx.BatchFlagAllOrNothing).
		AddInnerTx(MakeInnerPaymentXRP(alice, bob, 10, aliceSeq+2)).
		AddInnerTx(MakeInnerPaymentXRP(bob, alice, 5, bobSeq)).
		AddSigner(bob).
		MustBuild()
	result = env.Submit(batch)
	jtx.RequireTxSuccess(t, result)

	// Bob pays Alice (Open Ledger) at bobSeq+1. The Batch inner that consumes
	// bobSeq is deferred until close, so this transaction is held for retry.
	bobPaySeq := bobSeq + 1
	payTxn2 := payment.NewPayment(bob.Address, alice.Address, tx.NewXRPAmount(jtx.XRP(5)))
	payTxn2.Fee = fmt.Sprintf("%d", env.BaseFee())
	payTxn2.SigningPubKey = bob.PublicKeyHex()
	payTxn2.Sequence = &bobPaySeq
	payResult2 := env.Submit(payTxn2)
	jtx.RequireTxFail(t, payResult2, "terPRE_SEQ")
	env.Close()

	env.Close()

	require.Equal(t, aliceSeq+3, env.Seq(alice),
		"alice seq should have advanced by 3")

	require.Equal(t, bobSeq+2, env.Seq(bob),
		"bob seq should have advanced by 2")

	jtx.RequireBalance(t, env, alice, preAlice-uint64(jtx.XRP(10))-batchFee-env.BaseFee())

	jtx.RequireBalance(t, env, bob, preBob+uint64(jtx.XRP(10))-env.BaseFee())
}

func TestBadRawTxn(t *testing.T) {
	t.Run("nil inner transaction", func(t *testing.T) {
		env := newBatchEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		env.Fund(alice, bob)
		env.Close()

		seq := env.Seq(alice)
		batchFee := CalcBatchFeeFromEnv(env, 0, 2)

		batch := batchtx.NewBatch(alice.Address)
		batch.Fee = fmt.Sprintf("%d", batchFee)
		batch.SetSequence(seq)
		batch.SetFlags(batchtx.BatchFlagAllOrNothing)
		batch.RawTransactions = []batchtx.RawTransaction{
			{RawTransaction: batchtx.RawTransactionData{InnerTx: nil}},
			{RawTransaction: batchtx.RawTransactionData{InnerTx: MakeInnerPaymentXRP(alice, bob, 1, seq+2)}},
		}

		result := env.Submit(batch)
		jtx.RequireTxFail(t, result, "temMALFORMED")
		jtx.RequireSequence(t, env, alice, seq)
	})
}
