package check_test

import (
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/accountset"
	checkbuilder "github.com/LeJamon/go-xrpl/internal/testing/check"
	"github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/internal/testing/trustset"
	"github.com/LeJamon/go-xrpl/internal/tx"
)

func TestCheck_NonIssuerTrustLineCreation(t *testing.T) {
	t.Run("NoDefaultRipple", func(t *testing.T) {
		env, gateway, alice, bob, USD := newNonIssuerCheckFixture(t)
		checkID := submitNonIssuerCheck(t, env, alice, bob, USD(25))

		jtx.RequireTxFail(t, env.Submit(checkbuilder.CheckCashAmount(bob, checkID, USD(25)).Build()), jtx.TerNO_RIPPLE)
		env.Close()
		requireCheckMPTExists(t, env, checkID, true)
		if env.TrustLineExists(bob, gateway, "USD") {
			t.Fatal("failed cash created a destination trust line")
		}
		jtx.RequireIOUBalance(t, env, alice, gateway, "USD", 50)
		jtx.RequireIOUBalance(t, env, bob, gateway, "USD", 0)
		jtx.RequireTxSuccess(t, env.Submit(checkbuilder.CheckCancel(alice, checkID).Build()))
		env.Close()
		requireCheckMPTExists(t, env, checkID, false)
	})

	t.Run("DefaultRipple", func(t *testing.T) {
		env, gateway, alice, bob, USD := newNonIssuerCheckFixture(t)
		jtx.RequireTxSuccess(t, env.Submit(accountset.AccountSet(gateway).DefaultRipple().Build()))
		env.Close()
		checkID := submitNonIssuerCheck(t, env, alice, bob, USD(25))

		result := env.Submit(checkbuilder.CheckCashAmount(bob, checkID, USD(25)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
		requireCheckMPTExists(t, env, checkID, false)
		requireTrustLineInBothDirs(t, env, bob, gateway, "USD")
		jtx.RequireIOUBalance(t, env, alice, gateway, "USD", 25)
		jtx.RequireIOUBalance(t, env, bob, gateway, "USD", 25)
		requireDeliveredAmount(t, result, USD(25))
	})

	t.Run("DepositAuth", func(t *testing.T) {
		env, gateway, alice, bob, USD := newNonIssuerCheckFixture(t)
		jtx.RequireTxSuccess(t, env.Submit(accountset.AccountSet(gateway).DefaultRipple().Build()))
		env.EnableDepositAuth(gateway)
		env.EnableDepositAuth(alice)
		env.EnableDepositAuth(bob)
		env.Close()
		checkID := submitNonIssuerCheck(t, env, alice, bob, USD(25))

		result := env.Submit(checkbuilder.CheckCashAmount(bob, checkID, USD(25)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
		requireCheckMPTExists(t, env, checkID, false)
		requireTrustLineInBothDirs(t, env, bob, gateway, "USD")
		jtx.RequireIOUBalance(t, env, alice, gateway, "USD", 25)
		jtx.RequireIOUBalance(t, env, bob, gateway, "USD", 25)
		jtx.RequireOwnerCount(t, env, alice, 1)
		jtx.RequireOwnerCount(t, env, bob, 1)
		requireDeliveredAmount(t, result, USD(25))
	})

	t.Run("GlobalFreeze", func(t *testing.T) {
		env, gateway, alice, bob, USD := newNonIssuerCheckFixture(t)
		env.EnableGlobalFreeze(gateway)
		env.Close()
		checkID := checkbuilder.GetCheckID(alice, env.Seq(alice))

		jtx.RequireTxClaimed(t, env.Submit(checkbuilder.CheckCreate(alice, bob, USD(25)).Build()), jtx.TecFROZEN)
		env.Close()
		requireCheckMPTExists(t, env, checkID, false)
		jtx.RequireTxClaimed(t, env.Submit(checkbuilder.CheckCashAmount(bob, checkID, USD(25)).Build()), jtx.TecNO_ENTRY)
		if env.TrustLineExists(bob, gateway, "USD") {
			t.Fatal("global freeze created a destination trust line")
		}
	})

	t.Run("RequireAuth", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		gateway := jtx.NewAccount("gateway")
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		USD := func(value float64) tx.Amount {
			return tx.NewIssuedAmountFromFloat64(value, "USD", gateway.Address)
		}
		env.FundNoRipple(gateway, alice, bob)
		env.Close()
		env.EnableRequireAuth(gateway)
		env.Close()
		checkID := submitNonIssuerCheck(t, env, alice, bob, USD(25))

		jtx.RequireTxClaimed(t, env.Submit(checkbuilder.CheckCashAmount(bob, checkID, USD(25)).Build()), jtx.TecPATH_PARTIAL)
		env.Close()
		requireCheckMPTExists(t, env, checkID, true)
		if env.TrustLineExists(bob, gateway, "USD") {
			t.Fatal("RequireAuth failure created a destination trust line")
		}
		jtx.RequireTxSuccess(t, env.Submit(checkbuilder.CheckCancel(bob, checkID).Build()))
		env.Close()
		requireCheckMPTExists(t, env, checkID, false)
	})
}

func newNonIssuerCheckFixture(t *testing.T) (*jtx.TestEnv, *jtx.Account, *jtx.Account, *jtx.Account, func(float64) tx.Amount) {
	t.Helper()
	env := jtx.NewTestEnv(t)
	gateway := jtx.NewAccount("gateway")
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	USD := func(value float64) tx.Amount {
		return tx.NewIssuedAmountFromFloat64(value, "USD", gateway.Address)
	}
	env.FundNoRipple(gateway, alice, bob)
	env.Close()
	jtx.RequireTxSuccess(t, env.Submit(trustset.TrustSet(alice, USD(100)).Build()))
	env.Close()
	jtx.RequireTxSuccess(t, env.Submit(payment.PayIssued(gateway, alice, USD(50)).Build()))
	env.Close()
	return env, gateway, alice, bob, USD
}

func submitNonIssuerCheck(t *testing.T, env *jtx.TestEnv, alice, bob *jtx.Account, amount tx.Amount) string {
	t.Helper()
	checkID := checkbuilder.GetCheckID(alice, env.Seq(alice))
	jtx.RequireTxSuccess(t, env.Submit(checkbuilder.CheckCreate(alice, bob, amount).Build()))
	env.Close()
	requireCheckMPTExists(t, env, checkID, true)
	return checkID
}
