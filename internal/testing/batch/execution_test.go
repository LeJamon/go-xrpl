package batch

import (
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/tx"
	accounttx "github.com/LeJamon/go-xrpl/internal/tx/account"
	batchtx "github.com/LeJamon/go-xrpl/internal/tx/batch"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

func TestAllOrNothing(t *testing.T) {
	t.Run("all succeed", func(t *testing.T) {
		env := newBatchEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		env.Fund(alice, bob)
		env.Close()

		preAlice := env.Balance(alice)
		preBob := env.Balance(bob)

		batchFee := CalcBatchFeeFromEnv(env, 0, 2)
		seq := env.Seq(alice)
		batch := NewBatchBuilder(alice, seq, batchFee, batchtx.BatchFlagAllOrNothing).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 1, seq+1)).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 2, seq+2)).
			MustBuild()

		result := env.Submit(batch)
		jtx.RequireTxSuccess(t, result)
		env.Close()
		requireBatchLedgerData(t, env, batch, result, ter.TesSUCCESS, ter.TesSUCCESS)

		// Alice consumes sequences (outer + 2 inner)
		jtx.RequireSequence(t, env, alice, seq+3)

		// Alice pays XRP(3) + fee; Bob receives XRP(3)
		jtx.RequireBalance(t, env, alice, preAlice-uint64(jtx.XRP(3))-batchFee)
		jtx.RequireBalance(t, env, bob, preBob+uint64(jtx.XRP(3)))
	})

	t.Run("tec failure - all rolled back", func(t *testing.T) {
		env := newBatchEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		env.Fund(alice, bob)
		env.Close()

		preAlice := env.Balance(alice)
		preBob := env.Balance(bob)

		batchFee := CalcBatchFeeFromEnv(env, 0, 2)
		seq := env.Seq(alice)
		batch := NewBatchBuilder(alice, seq, batchFee, batchtx.BatchFlagAllOrNothing).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 1, seq+1)).
			// tecUNFUNDED_PAYMENT: alice does not have enough XRP
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 9999, seq+2)).
			MustBuild()

		result := env.Submit(batch)
		jtx.RequireTxSuccess(t, result) // Batch itself succeeds
		env.Close()
		requireBatchLedgerData(t, env, batch, result)

		// Only outer sequence consumed (inner txns rolled back)
		jtx.RequireSequence(t, env, alice, seq+1)

		// Alice pays fee only; Bob unaffected
		jtx.RequireBalance(t, env, alice, preAlice-batchFee)
		jtx.RequireBalance(t, env, bob, preBob)
	})

	t.Run("tef failure - all rolled back", func(t *testing.T) {
		env := newBatchEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		env.Fund(alice, bob)
		env.Close()

		preAlice := env.Balance(alice)
		preBob := env.Balance(bob)

		batchFee := CalcBatchFeeFromEnv(env, 0, 2)
		seq := env.Seq(alice)

		// Create a second inner tx that will cause a tef error.
		// AccountSet with SetFlag that requires authorization when not set up
		// triggers tefNO_AUTH_REQUIRED equivalent.
		// Use a past sequence for the second tx to trigger tefPAST_SEQ
		batch := NewBatchBuilder(alice, seq, batchFee, batchtx.BatchFlagAllOrNothing).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 1, seq+1)).
			AddInnerTx(MakeInnerPayment(alice, bob, jtx.XRP(1), 1)). // past seq -> tef
			MustBuild()

		result := env.Submit(batch)
		jtx.RequireTxSuccess(t, result) // Batch itself succeeds
		env.Close()
		requireBatchLedgerData(t, env, batch, result)

		// Only outer sequence consumed
		jtx.RequireSequence(t, env, alice, seq+1)

		// Alice pays fee only; Bob unaffected
		jtx.RequireBalance(t, env, alice, preAlice-batchFee)
		jtx.RequireBalance(t, env, bob, preBob)
	})
}

