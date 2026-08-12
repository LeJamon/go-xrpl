// Package batch provides integration tests for Batch transactions.
// Test structure mirrors rippled's Batch_test.cpp 1:1.
// Reference: rippled/src/test/app/Batch_test.cpp
package batch

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/tx"
	batchtx "github.com/LeJamon/go-xrpl/internal/tx/batch"
	"github.com/LeJamon/go-xrpl/internal/tx/payment"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
)

// newBatchEnv builds a test environment with the Batch amendment active.
// Batch is Supported::no upstream (rippled 3.1.1 withdrew support pending
// fixBatchInnerSigs), so the all-supported preset no longer activates it and
// every Batch test must opt in. EnableFeatureNow enables it from genesis,
// matching the behaviour these tests relied on when the preset carried Batch.
func newBatchEnv(t *testing.T) *jtx.TestEnv {
	t.Helper()
	env := jtx.NewTestEnv(t)
	env.EnableFeatureNow("Batch")
	return env
}

func requireBatchLedgerData(
	t *testing.T,
	env *jtx.TestEnv,
	batch *batchtx.Batch,
	result jtx.TxResult,
	expectedResults ...ter.Result,
) {
	t.Helper()
	require.NotNil(t, result.Metadata)
	require.Empty(t, result.AppliedInnerTransactions)

	batchID, err := tx.ComputeTransactionHash(batch)
	require.NoError(t, err)
	closed := env.LastClosedLedger()
	require.Equal(t, uint32(1+len(expectedResults)), closed.TxCount())
	outerStored, outerExists, err := closed.GetTransaction(batchID)
	require.NoError(t, err)
	require.True(t, outerExists)
	outerTxn, outerMeta, err := tx.SplitTxWithMetaBlobStrict(outerStored)
	require.NoError(t, err)
	decodedOuterTxn, err := binarycodec.DecodeBytes(outerTxn)
	require.NoError(t, err)
	require.Equal(t, tx.TypeBatch.String(), decodedOuterTxn["TransactionType"])
	decodedOuterMeta, err := binarycodec.DecodeBytes(outerMeta)
	require.NoError(t, err)
	require.Equal(t, ter.TesSUCCESS.String(), decodedOuterMeta["TransactionResult"])
	outerIndex, ok := decodedOuterMeta["TransactionIndex"].(uint32)
	require.True(t, ok)

	for i, expectedResult := range expectedResults {
		expectedTxn := batch.RawTransactions[i].RawTransaction.InnerTx
		expectedID, hashErr := tx.ComputeTransactionHash(expectedTxn)
		require.NoError(t, hashErr)

		stored, found, getErr := closed.GetTransaction(expectedID)
		require.NoError(t, getErr)
		require.True(t, found)
		storedTxn, storedMeta, splitErr := tx.SplitTxWithMetaBlobStrict(stored)
		require.NoError(t, splitErr)
		decodedTxn, decodeErr := binarycodec.DecodeBytes(storedTxn)
		require.NoError(t, decodeErr)
		require.Equal(t, expectedTxn.TxType().String(), decodedTxn["TransactionType"])
		decodedMeta, decodeErr := binarycodec.DecodeBytes(storedMeta)
		require.NoError(t, decodeErr)
		require.Equal(t, expectedResult.String(), decodedMeta["TransactionResult"])
		require.EqualValues(t, outerIndex+uint32(i)+1, decodedMeta["TransactionIndex"])
		require.Equal(t, fmt.Sprintf("%X", batchID), decodedMeta["ParentBatchID"])
	}

	for i := len(expectedResults); i < len(batch.RawTransactions); i++ {
		inner := batch.RawTransactions[i].RawTransaction.InnerTx
		innerID, hashErr := tx.ComputeTransactionHash(inner)
		require.NoError(t, hashErr)
		exists, existsErr := closed.TxExists(innerID)
		require.NoError(t, existsErr)
		require.False(t, exists, "uncommitted inner transaction %d persisted", i)
	}
}

