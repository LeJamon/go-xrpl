package batch

import (
	"fmt"
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	batchtx "github.com/LeJamon/go-xrpl/internal/tx/batch"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

func TestBatchSigningVectors(t *testing.T) {
	t.Run("temBAD_SIGNER - foreign inner with no batch signers", func(t *testing.T) {
		env := newBatchEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		env.Fund(alice, bob)
		env.Close()

		seq := env.Seq(alice)
		batchFee := CalcBatchFeeFromEnv(env, 0, 2)
		batch := NewBatchBuilder(alice, seq, batchFee, batchtx.BatchFlagAllOrNothing).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 10, seq+1)).
			AddInnerTx(MakeInnerPaymentXRP(bob, alice, 5, env.Seq(bob))).
			MustBuild()

		result := env.Submit(batch)
		jtx.RequireTxFail(t, result, "temBAD_SIGNER")
	})

	t.Run("temBAD_SIGNER - stray signer, no inner requires it", func(t *testing.T) {
		env := newBatchEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		env.Fund(alice, bob)
		env.Close()

		seq := env.Seq(alice)
		batchFee := CalcBatchFeeFromEnv(env, 1, 2)
		batch := NewBatchBuilder(alice, seq, batchFee, batchtx.BatchFlagAllOrNothing).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 10, seq+1)).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 5, seq+2)).
			AddSigner(bob).
			MustBuild()

		result := env.Submit(batch)
		jtx.RequireTxFail(t, result, "temBAD_SIGNER")
	})

	t.Run("temBAD_SIGNER - wrong signer for required inner account", func(t *testing.T) {
		env := newBatchEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		carol := jtx.NewAccount("carol")
		env.Fund(alice, bob, carol)
		env.Close()

		seq := env.Seq(alice)
		batchFee := CalcBatchFeeFromEnv(env, 1, 2)
		batch := NewBatchBuilder(alice, seq, batchFee, batchtx.BatchFlagAllOrNothing).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 10, seq+1)).
			AddInnerTx(MakeInnerPaymentXRP(bob, alice, 5, env.Seq(bob))).
			AddSigner(carol).
			MustBuild()

		result := env.Submit(batch)
		jtx.RequireTxFail(t, result, "temBAD_SIGNER")
	})

	t.Run("temBAD_SIGNER - required inner account uncovered", func(t *testing.T) {
		env := newBatchEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		carol := jtx.NewAccount("carol")
		env.Fund(alice, bob, carol)
		env.Close()

		seq := env.Seq(alice)
		batchFee := CalcBatchFeeFromEnv(env, 2, 3)
		batch := NewBatchBuilder(alice, seq, batchFee, batchtx.BatchFlagAllOrNothing).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 10, seq+1)).
			AddInnerTx(MakeInnerPaymentXRP(bob, alice, 5, env.Seq(bob))).
			AddInnerTx(MakeInnerPaymentXRP(carol, alice, 5, env.Seq(carol))).
			AddSigner(bob).
			MustBuild()

		result := env.Submit(batch)
		jtx.RequireTxFail(t, result, "temBAD_SIGNER")
	})

	// temBAD_SIGNATURE: signer Account is bob (required) and the signature is made
	// with bob's key, but the presented SigningPubKey is alice's, so the signature
	// fails to verify against it (Batch_test.cpp:555-579).
	// The cryptographic BatchSigner check runs in the engine's signature stage, so
	// it is exercised with VerifySignatures enabled (the outer batch is signed by
	// alice). This mirrors rippled, where checkBatchSign rejects in preflight before
	// the preclaim authorization check is reached.
	t.Run("temBAD_SIGNATURE - signature key mismatched to signing pubkey", func(t *testing.T) {
		env := newBatchEnv(t)
		env.VerifySignatures = true
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		env.Fund(alice, bob)
		env.Close()

		seq := env.Seq(alice)
		batchFee := CalcBatchFeeFromEnv(env, 1, 2)
		batch := NewBatchBuilder(alice, seq, batchFee, batchtx.BatchFlagAllOrNothing).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 10, seq+1)).
			AddInnerTx(MakeInnerPaymentXRP(bob, alice, 5, env.Seq(bob))).
			AddMismatchedSigner(bob, alice, bob).
			MustBuild()

		result := env.SubmitSigned(batch)
		jtx.RequireTxFail(t, result, "temINVALID")
	})

	t.Run("temBAD_SIGNATURE - garbage signature", func(t *testing.T) {
		env := newBatchEnv(t)
		env.VerifySignatures = true
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		env.Fund(alice, bob)
		env.Close()

		seq := env.Seq(alice)
		batchFee := CalcBatchFeeFromEnv(env, 1, 2)
		batch := NewBatchBuilder(alice, seq, batchFee, batchtx.BatchFlagAllOrNothing).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 10, seq+1)).
			AddInnerTx(MakeInnerPaymentXRP(bob, alice, 5, env.Seq(bob))).
			AddGarbageSigner(bob).
			MustBuild()

		result := env.SubmitSigned(batch)
		jtx.RequireTxFail(t, result, "temINVALID")
	})

	t.Run("tesSUCCESS - valid single-signed batch signer", func(t *testing.T) {
		env := newBatchEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		env.Fund(alice, bob)
		env.Close()

		seq := env.Seq(alice)
		batchFee := CalcBatchFeeFromEnv(env, 1, 2)
		batch := NewBatchBuilder(alice, seq, batchFee, batchtx.BatchFlagAllOrNothing).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 1, seq+1)).
			AddInnerTx(MakeInnerPaymentXRP(bob, alice, 2, env.Seq(bob))).
			AddSigner(bob).
			MustBuild()

		result := env.Submit(batch)
		jtx.RequireTxSuccess(t, result)
	})

	t.Run("tesSUCCESS - valid single-signed batch signer (verified)", func(t *testing.T) {
		env := newBatchEnv(t)
		env.VerifySignatures = true
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		env.Fund(alice, bob)
		env.Close()

		seq := env.Seq(alice)
		batchFee := CalcBatchFeeFromEnv(env, 1, 2)
		batch := NewBatchBuilder(alice, seq, batchFee, batchtx.BatchFlagAllOrNothing).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 1, seq+1)).
			AddInnerTx(MakeInnerPaymentXRP(bob, alice, 2, env.Seq(bob))).
			AddSigner(bob).
			MustBuild()

		result := env.SubmitSigned(batch)
		jtx.RequireTxSuccess(t, result)
	})

	t.Run("tesSUCCESS - valid multi-signed batch signer", func(t *testing.T) {
		env := newBatchEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		carol := jtx.NewAccount("carol")
		dave := jtx.NewAccount("dave")
		env.Fund(alice, bob, carol, dave)
		env.Close()

		env.SetSignerList(bob, 2, []jtx.TestSigner{
			{Account: carol, Weight: 1},
			{Account: dave, Weight: 1},
		})
		env.Close()

		seq := env.Seq(alice)
		batchFee := CalcBatchFeeFromEnv(env, 3, 2)
		batch := NewBatchBuilder(alice, seq, batchFee, batchtx.BatchFlagAllOrNothing).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 10, seq+1)).
			AddInnerTx(MakeInnerPaymentXRP(bob, alice, 5, env.Seq(bob))).
			AddMultiSignBatchSigner(bob, []*jtx.Account{carol, dave}).
			MustBuild()

		result := env.Submit(batch)
		jtx.RequireTxSuccess(t, result)
	})

	t.Run("tesSUCCESS - valid multi-signed batch signer (verified)", func(t *testing.T) {
		env := newBatchEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		carol := jtx.NewAccount("carol")
		dave := jtx.NewAccount("dave")
		env.Fund(alice, bob, carol, dave)
		env.Close()

		// Establish bob's signer list before enabling signature verification, since
		// the SetSignerList helper submits an unsigned SignerListSet.
		env.SetSignerList(bob, 2, []jtx.TestSigner{
			{Account: carol, Weight: 1},
			{Account: dave, Weight: 1},
		})
		env.Close()
		env.VerifySignatures = true

		seq := env.Seq(alice)
		batchFee := CalcBatchFeeFromEnv(env, 3, 2)
		batch := NewBatchBuilder(alice, seq, batchFee, batchtx.BatchFlagAllOrNothing).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 10, seq+1)).
			AddInnerTx(MakeInnerPaymentXRP(bob, alice, 5, env.Seq(bob))).
			AddMultiSignBatchSigner(bob, []*jtx.Account{carol, dave}).
			MustBuild()

		result := env.SubmitSigned(batch)
		jtx.RequireTxSuccess(t, result)
	})

	t.Run("tesSUCCESS - single-account batch needs no signers", func(t *testing.T) {
		env := newBatchEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		env.Fund(alice, bob)
		env.Close()

		seq := env.Seq(alice)
		batchFee := CalcBatchFeeFromEnv(env, 0, 2)
		batch := NewBatchBuilder(alice, seq, batchFee, batchtx.BatchFlagAllOrNothing).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 1, seq+1)).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 2, seq+2)).
			MustBuild()

		result := env.Submit(batch)
		jtx.RequireTxSuccess(t, result)
	})
}

