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

		jtx.RequireSequence(t, env, alice, seq+3)

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
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 9999, seq+2)).
			MustBuild()

		result := env.Submit(batch)
		jtx.RequireTxSuccess(t, result)
		env.Close()
		requireBatchLedgerData(t, env, batch, result)

		jtx.RequireSequence(t, env, alice, seq+1)

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

		batch := NewBatchBuilder(alice, seq, batchFee, batchtx.BatchFlagAllOrNothing).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 1, seq+1)).
			AddInnerTx(MakeInnerPayment(alice, bob, jtx.XRP(1), 1)).
			MustBuild()

		result := env.Submit(batch)
		jtx.RequireTxSuccess(t, result)
		env.Close()
		requireBatchLedgerData(t, env, batch, result)

		jtx.RequireSequence(t, env, alice, seq+1)

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
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 9999, seq+1)).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 9999, seq+2)).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 9999, seq+3)).
			MustBuild()

		result := env.Submit(batch)
		jtx.RequireTxSuccess(t, result)
		env.Close()
		requireBatchLedgerData(t, env, batch, result,
			ter.TecUNFUNDED_PAYMENT, ter.TecUNFUNDED_PAYMENT, ter.TecUNFUNDED_PAYMENT)

		jtx.RequireSequence(t, env, alice, seq+4)

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
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 9999, seq+1)).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 1, seq+2)).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 2, seq+3)).
			MustBuild()

		result := env.Submit(batch)
		jtx.RequireTxSuccess(t, result)
		env.Close()
		requireBatchLedgerData(t, env, batch, result, ter.TecUNFUNDED_PAYMENT, ter.TesSUCCESS)

		jtx.RequireSequence(t, env, alice, seq+3)

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
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 1, seq+1)).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 9999, seq+2)).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 2, seq+3)).
			MustBuild()

		result := env.Submit(batch)
		jtx.RequireTxSuccess(t, result)
		env.Close()
		requireBatchLedgerData(t, env, batch, result, ter.TesSUCCESS)

		jtx.RequireSequence(t, env, alice, seq+2)

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
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 9999, seq+1)).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 1, seq+2)).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 2, seq+3)).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 3, seq+4)).
			MustBuild()

		result := env.Submit(batch)
		jtx.RequireTxSuccess(t, result)
		env.Close()
		requireBatchLedgerData(t, env, batch, result, ter.TecUNFUNDED_PAYMENT)

		jtx.RequireSequence(t, env, alice, seq+2)

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

		jtx.RequireSequence(t, env, alice, seq+5)

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
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 9999, seq+3)).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 3, seq+4)).
			MustBuild()

		result := env.Submit(batch)
		jtx.RequireTxSuccess(t, result)
		env.Close()
		requireBatchLedgerData(t, env, batch, result,
			ter.TesSUCCESS, ter.TesSUCCESS, ter.TecUNFUNDED_PAYMENT)

		jtx.RequireSequence(t, env, alice, seq+4)

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
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 1, seq+1)).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 9999, seq+2)).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 9999, seq+3)).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 3, seq+4)).
			MustBuild()

		result := env.Submit(batch)
		jtx.RequireTxSuccess(t, result)
		env.Close()
		requireBatchLedgerData(t, env, batch, result,
			ter.TesSUCCESS, ter.TecUNFUNDED_PAYMENT, ter.TecUNFUNDED_PAYMENT, ter.TesSUCCESS)

		jtx.RequireSequence(t, env, alice, seq+5)

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
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 9999, seq+3)).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 3, seq+4)).
			MustBuild()

		result := env.Submit(batch)
		jtx.RequireTxSuccess(t, result)
		env.Close()
		requireBatchLedgerData(t, env, batch, result,
			ter.TesSUCCESS, ter.TesSUCCESS, ter.TecUNFUNDED_PAYMENT, ter.TesSUCCESS)

		jtx.RequireSequence(t, env, alice, seq+5)

		jtx.RequireBalance(t, env, alice, preAlice-uint64(jtx.XRP(6))-batchFee)
		jtx.RequireBalance(t, env, bob, preBob+uint64(jtx.XRP(6)))
	})
}

