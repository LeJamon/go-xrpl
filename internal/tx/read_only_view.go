package tx

import (
	"bytes"

	"github.com/LeJamon/go-xrpl/keylet"
)

// readOnlyLedgerView hides the concrete view from read-only transaction
// callbacks while keeping the data they receive independent of staged state.
type readOnlyLedgerView struct {
	ReadOnlyLedgerView
}

func NewReadOnlyLedgerView(view ReadOnlyLedgerView) ReadOnlyLedgerView {
	if view == nil {
		return nil
	}
	if _, ok := view.(*readOnlyLedgerView); ok {
		return view
	}
	return &readOnlyLedgerView{ReadOnlyLedgerView: view}
}

func (v *readOnlyLedgerView) Read(k keylet.Keylet) ([]byte, error) {
	data, err := v.ReadOnlyLedgerView.Read(k)
	return bytes.Clone(data), err
}

func (v *readOnlyLedgerView) ForEach(fn func(key [32]byte, data []byte) bool) error {
	return v.ReadOnlyLedgerView.ForEach(func(key [32]byte, data []byte) bool {
		return fn(key, bytes.Clone(data))
	})
}

func (v *readOnlyLedgerView) Succ(key [32]byte) ([32]byte, []byte, bool, error) {
	foundKey, data, found, err := v.ReadOnlyLedgerView.Succ(key)
	return foundKey, bytes.Clone(data), found, err
}

func (v *readOnlyLedgerView) BalanceHookMPT(account [20]byte, id [24]byte, amount int64) int64 {
	if hook, ok := v.ReadOnlyLedgerView.(interface {
		BalanceHookMPT([20]byte, [24]byte, int64) int64
	}); ok {
		return hook.BalanceHookMPT(account, id, amount)
	}
	return amount
}
