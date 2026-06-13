package tx

import (
	"strconv"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/keylet"
)

// recoveryTestAccount is a real, decodable classic address used to key the
// AccountRoot in the mock view for the engine-level recovery tests.
const recoveryTestAccount = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"

// recordingBaseView wraps mockBaseView to capture the drops passed to
// AdjustDropsDestroyed, so a test can assert that only the *clamped* fee — the
// fee actually charged — is destroyed.
type recordingBaseView struct {
	*mockBaseView
	destroyed drops.XRPAmount
}

func newRecordingBaseView() *recordingBaseView {
	return &recordingBaseView{mockBaseView: newMockBaseView()}
}

func (r *recordingBaseView) AdjustDropsDestroyed(d drops.XRPAmount) {
	r.destroyed += d
}

// recoveryEngine builds an engine over the supplied view configured for a
// closed-ledger apply (OpenLedger=false) with signature verification skipped, so
// a single-signed BaseTx reaches preclaim/commit without real crypto.
func recoveryEngine(view LedgerView, flags ApplyFlags) *Engine {
	return NewEngine(view, EngineConfig{
		BaseFee:                   10,
		LedgerSequence:            100,
		Rules:                     amendment.AllSupportedRules(),
		SkipSignatureVerification: true,
		OpenLedger:                false,
		ApplyFlags:                flags,
	})
}

func fundRecoveryAccount(t *testing.T, view interface {
	Insert(keylet.Keylet, []byte) error
}, balance uint64, seq uint32) keylet.Keylet {
	t.Helper()
	accountID, err := state.DecodeAccountID(recoveryTestAccount)
	if err != nil {
		t.Fatalf("DecodeAccountID: %v", err)
	}
	data, err := state.SerializeAccountRoot(&state.AccountRoot{
		Account:  recoveryTestAccount,
		Balance:  balance,
		Sequence: seq,
	})
	if err != nil {
		t.Fatalf("SerializeAccountRoot: %v", err)
	}
	k := keylet.Account(accountID)
	if err := view.Insert(k, data); err != nil {
		t.Fatalf("Insert account: %v", err)
	}
	return k
}

func readRecoveryAccount(t *testing.T, view LedgerView, k keylet.Keylet) *state.AccountRoot {
	t.Helper()
	data, err := view.Read(k)
	if err != nil || data == nil {
		t.Fatalf("Read account: data=%v err=%v", data, err)
	}
	acct, err := state.ParseAccountRoot(data)
	if err != nil {
		t.Fatalf("ParseAccountRoot: %v", err)
	}
	return acct
}

// recoveryTx builds a closed-ledger-applicable single-signed AccountSet-shaped
// BaseTx with an explicit fee and sequence. AccountSet is a no-op when it has no
// fields, so it never reaches a per-type Apply that would itself fail.
func recoveryTx(fee, seq uint32) *BaseTx {
	tx := NewBaseTx(TypeAccountSet, recoveryTestAccount)
	tx.Common.Fee = strconv.FormatUint(uint64(fee), 10)
	s := seq
	tx.Common.Sequence = &s
	return tx
}

// TestApply_TecInsuffFee_ClampsToBalance is the Item 1 regression: a closed-ledger
// apply that hits tecINSUFF_FEE (payer balance non-zero but below the fee) must
// charge only the balance, leaving the payer at exactly 0 — never underflowing
// the uint64 balance. The fee reported and destroyed is the clamped value.
// Reference: rippled Transactor::reset() Transactor.cpp:1027 (`if (fee > balance)
// fee = balance`) + destroyXRP(fee) at 1262-1263.
func TestApply_TecInsuffFee_ClampsToBalance(t *testing.T) {
	view := newRecordingBaseView()
	// Balance 5 drops, fee 10 drops: balance > 0 and balance < fee on a closed
	// ledger → checkFee returns tecINSUFF_FEE.
	acctKey := fundRecoveryAccount(t, view, 5, 1)

	e := recoveryEngine(view, TapNONE)
	res := e.Apply(recoveryTx(10, 1))

	if res.Result != TecINSUFF_FEE {
		t.Fatalf("result = %s, want tecINSUFF_FEE", res.Result)
	}
	if !res.Applied {
		t.Fatalf("tecINSUFF_FEE on a closed ledger must be applied (fee claimed)")
	}
	if res.Fee != 5 {
		t.Fatalf("charged fee = %d, want 5 (clamped to balance)", res.Fee)
	}
	if view.destroyed != drops.XRPAmount(5) {
		t.Fatalf("destroyed drops = %d, want 5 (clamped fee)", view.destroyed)
	}
	acct := readRecoveryAccount(t, view, acctKey)
	if acct.Balance != 0 {
		t.Fatalf("payer balance = %d, want 0 (clamped, not underflowed)", acct.Balance)
	}
	// Sequence is still consumed on a claimed-fee tec.
	if acct.Sequence != 2 {
		t.Fatalf("payer sequence = %d, want 2 (consumed)", acct.Sequence)
	}
}

