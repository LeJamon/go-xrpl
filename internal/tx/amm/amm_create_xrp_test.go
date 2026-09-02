package amm

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/stretchr/testify/require"
)

func TestTransferCreateXRPBalanceBoundary(t *testing.T) {
	tests := []struct {
		name            string
		balance         uint64
		open            bool
		wantResult      ter.Result
		wantSource      uint64
		wantDestination uint64
	}{
		{name: "closed_short", balance: 99, wantResult: ter.TecFAILED_PROCESSING, wantSource: 99},
		{name: "open_short", balance: 99, open: true, wantResult: ter.TelFAILED_PROCESSING, wantSource: 99},
		{name: "exact", balance: 100, wantResult: ter.TesSUCCESS, wantDestination: 100},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source := &state.AccountRoot{Balance: test.balance}
			destination := &state.AccountRoot{}
			ctx := &tx.ApplyContext{
				Account: source,
				Config:  tx.EngineConfig{ViewOpen: test.open},
			}

			result := transferCreateXRP(ctx, destination, tx.NewXRPAmount(100))

			require.Equal(t, test.wantResult, result)
			require.Equal(t, test.wantSource, source.Balance)
			require.Equal(t, test.wantDestination, destination.Balance)
		})
	}
}
