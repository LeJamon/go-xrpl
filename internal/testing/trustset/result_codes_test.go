package trustset

import (
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/payment"
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

	result := env.Submit(TrustLine(alice, "USD", gw, "0").Build())
	jtx.RequireTxClaimed(t, result, "tecNO_LINE_REDUNDANT")
}

// TestTrustSet_NoLineRedundant_QualityOneNotRedundant verifies that a
// zero-limit TrustSet on a non-existent line carrying QualityIn == QUALITY_ONE
// is NOT redundant: rippled leaves QualityIn unnormalized (only QualityOut is
// folded to zero), so the line is created rather than rejected.
//
// Reference: rippled SetTrust.cpp lines 409-414 (only uQualityOut normalized)
// and 698-708 (redundancy test reads raw uQualityIn).
func TestTrustSet_NoLineRedundant_QualityOneNotRedundant(t *testing.T) {
	env := jtx.NewTestEnv(t)
	gw := jtx.NewAccount("gw")
	alice := jtx.NewAccount("alice")
	env.Fund(gw, alice)
	env.Close()

	result := env.Submit(TrustLine(alice, "USD", gw, "0").QualityIn(QualityParity).Build())
	jtx.RequireTxSuccess(t, result)
	if !env.TrustLineExists(alice, gw, "USD") {
		t.Fatal("expected trust line to be created, not reported redundant")
	}
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

			jtx.RequireTxSuccess(t, env.Submit(TrustLine(bob, "USD", alice, "10000").Build()))
			env.Close()

			// alice issues 100 USD to bob. From alice's perspective the line
			// balance is now negative.
			jtx.RequireTxSuccess(t, env.Submit(payment.PayIssued(alice, bob, alice.IOU("USD", 100)).Build()))
			env.Close()

			result := env.Submit(TrustLine(alice, "USD", bob, "1000").NoRipple().Build())
			if withFix {
				jtx.RequireTxClaimed(t, result, "tecNO_PERMISSION")
			} else {
				jtx.RequireTxSuccess(t, result)
			}
		})
	}
}
