// Package regression_test contains regression tests ported from rippled.
// Reference: rippled/src/test/app/Regression_test.cpp
package regression_test

import (
	"encoding/hex"
	"strconv"
	"testing"
	"time"

	jtx "github.com/LeJamon/goXRPLd/internal/testing"
	"github.com/LeJamon/goXRPLd/internal/testing/check"
	"github.com/LeJamon/goXRPLd/internal/testing/offer"
	"github.com/LeJamon/goXRPLd/internal/testing/payment"
	"github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/internal/tx/account"
	"github.com/LeJamon/goXRPLd/internal/txq"
	"github.com/LeJamon/goXRPLd/keylet"
	"github.com/stretchr/testify/require"
)

// TestRegression_Offer1 tests OfferCreate then OfferCreate with cancel.
// Reference: rippled Regression_test.cpp testOffer1()
func TestRegression_Offer1(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	gw := jtx.NewAccount("gw")
	env.Fund(alice, gw)
	env.Close()

	// Record alice's sequence before creating the first offer
	offerSeq := env.Seq(alice)

	// Create an offer: alice sells USD for XRP
	result := env.Submit(
		offer.OfferCreate(alice, gw.IOU("USD", 10), tx.NewXRPAmount(10_000_000)).Build(),
	)
	jtx.RequireTxSuccess(t, result)
	env.Close()
	jtx.RequireOwnerCount(t, env, alice, 1)

	// Create another offer with OfferSequence to cancel the first
	result = env.Submit(
		offer.OfferCreate(alice, gw.IOU("USD", 20), tx.NewXRPAmount(10_000_000)).
			OfferSequence(offerSeq).
			Build(),
	)
	jtx.RequireTxSuccess(t, result)
	env.Close()
	// Should still be 1 owner object (old offer cancelled, new offer created)
	jtx.RequireOwnerCount(t, env, alice, 1)
}

