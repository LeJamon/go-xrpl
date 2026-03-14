// Package setauth_test tests TrustSet with tfSetfAuth authorization behavior.
// Reference: rippled/src/test/app/SetAuth_test.cpp
package setauth_test

import (
	"testing"

	jtx "github.com/LeJamon/goXRPLd/internal/testing"
	"github.com/LeJamon/goXRPLd/internal/testing/payment"
	"github.com/LeJamon/goXRPLd/internal/testing/trustset"
)

// TestSetAuth tests that authorization works for trust lines when RequireAuth
// is set on the issuer.
// Reference: rippled SetAuth_test.cpp testAuth()
//
// Flow:
// 1. Gateway sets asfRequireAuth
// 2. Gateway authorizes alice via TrustSet with tfSetfAuth
// 3. Alice and Bob create trust lines
// 4. Gateway can pay alice (authorized) but NOT bob (unauthorized)
// 5. Alice cannot pay bob (bob is unauthorized by gateway)
func TestSetAuth(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	gw := jtx.NewAccount("gw")
	env.Fund(alice, bob, gw)
	env.Close()

	// Gateway enables RequireAuth
	env.EnableRequireAuth(gw)
	env.Close()

	// Gateway authorizes alice for USD
	env.AuthorizeTrustLine(gw, alice, "USD")
	env.Close()

	// Verify the trust line exists between alice and gw
	jtx.RequireTrustLineExists(t, env, alice, gw, "USD")

	// Alice and Bob create trust lines to gateway
	result := env.Submit(trustset.TrustUSD(alice, gw, "1000").Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	result = env.Submit(trustset.TrustUSD(bob, gw, "1000").Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// Gateway pays alice 100 USD → should succeed (alice is authorized)
	result = env.Submit(
		payment.PayIssued(gw, alice, gw.IOU("USD", 100)).Build(),
	)
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// Gateway pays bob 100 USD → should fail (bob is NOT authorized)
	// Reference: rippled SetAuth_test.cpp:67-68 → tecPATH_DRY
	result = env.Submit(
		payment.PayIssued(gw, bob, gw.IOU("USD", 100)).Build(),
	)
	jtx.RequireTxClaimed(t, result, "tecPATH_DRY")

	// Alice pays bob 50 USD → should fail (bob's trust line is not authorized by gw)
	// Reference: rippled SetAuth_test.cpp:69-70 → tecPATH_DRY
	result = env.Submit(
		payment.PayIssued(alice, bob, gw.IOU("USD", 50)).Build(),
	)
	jtx.RequireTxClaimed(t, result, "tecPATH_DRY")
}
