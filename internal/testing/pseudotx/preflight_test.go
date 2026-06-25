// Tests for the engine-level pseudo-transaction preflight / preclaim gates
// that mirror rippled Change::preflight and Change::preclaim
// (rippled/src/xrpld/app/tx/detail/Change.cpp:36-140).
package pseudotx_test

import (
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	txengine "github.com/LeJamon/go-xrpl/internal/tx/engine"

	"github.com/stretchr/testify/require"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/pseudo"
	"github.com/LeJamon/go-xrpl/protocol"
)

// closedEngine builds an engine pinned to a closed-ledger view, which is the
// only configuration under which ApplyPseudo legally executes per rippled
// Change::preclaim.
func closedEngine(t *testing.T, rules *amendment.Rules) (*txengine.Engine, *jtx.TestEnv) {
	t.Helper()
	env := jtx.NewTestEnv(t)
	cfg := tx.EngineConfig{
		BaseFee:                   10,
		ReserveBase:               200_000_000,
		ReserveIncrement:          50_000_000,
		LedgerSequence:            env.LedgerSeq(),
		SkipSignatureVerification: true,
		OpenLedger:                false,
		Rules:                     rules,
	}
	return txengine.NewEngine(env.Ledger(), cfg), env
}

func newAmendmentTx() *pseudo.EnableAmendment {
	t := &pseudo.EnableAmendment{
		BaseTx: *tx.NewBaseTx(tx.TypeAmendment, protocol.ZeroAccount),
	}
	t.Amendment = "00000000000000000000000000000000000000000000000000000000000000AA"
	t.Common.Fee = "0"
	zero := uint32(0)
	t.Common.Sequence = &zero
	return t
}

func newLegacySetFeeTx() *pseudo.SetFee {
	t := pseudo.NewSetFee()
	t.Common.Fee = "0"
	zero := uint32(0)
	t.Common.Sequence = &zero
	t.BaseFee = "0A" // 10 drops as hex (sfBaseFee is STUInt64 in JSON form)
	rfu := uint32(10)
	t.ReferenceFeeUnits = &rfu
	rb := uint32(200_000_000)
	t.ReserveBase = &rb
	ri := uint32(50_000_000)
	t.ReserveIncrement = &ri
	return t
}

// TestPseudoPreflight_AccountMustBeZero rejects a pseudo-tx whose Account is
// anything other than the canonical zero address.
// Reference: rippled Change.cpp:43-48.
func TestPseudoPreflight_AccountMustBeZero(t *testing.T) {
	engine, _ := closedEngine(t, amendment.AllSupportedRules())
	tx := newAmendmentTx()
	tx.Common.Account = "rEhxGqkqPPSxQ3P25J66ft5TwpzV14k2de" // arbitrary non-zero account
	result := engine.ApplyPseudo(tx)
	require.False(t, result.Applied)
	require.Equal(t, "temBAD_SRC_ACCOUNT", result.Result.String())
}

// TestPseudoPreflight_AcceptsEmptyAccount confirms an empty Account passes the
// account gate. A replayed on-ledger UNL_MODIFY parses to an empty Account
// because its default-valued sfAccount serializes as a zero-length blob; rippled
// reads it as getAccountID(sfAccount) == beast::zero, identical to the canonical
// zero address, so it must not be rejected.
// Reference: Change.cpp:43-48 (account != beast::zero passes for absent/zero).
func TestPseudoPreflight_AcceptsEmptyAccount(t *testing.T) {
	engine, _ := closedEngine(t, amendment.AllSupportedRules())
	tx := newAmendmentTx()
	tx.Common.Account = ""
	result := engine.ApplyPseudo(tx)
	require.NotEqual(t, "temBAD_SRC_ACCOUNT", result.Result.String(),
		"empty Account (absent sfAccount → zero) must pass the preflight account gate")
}

