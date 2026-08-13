package handlers

import (
	"math"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/rpc/types"
)

func TestMulDivSaturating(t *testing.T) {
	tests := []struct {
		a, b, divisor uint64
		want          uint64
	}{
		{math.MaxUint64, 256, 256, math.MaxUint64},
		{math.MaxUint64, math.MaxUint64, 1, math.MaxUint64},
		{257, 256, 256, 257},
		{1, 1, 2, 0},
		{1, 1, 0, math.MaxUint64},
	}
	for _, test := range tests {
		if got := mulDivSaturating(test.a, test.b, test.divisor); got != test.want {
			t.Fatalf("mulDivSaturating(%d, %d, %d) = %d, want %d", test.a, test.b, test.divisor, got, test.want)
		}
	}
}

func TestComputeServerLoadKeepsRawAndScaledEscalationDistinct(t *testing.T) {
	services := &types.ServiceContainer{
		TxQMetrics: func() types.TxQServerMetrics {
			return types.TxQServerMetrics{
				ReferenceFeeLevel:  128,
				OpenLedgerFeeLevel: 256,
			}
		},
	}

	load := ComputeServerLoad(types.NewTestServiceGraph(services))
	if load.LoadFactorFeeEscalation != 256 {
		t.Fatalf("raw fee escalation = %d, want 256", load.LoadFactorFeeEscalation)
	}
	if load.LoadFactor != 512 {
		t.Fatalf("scaled overall load = %d, want 512", load.LoadFactor)
	}
}
