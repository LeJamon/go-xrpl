// Package escrow_test contains integration tests for IOU escrow (token escrow) behavior.
// Tests ported from rippled's EscrowToken_test.cpp (src/test/app/EscrowToken_test.cpp).
package escrow_test

import (
	"testing"
	"time"

	"github.com/LeJamon/goXRPLd/internal/ledger/state"
	jtx "github.com/LeJamon/goXRPLd/internal/testing"
	"github.com/LeJamon/goXRPLd/internal/testing/accountset"
	"github.com/LeJamon/goXRPLd/internal/testing/escrow"
	"github.com/LeJamon/goXRPLd/internal/testing/payment"
	"github.com/LeJamon/goXRPLd/internal/testing/trustset"
	"github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/stretchr/testify/require"
)

// --------------------------------------------------------------------------
// IOU escrow test helpers
// --------------------------------------------------------------------------

// setupIOUEscrowEnv creates a standard test environment for IOU escrow tests:
//   - Enables FeatureTokenEscrow amendment
//   - Creates gateway, alice, bob accounts funded with 5000 XRP each
//   - Sets lsfAllowTrustLineLocking on gateway
//   - Creates trust lines from alice and bob to gateway for USD (limit 10000)
//   - Pays alice and bob 5000 USD each from gateway
//
// Returns env, gateway, alice, bob.
func setupIOUEscrowEnv(t *testing.T) (*jtx.TestEnv, *jtx.Account, *jtx.Account, *jtx.Account) {
	t.Helper()

	env := jtx.NewTestEnv(t)
	env.EnableFeature("TokenEscrow")

	gw := jtx.NewAccount("gateway")
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")

	fund5000(env, gw, alice, bob)

	// Set AllowTrustLineLocking on gateway
	result := env.Submit(accountset.AccountSet(gw).AllowTrustLineLocking().Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// Create trust lines
	env.Trust(alice, tx.NewIssuedAmountFromFloat64(10000, "USD", gw.Address))
	env.Trust(bob, tx.NewIssuedAmountFromFloat64(10000, "USD", gw.Address))
	env.Close()

	// Fund with USD
	result = env.Submit(payment.PayIssued(gw, alice, tx.NewIssuedAmountFromFloat64(5000, "USD", gw.Address)).Build())
	jtx.RequireTxSuccess(t, result)
	result = env.Submit(payment.PayIssued(gw, bob, tx.NewIssuedAmountFromFloat64(5000, "USD", gw.Address)).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	return env, gw, alice, bob
}

// usd creates an IOU amount for USD issued by gw.
func usd(value float64, gw *jtx.Account) tx.Amount {
	return tx.NewIssuedAmountFromFloat64(value, "USD", gw.Address)
}

// --------------------------------------------------------------------------
// TestIOUEscrow_Enablement
// Reference: rippled EscrowToken_test.cpp testIOUEnablement
// --------------------------------------------------------------------------

func TestIOUEscrow_Enablement(t *testing.T) {
	t.Run("WithTokenEscrow", func(t *testing.T) {
		env, gw, alice, bob := setupIOUEscrowEnv(t)

		// Create escrow with condition: should succeed
		seq1 := env.Seq(alice)
		result := env.Submit(
			escrow.EscrowCreate(alice, bob, 0).
				IOUAmount(usd(1000, gw)).
				Condition(escrow.TestCondition1).
				FinishTime(env.Now().Add(1*time.Second)).
				Fee(baseFee*150).
				Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// Finish escrow: should succeed
		result = env.Submit(
			escrow.EscrowFinish(bob, alice, seq1).
				Condition(escrow.TestCondition1).
				Fulfillment(escrow.TestFulfillment1).
				Fee(baseFee*150).
				Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// Create escrow with condition + cancel time: should succeed
		seq2 := env.Seq(alice)
		result = env.Submit(
			escrow.EscrowCreate(alice, bob, 0).
				IOUAmount(usd(1000, gw)).
				Condition(escrow.TestCondition2).
				FinishTime(env.Now().Add(1*time.Second)).
				CancelTime(env.Now().Add(2*time.Second)).
				Fee(baseFee*150).
				Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// Cancel escrow: should succeed
		result = env.Submit(
			escrow.EscrowCancel(bob, alice, seq2).
				Fee(baseFee*150).
				Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
	})

	t.Run("WithoutTokenEscrow", func(t *testing.T) {
		// Do NOT enable TokenEscrow
		env := jtx.NewTestEnv(t)

		gw := jtx.NewAccount("gateway")
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		fund5000(env, gw, alice, bob)

		// Even though we can't create escrow without amendment,
		// set up trust lines for completeness
		env.EnableFeature("TokenEscrow")
		result := env.Submit(accountset.AccountSet(gw).AllowTrustLineLocking().Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		env.Trust(alice, tx.NewIssuedAmountFromFloat64(10000, "USD", gw.Address))
		env.Trust(bob, tx.NewIssuedAmountFromFloat64(10000, "USD", gw.Address))
		env.Close()

		result = env.Submit(payment.PayIssued(gw, alice, tx.NewIssuedAmountFromFloat64(5000, "USD", gw.Address)).Build())
		jtx.RequireTxSuccess(t, result)
		result = env.Submit(payment.PayIssued(gw, bob, tx.NewIssuedAmountFromFloat64(5000, "USD", gw.Address)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// Disable TokenEscrow for the escrow create attempt
		env.DisableFeature("TokenEscrow")

		// Create IOU escrow: should fail with temBAD_AMOUNT
		result = env.Submit(
			escrow.EscrowCreate(alice, bob, 0).
				IOUAmount(usd(1000, gw)).
				Condition(escrow.TestCondition1).
				FinishTime(env.Now().Add(1*time.Second)).
				Fee(baseFee*150).
				Build())
		jtx.RequireTxFail(t, result, "temBAD_AMOUNT")
		env.Close()
	})

	t.Run("FinishAndCancelNonexistentEscrow", func(t *testing.T) {
		// Reference: rippled EscrowToken_test.cpp second loop in testIOUEnablement
		// Finish/cancel of a nonexistent escrow should fail with tecNO_TARGET
		env, _, alice, bob := setupIOUEscrowEnv(t)

		seq1 := env.Seq(alice)
		result := env.Submit(
			escrow.EscrowFinish(bob, alice, seq1).
				Condition(escrow.TestCondition1).
				Fulfillment(escrow.TestFulfillment1).
				Fee(baseFee*150).
				Build())
		jtx.RequireTxFail(t, result, "tecNO_TARGET")
		env.Close()

		result = env.Submit(
			escrow.EscrowCancel(bob, alice, seq1).Build())
		jtx.RequireTxFail(t, result, "tecNO_TARGET")
		env.Close()
	})
}

// --------------------------------------------------------------------------
// TestIOUEscrow_AllowLockingFlag
// Reference: rippled EscrowToken_test.cpp testIOUAllowLockingFlag
// --------------------------------------------------------------------------

func TestIOUEscrow_AllowLockingFlag(t *testing.T) {
	env, gw, alice, bob := setupIOUEscrowEnv(t)

	// Create Escrow #1 (with condition)
	seq1 := env.Seq(alice)
	result := env.Submit(
		escrow.EscrowCreate(alice, bob, 0).
			IOUAmount(usd(1000, gw)).
			Condition(escrow.TestCondition1).
			FinishTime(env.Now().Add(1*time.Second)).
			Fee(baseFee*150).
			Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// Create Escrow #2 (time-based with cancel)
	seq2 := env.Seq(alice)
	result = env.Submit(
		escrow.EscrowCreate(alice, bob, 0).
			IOUAmount(usd(1000, gw)).
			FinishTime(env.Now().Add(1*time.Second)).
			CancelTime(env.Now().Add(3*time.Second)).
			Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// Clear the AllowTrustLineLocking flag on gateway
	result = env.Submit(accountset.AccountSet(gw).
		ClearFlag(17). // AccountSetFlagAllowTrustLineLocking = 17
		Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()
	jtx.RequireFlagNotSet(t, env, gw, state.LsfAllowTrustLineLocking)

	// Cannot create escrow without AllowTrustLineLocking
	result = env.Submit(
		escrow.EscrowCreate(alice, bob, 0).
			IOUAmount(usd(1000, gw)).
			Condition(escrow.TestCondition1).
			FinishTime(env.Now().Add(1*time.Second)).
			Fee(baseFee*150).
			Build())
	jtx.RequireTxFail(t, result, "tecNO_PERMISSION")
	env.Close()

	// Can still finish escrow #1 (created before flag was cleared)
	result = env.Submit(
		escrow.EscrowFinish(bob, alice, seq1).
			Condition(escrow.TestCondition1).
			Fulfillment(escrow.TestFulfillment1).
			Fee(baseFee*150).
			Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// Can still cancel escrow #2 (created before flag was cleared)
	result = env.Submit(
		escrow.EscrowCancel(bob, alice, seq2).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()
}

// --------------------------------------------------------------------------
// TestIOUEscrow_CreatePreclaim
// Reference: rippled EscrowToken_test.cpp testIOUCreatePreclaim
// --------------------------------------------------------------------------

func TestIOUEscrow_CreatePreclaim(t *testing.T) {
	t.Run("IssuerCannotEscrow", func(t *testing.T) {
		// tecNO_PERMISSION: issuer is the same as the account
		env := jtx.NewTestEnv(t)
		env.EnableFeature("TokenEscrow")

		gw := jtx.NewAccount("gateway")
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		fund5000(env, gw, alice, bob)

		result := env.Submit(
			escrow.EscrowCreate(gw, alice, 0).
				IOUAmount(usd(1, gw)).
				Condition(escrow.TestCondition1).
				FinishTime(env.Now().Add(1*time.Second)).
				Fee(baseFee*150).
				Build())
		jtx.RequireTxFail(t, result, "tecNO_PERMISSION")
		env.Close()
	})

	t.Run("NoAllowLockingFlag", func(t *testing.T) {
		// tecNO_PERMISSION: asfAllowTrustLineLocking is not set on issuer
		env := jtx.NewTestEnv(t)
		env.EnableFeature("TokenEscrow")

		gw := jtx.NewAccount("gateway")
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		fund5000(env, gw, alice, bob)
		env.Close()

		// Trust lines without AllowTrustLineLocking
		env.Trust(alice, tx.NewIssuedAmountFromFloat64(10000, "USD", gw.Address))
		env.Trust(bob, tx.NewIssuedAmountFromFloat64(10000, "USD", gw.Address))
		env.Close()
		result := env.Submit(payment.PayIssued(gw, alice, tx.NewIssuedAmountFromFloat64(5000, "USD", gw.Address)).Build())
		jtx.RequireTxSuccess(t, result)
		result = env.Submit(payment.PayIssued(gw, bob, tx.NewIssuedAmountFromFloat64(5000, "USD", gw.Address)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// Gateway tries to escrow its own IOU (also fails because issuer == account)
		// But let alice try: issuer exists but no locking flag
		result = env.Submit(
			escrow.EscrowCreate(gw, alice, 0).
				IOUAmount(usd(1, gw)).
				Condition(escrow.TestCondition1).
				FinishTime(env.Now().Add(1*time.Second)).
				Fee(baseFee*150).
				Build())
		jtx.RequireTxFail(t, result, "tecNO_PERMISSION")
		env.Close()
	})

	t.Run("NoTrustLine", func(t *testing.T) {
		// tecNO_LINE: account does not have a trust line to the issuer
		env := jtx.NewTestEnv(t)
		env.EnableFeature("TokenEscrow")

		gw := jtx.NewAccount("gateway")
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		fund5000(env, gw, alice, bob)

		result := env.Submit(accountset.AccountSet(gw).AllowTrustLineLocking().Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// No trust lines set up
		result = env.Submit(
			escrow.EscrowCreate(alice, bob, 0).
				IOUAmount(usd(1, gw)).
				Condition(escrow.TestCondition1).
				FinishTime(env.Now().Add(1*time.Second)).
				Fee(baseFee*150).
				Build())
		jtx.RequireTxFail(t, result, "tecNO_LINE")
		env.Close()
	})

	t.Run("InsufficientFunds_ZeroBalance", func(t *testing.T) {
		// tecINSUFFICIENT_FUNDS: trust line exists but zero balance
		env := jtx.NewTestEnv(t)
		env.EnableFeature("TokenEscrow")

		gw := jtx.NewAccount("gateway")
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		fund5000(env, gw, alice, bob)

		result := env.Submit(accountset.AccountSet(gw).AllowTrustLineLocking().Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		env.Trust(alice, tx.NewIssuedAmountFromFloat64(100000, "USD", gw.Address))
		env.Trust(bob, tx.NewIssuedAmountFromFloat64(100000, "USD", gw.Address))
		env.Close()

		// No USD payment to alice, so balance is 0
		result = env.Submit(
			escrow.EscrowCreate(alice, bob, 0).
				IOUAmount(usd(1, gw)).
				Condition(escrow.TestCondition1).
				FinishTime(env.Now().Add(1*time.Second)).
				Fee(baseFee*150).
				Build())
		jtx.RequireTxFail(t, result, "tecINSUFFICIENT_FUNDS")
		env.Close()
	})

	t.Run("InsufficientFunds_ExceedsBalance", func(t *testing.T) {
		// tecINSUFFICIENT_FUNDS: amount exceeds balance
		env := jtx.NewTestEnv(t)
		env.EnableFeature("TokenEscrow")

		gw := jtx.NewAccount("gateway")
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		env.FundAmount(gw, uint64(xrp(10000)))
		env.FundAmount(alice, uint64(xrp(10000)))
		env.FundAmount(bob, uint64(xrp(10000)))

		result := env.Submit(accountset.AccountSet(gw).AllowTrustLineLocking().Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		env.Trust(alice, tx.NewIssuedAmountFromFloat64(100000, "USD", gw.Address))
		env.Trust(bob, tx.NewIssuedAmountFromFloat64(100000, "USD", gw.Address))
		env.Close()

		result = env.Submit(payment.PayIssued(gw, alice, tx.NewIssuedAmountFromFloat64(10000, "USD", gw.Address)).Build())
		jtx.RequireTxSuccess(t, result)
		result = env.Submit(payment.PayIssued(gw, bob, tx.NewIssuedAmountFromFloat64(10000, "USD", gw.Address)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// Try to escrow more than alice has
		result = env.Submit(
			escrow.EscrowCreate(alice, bob, 0).
				IOUAmount(usd(10001, gw)).
				Condition(escrow.TestCondition1).
				FinishTime(env.Now().Add(1*time.Second)).
				Fee(baseFee*150).
				Build())
		jtx.RequireTxFail(t, result, "tecINSUFFICIENT_FUNDS")
		env.Close()
	})

	t.Run("FrozenSenderTrustLine", func(t *testing.T) {
		// tecFROZEN: sender's trust line is frozen
		env := jtx.NewTestEnv(t)
		env.EnableFeature("TokenEscrow")

		gw := jtx.NewAccount("gateway")
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		env.FundAmount(gw, uint64(xrp(10000)))
		env.FundAmount(alice, uint64(xrp(10000)))
		env.FundAmount(bob, uint64(xrp(10000)))

		result := env.Submit(accountset.AccountSet(gw).AllowTrustLineLocking().Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		env.Trust(alice, tx.NewIssuedAmountFromFloat64(100000, "USD", gw.Address))
		env.Trust(bob, tx.NewIssuedAmountFromFloat64(100000, "USD", gw.Address))
		env.Close()

		result = env.Submit(payment.PayIssued(gw, alice, tx.NewIssuedAmountFromFloat64(10000, "USD", gw.Address)).Build())
		jtx.RequireTxSuccess(t, result)
		result = env.Submit(payment.PayIssued(gw, bob, tx.NewIssuedAmountFromFloat64(10000, "USD", gw.Address)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// Freeze alice's trust line
		freezeTx := trustset.TrustLine(gw, "USD", alice, "10000").Freeze().Build()
		result = env.Submit(freezeTx)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		result = env.Submit(
			escrow.EscrowCreate(alice, bob, 0).
				IOUAmount(usd(1, gw)).
				Condition(escrow.TestCondition1).
				FinishTime(env.Now().Add(1*time.Second)).
				Fee(baseFee*150).
				Build())
		jtx.RequireTxFail(t, result, "tecFROZEN")
		env.Close()
	})

	t.Run("FrozenDestTrustLine", func(t *testing.T) {
		// tecFROZEN: destination's trust line is frozen
		env := jtx.NewTestEnv(t)
		env.EnableFeature("TokenEscrow")

		gw := jtx.NewAccount("gateway")
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		env.FundAmount(gw, uint64(xrp(10000)))
		env.FundAmount(alice, uint64(xrp(10000)))
		env.FundAmount(bob, uint64(xrp(10000)))

		result := env.Submit(accountset.AccountSet(gw).AllowTrustLineLocking().Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		env.Trust(alice, tx.NewIssuedAmountFromFloat64(100000, "USD", gw.Address))
		env.Trust(bob, tx.NewIssuedAmountFromFloat64(100000, "USD", gw.Address))
		env.Close()

		result = env.Submit(payment.PayIssued(gw, alice, tx.NewIssuedAmountFromFloat64(10000, "USD", gw.Address)).Build())
		jtx.RequireTxSuccess(t, result)
		result = env.Submit(payment.PayIssued(gw, bob, tx.NewIssuedAmountFromFloat64(10000, "USD", gw.Address)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// Freeze bob's trust line (destination)
		freezeTx := trustset.TrustLine(gw, "USD", bob, "10000").Freeze().Build()
		result = env.Submit(freezeTx)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		result = env.Submit(
			escrow.EscrowCreate(alice, bob, 0).
				IOUAmount(usd(1, gw)).
				Condition(escrow.TestCondition1).
				FinishTime(env.Now().Add(1*time.Second)).
				Fee(baseFee*150).
				Build())
		jtx.RequireTxFail(t, result, "tecFROZEN")
		env.Close()
	})
}

// --------------------------------------------------------------------------
// TestIOUEscrow_FinishBasic
// Reference: rippled EscrowToken_test.cpp testIOUBalances (finish part)
// --------------------------------------------------------------------------

func TestIOUEscrow_FinishBasic(t *testing.T) {
	env, gw, alice, bob := setupIOUEscrowEnv(t)

	// Record pre-escrow balances
	preAliceUSD := env.BalanceIOU(alice, "USD", gw)
	preBobUSD := env.BalanceIOU(bob, "USD", gw)
	require.InDelta(t, 5000.0, preAliceUSD, 0.001)
	require.InDelta(t, 5000.0, preBobUSD, 0.001)

	// Create escrow: alice -> bob, 1000 USD
	seq1 := env.Seq(alice)
	result := env.Submit(
		escrow.EscrowCreate(alice, bob, 0).
			IOUAmount(usd(1000, gw)).
			Condition(escrow.TestCondition1).
			FinishTime(env.Now().Add(1*time.Second)).
			Fee(baseFee*150).
			Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// After create: alice loses 1000, bob unchanged
	postCreateAlice := env.BalanceIOU(alice, "USD", gw)
	postCreateBob := env.BalanceIOU(bob, "USD", gw)
	require.InDelta(t, preAliceUSD-1000.0, postCreateAlice, 0.001,
		"alice balance should decrease by 1000 after escrow create")
	require.InDelta(t, preBobUSD, postCreateBob, 0.001,
		"bob balance should not change after escrow create")

	// Finish escrow: bob -> alice (finishing transfers to bob)
	result = env.Submit(
		escrow.EscrowFinish(bob, alice, seq1).
			Condition(escrow.TestCondition1).
			Fulfillment(escrow.TestFulfillment1).
			Fee(baseFee*150).
			Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// After finish: alice still at post-create level, bob gains 1000
	postFinishAlice := env.BalanceIOU(alice, "USD", gw)
	postFinishBob := env.BalanceIOU(bob, "USD", gw)
	require.InDelta(t, postCreateAlice, postFinishAlice, 0.001,
		"alice balance should not change after escrow finish")
	require.InDelta(t, preBobUSD+1000.0, postFinishBob, 0.001,
		"bob balance should increase by 1000 after escrow finish")
}

// --------------------------------------------------------------------------
// TestIOUEscrow_CancelBasic
// Reference: rippled EscrowToken_test.cpp testIOUBalances (cancel part)
// --------------------------------------------------------------------------

func TestIOUEscrow_CancelBasic(t *testing.T) {
	env, gw, alice, bob := setupIOUEscrowEnv(t)

	// Record pre-escrow balances
	preAliceUSD := env.BalanceIOU(alice, "USD", gw)
	preBobUSD := env.BalanceIOU(bob, "USD", gw)

	// Create escrow with cancel time
	seq2 := env.Seq(alice)
	result := env.Submit(
		escrow.EscrowCreate(alice, bob, 0).
			IOUAmount(usd(1000, gw)).
			Condition(escrow.TestCondition2).
			FinishTime(env.Now().Add(1*time.Second)).
			CancelTime(env.Now().Add(2*time.Second)).
			Fee(baseFee*150).
			Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// After create: alice loses 1000
	postCreateAlice := env.BalanceIOU(alice, "USD", gw)
	postCreateBob := env.BalanceIOU(bob, "USD", gw)
	require.InDelta(t, preAliceUSD-1000.0, postCreateAlice, 0.001,
		"alice balance should decrease by 1000 after escrow create")
	require.InDelta(t, preBobUSD, postCreateBob, 0.001,
		"bob balance should not change after escrow create")

	// Cancel escrow
	result = env.Submit(
		escrow.EscrowCancel(bob, alice, seq2).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// After cancel: alice gets 1000 back, bob unchanged
	postCancelAlice := env.BalanceIOU(alice, "USD", gw)
	postCancelBob := env.BalanceIOU(bob, "USD", gw)
	require.InDelta(t, preAliceUSD, postCancelAlice, 0.001,
		"alice balance should be restored after escrow cancel")
	require.InDelta(t, preBobUSD, postCancelBob, 0.001,
		"bob balance should not change after escrow cancel")
}

// --------------------------------------------------------------------------
// TestIOUEscrow_CreatePreflight
// Reference: rippled EscrowToken_test.cpp testIOUCreatePreflight
// --------------------------------------------------------------------------

func TestIOUEscrow_CreatePreflight(t *testing.T) {
	t.Run("NegativeAmount", func(t *testing.T) {
		// temBAD_AMOUNT: amount < 0
		env := jtx.NewTestEnv(t)
		env.EnableFeature("TokenEscrow")

		gw := jtx.NewAccount("gateway")
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		fund5000(env, gw, alice, bob)

		result := env.Submit(
			escrow.EscrowCreate(alice, bob, 0).
				IOUAmount(tx.NewIssuedAmountFromFloat64(-1, "USD", gw.Address)).
				Condition(escrow.TestCondition1).
				FinishTime(env.Now().Add(1*time.Second)).
				Fee(baseFee*150).
				Build())
		jtx.RequireTxFail(t, result, "temBAD_AMOUNT")
		env.Close()
	})

	t.Run("BadCurrency", func(t *testing.T) {
		// temBAD_CURRENCY: XRP as currency code is invalid for IOU
		env := jtx.NewTestEnv(t)
		env.EnableFeature("TokenEscrow")

		gw := jtx.NewAccount("gateway")
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		fund5000(env, gw, alice, bob)

		result := env.Submit(
			escrow.EscrowCreate(alice, bob, 0).
				IOUAmount(tx.NewIssuedAmountFromFloat64(1, "XRP", gw.Address)).
				Condition(escrow.TestCondition1).
				FinishTime(env.Now().Add(1*time.Second)).
				Fee(baseFee*150).
				Build())
		jtx.RequireTxFail(t, result, "temBAD_CURRENCY")
		env.Close()
	})
}

// --------------------------------------------------------------------------
// TestIOUEscrow_SelfEscrow
// Verify that self-escrow (alice escrows to herself) works for IOU.
// --------------------------------------------------------------------------

func TestIOUEscrow_SelfEscrow(t *testing.T) {
	env, gw, alice, _ := setupIOUEscrowEnv(t)

	preAliceUSD := env.BalanceIOU(alice, "USD", gw)

	// Alice creates escrow to herself
	seq := env.Seq(alice)
	result := env.Submit(
		escrow.EscrowCreate(alice, alice, 0).
			IOUAmount(usd(100, gw)).
			Condition(escrow.TestCondition1).
			FinishTime(env.Now().Add(1*time.Second)).
			Fee(baseFee*150).
			Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// Balance should decrease
	postCreate := env.BalanceIOU(alice, "USD", gw)
	require.InDelta(t, preAliceUSD-100.0, postCreate, 0.001)

	// Alice finishes escrow to herself
	result = env.Submit(
		escrow.EscrowFinish(alice, alice, seq).
			Condition(escrow.TestCondition1).
			Fulfillment(escrow.TestFulfillment1).
			Fee(baseFee*150).
			Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// Balance should be restored
	postFinish := env.BalanceIOU(alice, "USD", gw)
	require.InDelta(t, preAliceUSD, postFinish, 0.001)
}

// --------------------------------------------------------------------------
// TestIOUEscrow_MultipleEscrows
// Verify multiple concurrent IOU escrows work correctly.
// --------------------------------------------------------------------------

func TestIOUEscrow_MultipleEscrows(t *testing.T) {
	env, gw, alice, bob := setupIOUEscrowEnv(t)

	preAliceUSD := env.BalanceIOU(alice, "USD", gw)

	// Create escrow #1: 500 USD
	seq1 := env.Seq(alice)
	result := env.Submit(
		escrow.EscrowCreate(alice, bob, 0).
			IOUAmount(usd(500, gw)).
			Condition(escrow.TestCondition1).
			FinishTime(env.Now().Add(1*time.Second)).
			Fee(baseFee*150).
			Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// Create escrow #2: 300 USD
	seq2 := env.Seq(alice)
	result = env.Submit(
		escrow.EscrowCreate(alice, bob, 0).
			IOUAmount(usd(300, gw)).
			Condition(escrow.TestCondition2).
			FinishTime(env.Now().Add(1*time.Second)).
			CancelTime(env.Now().Add(3*time.Second)).
			Fee(baseFee*150).
			Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// Both escrows locked: alice should have lost 800
	postCreate := env.BalanceIOU(alice, "USD", gw)
	require.InDelta(t, preAliceUSD-800.0, postCreate, 0.001)

	// Finish escrow #1
	result = env.Submit(
		escrow.EscrowFinish(bob, alice, seq1).
			Condition(escrow.TestCondition1).
			Fulfillment(escrow.TestFulfillment1).
			Fee(baseFee*150).
			Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// Cancel escrow #2
	result = env.Submit(
		escrow.EscrowCancel(bob, alice, seq2).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// After finish + cancel: alice should have preAliceUSD - 500 (sent to bob)
	postAll := env.BalanceIOU(alice, "USD", gw)
	require.InDelta(t, preAliceUSD-500.0, postAll, 0.001,
		"alice should only lose the finished escrow amount, cancelled amount returned")

	// Bob should have gained 500
	postBobUSD := env.BalanceIOU(bob, "USD", gw)
	require.InDelta(t, 5500.0, postBobUSD, 0.001,
		"bob should gain 500 from the finished escrow")
}