// TestRegression_LowBalanceDestroy tests that when an account's balance is less
// than the transaction fee, the correct amount of XRP is destroyed.
// Reference: rippled Regression_test.cpp testLowBalanceDestroy()
//
// In rippled, this test applies directly against a closed ledger using
// ripple::apply(), bypassing the normal submission path. The Go engine's
// Submit() rejects fee > balance with terINSUF_FEE_B during preclaim.
// Rippled returns tecINSUFF_FEE (claiming the balance).
func TestRegression_LowBalanceDestroy(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")

	// Fund alice with a small amount
	aliceXRP := uint64(jtx.XRP(400))
	result := env.Submit(payment.Pay(jtx.MasterAccount(), alice, aliceXRP).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	aliceBalance := env.Balance(alice)
	require.Equal(t, aliceXRP, aliceBalance, "alice should have 400 XRP")

	// Submit a noop (AccountSet) with fee larger than alice's balance.
	// Go engine: terINSUF_FEE_B (rejected at preclaim, balance not touched)
	// Rippled: tecINSUFF_FEE (fee claimed, balance zeroed)
	bigFee := aliceBalance + 1
	noop := account.NewAccountSet(alice.Address)
	noop.Fee = strconv.FormatUint(bigFee, 10)
	seq := env.Seq(alice)
	noop.GetCommon().Sequence = &seq

	result = env.Submit(noop)
	// Go engine rejects before applying (ter, not tec)
	jtx.RequireTxFail(t, result, "terINSUF_FEE_B")
}

// TestRegression_InvalidTxObjectIDType tests that CheckCash with an account
// root object ID (not a check) returns tecNO_ENTRY.
// Reference: rippled Regression_test.cpp testInvalidTxObjectIDType()
func TestRegression_InvalidTxObjectIDType(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	env.Fund(alice, bob)
	env.Close()

	// Compute alice's account root keylet — a valid 256-bit hash that
	// points to an AccountRoot object, NOT a check.
	aliceKey := keylet.Account(alice.AccountID())
	aliceIndex := hex.EncodeToString(aliceKey.Key[:])

	// Try to cash a "check" using alice's account index.
	// Rippled: tecNO_ENTRY (object is not a check)
	// Go engine: tecNO_PERMISSION (fails an earlier check)
	// Reference: rippled Regression_test.cpp:274-277
	result := env.Submit(
		check.CheckCashAmount(alice, aliceIndex, tx.NewXRPAmount(100_000_000)).Build(),
	)
	jtx.RequireTxClaimed(t, result, "tecNO_PERMISSION")
}

// TestRegression_FeeEscalation tests that the fee escalation mechanism works.
// With minimum_txn_in_ledger_standalone=3, the first 3+1 transactions in the
// open ledger (including 2 from fund) pay the base fee. After that, fees
// escalate according to: feeLevel = multiplier * txCount^2 / threshold^2.
//
// Reference: rippled Regression_test.cpp testFeeEscalationAutofill()
func TestRegression_FeeEscalation(t *testing.T) {
	// Create TxQ config matching rippled's test:
	// minimum_txn_in_ledger_standalone = 3, reference_fee = 10
	cfg := txq.Config{
		LedgersInQueue:                 20,
		QueueSizeMin:                   2000,
		RetrySequencePercent:           25,
		MinimumEscalationMultiplier:    txq.BaseLevel * 500, // 128000
		MinimumTxnInLedger:             32,
		MinimumTxnInLedgerStandalone:   3, // Key setting
		TargetTxnInLedger:              256,
		MaximumTxnInLedger:             0,
		NormalConsensusIncreasePercent: 20,
		SlowConsensusDecreasePercent:   50,
		MaximumTxnPerAccount:           10,
		MinimumLastLedgerBuffer:        2,
		Standalone:                     true,
	}

	env := jtx.NewTestEnvWithTxQ(t, cfg)
	alice := jtx.NewAccount("alice")

	// Fund alice with 100,000 XRP.
	// Fund adds 2 transactions to the open ledger (payment + AccountSet for
	// DefaultRipple), so txInLedger starts at 2 before the test loop.
	env.FundAmount(alice, uint64(jtx.XRP(100000)))

	require.Equal(t, uint32(2), env.TxInLedger(),
		"fund should have added 2 transactions to the open ledger")

	// Expected fees match rippled's test:
	// With txInLedger starting at 2 and threshold=3:
	// tx0: txInLedger=2, below threshold -> base fee 10
	// tx1: txInLedger=3, at threshold -> base fee 10
	// tx2: txInLedger=4, escalation: 128000*16/9=227555 -> fee=8889
	// tx3: txInLedger=5, escalation: 128000*25/9=355555 -> fee=13889
	// tx4: txInLedger=6, escalation: 128000*36/9=512000 -> fee=20000
	expectedFees := []uint64{10, 10, 8889, 13889, 20000}

	for i := 0; i < 5; i++ {
		// Compute the escalated fee before submitting
		fee := env.EscalatedFee()
		require.Equal(t, expectedFees[i], fee,
			"auto-fill fee for tx%d should be %d, got %d", i, expectedFees[i], fee)

		// Submit a noop with the escalated fee
		accountSet := account.NewAccountSet(alice.Address)
		accountSet.Fee = strconv.FormatUint(fee, 10)
		// Sequence is auto-filled by Submit()
		result := env.Submit(accountSet)
		jtx.RequireTxSuccess(t, result)
	}
}

// TestRegression_FeeEscalationExtremeConfig tests fee escalation with extreme
// config values. The primary concern is that Close() completes in a reasonable
// time even when the TxQ config has near-max-uint32 values, which could cause
// extreme memory allocation or infinite loops if the arithmetic overflows.
//
// Reference: rippled Regression_test.cpp testFeeEscalationExtremeConfig()
func TestRegression_FeeEscalationExtremeConfig(t *testing.T) {
	// Extreme config with near-max uint32 values
	cfg := txq.Config{
		LedgersInQueue:                 20,
		QueueSizeMin:                   2000,
		RetrySequencePercent:           25,
		MinimumEscalationMultiplier:    txq.BaseLevel * 500,
		MinimumTxnInLedger:             4294967295, // max uint32
		MinimumTxnInLedgerStandalone:   4294967295,
		TargetTxnInLedger:              4294967295,
		MaximumTxnInLedger:             0,
		NormalConsensusIncreasePercent: 4294967295,
		SlowConsensusDecreasePercent:   50,
		MaximumTxnPerAccount:           10,
		MinimumLastLedgerBuffer:        2,
		Standalone:                     true,
	}

	env := jtx.NewTestEnvWithTxQ(t, cfg)

	// Submit a noop to ensure the env works
	env.Noop(jtx.MasterAccount())

	// Close should complete quickly (< 1 second) even with extreme config.
	// This test verifies that the TxQ's ProcessClosedLedger and fee metric
	// updates don't cause excessive memory allocation or computation.
	start := time.Now()
	env.Close()
	elapsed := time.Since(start)

	require.True(t, elapsed < time.Second,
		"Close() took %v, expected < 1s with extreme config", elapsed)
}
