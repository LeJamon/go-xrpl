// Package escrow_test contains integration tests for IOU escrow (token escrow) behavior.
// Tests ported from rippled's EscrowToken_test.cpp (src/test/app/EscrowToken_test.cpp).
package escrow_test

import (
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/accountset"
	"github.com/LeJamon/go-xrpl/internal/testing/escrow"
	"github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/internal/testing/trustset"
	"github.com/LeJamon/go-xrpl/internal/tx"
	sponsortx "github.com/LeJamon/go-xrpl/internal/tx/sponsor"
	"github.com/LeJamon/go-xrpl/keylet"
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
	return setupIOUEscrowEnvWithCleanupFix(t, true)
}

func setupIOUEscrowEnvWithCleanupFix(t *testing.T, cleanupFixEnabled bool) (*jtx.TestEnv, *jtx.Account, *jtx.Account, *jtx.Account) {
	t.Helper()

	env := jtx.NewTestEnv(t)
	env.EnableFeature("TokenEscrow")
	if !cleanupFixEnabled {
		env.DisableFeature("fixCleanup3_2_0")
	}

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
func usd(value int64, gw *jtx.Account) tx.Amount {
	return tx.NewIssuedAmount(value, 0, "USD", gw.Address)
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
				Condition(escrow.TestCondition1()).
				FinishTime(env.Now().Add(1 * time.Second)).
				Fee(env.BaseFee() * 150).
				Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// Finish escrow: should succeed
		result = env.Submit(
			escrow.EscrowFinish(bob, alice, seq1).
				Condition(escrow.TestCondition1()).
				Fulfillment(escrow.TestFulfillment1()).
				Fee(env.BaseFee() * 150).
				Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// Create escrow with condition + cancel time: should succeed
		seq2 := env.Seq(alice)
		result = env.Submit(
			escrow.EscrowCreate(alice, bob, 0).
				IOUAmount(usd(1000, gw)).
				Condition(escrow.TestCondition2()).
				FinishTime(env.Now().Add(1 * time.Second)).
				CancelTime(env.Now().Add(2 * time.Second)).
				Fee(env.BaseFee() * 150).
				Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// Cancel escrow: should succeed
		result = env.Submit(
			escrow.EscrowCancel(bob, alice, seq2).
				Fee(env.BaseFee() * 150).
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
		env.Close()

		// Create IOU escrow: should fail with temBAD_AMOUNT
		result = env.Submit(
			escrow.EscrowCreate(alice, bob, 0).
				IOUAmount(usd(1000, gw)).
				Condition(escrow.TestCondition1()).
				FinishTime(env.Now().Add(1 * time.Second)).
				Fee(env.BaseFee() * 150).
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
				Condition(escrow.TestCondition1()).
				Fulfillment(escrow.TestFulfillment1()).
				Fee(env.BaseFee() * 150).
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
			Condition(escrow.TestCondition1()).
			FinishTime(env.Now().Add(1 * time.Second)).
			Fee(env.BaseFee() * 150).
			Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// Create Escrow #2 (time-based with cancel)
	seq2 := env.Seq(alice)
	result = env.Submit(
		escrow.EscrowCreate(alice, bob, 0).
			IOUAmount(usd(1000, gw)).
			FinishTime(env.Now().Add(1 * time.Second)).
			CancelTime(env.Now().Add(3 * time.Second)).
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
			Condition(escrow.TestCondition1()).
			FinishTime(env.Now().Add(1 * time.Second)).
			Fee(env.BaseFee() * 150).
			Build())
	jtx.RequireTxFail(t, result, "tecNO_PERMISSION")
	env.Close()

	// Can still finish escrow #1 (created before flag was cleared)
	result = env.Submit(
		escrow.EscrowFinish(bob, alice, seq1).
			Condition(escrow.TestCondition1()).
			Fulfillment(escrow.TestFulfillment1()).
			Fee(env.BaseFee() * 150).
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
				Condition(escrow.TestCondition1()).
				FinishTime(env.Now().Add(1 * time.Second)).
				Fee(env.BaseFee() * 150).
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
				Condition(escrow.TestCondition1()).
				FinishTime(env.Now().Add(1 * time.Second)).
				Fee(env.BaseFee() * 150).
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
				Condition(escrow.TestCondition1()).
				FinishTime(env.Now().Add(1 * time.Second)).
				Fee(env.BaseFee() * 150).
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
				Condition(escrow.TestCondition1()).
				FinishTime(env.Now().Add(1 * time.Second)).
				Fee(env.BaseFee() * 150).
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
				Condition(escrow.TestCondition1()).
				FinishTime(env.Now().Add(1 * time.Second)).
				Fee(env.BaseFee() * 150).
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
				Condition(escrow.TestCondition1()).
				FinishTime(env.Now().Add(1 * time.Second)).
				Fee(env.BaseFee() * 150).
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
				Condition(escrow.TestCondition1()).
				FinishTime(env.Now().Add(1 * time.Second)).
				Fee(env.BaseFee() * 150).
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
	for _, cleanupFixEnabled := range []bool{false, true} {
		name := "WithoutFixCleanup3_2_0"
		if cleanupFixEnabled {
			name = "WithFixCleanup3_2_0"
		}
		t.Run(name, func(t *testing.T) {
			testIOUEscrowFinishBasic(t, cleanupFixEnabled)
		})
	}
}

func testIOUEscrowFinishBasic(t *testing.T, cleanupFixEnabled bool) {
	t.Helper()
	env, gw, alice, bob := setupIOUEscrowEnvWithCleanupFix(t, cleanupFixEnabled)

	require.Equal(t, usd(5000, gw), *env.IOUBalance(alice, gw, "USD"))
	require.Equal(t, usd(5000, gw), *env.IOUBalance(bob, gw, "USD"))
	aliceOwnerCount := env.OwnerCount(alice)
	bobOwnerCount := env.OwnerCount(bob)
	gwOwnerCount := env.OwnerCount(gw)
	aliceDirEntries := ownerDirEntryCount(t, env, alice)
	bobDirEntries := ownerDirEntryCount(t, env, bob)
	gwDirEntries := ownerDirEntryCount(t, env, gw)

	// Create escrow: alice -> bob, 1000 USD
	seq1 := env.Seq(alice)
	result := env.Submit(
		escrow.EscrowCreate(alice, bob, 0).
			IOUAmount(usd(1000, gw)).
			Condition(escrow.TestCondition1()).
			FinishTime(env.Now().Add(1 * time.Second)).
			Fee(env.BaseFee() * 150).
			Build())
	jtx.RequireTxSuccess(t, result)
	escrowKey := keylet.Escrow(alice.ID, seq1)
	requireEscrowMetaNode(t, result, "CreatedNode", escrowKey)
	require.True(t, env.LedgerEntryExists(escrowKey))
	requireOwnerDirContains(t, env, alice, escrowKey.Key, true)
	requireOwnerDirContains(t, env, bob, escrowKey.Key, true)
	requireOwnerDirContains(t, env, gw, escrowKey.Key, true)
	require.Equal(t, aliceDirEntries+1, ownerDirEntryCount(t, env, alice))
	require.Equal(t, bobDirEntries+1, ownerDirEntryCount(t, env, bob))
	require.Equal(t, gwDirEntries+1, ownerDirEntryCount(t, env, gw))
	require.Equal(t, aliceOwnerCount+1, env.OwnerCount(alice))
	require.Equal(t, bobOwnerCount, env.OwnerCount(bob))
	require.Equal(t, gwOwnerCount, env.OwnerCount(gw))
	env.Close()

	require.Equal(t, usd(4000, gw), *env.IOUBalance(alice, gw, "USD"))
	require.Equal(t, usd(5000, gw), *env.IOUBalance(bob, gw, "USD"))
	requireIOUIssuerAccounting(t, env, gw, alice, bob, escrowKey, usd(9000, gw), usd(1000, gw))

	// Finish escrow: bob -> alice (finishing transfers to bob)
	result = env.Submit(
		escrow.EscrowFinish(bob, alice, seq1).
			Condition(escrow.TestCondition1()).
			Fulfillment(escrow.TestFulfillment1()).
			Fee(env.BaseFee() * 150).
			Build())
	jtx.RequireTxSuccess(t, result)
	requireEscrowMetaNode(t, result, "DeletedNode", escrowKey)
	env.Close()

	require.False(t, env.LedgerEntryExists(escrowKey))
	requireOwnerDirContains(t, env, alice, escrowKey.Key, false)
	requireOwnerDirContains(t, env, bob, escrowKey.Key, false)
	requireOwnerDirContains(t, env, gw, escrowKey.Key, false)
	require.Equal(t, aliceDirEntries, ownerDirEntryCount(t, env, alice))
	require.Equal(t, bobDirEntries, ownerDirEntryCount(t, env, bob))
	require.Equal(t, gwDirEntries, ownerDirEntryCount(t, env, gw))
	require.Equal(t, aliceOwnerCount, env.OwnerCount(alice))
	require.Equal(t, bobOwnerCount, env.OwnerCount(bob))
	require.Equal(t, gwOwnerCount, env.OwnerCount(gw))
	require.Equal(t, usd(4000, gw), *env.IOUBalance(alice, gw, "USD"))
	require.Equal(t, usd(6000, gw), *env.IOUBalance(bob, gw, "USD"))
	requireIOUIssuerAccounting(t, env, gw, alice, bob, escrowKey, usd(10000, gw), usd(0, gw))
}

func TestIOUEscrow_LockedRate(t *testing.T) {
	tests := []struct {
		name        string
		currentRate uint32
		wantCredit  int64
	}{
		{name: "higher current rate uses locked rate", currentRate: 1_260_000_000, wantCredit: 100},
		{name: "lower current rate uses current rate", currentRate: 0, wantCredit: 125},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env, gw, alice, bob := setupIOUEscrowEnv(t)
			env.SetTransferRate(gw, 1_250_000_000)
			env.Close()

			require.Equal(t, usd(5000, gw), *env.IOUBalance(bob, gw, "USD"))
			seq := env.Seq(alice)
			result := env.Submit(
				escrow.EscrowCreate(alice, bob, 0).
					IOUAmount(usd(125, gw)).
					Condition(escrow.TestCondition1()).
					FinishTime(env.Now().Add(time.Second)).
					Fee(env.BaseFee() * 150).
					Build())
			jtx.RequireTxSuccess(t, result)
			env.Close()

			env.SetTransferRate(gw, tt.currentRate)
			env.Close()

			result = env.Submit(
				escrow.EscrowFinish(bob, alice, seq).
					Condition(escrow.TestCondition1()).
					Fulfillment(escrow.TestFulfillment1()).
					Fee(env.BaseFee() * 150).
					Build())
			jtx.RequireTxSuccess(t, result)
			env.Close()

			require.Equal(t, usd(5000+tt.wantCredit, gw), *env.IOUBalance(bob, gw, "USD"))
		})
	}
}

// --------------------------------------------------------------------------
// TestIOUEscrow_CancelBasic
// Reference: rippled EscrowToken_test.cpp testIOUBalances (cancel part)
// --------------------------------------------------------------------------

func TestIOUEscrow_CancelBasic(t *testing.T) {
	for _, cleanupFixEnabled := range []bool{false, true} {
		name := "WithoutFixCleanup3_2_0"
		if cleanupFixEnabled {
			name = "WithFixCleanup3_2_0"
		}
		t.Run(name, func(t *testing.T) {
			testIOUEscrowCancelBasic(t, cleanupFixEnabled)
		})
	}
}

func testIOUEscrowCancelBasic(t *testing.T, cleanupFixEnabled bool) {
	t.Helper()
	env, gw, alice, bob := setupIOUEscrowEnvWithCleanupFix(t, cleanupFixEnabled)

	aliceOwnerCount := env.OwnerCount(alice)
	bobOwnerCount := env.OwnerCount(bob)
	gwOwnerCount := env.OwnerCount(gw)
	aliceDirEntries := ownerDirEntryCount(t, env, alice)
	bobDirEntries := ownerDirEntryCount(t, env, bob)
	gwDirEntries := ownerDirEntryCount(t, env, gw)

	// Create escrow with cancel time
	seq2 := env.Seq(alice)
	result := env.Submit(
		escrow.EscrowCreate(alice, bob, 0).
			IOUAmount(usd(1000, gw)).
			Condition(escrow.TestCondition2()).
			FinishTime(env.Now().Add(1 * time.Second)).
			CancelTime(env.Now().Add(2 * time.Second)).
			Fee(env.BaseFee() * 150).
			Build())
	jtx.RequireTxSuccess(t, result)
	escrowKey := keylet.Escrow(alice.ID, seq2)
	requireEscrowMetaNode(t, result, "CreatedNode", escrowKey)
	require.True(t, env.LedgerEntryExists(escrowKey))
	requireOwnerDirContains(t, env, alice, escrowKey.Key, true)
	requireOwnerDirContains(t, env, bob, escrowKey.Key, true)
	requireOwnerDirContains(t, env, gw, escrowKey.Key, true)
	require.Equal(t, aliceDirEntries+1, ownerDirEntryCount(t, env, alice))
	require.Equal(t, bobDirEntries+1, ownerDirEntryCount(t, env, bob))
	require.Equal(t, gwDirEntries+1, ownerDirEntryCount(t, env, gw))
	require.Equal(t, aliceOwnerCount+1, env.OwnerCount(alice))
	require.Equal(t, bobOwnerCount, env.OwnerCount(bob))
	require.Equal(t, gwOwnerCount, env.OwnerCount(gw))
	env.Close()

	require.Equal(t, usd(4000, gw), *env.IOUBalance(alice, gw, "USD"))
	require.Equal(t, usd(5000, gw), *env.IOUBalance(bob, gw, "USD"))
	requireIOUIssuerAccounting(t, env, gw, alice, bob, escrowKey, usd(9000, gw), usd(1000, gw))

	// Cancel escrow
	result = env.Submit(
		escrow.EscrowCancel(bob, alice, seq2).Build())
	jtx.RequireTxSuccess(t, result)
	requireEscrowMetaNode(t, result, "DeletedNode", escrowKey)
	env.Close()

	require.False(t, env.LedgerEntryExists(escrowKey))
	requireOwnerDirContains(t, env, alice, escrowKey.Key, false)
	requireOwnerDirContains(t, env, bob, escrowKey.Key, false)
	requireOwnerDirContains(t, env, gw, escrowKey.Key, false)
	require.Equal(t, aliceDirEntries, ownerDirEntryCount(t, env, alice))
	require.Equal(t, bobDirEntries, ownerDirEntryCount(t, env, bob))
	require.Equal(t, gwDirEntries, ownerDirEntryCount(t, env, gw))
	require.Equal(t, aliceOwnerCount, env.OwnerCount(alice))
	require.Equal(t, bobOwnerCount, env.OwnerCount(bob))
	require.Equal(t, gwOwnerCount, env.OwnerCount(gw))
	require.Equal(t, usd(5000, gw), *env.IOUBalance(alice, gw, "USD"))
	require.Equal(t, usd(5000, gw), *env.IOUBalance(bob, gw, "USD"))
	requireIOUIssuerAccounting(t, env, gw, alice, bob, escrowKey, usd(10000, gw), usd(0, gw))
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
				IOUAmount(tx.NewIssuedAmount(-1, 0, "USD", gw.Address)).
				Condition(escrow.TestCondition1()).
				FinishTime(env.Now().Add(1 * time.Second)).
				Fee(env.BaseFee() * 150).
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
				Condition(escrow.TestCondition1()).
				FinishTime(env.Now().Add(1 * time.Second)).
				Fee(env.BaseFee() * 150).
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

	require.Equal(t, usd(5000, gw), *env.IOUBalance(alice, gw, "USD"))
	aliceOwnerCount := env.OwnerCount(alice)

	// Alice creates escrow to herself
	seq := env.Seq(alice)
	result := env.Submit(
		escrow.EscrowCreate(alice, alice, 0).
			IOUAmount(usd(100, gw)).
			Condition(escrow.TestCondition1()).
			FinishTime(env.Now().Add(1 * time.Second)).
			Fee(env.BaseFee() * 150).
			Build())
	jtx.RequireTxSuccess(t, result)
	escrowKey := keylet.Escrow(alice.ID, seq)
	requireEscrowMetaNode(t, result, "CreatedNode", escrowKey)
	require.True(t, env.LedgerEntryExists(escrowKey))
	requireOwnerDirContains(t, env, alice, escrowKey.Key, true)
	require.Equal(t, aliceOwnerCount+1, env.OwnerCount(alice))
	env.Close()

	require.Equal(t, usd(4900, gw), *env.IOUBalance(alice, gw, "USD"))

	// Alice finishes escrow to herself
	result = env.Submit(
		escrow.EscrowFinish(alice, alice, seq).
			Condition(escrow.TestCondition1()).
			Fulfillment(escrow.TestFulfillment1()).
			Fee(env.BaseFee() * 150).
			Build())
	jtx.RequireTxSuccess(t, result)
	requireEscrowMetaNode(t, result, "DeletedNode", escrowKey)
	env.Close()

	require.False(t, env.LedgerEntryExists(escrowKey))
	requireOwnerDirContains(t, env, alice, escrowKey.Key, false)
	require.Equal(t, aliceOwnerCount, env.OwnerCount(alice))
	require.Equal(t, usd(5000, gw), *env.IOUBalance(alice, gw, "USD"))
}

// --------------------------------------------------------------------------
// TestIOUEscrow_MultipleEscrows
// Verify multiple concurrent IOU escrows work correctly.
// --------------------------------------------------------------------------

func TestIOUEscrow_MultipleEscrows(t *testing.T) {
	env, gw, alice, bob := setupIOUEscrowEnv(t)

	require.Equal(t, usd(5000, gw), *env.IOUBalance(alice, gw, "USD"))

	// Create escrow #1: 500 USD
	seq1 := env.Seq(alice)
	result := env.Submit(
		escrow.EscrowCreate(alice, bob, 0).
			IOUAmount(usd(500, gw)).
			Condition(escrow.TestCondition1()).
			FinishTime(env.Now().Add(1 * time.Second)).
			Fee(env.BaseFee() * 150).
			Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// Create escrow #2: 300 USD
	seq2 := env.Seq(alice)
	result = env.Submit(
		escrow.EscrowCreate(alice, bob, 0).
			IOUAmount(usd(300, gw)).
			Condition(escrow.TestCondition2()).
			FinishTime(env.Now().Add(1 * time.Second)).
			CancelTime(env.Now().Add(3 * time.Second)).
			Fee(env.BaseFee() * 150).
			Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// Both escrows locked: alice should have lost 800
	require.Equal(t, usd(4200, gw), *env.IOUBalance(alice, gw, "USD"))

	// Finish escrow #1
	result = env.Submit(
		escrow.EscrowFinish(bob, alice, seq1).
			Condition(escrow.TestCondition1()).
			Fulfillment(escrow.TestFulfillment1()).
			Fee(env.BaseFee() * 150).
			Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// Cancel escrow #2
	result = env.Submit(
		escrow.EscrowCancel(bob, alice, seq2).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// After finish + cancel: alice should have preAliceUSD - 500 (sent to bob)
	require.Equal(t, usd(4500, gw), *env.IOUBalance(alice, gw, "USD"))
	require.Equal(t, usd(5500, gw), *env.IOUBalance(bob, gw, "USD"))
}

// --------------------------------------------------------------------------
// TestIOUEscrow_FinishTrustLine
// Reference: rippled EscrowToken_test.cpp testIOUFinishDoApply
//
// EscrowFinish auto-creates the destination's trust line only when the
// destination itself submits the finish (createAsset); third-party
// finishers get tecNO_LINE / tecLIMIT_EXCEEDED instead.
// --------------------------------------------------------------------------

func TestIOUEscrow_FinishTrustLine(t *testing.T) {
	// reserveShortBob asks newFinishEnv to fund bob with one drop less than
	// the reserve needed to add a trust line (accountReserve(0) + increment
	// - 1, like rippled's testIOUFinishDoApply).
	const reserveShortBob = uint64(0)

	// newFinishEnv funds gw and alice, flags gw with AllowTrustLineLocking,
	// gives alice an aliceLimit USD trust line fully funded to aliceBalance,
	// and funds bob with bobDrops XRP and NO USD trust line.
	newFinishEnv := func(t *testing.T, bobDrops uint64, aliceLimit string, aliceBalance int64) (*jtx.TestEnv, *jtx.Account, *jtx.Account, *jtx.Account) {
		t.Helper()
		env := jtx.NewTestEnv(t)
		env.EnableFeature("TokenEscrow")

		gw := jtx.NewAccount("gateway")
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")

		if bobDrops == reserveShortBob {
			bobDrops = env.ReserveBase() + env.ReserveIncrement() - 1
		}

		fund5000(env, gw, alice)
		env.FundAmount(bob, bobDrops)
		env.Close()

		result := env.Submit(accountset.AccountSet(gw).AllowTrustLineLocking().Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		result = env.Submit(trustset.TrustLine(alice, "USD", gw, aliceLimit).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		result = env.Submit(payment.PayIssued(gw, alice, usd(aliceBalance, gw)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		return env, gw, alice, bob
	}

	createEscrow := func(t *testing.T, env *jtx.TestEnv, alice, bob *jtx.Account, gw *jtx.Account, amount int64) uint32 {
		t.Helper()
		seq := env.Seq(alice)
		result := env.Submit(
			escrow.EscrowCreate(alice, bob, 0).
				IOUAmount(usd(amount, gw)).
				Condition(escrow.TestCondition1()).
				FinishTime(env.Now().Add(1 * time.Second)).
				Fee(env.BaseFee() * 150).
				Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
		return seq
	}

	finish := func(env *jtx.TestEnv, submitter, owner *jtx.Account, seq uint32) jtx.TxResult {
		return env.Submit(
			escrow.EscrowFinish(submitter, owner, seq).
				Condition(escrow.TestCondition1()).
				Fulfillment(escrow.TestFulfillment1()).
				Fee(env.BaseFee() * 150).
				Build())
	}

	t.Run("InsufficientReserveToCreateLine", func(t *testing.T) {
		// tecNO_LINE_INSUF_RESERVE: bob cannot cover the new line's reserve
		env, gw, alice, bob := newFinishEnv(t, reserveShortBob, "10000", 10000)
		seq := createEscrow(t, env, alice, bob, gw, 1)
		before := captureIOUEscrowSnapshot(t, env, gw, alice, bob, seq)

		jtx.RequireTxFail(t, finish(env, bob, alice, seq), "tecNO_LINE_INSUF_RESERVE")
		requireIOUEscrowSnapshot(t, env, gw, alice, bob, seq, before)
	})

	t.Run("ThirdPartyFinishWithoutLine", func(t *testing.T) {
		// tecNO_LINE: alice submits; the destination line is not created
		env, gw, alice, bob := newFinishEnv(t, uint64(jtx.XRP(5000)), "10000", 10000)
		seq := createEscrow(t, env, alice, bob, gw, 1)
		before := captureIOUEscrowSnapshot(t, env, gw, alice, bob, seq)

		jtx.RequireTxFail(t, finish(env, alice, alice, seq), "tecNO_LINE")
		requireIOUEscrowSnapshot(t, env, gw, alice, bob, seq, before)
	})

	t.Run("ThirdPartyFinishLimitExceeded", func(t *testing.T) {
		// tecLIMIT_EXCEEDED: alice submits; bob's limit < balance + amount
		env, gw, alice, bob := newFinishEnv(t, uint64(jtx.XRP(5000)), "1000", 1000)
		result := env.Submit(trustset.TrustLine(bob, "USD", gw, "1000").Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		seq := createEscrow(t, env, alice, bob, gw, 5)

		result = env.Submit(trustset.TrustLine(bob, "USD", gw, "1").Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
		before := captureIOUEscrowSnapshot(t, env, gw, alice, bob, seq)

		jtx.RequireTxFail(t, finish(env, alice, alice, seq), "tecLIMIT_EXCEEDED")
		requireIOUEscrowSnapshot(t, env, gw, alice, bob, seq, before)
	})

	t.Run("DestinationFinishIgnoresLimit", func(t *testing.T) {
		// tesSUCCESS: bob submits; his limit is below balance + amount but
		// the receiver of the funds is not limit-checked, and the limit is
		// left unchanged.
		env, gw, alice, bob := newFinishEnv(t, uint64(jtx.XRP(5000)), "1000", 1000)
		result := env.Submit(trustset.TrustLine(bob, "USD", gw, "1000").Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		seq := createEscrow(t, env, alice, bob, gw, 5)

		result = env.Submit(trustset.TrustLine(bob, "USD", gw, "1").Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		jtx.RequireTxSuccess(t, finish(env, bob, alice, seq))
		env.Close()

		requireTrustLineLimitValue(t, env, bob, gw, "USD", "1")
		require.Equal(t, usd(5, gw), *env.IOUBalance(bob, gw, "USD"))
	})

	t.Run("DestinationFinishCreatesLine", func(t *testing.T) {
		// tesSUCCESS: bob submits with no USD trust line at all; the line is
		// auto-created with a zero limit and credited.
		env, gw, alice, bob := newFinishEnv(t, uint64(jtx.XRP(5000)), "10000", 10000)
		seq := createEscrow(t, env, alice, bob, gw, 1)

		require.False(t, env.TrustLineExists(bob, gw, "USD"))
		jtx.RequireTxSuccess(t, finish(env, bob, alice, seq))
		env.Close()

		require.True(t, env.TrustLineExists(bob, gw, "USD"), "destination trust line must be auto-created")
		requireTrustLineLimitValue(t, env, bob, gw, "USD", "0")
		require.Equal(t, usd(1, gw), *env.IOUBalance(bob, gw, "USD"))
	})

	t.Run("SponsorReserveReleasedBeforeDestinationLineCreate", func(t *testing.T) {
		env, gw, alice, bob := newFinishEnv(t, uint64(jtx.XRP(5000)), "10000", 10000)
		env.EnableFeature("Sponsor")
		reserveSponsor := jtx.NewAccount("escrow-reserve-sponsor")
		env.FundAmount(reserveSponsor, env.ReserveBase()+3*env.ReserveIncrement()+2*env.BaseFee())
		env.Close()

		setRelation := func(sponsee *jtx.Account) {
			remaining := int32(1)
			relation := sponsortx.NewSponsorshipSet(reserveSponsor.Address)
			relation.Sponsee = sponsee.Address
			relation.RemainingOwnerCountDelta = &remaining
			jtx.RequireTxSuccess(t, env.Submit(relation))
		}
		setRelation(alice)
		setRelation(bob)

		seq := env.Seq(alice)
		create := escrow.EscrowCreate(alice, bob, 0).
			IOUAmount(usd(1, gw)).
			Condition(escrow.TestCondition1()).
			FinishTime(env.Now().Add(time.Second)).
			Fee(env.BaseFee() * 150).
			Build()
		create.Sponsor = reserveSponsor.Address
		reserve := tx.SpfSponsorReserve
		create.SponsorFlags = &reserve
		jtx.RequireTxSuccess(t, env.Submit(create))
		env.Close()

		finishTx := escrow.EscrowFinish(bob, alice, seq).
			Condition(escrow.TestCondition1()).
			Fulfillment(escrow.TestFulfillment1()).
			Fee(env.BaseFee() * 150).
			Build()
		finishTx.Sponsor = reserveSponsor.Address
		finishTx.SponsorFlags = &reserve
		jtx.RequireTxSuccess(t, env.Submit(finishTx))
		require.True(t, env.TrustLineExists(bob, gw, "USD"))
	})
}

func requireTrustLineLimitValue(
	t *testing.T,
	env *jtx.TestEnv,
	holder, issuer *jtx.Account,
	currency, want string,
) {
	t.Helper()
	data, err := env.Ledger().Read(keylet.Line(holder.ID, issuer.ID, currency))
	require.NoError(t, err)
	require.NotNil(t, data)
	line, err := state.ParseRippleState(data)
	require.NoError(t, err)
	limit := line.HighLimit
	if keylet.IsLowAccount(holder.ID, issuer.ID) {
		limit = line.LowLimit
	}
	require.Equal(t, want, limit.Value())
}

func requireIOUIssuerAccounting(
	t *testing.T,
	env *jtx.TestEnv,
	issuer, alice, bob *jtx.Account,
	escrowKey keylet.Keylet,
	wantCirculating, wantLocked tx.Amount,
) {
	t.Helper()
	circulating, err := env.IOUBalance(alice, issuer, "USD").Add(*env.IOUBalance(bob, issuer, "USD"))
	require.NoError(t, err)
	require.Equal(t, wantCirculating, circulating)

	if wantLocked.IsZero() {
		require.False(t, env.LedgerEntryExists(escrowKey))
	} else {
		data, readErr := env.LedgerEntry(escrowKey)
		require.NoError(t, readErr)
		escrowEntry, parseErr := state.ParseEscrow(data)
		require.NoError(t, parseErr)
		require.NotNil(t, escrowEntry.IOUAmount)
		require.Equal(t, wantLocked, *escrowEntry.IOUAmount)
	}

	outstanding, err := circulating.Add(wantLocked)
	require.NoError(t, err)
	require.Equal(t, usd(10000, issuer), outstanding)
}

type iouEscrowSnapshot struct {
	escrowData                  []byte
	aliceBalance, bobBalance    tx.Amount
	aliceLine, bobLine          bool
	aliceOwners, bobOwners      uint32
	issuerOwners                uint32
	aliceDir, bobDir, issuerDir int
}

func captureIOUEscrowSnapshot(
	t *testing.T,
	env *jtx.TestEnv,
	issuer, alice, bob *jtx.Account,
	sequence uint32,
) iouEscrowSnapshot {
	t.Helper()
	data, err := env.LedgerEntry(keylet.Escrow(alice.ID, sequence))
	require.NoError(t, err)
	return iouEscrowSnapshot{
		escrowData:   append([]byte(nil), data...),
		aliceBalance: *env.IOUBalance(alice, issuer, "USD"),
		bobBalance:   *env.IOUBalance(bob, issuer, "USD"),
		aliceLine:    env.TrustLineExists(alice, issuer, "USD"),
		bobLine:      env.TrustLineExists(bob, issuer, "USD"),
		aliceOwners:  env.OwnerCount(alice),
		bobOwners:    env.OwnerCount(bob),
		issuerOwners: env.OwnerCount(issuer),
		aliceDir:     ownerDirEntryCount(t, env, alice),
		bobDir:       ownerDirEntryCount(t, env, bob),
		issuerDir:    ownerDirEntryCount(t, env, issuer),
	}
}

func requireIOUEscrowSnapshot(
	t *testing.T,
	env *jtx.TestEnv,
	issuer, alice, bob *jtx.Account,
	sequence uint32,
	want iouEscrowSnapshot,
) {
	t.Helper()
	data, err := env.LedgerEntry(keylet.Escrow(alice.ID, sequence))
	require.NoError(t, err)
	require.Equal(t, want.escrowData, data)
	require.Equal(t, want.aliceBalance, *env.IOUBalance(alice, issuer, "USD"))
	require.Equal(t, want.bobBalance, *env.IOUBalance(bob, issuer, "USD"))
	require.Equal(t, want.aliceLine, env.TrustLineExists(alice, issuer, "USD"))
	require.Equal(t, want.bobLine, env.TrustLineExists(bob, issuer, "USD"))
	require.Equal(t, want.aliceOwners, env.OwnerCount(alice))
	require.Equal(t, want.bobOwners, env.OwnerCount(bob))
	require.Equal(t, want.issuerOwners, env.OwnerCount(issuer))
	require.Equal(t, want.aliceDir, ownerDirEntryCount(t, env, alice))
	require.Equal(t, want.bobDir, ownerDirEntryCount(t, env, bob))
	require.Equal(t, want.issuerDir, ownerDirEntryCount(t, env, issuer))
}
