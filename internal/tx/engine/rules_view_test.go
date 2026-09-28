package engine

import (
	"bytes"
	"errors"
	"reflect"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	txcore "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/account"
	"github.com/LeJamon/go-xrpl/internal/tx/applystate"
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

func TestPreclaimReadOnlyAdapterDoesNotAliasTrackedEntries(t *testing.T) {
	for _, inner := range []bool{false, true} {
		name := "normal"
		if inner {
			name = "batch inner"
		}
		t.Run(name, func(t *testing.T) {
			base := newRecordingBaseView()
			key := fundRecoveryAccount(t, base, 1_000_000, 1)
			table := applystate.NewApplyStateTable(
				base,
				[32]byte{2},
				100,
				amendment.AllSupportedRules(),
			)
			original, err := table.Read(key)
			if err != nil {
				t.Fatalf("seed Read() error = %v", err)
			}
			original = bytes.Clone(original)

			txn := boundaryPreclaimTx{
				BaseTx: recoveryTx(10, 1),
				check: func(view txcore.ReadOnlyLedgerView) {
					data, err := view.Read(key)
					if err != nil {
						t.Fatalf("preclaim Read() error = %v", err)
					}
					data[0]++

					if err := view.ForEach(func(_ [32]byte, data []byte) bool {
						data[0]++
						return true
					}); err != nil {
						t.Fatalf("preclaim ForEach() error = %v", err)
					}

					_, data, found, err := view.Succ([32]byte{})
					if err != nil {
						t.Fatalf("preclaim Succ() error = %v", err)
					}
					if !found || len(data) == 0 {
						t.Fatal("preclaim Succ() did not return the tracked entry")
					}
					data[0]++
				},
			}

			e := recoveryEngine(table, txcore.TapNONE)
			var result ter.Result
			if inner {
				result = e.preclaimInner(txn, [32]byte{3})
			} else {
				result = e.preclaim(txn, [32]byte{3})
			}
			if result != ter.TecUNFUNDED_PAYMENT {
				t.Fatalf("preclaim result = %s, want tecUNFUNDED_PAYMENT", result)
			}

			got, err := table.Read(key)
			if err != nil {
				t.Fatalf("post-preclaim Read() error = %v", err)
			}
			if !bytes.Equal(got, original) {
				t.Fatal("preclaim mutated tracked ApplyStateTable data")
			}
			baseData, err := base.Read(key)
			if err != nil {
				t.Fatalf("base Read() error = %v", err)
			}
			if !bytes.Equal(baseData, original) {
				t.Fatal("preclaim mutated base ledger data through an alias")
			}
		})
	}
}