func TestOnlyOne(t *testing.T) {
	t.Run("all transactions fail", func(t *testing.T) {
		env := newBatchEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		env.Fund(alice, bob)
		env.Close()

		preAlice := env.Balance(alice)
		preBob := env.Balance(bob)

		batchFee := CalcBatchFeeFromEnv(env, 0, 3)
		seq := env.Seq(alice)
		batch := NewBatchBuilder(alice, seq, batchFee, batchtx.BatchFlagOnlyOne).
			// All underfunded
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 9999, seq+1)).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 9999, seq+2)).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 9999, seq+3)).
			MustBuild()

		result := env.Submit(batch)
		jtx.RequireTxSuccess(t, result)
		env.Close()
		requireBatchLedgerData(t, env, batch, result,
			ter.TecUNFUNDED_PAYMENT, ter.TecUNFUNDED_PAYMENT, ter.TecUNFUNDED_PAYMENT)

		// All inner txns executed (all failed) -> seq advances by 4 (outer + 3 inner)
		jtx.RequireSequence(t, env, alice, seq+4)

		// Alice pays fee only; Bob unaffected
		jtx.RequireBalance(t, env, alice, preAlice-batchFee)
		jtx.RequireBalance(t, env, bob, preBob)
	})

	t.Run("first fails then succeeds - stops after success", func(t *testing.T) {
		env := newBatchEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		env.Fund(alice, bob)
		env.Close()

		preAlice := env.Balance(alice)
		preBob := env.Balance(bob)

		batchFee := CalcBatchFeeFromEnv(env, 0, 3)
		seq := env.Seq(alice)
		batch := NewBatchBuilder(alice, seq, batchFee, batchtx.BatchFlagOnlyOne).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 9999, seq+1)). // fails
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 1, seq+2)).    // succeeds -> stop
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 2, seq+3)).    // not executed
			MustBuild()

		result := env.Submit(batch)
		jtx.RequireTxSuccess(t, result)
		env.Close()
		requireBatchLedgerData(t, env, batch, result, ter.TecUNFUNDED_PAYMENT, ter.TesSUCCESS)

		// Only 2 inner txns processed (fail + success) -> seq advances by 3
		jtx.RequireSequence(t, env, alice, seq+3)

		// Alice pays XRP(1) + fee
		jtx.RequireBalance(t, env, alice, preAlice-uint64(jtx.XRP(1))-batchFee)
		jtx.RequireBalance(t, env, bob, preBob+uint64(jtx.XRP(1)))
	})

	t.Run("succeeds first - stops immediately", func(t *testing.T) {
		env := newBatchEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		env.Fund(alice, bob)
		env.Close()

		preAlice := env.Balance(alice)
		preBob := env.Balance(bob)

		batchFee := CalcBatchFeeFromEnv(env, 0, 3)
		seq := env.Seq(alice)
		batch := NewBatchBuilder(alice, seq, batchFee, batchtx.BatchFlagOnlyOne).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 1, seq+1)).    // succeeds -> stop
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 9999, seq+2)). // not executed
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 2, seq+3)).    // not executed
			MustBuild()

		result := env.Submit(batch)
		jtx.RequireTxSuccess(t, result)
		env.Close()
		requireBatchLedgerData(t, env, batch, result, ter.TesSUCCESS)

		// Only 1 inner txn processed -> seq advances by 2
		jtx.RequireSequence(t, env, alice, seq+2)

		// Alice pays XRP(1) + fee
		jtx.RequireBalance(t, env, alice, preAlice-uint64(jtx.XRP(1))-batchFee)
		jtx.RequireBalance(t, env, bob, preBob+uint64(jtx.XRP(1)))
	})
}

