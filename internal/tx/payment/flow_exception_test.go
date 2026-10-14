package payment

import (
	"errors"
	"maps"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

// Inject at the ledger boundary so the public RippleCalculate entry point
// exercises both strand construction and flow execution, not a test-only
// exception wrapper. The native-range case uses the actual amount arithmetic.
type paymentFlowExceptionView struct {
	*paymentMockLedgerView
	stage string
	raise func()
	calls int
}

func (v *paymentFlowExceptionView) Read(k keylet.Keylet) ([]byte, error) {
	if v.stage == "strands" {
		v.calls++
		v.raise()
	}
	return v.paymentMockLedgerView.Read(k)
}

func (v *paymentFlowExceptionView) Succ(k [32]byte) ([32]byte, []byte, bool, error) {
	if v.stage == "flow" {
		v.calls++
		v.raise()
	}
	return v.paymentMockLedgerView.Succ(k)
}

// Testnet ledger 21485530, tx 1E2B5609...F505A, contains a partial XRP->USD
// Payment whose amount-range exception is tecINTERNAL in rippled 3.4.1.
// Letting it escape to Engine.invokeApplySafely instead produced tefEXCEPTION
// and latched an execution-disagreement fault. This is a boundary regression,
// not a reconstruction of that ledger's full book/AMM state.
func TestRippleCalculateFlowExceptions(t *testing.T) {
	tests := []struct {
		name  string
		raise func()
	}{
		{"native range", func() { state.CanonicalizeDrops(100_000_000_000_000_001, 0) }},
		{"IOU overflow", func() { panic("IOUAmount overflow") }},
		{"number overflow", func() { panic(errors.New("XRPLNumber::normalize overflow")) }},
		{"division by zero", func() { panic("division by zero") }},
	}
	for _, test := range tests {
		for _, stage := range []string{"strands", "flow"} {
			t.Run(test.name+"/"+stage, func(t *testing.T) {
				view, run := exceptionPayment(t, stage, test.raise)
				before := maps.Clone(view.data)
				var result RippleCalculateResult
				require.NotPanics(t, func() { result = run() })
				require.Positive(t, view.calls, "the exception must actually be exercised")
				require.Equal(t, ter.TecINTERNAL, result.Result)
				require.True(t, result.ActualIn.IsZero())
				require.True(t, result.ActualOut.IsZero())
				require.Nil(t, result.Sandbox, "failed flow must not expose a committable sandbox")
				require.Empty(t, result.RemovableOffers, "failed flow must not commit offer grooming")
				require.Equal(t, before, view.data, "failed flow must not write the base view")
			})
		}
	}
}

func TestRippleCalculateDoesNotHideUnexpectedPanics(t *testing.T) {
	for _, value := range []any{"unrelated bug", "storage cache overflow", errors.New("storage read failed"), flowError{ter: ter.TefINTERNAL}, 42} {
		_, run := exceptionPayment(t, "strands", func() { panic(value) })
		require.PanicsWithValue(t, value, func() { run() })
	}
	_, run := exceptionPayment(t, "flow", func() {
		var values []int
		_ = values[1] // A Go runtime bounds panic is not a protocol exception.
	})
	require.Panics(t, func() { run() })
}

func exceptionPayment(t *testing.T, stage string, raise func()) (*paymentFlowExceptionView, func() RippleCalculateResult) {
	t.Helper()
	src, dst, issuer := [20]byte{1}, [20]byte{2}, [20]byte{3}
	view := &paymentFlowExceptionView{paymentMockLedgerView: newPaymentMockLedgerView(), stage: stage, raise: raise}
	for _, account := range [][20]byte{src, dst, issuer} {
		view.createAccount(account, 100_000_000, 0)
	}
	view.createTrustLine(dst, issuer, "USD", 0, 100, 100)
	return view, func() RippleCalculateResult {
		sendMax := tx.NewXRPAmount(90_000_000)
		return RippleCalculate(view, src, dst,
			tx.NewIssuedAmount(20, 0, "USD", state.EncodeAccountIDSafe(issuer)),
			&sendMax, nil, true, true, false, [32]byte{1}, 21485530)
	}
}
