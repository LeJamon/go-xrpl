package txq

import (
	"context"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/stretchr/testify/require"
)

func TestAcceptContextCancelsAtTransactionBoundary(t *testing.T) {
	q := mustNew(makeAdmissionConfig())
	account := [20]byte{9}
	aq := NewAccountQueue(account)
	q.byAccount[account] = aq
	addQueued(q, aq, 1, 2)
	addQueued(q, aq, 2, 3)

	cancelCtx, cancel := context.WithCancel(context.Background())
	applyCalls := 0
	acceptCtx := &stubApplyCtx{
		applyFn: func(tx.Transaction) (ter.Result, bool) {
			applyCalls++
			cancel()
			return ter.TesSUCCESS, true
		},
	}

	changed := q.AcceptContext(cancelCtx, acceptCtx)

	require.False(t, changed)
	require.Equal(t, 1, applyCalls)
	// The private view may have applied the first candidate, but cancellation
	// happens before queue ownership is advanced. The caller discards both
	// private objects and retries from this unchanged queue snapshot.
	require.Equal(t, 2, q.Size())
}

func TestAcceptContextAlreadyCanceledDoesNotTouchQueue(t *testing.T) {
	q := mustNew(makeAdmissionConfig())
	account := [20]byte{9}
	aq := NewAccountQueue(account)
	q.byAccount[account] = aq
	addQueued(q, aq, 1, 2)

	cancelCtx, cancel := context.WithCancel(context.Background())
	cancel()
	acceptCtx := &stubApplyCtx{applyRes: ter.TesSUCCESS, applied: true}

	require.False(t, q.AcceptContext(cancelCtx, acceptCtx))
	require.Equal(t, 1, q.Size())
}

func TestAcceptContextWithObserverReportsOneSlowTransaction(t *testing.T) {
	q := mustNew(makeAdmissionConfig())
	account := [20]byte{9}
	aq := NewAccountQueue(account)
	q.byAccount[account] = aq
	addQueued(q, aq, 1, 2)
	addQueued(q, aq, 2, 3)

	var diagnostics []SlowTransactionDiagnostic
	acceptCtx := &stubApplyCtx{applyRes: ter.TesSUCCESS, applied: true}
	changed := q.AcceptContextWithObserver(
		context.Background(),
		acceptCtx,
		time.Nanosecond,
		func(d SlowTransactionDiagnostic) { diagnostics = append(diagnostics, d) },
	)

	require.True(t, changed)
	require.Len(t, diagnostics, 1)
	require.Equal(t, tx.TypeAccountSet, diagnostics[0].Type)
	require.Equal(t, "queue", diagnostics[0].Phase)
	require.Zero(t, diagnostics[0].Pass)
	require.Positive(t, diagnostics[0].Duration)
}