func TestUntilFailure(t *testing.T) {
	t.Run("first transaction fails - stops immediately", func(t *testing.T) {
		env := newBatchEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		env.Fund(alice, bob)
		env.Close()

		preAlice := env.Balance(alice)
		preBob := env.Balance(bob)

		batchFee := CalcBatchFeeFromEnv(env, 0, 4)
		seq := env.Seq(alice)
		batch := NewBatchBuilder(alice, seq, batchFee, batchtx.BatchFlagUntilFailure).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 9999, seq+1)). // fails -> stop
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 1, seq+2)).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 2, seq+3)).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 3, seq+4)).
			MustBuild()

		result := env.Submit(batch)
		jtx.RequireTxSuccess(t, result)
		env.Close()
		requireBatchLedgerData(t, env, batch, result, ter.TecUNFUNDED_PAYMENT)

		// 1 inner txn processed (the failure) -> seq advances by 2
		jtx.RequireSequence(t, env, alice, seq+2)

		// Alice pays fee only; Bob unaffected
		jtx.RequireBalance(t, env, alice, preAlice-batchFee)
		jtx.RequireBalance(t, env, bob, preBob)
	})

	t.Run("all transactions succeed", func(t *testing.T) {
		env := newBatchEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		env.Fund(alice, bob)
		env.Close()

		preAlice := env.Balance(alice)
		preBob := env.Balance(bob)

		batchFee := CalcBatchFeeFromEnv(env, 0, 4)
		seq := env.Seq(alice)
		batch := NewBatchBuilder(alice, seq, batchFee, batchtx.BatchFlagUntilFailure).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 1, seq+1)).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 2, seq+2)).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 3, seq+3)).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 4, seq+4)).
			MustBuild()

		result := env.Submit(batch)
		jtx.RequireTxSuccess(t, result)
		env.Close()
		requireBatchLedgerData(t, env, batch, result,
			ter.TesSUCCESS, ter.TesSUCCESS, ter.TesSUCCESS, ter.TesSUCCESS)

		// All 4 inner txns succeed -> seq advances by 5
		jtx.RequireSequence(t, env, alice, seq+5)

		// Alice pays XRP(10) + fee
		jtx.RequireBalance(t, env, alice, preAlice-uint64(jtx.XRP(10))-batchFee)
		jtx.RequireBalance(t, env, bob, preBob+uint64(jtx.XRP(10)))
	})

	t.Run("tec error in middle - stops at failure", func(t *testing.T) {
		env := newBatchEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		env.Fund(alice, bob)
		env.Close()

		preAlice := env.Balance(alice)
		preBob := env.Balance(bob)

		batchFee := CalcBatchFeeFromEnv(env, 0, 4)
		seq := env.Seq(alice)
		batch := NewBatchBuilder(alice, seq, batchFee, batchtx.BatchFlagUntilFailure).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 1, seq+1)).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 2, seq+2)).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 9999, seq+3)). // fails -> stop
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 3, seq+4)).    // not executed
			MustBuild()

		result := env.Submit(batch)
		jtx.RequireTxSuccess(t, result)
		env.Close()
		requireBatchLedgerData(t, env, batch, result,
			ter.TesSUCCESS, ter.TesSUCCESS, ter.TecUNFUNDED_PAYMENT)

		// 3 inner txns processed (2 success + 1 failure) -> seq advances by 4
		jtx.RequireSequence(t, env, alice, seq+4)

		// Alice pays XRP(3) + fee (the 2 successful payments)
		jtx.RequireBalance(t, env, alice, preAlice-uint64(jtx.XRP(3))-batchFee)
		jtx.RequireBalance(t, env, bob, preBob+uint64(jtx.XRP(3)))
	})
}

