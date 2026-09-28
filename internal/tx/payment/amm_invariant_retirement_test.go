package payment

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	tx "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

func TestAMMInvariantFailureRetainsTERWithoutRetiredAmendment(t *testing.T) {
	rules := amendment.EmptyRules()
	liq := &AMMLiquidity{
		ammContext: NewAMMContext([20]byte{}, false, tx.NumberContextForRules(rules)),
	}
	poolIn := state.NewXRPAmountFromInt(100_000_000)
	poolOut := state.NewIssuedAmountFromValue(100, 0, "USD", "issuer")
	offer := NewAMMOffer(liq, poolIn, poolOut, poolIn, poolOut)
	consumedIn := toEitherAmt(state.NewXRPAmountFromInt(1_000_000))
	consumedOut := toEitherAmt(state.NewIssuedAmountFromValue(10, 0, "USD", "issuer"))

	err := (&BookStep{}).consumeAMMOffer(nil, offer, consumedIn, consumedIn, consumedOut, consumedOut)
	result, ok := ter.AsResultError(err)
	require.True(t, ok, "invariant failure must retain a typed TER: %v", err)
	require.Equal(t, ter.TecINVARIANT_FAILED, result.Code)
	require.False(t, offer.consumed)
	failure := recoverFlowError(t, func() { throwConsumeFailure(err) })
	require.Equal(t, ter.TecINVARIANT_FAILED, failure.ter)
	require.False(t, failure.fatal)
}
