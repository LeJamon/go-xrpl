// Package clawback_test contains integration tests for Clawback transaction behavior.
// Tests ported from rippled's Clawback_test.cpp
package clawback_test

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/accountset"
	"github.com/LeJamon/go-xrpl/internal/testing/clawback"
	"github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/internal/testing/trustset"
	"github.com/LeJamon/go-xrpl/internal/tx"
	accounttx "github.com/LeJamon/go-xrpl/internal/tx/account"
	clawbacktx "github.com/LeJamon/go-xrpl/internal/tx/clawback"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

type clawbackWithTopLevelMPTID struct {
	*clawbacktx.Clawback
}

func (c *clawbackWithTopLevelMPTID) Flatten() (map[string]any, error) {
	values, err := c.Clawback.Flatten()
	if err != nil {
		return nil, err
	}
	values["MPTokenIssuanceID"] = "000000000000000000000001000000000000000000000001"
	return values, nil
}

// TestClawback_AllowTrustLineClawbackFlag tests the AllowTrustLineClawback flag.
// Reference: rippled Clawback_test.cpp testAllowTrustLineClawbackFlag (lines 64-191)
func TestClawback_AllowTrustLineClawbackFlag(t *testing.T) {
	// Test that one can successfully set asfAllowTrustLineClawback flag.
	// If successful, asfNoFreeze can no longer be set.
	// Also, asfAllowTrustLineClawback cannot be cleared.
	t.Run("SetAndClearClawbackFlag", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		alice := jtx.NewAccount("alice")

		env.Fund(alice)
		env.Close()

		// set asfAllowTrustLineClawback
		result := env.Submit(accountset.AccountSet(alice).AllowClawback().Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
		jtx.RequireFlagSet(t, env, alice, state.LsfAllowTrustLineClawback)

		// clear asfAllowTrustLineClawback does nothing
		result = env.Submit(accountset.AccountSet(alice).ClearFlag(accounttx.AccountSetFlagAllowTrustLineClawback).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
		jtx.RequireFlagSet(t, env, alice, state.LsfAllowTrustLineClawback)

		// asfNoFreeze cannot be set when asfAllowTrustLineClawback is set
		jtx.RequireFlagNotSet(t, env, alice, state.LsfNoFreeze)
		result = env.Submit(accountset.AccountSet(alice).NoFreeze().Build())
		jtx.RequireTxFail(t, result, "tecNO_PERMISSION")
		env.Close()
	})

	// Test that asfAllowTrustLineClawback cannot be set when asfNoFreeze has been set
	t.Run("CannotSetClawbackWithNoFreeze", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		alice := jtx.NewAccount("alice")

		env.Fund(alice)
		env.Close()

		jtx.RequireFlagNotSet(t, env, alice, state.LsfNoFreeze)

		// set asfNoFreeze
		result := env.Submit(accountset.AccountSet(alice).NoFreeze().Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// NoFreeze is set
		jtx.RequireFlagSet(t, env, alice, state.LsfNoFreeze)

		// asfAllowTrustLineClawback cannot be set if asfNoFreeze is set
		result = env.Submit(accountset.AccountSet(alice).AllowClawback().Build())
		jtx.RequireTxFail(t, result, "tecNO_PERMISSION")
		env.Close()

		jtx.RequireFlagNotSet(t, env, alice, state.LsfAllowTrustLineClawback)
	})

	// Test that asfAllowTrustLineClawback is not allowed when owner dir is non-empty
	t.Run("CannotSetClawbackWithNonEmptyOwnerDir", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")

		env.Fund(alice, bob)
		env.Close()

		jtx.RequireFlagNotSet(t, env, alice, state.LsfAllowTrustLineClawback)

		// alice issues 10 USD to bob
		env.Trust(bob, tx.NewIssuedAmountFromFloat64(1000, "USD", alice.Address))
		result := env.Submit(payment.PayIssued(alice, bob, tx.NewIssuedAmountFromFloat64(10, "USD", alice.Address)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		jtx.RequireOwnerCount(t, env, alice, 0)
		jtx.RequireOwnerCount(t, env, bob, 1)

		// alice fails to enable clawback because she has trustline with bob
		result = env.Submit(accountset.AccountSet(alice).AllowClawback().Build())
		jtx.RequireTxFail(t, result, "tecOWNERS")
		env.Close()

		// bob sets trustline to default limit and pays alice back to delete the trustline
		result = env.Submit(trustset.TrustSet(bob, tx.NewIssuedAmountFromFloat64(0, "USD", alice.Address)).Build())
		jtx.RequireTxSuccess(t, result)
		result = env.Submit(payment.PayIssued(bob, alice, tx.NewIssuedAmountFromFloat64(10, "USD", alice.Address)).Build())
		jtx.RequireTxSuccess(t, result)

		jtx.RequireOwnerCount(t, env, alice, 0)
		jtx.RequireOwnerCount(t, env, bob, 0)

		// alice now is able to set asfAllowTrustLineClawback
		result = env.Submit(accountset.AccountSet(alice).AllowClawback().Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
		jtx.RequireFlagSet(t, env, alice, state.LsfAllowTrustLineClawback)

		jtx.RequireOwnerCount(t, env, alice, 0)
		jtx.RequireOwnerCount(t, env, bob, 0)
	})

	t.Run("RetiredAmendmentRemainsActive", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		env.DisableFeature("Clawback")

		alice := jtx.NewAccount("alice")

		env.Fund(alice)
		env.Close()

		jtx.RequireFlagNotSet(t, env, alice, state.LsfAllowTrustLineClawback)

		result := env.Submit(accountset.AccountSet(alice).AllowClawback().Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
		jtx.RequireFlagSet(t, env, alice, state.LsfAllowTrustLineClawback)
	})
}

// TestClawback_Validation tests Clawback transaction validation (preflight + preclaim).
// Reference: rippled Clawback_test.cpp testValidation (lines 194-320)
func TestClawback_Validation(t *testing.T) {
	t.Run("RetiredAmendmentAndFlagNotSet", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		env.DisableFeature("Clawback")

		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")

		env.Fund(alice, bob)
		env.Close()

		jtx.RequireFlagNotSet(t, env, alice, state.LsfAllowTrustLineClawback)

		// alice issues 10 USD to bob
		env.Trust(bob, tx.NewIssuedAmountFromFloat64(1000, "USD", alice.Address))
		result := env.Submit(payment.PayIssued(alice, bob, tx.NewIssuedAmountFromFloat64(10, "USD", alice.Address)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		jtx.RequireIOUBalance(t, env, bob, alice, "USD", 10)
		jtx.RequireIOUBalance(t, env, alice, bob, "USD", -10)

		// Clawback remains enabled, but the issuer has not opted in.
		result = env.Submit(clawback.Claw(alice, bob, "USD", 5).Build())
		jtx.RequireTxFail(t, result, "tecNO_PERMISSION")
		env.Close()

		jtx.RequireIOUBalance(t, env, bob, alice, "USD", 10)
		jtx.RequireIOUBalance(t, env, alice, bob, "USD", -10)
	})

	// Test that Clawback tx fails for invalid flags, amounts, self-claw, zero balance, no trustline
	t.Run("InvalidInputs", func(t *testing.T) {
		env := jtx.NewTestEnv(t)

		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")

		env.Fund(alice, bob)
		env.Close()

		// alice sets asfAllowTrustLineClawback
		result := env.Submit(accountset.AccountSet(alice).AllowClawback().Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
		jtx.RequireFlagSet(t, env, alice, state.LsfAllowTrustLineClawback)

		// alice issues 10 USD to bob
		env.Trust(bob, tx.NewIssuedAmountFromFloat64(1000, "USD", alice.Address))
		result = env.Submit(payment.PayIssued(alice, bob, tx.NewIssuedAmountFromFloat64(10, "USD", alice.Address)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		jtx.RequireIOUBalance(t, env, bob, alice, "USD", 10)
		jtx.RequireIOUBalance(t, env, alice, bob, "USD", -10)

		// fails due to invalid flag
		result = env.Submit(clawback.Claw(alice, bob, "USD", 5).Flags(0x00008000).Build())
		require.Equal(t, "temINVALID_FLAG", result.Code)
		env.Close()

		// fails due to negative amount
		result = env.Submit(clawback.Claw(alice, bob, "USD", -5).Build())
		require.Equal(t, "temBAD_AMOUNT", result.Code)
		env.Close()

		// fails due to zero amount
		result = env.Submit(clawback.Claw(alice, bob, "USD", 0).Build())
		require.Equal(t, "temBAD_AMOUNT", result.Code)
		env.Close()

		// fails because amount is in XRP
		{
			cb := clawback.Claw(alice, bob, "USD", 5).Build()
			cb.Amount = tx.NewXRPAmount(jtx.XRP(10))
			result = env.Submit(cb)
			require.Equal(t, "temBAD_AMOUNT", result.Code)
			env.Close()
		}

		// fails when `issuer` field in `amount` is not token holder (self-claw)
		result = env.Submit(clawback.Claw(alice, alice, "USD", 5).Build())
		require.Equal(t, "temBAD_AMOUNT", result.Code)
		env.Close()

		// bob pays alice back, trustline has a balance of 0
		result = env.Submit(payment.PayIssued(bob, alice, tx.NewIssuedAmountFromFloat64(10, "USD", alice.Address)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// bob still owns the trustline that has 0 balance
		jtx.RequireOwnerCount(t, env, alice, 0)
		jtx.RequireOwnerCount(t, env, bob, 1)
		jtx.RequireIOUBalance(t, env, bob, alice, "USD", 0)
		jtx.RequireIOUBalance(t, env, alice, bob, "USD", 0)

		// clawback fails because balance is 0
		result = env.Submit(clawback.Claw(alice, bob, "USD", 5).Build())
		require.Equal(t, "tecINSUFFICIENT_FUNDS", result.Code)
		env.Close()

		// set the limit to default, which should delete the trustline
		result = env.Submit(trustset.TrustSet(bob, tx.NewIssuedAmountFromFloat64(0, "USD", alice.Address)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// bob no longer owns the trustline
		jtx.RequireOwnerCount(t, env, alice, 0)
		jtx.RequireOwnerCount(t, env, bob, 0)

		// clawback fails because trustline does not exist
		result = env.Submit(clawback.Claw(alice, bob, "USD", 5).Build())
		require.Equal(t, "tecNO_LINE", result.Code)
		env.Close()
	})
}

func TestClawback_TypedSubmissionRejectsTopLevelMPTID(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	env.Fund(alice, bob)
	transaction := &clawbackWithTopLevelMPTID{Clawback: clawbacktx.NewMPTokenClawback(
		alice.Address,
		bob.Address,
		state.NewMPTAmountWithIssuanceID(1, alice.Address, "000000000000000000000001000000000000000000000001"),
	)}
	jtx.RequireTxFail(t, env.Submit(transaction), jtx.TemMALFORMED)
}

// TestClawback_Permission tests clawback permission checks (preclaim).
// Reference: rippled Clawback_test.cpp testPermission (lines 323-458)
func TestClawback_Permission(t *testing.T) {
	// Clawing back from a non-existent account returns error
	t.Run("NonExistentHolder", func(t *testing.T) {
		env := jtx.NewTestEnv(t)

		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")

		// bob's account is not funded and does not exist
		env.Fund(alice)
		env.Close()

		// alice sets asfAllowTrustLineClawback
		result := env.Submit(accountset.AccountSet(alice).AllowClawback().Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
		jtx.RequireFlagSet(t, env, alice, state.LsfAllowTrustLineClawback)

		// bob, the token holder, does not exist
		result = env.Submit(clawback.Claw(alice, bob, "USD", 5).Build())
		require.Equal(t, "terNO_ACCOUNT", result.Code)
		env.Close()
	})

	// Test that trustline cannot be clawed by someone who is not the issuer
	t.Run("ThirdPartyWithoutTrustLine", func(t *testing.T) {
		env := jtx.NewTestEnv(t)

		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		cindy := jtx.NewAccount("cindy")

		env.Fund(alice, bob, cindy)
		env.Close()

		// alice sets asfAllowTrustLineClawback
		result := env.Submit(accountset.AccountSet(alice).AllowClawback().Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
		jtx.RequireFlagSet(t, env, alice, state.LsfAllowTrustLineClawback)

		// cindy sets asfAllowTrustLineClawback
		result = env.Submit(accountset.AccountSet(cindy).AllowClawback().Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
		jtx.RequireFlagSet(t, env, cindy, state.LsfAllowTrustLineClawback)

		// alice issues 1000 USD to bob
		env.Trust(bob, tx.NewIssuedAmountFromFloat64(1000, "USD", alice.Address))
		result = env.Submit(payment.PayIssued(alice, bob, tx.NewIssuedAmountFromFloat64(1000, "USD", alice.Address)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		jtx.RequireIOUBalance(t, env, bob, alice, "USD", 1000)
		jtx.RequireIOUBalance(t, env, alice, bob, "USD", -1000)

		// cindy tries to claw from bob, and fails because trustline does not exist
		result = env.Submit(clawback.Claw(cindy, bob, "USD", 200).Build())
		jtx.RequireTxClaimed(t, result, "tecNO_LINE")
		env.Close()
		jtx.RequireIOUBalance(t, env, bob, alice, "USD", 1000)
		jtx.RequireIOUBalance(t, env, alice, bob, "USD", -1000)
		jtx.RequireOwnerCount(t, env, bob, 1)
		jtx.RequireOwnerCount(t, env, cindy, 0)
	})

	// When a trustline is created between issuer and holder,
	// we must make sure the holder is unable to claw back from
	// the issuer by impersonating the issuer account.
	// This must be tested bidirectionally for both accounts.
	t.Run("BidirectionalImpersonation", func(t *testing.T) {
		env := jtx.NewTestEnv(t)

		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")

		env.Fund(alice, bob)
		env.Close()

		// alice sets asfAllowTrustLineClawback
		result := env.Submit(accountset.AccountSet(alice).AllowClawback().Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
		jtx.RequireFlagSet(t, env, alice, state.LsfAllowTrustLineClawback)

		// bob sets asfAllowTrustLineClawback
		result = env.Submit(accountset.AccountSet(bob).AllowClawback().Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
		jtx.RequireFlagSet(t, env, bob, state.LsfAllowTrustLineClawback)

		// alice issues 10 USD to bob.
		// bob then attempts to submit a clawback tx to claw USD from alice.
		// this must FAIL, because bob is not the issuer for this trustline!!!
		t.Run("BobCannotClawAliceUSD", func(t *testing.T) {
			// bob creates a trustline with alice, and alice sends 10 USD to bob
			env.Trust(bob, tx.NewIssuedAmountFromFloat64(1000, "USD", alice.Address))
			result = env.Submit(payment.PayIssued(alice, bob, tx.NewIssuedAmountFromFloat64(10, "USD", alice.Address)).Build())
			jtx.RequireTxSuccess(t, result)
			env.Close()

			jtx.RequireIOUBalance(t, env, bob, alice, "USD", 10)
			jtx.RequireIOUBalance(t, env, alice, bob, "USD", -10)

			// bob cannot claw back USD from alice because he's not the issuer
			result = env.Submit(clawback.Claw(bob, alice, "USD", 5).Build())
			jtx.RequireTxClaimed(t, result, "tecNO_PERMISSION")
			env.Close()
			jtx.RequireIOUBalance(t, env, bob, alice, "USD", 10)
			jtx.RequireIOUBalance(t, env, alice, bob, "USD", -10)
			jtx.RequireOwnerCount(t, env, bob, 1)
		})

		// bob issues 10 CAD to alice.
		// alice then attempts to submit a clawback tx to claw CAD from bob.
		// this must FAIL, because alice is not the issuer for this trustline!!!
		t.Run("AliceCannotClawBobCAD", func(t *testing.T) {
			// alice creates a trustline with bob, and bob sends 10 CAD to alice
			env.Trust(alice, tx.NewIssuedAmountFromFloat64(1000, "CAD", bob.Address))
			result = env.Submit(payment.PayIssued(bob, alice, tx.NewIssuedAmountFromFloat64(10, "CAD", bob.Address)).Build())
			jtx.RequireTxSuccess(t, result)
			env.Close()

			jtx.RequireIOUBalance(t, env, bob, alice, "CAD", -10)
			jtx.RequireIOUBalance(t, env, alice, bob, "CAD", 10)

			// alice cannot claw back CAD from bob because she's not the issuer
			result = env.Submit(clawback.Claw(alice, bob, "CAD", 5).Build())
			jtx.RequireTxClaimed(t, result, "tecNO_PERMISSION")
			env.Close()
			jtx.RequireIOUBalance(t, env, bob, alice, "CAD", -10)
			jtx.RequireIOUBalance(t, env, alice, bob, "CAD", 10)
			jtx.RequireOwnerCount(t, env, alice, 1)
		})
	})
}

// TestClawback_Enabled tests basic successful clawback operations.
// Reference: rippled Clawback_test.cpp testEnabled (lines 461-505)
func TestClawback_Enabled(t *testing.T) {
	env := jtx.NewTestEnv(t)

	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")

	env.Fund(alice, bob)
	env.Close()

	// alice sets asfAllowTrustLineClawback
	result := env.Submit(accountset.AccountSet(alice).AllowClawback().Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()
	jtx.RequireFlagSet(t, env, alice, state.LsfAllowTrustLineClawback)

	// alice issues 1000 USD to bob
	env.Trust(bob, tx.NewIssuedAmountFromFloat64(1000, "USD", alice.Address))
	result = env.Submit(payment.PayIssued(alice, bob, tx.NewIssuedAmountFromFloat64(1000, "USD", alice.Address)).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	jtx.RequireIOUBalance(t, env, bob, alice, "USD", 1000)
	jtx.RequireIOUBalance(t, env, alice, bob, "USD", -1000)

	// alice claws back 200 USD from bob
	result = env.Submit(clawback.Claw(alice, bob, "USD", 200).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// bob should have 800 USD left
	jtx.RequireIOUBalance(t, env, bob, alice, "USD", 800)
	jtx.RequireIOUBalance(t, env, alice, bob, "USD", -800)

	// alice claws back 800 USD from bob again
	result = env.Submit(clawback.Claw(alice, bob, "USD", 800).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// trustline has a balance of 0
	jtx.RequireIOUBalance(t, env, bob, alice, "USD", 0)
	jtx.RequireIOUBalance(t, env, alice, bob, "USD", 0)
}

// TestClawback_MultiLine tests clawback with multiple trust lines.
// Reference: rippled Clawback_test.cpp testMultiLine (lines 508-632)
func TestClawback_MultiLine(t *testing.T) {
	// Both alice and bob issue their own "USD" to cindy.
	// When alice and bob try to claw back, they will only
	// claw back from their respective trustline.
	t.Run("MultipleIssuersToSameHolder", func(t *testing.T) {
		env := jtx.NewTestEnv(t)

		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		cindy := jtx.NewAccount("cindy")

		env.Fund(alice, bob, cindy)
		env.Close()

		// alice sets asfAllowTrustLineClawback
		result := env.Submit(accountset.AccountSet(alice).AllowClawback().Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
		jtx.RequireFlagSet(t, env, alice, state.LsfAllowTrustLineClawback)

		// bob sets asfAllowTrustLineClawback
		result = env.Submit(accountset.AccountSet(bob).AllowClawback().Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
		jtx.RequireFlagSet(t, env, bob, state.LsfAllowTrustLineClawback)

		// alice sends 1000 USD to cindy
		env.Trust(cindy, tx.NewIssuedAmountFromFloat64(1000, "USD", alice.Address))
		result = env.Submit(payment.PayIssued(alice, cindy, tx.NewIssuedAmountFromFloat64(1000, "USD", alice.Address)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// bob sends 1000 USD to cindy
		env.Trust(cindy, tx.NewIssuedAmountFromFloat64(1000, "USD", bob.Address))
		result = env.Submit(payment.PayIssued(bob, cindy, tx.NewIssuedAmountFromFloat64(1000, "USD", bob.Address)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// alice claws back 200 USD from cindy
		result = env.Submit(clawback.Claw(alice, cindy, "USD", 200).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// cindy has 800 USD left in alice's trustline after clawed by alice
		jtx.RequireIOUBalance(t, env, cindy, alice, "USD", 800)
		jtx.RequireIOUBalance(t, env, alice, cindy, "USD", -800)

		// cindy still has 1000 USD in bob's trustline
		jtx.RequireIOUBalance(t, env, cindy, bob, "USD", 1000)
		jtx.RequireIOUBalance(t, env, bob, cindy, "USD", -1000)

		// bob claws back 600 USD from cindy
		result = env.Submit(clawback.Claw(bob, cindy, "USD", 600).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// cindy has 400 USD left in bob's trustline after clawed by bob
		jtx.RequireIOUBalance(t, env, cindy, bob, "USD", 400)
		jtx.RequireIOUBalance(t, env, bob, cindy, "USD", -400)

		// cindy still has 800 USD in alice's trustline
		jtx.RequireIOUBalance(t, env, cindy, alice, "USD", 800)
		jtx.RequireIOUBalance(t, env, alice, cindy, "USD", -800)
	})

	// alice issues USD to both bob and cindy.
	// when alice claws back from bob, only bob's USD balance is
	// affected, and cindy's balance remains unchanged, and vice versa.
	t.Run("OneIssuerMultipleHolders", func(t *testing.T) {
		env := jtx.NewTestEnv(t)

		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		cindy := jtx.NewAccount("cindy")

		env.Fund(alice, bob, cindy)
		env.Close()

		// alice sets asfAllowTrustLineClawback
		result := env.Submit(accountset.AccountSet(alice).AllowClawback().Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
		jtx.RequireFlagSet(t, env, alice, state.LsfAllowTrustLineClawback)

		// alice sends 600 USD to bob
		env.Trust(bob, tx.NewIssuedAmountFromFloat64(1000, "USD", alice.Address))
		result = env.Submit(payment.PayIssued(alice, bob, tx.NewIssuedAmountFromFloat64(600, "USD", alice.Address)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		jtx.RequireIOUBalance(t, env, alice, bob, "USD", -600)
		jtx.RequireIOUBalance(t, env, bob, alice, "USD", 600)

		// alice sends 1000 USD to cindy
		env.Trust(cindy, tx.NewIssuedAmountFromFloat64(1000, "USD", alice.Address))
		result = env.Submit(payment.PayIssued(alice, cindy, tx.NewIssuedAmountFromFloat64(1000, "USD", alice.Address)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		jtx.RequireIOUBalance(t, env, alice, cindy, "USD", -1000)
		jtx.RequireIOUBalance(t, env, cindy, alice, "USD", 1000)

		// alice claws back 500 USD from bob
		result = env.Submit(clawback.Claw(alice, bob, "USD", 500).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// bob's balance is reduced
		jtx.RequireIOUBalance(t, env, alice, bob, "USD", -100)
		jtx.RequireIOUBalance(t, env, bob, alice, "USD", 100)

		// cindy's balance is unchanged
		jtx.RequireIOUBalance(t, env, alice, cindy, "USD", -1000)
		jtx.RequireIOUBalance(t, env, cindy, alice, "USD", 1000)

		// alice claws back 300 USD from cindy
		result = env.Submit(clawback.Claw(alice, cindy, "USD", 300).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// bob's balance is unchanged
		jtx.RequireIOUBalance(t, env, alice, bob, "USD", -100)
		jtx.RequireIOUBalance(t, env, bob, alice, "USD", 100)

		// cindy's balance is reduced
		jtx.RequireIOUBalance(t, env, alice, cindy, "USD", -700)
		jtx.RequireIOUBalance(t, env, cindy, alice, "USD", 700)
	})
}

// TestClawback_BidirectionalLine tests clawback on bidirectional trust lines.
// Reference: rippled Clawback_test.cpp testBidirectionalLine (lines 635-724)
func TestClawback_BidirectionalLine(t *testing.T) {
	// Test when both alice and bob issue USD to each other.
	// This scenario creates only one trustline.
	// We test that only the person who has a negative balance from their
	// perspective is allowed to clawback.
	env := jtx.NewTestEnv(t)

	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")

	env.Fund(alice, bob)
	env.Close()

	// alice sets asfAllowTrustLineClawback
	result := env.Submit(accountset.AccountSet(alice).AllowClawback().Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()
	jtx.RequireFlagSet(t, env, alice, state.LsfAllowTrustLineClawback)

	// bob sets asfAllowTrustLineClawback
	result = env.Submit(accountset.AccountSet(bob).AllowClawback().Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()
	jtx.RequireFlagSet(t, env, bob, state.LsfAllowTrustLineClawback)

	// alice issues 1000 USD to bob
	env.Trust(bob, tx.NewIssuedAmountFromFloat64(1000, "USD", alice.Address))
	result = env.Submit(payment.PayIssued(alice, bob, tx.NewIssuedAmountFromFloat64(1000, "USD", alice.Address)).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	jtx.RequireOwnerCount(t, env, alice, 0)
	jtx.RequireOwnerCount(t, env, bob, 1)

	// bob is the holder, and alice is the issuer
	jtx.RequireIOUBalance(t, env, bob, alice, "USD", 1000)
	jtx.RequireIOUBalance(t, env, alice, bob, "USD", -1000)

	// bob issues 1500 USD to alice
	env.Trust(alice, tx.NewIssuedAmountFromFloat64(1500, "USD", bob.Address))
	result = env.Submit(payment.PayIssued(bob, alice, tx.NewIssuedAmountFromFloat64(1500, "USD", bob.Address)).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	jtx.RequireOwnerCount(t, env, alice, 1)
	jtx.RequireOwnerCount(t, env, bob, 1)

	// bob has negative 500 USD because bob issued 500 USD more than alice
	// bob can now be seen as the issuer, while alice is the holder
	jtx.RequireIOUBalance(t, env, bob, alice, "USD", -500)
	jtx.RequireIOUBalance(t, env, alice, bob, "USD", 500)

	// alice fails to clawback. Even though she is also an issuer,
	// the trustline balance is positive from her perspective
	result = env.Submit(clawback.Claw(alice, bob, "USD", 200).Build())
	jtx.RequireTxClaimed(t, result, "tecNO_PERMISSION")
	env.Close()
	jtx.RequireIOUBalance(t, env, bob, alice, "USD", -500)
	jtx.RequireIOUBalance(t, env, alice, bob, "USD", 500)
	jtx.RequireOwnerCount(t, env, alice, 1)
	jtx.RequireOwnerCount(t, env, bob, 1)

	// bob is able to successfully clawback from alice because
	// the trustline balance is negative from his perspective
	result = env.Submit(clawback.Claw(bob, alice, "USD", 200).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	jtx.RequireIOUBalance(t, env, bob, alice, "USD", -300)
	jtx.RequireIOUBalance(t, env, alice, bob, "USD", 300)

	// alice pays bob 1000 USD
	result = env.Submit(payment.PayIssued(alice, bob, tx.NewIssuedAmountFromFloat64(1000, "USD", alice.Address)).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// bob's balance becomes positive from his perspective because
	// alice issued more USD than the balance
	jtx.RequireIOUBalance(t, env, bob, alice, "USD", 700)
	jtx.RequireIOUBalance(t, env, alice, bob, "USD", -700)

	// bob is now the holder and fails to clawback
	result = env.Submit(clawback.Claw(bob, alice, "USD", 200).Build())
	jtx.RequireTxClaimed(t, result, "tecNO_PERMISSION")
	env.Close()
	jtx.RequireIOUBalance(t, env, bob, alice, "USD", 700)
	jtx.RequireIOUBalance(t, env, alice, bob, "USD", -700)
	jtx.RequireOwnerCount(t, env, alice, 1)
	jtx.RequireOwnerCount(t, env, bob, 1)

	// alice successfully claws back
	result = env.Submit(clawback.Claw(alice, bob, "USD", 200).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	jtx.RequireIOUBalance(t, env, bob, alice, "USD", 500)
	jtx.RequireIOUBalance(t, env, alice, bob, "USD", -500)
}

// TestClawback_DeleteDefaultLine tests that clawback deletes default trustlines.
// Reference: rippled Clawback_test.cpp testDeleteDefaultLine (lines 727-774)
func TestClawback_DeleteDefaultLine(t *testing.T) {
	// If clawback results the trustline to be default,
	// trustline should be automatically deleted
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")

	env.Fund(alice, bob)
	env.Close()

	// alice sets asfAllowTrustLineClawback
	result := env.Submit(accountset.AccountSet(alice).AllowClawback().Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()
	jtx.RequireFlagSet(t, env, alice, state.LsfAllowTrustLineClawback)

	// alice issues 1000 USD to bob
	env.Trust(bob, tx.NewIssuedAmountFromFloat64(1000, "USD", alice.Address))
	result = env.Submit(payment.PayIssued(alice, bob, tx.NewIssuedAmountFromFloat64(1000, "USD", alice.Address)).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	jtx.RequireOwnerCount(t, env, alice, 0)
	jtx.RequireOwnerCount(t, env, bob, 1)

	jtx.RequireIOUBalance(t, env, bob, alice, "USD", 1000)
	jtx.RequireIOUBalance(t, env, alice, bob, "USD", -1000)

	// set limit to default
	result = env.Submit(trustset.TrustSet(bob, tx.NewIssuedAmountFromFloat64(0, "USD", alice.Address)).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	jtx.RequireOwnerCount(t, env, alice, 0)
	jtx.RequireOwnerCount(t, env, bob, 1)

	// alice claws back full amount from bob, and should also delete trustline
	result = env.Submit(clawback.Claw(alice, bob, "USD", 1000).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// bob no longer owns the trustline because it was deleted
	jtx.RequireOwnerCount(t, env, alice, 0)
	jtx.RequireOwnerCount(t, env, bob, 0)
	jtx.RequireTrustLineNotExists(t, env, alice, bob, "USD")
	jtx.RequireLedgerEntryNotExists(t, env, keylet.Line(alice.ID, bob.ID, "USD"))
	jtx.RequireLines(t, env, alice, 0)
	jtx.RequireLines(t, env, bob, 0)
}

// TestClawback_FrozenLine tests clawback on frozen trust lines.
// Reference: rippled Clawback_test.cpp testFrozenLine (lines 777-820)
func TestClawback_FrozenLine(t *testing.T) {
	// Claws back from frozen trustline and the trustline should remain frozen
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")

	env.Fund(alice, bob)
	env.Close()

	// alice sets asfAllowTrustLineClawback
	result := env.Submit(accountset.AccountSet(alice).AllowClawback().Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()
	jtx.RequireFlagSet(t, env, alice, state.LsfAllowTrustLineClawback)

	// alice issues 1000 USD to bob
	env.Trust(bob, tx.NewIssuedAmountFromFloat64(1000, "USD", alice.Address))
	result = env.Submit(payment.PayIssued(alice, bob, tx.NewIssuedAmountFromFloat64(1000, "USD", alice.Address)).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	jtx.RequireIOUBalance(t, env, bob, alice, "USD", 1000)
	jtx.RequireIOUBalance(t, env, alice, bob, "USD", -1000)

	// freeze trustline
	env.FreezeTrustLine(alice, bob, "USD")
	env.Close()

	// alice claws back 200 USD from bob
	result = env.Submit(clawback.Claw(alice, bob, "USD", 200).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// bob should have 800 USD left
	jtx.RequireIOUBalance(t, env, bob, alice, "USD", 800)
	jtx.RequireIOUBalance(t, env, alice, bob, "USD", -800)

	data, err := env.Ledger().Read(keylet.Line(alice.ID, bob.ID, "USD"))
	require.NoError(t, err)
	require.NotNil(t, data)
	line, err := state.ParseRippleState(data)
	require.NoError(t, err)
	expected, opposite := state.LsfHighFreeze, state.LsfLowFreeze
	if state.CompareAccountIDs(alice.ID, bob.ID) < 0 {
		expected, opposite = state.LsfLowFreeze, state.LsfHighFreeze
	}
	require.NotZero(t, line.Flags&expected, "issuer-side freeze flag must remain set")
	require.Zero(t, line.Flags&opposite, "holder-side freeze flag must remain clear")
}

// TestClawback_AmountExceedsAvailable tests clawback when amount exceeds balance.
// Reference: rippled Clawback_test.cpp testAmountExceedsAvailable (lines 823-873)
func TestClawback_AmountExceedsAvailable(t *testing.T) {
	// When alice tries to claw back an amount that is greater
	// than what bob holds, only the max available balance is clawed
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")

	env.Fund(alice, bob)
	env.Close()

	// alice sets asfAllowTrustLineClawback
	result := env.Submit(accountset.AccountSet(alice).AllowClawback().Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()
	jtx.RequireFlagSet(t, env, alice, state.LsfAllowTrustLineClawback)

	// alice issues 1000 USD to bob
	env.Trust(bob, tx.NewIssuedAmountFromFloat64(1000, "USD", alice.Address))
	result = env.Submit(payment.PayIssued(alice, bob, tx.NewIssuedAmountFromFloat64(1000, "USD", alice.Address)).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	jtx.RequireIOUBalance(t, env, bob, alice, "USD", 1000)
	jtx.RequireIOUBalance(t, env, alice, bob, "USD", -1000)

	// alice tries to claw back 2000 USD
	result = env.Submit(clawback.Claw(alice, bob, "USD", 2000).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// check alice and bob's balance.
	// alice was only able to claw back 1000 USD at maximum
	jtx.RequireIOUBalance(t, env, bob, alice, "USD", 0)
	jtx.RequireIOUBalance(t, env, alice, bob, "USD", 0)

	// bob still owns the trustline because trustline is not in default state
	jtx.RequireOwnerCount(t, env, alice, 0)
	jtx.RequireOwnerCount(t, env, bob, 1)

	// set limit to default
	result = env.Submit(trustset.TrustSet(bob, tx.NewIssuedAmountFromFloat64(0, "USD", alice.Address)).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// verify that bob's trustline was deleted
	jtx.RequireOwnerCount(t, env, alice, 0)
	jtx.RequireOwnerCount(t, env, bob, 0)
	jtx.RequireTrustLineNotExists(t, env, alice, bob, "USD")
	jtx.RequireLedgerEntryNotExists(t, env, keylet.Line(alice.ID, bob.ID, "USD"))
	jtx.RequireLines(t, env, alice, 0)
	jtx.RequireLines(t, env, bob, 0)
}