func TestIndependent(t *testing.T) {
	t.Run("multiple transactions fail - all execute", func(t *testing.T) {
		env := newBatchEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		env.Fund(alice, bob)
		env.Close()

		preAlice := env.Balance(alice)
		preBob := env.Balance(bob)

		batchFee := CalcBatchFeeFromEnv(env, 0, 4)
		seq := env.Seq(alice)
		batch := NewBatchBuilder(alice, seq, batchFee, batchtx.BatchFlagIndependent).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 1, seq+1)).    // succeeds
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 9999, seq+2)). // fails
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 9999, seq+3)). // fails
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 3, seq+4)).    // succeeds
			MustBuild()

		result := env.Submit(batch)
		jtx.RequireTxSuccess(t, result)
		env.Close()
		requireBatchLedgerData(t, env, batch, result,
			ter.TesSUCCESS, ter.TecUNFUNDED_PAYMENT, ter.TecUNFUNDED_PAYMENT, ter.TesSUCCESS)

		// All 4 inner txns processed -> seq advances by 5
		jtx.RequireSequence(t, env, alice, seq+5)

		// Alice pays XRP(4) + fee (only successful payments)
		jtx.RequireBalance(t, env, alice, preAlice-uint64(jtx.XRP(4))-batchFee)
		jtx.RequireBalance(t, env, bob, preBob+uint64(jtx.XRP(4)))
	})

	t.Run("tec error in middle - continues executing", func(t *testing.T) {
		env := newBatchEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		env.Fund(alice, bob)
		env.Close()

		preAlice := env.Balance(alice)
		preBob := env.Balance(bob)

		batchFee := CalcBatchFeeFromEnv(env, 0, 4)
		seq := env.Seq(alice)
		batch := NewBatchBuilder(alice, seq, batchFee, batchtx.BatchFlagIndependent).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 1, seq+1)).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 2, seq+2)).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 9999, seq+3)). // fails
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 3, seq+4)).
			MustBuild()

		result := env.Submit(batch)
		jtx.RequireTxSuccess(t, result)
		env.Close()
		requireBatchLedgerData(t, env, batch, result,
			ter.TesSUCCESS, ter.TesSUCCESS, ter.TecUNFUNDED_PAYMENT, ter.TesSUCCESS)

		// All 4 inner txns processed -> seq advances by 5
		jtx.RequireSequence(t, env, alice, seq+5)

		// Alice pays XRP(6) + fee (3 successful payments)
		jtx.RequireBalance(t, env, alice, preAlice-uint64(jtx.XRP(6))-batchFee)
		jtx.RequireBalance(t, env, bob, preBob+uint64(jtx.XRP(6)))
	})
}

func TestAccountActivation(t *testing.T) {
	env := newBatchEnv(t)
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	env.FundAmount(alice, uint64(jtx.XRP(10000))) // rippled funds with XRP(10000)
	env.Close()

	// Bob does not exist yet
	jtx.RequireAccountNotExists(t, env, bob)

	preAlice := env.Balance(alice)

	seq := env.Seq(alice)
	batchFee := CalcBatchFeeFromEnv(env, 0, 2)

	// Create bob by funding and then do an AccountSet on bob within the batch
	batch := NewBatchBuilder(alice, seq, batchFee, batchtx.BatchFlagAllOrNothing).
		AddInnerTx(MakeInnerPaymentXRP(alice, bob, 1000, seq+1)).
		AddInnerTx(MakeInnerPaymentXRP(alice, bob, 1, seq+2)). // second payment to newly created bob
		MustBuild()

	result := env.Submit(batch)
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// Bob now exists
	jtx.RequireAccountExists(t, env, bob)

	// Alice consumes sequences (outer + 2 inner)
	jtx.RequireSequence(t, env, alice, seq+3)

	// Alice pays XRP(1001) + fee; Bob receives XRP(1001)
	jtx.RequireBalance(t, env, alice, preAlice-uint64(jtx.XRP(1001))-batchFee)
	jtx.RequireBalance(t, env, bob, uint64(jtx.XRP(1001)))
}

