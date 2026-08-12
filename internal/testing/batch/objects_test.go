package batch

import (
	"fmt"
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/tx"
	batchtx "github.com/LeJamon/go-xrpl/internal/tx/batch"
	"github.com/stretchr/testify/require"
)

func TestAccountDelete(t *testing.T) {
	t.Run("tfIndependent - account delete success", func(t *testing.T) {
		// Reference: rippled Batch_test.cpp testAccountDelete() - tfIndependent success
		env := newBatchEnv(t)

		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		env.FundAmount(alice, uint64(jtx.XRP(10000)))
		env.FundAmount(bob, uint64(jtx.XRP(10000)))
		env.Close()

		env.IncLedgerSeqForAccDel(alice)
		for range 5 {
			env.Close()
		}

		preAlice := env.Balance(alice)
		preBob := env.Balance(bob)

		seq := env.Seq(alice)
		batch := NewBatchBuilder(alice, seq, 0, batchtx.BatchFlagIndependent).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 1, seq+1)).
			AddInnerTx(MakeInnerAccountDelete(alice, bob, seq+2)).
			// terNO_ACCOUNT: alice does not exist after deletion
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 2, seq+3)).
			MustBuild()
		batchFee := SetMinimumBatchFeeFromEnv(env, batch)

		result := env.Submit(batch)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// Alice does not exist; Bob receives Alice's XRP
		jtx.RequireAccountNotExists(t, env, alice)
		jtx.RequireBalance(t, env, bob, preBob+(preAlice-batchFee))
	})

	t.Run("tfIndependent - account delete fails", func(t *testing.T) {
		// Reference: rippled Batch_test.cpp testAccountDelete() - tfIndependent fails
		env := newBatchEnv(t)

		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		env.FundAmount(alice, uint64(jtx.XRP(10000)))
		env.FundAmount(bob, uint64(jtx.XRP(10000)))
		env.Close()

		env.IncLedgerSeqForAccDel(alice)
		for range 5 {
			env.Close()
		}

		preBob := env.Balance(bob)

		// Alice creates a trust line which counts as an obligation
		env.Trust(alice, bob.IOU("USD", 1000))
		env.Close()

		seq := env.Seq(alice)
		batch := NewBatchBuilder(alice, seq, 0, batchtx.BatchFlagIndependent).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 1, seq+1)).
			// tecHAS_OBLIGATIONS: alice has obligations (trust line)
			AddInnerTx(MakeInnerAccountDelete(alice, bob, seq+2)).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 2, seq+3)).
			MustBuild()
		SetMinimumBatchFeeFromEnv(env, batch)

		result := env.Submit(batch)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// Alice still exists; Bob receives XRP(3) from the two successful payments
		jtx.RequireAccountExists(t, env, alice)
		jtx.RequireBalance(t, env, bob, preBob+uint64(jtx.XRP(3)))
	})

	t.Run("tfAllOrNothing - account delete fails", func(t *testing.T) {
		// Reference: rippled Batch_test.cpp testAccountDelete() - tfAllOrNothing fails
		env := newBatchEnv(t)

		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		env.FundAmount(alice, uint64(jtx.XRP(10000)))
		env.FundAmount(bob, uint64(jtx.XRP(10000)))
		env.Close()

		env.IncLedgerSeqForAccDel(alice)
		for range 5 {
			env.Close()
		}

		preBob := env.Balance(bob)

		seq := env.Seq(alice)
		batch := NewBatchBuilder(alice, seq, 0, batchtx.BatchFlagAllOrNothing).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 1, seq+1)).
			AddInnerTx(MakeInnerAccountDelete(alice, bob, seq+2)).
			// terNO_ACCOUNT: alice does not exist after deletion, causing rollback
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 2, seq+3)).
			MustBuild()
		SetMinimumBatchFeeFromEnv(env, batch)

		result := env.Submit(batch)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// Alice still exists (all rolled back); Bob is unchanged
		jtx.RequireAccountExists(t, env, alice)
		jtx.RequireBalance(t, env, bob, preBob)
	})
}

