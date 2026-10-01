package sign

import (
	"bytes"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/drops"
	txcore "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/keylet"
)

type feeBoundaryView struct {
	key  [32]byte
	data []byte
}

func (v *feeBoundaryView) Read(keylet.Keylet) ([]byte, error) { return v.data, nil }

func (v *feeBoundaryView) Exists(keylet.Keylet) (bool, error) { return true, nil }

func (v *feeBoundaryView) ForEach(fn func([32]byte, []byte) bool) error {
	fn(v.key, v.data)
	return nil
}

func (v *feeBoundaryView) Succ([32]byte) ([32]byte, []byte, bool, error) {
	return v.key, v.data, true, nil
}

func (*feeBoundaryView) TxExists([32]byte) (bool, error) { return false, nil }

func (*feeBoundaryView) Rules() *amendment.Rules { return nil }

func (*feeBoundaryView) LedgerSeq() uint32 { return 0 }

func (*feeBoundaryView) Insert(keylet.Keylet, []byte) error { return nil }

func (*feeBoundaryView) Update(keylet.Keylet, []byte) error { return nil }

func (*feeBoundaryView) Erase(keylet.Keylet) error { return nil }

func (*feeBoundaryView) AdjustDropsDestroyed(drops.XRPAmount) error { return nil }

type customBoundaryFeeTx struct {
	*txcore.BaseTx
	check func(txcore.ReadOnlyLedgerView)
}

func (t *customBoundaryFeeTx) CalculateBaseFee(view txcore.ReadOnlyLedgerView, _ txcore.EngineConfig) (uint64, error) {
	t.check(view)
	return 17, nil
}

type batchBoundaryFeeTx struct {
	*txcore.BaseTx
	check func(txcore.ReadOnlyLedgerView)
}

func (t *batchBoundaryFeeTx) CalculateMinimumFee(view txcore.ReadOnlyLedgerView, _ txcore.EngineConfig) uint64 {
	t.check(view)
	return 19
}

func assertFeeViewIsReadOnly(t *testing.T, view txcore.ReadOnlyLedgerView, source *feeBoundaryView) {
	t.Helper()
	if _, exposed := view.(txcore.LedgerView); exposed {
		t.Fatal("fee callback received a mutable ledger view")
	}

	data, err := view.Read(keylet.Child(source.key))
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	data[0]++

	if err := view.ForEach(func(_ [32]byte, data []byte) bool {
		data[0]++
		return true
	}); err != nil {
		t.Fatalf("ForEach() error = %v", err)
	}

	_, data, found, err := view.Succ([32]byte{})
	if err != nil {
		t.Fatalf("Succ() error = %v", err)
	}
	if !found || len(data) == 0 {
		t.Fatal("Succ() did not return the source entry")
	}
	data[0]++

	if !bytes.Equal(source.data, []byte{1, 2, 3}) {
		t.Fatalf("fee callback mutated source data: %v", source.data)
	}
}

func TestCalculateBaseFeeWrapsCustomCalculatorView(t *testing.T) {
	source := &feeBoundaryView{key: [32]byte{1}, data: []byte{1, 2, 3}}
	txn := &customBoundaryFeeTx{
		BaseTx: txcore.NewBaseTx(txcore.TypeAccountSet, "account"),
		check:  func(view txcore.ReadOnlyLedgerView) { assertFeeViewIsReadOnly(t, view, source) },
	}

	fee, err := CalculateBaseFee(txn, source, txcore.EngineConfig{BaseFee: 10})
	if err != nil {
		t.Fatalf("CalculateBaseFee() error = %v", err)
	}
	if fee != 17 {
		t.Fatalf("CalculateBaseFee() = %d, want 17", fee)
	}
}

func TestCalculateBaseFeeWrapsBatchCalculatorView(t *testing.T) {
	source := &feeBoundaryView{key: [32]byte{1}, data: []byte{1, 2, 3}}
	txn := &batchBoundaryFeeTx{
		BaseTx: txcore.NewBaseTx(txcore.TypeBatch, "account"),
		check:  func(view txcore.ReadOnlyLedgerView) { assertFeeViewIsReadOnly(t, view, source) },
	}

	fee, err := CalculateBaseFee(txn, source, txcore.EngineConfig{BaseFee: 10})
	if err != nil {
		t.Fatalf("CalculateBaseFee() error = %v", err)
	}
	if fee != 19 {
		t.Fatalf("CalculateBaseFee() = %d, want 19", fee)
	}
}

var _ txcore.CustomBaseFeeCalculator = (*customBoundaryFeeTx)(nil)
var _ txcore.BatchFeeCalculator = (*batchBoundaryFeeTx)(nil)
