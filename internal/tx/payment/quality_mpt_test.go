package payment

import (
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/stretchr/testify/require"
)

type qualityFunctionStep struct {
	fakeStep
	qf *QualityFunction
}

func (s *qualityFunctionStep) GetQualityFunc(*PaymentSandbox, DebtDirection) (*QualityFunction, DebtDirection) {
	return s.qf, DebtDirectionIssues
}

func TestQualityMPTCeilUsesIntegralRounding(t *testing.T) {
	var id [24]byte
	id[23] = 1

	in := NewMPTEitherAmount(5, id)
	out := NewMPTEitherAmount(10, id)
	q := QualityFromAmounts(in, out)
	roundedIn, limitedOut := q.CeilOutStrict(in, out, NewMPTEitherAmount(5, id), true)
	require.Equal(t, int64(3), roundedIn.MPT)
	require.Equal(t, int64(5), limitedOut.MPT)
	require.Equal(t, id, roundedIn.MPTID)
	roundedIn, _ = q.CeilOutStrict(in, out, NewMPTEitherAmount(5, id), false)
	require.Equal(t, int64(2), roundedIn.MPT)

	in = NewMPTEitherAmount(10, id)
	out = NewMPTEitherAmount(5, id)
	q = QualityFromAmounts(in, out)
	limitedIn, roundedOut := q.CeilIn(in, out, NewMPTEitherAmount(5, id))
	require.Equal(t, int64(5), limitedIn.MPT)
	require.Equal(t, int64(3), roundedOut.MPT)
	require.Equal(t, id, roundedOut.MPTID)
	_, roundedOut = q.CeilInStrict(in, out, NewMPTEitherAmount(5, id), false)
	require.Equal(t, int64(2), roundedOut.MPT)
}

func TestLimitOutIntegralRoundingHonorsAverageQuality(t *testing.T) {
	qf := NewAMMQualityFunction(tx.NewXRPAmount(10), tx.NewXRPAmount(10), 0)
	require.NotNil(t, qf)
	limitQuality := QualityFromAmounts(NewXRPEitherAmount(25), NewXRPEitherAmount(21))
	step := &qualityFunctionStep{qf: qf}

	for _, test := range []struct {
		name    string
		enabled bool
		want    int64
	}{
		{name: "before MPTokensV2", want: 2},
		{name: "with MPTokensV2", enabled: true, want: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			view := newPaymentMockLedgerView()
			rules := amendment.NewRulesBuilder().FromPreset(amendment.PresetAllSupported)
			if test.enabled {
				rules.Enable(amendment.FeatureMPTokensV2)
			}
			view.rules = rules.Build()
			sb := NewPaymentSandbox(view)

			got := limitOut(sb, Strand{step}, NewXRPEitherAmount(10), limitQuality)
			require.Equal(t, test.want, got.XRP)
		})
	}

	// The continuous solution is 1.6 drops. Nearest rounding yields two drops,
	// whose average quality is below the requested 25/21; the amendment therefore
	// selects the downward one-drop result.
	continuous := qf.OutFromAvgQ(limitQuality)
	require.NotNil(t, continuous)
	require.True(t, qf.SatisfiesAvgQ(limitQuality, qf.math.fromAmount(tx.NewXRPAmount(1), state.RoundToNearest)))
	require.False(t, qf.SatisfiesAvgQ(limitQuality, qf.math.fromAmount(tx.NewXRPAmount(2), state.RoundToNearest)))
}