func TestInnerSetRegularKeyDoesNotReceiveFreePasswordChange(t *testing.T) {
	env := newBatchEnv(t)
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	regularKey := jtx.NewAccount("regular-key")
	env.Fund(alice, bob)

	seq := env.Seq(alice)
	setRegularKey := jtx.NewSetRegularKeyTx(alice, regularKey)
	setRegularKey.GetCommon().Fee = "0"
	setRegularKey.GetCommon().SigningPubKey = ""
	setRegularKey.GetCommon().SetSequence(seq + 1)
	setRegularKey.GetCommon().SetFlags(tx.TfInnerBatchTxn)

	batch := NewBatchBuilder(alice, seq, CalcBatchFeeFromEnv(env, 0, 2), batchtx.BatchFlagAllOrNothing).
		AddInnerTx(setRegularKey).
		AddInnerTx(MakeInnerPaymentXRP(alice, bob, 1, seq+2)).
		MustBuild()
	jtx.RequireTxSuccess(t, env.Submit(batch))
	env.Close()

	data, err := env.Ledger().Read(keylet.Account(alice.ID))
	require.NoError(t, err)
	account, err := state.ParseAccountRoot(data)
	require.NoError(t, err)
	require.Equal(t, regularKey.Address, account.RegularKey)
	require.Zero(t, account.Flags&state.LsfPasswordSpent)
}

func TestEnabled(t *testing.T) {
	t.Run("batch enabled", func(t *testing.T) {
		env := newBatchEnv(t)

		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		env.Fund(alice, bob)
		env.Close()

		// Submit a valid batch with feature enabled - should succeed
		seq := env.Seq(alice)
		batchFee := CalcBatchFeeFromEnv(env, 0, 2)
		batch := NewBatchBuilder(alice, seq, batchFee, batchtx.BatchFlagAllOrNothing).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 1, seq+1)).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 1, seq+2)).
			MustBuild()

		result := env.Submit(batch)
		jtx.RequireTxSuccess(t, result)
		env.Close()
	})

	t.Run("batch disabled", func(t *testing.T) {
		env := jtx.NewTestEnv(t)

		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		env.Fund(alice, bob)
		env.Close()

		// Submit a batch with feature disabled - should fail with temDISABLED
		seq := env.Seq(alice)
		batchFee := CalcBatchFeeFromEnv(env, 0, 2)
		batch := NewBatchBuilder(alice, seq, batchFee, batchtx.BatchFlagAllOrNothing).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 1, seq+1)).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 1, seq+2)).
			MustBuild()

		result := env.Submit(batch)
		jtx.RequireTxFail(t, result, "temDISABLED")
	})

	t.Run("tfInnerBatchTxn on non-batch tx - feature enabled", func(t *testing.T) {
		env := newBatchEnv(t)

		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		env.Fund(alice, bob)
		env.Close()

		// A directly-submitted inner-flagged transaction (no signature) reaches
		// the engine and fails with temINVALID_FLAG. Reference: rippled
		// checkValidity short-circuits it to Valid before fixBatchInnerSigs, so
		// it reaches the engine (Batch_test.cpp doTestInnerSubmitRPC).
		p := MakeInnerPayment(alice, bob, jtx.XRP(1), env.Seq(alice))
		p.Fee = fmt.Sprintf("%d", env.BaseFee())
		p.SigningPubKey = "" // inner batch format, but submitted directly

		result := env.SubmitWithOptions(p, jtx.SubmitOptions{SkipSignature: true})
		jtx.RequireTxFail(t, result, "temINVALID_FLAG")
	})

	t.Run("tfInnerBatchTxn on non-batch tx - fixBatchInnerSigs enabled", func(t *testing.T) {
		env := newBatchEnv(t)
		env.EnableFeatureNow("fixBatchInnerSigs")

		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		env.Fund(alice, bob)
		env.Close()

		// With fixBatchInnerSigs an inner-flagged transaction never has a valid
		// signature, so it is rejected as invalid rather than reaching the
		// engine. Reference: rippled apply.cpp checkValidity (PR #6069).
		p := MakeInnerPayment(alice, bob, jtx.XRP(1), env.Seq(alice))
		p.Fee = fmt.Sprintf("%d", env.BaseFee())
		p.SigningPubKey = ""

		result := env.SubmitWithOptions(p, jtx.SubmitOptions{SkipSignature: true})
		jtx.RequireTxFail(t, result, "temINVALID")
	})

	t.Run("tfInnerBatchTxn on non-batch tx - feature disabled", func(t *testing.T) {
		env := jtx.NewTestEnv(t)

		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		env.Fund(alice, bob)
		env.Close()

		// A regular payment with tfInnerBatchTxn should fail with temINVALID_FLAG
		p := MakeInnerPayment(alice, bob, jtx.XRP(1), env.Seq(alice))
		p.Fee = fmt.Sprintf("%d", env.BaseFee())
		p.SigningPubKey = ""

		result := env.SubmitWithOptions(p, jtx.SubmitOptions{SkipSignature: true})
		jtx.RequireTxFail(t, result, "temINVALID_FLAG")
	})
}