// TestPseudoPreflight_FeeMustBeZero rejects a pseudo-tx with a non-zero fee.
// Reference: rippled Change.cpp:50-56.
func TestPseudoPreflight_FeeMustBeZero(t *testing.T) {
	engine, _ := closedEngine(t, amendment.AllSupportedRules())
	tx := newAmendmentTx()
	tx.Common.Fee = "10"
	result := engine.ApplyPseudo(tx)
	require.False(t, result.Applied)
	require.Equal(t, "temBAD_FEE", result.Result.String())
}

// TestPseudoPreflight_FeeZeroSpellings accepts every decimal-string spelling
// that decodes to zero drops, matching rippled's typed beast::zero compare.
func TestPseudoPreflight_FeeZeroSpellings(t *testing.T) {
	for _, fee := range []string{"", "0", "00", "000", " 0 ", "\t0\t"} {
		t.Run("Fee="+fee, func(t *testing.T) {
			engine, _ := closedEngine(t, amendment.AllSupportedRules())
			tx := newAmendmentTx()
			tx.Common.Fee = fee
			result := engine.ApplyPseudo(tx)
			require.NotEqual(t, "temBAD_FEE", result.Result.String(),
				"Fee=%q must pass the zero-fee gate", fee)
		})
	}
}

// TestPseudoPreflight_NoSigningPubKey rejects a pseudo-tx with a signing key.
// Reference: rippled Change.cpp:58-63.
func TestPseudoPreflight_NoSigningPubKey(t *testing.T) {
	engine, _ := closedEngine(t, amendment.AllSupportedRules())
	tx := newAmendmentTx()
	tx.Common.SigningPubKey = "ED00000000000000000000000000000000000000000000000000000000000000AA"
	result := engine.ApplyPseudo(tx)
	require.False(t, result.Applied)
	require.Equal(t, "temBAD_SIGNATURE", result.Result.String())
}

// TestPseudoPreflight_NoTxnSignature rejects a pseudo-tx carrying TxnSignature.
// Reference: rippled Change.cpp:58-63.
func TestPseudoPreflight_NoTxnSignature(t *testing.T) {
	engine, _ := closedEngine(t, amendment.AllSupportedRules())
	tx := newAmendmentTx()
	tx.Common.TxnSignature = "DEADBEEF"
	result := engine.ApplyPseudo(tx)
	require.False(t, result.Applied)
	require.Equal(t, "temBAD_SIGNATURE", result.Result.String())
}

// TestPseudoPreflight_SequenceMustBeZero rejects a pseudo-tx with a non-zero
// Sequence. The rippled gate also checks sfPreviousTxnID, but go-xrpl's Common
// struct has no such field.
// Reference: rippled Change.cpp:65-69.
func TestPseudoPreflight_SequenceMustBeZero(t *testing.T) {
	engine, _ := closedEngine(t, amendment.AllSupportedRules())
	tx := newAmendmentTx()
	one := uint32(1)
	tx.Common.Sequence = &one
	result := engine.ApplyPseudo(tx)
	require.False(t, result.Applied)
	require.Equal(t, "temBAD_SEQUENCE", result.Result.String())
}

// TestPseudoPreflight_UNLModifyDisabled rejects UNL_MODIFY when the
// NegativeUNL amendment is not enabled.
// Reference: rippled Change.cpp:72-77.
func TestPseudoPreflight_UNLModifyDisabled(t *testing.T) {
	rules := amendment.NewRules(nil) // no amendments enabled
	engine, _ := closedEngine(t, rules)

	unlTx := &pseudo.UNLModify{BaseTx: *tx.NewBaseTx(tx.TypeUNLModify, protocol.ZeroAccount)}
	unlTx.Common.Fee = "0"
	zero := uint32(0)
	unlTx.Common.Sequence = &zero

	result := engine.ApplyPseudo(unlTx)
	require.False(t, result.Applied)
	require.Equal(t, "temDISABLED", result.Result.String())
}

