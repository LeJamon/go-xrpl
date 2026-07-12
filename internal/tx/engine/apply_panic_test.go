package engine

import (
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	txcore "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/pseudo"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/protocol"
)

type panicApplyTx struct {
	txcore.Transaction
	writeKey keylet.Keylet
	mutated  bool
}

func (t *panicApplyTx) Apply(ctx *txcore.ApplyContext) ter.Result {
	if err := ctx.View.Insert(t.writeKey, []byte("uncommitted")); err != nil {
		panic(err)
	}
	t.mutated = true
	panic("apply failed")
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

func TestApplyPseudoPanicReturnsTefExceptionWithoutMutatingState(t *testing.T) {
	view := newMockBaseView()
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
	engine := NewEngine(view, txcore.EngineConfig{
		LedgerSequence: ledgerSequence,
		Rules:          amendment.AllSupportedRules(),
	})

	result := engine.ApplyPseudo(txn)

	assertRecoveredApplyPanic(t, result, txn, view, writeKey)
	if engine.TxCount() != 0 {
		t.Fatalf("transaction count = %d, want 0", engine.TxCount())
	}
}

func assertRecoveredApplyPanic(t *testing.T, result txcore.ApplyResult, txn *panicApplyTx, view *mockBaseView, writeKey keylet.Keylet) {
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