func TestObjectCreateSequence(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		// Create a CheckCreate from bob to alice, then CheckCash from alice, all in a batch.
		// Reference: rippled Batch_test.cpp testObjectCreateSequence() - success
		env := newBatchEnv(t)

		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		gw := jtx.NewAccount("gw")
		env.FundAmount(alice, uint64(jtx.XRP(10000)))
		env.FundAmount(bob, uint64(jtx.XRP(10000)))
		env.FundAmount(gw, uint64(jtx.XRP(10000)))
		env.Close()

		// Set up trust lines and issue USD
		usd10 := tx.NewIssuedAmountFromFloat64(10, "USD", gw.Address)
		usd1000 := tx.NewIssuedAmountFromFloat64(1000, "USD", gw.Address)

		env.Trust(alice, usd1000)
		env.Trust(bob, usd1000)
		env.PayIOU(gw, alice, gw, "USD", 100)
		env.PayIOU(gw, bob, gw, "USD", 100)
		env.Close()

		aliceSeq := env.Seq(alice)
		bobSeq := env.Seq(bob)
		preAlice := env.Balance(alice)
		preBob := env.Balance(bob)
		preAliceUSD := env.BalanceIOU(alice, "USD", gw)
		preBobUSD := env.BalanceIOU(bob, "USD", gw)

		// CheckCreate from bob to alice for USD(10), then CheckCash from alice
		// chkID is derived from bob's account and bob's current seq
		chkID := GetCheckIndex(bob, bobSeq)

		batchFee := CalcBatchFeeFromEnv(env, 1, 2)
		batch := NewBatchBuilder(alice, aliceSeq, batchFee, batchtx.BatchFlagAllOrNothing).
			AddInnerTx(MakeInnerCheckCreate(bob, alice, usd10, bobSeq)).
			AddInnerTx(MakeInnerCheckCash(alice, chkID, usd10, aliceSeq+1)).
			AddSigner(bob).
			MustBuild()

		result := env.Submit(batch)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// Alice consumes sequences (outer + 1 inner)
		jtx.RequireSequence(t, env, alice, aliceSeq+2)

		// Bob consumes sequences (1 inner)
		jtx.RequireSequence(t, env, bob, bobSeq+1)

		// Alice pays fee; Bob XRP unchanged
		jtx.RequireBalance(t, env, alice, preAlice-batchFee)
		jtx.RequireBalance(t, env, bob, preBob)

		// Alice gains USD(10); Bob loses USD(10)
		require.InDelta(t, preAliceUSD+10.0, env.BalanceIOU(alice, "USD", gw), 0.001,
			"alice should have gained USD 10")
		require.InDelta(t, preBobUSD-10.0, env.BalanceIOU(bob, "USD", gw), 0.001,
			"bob should have lost USD 10")
	})

	t.Run("failure - tecDST_TAG_NEEDED", func(t *testing.T) {
		// Alice enables asfRequireDest, so CheckCreate to alice fails with tecDST_TAG_NEEDED.
		// In Independent mode, CheckCash then fails with tecNO_ENTRY.
		// Reference: rippled Batch_test.cpp testObjectCreateSequence() - failure
		env := newBatchEnv(t)

		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		gw := jtx.NewAccount("gw")
		env.FundAmount(alice, uint64(jtx.XRP(10000)))
		env.FundAmount(bob, uint64(jtx.XRP(10000)))
		env.FundAmount(gw, uint64(jtx.XRP(10000)))
		env.Close()

		usd10 := tx.NewIssuedAmountFromFloat64(10, "USD", gw.Address)
		usd1000 := tx.NewIssuedAmountFromFloat64(1000, "USD", gw.Address)

		env.Trust(alice, usd1000)
		env.Trust(bob, usd1000)
		env.PayIOU(gw, alice, gw, "USD", 100)
		env.PayIOU(gw, bob, gw, "USD", 100)
		env.Close()

		// Enable RequireDest on alice
		env.EnableRequireDest(alice)
		env.Close()

		aliceSeq := env.Seq(alice)
		bobSeq := env.Seq(bob)
		preAlice := env.Balance(alice)
		preBob := env.Balance(bob)
		preAliceUSD := env.BalanceIOU(alice, "USD", gw)
		preBobUSD := env.BalanceIOU(bob, "USD", gw)

		chkID := GetCheckIndex(bob, bobSeq)

		batchFee := CalcBatchFeeFromEnv(env, 1, 2)
		batch := NewBatchBuilder(alice, aliceSeq, batchFee, batchtx.BatchFlagIndependent).
			AddInnerTx(MakeInnerCheckCreate(bob, alice, usd10, bobSeq)).
			AddInnerTx(MakeInnerCheckCash(alice, chkID, usd10, aliceSeq+1)).
			AddSigner(bob).
			MustBuild()

		result := env.Submit(batch)
		jtx.RequireTxSuccess(t, result) // Batch itself succeeds
		env.Close()

		// Alice consumes sequences (outer + 1 inner)
		jtx.RequireSequence(t, env, alice, aliceSeq+2)

		// Bob consumes sequences (1 inner)
		jtx.RequireSequence(t, env, bob, bobSeq+1)

		// Alice pays fee only; Bob XRP unchanged
		jtx.RequireBalance(t, env, alice, preAlice-batchFee)
		jtx.RequireBalance(t, env, bob, preBob)

		// USD balances unchanged (both inner txns failed)
		require.InDelta(t, preAliceUSD, env.BalanceIOU(alice, "USD", gw), 0.001,
			"alice USD should be unchanged")
		require.InDelta(t, preBobUSD, env.BalanceIOU(bob, "USD", gw), 0.001,
			"bob USD should be unchanged")
	})
}

