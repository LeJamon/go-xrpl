package jtx

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/tx"
)

func TestFeeMetricTransactionsGateBatchInnersOnOuterSuccess(t *testing.T) {
	inner1 := tx.NewBaseTx(tx.TypePayment, "rInner1")
	inner2 := tx.NewBaseTx(tx.TypePayment, "rInner2")
	batch := tx.NewBaseTx(tx.TypeBatch, "rOuter")
	inners := []tx.AppliedInnerTransaction{
		{Transaction: inner1},
		{Transaction: inner2},
	}

	if got := feeMetricTransactions(batch, nil); len(got) != 1 || got[0] != batch {
		t.Fatalf("outer tec entries = %v, want outer only", got)
	}
	got := feeMetricTransactions(batch, inners)
	if len(got) != 3 || got[0] != batch || got[1] != inner1 || got[2] != inner2 {
		t.Fatalf("outer tes entries = %v, want outer and committed inners", got)
	}
}