// TestActivateTwoAccounts is the issue #846 regression: a Batch whose inner txs
// fund TWO distinct new accounts must succeed. rippled runs each inner Payment
// through its own invariant pass, so each pass sees exactly one account
// creation; the outer ttBATCH pass never sees the combined two-creation delta.
// goXRPL previously ran the shared ValidNewAccountRoot checker on the combined
// outer delta (createdCount=2) and wrongly returned tecINVARIANT_FAILED.
// Reference: rippled apply.cpp:189-207, InvariantCheck.cpp:964-967.
func TestActivateTwoAccounts(t *testing.T) {
	env := newBatchEnv(t)
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	carol := jtx.NewAccount("carol")
	env.FundAmount(alice, uint64(jtx.XRP(10000)))
	env.Close()

	jtx.RequireAccountNotExists(t, env, bob)
	jtx.RequireAccountNotExists(t, env, carol)

	preAlice := env.Balance(alice)
	seq := env.Seq(alice)
	batchFee := CalcBatchFeeFromEnv(env, 0, 2)

	// Two funding Payments to two brand-new accounts in a single batch.
	batch := NewBatchBuilder(alice, seq, batchFee, batchtx.BatchFlagAllOrNothing).
		AddInnerTx(MakeInnerPaymentXRP(alice, bob, 1000, seq+1)).
		AddInnerTx(MakeInnerPaymentXRP(alice, carol, 1000, seq+2)).
		MustBuild()

	result := env.Submit(batch)
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// Both new accounts now exist and are funded.
	jtx.RequireAccountExists(t, env, bob)
	jtx.RequireAccountExists(t, env, carol)

	// Alice consumes sequences (outer + 2 inner).
	jtx.RequireSequence(t, env, alice, seq+3)

	// Alice pays XRP(2000) + fee; bob and carol each receive XRP(1000).
	jtx.RequireBalance(t, env, alice, preAlice-uint64(jtx.XRP(2000))-batchFee)
	jtx.RequireBalance(t, env, bob, uint64(jtx.XRP(1000)))
	jtx.RequireBalance(t, env, carol, uint64(jtx.XRP(1000)))
}

func TestAccountSet(t *testing.T) {
	env := newBatchEnv(t)
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	env.Fund(alice, bob)
	env.Close()

	preAlice := env.Balance(alice)
	preBob := env.Balance(bob)

	seq := env.Seq(alice)
	batchFee := CalcBatchFeeFromEnv(env, 0, 2)

	// Create an AccountSet (require dest tag) as inner tx
	as := accounttx.NewAccountSet(alice.Address)
	as.Fee = "0"
	as.SigningPubKey = ""
	as.SetSequence(seq + 1)
	as.SetFlags(tx.TfInnerBatchTxn)
	flag := accounttx.AccountSetFlagRequireDest
	as.SetFlag = &flag

	batch := NewBatchBuilder(alice, seq, batchFee, batchtx.BatchFlagAllOrNothing).
		AddInnerTx(as).
		AddInnerTx(MakeInnerPaymentXRP(alice, bob, 1, seq+2)).
		MustBuild()

	result := env.Submit(batch)
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// Alice consumes sequences (outer + 2 inner)
	jtx.RequireSequence(t, env, alice, seq+3)

	// Alice pays XRP(1) + fee; Bob receives XRP(1)
	jtx.RequireBalance(t, env, alice, preAlice-uint64(jtx.XRP(1))-batchFee)
	jtx.RequireBalance(t, env, bob, preBob+uint64(jtx.XRP(1)))
}