// TestApply_FailHard_TecNotApplied is the Item 2 regression: when TapFAIL_HARD is
// set, a tec result must do nothing — no fee charged, no sequence consumed,
// Applied=false, and the result code preserved. Reference: rippled Transactor.cpp
// :1114-1120 (`if (isTecClaim(result) && (view().flags() & tapFAIL_HARD)) {
// ctx_.discard(); applied = false; }`).
func TestApply_FailHard_TecNotApplied(t *testing.T) {
	view := newRecordingBaseView()
	acctKey := fundRecoveryAccount(t, view, 5, 1)

	e := recoveryEngine(view, TapFAIL_HARD)
	res := e.Apply(recoveryTx(10, 1))

	if res.Result != TecINSUFF_FEE {
		t.Fatalf("result = %s, want tecINSUFF_FEE (preserved under fail_hard)", res.Result)
	}
	if res.Applied {
		t.Fatalf("fail_hard tec must not be applied")
	}
	if view.destroyed != 0 {
		t.Fatalf("destroyed drops = %d, want 0 (fail_hard charges no fee)", view.destroyed)
	}
	acct := readRecoveryAccount(t, view, acctKey)
	if acct.Balance != 5 {
		t.Fatalf("payer balance = %d, want 5 (unchanged under fail_hard)", acct.Balance)
	}
	if acct.Sequence != 1 {
		t.Fatalf("payer sequence = %d, want 1 (not consumed under fail_hard)", acct.Sequence)
	}
}

// codeTecTx is a minimal Appliable whose Apply() returns a configurable tec
// code, used to drive the doApply tec branch (as opposed to the preclaim-tec
// branch) for a specific code.
type codeTecTx struct {
	*BaseTx
	code Result
}

func (t codeTecTx) Apply(*ApplyContext) Result { return t.code }

// TestApply_FailHard_DoApplyTecNotApplied covers the second half of Item 2: a tec
// that surfaces from doApply (not preclaim) must also be discarded under
// TapFAIL_HARD — no fee charged, no sequence consumed, Applied=false.
func TestApply_FailHard_DoApplyTecNotApplied(t *testing.T) {
	view := newRecordingBaseView()
	// Ample balance so preclaim passes and doApply runs to return its tec.
	acctKey := fundRecoveryAccount(t, view, 1_000_000, 1)

	e := recoveryEngine(view, TapFAIL_HARD)
	txn := codeTecTx{BaseTx: recoveryTx(10, 1), code: TecUNFUNDED_PAYMENT}
	res := e.Apply(txn)

	if res.Result != TecUNFUNDED_PAYMENT {
		t.Fatalf("result = %s, want tecUNFUNDED_PAYMENT (preserved under fail_hard)", res.Result)
	}
	if res.Applied {
		t.Fatalf("fail_hard doApply tec must not be applied")
	}
	if view.destroyed != 0 {
		t.Fatalf("destroyed drops = %d, want 0 (fail_hard charges no fee)", view.destroyed)
	}
	acct := readRecoveryAccount(t, view, acctKey)
	if acct.Balance != 1_000_000 {
		t.Fatalf("payer balance = %d, want 1000000 (unchanged under fail_hard)", acct.Balance)
	}
	if acct.Sequence != 1 {
		t.Fatalf("payer sequence = %d, want 1 (not consumed under fail_hard)", acct.Sequence)
	}
}

// TestApply_Retry_GenericTecNotApplied: under TapRETRY a generic doApply tec
// (not one of the four work-on-tec codes) is NOT applied — no fee, no sequence,
// Applied=false — so the tx is retried on a later pass. Reference: rippled
// applySteps.h:49-51, isTecClaimHardFail = isTecClaim && !(flags & tapRETRY).
func TestApply_Retry_GenericTecNotApplied(t *testing.T) {
	view := newRecordingBaseView()
	acctKey := fundRecoveryAccount(t, view, 1_000_000, 1)

	e := recoveryEngine(view, TapRETRY)
	res := e.Apply(codeTecTx{BaseTx: recoveryTx(10, 1), code: TecUNFUNDED_PAYMENT})

	if res.Result != TecUNFUNDED_PAYMENT {
		t.Fatalf("result = %s, want tecUNFUNDED_PAYMENT", res.Result)
	}
	if res.Applied {
		t.Fatalf("generic tec under TapRETRY must not be applied")
	}
	if view.destroyed != 0 {
		t.Fatalf("destroyed drops = %d, want 0 (no fee under retry)", view.destroyed)
	}
	acct := readRecoveryAccount(t, view, acctKey)
	if acct.Balance != 1_000_000 || acct.Sequence != 1 {
		t.Fatalf("payer balance/seq = %d/%d, want 1000000/1 (untouched under retry)",
			acct.Balance, acct.Sequence)
	}
}

