// Package check_test contains integration tests for Check transaction behavior.
// Tests ported from rippled's Check_test.cpp
package check_test

import (
	"testing"
	"time"

	"github.com/LeJamon/goXRPLd/internal/core/tx"
	accounttx "github.com/LeJamon/goXRPLd/internal/core/tx/account"
	jtx "github.com/LeJamon/goXRPLd/internal/testing"
	"github.com/LeJamon/goXRPLd/internal/testing/accountset"
	"github.com/LeJamon/goXRPLd/internal/testing/check"
	"github.com/LeJamon/goXRPLd/internal/testing/payment"
	"github.com/LeJamon/goXRPLd/internal/testing/trustset"
	"github.com/stretchr/testify/require"
)

// TestCheck_Enabled tests that checks are gated by the Checks amendment.
// Reference: rippled Check_test.cpp testEnabled (lines 140-189)
func TestCheck_Enabled(t *testing.T) {
	t.Run("AmendmentDisabled", func(t *testing.T) {
		// If the Checks amendment is not enabled, you should not be able
		// to create, cash, or cancel checks.
		env := jtx.NewTestEnv(t)
		env.DisableFeature("Checks")

		alice := jtx.NewAccount("alice")
		env.Fund(alice)
		env.Close()

		master := env.MasterAccount()
		chkID := check.GetCheckID(master, env.Seq(master))

		result := env.Submit(check.CheckCreate(master, alice, tx.NewXRPAmount(jtx.XRP(100))).Build())
		require.Equal(t, "temDISABLED", result.Code, "CheckCreate should be disabled")
		env.Close()

		result = env.Submit(check.CheckCashAmount(alice, chkID, tx.NewXRPAmount(jtx.XRP(100))).Build())
		require.Equal(t, "temDISABLED", result.Code, "CheckCash should be disabled")
		env.Close()

		result = env.Submit(check.CheckCancel(alice, chkID).Build())
		require.Equal(t, "temDISABLED", result.Code, "CheckCancel should be disabled")
		env.Close()
	})

	t.Run("AmendmentEnabled", func(t *testing.T) {
		// If the Checks amendment is enabled, all check-related facilities
		// should be available.
		env := jtx.NewTestEnv(t)

		alice := jtx.NewAccount("alice")
		env.Fund(alice)
		env.Close()

		master := env.MasterAccount()

		// Create and cash a check
		chkID1 := check.GetCheckID(master, env.Seq(master))
		result := env.Submit(check.CheckCreate(master, alice, tx.NewXRPAmount(jtx.XRP(100))).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		result = env.Submit(check.CheckCashAmount(alice, chkID1, tx.NewXRPAmount(jtx.XRP(100))).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// Create and cancel a check
		chkID2 := check.GetCheckID(master, env.Seq(master))
		result = env.Submit(check.CheckCreate(master, alice, tx.NewXRPAmount(jtx.XRP(100))).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		result = env.Submit(check.CheckCancel(alice, chkID2).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
	})
}

// TestCheck_CreateValid tests many valid ways to create a check.
// Reference: rippled Check_test.cpp testCreateValid (lines 192-289)
func TestCheck_CreateValid(t *testing.T) {
	env := jtx.NewTestEnv(t)

	gw := jtx.NewAccount("gateway")
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	USD := func(value float64) tx.Amount {
		return tx.NewIssuedAmountFromFloat64(value, "USD", gw.Address)
	}

	startBalance := uint64(jtx.XRP(1000))
	env.FundAmount(gw, startBalance)
	env.FundAmount(alice, startBalance)
	env.FundAmount(bob, startBalance)
	env.Close()

	// Helper: write two checks (XRP and IOU) from->to and verify owner counts.
	writeTwoChecks := func(from, to *jtx.Account) {
		fromOwnerCount := env.AccountInfo(from).OwnerCount
		toOwnerCount := env.AccountInfo(to).OwnerCount

		result := env.Submit(check.CheckCreate(from, to, tx.NewXRPAmount(jtx.XRP(2000))).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		result = env.Submit(check.CheckCreate(from, to, USD(50)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		jtx.RequireOwnerCount(t, env, from, fromOwnerCount+2)
		if from.Address != to.Address {
			jtx.RequireOwnerCount(t, env, to, toOwnerCount)
		}
	}

	t.Run("BasicPairs", func(t *testing.T) {
		writeTwoChecks(alice, bob)
		writeTwoChecks(gw, alice)
		writeTwoChecks(alice, gw)
	})

	t.Run("OptionalFields", func(t *testing.T) {
		// Expiration
		result := env.Submit(check.CheckCreate(alice, bob, USD(50)).
			Expiration(uint32(env.Now().Unix()-946684800) + 1).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// SourceTag
		result = env.Submit(check.CheckCreate(alice, bob, USD(50)).
			SourceTag(2).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// DestinationTag
		result = env.Submit(check.CheckCreate(alice, bob, USD(50)).
			DestTag(3).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// InvoiceID
		result = env.Submit(check.CheckCreate(alice, bob, USD(50)).
			InvoiceID("0000000000000000000000000000000000000000000000000000000000000004").Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// All optional fields combined
		result = env.Submit(check.CheckCreate(alice, bob, USD(50)).
			Expiration(uint32(env.Now().Unix()-946684800) + 1).
			SourceTag(12).
			DestTag(13).
			InvoiceID("0000000000000000000000000000000000000000000000000000000000000004").Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
	})

	// Reference: rippled Check_test.cpp testCreateValid (lines 266-289)
	t.Run("RegularKey", func(t *testing.T) {
		// alice uses her regular key to create a check.
		alie := jtx.NewAccountWithKeyType("alie", jtx.KeyTypeEd25519)
		env.SetRegularKey(alice, alie)
		env.Close()

		aliceOwnerCount := env.OwnerCount(alice)

		result := env.SubmitSignedWith(check.CheckCreate(alice, bob, USD(50)).Build(), alie)
		jtx.RequireTxSuccess(t, result)
		env.Close()
		jtx.RequireOwnerCount(t, env, alice, aliceOwnerCount+1)
	})

	t.Run("MultiSign", func(t *testing.T) {
		// Set up signers on alice.
		bogie := jtx.NewAccount("bogie")
		demon := jtx.NewAccountWithKeyType("demon", jtx.KeyTypeEd25519)

		env.SetSignerList(alice, 2, []jtx.TestSigner{
			{Account: bogie, Weight: 1},
			{Account: demon, Weight: 1},
		})
		env.Close()

		aliceOwnerCount := env.OwnerCount(alice)

		// alice uses multisigning to create a check.
		result := env.SubmitMultiSigned(
			check.CheckCreate(alice, bob, USD(50)).Build(),
			[]*jtx.Account{bogie, demon},
		)
		jtx.RequireTxSuccess(t, result)
		env.Close()
		jtx.RequireOwnerCount(t, env, alice, aliceOwnerCount+1)
	})
}

// TestCheck_CreateDisallowIncoming tests the DisallowIncomingCheck flag.
// Reference: rippled Check_test.cpp testCreateDisallowIncoming (lines 292-384)
func TestCheck_CreateDisallowIncoming(t *testing.T) {
	t.Run("FlagNotSetWithoutAmendment", func(t *testing.T) {
		// Test flag doesn't set unless amendment enabled
		env := jtx.NewTestEnv(t)
		env.DisableFeature("DisallowIncoming")

		alice := jtx.NewAccount("alice")
		env.FundAmount(alice, uint64(jtx.XRP(10000)))
		env.Close()

		// Set the DisallowIncomingCheck flag
		result := env.Submit(accountset.AccountSet(alice).
			SetFlag(accounttx.AccountSetFlagDisallowIncomingCheck).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// The flag should NOT be set on the account (amendment not enabled)
		info := env.AccountInfo(alice)
		require.NotNil(t, info)
		// lsfDisallowIncomingCheck = 0x08000000
		require.Equal(t, uint32(0), info.Flags&0x08000000,
			"DisallowIncomingCheck flag should not be set without amendment")
	})

	t.Run("FlagBlocksIncomingChecks", func(t *testing.T) {
		env := jtx.NewTestEnv(t)

		gw := jtx.NewAccount("gateway")
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		USD := func(value float64) tx.Amount {
			return tx.NewIssuedAmountFromFloat64(value, "USD", gw.Address)
		}

		env.FundAmount(gw, uint64(jtx.XRP(1000)))
		env.FundAmount(alice, uint64(jtx.XRP(1000)))
		env.FundAmount(bob, uint64(jtx.XRP(1000)))
		env.Close()

		// Enable DisallowIncomingCheck on both alice and bob
		result := env.Submit(accountset.AccountSet(bob).
			SetFlag(accounttx.AccountSetFlagDisallowIncomingCheck).Build())
		jtx.RequireTxSuccess(t, result)
		result = env.Submit(accountset.AccountSet(alice).
			SetFlag(accounttx.AccountSetFlagDisallowIncomingCheck).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// Both alice and bob can't receive checks
		result = env.Submit(check.CheckCreate(alice, bob, tx.NewXRPAmount(jtx.XRP(2000))).Build())
		require.Equal(t, "tecNO_PERMISSION", result.Code)
		env.Close()
		result = env.Submit(check.CheckCreate(alice, bob, USD(50)).Build())
		require.Equal(t, "tecNO_PERMISSION", result.Code)
		env.Close()

		result = env.Submit(check.CheckCreate(gw, alice, tx.NewXRPAmount(jtx.XRP(2000))).Build())
		require.Equal(t, "tecNO_PERMISSION", result.Code)
		env.Close()
		result = env.Submit(check.CheckCreate(gw, alice, USD(50)).Build())
		require.Equal(t, "tecNO_PERMISSION", result.Code)
		env.Close()

		// Remove flag from alice but not from bob
		result = env.Submit(accountset.AccountSet(alice).
			ClearFlag(accounttx.AccountSetFlagDisallowIncomingCheck).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// Now bob can send alice a check but not vice-versa
		result = env.Submit(check.CheckCreate(bob, alice, tx.NewXRPAmount(jtx.XRP(2000))).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
		result = env.Submit(check.CheckCreate(bob, alice, USD(50)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		result = env.Submit(check.CheckCreate(alice, bob, tx.NewXRPAmount(jtx.XRP(2000))).Build())
		require.Equal(t, "tecNO_PERMISSION", result.Code)
		env.Close()
		result = env.Submit(check.CheckCreate(alice, bob, USD(50)).Build())
		require.Equal(t, "tecNO_PERMISSION", result.Code)
		env.Close()

		// Remove bob's flag too
		result = env.Submit(accountset.AccountSet(bob).
			ClearFlag(accounttx.AccountSetFlagDisallowIncomingCheck).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// Now they can send checks freely
		result = env.Submit(check.CheckCreate(bob, alice, tx.NewXRPAmount(jtx.XRP(2000))).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
		result = env.Submit(check.CheckCreate(alice, bob, tx.NewXRPAmount(jtx.XRP(2000))).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
	})
}

// TestCheck_CreateInvalid tests many invalid ways to create a check.
// Reference: rippled Check_test.cpp testCreateInvalid (lines 387-571)
func TestCheck_CreateInvalid(t *testing.T) {
	env := jtx.NewTestEnv(t)

	gw1 := jtx.NewAccount("gateway1")
	gwF := jtx.NewAccount("gatewayFrozen")
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	USD := func(value float64) tx.Amount {
		return tx.NewIssuedAmountFromFloat64(value, "USD", gw1.Address)
	}

	env.FundAmount(gw1, uint64(jtx.XRP(1000)))
	env.FundAmount(gwF, uint64(jtx.XRP(1000)))
	env.FundAmount(alice, uint64(jtx.XRP(1000)))
	env.FundAmount(bob, uint64(jtx.XRP(1000)))
	env.Close()

	// Bad fee
	t.Run("BadFee", func(t *testing.T) {
		result := env.Submit(check.CheckCreate(alice, bob, USD(50)).Fee(0).Build())
		require.Equal(t, "temBAD_FEE", result.Code)
		env.Close()
	})

	// Bad flags
	t.Run("BadFlags", func(t *testing.T) {
		// tfImmediateOrCancel = 0x00020000
		result := env.Submit(check.CheckCreate(alice, bob, USD(50)).Flags(0x00020000).Build())
		require.Equal(t, "temINVALID_FLAG", result.Code)
		env.Close()
	})

	// Check to self
	t.Run("CheckToSelf", func(t *testing.T) {
		result := env.Submit(check.CheckCreate(alice, alice, tx.NewXRPAmount(jtx.XRP(10))).Build())
		require.Equal(t, "temREDUNDANT", result.Code)
		env.Close()
	})

	// Bad amount
	t.Run("BadAmountNegativeXRP", func(t *testing.T) {
		result := env.Submit(check.CheckCreate(alice, bob, tx.NewXRPAmount(-1)).Build())
		require.Equal(t, "temBAD_AMOUNT", result.Code)
		env.Close()
	})

	t.Run("BadAmountZeroXRP", func(t *testing.T) {
		result := env.Submit(check.CheckCreate(alice, bob, tx.NewXRPAmount(0)).Build())
		require.Equal(t, "temBAD_AMOUNT", result.Code)
		env.Close()
	})

	t.Run("ValidMinimalXRP", func(t *testing.T) {
		result := env.Submit(check.CheckCreate(alice, bob, tx.NewXRPAmount(1)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
	})

	t.Run("BadAmountNegativeIOU", func(t *testing.T) {
		result := env.Submit(check.CheckCreate(alice, bob, tx.NewIssuedAmountFromFloat64(-1, "USD", gw1.Address)).Build())
		require.Equal(t, "temBAD_AMOUNT", result.Code)
		env.Close()
	})

	t.Run("BadAmountZeroIOU", func(t *testing.T) {
		result := env.Submit(check.CheckCreate(alice, bob, tx.NewIssuedAmountFromFloat64(0, "USD", gw1.Address)).Build())
		require.Equal(t, "temBAD_AMOUNT", result.Code)
		env.Close()
	})

	t.Run("ValidMinimalIOU", func(t *testing.T) {
		result := env.Submit(check.CheckCreate(alice, bob, USD(1)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
	})

	// Bad currency
	t.Run("BadCurrency", func(t *testing.T) {
		// badCurrency() in rippled returns a currency with all zeros except byte 0
		badAmount := tx.NewIssuedAmountFromFloat64(2, "\x00\x00\x00", gw1.Address)
		result := env.Submit(check.CheckCreate(alice, bob, badAmount).Build())
		require.Equal(t, "temBAD_CURRENCY", result.Code)
		env.Close()
	})

	// Bad expiration (zero)
	t.Run("BadExpiration", func(t *testing.T) {
		result := env.Submit(check.CheckCreate(alice, bob, USD(50)).
			Expiration(0).Build())
		require.Equal(t, "temBAD_EXPIRATION", result.Code)
		env.Close()
	})

	// Destination does not exist
	t.Run("DestinationNotFound", func(t *testing.T) {
		bogie := jtx.NewAccount("bogie")
		result := env.Submit(check.CheckCreate(alice, bogie, USD(50)).Build())
		require.Equal(t, "tecNO_DST", result.Code)
		env.Close()
	})

	// Require destination tag
	t.Run("RequireDestTag", func(t *testing.T) {
		// Set asfRequireDest on bob
		result := env.Submit(accountset.AccountSet(bob).RequireDest().Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// Check without dest tag should fail
		result = env.Submit(check.CheckCreate(alice, bob, USD(50)).Build())
		require.Equal(t, "tecDST_TAG_NEEDED", result.Code)
		env.Close()

		// Check with dest tag should succeed
		result = env.Submit(check.CheckCreate(alice, bob, USD(50)).DestTag(11).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// Clear the RequireDest flag
		result = env.Submit(accountset.AccountSet(bob).
			ClearFlag(accounttx.AccountSetFlagRequireDest).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
	})

	// Globally frozen asset
	t.Run("GloballyFrozenAsset", func(t *testing.T) {
		USF := func(value float64) tx.Amount {
			return tx.NewIssuedAmountFromFloat64(value, "USF", gwF.Address)
		}

		env.EnableGlobalFreeze(gwF)
		env.Close()

		result := env.Submit(check.CheckCreate(alice, bob, USF(50)).Build())
		require.Equal(t, "tecFROZEN", result.Code)
		env.Close()

		env.DisableGlobalFreeze(gwF)
		env.Close()

		result = env.Submit(check.CheckCreate(alice, bob, USF(50)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
	})

	// Frozen trust line
	t.Run("FrozenTrustLine", func(t *testing.T) {
		// Set up trust lines
		result := env.Submit(trustset.TrustSet(alice, USD(1000)).Build())
		jtx.RequireTxSuccess(t, result)
		result = env.Submit(trustset.TrustSet(bob, USD(1000)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
		result = env.Submit(payment.PayIssued(gw1, alice, USD(25)).Build())
		jtx.RequireTxSuccess(t, result)
		result = env.Submit(payment.PayIssued(gw1, bob, USD(25)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// Freeze alice's trust line from gw1's side
		aliceUSD := tx.NewIssuedAmountFromFloat64(0, "USD", alice.Address)
		result = env.Submit(trustset.TrustSet(gw1, aliceUSD).Freeze().Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// alice can't create USD check (her line is frozen)
		result = env.Submit(check.CheckCreate(alice, bob, USD(50)).Build())
		require.Equal(t, "tecFROZEN", result.Code)
		env.Close()

		// bob can still create USD check to alice
		result = env.Submit(check.CheckCreate(bob, alice, USD(50)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// gw1 can still create USD check to alice
		result = env.Submit(check.CheckCreate(gw1, alice, USD(50)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// Clear the freeze
		result = env.Submit(trustset.TrustSet(gw1, aliceUSD).ClearFreeze().Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// Now alice can create USD checks again
		result = env.Submit(check.CheckCreate(alice, bob, USD(50)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// Freeze from alice's side
		result = env.Submit(trustset.TrustSet(alice, USD(0)).Freeze().Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// alice can still create USD checks
		result = env.Submit(check.CheckCreate(alice, bob, USD(50)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// But bob and gw1 can't create USD checks to alice
		result = env.Submit(check.CheckCreate(bob, alice, USD(50)).Build())
		require.Equal(t, "tecFROZEN", result.Code)
		env.Close()

		result = env.Submit(check.CheckCreate(gw1, alice, USD(50)).Build())
		require.Equal(t, "tecFROZEN", result.Code)
		env.Close()

		// Clear the freeze from alice's side
		result = env.Submit(trustset.TrustSet(alice, USD(0)).ClearFreeze().Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
	})

	// Expired expiration
	t.Run("ExpiredExpiration", func(t *testing.T) {
		// Ripple epoch: current close time
		now := uint32(env.Now().Unix() - 946684800)
		result := env.Submit(check.CheckCreate(alice, bob, USD(50)).Expiration(now).Build())
		require.Equal(t, "tecEXPIRED", result.Code)
		env.Close()

		// Expiration well in the future should succeed (use +600 to survive Close() time advances)
		result = env.Submit(check.CheckCreate(alice, bob, USD(50)).Expiration(now + 600).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
	})

	// Insufficient reserve
	t.Run("InsufficientReserve", func(t *testing.T) {
		cheri := jtx.NewAccount("cheri")
		// Fund cheri with just barely not enough for reserve + one owner object
		// accountReserve(1) = reserveBase + 1*reserveIncrement = 10 XRP + 2 XRP = 12 XRP
		reserveForOne := env.ReserveBase() + env.ReserveIncrement()
		env.FundAmount(cheri, reserveForOne-1)
		env.Close()

		result := env.Submit(check.CheckCreate(cheri, bob, USD(50)).Build())
		require.Equal(t, "tecINSUFFICIENT_RESERVE", result.Code)
		env.Close()

		// Give cheri a bit more
		result = env.Submit(payment.Pay(bob, cheri, env.BaseFee()+1).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		result = env.Submit(check.CheckCreate(cheri, bob, USD(50)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
	})
}

// TestCheck_CashXRP tests many valid ways to cash a check for XRP.
// Reference: rippled Check_test.cpp testCashXRP (lines 574-691)
func TestCheck_CashXRP(t *testing.T) {
	env := jtx.NewTestEnv(t)

	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")

	baseFee := env.BaseFee()
	startBalance := uint64(jtx.XRP(300))
	env.FundAmount(alice, startBalance)
	env.FundAmount(bob, startBalance)
	env.Close()

	t.Run("BasicXRPCheck", func(t *testing.T) {
		chkID := check.GetCheckID(alice, env.Seq(alice))
		result := env.Submit(check.CheckCreate(alice, bob, tx.NewXRPAmount(jtx.XRP(10))).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		jtx.RequireBalance(t, env, alice, startBalance-baseFee)
		jtx.RequireBalance(t, env, bob, startBalance)
		jtx.RequireOwnerCount(t, env, alice, 1)
		jtx.RequireOwnerCount(t, env, bob, 0)

		result = env.Submit(check.CheckCashAmount(bob, chkID, tx.NewXRPAmount(jtx.XRP(10))).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		jtx.RequireBalance(t, env, alice, startBalance-uint64(jtx.XRP(10))-baseFee)
		jtx.RequireBalance(t, env, bob, startBalance+uint64(jtx.XRP(10))-baseFee)
		jtx.RequireOwnerCount(t, env, alice, 0)
		jtx.RequireOwnerCount(t, env, bob, 0)

		// Reset balances for next test
		master := env.MasterAccount()
		env.Submit(payment.Pay(master, alice, uint64(jtx.XRP(10))+baseFee).Build())
		env.Submit(payment.Pay(bob, master, uint64(jtx.XRP(10))-baseFee*2).Build())
		env.Close()
		jtx.RequireBalance(t, env, alice, startBalance)
		jtx.RequireBalance(t, env, bob, startBalance)
	})

	t.Run("CheckIntoReserve", func(t *testing.T) {
		// Write a check that chews into alice's reserve.
		reserve := env.ReserveBase()
		checkAmount := startBalance - reserve - baseFee
		chkID := check.GetCheckID(alice, env.Seq(alice))
		result := env.Submit(check.CheckCreate(alice, bob, tx.NewXRPAmount(int64(checkAmount))).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// bob tries to cash for more than the check amount
		result = env.Submit(check.CheckCashAmount(bob, chkID, tx.NewXRPAmount(int64(checkAmount+1))).Build())
		require.Equal(t, "tecPATH_PARTIAL", result.Code)
		env.Close()

		result = env.Submit(check.CheckCashDeliverMin(bob, chkID, tx.NewXRPAmount(int64(checkAmount+1))).Build())
		require.Equal(t, "tecPATH_PARTIAL", result.Code)
		env.Close()

		// bob cashes exactly the check amount with DeliverMin. Succeeds because
		// one unit of alice's reserve is released when the check is consumed.
		result = env.Submit(check.CheckCashDeliverMin(bob, chkID, tx.NewXRPAmount(int64(checkAmount))).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		jtx.RequireBalance(t, env, alice, reserve)
		jtx.RequireBalance(t, env, bob, startBalance+checkAmount-baseFee*3)
		jtx.RequireOwnerCount(t, env, alice, 0)
		jtx.RequireOwnerCount(t, env, bob, 0)

		// Reset balances
		master := env.MasterAccount()
		env.Submit(payment.Pay(master, alice, checkAmount+baseFee).Build())
		env.Submit(payment.Pay(bob, master, checkAmount-baseFee*4).Build())
		env.Close()
		jtx.RequireBalance(t, env, alice, startBalance)
		jtx.RequireBalance(t, env, bob, startBalance)
	})

	t.Run("CheckPastBalance", func(t *testing.T) {
		// Write a check that goes one drop past what alice can pay.
		reserve := env.ReserveBase()
		checkAmount := startBalance - reserve - baseFee + 1
		chkID := check.GetCheckID(alice, env.Seq(alice))
		result := env.Submit(check.CheckCreate(alice, bob, tx.NewXRPAmount(int64(checkAmount))).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// bob tries to cash for exactly the check amount. Fails because
		// alice is one drop shy of funding the check.
		result = env.Submit(check.CheckCashAmount(bob, chkID, tx.NewXRPAmount(int64(checkAmount))).Build())
		require.Equal(t, "tecPATH_PARTIAL", result.Code)
		env.Close()

		// bob decides to get what he can from the bounced check.
		result = env.Submit(check.CheckCashDeliverMin(bob, chkID, tx.NewXRPAmount(1)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		jtx.RequireBalance(t, env, alice, reserve)
		jtx.RequireBalance(t, env, bob, startBalance+checkAmount-baseFee*2-1)
		jtx.RequireOwnerCount(t, env, alice, 0)
		jtx.RequireOwnerCount(t, env, bob, 0)
	})
}

// TestCheck_CashIOU tests many valid ways to cash a check for an IOU.
// Reference: rippled Check_test.cpp testCashIOU (lines 694-1084)
func TestCheck_CashIOU(t *testing.T) {
	t.Run("SimpleIOUWithAmount", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		// Disable CheckCashMakesTrustLine so missing trust line returns tecNO_LINE
		// (matches rippled's first test pass: sa - featureCheckCashMakesTrustLine)
		env.DisableFeature("CheckCashMakesTrustLine")

		gw := jtx.NewAccount("gateway")
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		USD := func(value float64) tx.Amount {
			return tx.NewIssuedAmountFromFloat64(value, "USD", gw.Address)
		}

		env.Fund(gw, alice, bob)
		env.Close()

		// alice writes the check before she gets the funds
		chkID1 := check.GetCheckID(alice, env.Seq(alice))
		result := env.Submit(check.CheckCreate(alice, bob, USD(10)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// bob attempts to cash - should fail (alice has no USD)
		result = env.Submit(check.CheckCashAmount(bob, chkID1, USD(10)).Build())
		require.Equal(t, "tecPATH_PARTIAL", result.Code)
		env.Close()

		// alice gets almost enough funds
		result = env.Submit(trustset.TrustSet(alice, USD(20)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
		result = env.Submit(payment.PayIssued(gw, alice, USD(9.5)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// bob tries again - still fails
		result = env.Submit(check.CheckCashAmount(bob, chkID1, USD(10)).Build())
		require.Equal(t, "tecPATH_PARTIAL", result.Code)
		env.Close()

		// alice gets the last of the necessary funds
		result = env.Submit(payment.PayIssued(gw, alice, USD(0.5)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// bob tries but has no trust line for USD
		result = env.Submit(check.CheckCashAmount(bob, chkID1, USD(10)).Build())
		require.Equal(t, "tecNO_LINE", result.Code)
		env.Close()

		// bob sets up trust line, but not high enough
		result = env.Submit(trustset.TrustSet(bob, USD(9.5)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		result = env.Submit(check.CheckCashAmount(bob, chkID1, USD(10)).Build())
		require.Equal(t, "tecPATH_PARTIAL", result.Code)
		env.Close()

		// bob sets trust line high enough but asks for more than SendMax
		result = env.Submit(trustset.TrustSet(bob, USD(10.5)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		result = env.Submit(check.CheckCashAmount(bob, chkID1, USD(10.5)).Build())
		require.Equal(t, "tecPATH_PARTIAL", result.Code)
		env.Close()

		// bob asks for exactly the check amount - succeeds
		result = env.Submit(check.CheckCashAmount(bob, chkID1, USD(10)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// Verify balances
		aliceUSD := env.IOUBalance(alice, gw, "USD")
		require.NotNil(t, aliceUSD)
		bobUSD := env.IOUBalance(bob, gw, "USD")
		require.NotNil(t, bobUSD)
		jtx.RequireOwnerCount(t, env, alice, 1) // trust line
		jtx.RequireOwnerCount(t, env, bob, 1)   // trust line

		// Double-cash should fail
		result = env.Submit(check.CheckCashAmount(bob, chkID1, USD(10)).Build())
		require.Equal(t, "tecNO_ENTRY", result.Code)
		env.Close()
	})

	t.Run("SimpleIOUWithDeliverMin", func(t *testing.T) {
		env := jtx.NewTestEnv(t)

		gw := jtx.NewAccount("gateway")
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		USD := func(value float64) tx.Amount {
			return tx.NewIssuedAmountFromFloat64(value, "USD", gw.Address)
		}

		env.Fund(gw, alice, bob)
		env.Close()

		// Set up trust lines and fund alice
		result := env.Submit(trustset.TrustSet(alice, USD(20)).Build())
		jtx.RequireTxSuccess(t, result)
		result = env.Submit(trustset.TrustSet(bob, USD(20)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
		result = env.Submit(payment.PayIssued(gw, alice, USD(8)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// Create checks
		chkID9 := check.GetCheckID(alice, env.Seq(alice))
		env.Submit(check.CheckCreate(alice, bob, USD(9)).Build())
		env.Close()
		chkID8 := check.GetCheckID(alice, env.Seq(alice))
		env.Submit(check.CheckCreate(alice, bob, USD(8)).Build())
		env.Close()
		chkID7 := check.GetCheckID(alice, env.Seq(alice))
		env.Submit(check.CheckCreate(alice, bob, USD(7)).Build())
		env.Close()
		chkID6 := check.GetCheckID(alice, env.Seq(alice))
		env.Submit(check.CheckCreate(alice, bob, USD(6)).Build())
		env.Close()

		// DeliverMin exceeding available fails
		result = env.Submit(check.CheckCashDeliverMin(bob, chkID9, USD(9)).Build())
		require.Equal(t, "tecPATH_PARTIAL", result.Code)
		env.Close()

		// Lower DeliverMin succeeds, delivers what's available
		result = env.Submit(check.CheckCashDeliverMin(bob, chkID9, USD(7)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// Pay alice back some funds for next test
		result = env.Submit(payment.PayIssued(bob, alice, USD(7)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// Exact match with DeliverMin
		result = env.Submit(check.CheckCashDeliverMin(bob, chkID7, USD(7)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// Fund alice for next test
		result = env.Submit(payment.PayIssued(bob, alice, USD(8)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// Partial cash with lower DeliverMin
		result = env.Submit(check.CheckCashDeliverMin(bob, chkID6, USD(4)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// Last check with minimal DeliverMin
		result = env.Submit(check.CheckCashDeliverMin(bob, chkID8, USD(2)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
	})

	t.Run("RequireAuth", func(t *testing.T) {
		env := jtx.NewTestEnv(t)

		gw := jtx.NewAccount("gateway")
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		USD := func(value float64) tx.Amount {
			return tx.NewIssuedAmountFromFloat64(value, "USD", gw.Address)
		}

		env.Fund(gw, alice, bob)
		env.Close()

		// gw sets require auth
		env.EnableRequireAuth(gw)
		env.Close()

		// Authorize alice and set up her trust line
		result := env.Submit(trustset.TrustSet(gw, tx.NewIssuedAmountFromFloat64(100, "USD", alice.Address)).Auth().Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
		result = env.Submit(trustset.TrustSet(alice, USD(20)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
		result = env.Submit(payment.PayIssued(gw, alice, USD(8)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// Create check from alice to bob
		chkID := check.GetCheckID(alice, env.Seq(alice))
		result = env.Submit(check.CheckCreate(alice, bob, USD(7)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// bob can't cash without authorization
		result = env.Submit(check.CheckCashAmount(bob, chkID, USD(7)).Build())
		require.Equal(t, "tecNO_AUTH", result.Code)
		env.Close()

		// bob sets up trust line but still unauthorized
		result = env.Submit(trustset.TrustSet(bob, USD(5)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		result = env.Submit(check.CheckCashAmount(bob, chkID, USD(7)).Build())
		require.Equal(t, "tecNO_AUTH", result.Code)
		env.Close()

		// gw authorizes bob
		result = env.Submit(trustset.TrustSet(gw, tx.NewIssuedAmountFromFloat64(1, "USD", bob.Address)).Auth().Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// Now bob can cash
		result = env.Submit(check.CheckCashDeliverMin(bob, chkID, USD(4)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
	})

	t.Run("MultiSign", func(t *testing.T) {
		// Reference: rippled Check_test.cpp testCashIOU (lines 1045-1082)
		env := jtx.NewTestEnv(t)

		gw := jtx.NewAccount("gateway")
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		USD := func(value float64) tx.Amount {
			return tx.NewIssuedAmountFromFloat64(value, "USD", gw.Address)
		}

		env.Fund(gw, alice, bob)
		env.Close()

		// alice creates checks ahead of time.
		chkID1 := check.GetCheckID(alice, env.Seq(alice))
		env.Submit(check.CheckCreate(alice, bob, USD(1)).Build())
		env.Close()

		chkID2 := check.GetCheckID(alice, env.Seq(alice))
		env.Submit(check.CheckCreate(alice, bob, USD(2)).Build())
		env.Close()

		// Set up trust lines and fund alice.
		env.Submit(trustset.TrustSet(alice, USD(20)).Build())
		env.Submit(trustset.TrustSet(bob, USD(20)).Build())
		env.Close()
		env.Submit(payment.PayIssued(gw, alice, USD(8)).Build())
		env.Close()

		// Give bob a regular key and signers.
		bobby := jtx.NewAccount("bobby")
		env.SetRegularKey(bob, bobby)
		env.Close()

		bogie := jtx.NewAccount("bogie")
		demon := jtx.NewAccountWithKeyType("demon", jtx.KeyTypeEd25519)
		env.SetSignerList(bob, 2, []jtx.TestSigner{
			{Account: bogie, Weight: 1},
			{Account: demon, Weight: 1},
		})
		env.Close()

		// bob's signer list has an owner count of 1 (featureMultiSignReserve enabled).
		// bob owns: 1 trust line + 1 signer list = 2
		signersCount := uint32(1)
		jtx.RequireOwnerCount(t, env, bob, signersCount+1)

		// bob uses his regular key to cash a check.
		result := env.SubmitSignedWith(
			check.CheckCashAmount(bob, chkID1, USD(1)).Build(), bobby)
		jtx.RequireTxSuccess(t, result)
		env.Close()
		jtx.RequireIOUBalance(t, env, alice, gw, "USD", 7)
		jtx.RequireIOUBalance(t, env, bob, gw, "USD", 1)
		jtx.RequireOwnerCount(t, env, alice, 2) // 1 trust line + 1 check remaining
		jtx.RequireOwnerCount(t, env, bob, signersCount+1)

		// bob uses multisigning to cash a check.
		result = env.SubmitMultiSigned(
			check.CheckCashAmount(bob, chkID2, USD(2)).Build(),
			[]*jtx.Account{bogie, demon},
		)
		jtx.RequireTxSuccess(t, result)
		env.Close()
		jtx.RequireIOUBalance(t, env, alice, gw, "USD", 5)
		jtx.RequireIOUBalance(t, env, bob, gw, "USD", 3)
		jtx.RequireOwnerCount(t, env, alice, 1) // 1 trust line, 0 checks
		jtx.RequireOwnerCount(t, env, bob, signersCount+1)
	})
}

// TestCheck_CashXferFee tests check cashing with transfer fees.
// Reference: rippled Check_test.cpp testCashXferFee (lines 1087-1155)
func TestCheck_CashXferFee(t *testing.T) {
	env := jtx.NewTestEnv(t)

	gw := jtx.NewAccount("gateway")
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	USD := func(value float64) tx.Amount {
		return tx.NewIssuedAmountFromFloat64(value, "USD", gw.Address)
	}

	env.Fund(gw, alice, bob)
	env.Close()

	// Set up trust lines and fund alice
	result := env.Submit(trustset.TrustSet(alice, USD(1000)).Build())
	jtx.RequireTxSuccess(t, result)
	result = env.Submit(trustset.TrustSet(bob, USD(1000)).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()
	result = env.Submit(payment.PayIssued(gw, alice, USD(1000)).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// gw sets 25% transfer fee (1.25 * 1e9 = 1,250,000,000)
	env.SetTransferRate(gw, 1250000000)
	env.Close()

	// Create checks
	chkID125 := check.GetCheckID(alice, env.Seq(alice))
	result = env.Submit(check.CheckCreate(alice, bob, USD(125)).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	chkID120 := check.GetCheckID(alice, env.Seq(alice))
	result = env.Submit(check.CheckCreate(alice, bob, USD(120)).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// Cash for face value - fails because 125 with 25% fee exceeds SendMax
	result = env.Submit(check.CheckCashAmount(bob, chkID125, USD(125)).Build())
	require.Equal(t, "tecPATH_PARTIAL", result.Code)
	env.Close()

	result = env.Submit(check.CheckCashDeliverMin(bob, chkID125, USD(101)).Build())
	require.Equal(t, "tecPATH_PARTIAL", result.Code)
	env.Close()

	// Cash with acceptable DeliverMin
	result = env.Submit(check.CheckCashDeliverMin(bob, chkID125, USD(75)).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// gw changes fee to 20%
	env.SetTransferRate(gw, 1200000000)
	env.Close()

	// Cash the second check with new rate
	result = env.Submit(check.CheckCashAmount(bob, chkID120, USD(50)).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()
}

// TestCheck_CashQuality tests check cashing with QualityIn/QualityOut settings.
// Reference: rippled Check_test.cpp testCashQuality (lines 1158-1363)
func TestCheck_CashQuality(t *testing.T) {
	env := jtx.NewTestEnv(t)

	gw := jtx.NewAccount("gateway")
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	USD := func(value float64) tx.Amount {
		return tx.NewIssuedAmountFromFloat64(value, "USD", gw.Address)
	}

	env.Fund(gw, alice, bob)
	env.Close()

	// Set up trust lines and fund alice
	result := env.Submit(trustset.TrustSet(alice, USD(1000)).Build())
	jtx.RequireTxSuccess(t, result)
	result = env.Submit(trustset.TrustSet(bob, USD(1000)).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()
	result = env.Submit(payment.PayIssued(gw, alice, USD(1000)).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// Test non-issuer to non-issuer with QualityIn on alice
	t.Run("AliceQualityIn50", func(t *testing.T) {
		// Set alice's QualityIn to 50% (500,000,000)
		result := env.Submit(trustset.TrustSet(alice, USD(1000)).QualityIn(500_000_000).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		chkID := check.GetCheckID(alice, env.Seq(alice))
		result = env.Submit(check.CheckCreate(alice, bob, USD(10)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// Check cash should deliver USD(10) regardless of alice's QualityIn
		result = env.Submit(check.CheckCashAmount(bob, chkID, USD(10)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// Reset
		result = env.Submit(trustset.TrustSet(alice, USD(1000)).QualityIn(0).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
		result = env.Submit(payment.PayIssued(bob, alice, USD(10)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
	})

	// Test non-issuer to non-issuer with QualityIn on bob
	// This is the only case where the Quality affects the outcome.
	// Reference: rippled Check_test.cpp testCashQuality line 1249-1251:
	//   testNonIssuerQCheck(bob, gw["USD"], qIn, 50, 5);
	// Bob's QualityIn=50% means requesting USD(5) actually delivers USD(10)
	// on the trust line (alice sends 10, bob receives 10 but at 50% quality).
	t.Run("BobQualityIn50", func(t *testing.T) {
		// Set bob's QualityIn to 50%
		result := env.Submit(trustset.TrustSet(bob, USD(1000)).QualityIn(500_000_000).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		chkID := check.GetCheckID(alice, env.Seq(alice))
		result = env.Submit(check.CheckCreate(alice, bob, USD(10)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// With QualityIn=50%, bob can only effectively request USD(5).
		// The flow engine needs srcToDst=10 to deliver quality-adjusted 5.
		result = env.Submit(check.CheckCashAmount(bob, chkID, USD(5)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// Reset
		result = env.Submit(trustset.TrustSet(bob, USD(1000)).QualityIn(0).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
		result = env.Submit(payment.PayIssued(bob, alice, USD(10)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
	})

	// Test QualityOut on alice
	t.Run("AliceQualityOut200", func(t *testing.T) {
		result := env.Submit(trustset.TrustSet(alice, USD(1000)).QualityOut(2_000_000_000).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		chkID := check.GetCheckID(alice, env.Seq(alice))
		result = env.Submit(check.CheckCreate(alice, bob, USD(10)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		result = env.Submit(check.CheckCashAmount(bob, chkID, USD(10)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// Reset
		result = env.Submit(trustset.TrustSet(alice, USD(1000)).QualityOut(0).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
		result = env.Submit(payment.PayIssued(bob, alice, USD(10)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
	})

	// Test QualityOut on bob
	t.Run("BobQualityOut200", func(t *testing.T) {
		result := env.Submit(trustset.TrustSet(bob, USD(1000)).QualityOut(2_000_000_000).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		chkID := check.GetCheckID(alice, env.Seq(alice))
		result = env.Submit(check.CheckCreate(alice, bob, USD(10)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		result = env.Submit(check.CheckCashAmount(bob, chkID, USD(10)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// Reset
		result = env.Submit(trustset.TrustSet(bob, USD(1000)).QualityOut(0).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
		result = env.Submit(payment.PayIssued(bob, alice, USD(10)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
	})
}

// TestCheck_CashInvalid tests many invalid ways to cash a check.
// Reference: rippled Check_test.cpp testCashInvalid (lines 1366-1662)
func TestCheck_CashInvalid(t *testing.T) {
	env := jtx.NewTestEnv(t)
	// Disable CheckCashMakesTrustLine so missing trust line returns tecNO_LINE
	// (matches rippled's first test pass: sa - featureCheckCashMakesTrustLine)
	env.DisableFeature("CheckCashMakesTrustLine")

	gw := jtx.NewAccount("gateway")
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	zoe := jtx.NewAccount("zoe")
	USD := func(value float64) tx.Amount {
		return tx.NewIssuedAmountFromFloat64(value, "USD", gw.Address)
	}

	env.Fund(gw, alice, bob, zoe)
	env.Close()

	// Set up alice's trustline
	result := env.Submit(trustset.TrustSet(alice, USD(20)).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()
	result = env.Submit(payment.PayIssued(gw, alice, USD(20)).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// bob tries to cash without a trust line
	t.Run("NoBobTrustLine", func(t *testing.T) {
		chkID := check.GetCheckID(alice, env.Seq(alice))
		env.Submit(check.CheckCreate(alice, bob, USD(20)).Build())
		env.Close()

		result := env.Submit(check.CheckCashAmount(bob, chkID, USD(20)).Build())
		require.Equal(t, "tecNO_LINE", result.Code)
		env.Close()
	})

	// Set up bob's trustline
	result = env.Submit(trustset.TrustSet(bob, USD(20)).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// bob tries to cash a non-existent check
	t.Run("NonExistentCheck", func(t *testing.T) {
		fakeChkID := check.GetCheckID(alice, env.Seq(alice))
		result := env.Submit(check.CheckCashAmount(bob, fakeChkID, USD(20)).Build())
		require.Equal(t, "tecNO_ENTRY", result.Code)
		env.Close()
	})

	// Create checks for the common failure tests
	chkIDU := check.GetCheckID(alice, env.Seq(alice))
	env.Submit(check.CheckCreate(alice, bob, USD(20)).Build())
	env.Close()

	chkIDX := check.GetCheckID(alice, env.Seq(alice))
	env.Submit(check.CheckCreate(alice, bob, tx.NewXRPAmount(jtx.XRP(10))).Build())
	env.Close()

	// Create an expiring check
	now := uint32(env.Now().Unix() - 946684800)
	chkIDExp := check.GetCheckID(alice, env.Seq(alice))
	env.Submit(check.CheckCreate(alice, bob, tx.NewXRPAmount(jtx.XRP(10))).Expiration(now + 1).Build())
	env.Close()

	// Create checks for freeze tests
	chkIDFroz1 := check.GetCheckID(alice, env.Seq(alice))
	env.Submit(check.CheckCreate(alice, bob, USD(1)).Build())
	env.Close()
	chkIDFroz2 := check.GetCheckID(alice, env.Seq(alice))
	env.Submit(check.CheckCreate(alice, bob, USD(2)).Build())
	env.Close()
	chkIDFroz3 := check.GetCheckID(alice, env.Seq(alice))
	env.Submit(check.CheckCreate(alice, bob, USD(3)).Build())
	env.Close()
	chkIDFroz4 := check.GetCheckID(alice, env.Seq(alice))
	env.Submit(check.CheckCreate(alice, bob, USD(4)).Build())
	env.Close()

	// Create checks for RequireDest tests
	chkIDNoDest1 := check.GetCheckID(alice, env.Seq(alice))
	env.Submit(check.CheckCreate(alice, bob, USD(1)).Build())
	env.Close()
	chkIDHasDest2 := check.GetCheckID(alice, env.Seq(alice))
	env.Submit(check.CheckCreate(alice, bob, USD(2)).DestTag(7).Build())
	env.Close()

	// Common failing cases for both XRP and IOU
	runFailingCases := func(t *testing.T, chkID string, amount tx.Amount, label string) {
		t.Run("BadFlags_"+label, func(t *testing.T) {
			result := env.Submit(check.CheckCashAmount(bob, chkID, amount).Flags(0x00020000).Build())
			require.Equal(t, "temINVALID_FLAG", result.Code)
			env.Close()
		})

		t.Run("NegativeAmount_"+label, func(t *testing.T) {
			// Create a negative version of the amount
			if amount.IsNative() {
				negAmt := tx.NewXRPAmount(-amount.Drops())
				result := env.Submit(check.CheckCashAmount(bob, chkID, negAmt).Build())
				require.Equal(t, "temBAD_AMOUNT", result.Code)
				env.Close()
			} else {
				negAmt := tx.NewIssuedAmountFromFloat64(-10, amount.Currency, amount.Issuer)
				result := env.Submit(check.CheckCashAmount(bob, chkID, negAmt).Build())
				require.Equal(t, "temBAD_AMOUNT", result.Code)
				env.Close()
			}
		})

		t.Run("ZeroAmount_"+label, func(t *testing.T) {
			if amount.IsNative() {
				zeroAmt := tx.NewXRPAmount(0)
				result := env.Submit(check.CheckCashAmount(bob, chkID, zeroAmt).Build())
				require.Equal(t, "temBAD_AMOUNT", result.Code)
				env.Close()
			} else {
				zeroAmt := tx.NewIssuedAmountFromFloat64(0, amount.Currency, amount.Issuer)
				result := env.Submit(check.CheckCashAmount(bob, chkID, zeroAmt).Build())
				require.Equal(t, "temBAD_AMOUNT", result.Code)
				env.Close()
			}
		})

		t.Run("NotDestination_"+label, func(t *testing.T) {
			// alice (creator) tries to cash her own check
			result := env.Submit(check.CheckCashAmount(alice, chkID, amount).Build())
			require.Equal(t, "tecNO_PERMISSION", result.Code)
			env.Close()

			// gw (outsider) tries to cash
			result = env.Submit(check.CheckCashAmount(gw, chkID, amount).Build())
			require.Equal(t, "tecNO_PERMISSION", result.Code)
			env.Close()
		})

		t.Run("AmountExceedsSendMax_"+label, func(t *testing.T) {
			// Double the amount to exceed SendMax
			if amount.IsNative() {
				doubleAmt := tx.NewXRPAmount(amount.Drops() * 2)
				result := env.Submit(check.CheckCashAmount(bob, chkID, doubleAmt).Build())
				require.Equal(t, "tecPATH_PARTIAL", result.Code)
				env.Close()
			} else {
				doubleAmt := tx.NewIssuedAmountFromFloat64(40, amount.Currency, amount.Issuer)
				result := env.Submit(check.CheckCashAmount(bob, chkID, doubleAmt).Build())
				require.Equal(t, "tecPATH_PARTIAL", result.Code)
				env.Close()
			}
		})

		t.Run("DeliverMinExceedsSendMax_"+label, func(t *testing.T) {
			if amount.IsNative() {
				doubleAmt := tx.NewXRPAmount(amount.Drops() * 2)
				result := env.Submit(check.CheckCashDeliverMin(bob, chkID, doubleAmt).Build())
				require.Equal(t, "tecPATH_PARTIAL", result.Code)
				env.Close()
			} else {
				doubleAmt := tx.NewIssuedAmountFromFloat64(40, amount.Currency, amount.Issuer)
				result := env.Submit(check.CheckCashDeliverMin(bob, chkID, doubleAmt).Build())
				require.Equal(t, "tecPATH_PARTIAL", result.Code)
				env.Close()
			}
		})
	}

	t.Run("XRP", func(t *testing.T) {
		runFailingCases(t, chkIDX, tx.NewXRPAmount(jtx.XRP(10)), "XRP")
	})

	t.Run("USD", func(t *testing.T) {
		runFailingCases(t, chkIDU, USD(20), "USD")
	})

	// Verify both checks are still cashable
	t.Run("VerifyCashable", func(t *testing.T) {
		result := env.Submit(check.CheckCashAmount(bob, chkIDU, USD(20)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		result = env.Submit(check.CheckCashDeliverMin(bob, chkIDX, tx.NewXRPAmount(jtx.XRP(10))).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
	})

	// Try to cash an expired check
	t.Run("ExpiredCheck", func(t *testing.T) {
		// Advance time past the expiration
		env.AdvanceTime(2 * time.Second)
		env.Close()

		result := env.Submit(check.CheckCashAmount(bob, chkIDExp, tx.NewXRPAmount(jtx.XRP(10))).Build())
		require.Equal(t, "tecEXPIRED", result.Code)
		env.Close()

		// Anyone can cancel an expired check
		result = env.Submit(check.CheckCancel(zoe, chkIDExp).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
	})

	// Frozen currency checks
	t.Run("FrozenCurrency", func(t *testing.T) {
		// Give alice her USD back for the frozen tests
		result := env.Submit(payment.PayIssued(bob, alice, USD(20)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// Global freeze
		env.EnableGlobalFreeze(gw)
		env.Close()

		result = env.Submit(check.CheckCashAmount(bob, chkIDFroz1, USD(1)).Build())
		require.Equal(t, "tecPATH_PARTIAL", result.Code)
		env.Close()

		result = env.Submit(check.CheckCashDeliverMin(bob, chkIDFroz1, USD(0.5)).Build())
		require.Equal(t, "tecPATH_PARTIAL", result.Code)
		env.Close()

		env.DisableGlobalFreeze(gw)
		env.Close()

		// No longer frozen - success
		result = env.Submit(check.CheckCashAmount(bob, chkIDFroz1, USD(1)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// Freeze individual trustline (alice's side from gw)
		aliceUSD := tx.NewIssuedAmountFromFloat64(0, "USD", alice.Address)
		env.Submit(trustset.TrustSet(gw, aliceUSD).Freeze().Build())
		env.Close()

		result = env.Submit(check.CheckCashAmount(bob, chkIDFroz2, USD(2)).Build())
		require.Equal(t, "tecPATH_PARTIAL", result.Code)
		env.Close()

		// Clear freeze
		env.Submit(trustset.TrustSet(gw, aliceUSD).ClearFreeze().Build())
		env.Close()

		result = env.Submit(check.CheckCashAmount(bob, chkIDFroz2, USD(2)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// Freeze bob's trustline
		bobUSD := tx.NewIssuedAmountFromFloat64(0, "USD", bob.Address)
		env.Submit(trustset.TrustSet(gw, bobUSD).Freeze().Build())
		env.Close()

		result = env.Submit(check.CheckCashAmount(bob, chkIDFroz3, USD(3)).Build())
		require.Equal(t, "tecFROZEN", result.Code)
		env.Close()

		result = env.Submit(check.CheckCashDeliverMin(bob, chkIDFroz3, USD(1)).Build())
		require.Equal(t, "tecFROZEN", result.Code)
		env.Close()

		// Clear bob's freeze
		env.Submit(trustset.TrustSet(gw, bobUSD).ClearFreeze().Build())
		env.Close()

		result = env.Submit(check.CheckCashDeliverMin(bob, chkIDFroz3, USD(1)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// Freeze from bob's direction
		env.Submit(trustset.TrustSet(bob, USD(20)).Freeze().Build())
		env.Close()

		result = env.Submit(check.CheckCashAmount(bob, chkIDFroz4, USD(4)).Build())
		require.Equal(t, "terNO_LINE", result.Code)
		env.Close()

		result = env.Submit(check.CheckCashDeliverMin(bob, chkIDFroz4, USD(1)).Build())
		require.Equal(t, "terNO_LINE", result.Code)
		env.Close()

		// Clear bob's freeze
		env.Submit(trustset.TrustSet(bob, USD(20)).ClearFreeze().Build())
		env.Close()

		result = env.Submit(check.CheckCashAmount(bob, chkIDFroz4, USD(4)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
	})

	// RequireDest flag
	t.Run("RequireDest", func(t *testing.T) {
		// Set RequireDest on bob
		result := env.Submit(accountset.AccountSet(bob).RequireDest().Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// Cash check without dest tag should fail
		result = env.Submit(check.CheckCashAmount(bob, chkIDNoDest1, USD(1)).Build())
		require.Equal(t, "tecDST_TAG_NEEDED", result.Code)
		env.Close()

		result = env.Submit(check.CheckCashDeliverMin(bob, chkIDNoDest1, USD(0.5)).Build())
		require.Equal(t, "tecDST_TAG_NEEDED", result.Code)
		env.Close()

		// Check with dest tag should work
		result = env.Submit(check.CheckCashAmount(bob, chkIDHasDest2, USD(2)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// Clear RequireDest so the other check can be cashed
		result = env.Submit(accountset.AccountSet(bob).
			ClearFlag(accounttx.AccountSetFlagRequireDest).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		result = env.Submit(check.CheckCashAmount(bob, chkIDNoDest1, USD(1)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
	})
}

// TestCheck_CancelValid tests many valid ways to cancel a check.
// Reference: rippled Check_test.cpp testCancelValid (lines 1665-1833)
func TestCheck_CancelValid(t *testing.T) {
	env := jtx.NewTestEnv(t)

	gw := jtx.NewAccount("gateway")
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	zoe := jtx.NewAccount("zoe")
	USD := func(value float64) tx.Amount {
		return tx.NewIssuedAmountFromFloat64(value, "USD", gw.Address)
	}

	env.Fund(gw, alice, bob, zoe)
	env.Close()

	// Create checks ahead of time.
	// Three ordinary checks with no expiration.
	chkID1 := check.GetCheckID(alice, env.Seq(alice))
	env.Submit(check.CheckCreate(alice, bob, USD(10)).Build())
	env.Close()

	chkID2 := check.GetCheckID(alice, env.Seq(alice))
	env.Submit(check.CheckCreate(alice, bob, tx.NewXRPAmount(jtx.XRP(10))).Build())
	env.Close()

	chkID3 := check.GetCheckID(alice, env.Seq(alice))
	env.Submit(check.CheckCreate(alice, bob, USD(10)).Build())
	env.Close()

	// Three checks that expire in 10 minutes.
	now := uint32(env.Now().Unix() - 946684800)
	chkIDNotExp1 := check.GetCheckID(alice, env.Seq(alice))
	env.Submit(check.CheckCreate(alice, bob, tx.NewXRPAmount(jtx.XRP(10))).Expiration(now + 600).Build())
	env.Close()

	chkIDNotExp2 := check.GetCheckID(alice, env.Seq(alice))
	env.Submit(check.CheckCreate(alice, bob, USD(10)).Expiration(now + 600).Build())
	env.Close()

	chkIDNotExp3 := check.GetCheckID(alice, env.Seq(alice))
	env.Submit(check.CheckCreate(alice, bob, tx.NewXRPAmount(jtx.XRP(10))).Expiration(now + 600).Build())
	env.Close()

	// Three checks that expire in 1 second.
	// Re-capture now so that expiration is in the future during creation.
	// All three are created without intermediate Close() calls since Close()
	// advances time by 10 seconds, which would expire them.
	now = uint32(env.Now().Unix() - 946684800)
	chkIDExp1 := check.GetCheckID(alice, env.Seq(alice))
	env.Submit(check.CheckCreate(alice, bob, USD(10)).Expiration(now + 1).Build())

	chkIDExp2 := check.GetCheckID(alice, env.Seq(alice))
	env.Submit(check.CheckCreate(alice, bob, tx.NewXRPAmount(jtx.XRP(10))).Expiration(now + 1).Build())

	chkIDExp3 := check.GetCheckID(alice, env.Seq(alice))
	env.Submit(check.CheckCreate(alice, bob, USD(10)).Expiration(now + 1).Build())
	env.Close()

	// Two checks to cancel using a regular key and using multisigning.
	// Reference: rippled Check_test.cpp testCancelValid (lines 1733-1740)
	chkIDReg := check.GetCheckID(alice, env.Seq(alice))
	env.Submit(check.CheckCreate(alice, bob, USD(10)).Build())
	env.Close()

	chkIDMSig := check.GetCheckID(alice, env.Seq(alice))
	env.Submit(check.CheckCreate(alice, bob, tx.NewXRPAmount(jtx.XRP(10))).Build())
	env.Close()

	jtx.RequireOwnerCount(t, env, alice, 11)

	// Creator cancels
	t.Run("CreatorCancels", func(t *testing.T) {
		result := env.Submit(check.CheckCancel(alice, chkID1).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
		jtx.RequireOwnerCount(t, env, alice, 10)
	})

	// Destination cancels
	t.Run("DestinationCancels", func(t *testing.T) {
		result := env.Submit(check.CheckCancel(bob, chkID2).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
		jtx.RequireOwnerCount(t, env, alice, 9)
	})

	// Outsider can't cancel non-expired check
	t.Run("OutsiderCantCancel", func(t *testing.T) {
		result := env.Submit(check.CheckCancel(zoe, chkID3).Build())
		require.Equal(t, "tecNO_PERMISSION", result.Code)
		env.Close()
		jtx.RequireOwnerCount(t, env, alice, 9)
	})

	// Creator cancels unexpired check
	t.Run("CreatorCancelsUnexpired", func(t *testing.T) {
		result := env.Submit(check.CheckCancel(alice, chkIDNotExp1).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
		jtx.RequireOwnerCount(t, env, alice, 8)
	})

	// Destination cancels unexpired check
	t.Run("DestinationCancelsUnexpired", func(t *testing.T) {
		result := env.Submit(check.CheckCancel(bob, chkIDNotExp2).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
		jtx.RequireOwnerCount(t, env, alice, 7)
	})

	// Outsider can't cancel unexpired check
	t.Run("OutsiderCantCancelUnexpired", func(t *testing.T) {
		result := env.Submit(check.CheckCancel(zoe, chkIDNotExp3).Build())
		require.Equal(t, "tecNO_PERMISSION", result.Code)
		env.Close()
		jtx.RequireOwnerCount(t, env, alice, 7)
	})

	// Advance time past expiration
	env.AdvanceTime(2 * time.Second)
	env.Close()

	// Creator cancels expired check
	t.Run("CreatorCancelsExpired", func(t *testing.T) {
		result := env.Submit(check.CheckCancel(alice, chkIDExp1).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
		jtx.RequireOwnerCount(t, env, alice, 6)
	})

	// Destination cancels expired check
	t.Run("DestinationCancelsExpired", func(t *testing.T) {
		result := env.Submit(check.CheckCancel(bob, chkIDExp2).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
		jtx.RequireOwnerCount(t, env, alice, 5)
	})

	// Outsider CAN cancel expired check
	t.Run("OutsiderCancelsExpired", func(t *testing.T) {
		result := env.Submit(check.CheckCancel(zoe, chkIDExp3).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
		jtx.RequireOwnerCount(t, env, alice, 4)
	})

	// Use a regular key and also multisign to cancel checks.
	// Reference: rippled Check_test.cpp testCancelValid (lines 1792-1820)
	t.Run("RegularKey", func(t *testing.T) {
		alie := jtx.NewAccountWithKeyType("alie", jtx.KeyTypeEd25519)
		env.SetRegularKey(alice, alie)
		env.Close()

		// alice uses her regular key to cancel a check.
		result := env.SubmitSignedWith(check.CheckCancel(alice, chkIDReg).Build(), alie)
		jtx.RequireTxSuccess(t, result)
		env.Close()
		// 4 checks remain - 1 cancelled = 3 checks + 1 signer list (set below)
		jtx.RequireOwnerCount(t, env, alice, 3)
	})

	t.Run("MultiSign", func(t *testing.T) {
		bogie := jtx.NewAccount("bogie")
		demon := jtx.NewAccountWithKeyType("demon", jtx.KeyTypeEd25519)

		env.SetSignerList(alice, 2, []jtx.TestSigner{
			{Account: bogie, Weight: 1},
			{Account: demon, Weight: 1},
		})
		env.Close()

		// featureMultiSignReserve is enabled: signer list = 1 owner
		signersCount := uint32(1)

		// alice uses multisigning to cancel a check.
		result := env.SubmitMultiSigned(
			check.CheckCancel(alice, chkIDMSig).Build(),
			[]*jtx.Account{bogie, demon},
		)
		jtx.RequireTxSuccess(t, result)
		env.Close()
		jtx.RequireOwnerCount(t, env, alice, signersCount+2)
	})

	// Creator and destination cancel the remaining checks.
	// Reference: rippled Check_test.cpp testCancelValid (lines 1822-1831)
	t.Run("CleanupRemaining", func(t *testing.T) {
		signersCount := uint32(1)

		result := env.Submit(check.CheckCancel(alice, chkID3).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
		jtx.RequireOwnerCount(t, env, alice, signersCount+1)

		result = env.Submit(check.CheckCancel(bob, chkIDNotExp3).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
		jtx.RequireOwnerCount(t, env, alice, signersCount+0)
	})
}

// TestCheck_CancelInvalid tests many invalid ways to cancel a check.
// Reference: rippled Check_test.cpp testCancelInvalid (lines 1836-1867)
func TestCheck_CancelInvalid(t *testing.T) {
	env := jtx.NewTestEnv(t)

	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")

	env.Fund(alice, bob)
	env.Close()

	// Bad fee
	t.Run("BadFee", func(t *testing.T) {
		fakeChkID := check.GetCheckID(alice, env.Seq(alice))
		result := env.Submit(check.CheckCancel(bob, fakeChkID).Fee(0).Build())
		require.Equal(t, "temBAD_FEE", result.Code)
		env.Close()
	})

	// Bad flags
	t.Run("BadFlags", func(t *testing.T) {
		fakeChkID := check.GetCheckID(alice, env.Seq(alice))
		result := env.Submit(check.CheckCancel(bob, fakeChkID).Flags(0x00020000).Build())
		require.Equal(t, "temINVALID_FLAG", result.Code)
		env.Close()
	})

	// Non-existent check
	t.Run("NonExistentCheck", func(t *testing.T) {
		fakeChkID := check.GetCheckID(alice, env.Seq(alice))
		result := env.Submit(check.CheckCancel(bob, fakeChkID).Build())
		require.Equal(t, "tecNO_ENTRY", result.Code)
		env.Close()
	})
}

// TestCheck_Fix1623Enable tests the fix1623 amendment for DeliveredAmount.
// Reference: rippled Check_test.cpp testFix1623Enable (lines 1870-1913)
func TestCheck_Fix1623Enable(t *testing.T) {
	t.Run("WithoutFix1623", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		env.DisableFeature("fix1623")

		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")

		env.Fund(alice, bob)
		env.Close()

		chkID := check.GetCheckID(alice, env.Seq(alice))
		result := env.Submit(check.CheckCreate(alice, bob, tx.NewXRPAmount(jtx.XRP(200))).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		result = env.Submit(check.CheckCashDeliverMin(bob, chkID, tx.NewXRPAmount(jtx.XRP(100))).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// Without fix1623, there should be NO DeliveredAmount in metadata
		// TODO: Verify metadata when RPC is available
	})

	t.Run("WithFix1623", func(t *testing.T) {
		env := jtx.NewTestEnv(t)

		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")

		env.Fund(alice, bob)
		env.Close()

		chkID := check.GetCheckID(alice, env.Seq(alice))
		result := env.Submit(check.CheckCreate(alice, bob, tx.NewXRPAmount(jtx.XRP(200))).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		result = env.Submit(check.CheckCashDeliverMin(bob, chkID, tx.NewXRPAmount(jtx.XRP(100))).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// With fix1623, DeliveredAmount and delivered_amount should be in metadata
		// TODO: Verify metadata when RPC is available
	})
}

// TestCheck_WithTickets tests check operations using tickets.
// Reference: rippled Check_test.cpp testWithTickets (lines 1916-2013)
func TestCheck_WithTickets(t *testing.T) {
	env := jtx.NewTestEnv(t)

	gw := jtx.NewAccount("gateway")
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	USD := func(value float64) tx.Amount {
		return tx.NewIssuedAmountFromFloat64(value, "USD", gw.Address)
	}

	env.Fund(gw, alice, bob)
	env.Close()

	// alice and bob grab enough tickets for all of the following transactions.
	// Note that once tickets are acquired, account sequence numbers should not advance.
	aliceTicketSeq := env.CreateTickets(alice, 10)
	aliceSeq := env.Seq(alice)

	bobTicketSeq := env.CreateTickets(bob, 10)
	bobSeq := env.Seq(bob)

	env.Close()
	jtx.RequireOwnerCount(t, env, alice, 10)
	jtx.RequireOwnerCount(t, env, bob, 10)

	// Set up trust lines using tickets.
	aliceTrust := trustset.TrustSet(alice, USD(1000)).Build()
	jtx.WithTicketSeq(aliceTrust, aliceTicketSeq)
	aliceTicketSeq++
	result := env.Submit(aliceTrust)
	jtx.RequireTxSuccess(t, result)

	bobTrust := trustset.TrustSet(bob, USD(1000)).Build()
	jtx.WithTicketSeq(bobTrust, bobTicketSeq)
	bobTicketSeq++
	result = env.Submit(bobTrust)
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// Owner counts stay at 10: used 1 ticket each (-1) but created 1 trust line each (+1).
	jtx.RequireOwnerCount(t, env, alice, 10)
	jtx.RequireOwnerCount(t, env, bob, 10)

	// Sequences should not have advanced.
	jtx.RequireSequence(t, env, alice, aliceSeq)
	jtx.RequireSequence(t, env, bob, bobSeq)

	// Fund alice with USD.
	result = env.Submit(payment.PayIssued(gw, alice, USD(900)).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// alice creates four checks using tickets: two XRP, two IOU.
	chkIdXrp1 := check.GetCheckID(alice, aliceTicketSeq)
	chkCreateXrp1 := check.CheckCreate(alice, bob, tx.NewXRPAmount(jtx.XRP(200))).Build()
	jtx.WithTicketSeq(chkCreateXrp1, aliceTicketSeq)
	aliceTicketSeq++
	result = env.Submit(chkCreateXrp1)
	jtx.RequireTxSuccess(t, result)

	chkIdXrp2 := check.GetCheckID(alice, aliceTicketSeq)
	chkCreateXrp2 := check.CheckCreate(alice, bob, tx.NewXRPAmount(jtx.XRP(300))).Build()
	jtx.WithTicketSeq(chkCreateXrp2, aliceTicketSeq)
	aliceTicketSeq++
	result = env.Submit(chkCreateXrp2)
	jtx.RequireTxSuccess(t, result)

	chkIdUsd1 := check.GetCheckID(alice, aliceTicketSeq)
	chkCreateUsd1 := check.CheckCreate(alice, bob, USD(200)).Build()
	jtx.WithTicketSeq(chkCreateUsd1, aliceTicketSeq)
	aliceTicketSeq++
	result = env.Submit(chkCreateUsd1)
	jtx.RequireTxSuccess(t, result)

	chkIdUsd2 := check.GetCheckID(alice, aliceTicketSeq)
	chkCreateUsd2 := check.CheckCreate(alice, bob, USD(300)).Build()
	jtx.WithTicketSeq(chkCreateUsd2, aliceTicketSeq)
	aliceTicketSeq++
	result = env.Submit(chkCreateUsd2)
	jtx.RequireTxSuccess(t, result)

	env.Close()

	// Alice used 4 tickets but created 4 checks. Owner count stays at 10.
	jtx.RequireOwnerCount(t, env, alice, 10)
	jtx.RequireSequence(t, env, alice, aliceSeq)
	jtx.RequireOwnerCount(t, env, bob, 10)
	jtx.RequireSequence(t, env, bob, bobSeq)

	// Bob cancels two of alice's checks using tickets.
	cancelXrp1 := check.CheckCancel(bob, chkIdXrp1).Build()
	jtx.WithTicketSeq(cancelXrp1, bobTicketSeq)
	bobTicketSeq++
	result = env.Submit(cancelXrp1)
	jtx.RequireTxSuccess(t, result)

	cancelUsd2 := check.CheckCancel(bob, chkIdUsd2).Build()
	jtx.WithTicketSeq(cancelUsd2, bobTicketSeq)
	bobTicketSeq++
	result = env.Submit(cancelUsd2)
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// Alice: 10 - 2 cancelled checks = 8; sequence unchanged.
	jtx.RequireOwnerCount(t, env, alice, 8)
	jtx.RequireSequence(t, env, alice, aliceSeq)

	// Bob: 10 - 2 tickets used = 8; sequence unchanged.
	jtx.RequireOwnerCount(t, env, bob, 8)
	jtx.RequireSequence(t, env, bob, bobSeq)

	// Bob cashes alice's two remaining checks using tickets.
	cashXrp2 := check.CheckCashAmount(bob, chkIdXrp2, tx.NewXRPAmount(jtx.XRP(300))).Build()
	jtx.WithTicketSeq(cashXrp2, bobTicketSeq)
	bobTicketSeq++
	result = env.Submit(cashXrp2)
	jtx.RequireTxSuccess(t, result)

	cashUsd1 := check.CheckCashAmount(bob, chkIdUsd1, USD(200)).Build()
	jtx.WithTicketSeq(cashUsd1, bobTicketSeq)
	bobTicketSeq++
	result = env.Submit(cashUsd1)
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// Alice: 8 - 2 cashed checks = 6; sequence unchanged.
	jtx.RequireOwnerCount(t, env, alice, 6)
	jtx.RequireSequence(t, env, alice, aliceSeq)
	jtx.RequireIOUBalance(t, env, alice, gw, "USD", 700)

	// Bob: 8 - 2 tickets used = 6; sequence unchanged.
	jtx.RequireOwnerCount(t, env, bob, 6)
	jtx.RequireSequence(t, env, bob, bobSeq)
	jtx.RequireIOUBalance(t, env, bob, gw, "USD", 200)
}

// TestCheck_TrustLineCreation tests automatic trust line creation when cashing checks.
// Reference: rippled Check_test.cpp testTrustLineCreation (lines 2016-2698)
func TestCheck_TrustLineCreation(t *testing.T) {
	// This test requires featureCheckCashMakesTrustLine

	t.Run("InsufficientReserve", func(t *testing.T) {
		env := jtx.NewTestEnv(t)

		gw1 := jtx.NewAccount("gw1")
		yui := jtx.NewAccount("yui")
		CK8 := func(value float64) tx.Amount {
			return tx.NewIssuedAmountFromFloat64(value, "CK8", gw1.Address)
		}

		env.FundAmount(gw1, uint64(jtx.XRP(5000)))
		// Fund yui with just barely not enough for reserve + 1 owner object
		// accountReserve(1) = reserveBase + reserveIncrement
		// rippled test uses 200 XRP with reserve of 250 XRP.
		// Our test env has reserveBase=10 XRP, reserveIncrement=2 XRP → 12 XRP for 1 owner.
		reserveForOne := env.ReserveBase() + env.ReserveIncrement()
		env.FundAmount(yui, reserveForOne-1)
		env.Close()

		// gw1 creates a CK8 check to yui
		chkID := check.GetCheckID(gw1, env.Seq(gw1))
		result := env.Submit(check.CheckCreate(gw1, yui, CK8(99)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// yui tries to cash but doesn't have reserve for trustline
		result = env.Submit(check.CheckCashAmount(yui, chkID, CK8(99)).Build())
		require.Equal(t, "tecNO_LINE_INSUF_RESERVE", result.Code)
		env.Close()

		// Fund yui with enough to cover the reserve
		master := env.MasterAccount()
		result = env.Submit(payment.Pay(master, yui, env.BaseFee()+2).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// Now yui can cash
		result = env.Submit(check.CheckCashAmount(yui, chkID, CK8(99)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		jtx.RequireOwnerCount(t, env, yui, 1) // trustline
	})

	t.Run("NoFlagsIssuerCheck", func(t *testing.T) {
		env := jtx.NewTestEnv(t)

		gw1 := jtx.NewAccount("gw1")
		alice := jtx.NewAccount("alice")
		CK1 := func(value float64) tx.Amount {
			return tx.NewIssuedAmountFromFloat64(value, "CK1", gw1.Address)
		}

		env.FundAmount(gw1, uint64(jtx.XRP(5000)))
		env.FundAmount(alice, uint64(jtx.XRP(5000)))
		env.Close()

		// Issuer creates check to alice (no trust line needed beforehand)
		chkID := check.GetCheckID(gw1, env.Seq(gw1))
		result := env.Submit(check.CheckCreate(gw1, alice, CK1(98)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// alice cashes - should auto-create trust line
		result = env.Submit(check.CheckCashAmount(alice, chkID, CK1(98)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		jtx.RequireOwnerCount(t, env, alice, 1) // auto-created trustline
	})

	t.Run("GlobalFreezeIssuerCheck", func(t *testing.T) {
		env := jtx.NewTestEnv(t)

		gw1 := jtx.NewAccount("gw1")
		alice := jtx.NewAccount("alice")
		CK5 := func(value float64) tx.Amount {
			return tx.NewIssuedAmountFromFloat64(value, "CK5", gw1.Address)
		}

		env.FundAmount(gw1, uint64(jtx.XRP(5000)))
		env.FundAmount(alice, uint64(jtx.XRP(5000)))
		env.Close()

		// Set global freeze
		env.EnableGlobalFreeze(gw1)
		env.Close()

		// CheckCreate fails with tecFROZEN because issuer is globally frozen
		// Reference: rippled Check_test.cpp L2522-2523
		chkID := check.GetCheckID(gw1, env.Seq(gw1))
		result := env.Submit(check.CheckCreate(gw1, alice, CK5(98)).Build())
		require.Equal(t, "tecFROZEN", result.Code)
		env.Close()

		// Cash fails with tecNO_ENTRY because the check was never created
		result = env.Submit(check.CheckCashAmount(alice, chkID, CK5(98)).Build())
		require.Equal(t, "tecNO_ENTRY", result.Code)
		env.Close()

		// No trustline created
		require.False(t, env.TrustLineExists(alice, gw1, "CK5"))
		jtx.RequireOwnerCount(t, env, alice, 0)
	})

	t.Run("RequireAuthIssuerCheck", func(t *testing.T) {
		env := jtx.NewTestEnv(t)

		gw2 := jtx.NewAccount("gw2")
		alice := jtx.NewAccount("alice")
		CK6 := func(value float64) tx.Amount {
			return tx.NewIssuedAmountFromFloat64(value, "CK6", gw2.Address)
		}

		env.FundAmount(gw2, uint64(jtx.XRP(5000)))
		env.FundAmount(alice, uint64(jtx.XRP(5000)))
		env.Close()

		// Set RequireAuth
		env.EnableRequireAuth(gw2)
		env.Close()

		chkID := check.GetCheckID(gw2, env.Seq(gw2))
		result := env.Submit(check.CheckCreate(gw2, alice, CK6(98)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// Cash should fail - alice is not authorized
		result = env.Submit(check.CheckCashAmount(alice, chkID, CK6(98)).Build())
		require.Equal(t, "tecNO_AUTH", result.Code)
		env.Close()

		// No trustline created
		require.False(t, env.TrustLineExists(alice, gw2, "CK6"))
		jtx.RequireOwnerCount(t, env, alice, 0)
	})

	t.Run("DefaultRippleIssuerCheck", func(t *testing.T) {
		env := jtx.NewTestEnv(t)

		gw1 := jtx.NewAccount("gw1")
		alice := jtx.NewAccount("alice")
		CK3 := func(value float64) tx.Amount {
			return tx.NewIssuedAmountFromFloat64(value, "CK3", gw1.Address)
		}

		env.FundAmount(gw1, uint64(jtx.XRP(5000)))
		env.FundAmount(alice, uint64(jtx.XRP(5000)))
		env.Close()

		// Enable DefaultRipple
		result := env.Submit(accountset.AccountSet(gw1).DefaultRipple().Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		chkID := check.GetCheckID(gw1, env.Seq(gw1))
		result = env.Submit(check.CheckCreate(gw1, alice, CK3(98)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		result = env.Submit(check.CheckCashAmount(alice, chkID, CK3(98)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		jtx.RequireOwnerCount(t, env, alice, 1)
	})

	t.Run("DepositAuthIssuerCheck", func(t *testing.T) {
		env := jtx.NewTestEnv(t)

		gw1 := jtx.NewAccount("gw1")
		alice := jtx.NewAccount("alice")
		CK4 := func(value float64) tx.Amount {
			return tx.NewIssuedAmountFromFloat64(value, "CK4", gw1.Address)
		}

		env.FundAmount(gw1, uint64(jtx.XRP(5000)))
		env.FundAmount(alice, uint64(jtx.XRP(5000)))
		env.Close()

		// Enable DepositAuth on both
		env.EnableDepositAuth(gw1)
		env.EnableDepositAuth(alice)
		env.Close()

		chkID := check.GetCheckID(gw1, env.Seq(gw1))
		result := env.Submit(check.CheckCreate(gw1, alice, CK4(98)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// DepositAuth should be ignored for check cash (destination signs)
		result = env.Submit(check.CheckCashAmount(alice, chkID, CK4(98)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		jtx.RequireOwnerCount(t, env, alice, 1)
	})
}
