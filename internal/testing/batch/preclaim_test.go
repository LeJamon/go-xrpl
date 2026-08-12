package batch

import (
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	batchtx "github.com/LeJamon/go-xrpl/internal/tx/batch"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

func TestPreclaim(t *testing.T) {
	t.Run("checkSign.checkSingleSign/tefBAD_AUTH", func(t *testing.T) {
		env := newBatchEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		env.Fund(alice, bob)
		env.Close()

		seq := env.Seq(alice)
		batch := NewBatchBuilder(alice, seq, CalcBatchFeeFromEnv(env, 0, 2), batchtx.BatchFlagAllOrNothing).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 10, seq+1)).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 20, seq+2)).
			MustBuild()
		result := env.SubmitSignedWith(batch, bob)
		jtx.RequireTxFail(t, result, "tefBAD_AUTH")
		jtx.RequireSequence(t, env, alice, seq)
	})

	t.Run("checkSign.multisign/tesSUCCESS_with_nested_BatchSigners", func(t *testing.T) {
		env := newBatchEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		carol := jtx.NewAccount("carol")
		dave := jtx.NewAccount("dave")
		env.Fund(alice, bob, carol, dave)
		env.Close()
		env.SetSignerList(alice, 2, []jtx.TestSigner{{Account: bob, Weight: 1}, {Account: carol, Weight: 1}})
		env.SetSignerList(bob, 2, []jtx.TestSigner{{Account: carol, Weight: 1}, {Account: dave, Weight: 1}})
		env.Close()

		aliceSeq := env.Seq(alice)
		bobSeq := env.Seq(bob)
		batch := NewBatchBuilder(alice, aliceSeq, CalcBatchFeeFromEnv(env, 4, 2), batchtx.BatchFlagAllOrNothing).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 10, aliceSeq+1)).
			AddInnerTx(MakeInnerPaymentXRP(bob, alice, 5, bobSeq)).
			AddMultiSignBatchSigner(bob, []*jtx.Account{carol, dave}).
			MustBuild()
		result := env.SubmitMultiSigned(batch, []*jtx.Account{bob, carol})
		jtx.RequireTxSuccess(t, result)
		env.Close()
		requireBatchLedgerData(t, env, batch, result, ter.TesSUCCESS, ter.TesSUCCESS)
	})

	// The remaining cases share state in the same order as the protocol scenario.

	env := newBatchEnv(t)

	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	carol := jtx.NewAccount("carol")
	dave := jtx.NewAccount("dave")
	elsa := jtx.NewAccount("elsa")
	frank := jtx.NewAccount("frank")
	phantom := jtx.NewAccount("phantom") // not funded — phantom account

	env.FundAmount(alice, uint64(jtx.XRP(10000)))
	env.FundAmount(bob, uint64(jtx.XRP(10000)))
	env.FundAmount(carol, uint64(jtx.XRP(10000)))
	env.FundAmount(dave, uint64(jtx.XRP(10000)))
	env.FundAmount(elsa, uint64(jtx.XRP(10000)))
	env.FundAmount(frank, uint64(jtx.XRP(10000)))
	env.Close()

	// tefNOT_MULTI_SIGNING: SignersList not enabled for bob
	t.Run("checkBatchSign.checkMultiSign/tefNOT_MULTI_SIGNING", func(t *testing.T) {
		seq := env.Seq(alice)
		batchFee := CalcBatchFeeFromEnv(env, 3, 2)
		batch := NewBatchBuilder(alice, seq, batchFee, batchtx.BatchFlagAllOrNothing).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 10, seq+1)).
			AddInnerTx(MakeInnerPaymentXRP(bob, alice, 5, env.Seq(bob))).
			AddMultiSignBatchSigner(bob, []*jtx.Account{dave, carol}).
			MustBuild()
		result := env.Submit(batch)
		jtx.RequireTxFail(t, result, "tefNOT_MULTI_SIGNING")
		env.Close()
	})

	// Set up signer lists for alice and bob
	// alice: quorum=2, signers={bob:1, carol:1}
	env.SetSignerList(alice, 2, []jtx.TestSigner{
		{Account: bob, Weight: 1},
		{Account: carol, Weight: 1},
	})
	env.Close()

	// bob: quorum=2, signers={carol:1, dave:1, elsa:1}
	env.SetSignerList(bob, 2, []jtx.TestSigner{
		{Account: carol, Weight: 1},
		{Account: dave, Weight: 1},
		{Account: elsa, Weight: 1},
	})
	env.Close()

	// tefBAD_SIGNATURE: Account not in SignersList (frank not in bob's list)
	t.Run("checkBatchSign.checkMultiSign/tefBAD_SIGNATURE_not_in_list", func(t *testing.T) {
		seq := env.Seq(alice)
		batchFee := CalcBatchFeeFromEnv(env, 3, 2)
		batch := NewBatchBuilder(alice, seq, batchFee, batchtx.BatchFlagAllOrNothing).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 10, seq+1)).
			AddInnerTx(MakeInnerPaymentXRP(bob, alice, 5, env.Seq(bob))).
			AddMultiSignBatchSigner(bob, []*jtx.Account{carol, frank}).
			MustBuild()
		result := env.Submit(batch)
		jtx.RequireTxFail(t, result, "tefBAD_SIGNATURE")
		env.Close()
	})

	// tefBAD_SIGNATURE: Wrong publicKey type (ed25519 dave not in signer list)
	t.Run("checkBatchSign.checkMultiSign/tefBAD_SIGNATURE_wrong_key_type", func(t *testing.T) {
		daveEd := jtx.NewAccountWithKeyType("dave", jtx.KeyTypeEd25519)
		seq := env.Seq(alice)
		batchFee := CalcBatchFeeFromEnv(env, 3, 2)
		batch := NewBatchBuilder(alice, seq, batchFee, batchtx.BatchFlagAllOrNothing).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 10, seq+1)).
			AddInnerTx(MakeInnerPaymentXRP(bob, alice, 5, env.Seq(bob))).
			AddMultiSignBatchSigner(bob, []*jtx.Account{carol, daveEd}).
			MustBuild()
		result := env.Submit(batch)
		jtx.RequireTxFail(t, result, "tefBAD_SIGNATURE")
		env.Close()
	})

	// tefMASTER_DISABLED: elsa has master disabled
	env.SetRegularKey(elsa, frank)
	env.DisableMasterKey(elsa)
	t.Run("checkBatchSign.checkMultiSign/tefMASTER_DISABLED", func(t *testing.T) {
		seq := env.Seq(alice)
		batchFee := CalcBatchFeeFromEnv(env, 3, 2)
		batch := NewBatchBuilder(alice, seq, batchFee, batchtx.BatchFlagAllOrNothing).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 10, seq+1)).
			AddInnerTx(MakeInnerPaymentXRP(bob, alice, 5, env.Seq(bob))).
			AddMultiSignBatchSigner(bob, []*jtx.Account{carol, elsa}).
			MustBuild()
		result := env.Submit(batch)
		jtx.RequireTxFail(t, result, "tefMASTER_DISABLED")
		env.Close()
	})

	// tefBAD_SIGNATURE: Signer does not exist (phantom not in ledger, not in signer list)
	t.Run("checkBatchSign.checkMultiSign/tefBAD_SIGNATURE_phantom", func(t *testing.T) {
		seq := env.Seq(alice)
		batchFee := CalcBatchFeeFromEnv(env, 3, 2)
		batch := NewBatchBuilder(alice, seq, batchFee, batchtx.BatchFlagAllOrNothing).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 10, seq+1)).
			AddInnerTx(MakeInnerPaymentXRP(bob, alice, 5, env.Seq(bob))).
			AddMultiSignBatchSigner(bob, []*jtx.Account{carol, phantom}).
			MustBuild()
		result := env.Submit(batch)
		jtx.RequireTxFail(t, result, "tefBAD_SIGNATURE")
		env.Close()
	})

	// tefBAD_SIGNATURE: Signer has not enabled RegularKey
	// dave signs with davo (ed25519) key, but dave has no regular key set
	t.Run("checkBatchSign.checkMultiSign/tefBAD_SIGNATURE_no_regkey", func(t *testing.T) {
		davo := jtx.NewAccountWithKeyType("davo", jtx.KeyTypeEd25519)
		seq := env.Seq(alice)
		batchFee := CalcBatchFeeFromEnv(env, 3, 2)
		batch := NewBatchBuilder(alice, seq, batchFee, batchtx.BatchFlagAllOrNothing).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 10, seq+1)).
			AddInnerTx(MakeInnerPaymentXRP(bob, alice, 5, env.Seq(bob))).
			AddMultiSignBatchSignerWithRegKeys(bob, []RegKeySigner{
				{Account: carol, SigningKey: carol}, // carol signs with own key
				{Account: dave, SigningKey: davo},   // dave signs with davo's key (no regkey set)
			}).
			MustBuild()
		result := env.Submit(batch)
		jtx.RequireTxFail(t, result, "tefBAD_SIGNATURE")
		env.Close()
	})

	// tefBAD_SIGNATURE: Wrong RegularKey Set
	// dave's regular key is frank, but trying to sign with davo
	env.SetRegularKey(dave, frank)
	t.Run("checkBatchSign.checkMultiSign/tefBAD_SIGNATURE_wrong_regkey", func(t *testing.T) {
		davo := jtx.NewAccountWithKeyType("davo", jtx.KeyTypeEd25519)
		seq := env.Seq(alice)
		batchFee := CalcBatchFeeFromEnv(env, 3, 2)
		batch := NewBatchBuilder(alice, seq, batchFee, batchtx.BatchFlagAllOrNothing).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 10, seq+1)).
			AddInnerTx(MakeInnerPaymentXRP(bob, alice, 5, env.Seq(bob))).
			AddMultiSignBatchSignerWithRegKeys(bob, []RegKeySigner{
				{Account: carol, SigningKey: carol},
				{Account: dave, SigningKey: davo}, // davo != frank (dave's regular key)
			}).
			MustBuild()
		result := env.Submit(batch)
		jtx.RequireTxFail(t, result, "tefBAD_SIGNATURE")
		env.Close()
	})

	// tefBAD_QUORUM: Only carol signs (weight 1), quorum is 2
	t.Run("checkBatchSign.checkMultiSign/tefBAD_QUORUM", func(t *testing.T) {
		seq := env.Seq(alice)
		batchFee := CalcBatchFeeFromEnv(env, 2, 2)
		batch := NewBatchBuilder(alice, seq, batchFee, batchtx.BatchFlagAllOrNothing).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 10, seq+1)).
			AddInnerTx(MakeInnerPaymentXRP(bob, alice, 5, env.Seq(bob))).
			AddMultiSignBatchSigner(bob, []*jtx.Account{carol}).
			MustBuild()
		result := env.Submit(batch)
		jtx.RequireTxFail(t, result, "tefBAD_QUORUM")
		env.Close()
	})

	// tesSUCCESS: BatchSigners.Signers with carol + dave (weight 2, quorum 2)
	t.Run("checkBatchSign.checkMultiSign/tesSUCCESS", func(t *testing.T) {
		seq := env.Seq(alice)
		batchFee := CalcBatchFeeFromEnv(env, 3, 2)
		batch := NewBatchBuilder(alice, seq, batchFee, batchtx.BatchFlagAllOrNothing).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 10, seq+1)).
			AddInnerTx(MakeInnerPaymentXRP(bob, alice, 5, env.Seq(bob))).
			AddMultiSignBatchSigner(bob, []*jtx.Account{carol, dave}).
			MustBuild()
		result := env.Submit(batch)
		jtx.RequireTxSuccess(t, result)
		env.Close()
	})

	// tefBAD_AUTH: Inner Account (phantom) is not a signer — phantom doesn't exist and
	// carol's pubkey doesn't derive to phantom's address
	t.Run("checkBatchSign.checkSingleSign/tefBAD_AUTH_phantom", func(t *testing.T) {
		seq := env.Seq(alice)
		batchFee := CalcBatchFeeFromEnv(env, 1, 2)
		batch := NewBatchBuilder(alice, seq, batchFee, batchtx.BatchFlagAllOrNothing).
			AddInnerTx(MakeInnerPaymentXRP(alice, phantom, 1000, seq+1)).
			AddInnerTx(MakeInnerAccountSet(phantom, env.LedgerSeq())).
			AddSignerWithRegKey(phantom, carol).
			MustBuild()
		result := env.Submit(batch)
		jtx.RequireTxFail(t, result, "tefBAD_AUTH")
		env.Close()
	})

	// Bob has not authorized carol as a regular key.
	t.Run("checkBatchSign.checkSingleSign/tefBAD_AUTH_not_signer", func(t *testing.T) {
		seq := env.Seq(alice)
		batchFee := CalcBatchFeeFromEnv(env, 1, 2)
		batch := NewBatchBuilder(alice, seq, batchFee, batchtx.BatchFlagAllOrNothing).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 1000, seq+1)).
			AddInnerTx(MakeInnerAccountSet(bob, env.LedgerSeq())).
			AddSignerWithRegKey(bob, carol).
			MustBuild()
		result := env.Submit(batch)
		jtx.RequireTxFail(t, result, "tefBAD_AUTH")
		env.Close()
	})

	// tesSUCCESS: Signed With Regular Key
	env.SetRegularKey(bob, carol)
	t.Run("checkBatchSign.checkSingleSign/tesSUCCESS_regular_key", func(t *testing.T) {
		seq := env.Seq(alice)
		batchFee := CalcBatchFeeFromEnv(env, 1, 2)
		batch := NewBatchBuilder(alice, seq, batchFee, batchtx.BatchFlagAllOrNothing).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 1, seq+1)).
			AddInnerTx(MakeInnerPaymentXRP(bob, alice, 2, env.Seq(bob))).
			AddSignerWithRegKey(bob, carol).
			MustBuild()
		result := env.Submit(batch)
		jtx.RequireTxSuccess(t, result)
		env.Close()
	})

	// tesSUCCESS: Signed With Master Key
	t.Run("checkBatchSign.checkSingleSign/tesSUCCESS_master_key", func(t *testing.T) {
		seq := env.Seq(alice)
		batchFee := CalcBatchFeeFromEnv(env, 1, 2)
		batch := NewBatchBuilder(alice, seq, batchFee, batchtx.BatchFlagAllOrNothing).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 1, seq+1)).
			AddInnerTx(MakeInnerPaymentXRP(bob, alice, 2, env.Seq(bob))).
			AddSigner(bob).
			MustBuild()
		result := env.Submit(batch)
		jtx.RequireTxSuccess(t, result)
		env.Close()
	})

	// tefMASTER_DISABLED: Signed With Master Key Disabled
	env.SetRegularKey(bob, carol)
	env.DisableMasterKey(bob)
	t.Run("checkBatchSign.checkSingleSign/tefMASTER_DISABLED", func(t *testing.T) {
		seq := env.Seq(alice)
		batchFee := CalcBatchFeeFromEnv(env, 1, 2)
		batch := NewBatchBuilder(alice, seq, batchFee, batchtx.BatchFlagAllOrNothing).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 1, seq+1)).
			AddInnerTx(MakeInnerPaymentXRP(bob, alice, 2, env.Seq(bob))).
			AddSigner(bob).
			MustBuild()
		result := env.Submit(batch)
		jtx.RequireTxFail(t, result, "tefMASTER_DISABLED")
		env.Close()
	})
}
