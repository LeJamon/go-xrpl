package amm

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

func TestCheckAMMPrecisionLoss(t *testing.T) {
	ctx := tx.NumberContextForRules(nil)
	xrp := func(drops int64) tx.Amount { return state.NewXRPAmountFromInt(drops) }

	tests := []struct {
		name  string
		pool1 tx.Amount
		pool2 tx.Amount
		newLP tx.Amount
		want  ter.Result
	}{
		{name: "strong invariant", pool1: xrp(4), pool2: xrp(9), newLP: xrp(6), want: ter.TesSUCCESS},
		{name: "precision loss", pool1: xrp(1), pool2: xrp(1), newLP: xrp(2), want: ter.TecPRECISION_LOSS},
		{name: "zero LP balance", pool1: xrp(1), pool2: xrp(1), newLP: xrp(0), want: ter.TesSUCCESS},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := checkAMMPrecisionLoss(tt.pool1, tt.pool2, tt.newLP, ctx); got != tt.want {
				t.Fatalf("checkAMMPrecisionLoss = %v, want %v", got, tt.want)
			}
		})
	}
}
