// Package pseudotx_test pins SetFee preflight/preclaim against rippled
// Change.cpp:43-385 — the goxrpl pseudo-tx pipeline bypasses the
// engine's common preflight (applyPseudoTransaction calls Apply()
// directly), so the per-tx preflight/preclaim must reject malformed
// fields before the FeeSettings mutation runs.
package pseudotx_test

import (
	"testing"

	jtx "github.com/LeJamon/goXRPLd/internal/testing"
	"github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/internal/tx/pseudo"
	"github.com/stretchr/testify/require"
)

// newSetFeeLegacyEnv builds a TestEnv with XRPFees disabled so the
// legacy fee fields (BaseFee / ReferenceFeeUnits / ReserveBase /
// ReserveIncrement) drive the SetFee preclaim branch.
func newSetFeeLegacyEnv(t *testing.T) *jtx.TestEnv {
	t.Helper()
	env := jtx.NewTestEnv(t)
	env.DisableFeature("XRPFees")
	return env
}

// newSetFeeLegacy returns a fully-populated legacy (pre-XRPFees) SetFee
// pseudo-tx. NewSetFee stamps the canonical zero account plus the
// rippled-default common fields, so callers only need to populate the
// fee fields they want to mutate / leave absent.
func newSetFeeLegacy() *pseudo.SetFee {
	s := pseudo.NewSetFee()
	s.BaseFee = "A" // hex: 10 drops
	ref := uint32(10)
	rb := uint32(200_000_000)
	ri := uint32(50_000_000)
	s.ReferenceFeeUnits = &ref
	s.ReserveBase = &rb
	s.ReserveIncrement = &ri
	return s
}

// newSetFeeModern returns a fully-populated XRPFees-era SetFee.
func newSetFeeModern() *pseudo.SetFee {
	s := pseudo.NewSetFee()
	s.BaseFeeDrops = "10"
	s.ReserveBaseDrops = "200000000"
	s.ReserveIncrementDrops = "50000000"
	return s
}

// Apply-success paths (legacy + modern) intentionally aren't pinned
// here: the FeeSettings typed metadata decoder
// (internal/tx/ledgerfields/fee_settings_gen.go) does not accept the
// blob SerializeFeeSettings emits, so SetFee.Apply()'s happy path
// returns tefINTERNAL during metadata generation regardless of
// preflight/preclaim. The tests below pin only the rejection paths.

// TestSetFee_Preflight_BadAccount pins Change.cpp:43-48 — any non-zero
// source account must reject with temBAD_SRC_ACCOUNT before mutation.
func TestSetFee_Preflight_BadAccount(t *testing.T) {
	env := jtx.NewTestEnv(t)
	s := newSetFeeModern()
	s.Common.Account = "rPT1Sjq2YGrBMTttX4GZHjKu9dyfzbpAYe"
	result := env.SubmitPseudo(s)
	jtx.RequireTxFail(t, result, "temBAD_SRC_ACCOUNT")
}

// TestSetFee_Preflight_BadFee pins Change.cpp:50-56 — Fee != 0 rejects
// with temBAD_FEE.
func TestSetFee_Preflight_BadFee(t *testing.T) {
	env := jtx.NewTestEnv(t)
	s := newSetFeeModern()
	s.Common.Fee = "10"
	result := env.SubmitPseudo(s)
	jtx.RequireTxFail(t, result, "temBAD_FEE")
}

// TestSetFee_Preflight_BadSignature pins Change.cpp:58-63 — any signing
// material (SigningPubKey, TxnSignature, or Signers array) must reject
// with temBAD_SIGNATURE.
func TestSetFee_Preflight_BadSignature(t *testing.T) {
	env := jtx.NewTestEnv(t)
	t.Run("SigningPubKey present", func(t *testing.T) {
		s := newSetFeeModern()
		s.Common.SigningPubKey = "ED5F5AC8B98974A3CA843326D9B88CEBD0560177B973EE0B149F782CFAA06DC66A"
		result := env.SubmitPseudo(s)
		jtx.RequireTxFail(t, result, "temBAD_SIGNATURE")
	})
	t.Run("TxnSignature present", func(t *testing.T) {
		s := newSetFeeModern()
		s.Common.TxnSignature = "AB"
		result := env.SubmitPseudo(s)
		jtx.RequireTxFail(t, result, "temBAD_SIGNATURE")
	})
}

// TestSetFee_Preflight_BadSequence pins Change.cpp:65-70 — Sequence
// must be zero.
func TestSetFee_Preflight_BadSequence(t *testing.T) {
	env := jtx.NewTestEnv(t)
	s := newSetFeeModern()
	seq := uint32(7)
	s.Common.Sequence = &seq
	result := env.SubmitPseudo(s)
	jtx.RequireTxFail(t, result, "temBAD_SEQUENCE")
}

// TestSetFee_Preclaim_ModernMissingFields pins Change.cpp:101-104 —
// with XRPFees active, the *Drops triple is REQUIRED.
func TestSetFee_Preclaim_ModernMissingFields(t *testing.T) {
	env := jtx.NewTestEnv(t)
	s := pseudo.NewSetFee()
	s.BaseFeeDrops = "10"
	// Intentionally leave ReserveBaseDrops, ReserveIncrementDrops absent.
	result := env.SubmitPseudo(s)
	jtx.RequireTxFail(t, result, "temMALFORMED")
}

// TestSetFee_Preclaim_ModernForbidsLegacy pins Change.cpp:108-112 —
// with XRPFees active, the legacy quartet is forbidden.
func TestSetFee_Preclaim_ModernForbidsLegacy(t *testing.T) {
	env := jtx.NewTestEnv(t)
	s := newSetFeeModern()
	s.BaseFee = "A" // re-introduces a legacy field
	result := env.SubmitPseudo(s)
	jtx.RequireTxFail(t, result, "temMALFORMED")
}

// TestSetFee_Preclaim_LegacyMissingFields pins Change.cpp:114-124 —
// when XRPFees is OFF the legacy quartet is REQUIRED.
func TestSetFee_Preclaim_LegacyMissingFields(t *testing.T) {
	env := newSetFeeLegacyEnv(t)
	s := pseudo.NewSetFee()
	s.BaseFee = "A"
	// Intentionally leave ReferenceFeeUnits, ReserveBase, ReserveIncrement absent.
	result := env.SubmitPseudo(s)
	jtx.RequireTxFail(t, result, "temMALFORMED")
}

// TestSetFee_Preclaim_LegacyForbidsModern pins Change.cpp:128-131 —
// without XRPFees, the modern *Drops fields must reject with
// temDISABLED.
func TestSetFee_Preclaim_LegacyForbidsModern(t *testing.T) {
	env := newSetFeeLegacyEnv(t)
	s := newSetFeeLegacy()
	s.BaseFeeDrops = "10"
	result := env.SubmitPseudo(s)
	jtx.RequireTxFail(t, result, "temDISABLED")
}

// TestSetFee_Validate_NoFieldsAtAll pins that a pseudo-tx with no fee
// fields at all fails Apply()'s preclaim with temMALFORMED — the
// modern branch requires the *Drops triple.
func TestSetFee_Validate_NoFieldsAtAll(t *testing.T) {
	env := jtx.NewTestEnv(t)
	s := pseudo.NewSetFee()
	result := env.SubmitPseudo(s)
	jtx.RequireTxFail(t, result, "temMALFORMED")
	// Sanity: Validate() short-circuits a totally-empty SetFee.
	require.Error(t, (&pseudo.SetFee{BaseTx: *tx.NewBaseTx(tx.TypeFee, pseudo.ZeroAccount)}).Validate())
}