func TestAccountActivation(t *testing.T) {
	env := newBatchEnv(t)
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	env.FundAmount(alice, uint64(jtx.XRP(10000)))
	env.Close()

	jtx.RequireAccountNotExists(t, env, bob)

	preAlice := env.Balance(alice)

	seq := env.Seq(alice)
	batchFee := CalcBatchFeeFromEnv(env, 0, 2)

	batch := NewBatchBuilder(alice, seq, batchFee, batchtx.BatchFlagAllOrNothing).
		AddInnerTx(MakeInnerPaymentXRP(alice, bob, 1000, seq+1)).
		AddInnerTx(MakeInnerPaymentXRP(alice, bob, 1, seq+2)).
		MustBuild()

	result := env.Submit(batch)
	jtx.RequireTxSuccess(t, result)
	env.Close()

	jtx.RequireAccountExists(t, env, bob)

	jtx.RequireSequence(t, env, alice, seq+3)

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

	batch := NewBatchBuilder(alice, seq, batchFee, batchtx.BatchFlagAllOrNothing).
		AddInnerTx(MakeInnerPaymentXRP(alice, bob, 1000, seq+1)).
		AddInnerTx(MakeInnerPaymentXRP(alice, carol, 1000, seq+2)).
		MustBuild()

	result := env.Submit(batch)
	jtx.RequireTxSuccess(t, result)
	env.Close()

	jtx.RequireAccountExists(t, env, bob)
	jtx.RequireAccountExists(t, env, carol)

	jtx.RequireSequence(t, env, alice, seq+3)

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

	jtx.RequireSequence(t, env, alice, seq+3)

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
			AddInnerTx(MakeInnerPayment(alice, bob, jtx.XRP(10), 1)).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 5, preAliceSeq+1)).
			MustBuild()

		result := env.Submit(batch)
		jtx.RequireTxSuccess(t, result)
		env.Close()

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
			AddInnerTx(MakeInnerPayment(alice, bob, jtx.XRP(10), preAliceSeq+10)).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 5, preAliceSeq+1)).
			MustBuild()

		result := env.Submit(batch)
		jtx.RequireTxSuccess(t, result)
		env.Close()

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
			AddInnerTx(MakeInnerPayment(alice, bob, jtx.XRP(10), preAliceSeq)).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 5, preAliceSeq+1)).
			MustBuild()

		result := env.Submit(batch)
		jtx.RequireTxSuccess(t, result)
		env.Close()

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

		badFee := CalcBatchFeeFromEnv(env, 0, 1)
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
		badFee := CalcBatchFeeFromEnv(env, 0, 2)
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
		// Reference: rippled Batch_test.cpp testBatchDelegate() - "delegated non atomic inner"
		env := newBatchEnv(t)
		env.EnableFeature("PermissionDelegationV1_1")

		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		env.FundAmount(alice, uint64(jtx.XRP(10000)))
		env.FundAmount(bob, uint64(jtx.XRP(10000)))
		env.Close()

		env.SetDelegate(alice, bob, []string{"Payment"})
		env.Close()

		preAlice := env.Balance(alice)
		preBob := env.Balance(bob)

		batchFee := CalcBatchFeeFromEnv(env, 0, 2)
		seq := env.Seq(alice)

		innerTx0 := MakeInnerPaymentXRPWithDelegate(alice, bob, 1, seq+1, bob)
		innerTx1 := MakeInnerPaymentXRP(alice, bob, 2, seq+2)

		batch := NewBatchBuilder(alice, seq, batchFee, batchtx.BatchFlagAllOrNothing).
			AddInnerTx(innerTx0).
			AddInnerTx(innerTx1).
			MustBuild()

		result := env.Submit(batch)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		jtx.RequireSequence(t, env, alice, seq+3)

		jtx.RequireBalance(t, env, alice, preAlice-uint64(jtx.XRP(3))-batchFee)
		jtx.RequireBalance(t, env, bob, preBob+uint64(jtx.XRP(3)))
	})

	t.Run("delegated atomic inner", func(t *testing.T) {
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

		env.SetDelegate(bob, carol, []string{"Payment"})
		env.Close()

		preAlice := env.Balance(alice)
		preBob := env.Balance(bob)
		preCarol := env.Balance(carol)

		batchFee := CalcBatchFeeFromEnv(env, 1, 2)
		aliceSeq := env.Seq(alice)
		bobSeq := env.Seq(bob)

		innerTx0 := MakeInnerPaymentXRPWithDelegate(bob, alice, 1, bobSeq, carol)
		innerTx1 := MakeInnerPaymentXRP(alice, bob, 2, aliceSeq+1)

		batch := NewBatchBuilder(alice, aliceSeq, batchFee, batchtx.BatchFlagAllOrNothing).
			AddInnerTx(innerTx0).
			AddInnerTx(innerTx1).
			AddSigner(bob).
			MustBuild()

		result := env.Submit(batch)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		jtx.RequireSequence(t, env, alice, aliceSeq+2)
		jtx.RequireSequence(t, env, bob, bobSeq+1)

		jtx.RequireBalance(t, env, alice, preAlice-uint64(jtx.XRP(1))-batchFee)
		jtx.RequireBalance(t, env, bob, preBob+uint64(jtx.XRP(1)))
		jtx.RequireBalance(t, env, carol, preCarol)
	})
}
