package payment

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFlowOffersConsideredTelemetryAggregatesWithoutChangingResult(t *testing.T) {
	newFlow := func(counter *uint32) FlowResult {
		view := newPaymentMockLedgerView()
		sandbox := NewPaymentSandbox(view)
		strands := []Strand{
			{&fakeStep{
				offersUsed: 7,
				revFn: func(EitherAmount) (EitherAmount, EitherAmount) {
					return ZeroXRPEitherAmount(), ZeroXRPEitherAmount()
				},
			}},
			{&fakeStep{
				offersUsed: 11,
				revFn: func(EitherAmount) (EitherAmount, EitherAmount) {
					return ZeroXRPEitherAmount(), ZeroXRPEitherAmount()
				},
			}},
		}
		var opts []FlowOption
		if counter != nil {
			opts = append(opts, withOffersConsidered(counter))
		}
		return Flow(
			sandbox,
			strands,
			NewXRPEitherAmount(10_000_000),
			false,
			nil,
			nil,
			nil,
			false,
			opts...,
		)
	}

	baseline := newFlow(nil)
	var counted uint32
	observed := newFlow(&counted)

	require.Equal(t, uint32(18), counted)
	require.Equal(t, baseline.Result, observed.Result)
	require.Equal(t, 0, baseline.In.Compare(observed.In))
	require.Equal(t, 0, baseline.Out.Compare(observed.Out))
	require.Equal(t, baseline.RemovableOffers, observed.RemovableOffers)
}
