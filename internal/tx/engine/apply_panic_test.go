package engine

import (
	"errors"
	"os"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	txcore "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/applystate"
	"github.com/LeJamon/go-xrpl/internal/tx/pseudo"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/LeJamon/go-xrpl/shamap"
)

type panicApplyTx struct {
	txcore.Transaction
	writeKey keylet.Keylet
	mutated  bool
	panicNil bool
}

func (t *panicApplyTx) Apply(ctx *txcore.ApplyContext) ter.Result {
	if err := ctx.View.Insert(t.writeKey, []byte("uncommitted")); err != nil {
		panic(err)
	}
	t.mutated = true
	if t.panicNil {
		panic(nil)
	}
	panic("apply failed")
}

type nonAppliableTx struct {
	txcore.Transaction
}

type panicGetCommonTx struct {
	txcore.Transaction
	panicNil bool
}

func (t *panicGetCommonTx) GetCommon() *txcore.Common {
	if t.panicNil {
		panic(nil)
	}
	panic("preflight failed")
}

type panicPseudoPreclaimTx struct {
	txcore.Transaction
}

func (t *panicPseudoPreclaimTx) PreclaimPseudo(*amendment.Rules) ter.Result {
	panic("preclaim failed")
}

type stagedStateApplyTx struct {
	txcore.Transaction
	key  keylet.Keylet
	data []byte
}

func (t *stagedStateApplyTx) Apply(ctx *txcore.ApplyContext) ter.Result {
	if err := ctx.View.Insert(t.key, t.data); err != nil {
		return ter.TefINTERNAL
	}
	return ter.TesSUCCESS
}

type failingAtomicPseudoView struct {
	*ledger.Ledger
	candidate *ledger.Ledger
}

func (v *failingAtomicPseudoView) ApplyAtomically(apply func(ledger.Writer) error) error {
	return v.Ledger.ApplyAtomically(apply)
}

func (v *failingAtomicPseudoView) ConsumeState(candidate *ledger.Ledger) error {
	v.candidate = candidate
	return errors.New("commit failed")
}

func TestApplyPanicReturnsTefExceptionWithoutMutatingState(t *testing.T) {
	view := newMockBaseView()
	accountKey := fundRecoveryAccount(t, view, 1_000_000, 1)
	writeKey := keylet.Keylet{Key: [32]byte{1}}
	txn := &panicApplyTx{
		Transaction: recoveryTx(10, 1),
		writeKey:    writeKey,
	}
	engine := recoveryEngine(view, txcore.TapNONE)

	result := engine.Apply(txn)

	assertRecoveredApplyPanic(t, result, txn, view, writeKey)
	if engine.TxCount() != 0 {
		t.Fatalf("transaction count = %d, want 0", engine.TxCount())
	}
	account := readRecoveryAccount(t, view, accountKey)
	if account.Balance != 1_000_000 || account.Sequence != 1 {
		t.Fatalf("account balance/sequence = %d/%d, want 1000000/1", account.Balance, account.Sequence)
	}
}

func TestApplyNilPanicReturnsTefExceptionWithoutMutatingState(t *testing.T) {
	enableNilPanic(t)

	view := newMockBaseView()
	accountKey := fundRecoveryAccount(t, view, 1_000_000, 1)
	writeKey := keylet.Keylet{Key: [32]byte{3}}
	txn := &panicApplyTx{
		Transaction: recoveryTx(10, 1),
		writeKey:    writeKey,
		panicNil:    true,
	}
	engine := recoveryEngine(view, txcore.TapNONE)

	result := engine.Apply(txn)

	assertRecoveredApplyPanic(t, result, txn, view, writeKey)
	if engine.TxCount() != 0 {
		t.Fatalf("transaction count = %d, want 0", engine.TxCount())
	}
	account := readRecoveryAccount(t, view, accountKey)
	if account.Balance != 1_000_000 || account.Sequence != 1 {
		t.Fatalf("account balance/sequence = %d/%d, want 1000000/1", account.Balance, account.Sequence)
	}
}

