package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/stretchr/testify/require"
)

func TestPreferredPreparationKeepsReadersResponsiveAndRetainsIngress(t *testing.T) {
	svc, preferred, family := preferredPreparationFixture(t)
	oldClosed := svc.GetClosedLedger()
	env := jtx.NewTestEnv(t)
	destination := jtx.NewAccount("preferred-preparation-ingress")
	prior, priorHash := preferredSwitchPaymentBlob(t, env, jtx.MasterAccount(), destination, 20_000_000, 100, 1)
	late, lateHash := preferredSwitchPaymentBlob(t, env, jtx.MasterAccount(), destination, 1_000_000, 100, 2)
	result, err := svc.SubmitOpenLedgerTxDetailed(prior, true)
	require.NoError(t, err)
	require.Equal(t, ter.TesSUCCESS, result.Result)
	family.armed.Store(true)
	switched := make(chan error, 1)
	go func() { switched <- svc.SwitchToPreferredLedger(preferred) }()
	awaitPreferredPreparation(t, family.entered)

	reads := make(chan *ledger.Ledger, 1)
	go func() {
		_, _ = svc.GetLedgerBySequence(oldClosed.Sequence())
		_ = svc.TxQMetrics()
		reads <- svc.GetClosedLedger()
	}()
	select {
	case current := <-reads:
		require.Same(t, oldClosed, current)
	case <-time.After(time.Second):
		t.Fatal("preferred successor preparation held service/history locks")
	}
	require.False(t, svc.openLedgerMu.Snapshot().Held, "successor preparation retained the service gate")
	submitted := make(chan error, 1)
	go func() {
		result, err := svc.SubmitOpenLedgerTxDetailed(late, true)
		if err == nil && result.Result != ter.TesSUCCESS {
			err = fmt.Errorf("late submission returned %s", result.Result)
		}
		submitted <- err
	}()
	family.unblock()
	require.NoError(t, <-switched)
	require.NoError(t, <-submitted)
	require.Same(t, preferred, svc.GetClosedLedger())
	for _, hash := range [][32]byte{priorHash, lateHash} {
		present, err := svc.openLedgerView.Current().TxExists(hash)
		require.NoError(t, err)
		require.True(t, present)
	}
	require.Equal(t, uint32(2), svc.openLedgerView.Current().TxCount())
}

func TestPreferredPreparationCancellationKeepsPublishedState(t *testing.T) {
	svc, preferred, family := preferredPreparationFixture(t)
	closed, open, queue := svc.GetClosedLedger(), svc.openLedgerView.Current(), svc.txQueue
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	family.armed.Store(true)
	done := make(chan error, 1)
	go func() {
		prepared, err := svc.PreparePreferredLedger(ctx, preferred)
		if err == nil {
			defer prepared.Release()
			err = prepared.Wait(ctx)
			if err == nil {
				_, err = prepared.TryPublish()
			}
		}
		done <- err
	}()
	awaitPreferredPreparation(t, family.entered)
	cancel()
	family.unblock()
	require.ErrorIs(t, <-done, context.Canceled)
	require.Same(t, closed, svc.GetClosedLedger())
	require.Same(t, open, svc.openLedgerView.Current())
	require.Same(t, queue, svc.txQueue)
	require.False(t, svc.openLedgerMu.Snapshot().Held)
}

func TestPreferredPreparationRejectsChangedOwnership(t *testing.T) {
	for _, name := range []string{"closed", "open", "queue", "validated"} {
		t.Run(name, func(t *testing.T) {
			svc, preferred, _ := preferredPreparationFixture(t)
			prepared, err := svc.PreparePreferredLedger(t.Context(), preferred)
			require.NoError(t, err)
			defer prepared.Release()
			require.NoError(t, prepared.Wait(t.Context()))
			svc.mu.Lock()
			switch name {
			case "closed":
				svc.closedLedger = preferred
			case "open":
				svc.openLedger, err = ledger.NewOpen(preferred, time.Now())
			case "queue":
				svc.txQueue, err = svc.txQueue.Fork()
			case "validated":
				svc.validatedLedger = svc.closedLedger
			}
			closed, open, queue := svc.closedLedger, svc.openLedger, svc.txQueue
			svc.mu.Unlock()
			require.NoError(t, err)
			published, err := prepared.TryPublish()
			require.False(t, published)
			require.ErrorIs(t, err, ErrPreferredTransitionStale)
			require.Same(t, closed, svc.GetClosedLedger())
			require.Same(t, open, svc.openLedger)
			require.Same(t, queue, svc.txQueue)
		})
	}
}

func preferredPreparationFixture(t *testing.T) (*Service, *ledger.Ledger, *acceptanceBlockingFamily) {
	t.Helper()
	svc, err := New(DefaultConfig())
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)
	preferred, family := coldAcceptanceParent(t, svc.GetClosedLedger(), svc.shamapFamily)
	t.Cleanup(svc.Stop)
	require.NoError(t, preferred.SetValidated())
	// The fixture's complete backing store was copied from a real ledger. Keep
	// it service-owned so the barrier exercises successor work, not admission.
	svc.mu.Lock()
	svc.historyComponent.mu.Lock()
	svc.putHistoryLocked(preferred)
	svc.validatedLedger = preferred
	svc.historyComponent.mu.Unlock()
	svc.mu.Unlock()
	return svc, preferred, family
}

func awaitPreferredPreparation(t *testing.T, entered <-chan struct{}) {
	t.Helper()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("preferred preparation did not reach the storage barrier")
	}
}
