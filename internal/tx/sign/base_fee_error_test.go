package sign

import (
	"testing"

	txcore "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

type panicBaseFeeTx struct {
	*txcore.BaseTx
}

func (panicBaseFeeTx) CalculateBaseFee(txcore.LedgerView, txcore.EngineConfig) uint64 {
	panic("controlled base-fee failure")
}

func TestCalculateBaseFeePanicReturnsTypedTefException(t *testing.T) {
	txn := &panicBaseFeeTx{BaseTx: txcore.NewBaseTx(txcore.TypeAccountSet, "account")}

	fee, err := CalculateBaseFee(txn, nil, txcore.EngineConfig{BaseFee: 10})

	if fee != 0 {
		t.Fatalf("fee = %d, want 0 on calculator panic", fee)
	}
	resultErr, ok := ter.AsResultError(err)
	if !ok {
		t.Fatalf("error = %T (%v), want *ter.ResultError", err, err)
	}
	if resultErr.Code != ter.TefEXCEPTION {
		t.Fatalf("error code = %s, want tefEXCEPTION", resultErr.Code)
	}
}

var _ txcore.CustomBaseFeeCalculator = (*panicBaseFeeTx)(nil)
