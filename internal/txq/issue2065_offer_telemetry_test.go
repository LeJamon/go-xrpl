package txq

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

type issue2065DiagnosticApplyContext struct {
	stubApplyCtx
	counter    *uint32
	bindCalls  int
	offerSteps uint32
}

func (c *issue2065DiagnosticApplyContext) SetOffersConsideredCounter(counter *uint32) {
	c.counter = counter
	c.bindCalls++
}

func (c *issue2065DiagnosticApplyContext) ApplyTransactionWithFlags(tx.Transaction, tx.ApplyFlags) (ter.Result, bool) {
	if c.counter != nil {
		*c.counter = c.offerSteps
	}
	return ter.TesSUCCESS, true
}

func TestAcceptContextWithObserverReportsOfferStepsAndKeepsDisabledPathIdentical(t *testing.T) {
	newQueue := func() *TxQ {
		q := mustNew(DefaultConfig())
		account := [20]byte{9}
		aq := NewAccountQueue(account)
		q.byAccount[account] = aq
		addQueued(q, aq, 1, 2)
		return q
	}

	withObserver := newQueue()
	withContext := &issue2065DiagnosticApplyContext{
		stubApplyCtx: stubApplyCtx{},
		offerSteps:   18,
	}
	var diagnostic SlowTransactionDiagnostic
	var callbacks int
	accepted := withObserver.AcceptContextWithObserver(
		context.Background(),
		withContext,
		time.Nanosecond,
		func(observed SlowTransactionDiagnostic) {
			callbacks++
			diagnostic = observed
		},
	)

	require.True(t, accepted)
	require.Equal(t, 1, callbacks)
	require.Equal(t, uint32(18), diagnostic.OffersConsidered)
	require.Equal(t, tx.TypeAccountSet, diagnostic.Type)
	require.Equal(t, "queue", diagnostic.Phase)
	require.Equal(t, 0, diagnostic.Pass)
	require.Equal(t, 0, withObserver.Size())

	disabled := newQueue()
	disabledContext := &issue2065DiagnosticApplyContext{
		stubApplyCtx: stubApplyCtx{},
		offerSteps:   18,
	}
	var disabledCallbacks int
	disabledAccepted := disabled.AcceptContextWithObserver(
		context.Background(),
		disabledContext,
		0,
		func(SlowTransactionDiagnostic) { disabledCallbacks++ },
	)

	require.True(t, disabledAccepted)
	require.Equal(t, 0, disabledCallbacks)
	require.Equal(t, 0, disabledContext.bindCalls)
	require.Equal(t, 0, disabled.Size())
}
