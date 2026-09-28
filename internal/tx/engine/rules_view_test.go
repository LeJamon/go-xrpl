package engine

import (
	"errors"
	"reflect"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	txcore "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/account"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
)

type readOnlyFakeView struct{ readErr error }

func (v *readOnlyFakeView) Read(keylet.Keylet) ([]byte, error) { return nil, v.readErr }

func (*readOnlyFakeView) Exists(keylet.Keylet) (bool, error) { return false, nil }

func (*readOnlyFakeView) ForEach(func([32]byte, []byte) bool) error { return nil }

func (*readOnlyFakeView) Succ([32]byte) ([32]byte, []byte, bool, error) {
	return [32]byte{}, nil, false, nil
}

func (*readOnlyFakeView) TxExists([32]byte) (bool, error) { return false, nil }

func (*readOnlyFakeView) Rules() *amendment.Rules { return nil }

func (*readOnlyFakeView) LedgerSeq() uint32 { return 0 }

type balanceHookView struct {
	readOnlyFakeView
	want int64
}

func (v *balanceHookView) BalanceHookMPT([20]byte, [24]byte, int64) int64 {
	return v.want
}

func TestRulesViewBalanceHookMPT(t *testing.T) {
	account := [20]byte{1}
	id := [24]byte{2}

	withHook := rulesView{ReadOnlyLedgerView: &balanceHookView{want: 7}}
	if got := withHook.BalanceHookMPT(account, id, 11); got != 7 {
		t.Fatalf("BalanceHookMPT() = %d, want 7", got)
	}

	withoutHook := rulesView{ReadOnlyLedgerView: &readOnlyFakeView{}}
	if got := withoutHook.BalanceHookMPT(account, id, 11); got != 11 {
		t.Fatalf("BalanceHookMPT() without hook = %d, want 11", got)
	}
}

type boundaryPreclaimTx struct {
	*txcore.BaseTx
	check func(txcore.ReadOnlyLedgerView)
}

func (tx boundaryPreclaimTx) Preclaim(view txcore.ReadOnlyLedgerView, _ txcore.EngineConfig) ter.Result {
	tx.check(view)
	return ter.TecUNFUNDED_PAYMENT
}

func TestPreclaimDispatchHidesWrites(t *testing.T) {
	for _, inner := range []bool{false, true} {
		name := "normal"
		if inner {
			name = "batch inner"
		}
		t.Run(name, func(t *testing.T) {
			base := newRecordingBaseView()
			fundRecoveryAccount(t, base, 1_000_000, 1)
			e := recoveryEngine(base, txcore.TapNONE)
			called := false
			txn := boundaryPreclaimTx{BaseTx: recoveryTx(10, 1), check: func(view txcore.ReadOnlyLedgerView) {
				called = true
				for _, method := range []string{"Insert", "Update", "Erase", "AdjustDropsDestroyed"} {
					if _, exposed := reflect.TypeOf(view).MethodByName(method); exposed {
						t.Errorf("preclaim view exposes %s", method)
					}
				}
				if view.Rules() != e.config.Rules {
					t.Error("preclaim lost engine rules")
				}
			}}
			var got ter.Result
			if inner {
				got = e.preclaimInner(txn, [32]byte{1})
			} else {
				got = e.preclaim(txn, [32]byte{1})
			}
			if !called {
				t.Fatal("transaction preclaim was not called")
			}
			if got != ter.TecUNFUNDED_PAYMENT {
				t.Fatalf("result = %s, want tecUNFUNDED_PAYMENT", got)
			}
			if base.destroyed != 0 {
				t.Fatal("preclaim destroyed XRP")
			}
		})
	}
}

func TestPreclaimWithReadOnlyFakePreservesReadErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want ter.Result
	}{
		{name: "missing", want: ter.TerNO_ACCOUNT},
		{name: "storage error", err: errors.New("storage unavailable"), want: ter.TefINTERNAL},
	} {
		t.Run(tc.name, func(t *testing.T) {
			view := &readOnlyFakeView{readErr: tc.err}
			txn := account.NewAccountSet(recoveryTestAccount)
			wrapped := rulesView{ReadOnlyLedgerView: view, rules: amendment.AllSupportedRules()}
			if got := txn.Preclaim(wrapped, txcore.EngineConfig{}); got != tc.want {
				t.Fatalf("Preclaim() = %s, want %s", got, tc.want)
			}
		})
	}
}
