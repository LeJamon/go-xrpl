// Package delegate_test contains behaviour tests for delegated transactions and
// the PermissionDelegationV1_1 amendment. Ported from rippled's Delegate_test.cpp;
// the granular-permission base behaviour is additionally covered by the
// app/Delegate conformance fixtures (rippled 2.6.2, which predates the amendment).
//
// PermissionDelegationV1_1 replaces the removed PermissionDelegation +
// fixDelegateV1_1 amendments, folding the delegatability restrictions in
// unconditionally. Delegation-permission denials return the retriable
// terNO_DELEGATE_PERMISSION (no fee claimed).
package delegate_test

import (
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	mpttester "github.com/LeJamon/go-xrpl/internal/testing/mpt"
	"github.com/LeJamon/go-xrpl/internal/tx"
	delegatetx "github.com/LeJamon/go-xrpl/internal/tx/delegate"
	paymenttx "github.com/LeJamon/go-xrpl/internal/tx/payment"
	"github.com/stretchr/testify/require"
)

// grantPermissions submits a DelegateSet from owner authorizing delegate with
// the given permission names and returns the result. With no permission names
// it submits an empty Permissions array (a delete/no-op request).
func grantPermissions(env *jtx.TestEnv, owner, delegate *jtx.Account, perms ...string) jtx.TxResult {
	ds := delegatetx.NewDelegateSet(owner.Address)
	ds.Authorize = delegate.Address
	for _, p := range perms {
		ds.Permissions = append(ds.Permissions, delegatetx.Permission{
			Permission: delegatetx.PermissionData{PermissionValue: p},
		})
	}
	return env.SubmitSignedWith(ds, owner)
}

// payDelegated submits a Payment where delegate acts on behalf of owner.
func payDelegated(env *jtx.TestEnv, owner, delegate, dest *jtx.Account, amount tx.Amount) jtx.TxResult {
	p := paymenttx.NewPayment(owner.Address, dest.Address, amount)
	p.GetCommon().Delegate = delegate.Address
	return env.SubmitSignedWith(p, delegate)
}

// TestDelegate_FeatureDisabled confirms a DelegateSet is rejected with
// temDISABLED until PermissionDelegationV1_1 is enabled.
// Reference: rippled Delegate_test.cpp testFeatureDisabled.
func TestDelegate_FeatureDisabled(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		name := "disabled"
		want := "temDISABLED"
		if enabled {
			name = "enabled"
			want = "tesSUCCESS"
		}
		t.Run(name, func(t *testing.T) {
			env := jtx.NewTestEnv(t)
			if enabled {
				env.EnableFeature("PermissionDelegationV1_1")
			}
			gw := jtx.NewAccount("gw")
			alice := jtx.NewAccount("alice")
			env.Fund(gw, alice)
			env.Close()

			require.Equal(t, want, grantPermissions(env, gw, alice, "Payment").Code)
		})
	}
}

// TestDelegate_NonDelegatableTransaction covers granting a non-delegatable
// transaction type: the delegatability check runs in preflight and rejects with
// temMALFORMED.
// Reference: rippled Delegate_test.cpp testInvalidRequest (non-delegatable case).
func TestDelegate_NonDelegatableTransaction(t *testing.T) {
	env := jtx.NewTestEnv(t)
	env.EnableFeature("PermissionDelegationV1_1")
	gw := jtx.NewAccount("gw")
	alice := jtx.NewAccount("alice")
	env.Fund(gw, alice)
	env.Close()

	nonDelegatable := []string{
		"SetRegularKey", "AccountSet", "SignerListSet", "DelegateSet",
		"EnableAmendment", "UNLModify", "SetFee", "Batch",
	}
	for _, perm := range nonDelegatable {
		res := grantPermissions(env, gw, alice, perm)
		require.Equalf(t, "temMALFORMED", res.Code, "permission %q", perm)
	}
}

// TestDelegate_VaultNotDelegatable covers the 3.2.0 change making the Single
// Asset Vault transactions non-delegatable. SingleAssetVault is enabled so the
// rejection is the NotDelegable rule, not the missing-amendment guard.
// Reference: rippled #6489 (transactions.macro Delegation::NotDelegable).
func TestDelegate_VaultNotDelegatable(t *testing.T) {
	env := jtx.NewTestEnv(t)
	env.EnableFeature("PermissionDelegationV1_1")
	env.EnableFeature("SingleAssetVault")
	gw := jtx.NewAccount("gw")
	alice := jtx.NewAccount("alice")
	env.Fund(gw, alice)
	env.Close()

	vaultPerms := []string{
		"VaultCreate", "VaultSet", "VaultDelete",
		"VaultDeposit", "VaultWithdraw", "VaultClawback",
	}
	for _, perm := range vaultPerms {
		res := grantPermissions(env, gw, alice, perm)
		require.Equalf(t, "temMALFORMED", res.Code, "permission %q", perm)
	}
}