func TestApplyPseudoPanicReturnsTefExceptionWithoutMutatingState(t *testing.T) {
	view := newApplyPanicLedger(t)
	writeKey := keylet.Keylet{Key: [32]byte{2}}
	ledgerSequence := uint32(100)
	txn := &panicApplyTx{
		Transaction: &pseudo.EnableAmendment{
			BaseTx:         *txcore.NewBaseTx(txcore.TypeAmendment, protocol.ZeroAccount),
			Amendment:      "0101010101010101010101010101010101010101010101010101010101010101",
			LedgerSequence: &ledgerSequence,
		},
		writeKey: writeKey,
	}
	engine := pseudoRecoveryEngine(view, ledgerSequence)

	result := engine.ApplyPseudo(txn)

	assertRecoveredApplyPanic(t, result, txn, view, writeKey)
	if engine.TxCount() != 0 {
		t.Fatalf("transaction count = %d, want 0", engine.TxCount())
	}
}

func TestApplyMissingAppliableReturnsTefInternal(t *testing.T) {
	view := newMockBaseView()
	accountKey := fundRecoveryAccount(t, view, 1_000_000, 1)
	engine := recoveryEngine(view, txcore.TapNONE)

	result := engine.Apply(nonAppliableTx{Transaction: recoveryTx(10, 1)})

	assertNonAppliedResult(t, result, ter.TefINTERNAL)
	if engine.TxCount() != 0 {
		t.Fatalf("transaction count = %d, want 0", engine.TxCount())
	}
	account := readRecoveryAccount(t, view, accountKey)
	if account.Balance != 1_000_000 || account.Sequence != 1 {
		t.Fatalf("account balance/sequence = %d/%d, want 1000000/1", account.Balance, account.Sequence)
	}
}

func TestApplyPseudoMissingAppliableReturnsTefInternal(t *testing.T) {
	ledgerSequence := uint32(100)
	view := newApplyPanicLedger(t)
	engine := pseudoRecoveryEngine(view, ledgerSequence)
	txn := nonAppliableTx{Transaction: newApplyPanicAmendment(ledgerSequence)}

	result := engine.ApplyPseudo(txn)

	assertNonAppliedResult(t, result, ter.TefINTERNAL)
	if engine.TxCount() != 0 {
		t.Fatalf("transaction count = %d, want 0", engine.TxCount())
	}
}

func TestApplyPseudoPreflightPanicReturnsTefException(t *testing.T) {
	ledgerSequence := uint32(100)
	view := newApplyPanicLedger(t)
	engine := pseudoRecoveryEngine(view, ledgerSequence)
	txn := &panicGetCommonTx{Transaction: newApplyPanicAmendment(ledgerSequence)}

	result := engine.ApplyPseudo(txn)

	assertNonAppliedResult(t, result, ter.TefEXCEPTION)
	if engine.TxCount() != 0 {
		t.Fatalf("transaction count = %d, want 0", engine.TxCount())
	}
}

func TestApplyPseudoNilPreflightPanicReturnsTefException(t *testing.T) {
	enableNilPanic(t)
	ledgerSequence := uint32(100)
	view := newApplyPanicLedger(t)
	engine := pseudoRecoveryEngine(view, ledgerSequence)
	txn := &panicGetCommonTx{
		Transaction: newApplyPanicAmendment(ledgerSequence),
		panicNil:    true,
	}

	result := engine.ApplyPseudo(txn)

	assertNonAppliedResult(t, result, ter.TefEXCEPTION)
	if engine.TxCount() != 0 {
		t.Fatalf("transaction count = %d, want 0", engine.TxCount())
	}
}

func TestApplyPseudoPreclaimPanicReturnsTefException(t *testing.T) {
	ledgerSequence := uint32(100)
	view := newApplyPanicLedger(t)
	engine := pseudoRecoveryEngine(view, ledgerSequence)
	txn := &panicPseudoPreclaimTx{Transaction: newApplyPanicAmendment(ledgerSequence)}

	result := engine.ApplyPseudo(txn)

	assertNonAppliedResult(t, result, ter.TefEXCEPTION)
	if engine.TxCount() != 0 {
		t.Fatalf("transaction count = %d, want 0", engine.TxCount())
	}
}

