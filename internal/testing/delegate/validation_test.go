package delegate_test

import (
	"strconv"
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	delegatetx "github.com/LeJamon/go-xrpl/internal/tx/delegate"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

func TestDelegateSet_PermissionCountBoundary(t *testing.T) {
	permissions := []string{
		"Payment", "EscrowCreate", "EscrowFinish", "EscrowCancel", "OfferCreate",
		"OfferCancel", "TicketCreate", "PaymentChannelCreate", "PaymentChannelFund",
		"PaymentChannelClaim", "CheckCreate",
	}

	for _, count := range []int{10, 11} {
		t.Run(strconv.Itoa(count), func(t *testing.T) {
			ds := delegatetx.NewDelegateSet(jtx.NewAccount("alice").Address)
			ds.Authorize = jtx.NewAccount("bob").Address
			for _, permission := range permissions[:count] {
				ds.Permissions = append(ds.Permissions, delegatetx.NewPermission(permission))
			}

			err := ds.Validate()
			if count == 10 {
				require.NoError(t, err)
				return
			}
			resultErr, ok := ter.AsResultError(err)
			require.True(t, ok)
			require.Equal(t, ter.TemARRAY_TOO_LARGE, resultErr.Code)
		})
	}
}

func TestDelegateSet_AuthorizeValidation(t *testing.T) {
	env := jtx.NewTestEnv(t)
	env.EnableFeature("PermissionDelegationV1_1")
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	env.Fund(alice)
	env.Close()

	require.Equal(t, "temMALFORMED", grantPermissions(env, alice, alice, "Payment").Code)
	require.Equal(t, "tecNO_TARGET", grantPermissions(env, alice, bob, "Payment").Code)
}

func TestDelegateSet_ReserveFailureDoesNotCreateObject(t *testing.T) {
	env := jtx.NewTestEnv(t)
	env.EnableFeature("PermissionDelegationV1_1")
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	env.FundAmountNoRipple(alice, env.ReserveBase()+env.ReserveIncrement()-1)
	env.Fund(bob)
	env.Close()

	delegateKey := keylet.Delegate(alice.ID, bob.ID)
	result := grantPermissions(env, alice, bob, "Payment")
	jtx.RequireTxFail(t, result, jtx.TecINSUFFICIENT_RESERVE)
	require.False(t, env.LedgerEntryExists(delegateKey))
	require.Zero(t, env.OwnerCount(alice))

	env.Pay(alice, env.BaseFee()+1)
	env.Close()
	jtx.RequireTxSuccess(t, grantPermissions(env, alice, bob, "Payment"))
	require.True(t, env.LedgerEntryExists(delegateKey))
}

func TestDelegateSet_PermissionIntroducingAmendment(t *testing.T) {
	env := jtx.NewTestEnv(t)
	env.EnableFeature("PermissionDelegationV1_1")
	env.DisableFeature("Clawback")
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	env.Fund(alice, bob)
	env.Close()

	require.Equal(t, "temMALFORMED", grantPermissions(env, alice, bob, "Clawback").Code)
	env.EnableFeature("Clawback")
	env.Close()
	require.Equal(t, "tesSUCCESS", grantPermissions(env, alice, bob, "Clawback").Code)

	env.DisableFeature("MPTokensV1")
	env.Close()
	require.Equal(t, "temMALFORMED", grantPermissions(env, alice, bob, "MPTokenIssuanceLock").Code)
	env.EnableFeature("MPTokensV1")
	env.Close()
	require.Equal(t, "tesSUCCESS", grantPermissions(env, alice, bob, "MPTokenIssuanceLock").Code)
}