// TestDelegate_DelegatableTransactionSucceeds confirms an ordinary delegatable
// transaction permission is accepted.
func TestDelegate_DelegatableTransactionSucceeds(t *testing.T) {
	env := jtx.NewTestEnv(t)
	env.EnableFeature("PermissionDelegationV1_1")
	gw := jtx.NewAccount("gw")
	alice := jtx.NewAccount("alice")
	env.Fund(gw, alice)
	env.Close()

	require.Equal(t, "tesSUCCESS", grantPermissions(env, gw, alice, "Payment").Code)
	require.Equal(t, "tesSUCCESS", grantPermissions(env, gw, alice, "TrustlineAuthorize").Code)
}

// TestDelegate_EmptyPermissionsNoObject covers the 3.2.0 rule (#6542): a
// DelegateSet with an empty Permissions array is invalid when no Delegate object
// exists — previously it created a useless empty entry.
func TestDelegate_EmptyPermissionsNoObject(t *testing.T) {
	env := jtx.NewTestEnv(t)
	env.EnableFeature("PermissionDelegationV1_1")
	gw := jtx.NewAccount("gw")
	alice := jtx.NewAccount("alice")
	env.Fund(gw, alice)
	env.Close()

	require.Equal(t, "tecNO_ENTRY", grantPermissions(env, gw, alice).Code)
}

// TestDelegate_CreateThenDelete exercises the full create/delete round trip: the
// Delegate object is linked into both accounts' owner directories on create
// (OwnerNode + DestinationNode) and removed from both when an empty permission
// list deletes it.
// Reference: rippled #6681.
func TestDelegate_CreateThenDelete(t *testing.T) {
	env := jtx.NewTestEnv(t)
	env.EnableFeature("PermissionDelegationV1_1")
	gw := jtx.NewAccount("gw")
	alice := jtx.NewAccount("alice")
	env.Fund(gw, alice)
	env.Close()

	require.Equal(t, "tesSUCCESS", grantPermissions(env, gw, alice, "Payment").Code)
	env.Close()

	// An empty permission list deletes the existing Delegate object.
	require.Equal(t, "tesSUCCESS", grantPermissions(env, gw, alice).Code)
}

// TestDelegate_PaymentGranularMintBurn covers the granular PaymentMint /
// PaymentBurn behaviour: a mint requires the sender to be the issuer, a burn
// requires the destination to be the issuer, and the wrong permission is
// rejected with terNO_DELEGATE_PERMISSION.
// Reference: rippled Delegate_test.cpp testPaymentGranular.
func TestDelegate_PaymentGranularMintBurn(t *testing.T) {
	env := jtx.NewTestEnv(t)
	env.EnableFeature("PermissionDelegationV1_1")
	gw := jtx.NewAccount("gw")
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	env.Fund(gw, alice, bob)
	env.Close()

	// alice trusts gw for USD so gw can mint to her.
	env.Trust(alice, tx.NewIssuedAmountFromFloat64(1000, "USD", gw.Address))
	env.Close()

	usd50 := tx.NewIssuedAmountFromFloat64(50, "USD", gw.Address)

	// Only PaymentBurn granted: a mint (gw is the issuer/sender) is rejected.
	require.Equal(t, "tesSUCCESS", grantPermissions(env, gw, bob, "PaymentBurn").Code)
	env.Close()
	require.Equal(t, "terNO_DELEGATE_PERMISSION", payDelegated(env, gw, bob, alice, usd50).Code)
	env.Close()

	// PaymentMint granted: gw mints USD to alice on behalf via bob.
	require.Equal(t, "tesSUCCESS", grantPermissions(env, gw, bob, "PaymentMint").Code)
	env.Close()
	require.Equal(t, "tesSUCCESS", payDelegated(env, gw, bob, alice, usd50).Code)
	env.Close()

	// XRP is never a mint or burn.
	require.Equal(t, "terNO_DELEGATE_PERMISSION",
		payDelegated(env, gw, bob, alice, tx.NewXRPAmount(1_000_000)).Code)
}