// TestBatchSignerArrayBound exercises the 32-entry upper bound on a
// multi-signed BatchSigner's nested Signers array. ExpandedSignerList is
// retired in rippled v3.2.0, so the legacy eight-entry regime is no longer
// reachable. An over-bound array surfaces as temBAD_SIGNATURE at the
// checkBatchSign call site.
// Reference: rippled STTx.h kMaxMultiSigners and STTx.cpp multiSignHelper.
func TestBatchSignerArrayBound(t *testing.T) {
	makeNestedSigners := func(env *jtx.TestEnv, n int) []*jtx.Account {
		signers := make([]*jtx.Account, n)
		for i := range n {
			s := jtx.NewAccount(fmt.Sprintf("nsigner%d", i))
			env.FundAmount(s, uint64(jtx.XRP(1000)))
			signers[i] = s
		}
		return signers
	}

	buildBatch := func(env *jtx.TestEnv, alice, bob *jtx.Account, signers []*jtx.Account) *batchtx.Batch {
		seq := env.Seq(alice)
		batchFee := CalcBatchFeeFromEnv(env, uint32(len(signers)), 2)
		return NewBatchBuilder(alice, seq, batchFee, batchtx.BatchFlagAllOrNothing).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 10, seq+1)).
			AddInnerTx(MakeInnerPaymentXRP(bob, alice, 5, env.Seq(bob))).
			AddMultiSignBatchSigner(bob, signers).
			MustBuild()
	}

	t.Run("32 nested signers accepted", func(t *testing.T) {
		env := newBatchEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		env.FundAmount(alice, uint64(jtx.XRP(10000)))
		env.FundAmount(bob, uint64(jtx.XRP(10000)))
		env.Close()

		signers := makeNestedSigners(env, 32)
		env.Close()
		list := make([]jtx.TestSigner, len(signers))
		for i, signer := range signers {
			list[i] = jtx.TestSigner{Account: signer, Weight: 1}
		}
		env.SetSignerList(bob, uint32(len(list)), list)
		env.Close()

		batch := buildBatch(env, alice, bob, signers)
		result := env.Submit(batch)
		jtx.RequireTxSuccess(t, result)
		env.Close()
		requireBatchLedgerData(t, env, batch, result, ter.TesSUCCESS, ter.TesSUCCESS)
	})

	t.Run("33 nested signers rejected", func(t *testing.T) {
		env := newBatchEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		env.FundAmount(alice, uint64(jtx.XRP(10000)))
		env.FundAmount(bob, uint64(jtx.XRP(10000)))
		env.Close()

		batch := buildBatch(env, alice, bob, makeNestedSigners(env, 33))
		env.Close()

		result := env.Submit(batch)
		jtx.RequireTxFail(t, result, "temINVALID")
	})
}