func TestApplyPseudoStateCommitErrorDoesNotMutateLedger(t *testing.T) {
	ledgerSequence := uint32(100)
	view := newApplyPanicLedger(t)
	atomicView := &failingAtomicPseudoView{Ledger: view}
	engine := pseudoRecoveryEngine(atomicView, ledgerSequence)
	accountID, err := state.DecodeAccountID(recoveryTestAccount)
	if err != nil {
		t.Fatalf("DecodeAccountID: %v", err)
	}
	validData, err := state.SerializeAccountRoot(&state.AccountRoot{
		Account:  recoveryTestAccount,
		Balance:  1_000_000,
		Sequence: 1,
	})
	if err != nil {
		t.Fatalf("SerializeAccountRoot: %v", err)
	}
	writeKey := keylet.Account(accountID)
	txn := &stagedStateApplyTx{
		Transaction: newApplyPanicAmendment(ledgerSequence),
		key:         writeKey,
		data:        validData,
	}
	wantHash, err := view.StateMapHash()
	if err != nil {
		t.Fatalf("StateMapHash: %v", err)
	}

	result := engine.ApplyPseudo(txn)

	assertNonAppliedResult(t, result, ter.TefINTERNAL)
	if atomicView.candidate == nil {
		t.Fatal("atomic commit was not attempted")
	}
	staged, readErr := atomicView.candidate.Read(writeKey)
	if readErr != nil {
		t.Fatalf("read staged key: %v", readErr)
	}
	if staged == nil {
		t.Fatal("snapshot did not contain the staged write")
	}
	gotHash, err := view.StateMapHash()
	if err != nil {
		t.Fatalf("StateMapHash: %v", err)
	}
	if gotHash != wantHash {
		t.Fatalf("state map hash changed after failed atomic commit")
	}
	data, readErr := view.Read(writeKey)
	if readErr != nil {
		t.Fatalf("read key after failed commit: %v", readErr)
	}
	if data != nil {
		t.Fatalf("key %x committed after failed atomic commit", writeKey.Key)
	}
	if engine.TxCount() != 0 {
		t.Fatalf("transaction count = %d, want 0", engine.TxCount())
	}
}

func assertRecoveredApplyPanic(t *testing.T, result txcore.ApplyResult, txn *panicApplyTx, view txcore.LedgerView, writeKey keylet.Keylet) {
	t.Helper()
	if !txn.mutated {
		t.Fatal("Apply did not mutate its sandbox before panicking")
	}
	if result.Result != ter.TefEXCEPTION {
		t.Fatalf("result = %s, want tefEXCEPTION", result.Result)
	}
	if result.Applied {
		t.Fatal("panicking transaction must not be applied")
	}
	if result.Fee != 0 {
		t.Fatalf("fee = %d, want 0", result.Fee)
	}
	if result.Metadata != nil {
		t.Fatalf("metadata = %#v, want nil", result.Metadata)
	}
	data, err := view.Read(writeKey)
	if err != nil {
		t.Fatalf("read sandbox mutation: %v", err)
	}
	if data != nil {
		t.Fatalf("sandbox mutation was committed: %q", data)
	}
}

func assertNonAppliedResult(t *testing.T, result txcore.ApplyResult, want ter.Result) {
	t.Helper()
	if result.Result != want {
		t.Fatalf("result = %s, want %s", result.Result, want)
	}
	if result.Applied {
		t.Fatal("transaction must not be applied")
	}
	if result.Fee != 0 {
		t.Fatalf("fee = %d, want 0", result.Fee)
	}
	if result.Metadata != nil {
		t.Fatalf("metadata = %#v, want nil", result.Metadata)
	}
}

func enableNilPanic(t *testing.T) {
	t.Helper()
	godebug := os.Getenv("GODEBUG")
	if godebug != "" {
		godebug += ","
	}
	t.Setenv("GODEBUG", godebug+"panicnil=1")
}

func newApplyPanicAmendment(ledgerSequence uint32) *pseudo.EnableAmendment {
	return &pseudo.EnableAmendment{
		BaseTx:         *txcore.NewBaseTx(txcore.TypeAmendment, protocol.ZeroAccount),
		Amendment:      "0101010101010101010101010101010101010101010101010101010101010101",
		LedgerSequence: &ledgerSequence,
	}
}

func newApplyPanicLedger(t *testing.T) *ledger.Ledger {
	t.Helper()
	view, err := ledger.NewOpenWithHeader(
		header.LedgerHeader{LedgerIndex: 100},
		shamap.New(shamap.TypeState),
		shamap.New(shamap.TypeTransaction),
		drops.Fees{},
	)
	if err != nil {
		t.Fatalf("NewOpenWithHeader: %v", err)
	}
	return view
}

func pseudoRecoveryEngine(view applystate.AtomicLedgerView, ledgerSequence uint32) *Engine {
	return NewEngine(view, txcore.EngineConfig{
		LedgerSequence: ledgerSequence,
		Rules:          amendment.AllSupportedRules(),
	})
}