// TestDelegate_PaymentBurn covers a burn: the destination (gw) is the issuer and
// the delegate holds PaymentBurn.
func TestDelegate_PaymentBurn(t *testing.T) {
	env := jtx.NewTestEnv(t)
	env.EnableFeature("PermissionDelegationV1_1")
	gw := jtx.NewAccount("gw")
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	env.Fund(gw, alice, bob)
	env.Close()

	env.Trust(alice, tx.NewIssuedAmountFromFloat64(1000, "USD", gw.Address))
	env.Close()
	env.PayIOU(gw, alice, gw, "USD", 100)
	env.Close()

	// alice delegates PaymentBurn to bob; bob sends alice's USD back to the
	// issuer gw (a burn).
	require.Equal(t, "tesSUCCESS", grantPermissions(env, alice, bob, "PaymentBurn").Code)
	env.Close()
	require.Equal(t, "tesSUCCESS",
		payDelegated(env, alice, bob, gw, tx.NewIssuedAmountFromFloat64(30, "USD", gw.Address)).Code)
}

// TestDelegate_PaymentCrossCurrency covers the restriction that a delegate
// holding only PaymentMint/PaymentBurn may not send a cross-currency payment
// (SendMax asset differing from the delivered asset).
// Reference: rippled Delegate_test.cpp testPaymentGranular (cross-currency case).
func TestDelegate_PaymentCrossCurrency(t *testing.T) {
	env := jtx.NewTestEnv(t)
	env.EnableFeature("PermissionDelegationV1_1")
	gw := jtx.NewAccount("gw")
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	env.Fund(gw, alice, bob)
	env.Close()

	require.Equal(t, "tesSUCCESS", grantPermissions(env, gw, bob, "PaymentMint").Code)
	env.Close()

	// Deliver USD but source EUR: cross-currency is forbidden under the granular
	// mint permission.
	p := paymenttx.NewPayment(gw.Address, alice.Address, tx.NewIssuedAmountFromFloat64(50, "USD", gw.Address))
	sendMax := tx.NewIssuedAmountFromFloat64(50, "EUR", gw.Address)
	p.SendMax = &sendMax
	p.GetCommon().Delegate = bob.Address
	require.Equal(t, "terNO_DELEGATE_PERMISSION", env.SubmitSignedWith(p, bob).Code)
}

// TestDelegate_PaymentMPT covers granular PaymentMint for an MPT: the mint/burn
// checks extend to MPT amounts.
// Reference: rippled Delegate_test.cpp testPaymentGranular (MPT case).
func TestDelegate_PaymentMPT(t *testing.T) {
	env := jtx.NewTestEnv(t)
	env.EnableFeature("PermissionDelegationV1_1")
	gw := jtx.NewAccount("gw")
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	m := mpttester.NewMPTTester(t, env, gw, mpttester.MPTInit{Holders: []*jtx.Account{alice, bob}})
	env.Close()

	m.Create(mpttester.CreateOpts{Flags: mpttester.TfMPTCanTransfer})
	env.Close()
	m.Authorize(mpttester.AuthorizeOpts{Account: alice})
	env.Close()

	require.Equal(t, "tesSUCCESS", grantPermissions(env, gw, bob, "PaymentMint").Code)
	env.Close()

	// gw mints MPT to alice on behalf via bob.
	mint := paymenttx.NewPayment(gw.Address, alice.Address, m.MPTAmount(50))
	mint.MPTokenIssuanceID = m.IssuanceID()
	mint.GetCommon().Delegate = bob.Address
	require.Equal(t, "tesSUCCESS", env.SubmitSignedWith(mint, bob).Code)
}

// TestDelegate_LendingNotDelegatable covers the 3.2.0 change making the
// Lending transactions non-delegatable. LendingProtocol is enabled so the
// rejection is the NotDelegable rule, not the missing-amendment guard.
func TestDelegate_LendingNotDelegatable(t *testing.T) {
	env := jtx.NewTestEnv(t)
	env.EnableFeature("PermissionDelegationV1_1")
	env.EnableFeature("LendingProtocol")
	gw := jtx.NewAccount("gw")
	alice := jtx.NewAccount("alice")
	env.Fund(gw, alice)
	env.Close()

	loanPerms := []string{
		"LoanBrokerSet", "LoanBrokerDelete", "LoanBrokerCoverDeposit",
		"LoanBrokerCoverWithdraw", "LoanBrokerCoverClawback",
		"LoanSet", "LoanDelete", "LoanManage", "LoanPay",
	}
	for _, perm := range loanPerms {
		res := grantPermissions(env, gw, alice, perm)
		require.Equalf(t, "temMALFORMED", res.Code, "permission %q", perm)
	}
}