func TestObjectCreateTicket(t *testing.T) {
	// Create tickets inside a batch, then use a ticket for CheckCreate, then CheckCash.
	// Reference: rippled Batch_test.cpp testObjectCreateTicket()
	env := newBatchEnv(t)

	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	gw := jtx.NewAccount("gw")
	env.FundAmount(alice, uint64(jtx.XRP(10000)))
	env.FundAmount(bob, uint64(jtx.XRP(10000)))
	env.FundAmount(gw, uint64(jtx.XRP(10000)))
	env.Close()

	// Set up trust lines and issue USD
	usd10 := tx.NewIssuedAmountFromFloat64(10, "USD", gw.Address)
	usd1000 := tx.NewIssuedAmountFromFloat64(1000, "USD", gw.Address)

	env.Trust(alice, usd1000)
	env.Trust(bob, usd1000)
	env.PayIOU(gw, alice, gw, "USD", 100)
	env.PayIOU(gw, bob, gw, "USD", 100)
	env.Close()

	aliceSeq := env.Seq(alice)
	bobSeq := env.Seq(bob)
	preAlice := env.Balance(alice)
	preBob := env.Balance(bob)
	preAliceUSD := env.BalanceIOU(alice, "USD", gw)
	preBobUSD := env.BalanceIOU(bob, "USD", gw)

	// Batch with 3 inner txns:
	// 1. TicketCreate(bob, 10) using bobSeq
	// 2. CheckCreate(bob->alice, USD(10)) using ticket bobSeq+1
	// 3. CheckCash(alice, chkID, USD(10)) using aliceSeq+1
	//
	// After TicketCreate, bob's sequence advances by 10 (tickets) + 1 (for the TicketCreate).
	// The first ticket is at bobSeq+1. CheckCreate uses ticket bobSeq+1.
	// The check ID is derived from bob's account and the ticket sequence.
	chkID := GetCheckIndex(bob, bobSeq+1)

	batchFee := CalcBatchFeeFromEnv(env, 1, 3)
	batch := NewBatchBuilder(alice, aliceSeq, batchFee, batchtx.BatchFlagAllOrNothing).
		AddInnerTx(MakeInnerTicketCreate(bob, 10, bobSeq)).
		AddInnerTx(MakeInnerCheckCreateWithTicket(bob, alice, usd10, bobSeq+1)).
		AddInnerTx(MakeInnerCheckCash(alice, chkID, usd10, aliceSeq+1)).
		AddSigner(bob).
		MustBuild()

	result := env.Submit(batch)
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// Alice consumes sequences: outer + 1 inner = aliceSeq + 2
	jtx.RequireSequence(t, env, alice, aliceSeq+2)

	// Bob: TicketCreate uses seq bobSeq (sequence advances by 1 + 10 tickets = 11).
	// CheckCreate uses ticket (no sequence advancement).
	// So bob's sequence = bobSeq + 10 + 1 = bobSeq + 11
	jtx.RequireSequence(t, env, bob, bobSeq+10+1)

	// Alice pays fee; Bob XRP unchanged
	jtx.RequireBalance(t, env, alice, preAlice-batchFee)
	jtx.RequireBalance(t, env, bob, preBob)

	// Alice gains USD(10); Bob loses USD(10)
	require.InDelta(t, preAliceUSD+10.0, env.BalanceIOU(alice, "USD", gw), 0.001,
		"alice should have gained USD 10")
	require.InDelta(t, preBobUSD-10.0, env.BalanceIOU(bob, "USD", gw), 0.001,
		"bob should have lost USD 10")
}

