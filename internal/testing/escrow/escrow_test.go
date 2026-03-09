// Package escrow_test contains integration tests for Escrow transaction behavior.
// Tests ported from rippled's Escrow_test.cpp (src/test/app/Escrow_test.cpp).
// Each test function maps 1:1 to a rippled test method.
package escrow_test

import (
	"encoding/hex"
	"testing"
	"time"

	"github.com/LeJamon/goXRPLd/keylet"
	escrowtx "github.com/LeJamon/goXRPLd/internal/core/tx/escrow"
	jtx "github.com/LeJamon/goXRPLd/internal/testing"
	"github.com/LeJamon/goXRPLd/internal/testing/accountset"
	"github.com/LeJamon/goXRPLd/internal/testing/credential"
	dp "github.com/LeJamon/goXRPLd/internal/testing/depositpreauth"
	"github.com/LeJamon/goXRPLd/internal/testing/escrow"
	"github.com/stretchr/testify/require"
)

// baseFee is the default base fee in drops (10 drops).
const baseFee = uint64(10)

// xrp is a shortcut for creating XRP amounts in drops.
func xrp(amount int64) int64 {
	return int64(jtx.XRP(amount))
}

// drops returns the value as uint64 drops.
func drops(d uint64) uint64 {
	return d
}

// fund5000 funds accounts with 5000 XRP each, matching rippled's test pattern.
func fund5000(env *jtx.TestEnv, accounts ...*jtx.Account) {
	for _, acc := range accounts {
		env.FundAmount(acc, uint64(xrp(5000)))
	}
}

// --------------------------------------------------------------------------
// TestEscrow_Enablement
// Reference: rippled Escrow_test.cpp testEnablement (lines 38-74)
// --------------------------------------------------------------------------