// TestApply_Retry_WorkOnTecReapplied: under TapRETRY the four "work-on-tec"
// codes (tecOVERSIZE/tecKILLED/tecINCOMPLETE/tecEXPIRED) are STILL reapplied —
// fee claimed, sequence consumed, Applied=true. rippled lists them
// unconditionally in the reapply branch (Transactor.cpp:1121-1124); only the
// generic isTecClaimHardFail term is the one suppressed under tapRETRY.
func TestApply_Retry_WorkOnTecReapplied(t *testing.T) {
	view := newRecordingBaseView()
	acctKey := fundRecoveryAccount(t, view, 1_000_000, 1)

	e := recoveryEngine(view, TapRETRY)
	res := e.Apply(codeTecTx{BaseTx: recoveryTx(10, 1), code: TecKILLED})

	if res.Result != TecKILLED {
		t.Fatalf("result = %s, want tecKILLED", res.Result)
	}
	if !res.Applied {
		t.Fatalf("work-on-tec code under TapRETRY must be applied (fee claimed)")
	}
	if res.Fee != 10 {
		t.Fatalf("charged fee = %d, want 10", res.Fee)
	}
	if view.destroyed != drops.XRPAmount(10) {
		t.Fatalf("destroyed drops = %d, want 10 (fee claimed)", view.destroyed)
	}
	acct := readRecoveryAccount(t, view, acctKey)
	if acct.Balance != 999_990 {
		t.Fatalf("payer balance = %d, want 999990 (fee charged)", acct.Balance)
	}
	if acct.Sequence != 2 {
		t.Fatalf("payer sequence = %d, want 2 (consumed)", acct.Sequence)
	}
}

// TestApply_PreclaimTec_InvariantViolation is the Item 3 regression: a tec that
// claims a fee straight out of preclaim (never entering doApply) must still run
// the invariant pass on its fee-only delta. A forced violation escalates the
// result to tecINVARIANT_FAILED. Reference: rippled Transactor.cpp:1218-1238 —
// checkInvariants runs for every applied result, tec claims included.
func TestApply_PreclaimTec_InvariantViolation(t *testing.T) {
	view := newRecordingBaseView()
	// Balance well above the fee so checkFee yields the deterministic tec
	// (tecINSUFF_FEE needs balance < fee); use a fee the balance cannot cover on
	// a closed ledger to reach the preclaim-tec commit path.
	fundRecoveryAccount(t, view, 5, 1)

	e := recoveryEngine(view, TapNONE)
	// Force an invariant violation on the FIRST pass only. rippled's two-pass
	// escalation (Transactor.cpp:1224-1238) resets to a fee-only state and
	// re-checks: a clean second pass yields tecINVARIANT_FAILED, a second
	// violation yields tefINVARIANT_FAILED. Firing once exercises the
	// tec→fee-only-claim escalation that the preclaim-tec commit must now run.
	firstPass := true
	e.SetInvariantViolationHookForTest(func(result Result, table *ApplyStateTable) *InvariantViolationValue {
		if firstPass {
			firstPass = false
			return NewInvariantViolation("forced", "forced violation for test")
		}
		return nil
	})

	res := e.Apply(recoveryTx(10, 1))
	if res.Result != TecINVARIANT_FAILED {
		t.Fatalf("result = %s, want tecINVARIANT_FAILED", res.Result)
	}
}

// TestPreflight_InvalidSigningPubKey is the Item 5 regression: a non-empty but
// malformed SigningPubKey must be rejected with temBAD_SIGNATURE in preflight,
// independent of SkipSignatureVerification. Reference: rippled preflight1
// Transactor.cpp:129-135 (`!spk.empty() && !publicKeyType(makeSlice(spk))`).
func TestPreflight_InvalidSigningPubKey(t *testing.T) {
	view := newRecordingBaseView()
	fundRecoveryAccount(t, view, 1_000_000, 1)

	// SkipSignatureVerification=true is the standalone-RPC ingress mode that
	// bypasses crypto verification; the key-type rejection must still fire.
	e := recoveryEngine(view, TapNONE)

	tx := recoveryTx(10, 1)
	// 0x99-prefixed 33-byte blob: a valid-length hex payload that is NOT a valid
	// key type (publicKeyType rejects it).
	tx.Common.SigningPubKey = "99" +
		"00000000000000000000000000000000000000000000000000000000000000"

	res := e.Apply(tx)
	if res.Result != TemBAD_SIGNATURE {
		t.Fatalf("result = %s, want temBAD_SIGNATURE for invalid signing key type", res.Result)
	}
	if res.Applied {
		t.Fatalf("malformed-key tx must not be applied")
	}
}