func TestObjectCreate3rdParty(t *testing.T) {
	// Reference: rippled Batch_test.cpp testObjectCreate3rdParty()
	// Carol submits a batch containing inner transactions from alice and bob.
	// bob creates a check for alice, alice cashes it.
	// batch::sig(alice, bob) provides authorization.

	env := newBatchEnv(t)

	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	carol := jtx.NewAccount("carol")
	gw := jtx.NewAccount("gw")

	env.FundAmount(alice, uint64(jtx.XRP(10000)))
	env.FundAmount(bob, uint64(jtx.XRP(10000)))
	env.FundAmount(carol, uint64(jtx.XRP(10000)))
	env.FundAmount(gw, uint64(jtx.XRP(10000)))
	env.Close()

	// Set up trust lines and fund IOU
	env.Trust(alice, tx.NewIssuedAmountFromFloat64(1000, "USD", gw.Address))
	env.Trust(bob, tx.NewIssuedAmountFromFloat64(1000, "USD", gw.Address))
	env.PayIOU(gw, alice, gw, "USD", 100)
	env.PayIOU(gw, bob, gw, "USD", 100)
	env.Close()

	aliceSeq := env.Seq(alice)
	bobSeq := env.Seq(bob)
	carolSeq := env.Seq(carol)
	preAlice := env.Balance(alice)
	preBob := env.Balance(bob)
	preCarol := env.Balance(carol)
	preAliceUSD := env.BalanceIOU(alice, "USD", gw)
	preBobUSD := env.BalanceIOU(bob, "USD", gw)

	// Build the check ID from bob's current sequence
	chkID := GetCheckIndex(bob, bobSeq)

	batchFee := CalcBatchFeeFromEnv(env, 2, 2)
	batch := NewBatchBuilder(carol, carolSeq, batchFee, batchtx.BatchFlagAllOrNothing).
		AddInnerTx(MakeInnerCheckCreate(bob, alice, tx.NewIssuedAmountFromFloat64(10.0, "USD", gw.Address), bobSeq)).
		AddInnerTx(MakeInnerCheckCash(alice, chkID, tx.NewIssuedAmountFromFloat64(10.0, "USD", gw.Address), aliceSeq)).
		AddSigner(alice).
		AddSigner(bob).
		MustBuild()

	result := env.Submit(batch)
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// Verify sequences advanced
	jtx.RequireSequence(t, env, alice, aliceSeq+1)
	jtx.RequireSequence(t, env, bob, bobSeq+1)
	jtx.RequireSequence(t, env, carol, carolSeq+1)

	// Verify XRP balances: alice and bob unchanged, carol pays fee
	jtx.RequireBalance(t, env, alice, preAlice)
	jtx.RequireBalance(t, env, bob, preBob)
	jtx.RequireBalance(t, env, carol, preCarol-batchFee)

	// Verify IOU balances: alice gains USD(10), bob loses USD(10)
	require.InDelta(t, preAliceUSD+10.0, env.BalanceIOU(alice, "USD", gw), 0.001,
		"alice should have gained USD 10")
	require.InDelta(t, preBobUSD-10.0, env.BalanceIOU(bob, "USD", gw), 0.001,
		"bob should have lost USD 10")
}

func TestBatchCalculateBaseFee(t *testing.T) {
	t.Run("too many txns returns error fee", func(t *testing.T) {
		env := newBatchEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		env.Fund(alice, bob)
		env.Close()

		// 9 inner txns should exceed max (8)
		seq := env.Seq(alice)
		batchFee := CalcBatchFeeFromEnv(env, 0, 9)
		builder := NewBatchBuilder(alice, seq, batchFee, batchtx.BatchFlagAllOrNothing)
		for i := range 9 {
			builder.AddInnerTx(MakeFakeInnerTx(uint32(i + 1)))
		}
		batch := builder.MustBuild()

		// Should fail validation
		err := batch.Validate()
		require.Error(t, err)
		require.Contains(t, err.Error(), "exceeds 8")
	})

	t.Run("too many signers returns error fee", func(t *testing.T) {
		env := newBatchEnv(t)
		alice := jtx.NewAccount("alice")
		env.Fund(alice)
		env.Close()

		bob := jtx.NewAccount("bob")
		seq := env.Seq(alice)
		batchFee := CalcBatchFeeFromEnv(env, 9, 2)
		builder := NewBatchBuilder(alice, seq, batchFee, batchtx.BatchFlagAllOrNothing).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 1, seq+1)).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 1, seq+2))
		for i := range 9 {
			signer := jtx.NewAccount(fmt.Sprintf("signer%d", i))
			builder.AddSigner(signer)
		}
		batch := builder.MustBuild()

		err := batch.Validate()
		require.Error(t, err)
		require.Contains(t, err.Error(), "exceeds 8")
	})

	t.Run("valid batch fee calculation", func(t *testing.T) {
		env := newBatchEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		env.Fund(alice, bob)
		env.Close()

		seq := env.Seq(alice)
		batchFee := CalcBatchFeeFromEnv(env, 0, 2) // = 40 with base fee 10
		batch := NewBatchBuilder(alice, seq, batchFee, batchtx.BatchFlagAllOrNothing).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 1, seq+1)).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 2, seq+2)).
			MustBuild()

		err := batch.Validate()
		require.NoError(t, err)

		// Verify fee is correct
		require.Equal(t, fmt.Sprintf("%d", batchFee), batch.Fee)
	})
}

// Batch signature verification vectors
// These exercise serializeBatch digest verification and the requiredSigners
// coverage rule, which run in preflight (Batch.Validate).