func TestBadSequence(t *testing.T) {
	t.Run("past sequence - inner tx with past seq", func(t *testing.T) {
		env := newBatchEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		env.Fund(alice, bob)
		env.Close()

		preAlice := env.Balance(alice)
		preBob := env.Balance(bob)
		preAliceSeq := env.Seq(alice)

		batchFee := CalcBatchFeeFromEnv(env, 0, 2)
		batch := NewBatchBuilder(alice, preAliceSeq, batchFee, batchtx.BatchFlagAllOrNothing).
			// Past sequence (before current)
			AddInnerTx(MakeInnerPayment(alice, bob, jtx.XRP(10), 1)).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 5, preAliceSeq+1)).
			MustBuild()

		result := env.Submit(batch)
		jtx.RequireTxSuccess(t, result) // Batch itself succeeds
		env.Close()

		// Alice pays fee only, sequence advances by 1 (outer only)
		jtx.RequireSequence(t, env, alice, preAliceSeq+1)
		jtx.RequireBalance(t, env, alice, preAlice-batchFee)
		jtx.RequireBalance(t, env, bob, preBob)
	})

	t.Run("future sequence - inner tx with far future seq", func(t *testing.T) {
		env := newBatchEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		env.Fund(alice, bob)
		env.Close()

		preAlice := env.Balance(alice)
		preBob := env.Balance(bob)
		preAliceSeq := env.Seq(alice)

		batchFee := CalcBatchFeeFromEnv(env, 0, 2)
		batch := NewBatchBuilder(alice, preAliceSeq, batchFee, batchtx.BatchFlagAllOrNothing).
			// Future sequence (well ahead of current)
			AddInnerTx(MakeInnerPayment(alice, bob, jtx.XRP(10), preAliceSeq+10)).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 5, preAliceSeq+1)).
			MustBuild()

		result := env.Submit(batch)
		jtx.RequireTxSuccess(t, result) // Batch itself succeeds
		env.Close()

		// Alice pays fee only, sequence advances by 1 (outer only)
		jtx.RequireSequence(t, env, alice, preAliceSeq+1)
		jtx.RequireBalance(t, env, alice, preAlice-batchFee)
		jtx.RequireBalance(t, env, bob, preBob)
	})

	t.Run("same sequence as outer - inner tx uses outer's seq", func(t *testing.T) {
		env := newBatchEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		env.Fund(alice, bob)
		env.Close()

		preAlice := env.Balance(alice)
		preBob := env.Balance(bob)
		preAliceSeq := env.Seq(alice)

		batchFee := CalcBatchFeeFromEnv(env, 0, 2)
		batch := NewBatchBuilder(alice, preAliceSeq, batchFee, batchtx.BatchFlagAllOrNothing).
			// Same sequence as outer
			AddInnerTx(MakeInnerPayment(alice, bob, jtx.XRP(10), preAliceSeq)).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 5, preAliceSeq+1)).
			MustBuild()

		result := env.Submit(batch)
		jtx.RequireTxSuccess(t, result) // Batch itself succeeds
		env.Close()

		// Alice pays fee only
		jtx.RequireSequence(t, env, alice, preAliceSeq+1)
		jtx.RequireBalance(t, env, alice, preAlice-batchFee)
		jtx.RequireBalance(t, env, bob, preBob)
	})
}

func TestBadOuterFee(t *testing.T) {
	t.Run("insufficient fee without signers", func(t *testing.T) {
		env := newBatchEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		env.Fund(alice, bob)
		env.Close()

		// Bad fee: should be calcBatchFee(env, 0, 2) = 40, but we use 30
		badFee := CalcBatchFeeFromEnv(env, 0, 1) // 30 instead of 40
		seq := env.Seq(alice)
		batch := NewBatchBuilder(alice, seq, badFee, batchtx.BatchFlagAllOrNothing).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 10, seq+1)).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 15, seq+2)).
			MustBuild()

		result := env.Submit(batch)
		jtx.RequireTxFail(t, result, "telINSUF_FEE_P")
	})

	t.Run("insufficient fee with batch signers", func(t *testing.T) {
		env := newBatchEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		env.Fund(alice, bob)
		env.Close()

		// Bad fee: should be calcBatchFee(env, 1, 2) = 50, but we use 40.
		// The second inner is from bob so bob is a genuinely required signer,
		// letting preflight pass and the fee floor fire in preclaim.
		badFee := CalcBatchFeeFromEnv(env, 0, 2) // 40 instead of 50
		seq := env.Seq(alice)
		batch := NewBatchBuilder(alice, seq, badFee, batchtx.BatchFlagAllOrNothing).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 10, seq+1)).
			AddInnerTx(MakeInnerPaymentXRP(bob, alice, 5, env.Seq(bob))).
			AddSigner(bob).
			MustBuild()

		result := env.Submit(batch)
		jtx.RequireTxFail(t, result, "telINSUF_FEE_P")
	})
}