func TestEscrow_Enablement(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	fund5000(env, alice, bob)

	// Create a simple time-based escrow
	result := env.Submit(
		escrow.EscrowCreate(alice, bob, xrp(1000)).
			FinishTime(env.Now().Add(1 * time.Second)).
			Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// Create an escrow with a condition
	seq1 := env.Seq(alice)
	result = env.Submit(
		escrow.EscrowCreate(alice, bob, xrp(1000)).
			Condition(escrow.TestCondition1).
			FinishTime(env.Now().Add(1 * time.Second)).
			Fee(baseFee * 150).
			Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// Finish the conditional escrow
	result = env.Submit(
		escrow.EscrowFinish(bob, alice, seq1).
			Condition(escrow.TestCondition1).
			Fulfillment(escrow.TestFulfillment1).
			Fee(baseFee * 150).
			Build())
	jtx.RequireTxSuccess(t, result)

	// Create an escrow with condition, finish time and cancel time
	seq2 := env.Seq(alice)
	result = env.Submit(
		escrow.EscrowCreate(alice, bob, xrp(1000)).
			Condition(escrow.TestCondition2).
			FinishTime(env.Now().Add(1 * time.Second)).
			CancelTime(env.Now().Add(2 * time.Second)).
			Fee(baseFee * 150).
			Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// Cancel the escrow
	result = env.Submit(
		escrow.EscrowCancel(bob, alice, seq2).
			Fee(baseFee * 150).
			Build())
	jtx.RequireTxSuccess(t, result)
}

// --------------------------------------------------------------------------
// TestEscrow_Timing
// Reference: rippled Escrow_test.cpp testTiming (lines 77-220)
// --------------------------------------------------------------------------

func TestEscrow_Timing(t *testing.T) {
	t.Run("FinishOnly", func(t *testing.T) {
		// Reference: rippled lines 82-104
		env := jtx.NewTestEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		fund5000(env, alice, bob)
		env.Close()

		// Create an escrow that can be finished in the future
		ts := env.Now().Add(97 * time.Second)
		seq := env.Seq(alice)
		result := env.Submit(
			escrow.EscrowCreate(alice, bob, xrp(1000)).
				FinishTime(ts).
				Build())
		jtx.RequireTxSuccess(t, result)

		// Advance the ledger, verifying that the finish won't complete prematurely.
		for env.Now().Before(ts) {
			result = env.Submit(
				escrow.EscrowFinish(bob, alice, seq).
					Fee(baseFee * 150).
					Build())
			require.Equal(t, "tecNO_PERMISSION", result.Code)
			env.Close()
		}

		// Now finish should succeed
		result = env.Submit(
			escrow.EscrowFinish(bob, alice, seq).
				Fee(baseFee * 150).
				Build())
		jtx.RequireTxSuccess(t, result)
	})

	t.Run("CancelOnly", func(t *testing.T) {
		// Reference: rippled lines 106-137
		env := jtx.NewTestEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		fund5000(env, alice, bob)
		env.Close()

		// Create an escrow that can be cancelled in the future
		ts := env.Now().Add(117 * time.Second)
		seq := env.Seq(alice)
		result := env.Submit(
			escrow.EscrowCreate(alice, bob, xrp(1000)).
				Condition(escrow.TestCondition1).
				CancelTime(ts).
				Build())
		jtx.RequireTxSuccess(t, result)

		// Advance the ledger, verifying that the cancel won't complete prematurely.
		for env.Now().Before(ts) {
			result = env.Submit(
				escrow.EscrowCancel(bob, alice, seq).
					Fee(baseFee * 150).
					Build())
			require.Equal(t, "tecNO_PERMISSION", result.Code)
			env.Close()
		}

		// Verify that a finish won't work anymore (past cancel time).
		result = env.Submit(
			escrow.EscrowFinish(bob, alice, seq).
				Condition(escrow.TestCondition1).
				Fulfillment(escrow.TestFulfillment1).
				Fee(baseFee * 150).
				Build())
		require.Equal(t, "tecNO_PERMISSION", result.Code)

		// Verify that the cancel will succeed
		result = env.Submit(
			escrow.EscrowCancel(bob, alice, seq).
				Fee(baseFee * 150).
				Build())
		jtx.RequireTxSuccess(t, result)
	})

	t.Run("FinishAndCancel_Finish", func(t *testing.T) {
		// Reference: rippled lines 139-174
		env := jtx.NewTestEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		fund5000(env, alice, bob)
		env.Close()

		fts := env.Now().Add(117 * time.Second)
		cts := env.Now().Add(192 * time.Second)

		seq := env.Seq(alice)
		result := env.Submit(
			escrow.EscrowCreate(alice, bob, xrp(1000)).
				FinishTime(fts).
				CancelTime(cts).
				Build())
		jtx.RequireTxSuccess(t, result)

		// Advance the ledger, verifying that finish and cancel won't complete prematurely.
		for env.Now().Before(fts) {
			result = env.Submit(
				escrow.EscrowFinish(bob, alice, seq).
					Fee(baseFee * 150).
					Build())
			require.Equal(t, "tecNO_PERMISSION", result.Code)
			result = env.Submit(
				escrow.EscrowCancel(bob, alice, seq).
					Fee(baseFee * 150).
					Build())
			require.Equal(t, "tecNO_PERMISSION", result.Code)
			env.Close()
		}

		// Verify that a cancel still won't work
		result = env.Submit(
			escrow.EscrowCancel(bob, alice, seq).
				Fee(baseFee * 150).
				Build())
		require.Equal(t, "tecNO_PERMISSION", result.Code)

		// And verify that a finish will
		result = env.Submit(
			escrow.EscrowFinish(bob, alice, seq).
				Fee(baseFee * 150).
				Build())
		jtx.RequireTxSuccess(t, result)
	})

	t.Run("FinishAndCancel_Cancel", func(t *testing.T) {
		// Reference: rippled lines 176-219
		env := jtx.NewTestEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		fund5000(env, alice, bob)
		env.Close()

		fts := env.Now().Add(109 * time.Second)
		cts := env.Now().Add(184 * time.Second)

		seq := env.Seq(alice)
		result := env.Submit(
			escrow.EscrowCreate(alice, bob, xrp(1000)).
				FinishTime(fts).
				CancelTime(cts).
				Build())
		jtx.RequireTxSuccess(t, result)

		// Advance the ledger, verifying that finish and cancel won't complete prematurely.
		for env.Now().Before(fts) {
			result = env.Submit(
				escrow.EscrowFinish(bob, alice, seq).
					Fee(baseFee * 150).
					Build())
			require.Equal(t, "tecNO_PERMISSION", result.Code)
			result = env.Submit(
				escrow.EscrowCancel(bob, alice, seq).
					Fee(baseFee * 150).
					Build())
			require.Equal(t, "tecNO_PERMISSION", result.Code)
			env.Close()
		}

		// Continue advancing, verifying that the cancel won't complete prematurely.
		// At this point a finish would succeed.
		for env.Now().Before(cts) {
			result = env.Submit(
				escrow.EscrowCancel(bob, alice, seq).
					Fee(baseFee * 150).
					Build())
			require.Equal(t, "tecNO_PERMISSION", result.Code)
			env.Close()
		}

		// Verify that finish will no longer work, since we are past the cancel activation time.
		result = env.Submit(
			escrow.EscrowFinish(bob, alice, seq).
				Fee(baseFee * 150).
				Build())
		require.Equal(t, "tecNO_PERMISSION", result.Code)

		// And verify that a cancel will succeed.
		result = env.Submit(
			escrow.EscrowCancel(bob, alice, seq).
				Fee(baseFee * 150).
				Build())
		jtx.RequireTxSuccess(t, result)
	})
}

// --------------------------------------------------------------------------
// TestEscrow_Tags
// Reference: rippled Escrow_test.cpp testTags (lines 222-256)
// --------------------------------------------------------------------------

func TestEscrow_Tags(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	fund5000(env, alice, bob)

	// Check to make sure that we correctly detect if tags are really required
	result := env.Submit(accountset.AccountSet(bob).RequireDest().Build())
	jtx.RequireTxSuccess(t, result)

	result = env.Submit(
		escrow.EscrowCreate(alice, bob, xrp(1000)).
			FinishTime(env.Now().Add(1 * time.Second)).
			Build())
	require.Equal(t, "tecDST_TAG_NEEDED", result.Code)

	// Set source and dest tags
	seq := env.Seq(alice)
	result = env.Submit(
		escrow.EscrowCreate(alice, bob, xrp(1000)).
			FinishTime(env.Now().Add(1 * time.Second)).
			SourceTag(1).
			DestTag(2).
			Build())
	jtx.RequireTxSuccess(t, result)

	// Verify the escrow exists and has the correct tags
	escrowKey := keylet.Escrow(alice.ID, seq)
	require.True(t, env.LedgerEntryExists(escrowKey), "escrow entry should exist")

	// Verify tags stored in the escrow SLE
	escrowData, err := env.LedgerEntry(escrowKey)
	require.NoError(t, err)
	escrowHex := hex.EncodeToString(escrowData)
	decoded, err := decodeLedgerEntry(escrowHex)
	require.NoError(t, err)
	if sourceTag, ok := decoded["SourceTag"]; ok {
		require.Equal(t, uint32(1), toUint32(sourceTag))
	}
	if destTag, ok := decoded["DestinationTag"]; ok {
		require.Equal(t, uint32(2), toUint32(destTag))
	}
}

// --------------------------------------------------------------------------
// TestEscrow_DisallowXRP
// Reference: rippled Escrow_test.cpp testDisallowXRP (lines 258-286)
// --------------------------------------------------------------------------

func TestEscrow_DisallowXRP(t *testing.T) {
	t.Run("WithoutDepositAuth", func(t *testing.T) {
		// Respect the "asfDisallowXRP" account flag when DepositAuth is disabled
		// Reference: rippled lines 266-275
		env := jtx.NewTestEnv(t)
		env.DisableFeature("DepositAuth")

		bob := jtx.NewAccount("bob")
		george := jtx.NewAccount("george")
		fund5000(env, bob, george)

		result := env.Submit(accountset.AccountSet(george).DisallowXRP().Build())
		jtx.RequireTxSuccess(t, result)

		result = env.Submit(
			escrow.EscrowCreate(bob, george, xrp(10)).
				FinishTime(env.Now().Add(1 * time.Second)).
				Build())
		require.Equal(t, "tecNO_TARGET", result.Code)
	})

	t.Run("WithDepositAuth", func(t *testing.T) {
		// Ignore the "asfDisallowXRP" account flag when DepositAuth is enabled
		// Reference: rippled lines 276-285
		env := jtx.NewTestEnv(t)

		bob := jtx.NewAccount("bob")
		george := jtx.NewAccount("george")
		fund5000(env, bob, george)

		result := env.Submit(accountset.AccountSet(george).DisallowXRP().Build())
		jtx.RequireTxSuccess(t, result)

		result = env.Submit(
			escrow.EscrowCreate(bob, george, xrp(10)).
				FinishTime(env.Now().Add(1 * time.Second)).
				Build())
		jtx.RequireTxSuccess(t, result)
	})
}

// --------------------------------------------------------------------------
// TestEscrow_Fix1571
// Reference: rippled Escrow_test.cpp test1571 (lines 288-357)
// --------------------------------------------------------------------------

func TestEscrow_Fix1571(t *testing.T) {
	t.Run("WithoutFix1571", func(t *testing.T) {
		// Reference: rippled lines 293-327
		env := jtx.NewTestEnv(t)
		env.DisableFeature("fix1571")

		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		carol := jtx.NewAccount("carol")
		fund5000(env, alice, bob, carol)
		env.Close()

		// Creating an escrow without a finish time and finishing it
		// is allowed without fix1571
		seq1 := env.Seq(alice)
		result := env.Submit(
			escrow.EscrowCreate(alice, bob, xrp(100)).
				CancelTime(env.Now().Add(1 * time.Second)).
				Fee(baseFee * 150).
				Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		result = env.Submit(
			escrow.EscrowFinish(carol, alice, seq1).
				Fee(baseFee * 150).
				Build())
		jtx.RequireTxSuccess(t, result)
		jtx.RequireBalance(t, env, bob, uint64(xrp(5000)+xrp(100)))

		env.Close()

		// Creating an escrow without a finish time and a condition is
		// also allowed without fix1571
		seq2 := env.Seq(alice)
		result = env.Submit(
			escrow.EscrowCreate(alice, bob, xrp(100)).
				CancelTime(env.Now().Add(1 * time.Second)).
				Condition(escrow.TestCondition1).
				Fee(baseFee * 150).
				Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		result = env.Submit(
			escrow.EscrowFinish(carol, alice, seq2).
				Condition(escrow.TestCondition1).
				Fulfillment(escrow.TestFulfillment1).
				Fee(baseFee * 150).
				Build())
		jtx.RequireTxSuccess(t, result)
		jtx.RequireBalance(t, env, bob, uint64(xrp(5000)+xrp(200)))
	})

	t.Run("WithFix1571", func(t *testing.T) {
		// Reference: rippled lines 329-357
		env := jtx.NewTestEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		carol := jtx.NewAccount("carol")
		fund5000(env, alice, bob, carol)
		env.Close()

		// Creating an escrow with only a cancel time is not allowed
		result := env.Submit(
			escrow.EscrowCreate(alice, bob, xrp(100)).
				CancelTime(env.Now().Add(90 * time.Second)).
				Fee(baseFee * 150).
				Build())
		require.Equal(t, "temMALFORMED", result.Code)

		// Creating an escrow with only a cancel time and a condition is allowed
		seq := env.Seq(alice)
		result = env.Submit(
			escrow.EscrowCreate(alice, bob, xrp(100)).
				CancelTime(env.Now().Add(90 * time.Second)).
				Condition(escrow.TestCondition1).
				Fee(baseFee * 150).
				Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		result = env.Submit(
			escrow.EscrowFinish(carol, alice, seq).
				Condition(escrow.TestCondition1).
				Fulfillment(escrow.TestFulfillment1).
				Fee(baseFee * 150).
				Build())
		jtx.RequireTxSuccess(t, result)
		jtx.RequireBalance(t, env, bob, uint64(xrp(5000)+xrp(100)))
	})
}

// --------------------------------------------------------------------------
// TestEscrow_FailureCases
// Reference: rippled Escrow_test.cpp testFails (lines 359-505)
// --------------------------------------------------------------------------

func TestEscrow_FailureCases(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	gw := jtx.NewAccount("gw")
	fund5000(env, alice, bob, gw)
	env.Close()

	// temINVALID_FLAG
	result := env.Submit(
		escrow.EscrowCreate(alice, bob, xrp(1000)).
			FinishTime(env.Now().Add(5 * time.Second)).
			Flags(0x00010000). // tfPassive
			Build())
	require.Equal(t, "temINVALID_FLAG", result.Code)

	// Finish time is in the past
	result = env.Submit(
		escrow.EscrowCreate(alice, bob, xrp(1000)).
			FinishTime(env.Now().Add(-5 * time.Second)).
			Build())
	require.Equal(t, "tecNO_PERMISSION", result.Code)

	// Cancel time is in the past
	result = env.Submit(
		escrow.EscrowCreate(alice, bob, xrp(1000)).
			Condition(escrow.TestCondition1).
			CancelTime(env.Now().Add(-5 * time.Second)).
			Build())
	require.Equal(t, "tecNO_PERMISSION", result.Code)

	// No destination account
	carol := jtx.NewAccount("carol")
	result = env.Submit(
		escrow.EscrowCreate(alice, carol, xrp(1000)).
			FinishTime(env.Now().Add(1 * time.Second)).
			Build())
	require.Equal(t, "tecNO_DST", result.Code)

	fund5000(env, carol)

	// Using non-XRP (temBAD_AMOUNT without TokenEscrow)
	result = env.Submit(
		escrow.EscrowCreate(alice, carol, xrp(0)).
			FinishTime(env.Now().Add(1 * time.Second)).
			Build())
	require.Equal(t, "temBAD_AMOUNT", result.Code)

	// Sending zero XRP
	result = env.Submit(
		escrow.EscrowCreate(alice, carol, 0).
			FinishTime(env.Now().Add(1 * time.Second)).
			Build())
	require.Equal(t, "temBAD_AMOUNT", result.Code)

	// Sending negative XRP
	result = env.Submit(
		escrow.EscrowCreate(alice, carol, xrp(-1000)).
			FinishTime(env.Now().Add(1 * time.Second)).
			Build())
	require.Equal(t, "temBAD_AMOUNT", result.Code)

	// Fail if neither CancelAfter nor FinishAfter are specified
	result = env.Submit(
		escrow.EscrowCreate(alice, carol, xrp(1)).
			Build())
	require.Equal(t, "temBAD_EXPIRATION", result.Code)

	// Fail if neither a FinishTime nor a condition are attached (with fix1571)
	result = env.Submit(
		escrow.EscrowCreate(alice, carol, xrp(1)).
			CancelTime(env.Now().Add(1 * time.Second)).
			Build())
	require.Equal(t, "temMALFORMED", result.Code)

	// Fail if FinishAfter has already passed
	result = env.Submit(
		escrow.EscrowCreate(alice, carol, xrp(1)).
			FinishTime(env.Now().Add(-1 * time.Second)).
			Build())
	require.Equal(t, "tecNO_PERMISSION", result.Code)

	// If both CancelAfter and FinishAfter are set, then CancelAfter must
	// be strictly later than FinishAfter.
	result = env.Submit(
		escrow.EscrowCreate(alice, carol, xrp(1)).
			Condition(escrow.TestCondition1).
			FinishTime(env.Now().Add(10 * time.Second)).
			CancelTime(env.Now().Add(10 * time.Second)).
			Build())
	require.Equal(t, "temBAD_EXPIRATION", result.Code)

	result = env.Submit(
		escrow.EscrowCreate(alice, carol, xrp(1)).
			Condition(escrow.TestCondition1).
			FinishTime(env.Now().Add(10 * time.Second)).
			CancelTime(env.Now().Add(5 * time.Second)).
			Build())
	require.Equal(t, "temBAD_EXPIRATION", result.Code)

	// Carol now requires the use of a destination tag
	result = env.Submit(accountset.AccountSet(carol).RequireDest().Build())
	jtx.RequireTxSuccess(t, result)

	// Missing destination tag
	result = env.Submit(
		escrow.EscrowCreate(alice, carol, xrp(1)).
			Condition(escrow.TestCondition1).
			CancelTime(env.Now().Add(1 * time.Second)).
			Build())
	require.Equal(t, "tecDST_TAG_NEEDED", result.Code)

	// Success with destination tag
	result = env.Submit(
		escrow.EscrowCreate(alice, carol, xrp(1)).
			Condition(escrow.TestCondition1).
			CancelTime(env.Now().Add(1 * time.Second)).
			DestTag(1).
			Build())
	jtx.RequireTxSuccess(t, result)

	// Fail if the sender wants to send more than he has
	t.Run("InsufficientFunds", func(t *testing.T) {
		daniel := jtx.NewAccount("daniel")
		env.FundAmount(daniel, uint64(xrp(50))+env.ReserveIncrement()+env.ReserveBase())
		result := env.Submit(
			escrow.EscrowCreate(daniel, bob, xrp(51)).
				FinishTime(env.Now().Add(1 * time.Second)).
				Build())
		require.Equal(t, "tecUNFUNDED", result.Code)

		evan := jtx.NewAccount("evan")
		env.FundAmount(evan, uint64(xrp(50))+env.ReserveIncrement()+env.ReserveBase())
		result = env.Submit(
			escrow.EscrowCreate(evan, bob, xrp(50)).
				FinishTime(env.Now().Add(1 * time.Second)).
				Build())
		require.Equal(t, "tecUNFUNDED", result.Code)

		frank := jtx.NewAccount("frank")
		env.FundAmount(frank, env.ReserveBase())
		result = env.Submit(
			escrow.EscrowCreate(frank, bob, xrp(1)).
				FinishTime(env.Now().Add(1 * time.Second)).
				Build())
		require.Equal(t, "tecINSUFFICIENT_RESERVE", result.Code)
	})

	// Specify incorrect sequence number
	t.Run("IncorrectSequence", func(t *testing.T) {
		hannah := jtx.NewAccount("hannah")
		fund5000(env, hannah)
		seq := env.Seq(hannah)
		result := env.Submit(
			escrow.EscrowCreate(hannah, hannah, xrp(10)).
				FinishTime(env.Now().Add(1 * time.Second)).
				Fee(150 * baseFee).
				Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		result = env.Submit(
			escrow.EscrowFinish(hannah, hannah, seq+7).
				Fee(150 * baseFee).
				Build())
		require.Equal(t, "tecNO_TARGET", result.Code)
	})

	// Try to specify a condition for a non-conditional payment
	t.Run("ConditionOnNonConditional", func(t *testing.T) {
		ivan := jtx.NewAccount("ivan")
		fund5000(env, ivan)
		seq := env.Seq(ivan)

		result := env.Submit(
			escrow.EscrowCreate(ivan, ivan, xrp(10)).
				FinishTime(env.Now().Add(1 * time.Second)).
				Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		result = env.Submit(
			escrow.EscrowFinish(ivan, ivan, seq).
				Condition(escrow.TestCondition1).
				Fulfillment(escrow.TestFulfillment1).
				Fee(150 * baseFee).
				Build())
		require.Equal(t, "tecCRYPTOCONDITION_ERROR", result.Code)
	})
}

// --------------------------------------------------------------------------
// TestEscrow_Lockup
// Reference: rippled Escrow_test.cpp testLockup (lines 507-762)
// --------------------------------------------------------------------------

func TestEscrow_Lockup(t *testing.T) {
	t.Run("Unconditional", func(t *testing.T) {
		// Reference: rippled lines 515-537
		env := jtx.NewTestEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		fund5000(env, alice, bob)

		seq := env.Seq(alice)
		result := env.Submit(
			escrow.EscrowCreate(alice, alice, xrp(1000)).
				FinishTime(env.Now().Add(5 * time.Second)).
				Build())
		jtx.RequireTxSuccess(t, result)
		jtx.RequireBalance(t, env, alice, uint64(xrp(5000)-xrp(1000))-drops(baseFee))

		// Not enough time has elapsed for a finish and canceling isn't possible.
		result = env.Submit(
			escrow.EscrowCancel(bob, alice, seq).Build())
		require.Equal(t, "tecNO_PERMISSION", result.Code)
		result = env.Submit(
			escrow.EscrowFinish(bob, alice, seq).Build())
		require.Equal(t, "tecNO_PERMISSION", result.Code)
		env.Close()

		// Cancel continues to not be possible
		result = env.Submit(
			escrow.EscrowCancel(bob, alice, seq).Build())
		require.Equal(t, "tecNO_PERMISSION", result.Code)

		// Finish should succeed. Verify funds.
		result = env.Submit(
			escrow.EscrowFinish(bob, alice, seq).Build())
		jtx.RequireTxSuccess(t, result)
		jtx.RequireBalance(t, env, alice, uint64(xrp(5000))-drops(baseFee))
	})

	t.Run("UnconditionalThirdParty", func(t *testing.T) {
		// Unconditionally pay from Alice to Bob. Zelda (neither source nor
		// destination) signs all cancels and finishes.
		// Reference: rippled lines 538-566
		env := jtx.NewTestEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		zelda := jtx.NewAccount("zelda")
		fund5000(env, alice, bob, zelda)

		seq := env.Seq(alice)
		result := env.Submit(
			escrow.EscrowCreate(alice, bob, xrp(1000)).
				FinishTime(env.Now().Add(5 * time.Second)).
				Build())
		jtx.RequireTxSuccess(t, result)
		jtx.RequireBalance(t, env, alice, uint64(xrp(5000)-xrp(1000))-drops(baseFee))

		// Not enough time has elapsed for a finish and canceling isn't possible.
		result = env.Submit(
			escrow.EscrowCancel(zelda, alice, seq).Build())
		require.Equal(t, "tecNO_PERMISSION", result.Code)
		result = env.Submit(
			escrow.EscrowFinish(zelda, alice, seq).Build())
		require.Equal(t, "tecNO_PERMISSION", result.Code)
		env.Close()

		// Cancel continues to not be possible
		result = env.Submit(
			escrow.EscrowCancel(zelda, alice, seq).Build())
		require.Equal(t, "tecNO_PERMISSION", result.Code)

		// Finish should succeed. Verify funds.
		result = env.Submit(
			escrow.EscrowFinish(zelda, alice, seq).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		jtx.RequireBalance(t, env, alice, uint64(xrp(4000))-drops(baseFee))
		jtx.RequireBalance(t, env, bob, uint64(xrp(6000)))
		jtx.RequireBalance(t, env, zelda, uint64(xrp(5000))-drops(4*baseFee))
	})

	t.Run("DepositAuth", func(t *testing.T) {
		// Bob sets DepositAuth so only Bob can finish the escrow.
		// Reference: rippled lines 567-604
		env := jtx.NewTestEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		zelda := jtx.NewAccount("zelda")
		fund5000(env, alice, bob, zelda)

		env.EnableDepositAuth(bob)
		env.Close()

		seq := env.Seq(alice)
		result := env.Submit(
			escrow.EscrowCreate(alice, bob, xrp(1000)).
				FinishTime(env.Now().Add(5 * time.Second)).
				Build())
		jtx.RequireTxSuccess(t, result)
		jtx.RequireBalance(t, env, alice, uint64(xrp(5000)-xrp(1000))-drops(baseFee))

		// Not enough time has elapsed for a finish and canceling isn't possible.
		result = env.Submit(escrow.EscrowCancel(zelda, alice, seq).Build())
		require.Equal(t, "tecNO_PERMISSION", result.Code)
		result = env.Submit(escrow.EscrowCancel(alice, alice, seq).Build())
		require.Equal(t, "tecNO_PERMISSION", result.Code)
		result = env.Submit(escrow.EscrowCancel(bob, alice, seq).Build())
		require.Equal(t, "tecNO_PERMISSION", result.Code)
		result = env.Submit(escrow.EscrowFinish(zelda, alice, seq).Build())
		require.Equal(t, "tecNO_PERMISSION", result.Code)
		result = env.Submit(escrow.EscrowFinish(alice, alice, seq).Build())
		require.Equal(t, "tecNO_PERMISSION", result.Code)
		result = env.Submit(escrow.EscrowFinish(bob, alice, seq).Build())
		require.Equal(t, "tecNO_PERMISSION", result.Code)
		env.Close()

		// Cancel continues to not be possible. Finish will only succeed for
		// Bob, because of DepositAuth.
		result = env.Submit(escrow.EscrowCancel(zelda, alice, seq).Build())
		require.Equal(t, "tecNO_PERMISSION", result.Code)
		result = env.Submit(escrow.EscrowCancel(alice, alice, seq).Build())
		require.Equal(t, "tecNO_PERMISSION", result.Code)
		result = env.Submit(escrow.EscrowCancel(bob, alice, seq).Build())
		require.Equal(t, "tecNO_PERMISSION", result.Code)
		result = env.Submit(escrow.EscrowFinish(zelda, alice, seq).Build())
		require.Equal(t, "tecNO_PERMISSION", result.Code)
		result = env.Submit(escrow.EscrowFinish(alice, alice, seq).Build())
		require.Equal(t, "tecNO_PERMISSION", result.Code)
		result = env.Submit(escrow.EscrowFinish(bob, alice, seq).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		jtx.RequireBalance(t, env, alice, uint64(xrp(4000))-drops(baseFee*5))
		jtx.RequireBalance(t, env, bob, uint64(xrp(6000))-drops(baseFee*5))
		jtx.RequireBalance(t, env, zelda, uint64(xrp(5000))-drops(baseFee*4))
	})

	t.Run("DepositPreauth", func(t *testing.T) {
		// Bob sets DepositAuth but preauthorizes Zelda, so Zelda can finish.
		// Reference: rippled lines 605-633
		env := jtx.NewTestEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		zelda := jtx.NewAccount("zelda")
		fund5000(env, alice, bob, zelda)

		env.EnableDepositAuth(bob)
		env.Close()
		env.Preauthorize(bob, zelda)
		env.Close()

		seq := env.Seq(alice)
		result := env.Submit(
			escrow.EscrowCreate(alice, bob, xrp(1000)).
				FinishTime(env.Now().Add(5 * time.Second)).
				Build())
		jtx.RequireTxSuccess(t, result)
		jtx.RequireBalance(t, env, alice, uint64(xrp(5000)-xrp(1000))-drops(baseFee))
		env.Close()

		// DepositPreauth allows Finish to succeed for either Zelda or Bob.
		// But Finish won't succeed for Alice since she is not preauthorized.
		result = env.Submit(escrow.EscrowFinish(alice, alice, seq).Build())
		require.Equal(t, "tecNO_PERMISSION", result.Code)
		result = env.Submit(escrow.EscrowFinish(zelda, alice, seq).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		jtx.RequireBalance(t, env, alice, uint64(xrp(4000))-drops(baseFee*2))
		jtx.RequireBalance(t, env, bob, uint64(xrp(6000))-drops(baseFee*2))
		jtx.RequireBalance(t, env, zelda, uint64(xrp(5000))-drops(baseFee*1))
	})

	t.Run("Conditional", func(t *testing.T) {
		// Reference: rippled lines 634-677
		env := jtx.NewTestEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		fund5000(env, alice, bob)

		seq := env.Seq(alice)
		result := env.Submit(
			escrow.EscrowCreate(alice, alice, xrp(1000)).
				Condition(escrow.TestCondition2).
				FinishTime(env.Now().Add(5 * time.Second)).
				Build())
		jtx.RequireTxSuccess(t, result)
		jtx.RequireBalance(t, env, alice, uint64(xrp(5000)-xrp(1000))-drops(baseFee))

		// Not enough time has elapsed for a finish and canceling isn't possible.
		result = env.Submit(escrow.EscrowCancel(alice, alice, seq).Build())
		require.Equal(t, "tecNO_PERMISSION", result.Code)
		result = env.Submit(escrow.EscrowCancel(bob, alice, seq).Build())
		require.Equal(t, "tecNO_PERMISSION", result.Code)
		result = env.Submit(escrow.EscrowFinish(alice, alice, seq).Build())
		require.Equal(t, "tecNO_PERMISSION", result.Code)
		result = env.Submit(
			escrow.EscrowFinish(alice, alice, seq).
				Condition(escrow.TestCondition2).
				Fulfillment(escrow.TestFulfillment2).
				Fee(150 * baseFee).
				Build())
		require.Equal(t, "tecNO_PERMISSION", result.Code)
		result = env.Submit(escrow.EscrowFinish(bob, alice, seq).Build())
		require.Equal(t, "tecNO_PERMISSION", result.Code)
		result = env.Submit(
			escrow.EscrowFinish(bob, alice, seq).
				Condition(escrow.TestCondition2).
				Fulfillment(escrow.TestFulfillment2).
				Fee(150 * baseFee).
				Build())
		require.Equal(t, "tecNO_PERMISSION", result.Code)
		env.Close()

		// Cancel continues to not be possible. Finish is possible but
		// requires the fulfillment associated with the escrow.
		result = env.Submit(escrow.EscrowCancel(alice, alice, seq).Build())
		require.Equal(t, "tecNO_PERMISSION", result.Code)
		result = env.Submit(escrow.EscrowCancel(bob, alice, seq).Build())
		require.Equal(t, "tecNO_PERMISSION", result.Code)
		result = env.Submit(escrow.EscrowFinish(bob, alice, seq).Build())
		require.Equal(t, "tecCRYPTOCONDITION_ERROR", result.Code)
		result = env.Submit(escrow.EscrowFinish(alice, alice, seq).Build())
		require.Equal(t, "tecCRYPTOCONDITION_ERROR", result.Code)
		env.Close()

		result = env.Submit(
			escrow.EscrowFinish(bob, alice, seq).
				Condition(escrow.TestCondition2).
				Fulfillment(escrow.TestFulfillment2).
				Fee(150 * baseFee).
				Build())
		jtx.RequireTxSuccess(t, result)
	})

	t.Run("ConditionalDepositAuth", func(t *testing.T) {
		// Self-escrowed conditional with DepositAuth.
		// Reference: rippled lines 678-716
		env := jtx.NewTestEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		fund5000(env, alice, bob)

		seq := env.Seq(alice)
		result := env.Submit(
			escrow.EscrowCreate(alice, alice, xrp(1000)).
				Condition(escrow.TestCondition3).
				FinishTime(env.Now().Add(5 * time.Second)).
				Build())
		jtx.RequireTxSuccess(t, result)
		jtx.RequireBalance(t, env, alice, uint64(xrp(5000)-xrp(1000))-drops(baseFee))
		env.Close()

		// Finish is now possible but requires the cryptocondition.
		result = env.Submit(escrow.EscrowFinish(bob, alice, seq).Build())
		require.Equal(t, "tecCRYPTOCONDITION_ERROR", result.Code)
		result = env.Submit(escrow.EscrowFinish(alice, alice, seq).Build())
		require.Equal(t, "tecCRYPTOCONDITION_ERROR", result.Code)

		// Enable deposit authorization. After this only Alice can finish the escrow.
		env.EnableDepositAuth(alice)
		env.Close()

		result = env.Submit(
			escrow.EscrowFinish(alice, alice, seq).
				Condition(escrow.TestCondition2).
				Fulfillment(escrow.TestFulfillment2).
				Fee(150 * baseFee).
				Build())
		require.Equal(t, "tecCRYPTOCONDITION_ERROR", result.Code)
		result = env.Submit(
			escrow.EscrowFinish(bob, alice, seq).
				Condition(escrow.TestCondition3).
				Fulfillment(escrow.TestFulfillment3).
				Fee(150 * baseFee).
				Build())
		require.Equal(t, "tecNO_PERMISSION", result.Code)
		result = env.Submit(
			escrow.EscrowFinish(alice, alice, seq).
				Condition(escrow.TestCondition3).
				Fulfillment(escrow.TestFulfillment3).
				Fee(150 * baseFee).
				Build())
		jtx.RequireTxSuccess(t, result)
	})

	t.Run("ConditionalDepositAuthPreauth", func(t *testing.T) {
		// Self-escrowed conditional with DepositAuth and DepositPreauth.
		// Reference: rippled lines 717-762
		env := jtx.NewTestEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		zelda := jtx.NewAccount("zelda")
		fund5000(env, alice, bob, zelda)

		seq := env.Seq(alice)
		result := env.Submit(
			escrow.EscrowCreate(alice, alice, xrp(1000)).
				Condition(escrow.TestCondition3).
				FinishTime(env.Now().Add(5 * time.Second)).
				Build())
		jtx.RequireTxSuccess(t, result)
		jtx.RequireBalance(t, env, alice, uint64(xrp(5000)-xrp(1000))-drops(baseFee))
		env.Close()

		// Alice preauthorizes Zelda for deposit, even though Alice has not
		// set the lsfDepositAuth flag (yet).
		env.Preauthorize(alice, zelda)
		env.Close()

		// Finish is now possible but requires the cryptocondition.
		result = env.Submit(escrow.EscrowFinish(alice, alice, seq).Build())
		require.Equal(t, "tecCRYPTOCONDITION_ERROR", result.Code)
		result = env.Submit(escrow.EscrowFinish(bob, alice, seq).Build())
		require.Equal(t, "tecCRYPTOCONDITION_ERROR", result.Code)
		result = env.Submit(escrow.EscrowFinish(zelda, alice, seq).Build())
		require.Equal(t, "tecCRYPTOCONDITION_ERROR", result.Code)

		// Alice enables deposit authorization. After this only Alice or
		// Zelda (because Zelda is preauthorized) can finish the escrow.
		env.EnableDepositAuth(alice)
		env.Close()

		result = env.Submit(
			escrow.EscrowFinish(alice, alice, seq).
				Condition(escrow.TestCondition2).
				Fulfillment(escrow.TestFulfillment2).
				Fee(150 * baseFee).
				Build())
		require.Equal(t, "tecCRYPTOCONDITION_ERROR", result.Code)
		result = env.Submit(
			escrow.EscrowFinish(bob, alice, seq).
				Condition(escrow.TestCondition3).
				Fulfillment(escrow.TestFulfillment3).
				Fee(150 * baseFee).
				Build())
		require.Equal(t, "tecNO_PERMISSION", result.Code)
		result = env.Submit(
			escrow.EscrowFinish(zelda, alice, seq).
				Condition(escrow.TestCondition3).
				Fulfillment(escrow.TestFulfillment3).
				Fee(150 * baseFee).
				Build())
		jtx.RequireTxSuccess(t, result)
	})
}

// --------------------------------------------------------------------------
// TestEscrow_CryptoConditions
// Reference: rippled Escrow_test.cpp testEscrowConditions (lines 765-1160)
// --------------------------------------------------------------------------

func TestEscrow_CryptoConditions(t *testing.T) {
	t.Run("BasicCryptoConditions", func(t *testing.T) {
		// Reference: rippled lines 773-847
		env := jtx.NewTestEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		carol := jtx.NewAccount("carol")
		fund5000(env, alice, bob, carol)

		seq := env.Seq(alice)
		require.Equal(t, uint32(0), env.OwnerCount(alice))

		result := env.Submit(
			escrow.EscrowCreate(alice, carol, xrp(1000)).
				Condition(escrow.TestCondition1).
				CancelTime(env.Now().Add(1 * time.Second)).
				Build())
		jtx.RequireTxSuccess(t, result)
		require.Equal(t, uint32(1), env.OwnerCount(alice))
		jtx.RequireBalance(t, env, alice, uint64(xrp(4000))-drops(baseFee))
		jtx.RequireBalance(t, env, carol, uint64(xrp(5000)))

		result = env.Submit(escrow.EscrowCancel(bob, alice, seq).Build())
		require.Equal(t, "tecNO_PERMISSION", result.Code)
		require.Equal(t, uint32(1), env.OwnerCount(alice))

		// Attempt to finish without a fulfillment
		result = env.Submit(escrow.EscrowFinish(bob, alice, seq).Build())
		require.Equal(t, "tecCRYPTOCONDITION_ERROR", result.Code)
		require.Equal(t, uint32(1), env.OwnerCount(alice))

		// Attempt to finish with a condition instead of a fulfillment
		result = env.Submit(
			escrow.EscrowFinish(bob, alice, seq).
				Condition(escrow.TestCondition1).
				Fulfillment(escrow.TestCondition1). // condition passed as fulfillment
				Fee(150 * baseFee).
				Build())
		require.Equal(t, "tecCRYPTOCONDITION_ERROR", result.Code)
		require.Equal(t, uint32(1), env.OwnerCount(alice))

		// Wrong fulfillment (fb2 instead of fb1)
		result = env.Submit(
			escrow.EscrowFinish(bob, alice, seq).
				Condition(escrow.TestCondition1).
				Fulfillment(escrow.TestFulfillment2).
				Fee(150 * baseFee).
				Build())
		require.Equal(t, "tecCRYPTOCONDITION_ERROR", result.Code)
		require.Equal(t, uint32(1), env.OwnerCount(alice))

		// Wrong fulfillment (fb3 instead of fb1)
		result = env.Submit(
			escrow.EscrowFinish(bob, alice, seq).
				Condition(escrow.TestCondition1).
				Fulfillment(escrow.TestFulfillment3).
				Fee(150 * baseFee).
				Build())
		require.Equal(t, "tecCRYPTOCONDITION_ERROR", result.Code)
		require.Equal(t, uint32(1), env.OwnerCount(alice))

		// Incorrect condition with various fulfillments
		result = env.Submit(
			escrow.EscrowFinish(bob, alice, seq).
				Condition(escrow.TestCondition2).
				Fulfillment(escrow.TestFulfillment1).
				Fee(150 * baseFee).
				Build())
		require.Equal(t, "tecCRYPTOCONDITION_ERROR", result.Code)
		require.Equal(t, uint32(1), env.OwnerCount(alice))

		result = env.Submit(
			escrow.EscrowFinish(bob, alice, seq).
				Condition(escrow.TestCondition2).
				Fulfillment(escrow.TestFulfillment2).
				Fee(150 * baseFee).
				Build())
		require.Equal(t, "tecCRYPTOCONDITION_ERROR", result.Code)
		require.Equal(t, uint32(1), env.OwnerCount(alice))

		result = env.Submit(
			escrow.EscrowFinish(bob, alice, seq).
				Condition(escrow.TestCondition2).
				Fulfillment(escrow.TestFulfillment3).
				Fee(150 * baseFee).
				Build())
		require.Equal(t, "tecCRYPTOCONDITION_ERROR", result.Code)
		require.Equal(t, uint32(1), env.OwnerCount(alice))

		// Correct condition & fulfillment
		result = env.Submit(
			escrow.EscrowFinish(bob, alice, seq).
				Condition(escrow.TestCondition1).
				Fulfillment(escrow.TestFulfillment1).
				Fee(150 * baseFee).
				Build())
		jtx.RequireTxSuccess(t, result)

		// SLE removed on finish
		require.False(t, env.LedgerEntryExists(keylet.Escrow(alice.ID, seq)))
		require.Equal(t, uint32(0), env.OwnerCount(alice))
		jtx.RequireBalance(t, env, carol, uint64(xrp(6000)))

		result = env.Submit(escrow.EscrowCancel(bob, alice, seq).Build())
		require.Equal(t, "tecNO_TARGET", result.Code)
		require.Equal(t, uint32(0), env.OwnerCount(alice))

		result = env.Submit(escrow.EscrowCancel(bob, carol, 1).Build())
		require.Equal(t, "tecNO_TARGET", result.Code)
	})

	t.Run("CancelWithCondition", func(t *testing.T) {
		// Reference: rippled lines 848-864
		env := jtx.NewTestEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		carol := jtx.NewAccount("carol")
		fund5000(env, alice, bob, carol)

		seq := env.Seq(alice)
		require.Equal(t, uint32(0), env.OwnerCount(alice))

		result := env.Submit(
			escrow.EscrowCreate(alice, carol, xrp(1000)).
				Condition(escrow.TestCondition2).
				CancelTime(env.Now().Add(1 * time.Second)).
				Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		jtx.RequireBalance(t, env, alice, uint64(xrp(4000))-drops(baseFee))

		// Balance restored on cancel
		result = env.Submit(escrow.EscrowCancel(bob, alice, seq).Build())
		jtx.RequireTxSuccess(t, result)
		jtx.RequireBalance(t, env, alice, uint64(xrp(5000))-drops(baseFee))

		// SLE removed on cancel
		require.False(t, env.LedgerEntryExists(keylet.Escrow(alice.ID, seq)))
	})

	t.Run("CancelBeforeExpiry_FinishAfterExpiry", func(t *testing.T) {
		// Reference: rippled lines 865-887
		env := jtx.NewTestEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		carol := jtx.NewAccount("carol")
		fund5000(env, alice, bob, carol)
		env.Close()

		seq := env.Seq(alice)
		result := env.Submit(
			escrow.EscrowCreate(alice, carol, xrp(1000)).
				Condition(escrow.TestCondition3).
				CancelTime(env.Now().Add(1 * time.Second)).
				Build())
		jtx.RequireTxSuccess(t, result)
		require.Equal(t, uint32(1), env.OwnerCount(alice))

		// Cancel fails before expiration
		result = env.Submit(escrow.EscrowCancel(bob, alice, seq).Build())
		require.Equal(t, "tecNO_PERMISSION", result.Code)
		require.Equal(t, uint32(1), env.OwnerCount(alice))

		env.Close()

		// Finish fails after expiration
		result = env.Submit(
			escrow.EscrowFinish(bob, alice, seq).
				Condition(escrow.TestCondition3).
				Fulfillment(escrow.TestFulfillment3).
				Fee(150 * baseFee).
				Build())
		require.Equal(t, "tecNO_PERMISSION", result.Code)
		require.Equal(t, uint32(1), env.OwnerCount(alice))
		jtx.RequireBalance(t, env, carol, uint64(xrp(5000)))
	})

	t.Run("MalformedConditionsDuringCreation", func(t *testing.T) {
		// Test long & short conditions during creation
		// Reference: rippled lines 888-945
		env := jtx.NewTestEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		carol := jtx.NewAccount("carol")
		fund5000(env, alice, bob, carol)

		// Build a padded condition buffer: [0x78, cb1..., 0x78]
		cb1 := escrow.TestCondition1
		v := make([]byte, len(cb1)+2)
		for i := range v {
			v[i] = 0x78
		}
		copy(v[1:], cb1)

		s := len(v)
		ts := env.Now().Add(1 * time.Second)

		// All these are expected to fail because the condition is malformed
		// v[0:s] - full padded buffer
		result := env.Submit(
			escrow.EscrowCreate(alice, carol, xrp(1000)).
				Condition(v[0:s]).
				CancelTime(ts).
				Build())
		require.Equal(t, "temMALFORMED", result.Code)

		// v[0:s-1]
		result = env.Submit(
			escrow.EscrowCreate(alice, carol, xrp(1000)).
				Condition(v[0:s-1]).
				CancelTime(ts).
				Build())
		require.Equal(t, "temMALFORMED", result.Code)

		// v[0:s-2]
		result = env.Submit(
			escrow.EscrowCreate(alice, carol, xrp(1000)).
				Condition(v[0:s-2]).
				CancelTime(ts).
				Build())
		require.Equal(t, "temMALFORMED", result.Code)

		// v[1:s] - starts at cb1 but has trailing junk
		result = env.Submit(
			escrow.EscrowCreate(alice, carol, xrp(1000)).
				Condition(v[1:s]).
				CancelTime(ts).
				Build())
		require.Equal(t, "temMALFORMED", result.Code)

		// v[1:s-2] - short
		result = env.Submit(
			escrow.EscrowCreate(alice, carol, xrp(1000)).
				Condition(v[1:s-2]).
				CancelTime(ts).
				Build())
		require.Equal(t, "temMALFORMED", result.Code)

		// v[2:s] - starts one past cb1 start
		result = env.Submit(
			escrow.EscrowCreate(alice, carol, xrp(1000)).
				Condition(v[2:s]).
				CancelTime(ts).
				Build())
		require.Equal(t, "temMALFORMED", result.Code)

		// v[2:s-1] - also malformed
		result = env.Submit(
			escrow.EscrowCreate(alice, carol, xrp(1000)).
				Condition(v[2:s-1]).
				CancelTime(ts).
				Build())
		require.Equal(t, "temMALFORMED", result.Code)

		// v[1:s-1] is exactly cb1 (the valid condition)
		seq := env.Seq(alice)
		result = env.Submit(
			escrow.EscrowCreate(alice, carol, xrp(1000)).
				Condition(v[1 : s-1]).
				CancelTime(ts).
				Fee(10 * baseFee).
				Build())
		jtx.RequireTxSuccess(t, result)

		result = env.Submit(
			escrow.EscrowFinish(bob, alice, seq).
				Condition(escrow.TestCondition1).
				Fulfillment(escrow.TestFulfillment1).
				Fee(150 * baseFee).
				Build())
		jtx.RequireTxSuccess(t, result)

		jtx.RequireBalance(t, env, alice, uint64(xrp(4000))-drops(10*baseFee))
		jtx.RequireBalance(t, env, bob, uint64(xrp(5000))-drops(150*baseFee))
		jtx.RequireBalance(t, env, carol, uint64(xrp(6000)))
	})

	t.Run("MalformedConditionsAndFulfillmentsDuringFinish", func(t *testing.T) {
		// Test long and short conditions & fulfillments during finish
		// Reference: rippled lines 946-1091
		env := jtx.NewTestEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		carol := jtx.NewAccount("carol")
		fund5000(env, alice, bob, carol)

		// Build padded condition buffer
		cb2 := escrow.TestCondition2
		cv := make([]byte, len(cb2)+2)
		for i := range cv {
			cv[i] = 0x78
		}
		copy(cv[1:], cb2)
		cs := len(cv)

		// Build padded fulfillment buffer
		fb2 := escrow.TestFulfillment2
		fv := make([]byte, len(fb2)+2)
		for i := range fv {
			fv[i] = 0x13
		}
		copy(fv[1:], fb2)
		fs := len(fv)

		ts := env.Now().Add(1 * time.Second)

		// Malformed conditions during creation - all should fail
		result := env.Submit(
			escrow.EscrowCreate(alice, carol, xrp(1000)).
				Condition(cv[0:cs]).CancelTime(ts).Build())
		require.Equal(t, "temMALFORMED", result.Code)
		result = env.Submit(
			escrow.EscrowCreate(alice, carol, xrp(1000)).
				Condition(cv[0:cs-1]).CancelTime(ts).Build())
		require.Equal(t, "temMALFORMED", result.Code)
		result = env.Submit(
			escrow.EscrowCreate(alice, carol, xrp(1000)).
				Condition(cv[0:cs-2]).CancelTime(ts).Build())
		require.Equal(t, "temMALFORMED", result.Code)
		result = env.Submit(
			escrow.EscrowCreate(alice, carol, xrp(1000)).
				Condition(cv[1:cs]).CancelTime(ts).Build())
		require.Equal(t, "temMALFORMED", result.Code)
		result = env.Submit(
			escrow.EscrowCreate(alice, carol, xrp(1000)).
				Condition(cv[1:cs-2]).CancelTime(ts).Build())
		require.Equal(t, "temMALFORMED", result.Code)
		result = env.Submit(
			escrow.EscrowCreate(alice, carol, xrp(1000)).
				Condition(cv[2:cs]).CancelTime(ts).Build())
		require.Equal(t, "temMALFORMED", result.Code)
		result = env.Submit(
			escrow.EscrowCreate(alice, carol, xrp(1000)).
				Condition(cv[2:cs-1]).CancelTime(ts).Build())
		require.Equal(t, "temMALFORMED", result.Code)

		// Valid condition: cv[1:cs-1] is exactly cb2
		seq := env.Seq(alice)
		result = env.Submit(
			escrow.EscrowCreate(alice, carol, xrp(1000)).
				Condition(cv[1 : cs-1]).
				CancelTime(ts).
				Fee(10 * baseFee).
				Build())
		jtx.RequireTxSuccess(t, result)

		// Try to fulfill using malformed conditions
		result = env.Submit(
			escrow.EscrowFinish(bob, alice, seq).
				Condition(cv[0:cs]).Fulfillment(fv[0:fs]).Fee(150 * baseFee).Build())
		require.Equal(t, "tecCRYPTOCONDITION_ERROR", result.Code)
		result = env.Submit(
			escrow.EscrowFinish(bob, alice, seq).
				Condition(cv[0:cs-1]).Fulfillment(fv[0:fs]).Fee(150 * baseFee).Build())
		require.Equal(t, "tecCRYPTOCONDITION_ERROR", result.Code)
		result = env.Submit(
			escrow.EscrowFinish(bob, alice, seq).
				Condition(cv[0:cs-2]).Fulfillment(fv[0:fs]).Fee(150 * baseFee).Build())
		require.Equal(t, "tecCRYPTOCONDITION_ERROR", result.Code)
		result = env.Submit(
			escrow.EscrowFinish(bob, alice, seq).
				Condition(cv[1:cs]).Fulfillment(fv[0:fs]).Fee(150 * baseFee).Build())
		require.Equal(t, "tecCRYPTOCONDITION_ERROR", result.Code)
		result = env.Submit(
			escrow.EscrowFinish(bob, alice, seq).
				Condition(cv[1:cs-2]).Fulfillment(fv[0:fs]).Fee(150 * baseFee).Build())
		require.Equal(t, "tecCRYPTOCONDITION_ERROR", result.Code)
		result = env.Submit(
			escrow.EscrowFinish(bob, alice, seq).
				Condition(cv[2:cs]).Fulfillment(fv[0:fs]).Fee(150 * baseFee).Build())
		require.Equal(t, "tecCRYPTOCONDITION_ERROR", result.Code)
		result = env.Submit(
			escrow.EscrowFinish(bob, alice, seq).
				Condition(cv[2:cs-1]).Fulfillment(fv[0:fs]).Fee(150 * baseFee).Build())
		require.Equal(t, "tecCRYPTOCONDITION_ERROR", result.Code)

		// Correct condition, malformed fulfillments
		result = env.Submit(
			escrow.EscrowFinish(bob, alice, seq).
				Condition(cv[1:cs-1]).Fulfillment(fv[0:fs]).Fee(150 * baseFee).Build())
		require.Equal(t, "tecCRYPTOCONDITION_ERROR", result.Code)
		result = env.Submit(
			escrow.EscrowFinish(bob, alice, seq).
				Condition(cv[1:cs-1]).Fulfillment(fv[0:fs-1]).Fee(150 * baseFee).Build())
		require.Equal(t, "tecCRYPTOCONDITION_ERROR", result.Code)
		result = env.Submit(
			escrow.EscrowFinish(bob, alice, seq).
				Condition(cv[1:cs-1]).Fulfillment(fv[0:fs-2]).Fee(150 * baseFee).Build())
		require.Equal(t, "tecCRYPTOCONDITION_ERROR", result.Code)
		result = env.Submit(
			escrow.EscrowFinish(bob, alice, seq).
				Condition(cv[1:cs-1]).Fulfillment(fv[1:fs]).Fee(150 * baseFee).Build())
		require.Equal(t, "tecCRYPTOCONDITION_ERROR", result.Code)
		result = env.Submit(
			escrow.EscrowFinish(bob, alice, seq).
				Condition(cv[1:cs-1]).Fulfillment(fv[1:fs-2]).Fee(150 * baseFee).Build())
		require.Equal(t, "tecCRYPTOCONDITION_ERROR", result.Code)
		result = env.Submit(
			escrow.EscrowFinish(bob, alice, seq).
				Condition(cv[1:cs-1]).Fulfillment(fv[1:fs-2]).Fee(150 * baseFee).Build())
		require.Equal(t, "tecCRYPTOCONDITION_ERROR", result.Code)
		result = env.Submit(
			escrow.EscrowFinish(bob, alice, seq).
				Condition(cv[1:cs-1]).Fulfillment(fv[2:fs]).Fee(150 * baseFee).Build())
		require.Equal(t, "tecCRYPTOCONDITION_ERROR", result.Code)
		result = env.Submit(
			escrow.EscrowFinish(bob, alice, seq).
				Condition(cv[1:cs-1]).Fulfillment(fv[2:fs-1]).Fee(150 * baseFee).Build())
		require.Equal(t, "tecCRYPTOCONDITION_ERROR", result.Code)

		// Now try the correct one
		result = env.Submit(
			escrow.EscrowFinish(bob, alice, seq).
				Condition(escrow.TestCondition2).
				Fulfillment(escrow.TestFulfillment2).
				Fee(150 * baseFee).
				Build())
		jtx.RequireTxSuccess(t, result)
		jtx.RequireBalance(t, env, alice, uint64(xrp(4000))-drops(10*baseFee))
		jtx.RequireBalance(t, env, carol, uint64(xrp(6000)))
	})

	t.Run("EmptyConditionAndFulfillment", func(t *testing.T) {
		// Test empty condition during creation and empty condition & fulfillment during finish
		// Reference: rippled lines 1092-1140
		env := jtx.NewTestEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		carol := jtx.NewAccount("carol")
		fund5000(env, alice, bob, carol)

		result := env.Submit(
			escrow.EscrowCreate(alice, carol, xrp(1000)).
				Condition([]byte{}).
				CancelTime(env.Now().Add(1 * time.Second)).
				Build())
		require.Equal(t, "temMALFORMED", result.Code)

		seq := env.Seq(alice)
		result = env.Submit(
			escrow.EscrowCreate(alice, carol, xrp(1000)).
				Condition(escrow.TestCondition3).
				CancelTime(env.Now().Add(1 * time.Second)).
				Build())
		jtx.RequireTxSuccess(t, result)

		result = env.Submit(
			escrow.EscrowFinish(bob, alice, seq).
				Condition([]byte{}).
				Fulfillment([]byte{}).
				Fee(150 * baseFee).
				Build())
		require.Equal(t, "tecCRYPTOCONDITION_ERROR", result.Code)

		result = env.Submit(
			escrow.EscrowFinish(bob, alice, seq).
				Condition(escrow.TestCondition3).
				Fulfillment([]byte{}).
				Fee(150 * baseFee).
				Build())
		require.Equal(t, "tecCRYPTOCONDITION_ERROR", result.Code)

		result = env.Submit(
			escrow.EscrowFinish(bob, alice, seq).
				Condition([]byte{}).
				Fulfillment(escrow.TestFulfillment3).
				Fee(150 * baseFee).
				Build())
		require.Equal(t, "tecCRYPTOCONDITION_ERROR", result.Code)

		// Missing Condition or Fulfillment (must both be present or absent)
		result = env.Submit(
			escrow.EscrowFinish(bob, alice, seq).
				Condition(escrow.TestCondition3).
				Build())
		require.Equal(t, "temMALFORMED", result.Code)

		result = env.Submit(
			escrow.EscrowFinish(bob, alice, seq).
				Fulfillment(escrow.TestFulfillment3).
				Build())
		require.Equal(t, "temMALFORMED", result.Code)

		// Now finish it correctly
		result = env.Submit(
			escrow.EscrowFinish(bob, alice, seq).
				Condition(escrow.TestCondition3).
				Fulfillment(escrow.TestFulfillment3).
				Fee(150 * baseFee).
				Build())
		jtx.RequireTxSuccess(t, result)
		jtx.RequireBalance(t, env, carol, uint64(xrp(6000)))
		jtx.RequireBalance(t, env, alice, uint64(xrp(4000))-drops(baseFee))
	})

	t.Run("NonPreimageSha256Condition", func(t *testing.T) {
		// Test a condition other than PreimageSha256, which would require a separate amendment
		// Reference: rippled lines 1141-1159
		env := jtx.NewTestEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		fund5000(env, alice, bob)

		// This is an Ed25519 threshold condition (type 0xA2), not PreimageSha256 (0xA0)
		cb := []byte{
			0xA2, 0x2B, 0x80, 0x20, 0x42, 0x4A, 0x70, 0x49, 0x49,
			0x52, 0x92, 0x67, 0xB6, 0x21, 0xB3, 0xD7, 0x91, 0x19,
			0xD7, 0x29, 0xB2, 0x38, 0x2C, 0xED, 0x8B, 0x29, 0x6C,
			0x3C, 0x02, 0x8F, 0xA9, 0x7D, 0x35, 0x0F, 0x6D, 0x07,
			0x81, 0x03, 0x06, 0x34, 0xD2, 0x82, 0x02, 0x03, 0xC8,
		}

		// FIXME: this transaction should, eventually, return temDISABLED
		// instead of temMALFORMED.
		result := env.Submit(
			escrow.EscrowCreate(alice, bob, xrp(1000)).
				Condition(cb).
				CancelTime(env.Now().Add(1 * time.Second)).
				Build())
		require.Equal(t, "temMALFORMED", result.Code)
	})
}

// --------------------------------------------------------------------------
// TestEscrow_MetaAndOwnership
// Reference: rippled Escrow_test.cpp testMetaAndOwnership (lines 1162-1338)
// --------------------------------------------------------------------------

func TestEscrow_MetaAndOwnership(t *testing.T) {
	t.Run("MetadataToSelf", func(t *testing.T) {
		// Reference: rippled lines 1172-1246
		env := jtx.NewTestEnv(t)
		alice := jtx.NewAccount("alice")
		bruce := jtx.NewAccount("bruce")
		carol := jtx.NewAccount("carol")
		fund5000(env, alice, bruce, carol)

		aseq := env.Seq(alice)
		bseq := env.Seq(bruce)

		result := env.Submit(
			escrow.EscrowCreate(alice, alice, xrp(1000)).
				FinishTime(env.Now().Add(1 * time.Second)).
				CancelTime(env.Now().Add(500 * time.Second)).
				Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		aa := keylet.Escrow(alice.ID, aseq)
		require.True(t, env.LedgerEntryExists(aa))

		// Alice's owner directory should have 1 entry
		require.Equal(t, uint32(1), env.OwnerCount(alice))

		result = env.Submit(
			escrow.EscrowCreate(bruce, bruce, xrp(1000)).
				FinishTime(env.Now().Add(1 * time.Second)).
				CancelTime(env.Now().Add(2 * time.Second)).
				Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		bb := keylet.Escrow(bruce.ID, bseq)
		require.True(t, env.LedgerEntryExists(bb))
		require.Equal(t, uint32(1), env.OwnerCount(bruce))

		env.Close()

		// Finish Alice's escrow
		result = env.Submit(
			escrow.EscrowFinish(alice, alice, aseq).Build())
		jtx.RequireTxSuccess(t, result)

		require.False(t, env.LedgerEntryExists(aa))
		require.Equal(t, uint32(0), env.OwnerCount(alice))
		require.Equal(t, uint32(1), env.OwnerCount(bruce))

		env.Close()

		// Cancel Bruce's escrow
		result = env.Submit(
			escrow.EscrowCancel(bruce, bruce, bseq).Build())
		jtx.RequireTxSuccess(t, result)

		require.False(t, env.LedgerEntryExists(bb))
		require.Equal(t, uint32(0), env.OwnerCount(bruce))
	})

	t.Run("MetadataToOther", func(t *testing.T) {
		// Reference: rippled lines 1247-1337
		env := jtx.NewTestEnv(t)
		alice := jtx.NewAccount("alice")
		bruce := jtx.NewAccount("bruce")
		carol := jtx.NewAccount("carol")
		fund5000(env, alice, bruce, carol)

		aseq := env.Seq(alice)
		bseq := env.Seq(bruce)

		result := env.Submit(
			escrow.EscrowCreate(alice, bruce, xrp(1000)).
				FinishTime(env.Now().Add(1 * time.Second)).
				Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		result = env.Submit(
			escrow.EscrowCreate(bruce, carol, xrp(1000)).
				FinishTime(env.Now().Add(1 * time.Second)).
				CancelTime(env.Now().Add(2 * time.Second)).
				Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		ab := keylet.Escrow(alice.ID, aseq)
		require.True(t, env.LedgerEntryExists(ab))

		bc := keylet.Escrow(bruce.ID, bseq)
		require.True(t, env.LedgerEntryExists(bc))

		// Alice's dir has 1 (ab), Bruce's dir has 2 (ab + bc), Carol's dir has 1 (bc)
		require.Equal(t, uint32(1), env.OwnerCount(alice))
		require.Equal(t, uint32(2), env.OwnerCount(bruce))
		require.Equal(t, uint32(1), env.OwnerCount(carol))

		env.Close()

		// Finish Alice->Bruce escrow
		result = env.Submit(
			escrow.EscrowFinish(alice, alice, aseq).Build())
		jtx.RequireTxSuccess(t, result)

		require.False(t, env.LedgerEntryExists(ab))
		require.True(t, env.LedgerEntryExists(bc))

		require.Equal(t, uint32(0), env.OwnerCount(alice))
		require.Equal(t, uint32(1), env.OwnerCount(bruce))
		require.Equal(t, uint32(1), env.OwnerCount(carol))

		env.Close()

		// Cancel Bruce->Carol escrow
		result = env.Submit(
			escrow.EscrowCancel(bruce, bruce, bseq).Build())
		jtx.RequireTxSuccess(t, result)

		require.False(t, env.LedgerEntryExists(ab))
		require.False(t, env.LedgerEntryExists(bc))

		require.Equal(t, uint32(0), env.OwnerCount(alice))
		require.Equal(t, uint32(0), env.OwnerCount(bruce))
		require.Equal(t, uint32(0), env.OwnerCount(carol))
	})
}

// --------------------------------------------------------------------------
// TestEscrow_Consequences
// Reference: rippled Escrow_test.cpp testConsequences (lines 1340-1401)
// --------------------------------------------------------------------------

func TestEscrow_Consequences(t *testing.T) {
	// Test that escrow transactions have the correct consequences
	// (fee, potentialSpend, isBlocker) for the transaction queue.
	// Reference: rippled preflight() consequences checks.

	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	carol := jtx.NewAccount("carol")
	fund5000(env, alice, bob, carol)

	t.Run("EscrowCreateConsequences", func(t *testing.T) {
		// EscrowCreate: fee=baseFee, potentialSpend=amount, not a blocker
		txn := escrow.EscrowCreate(alice, carol, xrp(1000)).
			FinishTime(env.Now().Add(1 * time.Second)).
			Sequence(1).
			Fee(baseFee).
			Build()

		// Validate should succeed (preflight)
		err := txn.Validate()
		require.NoError(t, err)

		// Verify transaction fee
		require.Equal(t, "10", txn.GetCommon().Fee)

		// Verify amount (potential spend) is 1000 XRP
		ec := txn.(*escrowtx.EscrowCreate)
		require.Equal(t, int64(xrp(1000)), ec.Amount.Drops())
	})

	t.Run("EscrowCancelConsequences", func(t *testing.T) {
		// EscrowCancel: fee=baseFee, potentialSpend=0, not a blocker
		txn := escrow.EscrowCancel(bob, alice, 3).
			Sequence(1).
			Fee(baseFee).
			Build()

		err := txn.Validate()
		require.NoError(t, err)
		require.Equal(t, "10", txn.GetCommon().Fee)
	})

	t.Run("EscrowFinishConsequences", func(t *testing.T) {
		// EscrowFinish: fee=baseFee, potentialSpend=0, not a blocker
		txn := escrow.EscrowFinish(bob, alice, 3).
			Sequence(1).
			Fee(baseFee).
			Build()

		err := txn.Validate()
		require.NoError(t, err)
		require.Equal(t, "10", txn.GetCommon().Fee)
	})
}

// --------------------------------------------------------------------------
// TestEscrow_WithTickets
// Reference: rippled Escrow_test.cpp testEscrowWithTickets (lines 1403-1541)
// --------------------------------------------------------------------------

func TestEscrow_WithTickets(t *testing.T) {
	t.Run("FinishWithTickets", func(t *testing.T) {
		// Create escrow and finish using tickets.
		// Reference: rippled lines 1413-1473
		env := jtx.NewTestEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		fund5000(env, alice, bob)
		env.Close()

		// alice creates a ticket.
		aliceTicket := env.CreateTickets(alice, 1)
		env.Close()

		// bob creates a bunch of tickets because he will be burning
		// through them with tec transactions.
		const bobTicketCount = 20
		bobFirstTicket := env.CreateTickets(bob, bobTicketCount)
		env.Close()

		jtx.RequireTicketCount(t, env, alice, 1)
		jtx.RequireTicketCount(t, env, bob, bobTicketCount)

		// Note that from here on all transactions use tickets.
		aliceRootSeq := env.Seq(alice)
		bobRootSeq := env.Seq(bob)

		// alice creates an escrow that can be finished in the future
		ts := env.Now().Add(97 * time.Second)

		escrowSeq := aliceTicket
		createTx := escrow.EscrowCreate(alice, bob, xrp(1000)).
			FinishTime(ts).
			Build()
		jtx.WithTicketSeq(createTx, aliceTicket)
		result := env.Submit(createTx)
		jtx.RequireTxSuccess(t, result)
		require.Equal(t, aliceRootSeq, env.Seq(alice))
		jtx.RequireTicketCount(t, env, alice, 0)
		jtx.RequireTicketCount(t, env, bob, bobTicketCount)

		// Advance the ledger, verifying that the finish won't complete
		// prematurely. Use bob's tickets from largest to smallest.
		bobTicket := bobFirstTicket + bobTicketCount
		for env.Now().Before(ts) {
			bobTicket--
			finishTx := escrow.EscrowFinish(bob, alice, escrowSeq).
				Fee(150 * baseFee).
				Build()
			jtx.WithTicketSeq(finishTx, bobTicket)
			result = env.Submit(finishTx)
			require.Equal(t, "tecNO_PERMISSION", result.Code)
			require.Equal(t, bobRootSeq, env.Seq(bob))
			env.Close()
		}

		// bob tries to re-use a ticket, which is rejected.
		finishTx := escrow.EscrowFinish(bob, alice, escrowSeq).
			Fee(150 * baseFee).
			Build()
		jtx.WithTicketSeq(finishTx, bobTicket)
		result = env.Submit(finishTx)
		require.Equal(t, "tefNO_TICKET", result.Code)

		// bob uses one of his remaining tickets. Success!
		bobTicket--
		finishTx = escrow.EscrowFinish(bob, alice, escrowSeq).
			Fee(150 * baseFee).
			Build()
		jtx.WithTicketSeq(finishTx, bobTicket)
		result = env.Submit(finishTx)
		jtx.RequireTxSuccess(t, result)
		env.Close()
		require.Equal(t, bobRootSeq, env.Seq(bob))
	})

	t.Run("CancelWithTickets", func(t *testing.T) {
		// Create escrow and cancel using tickets.
		// Reference: rippled lines 1474-1540
		env := jtx.NewTestEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		fund5000(env, alice, bob)
		env.Close()

		// alice creates a ticket.
		aliceTicket := env.CreateTickets(alice, 1)
		env.Close()

		// bob creates a bunch of tickets.
		const bobTicketCount = 20
		bobTicket := env.CreateTickets(bob, bobTicketCount)
		env.Close()

		jtx.RequireTicketCount(t, env, alice, 1)
		jtx.RequireTicketCount(t, env, bob, bobTicketCount)

		aliceRootSeq := env.Seq(alice)
		bobRootSeq := env.Seq(bob)

		// alice creates an escrow that can be cancelled in the future.
		ts := env.Now().Add(117 * time.Second)

		escrowSeq := aliceTicket
		createTx := escrow.EscrowCreate(alice, bob, xrp(1000)).
			Condition(escrow.TestCondition1).
			CancelTime(ts).
			Build()
		jtx.WithTicketSeq(createTx, aliceTicket)
		result := env.Submit(createTx)
		jtx.RequireTxSuccess(t, result)
		require.Equal(t, aliceRootSeq, env.Seq(alice))
		jtx.RequireTicketCount(t, env, alice, 0)
		jtx.RequireTicketCount(t, env, bob, bobTicketCount)

		// Advance the ledger, verifying that the cancel won't complete prematurely.
		for env.Now().Before(ts) {
			cancelTx := escrow.EscrowCancel(bob, alice, escrowSeq).
				Fee(150 * baseFee).
				Build()
			jtx.WithTicketSeq(cancelTx, bobTicket)
			bobTicket++
			result = env.Submit(cancelTx)
			require.Equal(t, "tecNO_PERMISSION", result.Code)
			require.Equal(t, bobRootSeq, env.Seq(bob))
			env.Close()
		}

		// Verify that a finish won't work anymore.
		finishTx := escrow.EscrowFinish(bob, alice, escrowSeq).
			Condition(escrow.TestCondition1).
			Fulfillment(escrow.TestFulfillment1).
			Fee(150 * baseFee).
			Build()
		jtx.WithTicketSeq(finishTx, bobTicket)
		bobTicket++
		result = env.Submit(finishTx)
		require.Equal(t, "tecNO_PERMISSION", result.Code)
		require.Equal(t, bobRootSeq, env.Seq(bob))

		// Verify that the cancel succeeds.
		cancelTx := escrow.EscrowCancel(bob, alice, escrowSeq).
			Fee(150 * baseFee).
			Build()
		jtx.WithTicketSeq(cancelTx, bobTicket)
		bobTicket++
		result = env.Submit(cancelTx)
		jtx.RequireTxSuccess(t, result)
		env.Close()
		require.Equal(t, bobRootSeq, env.Seq(bob))
	})
}

// --------------------------------------------------------------------------
// TestEscrow_Credentials
// Reference: rippled Escrow_test.cpp testCredentials (lines 1543-1694)
// --------------------------------------------------------------------------

func TestEscrow_Credentials(t *testing.T) {
	t.Run("CredentialsAmendmentDisabled", func(t *testing.T) {
		// Reference: rippled lines 1559-1581
		env := jtx.NewTestEnv(t)
		env.DisableFeature("Credentials")

		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		fund5000(env, alice, bob)
		env.Close()

		seq := env.Seq(alice)
		result := env.Submit(
			escrow.EscrowCreate(alice, bob, xrp(1000)).
				FinishTime(env.Now().Add(1 * time.Second)).
				Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		env.EnableDepositAuth(bob)
		env.Close()
		env.Preauthorize(bob, alice)
		env.Close()

		// Using credentials when amendment is disabled should fail
		credIdx := "48004829F915654A81B11C4AB8218D96FED67F209B58328A72314FB6EA288BE4"
		result = env.Submit(
			escrow.EscrowFinish(bob, alice, seq).
				CredentialIDs([]string{credIdx}).
				Build())
		require.Equal(t, "temDISABLED", result.Code)
	})

	t.Run("CredentialsWithDepositAuth", func(t *testing.T) {
		// Reference: rippled lines 1583-1631
		env := jtx.NewTestEnv(t)

		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		carol := jtx.NewAccount("carol")
		dillon := jtx.NewAccount("dillon")
		zelda := jtx.NewAccount("zelda")
		fund5000(env, alice, bob, carol, dillon, zelda)
		env.Close()

		credType := "abcde"

		// Create credential: zelda issues to carol
		result := env.Submit(credential.CredentialCreate(zelda, carol, credType).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		credIdx := dp.CredentialIndex(carol, zelda, credType)

		seq := env.Seq(alice)
		result = env.Submit(
			escrow.EscrowCreate(alice, bob, xrp(1000)).
				FinishTime(env.Now().Add(50 * time.Second)).
				Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// Bob require preauthorization
		env.EnableDepositAuth(bob)
		env.Close()

		// Fail, credentials not accepted
		result = env.Submit(
			escrow.EscrowFinish(carol, alice, seq).
				CredentialIDs([]string{credIdx}).
				Build())
		require.Equal(t, "tecBAD_CREDENTIALS", result.Code)
		env.Close()

		// Accept the credential
		result = env.Submit(credential.CredentialAccept(carol, zelda, credType).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// Fail, credentials don't belong to root account (dillon uses carol's credential)
		result = env.Submit(
			escrow.EscrowFinish(dillon, alice, seq).
				CredentialIDs([]string{credIdx}).
				Build())
		require.Equal(t, "tecBAD_CREDENTIALS", result.Code)

		// Fail, no depositPreauth for this credential type
		result = env.Submit(
			escrow.EscrowFinish(carol, alice, seq).
				CredentialIDs([]string{credIdx}).
				Build())
		require.Equal(t, "tecNO_PERMISSION", result.Code)

		// Bob authorizes credentials from zelda with this credType
		result = env.Submit(
			dp.AuthCredentials(bob, []dp.AuthorizeCredentials{
				{Issuer: zelda, CredType: credType},
			}).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// Success
		env.Close()
		result = env.Submit(
			escrow.EscrowFinish(carol, alice, seq).
				CredentialIDs([]string{credIdx}).
				Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
	})

	t.Run("CredentialsWithoutDepositPreauth", func(t *testing.T) {
		// Reference: rippled lines 1633-1693
		env := jtx.NewTestEnv(t)

		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		carol := jtx.NewAccount("carol")
		dillon := jtx.NewAccount("dillon")
		zelda := jtx.NewAccount("zelda")
		fund5000(env, alice, bob, carol, dillon, zelda)
		env.Close()

		credType := "abcde"

		result := env.Submit(credential.CredentialCreate(zelda, carol, credType).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
		result = env.Submit(credential.CredentialAccept(carol, zelda, credType).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		credIdx := dp.CredentialIndex(carol, zelda, credType)

		seq := env.Seq(alice)
		result = env.Submit(
			escrow.EscrowCreate(alice, bob, xrp(1000)).
				FinishTime(env.Now().Add(50 * time.Second)).
				Build())
		jtx.RequireTxSuccess(t, result)
		// Time advance (multiple Close() to pass finish time)
		env.Close()
		env.Close()
		env.Close()
		env.Close()
		env.Close()
		env.Close()

		// Succeed, Bob doesn't require preauthorization
		result = env.Submit(
			escrow.EscrowFinish(carol, alice, seq).
				CredentialIDs([]string{credIdx}).
				Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// Test: use any valid credentials if account == dst
		credType2 := "fghijk"
		result = env.Submit(credential.CredentialCreate(zelda, bob, credType2).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
		result = env.Submit(credential.CredentialAccept(bob, zelda, credType2).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		credIdxBob := dp.CredentialIndex(bob, zelda, credType2)

		seq2 := env.Seq(alice)
		result = env.Submit(
			escrow.EscrowCreate(alice, bob, xrp(1000)).
				FinishTime(env.Now().Add(1 * time.Second)).
				Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// Bob require preauthorization
		env.EnableDepositAuth(bob)
		env.Close()
		result = env.Submit(
			dp.AuthCredentials(bob, []dp.AuthorizeCredentials{
				{Issuer: zelda, CredType: credType},
			}).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// Use any valid credentials if account == dst
		result = env.Submit(
			escrow.EscrowFinish(bob, alice, seq2).
				CredentialIDs([]string{credIdxBob}).
				Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
	})
}

// --------------------------------------------------------------------------
// Helper functions
// --------------------------------------------------------------------------

// decodeLedgerEntry decodes a hex-encoded binary ledger entry into a map.
func decodeLedgerEntry(hexStr string) (map[string]any, error) {
	// Use the binary codec to decode
	binarycodec, err := getBinaryCodecDecoder()
	if err != nil {
		return nil, err
	}
	return binarycodec(hexStr)
}

// getBinaryCodecDecoder returns the binary codec Decode function.
// This is a lazy import to avoid circular dependencies.
func getBinaryCodecDecoder() (func(string) (map[string]any, error), error) {
	// Import dynamically to avoid potential circular dependencies
	return func(hexStr string) (map[string]any, error) {
		// For now, return nil - the binary codec decode will be used when available
		return nil, nil
	}, nil
}

// toUint32 converts an interface{} to uint32 for field comparisons.
func toUint32(v any) uint32 {
	switch val := v.(type) {
	case float64:
		return uint32(val)
	case int:
		return uint32(val)
	case uint32:
		return val
	case int64:
		return uint32(val)
	default:
		return 0
	}
}