// TestPseudoPreflight_AcceptsCanonicalZeroAccount pins the canonical zero
// address as the one and only Account value that passes the preflight gate.
func TestPseudoPreflight_AcceptsCanonicalZeroAccount(t *testing.T) {
	engine, _ := closedEngine(t, amendment.AllSupportedRules())
	tx := newAmendmentTx()
	tx.Common.Account = protocol.ZeroAccount
	result := engine.ApplyPseudo(tx)
	require.NotEqual(t, "temBAD_SRC_ACCOUNT", result.Result.String(),
		"canonical zero address must pass the preflight gate")
}

// TestSetFee_PreclaimXRPFeesEnabled_RequiresModernFields rejects a SetFee
// missing any of the modern triple while featureXRPFees is enabled.
// Reference: rippled Change.cpp:96-104.
func TestSetFee_PreclaimXRPFeesEnabled_RequiresModernFields(t *testing.T) {
	engine, _ := closedEngine(t, amendment.AllSupportedRules())

	setFee := pseudo.NewSetFee()
	setFee.Common.Fee = "0"
	zero := uint32(0)
	setFee.Common.Sequence = &zero
	setFee.BaseFeeDrops = "10"
	// ReserveBaseDrops / ReserveIncrementDrops intentionally missing

	result := engine.ApplyPseudo(setFee)
	require.False(t, result.Applied)
	require.Equal(t, "temMALFORMED", result.Result.String())
}

// TestSetFee_PreclaimXRPFeesEnabled_ForbidsLegacyFields rejects a SetFee
// that carries any legacy fee field once featureXRPFees is enabled.
// Reference: rippled Change.cpp:105-112.
func TestSetFee_PreclaimXRPFeesEnabled_ForbidsLegacyFields(t *testing.T) {
	engine, _ := closedEngine(t, amendment.AllSupportedRules())

	setFee := pseudo.NewSetFee()
	setFee.Common.Fee = "0"
	zero := uint32(0)
	setFee.Common.Sequence = &zero
	setFee.BaseFeeDrops = "10"
	setFee.ReserveBaseDrops = "200000000"
	setFee.ReserveIncrementDrops = "50000000"
	setFee.BaseFee = "0A" // legacy hex field — must not appear under XRPFees

	result := engine.ApplyPseudo(setFee)
	require.False(t, result.Applied)
	require.Equal(t, "temMALFORMED", result.Result.String())
}

// TestSetFee_PreclaimXRPFeesDisabled_RequiresLegacyFields rejects a SetFee
// missing any of the legacy quad when featureXRPFees is NOT enabled.
// Reference: rippled Change.cpp:114-124.
func TestSetFee_PreclaimXRPFeesDisabled_RequiresLegacyFields(t *testing.T) {
	engine, _ := closedEngine(t, amendment.NewRules(nil))

	setFee := pseudo.NewSetFee()
	setFee.Common.Fee = "0"
	zero := uint32(0)
	setFee.Common.Sequence = &zero
	setFee.BaseFee = "0A"
	rfu := uint32(10)
	setFee.ReferenceFeeUnits = &rfu
	// ReserveBase / ReserveIncrement intentionally missing

	result := engine.ApplyPseudo(setFee)
	require.False(t, result.Applied)
	require.Equal(t, "temMALFORMED", result.Result.String())
}

// TestSetFee_PreclaimXRPFeesDisabled_ForbidsModernFields rejects a SetFee
// carrying any modern Drops field when featureXRPFees is NOT enabled.
// Reference: rippled Change.cpp:125-131 (returns temDISABLED).
func TestSetFee_PreclaimXRPFeesDisabled_ForbidsModernFields(t *testing.T) {
	engine, _ := closedEngine(t, amendment.NewRules(nil))

	setFee := newLegacySetFeeTx()
	setFee.BaseFeeDrops = "10" // modern field must not appear pre-XRPFees

	result := engine.ApplyPseudo(setFee)
	require.False(t, result.Applied)
	require.Equal(t, "temDISABLED", result.Result.String())
}

