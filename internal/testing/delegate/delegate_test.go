// Package delegate_test contains behaviour tests for delegated transactions and
// the fixDelegateV1_1 amendment. Ported from rippled's Delegate_test.cpp; the
// granular-permission base behaviour is additionally covered by the app/Delegate
// conformance fixtures (rippled 2.6.2, which predates fixDelegateV1_1).
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
// the given permission names and returns the result.
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

// TestDelegate_NonDelegatableTransaction covers granting a non-delegatable
// transaction type. Before fixDelegateV1_1 the check runs in preclaim
// (tecNO_PERMISSION); after the amendment it moves to preflight (temMALFORMED).
// Reference: rippled Delegate_test.cpp testInvalidRequest (non-delegatable case).
func TestDelegate_NonDelegatableTransaction(t *testing.T) {
	nonDelegatable := []string{
		"SetRegularKey", "AccountSet", "SignerListSet", "DelegateSet",
		"EnableAmendment", "UNLModify", "SetFee", "Batch",
	}

	for _, v1_1 := range []bool{false, true} {
		name := "preV1_1"
		want := "tecNO_PERMISSION"
		if v1_1 {
			name = "V1_1"
			want = "temMALFORMED"
		}
		t.Run(name, func(t *testing.T) {
			env := jtx.NewTestEnv(t)
			env.EnableFeature("PermissionDelegation")
			if v1_1 {
				env.EnableFeature("fixDelegateV1_1")
			}
			gw := jtx.NewAccount("gw")
			alice := jtx.NewAccount("alice")
			env.Fund(gw, alice)
			env.Close()

			for _, perm := range nonDelegatable {
				res := grantPermissions(env, gw, alice, perm)
				require.Equalf(t, want, res.Code, "permission %q", perm)
			}
		})
	}
}

// TestDelegate_DelegatableTransactionSucceeds confirms an ordinary delegatable
// transaction permission is accepted under both amendment states.
func TestDelegate_DelegatableTransactionSucceeds(t *testing.T) {
	for _, v1_1 := range []bool{false, true} {
		name := "preV1_1"
		if v1_1 {
			name = "V1_1"
		}
		t.Run(name, func(t *testing.T) {
			env := jtx.NewTestEnv(t)
			env.EnableFeature("PermissionDelegation")
			if v1_1 {
				env.EnableFeature("fixDelegateV1_1")
			}
			gw := jtx.NewAccount("gw")
			alice := jtx.NewAccount("alice")
			env.Fund(gw, alice)
			env.Close()

			require.Equal(t, "tesSUCCESS", grantPermissions(env, gw, alice, "Payment").Code)
			require.Equal(t, "tesSUCCESS", grantPermissions(env, gw, alice, "TrustlineAuthorize").Code)
		})
	}
}

// TestDelegate_PaymentGranularMintBurn covers the base (pre-amendment) granular
// PaymentMint / PaymentBurn behaviour: a mint requires the sender to be the
// issuer, a burn requires the destination to be the issuer, and the wrong
// permission is rejected.
// Reference: rippled Delegate_test.cpp testPaymentGranular.
func TestDelegate_PaymentGranularMintBurn(t *testing.T) {
	env := jtx.NewTestEnv(t)
	env.EnableFeature("PermissionDelegation")
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
	require.Equal(t, "tecNO_DELEGATE_PERMISSION", payDelegated(env, gw, bob, alice, usd50).Code)
	env.Close()

	// PaymentMint granted: gw mints USD to alice on behalf via bob.
	require.Equal(t, "tesSUCCESS", grantPermissions(env, gw, bob, "PaymentMint").Code)
	env.Close()
	require.Equal(t, "tesSUCCESS", payDelegated(env, gw, bob, alice, usd50).Code)
	env.Close()

	// XRP is never a mint or burn.
	require.Equal(t, "tecNO_DELEGATE_PERMISSION",
		payDelegated(env, gw, bob, alice, tx.NewXRPAmount(1_000_000)).Code)
}

// TestDelegate_PaymentBurn covers a burn: the destination (gw) is the issuer and
// the delegate holds PaymentBurn.
func TestDelegate_PaymentBurn(t *testing.T) {
	env := jtx.NewTestEnv(t)
	env.EnableFeature("PermissionDelegation")
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

// TestDelegate_PaymentCrossCurrencyV1_1 covers the fixDelegateV1_1 restriction:
// a delegate holding only PaymentMint/PaymentBurn may not send a cross-currency
// payment (SendMax asset differing from the delivered asset).
// Reference: rippled Delegate_test.cpp testPaymentGranular (cross-currency case).
func TestDelegate_PaymentCrossCurrencyV1_1(t *testing.T) {
	env := jtx.NewTestEnv(t)
	env.EnableFeature("PermissionDelegation")
	env.EnableFeature("fixDelegateV1_1")
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
	require.Equal(t, "tecNO_DELEGATE_PERMISSION", env.SubmitSignedWith(p, bob).Code)
}

// TestDelegate_PaymentMPT covers granular PaymentMint for an MPT: it is rejected
// before fixDelegateV1_1 (tefEXCEPTION — the pre-amendment path cannot inspect an
// MPT issue) and supported once the amendment is enabled.
// Reference: rippled Delegate_test.cpp testPaymentGranular (MPT case).
func TestDelegate_PaymentMPT(t *testing.T) {
	for _, v1_1 := range []bool{false, true} {
		name := "preV1_1"
		if v1_1 {
			name = "V1_1"
		}
		t.Run(name, func(t *testing.T) {
			env := jtx.NewTestEnv(t)
			env.EnableFeature("PermissionDelegation")
			if v1_1 {
				env.EnableFeature("fixDelegateV1_1")
			}
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
			res := env.SubmitSignedWith(mint, bob)

			if v1_1 {
				require.Equal(t, "tesSUCCESS", res.Code)
			} else {
				require.Equal(t, "tefEXCEPTION", res.Code)
			}
		})
	}
}
