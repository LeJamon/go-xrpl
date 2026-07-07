package engine

import (
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	txcore "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

// These tests cover the rippled 3.1.2 parity work (PR #6540): an
// internal-inconsistency / arithmetic-overflow condition reachable through the
// transaction pipeline must never terminate the node. rippled converted the
// offending LogicError()→abort() sites into catchable exceptions that its
// applySteps.cpp preflight/preclaim/doApply handlers absorb into tefEXCEPTION;
// go-xrpl surfaces the equivalent Go panics as tefEXCEPTION (engine phases) or a
// dropped-and-continue failure (consensus build loop). The synthetic panics
// below stand in for the real IOUAmount / XRPLNumber overflow panics in
// internal/ledger/state — the recover path they exercise is identical.

// panicPreclaimTx is an AccountSet-shaped tx whose per-type Preclaim panics,
// standing in for an amount/number overflow reached while reading crafted
// ledger state during preclaim.
type panicPreclaimTx struct {
	*txcore.BaseTx
}

func (panicPreclaimTx) Preclaim(txcore.LedgerView, txcore.EngineConfig) ter.Result {
	panic("simulated IOUAmount overflow during preclaim")
}

// panicPreflightTx is an AccountSet-shaped tx whose Validate panics, standing in
// for an overflow reached during the stateless preflight checks.
type panicPreflightTx struct {
	*txcore.BaseTx
}

func (panicPreflightTx) Validate() error {
	panic("simulated overflow during preflight validation")
}

// panicBookkeepingTx panics from GetCommon, which the engine touches in Apply
// bookkeeping outside the invokeApply / preflight / preclaim recover scopes —
// exactly the class of escape the consensus-build backstop must absorb.
type panicBookkeepingTx struct {
	*txcore.BaseTx
}

func (panicBookkeepingTx) GetCommon() *txcore.Common {
	panic("simulated engine-bookkeeping panic outside invokeApply")
}

// TestApply_PreclaimPanic_YieldsTefException: a panic raised inside preclaim is
// recovered and surfaced as tefEXCEPTION, with the transaction not applied and
// the engine still usable. Mirrors rippled applySteps.cpp preclaim()'s
// catch(std::exception) → {tefEXCEPTION}.
func TestApply_PreclaimPanic_YieldsTefException(t *testing.T) {
	view := newRecordingBaseView()
	fundRecoveryAccount(t, view, 1_000_000, 1)

	e := recoveryEngine(view, txcore.TapNONE)
	res := e.Apply(panicPreclaimTx{BaseTx: recoveryTx(10, 1)})

	if res.Result != ter.TefEXCEPTION {
		t.Fatalf("result = %s, want tefEXCEPTION", res.Result)
	}
	if res.Applied {
		t.Fatalf("a tefEXCEPTION tx must not be applied")
	}
	if view.destroyed != 0 {
		t.Fatalf("destroyed drops = %d, want 0 (tef charges no fee)", view.destroyed)
	}
}

// TestApply_PreflightPanic_YieldsTefException: a panic raised inside preflight is
// recovered and surfaced as tefEXCEPTION. Mirrors rippled applySteps.cpp
// preflight()'s catch(std::exception) → {tefEXCEPTION}.
func TestApply_PreflightPanic_YieldsTefException(t *testing.T) {
	view := newRecordingBaseView()
	fundRecoveryAccount(t, view, 1_000_000, 1)

	e := recoveryEngine(view, txcore.TapNONE)
	res := e.Apply(panicPreflightTx{BaseTx: recoveryTx(10, 1)})

	if res.Result != ter.TefEXCEPTION {
		t.Fatalf("result = %s, want tefEXCEPTION", res.Result)
	}
	if res.Applied {
		t.Fatalf("a tefEXCEPTION tx must not be applied")
	}
}

// TestBlockProcessor_ApplyPanic_BecomesErrorAndContinues is the consensus-build
// backstop: a panic escaping the engine's Apply-scoped recover is converted to an
// error by BlockProcessor.ApplyTransaction (so the build loop drops that one tx),
// and the processor keeps working — a following good tx still applies. Mirrors
// rippled applyTransactions' per-tx catch(std::exception): mark failed, continue.
func TestBlockProcessor_ApplyPanic_BecomesErrorAndContinues(t *testing.T) {
	view := newRecordingBaseView()
	fundRecoveryAccount(t, view, 1_000_000, 1)

	bp := NewBlockProcessor(recoveryEngine(view, txcore.TapNONE))

	// Poisoned tx: the recover must convert its panic into an error instead of
	// unwinding through the (would-be) consensus goroutine. The test itself
	// panicking would fail here, which is the property under test.
	if _, err := bp.ApplyTransaction(panicBookkeepingTx{BaseTx: recoveryTx(10, 1)}, []byte{0x01}); err == nil {
		t.Fatalf("expected a non-nil error from the recovered panic, got nil")
	}

	// The processor survived: a subsequent well-formed tx still applies. The
	// poisoned tx mutated nothing (it panicked before any state change), so the
	// account is untouched and this AccountSet no-op succeeds.
	res, err := bp.ApplyTransaction(recoveryTx(10, 1), []byte{0x02})
	if err != nil {
		t.Fatalf("good tx after a poisoned one returned err: %v", err)
	}
	if !res.ApplyResult.Result.IsSuccess() {
		t.Fatalf("good tx result = %s, want a success code", res.ApplyResult.Result)
	}
}

// txExistsView reports a chosen tx id as already present, driving the duplicate
// tx-id branch of preclaim's checkPriorTxAndLastLedger.
type txExistsView struct {
	*mockBaseView
	existing [32]byte
}

func (v txExistsView) TxExists(h [32]byte) bool { return h == v.existing }

// TestApply_DuplicateTxId_YieldsTefAlready: inserting a tx id already present in
// the view fails that one tx with tefALREADY (not applied, no crash), leaving the
// engine usable. Reference: rippled Transactor::checkPriorTxAndLastLedger's
// ctx.view.txExists() → tefALREADY; the open-ledger build loop likewise skips
// duplicate ids per tx without aborting the build.
func TestApply_DuplicateTxId_YieldsTefAlready(t *testing.T) {
	base := newMockBaseView()
	fundRecoveryAccount(t, base, 1_000_000, 1)

	txn := recoveryTx(10, 1)
	h, err := txcore.ComputeTransactionHash(txn)
	if err != nil {
		t.Fatalf("ComputeTransactionHash: %v", err)
	}

	view := txExistsView{mockBaseView: base, existing: h}
	e := NewEngine(view, txcore.EngineConfig{
		BaseFee:                   10,
		LedgerSequence:            100,
		Rules:                     amendment.AllSupportedRules(),
		SkipSignatureVerification: true,
		OpenLedger:                false,
	})

	res := e.Apply(txn)
	if res.Result != ter.TefALREADY {
		t.Fatalf("result = %s, want tefALREADY", res.Result)
	}
	if res.Applied {
		t.Fatalf("a duplicate tx must not be applied")
	}
}