func TestPreflight(t *testing.T) {
	t.Run("temBAD_FEE - negative fee", func(t *testing.T) {
		env := newBatchEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		env.Fund(alice, bob)
		env.Close()

		// Negative fee should fail
		batch := batchtx.NewBatch(alice.Address)
		batch.Fee = "-10"
		seq := env.Seq(alice)
		batch.SetSequence(seq)
		batch.SetFlags(batchtx.BatchFlagAllOrNothing)
		batch.AddInnerTransaction(MakeFakeInnerTx(1))
		batch.AddInnerTransaction(MakeFakeInnerTx(2))

		result := env.Submit(batch)
		jtx.RequireTxFail(t, result, "temBAD_FEE")
	})

	t.Run("temINVALID_FLAG - invalid batch flags", func(t *testing.T) {
		env := newBatchEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		env.Fund(alice, bob)
		env.Close()

		// Use a bit that is outside tfBatchMask (not a mode flag, not universal),
		// so the flag mask at preflight0 rejects it before any inner check.
		seq := env.Seq(alice)
		batchFee := CalcBatchFeeFromEnv(env, 0, 2)
		batch := NewBatchBuilder(alice, seq, batchFee, 0x00100000). // invalid flag
										AddInnerTx(MakeFakeInnerTx(1)).
										AddInnerTx(MakeFakeInnerTx(2)).
										MustBuild()

		result := env.Submit(batch)
		jtx.RequireTxFail(t, result, "temINVALID_FLAG")
	})

	t.Run("temINVALID_FLAG - too many mode flags", func(t *testing.T) {
		env := newBatchEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		env.Fund(alice, bob)
		env.Close()

		seq := env.Seq(alice)
		batchFee := CalcBatchFeeFromEnv(env, 0, 2)
		// Two mode flags set simultaneously
		batch := NewBatchBuilder(alice, seq, batchFee,
			batchtx.BatchFlagAllOrNothing|batchtx.BatchFlagOnlyOne).
			AddInnerTx(MakeFakeInnerTx(1)).
			AddInnerTx(MakeFakeInnerTx(2)).
			MustBuild()

		result := env.Submit(batch)
		jtx.RequireTxFail(t, result, "temINVALID_FLAG")
	})

	t.Run("temARRAY_EMPTY - no transactions", func(t *testing.T) {
		env := newBatchEnv(t)
		alice := jtx.NewAccount("alice")
		env.Fund(alice)
		env.Close()

		seq := env.Seq(alice)
		batchFee := CalcBatchFeeFromEnv(env, 0, 0)
		batch := NewBatchBuilder(alice, seq, batchFee, batchtx.BatchFlagAllOrNothing).
			MustBuild()

		result := env.Submit(batch)
		jtx.RequireTxFail(t, result, "temARRAY_EMPTY")
		jtx.RequireSequence(t, env, alice, seq)
	})

	t.Run("temARRAY_EMPTY - only 1 transaction", func(t *testing.T) {
		env := newBatchEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		env.Fund(alice, bob)
		env.Close()

		seq := env.Seq(alice)
		batchFee := CalcBatchFeeFromEnv(env, 0, 1)
		batch := NewBatchBuilder(alice, seq, batchFee, batchtx.BatchFlagAllOrNothing).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 1, seq+1)).
			MustBuild()

		result := env.Submit(batch)
		jtx.RequireTxFail(t, result, "temARRAY_EMPTY")
		jtx.RequireSequence(t, env, alice, seq)
	})

	t.Run("temARRAY_TOO_LARGE - more than 8 transactions", func(t *testing.T) {
		env := newBatchEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		env.Fund(alice, bob)
		env.Close()

		seq := env.Seq(alice)
		batchFee := CalcBatchFeeFromEnv(env, 0, 9)
		builder := NewBatchBuilder(alice, seq, batchFee, batchtx.BatchFlagAllOrNothing)
		for i := range 9 {
			builder.AddInnerTx(MakeFakeInnerTx(uint32(i + 1)))
		}
		batch := builder.MustBuild()

		result := env.Submit(batch)
		jtx.RequireTxFail(t, result, "temARRAY_TOO_LARGE")
		jtx.RequireSequence(t, env, alice, seq)
	})

	t.Run("temREDUNDANT - duplicate batch signer", func(t *testing.T) {
		env := newBatchEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		env.Fund(alice, bob)
		env.Close()

		seq := env.Seq(alice)
		batchFee := CalcBatchFeeFromEnv(env, 2, 2)
		batch := NewBatchBuilder(alice, seq, batchFee, batchtx.BatchFlagAllOrNothing).
			AddInnerTx(MakeInnerPaymentXRP(bob, alice, 1, env.Seq(bob))).
			AddInnerTx(MakeFakeInnerTx(2)).
			AddSigner(bob).
			AddSigner(bob). // duplicate
			MustBuild()

		result := env.Submit(batch)
		jtx.RequireTxFail(t, result, "temREDUNDANT")
		jtx.RequireSequence(t, env, alice, seq)
	})

	t.Run("temBAD_SIGNER - signer is outer account", func(t *testing.T) {
		env := newBatchEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		env.Fund(alice, bob)
		env.Close()

		seq := env.Seq(alice)
		batchFee := CalcBatchFeeFromEnv(env, 1, 2)
		batch := NewBatchBuilder(alice, seq, batchFee, batchtx.BatchFlagAllOrNothing).
			AddInnerTx(MakeFakeInnerTx(1)).
			AddInnerTx(MakeFakeInnerTx(2)).
			AddSigner(alice). // signer is outer account
			MustBuild()

		result := env.Submit(batch)
		jtx.RequireTxFail(t, result, "temBAD_SIGNER")
		jtx.RequireSequence(t, env, alice, seq)
	})

	t.Run("temARRAY_TOO_LARGE - too many signers", func(t *testing.T) {
		env := newBatchEnv(t)
		alice := jtx.NewAccount("alice")
		env.Fund(alice)
		env.Close()

		seq := env.Seq(alice)
		batchFee := CalcBatchFeeFromEnv(env, 9, 2)
		builder := NewBatchBuilder(alice, seq, batchFee, batchtx.BatchFlagAllOrNothing).
			AddInnerTx(MakeFakeInnerTx(1)).
			AddInnerTx(MakeFakeInnerTx(2))
		for i := range 9 {
			signer := jtx.NewAccount(fmt.Sprintf("signer%d", i))
			builder.AddSigner(signer)
		}
		batch := builder.MustBuild()

		result := env.Submit(batch)
		jtx.RequireTxFail(t, result, "temARRAY_TOO_LARGE")
		jtx.RequireSequence(t, env, alice, seq)
	})

	// Reference: rippled Batch_test.cpp:398-406.
	t.Run("temINVALID_INNER_BATCH - malformed inner tx", func(t *testing.T) {
		env := newBatchEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		env.Fund(alice, bob)
		env.Close()

		seq := env.Seq(alice)
		batchFee := CalcBatchFeeFromEnv(env, 0, 2)

		badInner := payment.NewPayment(alice.Address, bob.Address, tx.NewXRPAmount(-1))
		badInner.Fee = "0"
		badInner.SigningPubKey = ""
		badInner.SetSequence(seq + 2)
		badInner.SetFlags(tx.TfInnerBatchTxn)

		batch := NewBatchBuilder(alice, seq, batchFee, batchtx.BatchFlagAllOrNothing).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 1, seq+1)).
			AddInnerTx(badInner).
			MustBuild()

		result := env.Submit(batch)
		jtx.RequireTxFail(t, result, "temINVALID_INNER_BATCH")
	})

	// A batch may not wrap a blocklisted inner transaction type (all Vault and
	// Loan types). The rejection is unconditional and fires at preflight, before
	// the inner's own amendment/flag/fee checks.
	// Reference: rippled Batch::disabledTxTypes + Batch_test.cpp testLoan().
	t.Run("temINVALID_INNER_BATCH - disabled inner type (VaultCreate)", func(t *testing.T) {
		env := newBatchEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		env.Fund(alice, bob)
		env.Close()

		seq := env.Seq(alice)
		batchFee := CalcBatchFeeFromEnv(env, 0, 2)

		batch := NewBatchBuilder(alice, seq, batchFee, batchtx.BatchFlagAllOrNothing).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 1, seq+1)).
			AddInnerTx(MakeInnerVaultCreate(alice, seq+2)).
			MustBuild()

		result := env.Submit(batch)
		jtx.RequireTxFail(t, result, "temINVALID_INNER_BATCH")
	})

	// The blocklist check precedes the inner tfInnerBatchTxn-flag check: a
	// blocklisted inner missing the flag is still temINVALID_INNER_BATCH, not
	// temINVALID_FLAG.
	t.Run("temINVALID_INNER_BATCH - disabled type precedes flag check", func(t *testing.T) {
		env := newBatchEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		env.Fund(alice, bob)
		env.Close()

		seq := env.Seq(alice)
		batchFee := CalcBatchFeeFromEnv(env, 0, 2)

		badVault := MakeInnerVaultCreate(alice, seq+2)
		badVault.GetCommon().Flags = nil // omit tfInnerBatchTxn

		batch := NewBatchBuilder(alice, seq, batchFee, batchtx.BatchFlagAllOrNothing).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 1, seq+1)).
			AddInnerTx(badVault).
			MustBuild()

		result := env.Submit(batch)
		jtx.RequireTxFail(t, result, "temINVALID_INNER_BATCH")
	})

	// The blocklist check precedes the inner zero-fee check: a blocklisted inner
	// with a non-zero fee is still temINVALID_INNER_BATCH, not temBAD_FEE.
	t.Run("temINVALID_INNER_BATCH - disabled type precedes fee check", func(t *testing.T) {
		env := newBatchEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		env.Fund(alice, bob)
		env.Close()

		seq := env.Seq(alice)
		batchFee := CalcBatchFeeFromEnv(env, 0, 2)

		badVault := MakeInnerVaultCreate(alice, seq+2)
		badVault.Fee = fmt.Sprintf("%d", env.BaseFee())

		batch := NewBatchBuilder(alice, seq, batchFee, batchtx.BatchFlagAllOrNothing).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 1, seq+1)).
			AddInnerTx(badVault).
			MustBuild()

		result := env.Submit(batch)
		jtx.RequireTxFail(t, result, "temINVALID_INNER_BATCH")
	})

	// Reference: rippled Batch_test.cpp:410-501 (per-inner rejection cases).

	t.Run("temBAD_FEE - inner fee non-zero", func(t *testing.T) {
		env := newBatchEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		env.Fund(alice, bob)
		env.Close()

		seq := env.Seq(alice)
		batchFee := CalcBatchFeeFromEnv(env, 0, 2)
		badInner := MakeInnerPaymentXRP(alice, bob, 1, seq+2)
		badInner.Fee = fmt.Sprintf("%d", env.BaseFee())

		batch := NewBatchBuilder(alice, seq, batchFee, batchtx.BatchFlagAllOrNothing).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 1, seq+1)).
			AddInnerTx(badInner).
			MustBuild()

		result := env.Submit(batch)
		jtx.RequireTxFail(t, result, "temBAD_FEE")
	})

	t.Run("temSEQ_AND_TICKET - inner has both Sequence and TicketSequence", func(t *testing.T) {
		env := newBatchEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		env.Fund(alice, bob)
		env.Close()

		seq := env.Seq(alice)
		batchFee := CalcBatchFeeFromEnv(env, 0, 2)
		bothInner := MakeInnerPaymentXRP(alice, bob, 1, seq+2)
		ticket := uint32(1)
		bothInner.TicketSequence = &ticket

		batch := NewBatchBuilder(alice, seq, batchFee, batchtx.BatchFlagAllOrNothing).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 1, seq+1)).
			AddInnerTx(bothInner).
			MustBuild()

		result := env.Submit(batch)
		jtx.RequireTxFail(t, result, "temSEQ_AND_TICKET")
	})

	t.Run("temSEQ_AND_TICKET - inner has neither Sequence nor TicketSequence", func(t *testing.T) {
		env := newBatchEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		env.Fund(alice, bob)
		env.Close()

		seq := env.Seq(alice)
		batchFee := CalcBatchFeeFromEnv(env, 0, 2)
		neitherInner := MakeInnerPaymentXRP(alice, bob, 1, 0)

		batch := NewBatchBuilder(alice, seq, batchFee, batchtx.BatchFlagAllOrNothing).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 1, seq+1)).
			AddInnerTx(neitherInner).
			MustBuild()

		result := env.Submit(batch)
		jtx.RequireTxFail(t, result, "temSEQ_AND_TICKET")
	})

	t.Run("temBAD_SIGNATURE - inner has TxnSignature", func(t *testing.T) {
		env := newBatchEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		env.Fund(alice, bob)
		env.Close()

		seq := env.Seq(alice)
		batchFee := CalcBatchFeeFromEnv(env, 0, 2)
		signedInner := MakeInnerPaymentXRP(alice, bob, 1, seq+2)
		signedInner.TxnSignature = "DEADBEEF"

		batch := NewBatchBuilder(alice, seq, batchFee, batchtx.BatchFlagAllOrNothing).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 1, seq+1)).
			AddInnerTx(signedInner).
			MustBuild()

		result := env.Submit(batch)
		jtx.RequireTxFail(t, result, "temBAD_SIGNATURE")
	})

	t.Run("temBAD_SIGNER - inner has Signers", func(t *testing.T) {
		env := newBatchEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		env.Fund(alice, bob)
		env.Close()

		seq := env.Seq(alice)
		batchFee := CalcBatchFeeFromEnv(env, 0, 2)
		multiInner := MakeInnerPaymentXRP(alice, bob, 1, seq+2)
		multiInner.Signers = []tx.SignerWrapper{
			{Signer: tx.Signer{Account: bob.Address, SigningPubKey: bob.PublicKeyHex(), TxnSignature: "DEADBEEF"}},
		}

		batch := NewBatchBuilder(alice, seq, batchFee, batchtx.BatchFlagAllOrNothing).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 1, seq+1)).
			AddInnerTx(multiInner).
			MustBuild()

		result := env.Submit(batch)
		jtx.RequireTxFail(t, result, "temBAD_SIGNER")
	})

	t.Run("temBAD_REGKEY - inner has SigningPubKey", func(t *testing.T) {
		env := newBatchEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		env.Fund(alice, bob)
		env.Close()

		seq := env.Seq(alice)
		batchFee := CalcBatchFeeFromEnv(env, 0, 2)
		pkInner := MakeInnerPaymentXRP(alice, bob, 1, seq+2)
		pkInner.SigningPubKey = alice.PublicKeyHex()

		batch := NewBatchBuilder(alice, seq, batchFee, batchtx.BatchFlagAllOrNothing).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 1, seq+1)).
			AddInnerTx(pkInner).
			MustBuild()

		result := env.Submit(batch)
		jtx.RequireTxFail(t, result, "temBAD_REGKEY")
	})

	t.Run("temINVALID - inner is itself a Batch", func(t *testing.T) {
		env := newBatchEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		env.Fund(alice, bob)
		env.Close()

		seq := env.Seq(alice)
		batchFee := CalcBatchFeeFromEnv(env, 0, 2)
		innerBatch := batchtx.NewBatch(alice.Address)
		innerBatch.Fee = "0"
		innerBatch.SigningPubKey = ""
		innerBatch.SetSequence(seq + 2)
		innerBatch.SetFlags(tx.TfInnerBatchTxn | batchtx.BatchFlagAllOrNothing)

		batch := NewBatchBuilder(alice, seq, batchFee, batchtx.BatchFlagAllOrNothing).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 1, seq+1)).
			AddInnerTx(innerBatch).
			MustBuild()

		result := env.Submit(batch)
		jtx.RequireTxFail(t, result, "temINVALID")
	})

	t.Run("temINVALID_INNER_BATCH - inner ticket with AccountTxnID", func(t *testing.T) {
		// Pins finding Batch-inner-ticket-AccountTxnID: an inner tx that uses a
		// ticket may not also carry AccountTxnID (rippled inner preflight1 ->
		// temINVALID, surfaced on the outer as temINVALID_INNER_BATCH).
		env := newBatchEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		env.Fund(alice, bob)
		env.Close()

		seq := env.Seq(alice)
		batchFee := CalcBatchFeeFromEnv(env, 0, 2)
		badInner := MakeInnerPaymentXRPWithTicket(alice, bob, 1, seq+5)
		badInner.GetCommon().AccountTxnID = "00000000000000000000000000000000000000000000000000000000000000AA"

		batch := NewBatchBuilder(alice, seq, batchFee, batchtx.BatchFlagAllOrNothing).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 1, seq+1)).
			AddInnerTx(badInner).
			MustBuild()

		result := env.Submit(batch)
		jtx.RequireTxFail(t, result, "temINVALID_INNER_BATCH")
	})

	t.Run("temINVALID_FLAG - inner missing tfInnerBatchTxn", func(t *testing.T) {
		env := newBatchEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		env.Fund(alice, bob)
		env.Close()

		seq := env.Seq(alice)
		batchFee := CalcBatchFeeFromEnv(env, 0, 2)
		noFlagInner := MakeInnerPaymentXRP(alice, bob, 1, seq+2)
		noFlag := uint32(0)
		noFlagInner.Flags = &noFlag

		batch := NewBatchBuilder(alice, seq, batchFee, batchtx.BatchFlagAllOrNothing).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 1, seq+1)).
			AddInnerTx(noFlagInner).
			MustBuild()

		result := env.Submit(batch)
		jtx.RequireTxFail(t, result, "temINVALID_FLAG")
	})

	t.Run("temREDUNDANT - duplicate inner transactions", func(t *testing.T) {
		env := newBatchEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		env.Fund(alice, bob)
		env.Close()

		seq := env.Seq(alice)
		batchFee := CalcBatchFeeFromEnv(env, 0, 2)
		// Two identical inner txns → identical hashes → temREDUNDANT.
		batch := NewBatchBuilder(alice, seq, batchFee, batchtx.BatchFlagAllOrNothing).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 1, seq+1)).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 1, seq+1)).
			MustBuild()

		result := env.Submit(batch)
		jtx.RequireTxFail(t, result, "temREDUNDANT")
	})

	t.Run("temREDUNDANT - duplicate sequence per account under tfAllOrNothing", func(t *testing.T) {
		env := newBatchEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		env.Fund(alice, bob)
		env.Close()

		seq := env.Seq(alice)
		batchFee := CalcBatchFeeFromEnv(env, 0, 2)
		// Two distinct inner txns with the SAME Sequence for alice → temREDUNDANT.
		batch := NewBatchBuilder(alice, seq, batchFee, batchtx.BatchFlagAllOrNothing).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 1, seq+1)).
			AddInnerTx(MakeInnerPaymentXRP(alice, bob, 2, seq+1)).
			MustBuild()

		result := env.Submit(batch)
		jtx.RequireTxFail(t, result, "temREDUNDANT")
	})

	t.Run("valid batch with all four mode flags individually", func(t *testing.T) {
		for _, flag := range []uint32{
			batchtx.BatchFlagAllOrNothing,
			batchtx.BatchFlagOnlyOne,
			batchtx.BatchFlagUntilFailure,
			batchtx.BatchFlagIndependent,
		} {
			env := newBatchEnv(t)
			alice := jtx.NewAccount("alice")
			bob := jtx.NewAccount("bob")
			env.Fund(alice, bob)
			env.Close()

			seq := env.Seq(alice)
			batchFee := CalcBatchFeeFromEnv(env, 0, 2)
			batch := NewBatchBuilder(alice, seq, batchFee, flag).
				AddInnerTx(MakeInnerPaymentXRP(alice, bob, 1, seq+1)).
				AddInnerTx(MakeInnerPaymentXRP(alice, bob, 1, seq+2)).
				MustBuild()

			result := env.Submit(batch)
			jtx.RequireTxSuccess(t, result)
			env.Close()
		}
	})
}

func TestCalculateBaseFee(t *testing.T) {
	t.Run("fee formula correct", func(t *testing.T) {
		// (numSigners + 2) * baseFee + baseFee * txns
		baseFee := uint64(10)

		// 0 signers, 2 txns -> (0+2)*10 + 10*2 = 40
		require.Equal(t, uint64(40), CalcBatchFee(baseFee, 0, 2))

		// 1 signer, 2 txns -> (1+2)*10 + 10*2 = 50
		require.Equal(t, uint64(50), CalcBatchFee(baseFee, 1, 2))

		// 2 signers, 3 txns -> (2+2)*10 + 10*3 = 70
		require.Equal(t, uint64(70), CalcBatchFee(baseFee, 2, 3))

		// 0 signers, 8 txns -> (0+2)*10 + 10*8 = 100
		require.Equal(t, uint64(100), CalcBatchFee(baseFee, 0, 8))
	})

	t.Run("calculateBaseFee from env", func(t *testing.T) {
		env := newBatchEnv(t)
		// Default base fee is 10
		require.Equal(t, uint64(40), CalcBatchFeeFromEnv(env, 0, 2))
		require.Equal(t, uint64(50), CalcBatchFeeFromEnv(env, 1, 2))
	})
}
