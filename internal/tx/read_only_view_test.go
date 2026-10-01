package tx

import (
	"bytes"
	"errors"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/keylet"
)

type readOnlyViewSource struct {
	readData    []byte
	readErr     error
	forEachKey  [32]byte
	forEachData []byte
	forEachErr  error
	stopped     bool
	succKey     [32]byte
	succData    []byte
	succFound   bool
	succErr     error
	rules       *amendment.Rules
}

func (v *readOnlyViewSource) Read(keylet.Keylet) ([]byte, error) { return v.readData, v.readErr }

func (v *readOnlyViewSource) Exists(keylet.Keylet) (bool, error) { return v.readData != nil, nil }

func (v *readOnlyViewSource) ForEach(fn func([32]byte, []byte) bool) error {
	if !fn(v.forEachKey, v.forEachData) {
		v.stopped = true
	}
	return v.forEachErr
}

func (v *readOnlyViewSource) Succ([32]byte) ([32]byte, []byte, bool, error) {
	return v.succKey, v.succData, v.succFound, v.succErr
}

func (*readOnlyViewSource) TxExists([32]byte) (bool, error) { return false, nil }

func (v *readOnlyViewSource) Rules() *amendment.Rules { return v.rules }

func (*readOnlyViewSource) LedgerSeq() uint32 { return 7 }

func (*readOnlyViewSource) Insert(keylet.Keylet, []byte) error { return nil }

func (*readOnlyViewSource) Update(keylet.Keylet, []byte) error { return nil }

func (*readOnlyViewSource) Erase(keylet.Keylet) error { return nil }

func (*readOnlyViewSource) AdjustDropsDestroyed(drops.XRPAmount) error { return nil }

type balanceHookReadOnlyViewSource struct {
	readOnlyViewSource
	result  int64
	called  bool
	account [20]byte
	id      [24]byte
}

func (v *balanceHookReadOnlyViewSource) BalanceHookMPT(account [20]byte, id [24]byte, amount int64) int64 {
	v.called = true
	v.account = account
	v.id = id
	return v.result + amount
}

func TestNewReadOnlyLedgerViewBoundary(t *testing.T) {
	if got := NewReadOnlyLedgerView(nil); got != nil {
		t.Fatal("NewReadOnlyLedgerView(nil) returned a non-nil view")
	}

	rules := amendment.AllSupportedRules()
	source := &readOnlyViewSource{rules: rules}
	view := NewReadOnlyLedgerView(source)
	if _, exposed := view.(LedgerView); exposed {
		t.Fatal("read-only adapter exposes ledger mutation methods")
	}
	if got := NewReadOnlyLedgerView(view); got != view {
		t.Fatal("wrapping an adapter created a second adapter")
	}
	if view.Rules() != rules {
		t.Fatal("read-only adapter did not delegate rules")
	}

	source.readData = []byte{1, 2, 3}
	data, err := view.Read(keylet.Child([32]byte{1}))
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	data[0] = 9
	if source.readData[0] != 1 {
		t.Fatal("Read() returned an aliased slice")
	}

	source.readData = nil
	data, err = view.Read(keylet.Child([32]byte{2}))
	if err != nil || data != nil {
		t.Fatalf("Read() nil result = (%v, %v), want (nil, nil)", data, err)
	}
	source.readData = []byte{}
	data, err = view.Read(keylet.Child([32]byte{3}))
	if err != nil || data == nil || len(data) != 0 {
		t.Fatalf("Read() empty result = (%v, %v), want non-nil empty data", data, err)
	}

	readErr := errors.New("read failed")
	source.readData = []byte{4}
	source.readErr = readErr
	data, err = view.Read(keylet.Child([32]byte{4}))
	if !errors.Is(err, readErr) || !bytes.Equal(data, []byte{4}) {
		t.Fatalf("Read() with error = (%v, %v), want cloned data and original error", data, err)
	}
}

func TestReadOnlyLedgerViewClonesIterationResults(t *testing.T) {
	source := &readOnlyViewSource{
		forEachKey:  [32]byte{1},
		forEachData: []byte{2, 3},
		succKey:     [32]byte{4},
		succData:    []byte{5, 6},
		succFound:   true,
	}
	view := NewReadOnlyLedgerView(source)

	if err := view.ForEach(func(key [32]byte, data []byte) bool {
		if key != source.forEachKey {
			t.Fatalf("ForEach key = %x, want %x", key, source.forEachKey)
		}
		data[0] = 8
		return false
	}); err != nil {
		t.Fatalf("ForEach() error = %v", err)
	}
	if !source.stopped {
		t.Fatal("ForEach() did not preserve early stop")
	}
	if source.forEachData[0] != 2 {
		t.Fatal("ForEach() returned an aliased slice")
	}

	iterationErr := errors.New("iteration failed")
	source.forEachErr = iterationErr
	if err := view.ForEach(func([32]byte, []byte) bool { return true }); !errors.Is(err, iterationErr) {
		t.Fatalf("ForEach() error = %v, want %v", err, iterationErr)
	}

	key, data, found, err := view.Succ([32]byte{})
	if err != nil || !found || key != source.succKey {
		t.Fatalf("Succ() = (%x, %v, %v), want (%x, data, true)", key, data, err, source.succKey)
	}
	data[0] = 9
	if source.succData[0] != 5 {
		t.Fatal("Succ() returned an aliased slice")
	}

	source.succData = nil
	source.succFound = false
	_, data, found, err = view.Succ([32]byte{})
	if err != nil || found || data != nil {
		t.Fatalf("Succ() missing result = (%v, %v, %v), want (nil, false, nil)", data, found, err)
	}
}

func TestReadOnlyLedgerViewBalanceHook(t *testing.T) {
	account := [20]byte{1}
	id := [24]byte{2}

	withHook := &balanceHookReadOnlyViewSource{
		result: 7,
	}
	view := NewReadOnlyLedgerView(withHook)
	hook, ok := view.(interface {
		BalanceHookMPT([20]byte, [24]byte, int64) int64
	})
	if !ok {
		t.Fatal("read-only adapter does not provide BalanceHookMPT")
	}
	if got := hook.BalanceHookMPT(account, id, 11); got != 18 {
		t.Fatalf("BalanceHookMPT() = %d, want 18", got)
	}
	if !withHook.called || withHook.account != account || withHook.id != id {
		t.Fatal("read-only adapter did not forward BalanceHookMPT arguments")
	}

	withoutHook := NewReadOnlyLedgerView(&readOnlyViewSource{})
	hook = withoutHook.(interface {
		BalanceHookMPT([20]byte, [24]byte, int64) int64
	})
	if got := hook.BalanceHookMPT(account, id, 11); got != 11 {
		t.Fatalf("BalanceHookMPT() without hook = %d, want 11", got)
	}
}
