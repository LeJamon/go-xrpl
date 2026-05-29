package trustset

import (
	"testing"

	jtx "github.com/LeJamon/goXRPLd/internal/testing"
	"github.com/LeJamon/goXRPLd/internal/testing/payment"
)

// TestTrustSet_NoLineRedundant verifies that setting a non-existent trust line
// to its default state (zero limit, no auth, no quality) is reported as
// redundant rather than a silent success.
//
// Reference: rippled SetTrust.cpp lines 698-708 returns tecNO_LINE_REDUNDANT
// for the identical condition.
func TestTrustSet_NoLineRedundant(t *testing.T) {
	env := jtx.NewTestEnv(t)
	gw := jtx.NewAccount("gw")
	alice := jtx.NewAccount("alice")
	env.Fund(gw, alice)
	env.Close()

	// No trust line exists; a zero-limit, no-flag TrustSet is redundant.
	result := env.Submit(TrustLine(alice, "USD", gw, "0").Build())
	jtx.RequireTxClaimed(t, result, "tecNO_LINE_REDUNDANT")
}

// TestTrustSet_NoRippleNegativeBalance verifies the fix1578 behavior: NoRipple
// cannot be set on a trust line whose balance is negative from the sender's
// perspective. With fix1578 enabled the transaction is rejected with
// tecNO_PERMISSION; without it the flag is silently skipped and the tx succeeds.
//
// Reference: rippled SetTrust.cpp lines 577-585.
func TestTrustSet_NoRippleNegativeBalance(t *testing.T) {
	for _, withFix := range []bool{true, false} {
		name := "WithFix1578"
		if !withFix {
			name = "WithoutFix1578"
		}
		t.Run(name, func(t *testing.T) {
			env := jtx.NewTestEnv(t)
			if !withFix {
				env.DisableFeature("fix1578")
			}

			alice := jtx.NewAccount("alice")
			bob := jtx.NewAccount("bob")
			env.Fund(alice, bob)
			env.Close()

			// bob trusts alice's USD, creating the alice<->bob line.
			jtx.RequireTxSuccess(t, env.Submit(TrustLine(bob, "USD", alice, "10000").Build()))
			env.Close()

			// alice issues 100 USD to bob. From alice's perspective the line
			// balance is now negative.
			jtx.RequireTxSuccess(t, env.Submit(payment.PayIssued(alice, bob, alice.IOU("USD", 100)).Build()))
			env.Close()

			// alice re-asserts the line with tfSetNoRipple while holding a
			// negative balance.
			result := env.Submit(TrustLine(alice, "USD", bob, "1000").NoRipple().Build())
			if withFix {
				jtx.RequireTxClaimed(t, result, "tecNO_PERMISSION")
			} else {
				jtx.RequireTxSuccess(t, result)
			}
		})
	}
}