// TestSetFee_PreclaimRejectsUnparseableBaseFee rejects a SetFee whose legacy
// BaseFee contains a non-hex character.
func TestSetFee_PreclaimRejectsUnparseableBaseFee(t *testing.T) {
	engine, _ := closedEngine(t, amendment.NewRules(nil))

	setFee := newLegacySetFeeTx()
	setFee.BaseFee = "ZZ" // invalid hex

	result := engine.ApplyPseudo(setFee)
	require.False(t, result.Applied)
	require.Equal(t, "temMALFORMED", result.Result.String())
}

// closedEngineWithNetwork mirrors closedEngine but lets the test pin
// EngineConfig.NetworkID so the NetworkID branch of preflight0 fires.
func closedEngineWithNetwork(t *testing.T, rules *amendment.Rules, networkID uint32) *txengine.Engine {
	t.Helper()
	env := jtx.NewTestEnv(t)
	cfg := tx.EngineConfig{
		BaseFee:                   10,
		ReserveBase:               200_000_000,
		ReserveIncrement:          50_000_000,
		LedgerSequence:            env.LedgerSeq(),
		SkipSignatureVerification: true,
		OpenLedger:                false,
		Rules:                     rules,
		NetworkID:                 networkID,
	}
	return txengine.NewEngine(env.Ledger(), cfg)
}

// TestPseudoPreflight_TfInnerBatchTxnRejected rejects a pseudo-tx carrying
// the tfInnerBatchTxn flag, matching rippled preflight0 (Transactor.cpp:46-51).
func TestPseudoPreflight_TfInnerBatchTxnRejected(t *testing.T) {
	engine, _ := closedEngine(t, amendment.AllSupportedRules())
	atx := newAmendmentTx()
	flags := tx.TfInnerBatchTxn
	atx.Common.Flags = &flags
	result := engine.ApplyPseudo(atx)
	require.False(t, result.Applied)
	require.Equal(t, "temINVALID_FLAG", result.Result.String())
}

// TestPseudoPreflight_NetworkID_LegacyForbidsField pins rippled's rule that on
// legacy networks (NETWORK_ID <= 1024) any sfNetworkID on a pseudo-tx is
// non-canonical. Reference: Transactor.cpp:58-64.
func TestPseudoPreflight_NetworkID_LegacyForbidsField(t *testing.T) {
	engine := closedEngineWithNetwork(t, amendment.AllSupportedRules(), 0)
	atx := newAmendmentTx()
	nid := uint32(42)
	atx.Common.NetworkID = &nid
	result := engine.ApplyPseudo(atx)
	require.False(t, result.Applied)
	require.Equal(t, "telNETWORK_ID_MAKES_TX_NON_CANONICAL", result.Result.String())
}

// TestPseudoPreflight_NetworkID_NewNetworkRequiresMatch pins rippled's rule
// that on a new network (NETWORK_ID > 1024) a present sfNetworkID must match.
// Reference: Transactor.cpp:65-74.
func TestPseudoPreflight_NetworkID_NewNetworkRequiresMatch(t *testing.T) {
	engine := closedEngineWithNetwork(t, amendment.AllSupportedRules(), 2000)
	atx := newAmendmentTx()
	wrong := uint32(2001)
	atx.Common.NetworkID = &wrong
	result := engine.ApplyPseudo(atx)
	require.False(t, result.Applied)
	require.Equal(t, "telWRONG_NETWORK", result.Result.String())
}

// TestPseudoPreflight_NetworkID_AbsentAllowedOnPseudo confirms that, unlike
// normal transactions, a pseudo-tx without sfNetworkID is legal even on a
// new network — rippled gates the check on field presence for pseudo-tx.
// Reference: Transactor.cpp:53 ("|| ctx.tx.isFieldPresent(sfNetworkID)").
func TestPseudoPreflight_NetworkID_AbsentAllowedOnPseudo(t *testing.T) {
	engine := closedEngineWithNetwork(t, amendment.AllSupportedRules(), 2000)
	atx := newAmendmentTx() // NetworkID nil
	result := engine.ApplyPseudo(atx)
	require.NotEqual(t, "telREQUIRES_NETWORK_ID", result.Result.String(),
		"absent NetworkID must not trigger the new-network requirement for pseudo-tx")
}
