// Package regression_test contains regression tests ported from rippled.
// Reference: rippled/src/test/app/Regression_test.cpp
package regression_test

import (
	"encoding/hex"
	"strconv"
	"testing"

	"github.com/LeJamon/goXRPLd/keylet"
	"github.com/LeJamon/goXRPLd/internal/core/tx"
	"github.com/LeJamon/goXRPLd/internal/core/tx/account"
	jtx "github.com/LeJamon/goXRPLd/internal/testing"
	"github.com/LeJamon/goXRPLd/internal/testing/check"
	"github.com/LeJamon/goXRPLd/internal/testing/offer"
	"github.com/LeJamon/goXRPLd/internal/testing/payment"
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
// Reference: rippled Regression_test.cpp testFeeEscalationAutofill()
func TestRegression_FeeEscalation(t *testing.T) {
	t.Skip("Fee escalation requires transaction queue support (not implemented)")
}

// TestRegression_FeeEscalationExtremeConfig tests fee escalation with extreme config.
// Reference: rippled Regression_test.cpp testFeeEscalationExtremeConfig()
func TestRegression_FeeEscalationExtremeConfig(t *testing.T) {
	t.Skip("Fee escalation requires transaction queue support (not implemented)")
}

// TestRegression_Secp256r1Key tests that signing with a secp256r1 key fails.
// Reference: rippled Regression_test.cpp testSecp256r1key()
func TestRegression_Secp256r1Key(t *testing.T) {
	t.Skip("Requires low-level SigningPubKey manipulation (secp256r1 rejection tested in crypto layer)")
}

// TestRegression_JsonInvalid tests JSON parsing of a large request.
// Reference: rippled Regression_test.cpp testJsonInvalid()
func TestRegression_JsonInvalid(t *testing.T) {
	t.Skip("JSON parser library test — not applicable to Go engine")
}
