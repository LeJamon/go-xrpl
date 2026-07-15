package trustset

import (
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/tx"
)

// TestTrustSet_Precedence_DeepFreezeDisabledIsPreflightReject pins finding #2:
// without featureDeepFreeze, a deep-freeze flag is rejected temINVALID_FLAG at
// preflight — before any ledger-stage tec/tef. Previously this rejection lived
// in Apply(), after the tecNO_DST/tecNO_PERMISSION/tefNO_AUTH_REQUIRED checks, so
// a deep-freeze flag combined with e.g. tfSetfAuth surfaced tefNO_AUTH_REQUIRED
// (or a tec) instead — a tem-vs-tec/tef consensus fork.
// Reference: rippled SetTrust.cpp preflight() featureDeepFreeze gate (first check).
func TestTrustSet_Precedence_DeepFreezeDisabledIsPreflightReject(t *testing.T) {
	env := jtx.NewTestEnv(t)
	env.DisableFeature("DeepFreeze")
	gw := jtx.NewAccount("gw")
	alice := jtx.NewAccount("alice")
	env.Fund(gw, alice)
	env.Close()

	// Plain deep-freeze flag on a well-formed limit → temINVALID_FLAG.
	jtx.RequireTxFail(t, env.Submit(TrustLine(alice, "USD", gw, "100").DeepFreeze().Build()),
		"temINVALID_FLAG")

	// Combined with tfSetfAuth (no lsfRequireAuth): the preflight flag rejection
	// still wins over the Apply-stage tefNO_AUTH_REQUIRED.
	jtx.RequireTxFail(t, env.Submit(TrustLine(alice, "USD", gw, "100").SetAuth().DeepFreeze().Build()),
		"temINVALID_FLAG")
}

// TestTrustSet_Precedence_NativeLimitTooLarge pins finding #5: a native
// LimitAmount whose magnitude exceeds the total XRP supply (cMaxNativeN, 1e17
// drops) is temBAD_AMOUNT (isLegalNet), not temBAD_LIMIT.
// Reference: rippled SetTrust.cpp preflight() !isLegalNet check ahead of native.
func TestTrustSet_Precedence_NativeLimitTooLarge(t *testing.T) {
	env := jtx.NewTestEnv(t)
	gw := jtx.NewAccount("gw")
	alice := jtx.NewAccount("alice")
	env.Fund(gw, alice)
	env.Close()

	ts := TrustSet(alice, tx.NewXRPAmount(200_000_000_000_000_000)).Build()
	jtx.RequireTxFail(t, env.Submit(ts), "temBAD_AMOUNT")
}

// TestTrustSet_Precedence_SelfTrustAuthOrdering pins finding #3: temDST_IS_SRC is
// a ledger-stage preclaim check, evaluated AFTER the tfSetfAuth check. A self
// trust line carrying tfSetfAuth without lsfRequireAuth therefore surfaces
// tefNO_AUTH_REQUIRED, not temDST_IS_SRC; a plain self trust line still gets
// temDST_IS_SRC (fixTrustLinesToSelf is enabled in the test env).
// Reference: rippled SetTrust.cpp preclaim() order (tefNO_AUTH_REQUIRED then temDST_IS_SRC).
func TestTrustSet_Precedence_SelfTrustAuthOrdering(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	env.Fund(alice)
	env.Close()

	// Self trust + tfSetfAuth, sender lacks lsfRequireAuth → tefNO_AUTH_REQUIRED.
	jtx.RequireTxFail(t, env.Submit(TrustLine(alice, "USD", alice, "100").SetAuth().Build()),
		"tefNO_AUTH_REQUIRED")

	// Plain self trust → temDST_IS_SRC at the ledger stage.
	jtx.RequireTxFail(t, env.Submit(TrustLine(alice, "USD", alice, "100").Build()),
		"temDST_IS_SRC")
}

// TestTrustSet_Precedence_AuthBeforeNoDst pins finding #4: the tfSetfAuth check
// (tefNO_AUTH_REQUIRED) precedes the destination-existence check (tecNO_DST). A
// tfSetfAuth TrustSet without lsfRequireAuth, naming a non-existent issuer on a
// ledger with DisallowIncoming/AMM enabled, surfaces tefNO_AUTH_REQUIRED — not
// tecNO_DST (a tef-vs-tec consensus fork before the fix).
// Reference: rippled SetTrust.cpp preclaim() order (tefNO_AUTH_REQUIRED then tecNO_DST).
func TestTrustSet_Precedence_AuthBeforeNoDst(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	ghost := jtx.NewAccount("ghost") // never funded → does not exist in the ledger
	env.Fund(alice)
	env.Close()

	jtx.RequireTxFail(t, env.Submit(TrustLine(alice, "USD", ghost, "100").SetAuth().Build()),
		"tefNO_AUTH_REQUIRED")
}