func TestBatchDelegate(t *testing.T) {
	t.Run("delegated non atomic inner", func(t *testing.T) {
		// Alice delegates "Payment" permission to bob.
		// Inner tx[0] is a payment from alice to bob with Delegate=bob.
		// Inner tx[1] is a regular payment from alice to bob.
		// Reference: rippled Batch_test.cpp testBatchDelegate() - "delegated non atomic inner"
		env := newBatchEnv(t)
		env.EnableFeature("PermissionDelegationV1_1")

		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		env.FundAmount(alice, uint64(jtx.XRP(10000)))
		env.FundAmount(bob, uint64(jtx.XRP(10000)))
		env.Close()

		// Alice delegates Payment permission to bob
		env.SetDelegate(alice, bob, []string{"Payment"})
		env.Close()

		preAlice := env.Balance(alice)
		preBob := env.Balance(bob)

		batchFee := CalcBatchFeeFromEnv(env, 0, 2)
		seq := env.Seq(alice)

		// Inner tx[0]: payment from alice to bob, delegated to bob
		innerTx0 := MakeInnerPaymentXRPWithDelegate(alice, bob, 1, seq+1, bob)
		// Inner tx[1]: regular payment from alice to bob
		innerTx1 := MakeInnerPaymentXRP(alice, bob, 2, seq+2)

		batch := NewBatchBuilder(alice, seq, batchFee, batchtx.BatchFlagAllOrNothing).
			AddInnerTx(innerTx0).
			AddInnerTx(innerTx1).
			MustBuild()

		result := env.Submit(batch)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// Alice consumes sequences: outer + 2 inner = seq + 3
		jtx.RequireSequence(t, env, alice, seq+3)

		// Alice pays XRP(3) + fee; Bob receives XRP(3)
		jtx.RequireBalance(t, env, alice, preAlice-uint64(jtx.XRP(3))-batchFee)
		jtx.RequireBalance(t, env, bob, preBob+uint64(jtx.XRP(3)))
	})

	t.Run("delegated atomic inner", func(t *testing.T) {
		// Bob delegates "Payment" permission to carol.
		// Carol submits batch: inner tx[0] is payment bob->alice with Delegate=carol, inner tx[1] is payment alice->bob.
		// Reference: rippled Batch_test.cpp testBatchDelegate() - "delegated atomic inner"
		env := newBatchEnv(t)
		env.EnableFeature("PermissionDelegationV1_1")

		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		carol := jtx.NewAccount("carol")
		env.FundAmount(alice, uint64(jtx.XRP(10000)))
		env.FundAmount(bob, uint64(jtx.XRP(10000)))
		env.FundAmount(carol, uint64(jtx.XRP(10000)))
		env.Close()

		// Bob delegates Payment permission to carol
		env.SetDelegate(bob, carol, []string{"Payment"})
		env.Close()

		preAlice := env.Balance(alice)
		preBob := env.Balance(bob)
		preCarol := env.Balance(carol)

		batchFee := CalcBatchFeeFromEnv(env, 1, 2)
		aliceSeq := env.Seq(alice)
		bobSeq := env.Seq(bob)

		// Inner tx[0]: payment bob->alice, delegated to carol
		innerTx0 := MakeInnerPaymentXRPWithDelegate(bob, alice, 1, bobSeq, carol)
		// Inner tx[1]: payment alice->bob
		innerTx1 := MakeInnerPaymentXRP(alice, bob, 2, aliceSeq+1)

		batch := NewBatchBuilder(alice, aliceSeq, batchFee, batchtx.BatchFlagAllOrNothing).
			AddInnerTx(innerTx0).
			AddInnerTx(innerTx1).
			AddSigner(bob).
			MustBuild()

		result := env.Submit(batch)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// Alice: outer seq + 1 inner = aliceSeq + 2
		jtx.RequireSequence(t, env, alice, aliceSeq+2)
		// Bob: 1 inner = bobSeq + 1
		jtx.RequireSequence(t, env, bob, bobSeq+1)

		// Alice: -XRP(1) (net: pay 2 to bob, receive 1 from bob) - batchFee
		jtx.RequireBalance(t, env, alice, preAlice-uint64(jtx.XRP(1))-batchFee)
		// Bob: +XRP(1) (net: receive 2 from alice, pay 1 to alice)
		jtx.RequireBalance(t, env, bob, preBob+uint64(jtx.XRP(1)))
		// Carol: unchanged (batch is atomic, fee is paid by batch outer account)
		jtx.RequireBalance(t, env, carol, preCarol)
	})
}

// Tests 13-15: testTickets, testTicketsOpenLedger
