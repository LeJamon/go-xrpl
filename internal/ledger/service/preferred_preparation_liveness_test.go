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

func TestPreferredAdmissionDoesNotOwnTransition(t *testing.T) {
	svc, err := New(DefaultConfig())
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)

	preferred, family := coldAcceptanceParent(t, svc.GetClosedLedger())
	family.armed.Store(true)
	type result struct {
		prepared *PreparedPreferredLedger
		err      error
	}
	done := make(chan result, 1)
	go func() {
		prepared, err := svc.PreparePreferredLedger(t.Context(), preferred)
		done <- result{prepared: prepared, err: err}
	}()
	awaitPreferredPreparation(t, family.entered)
	require.False(t, svc.preferredTransitionIsActive(), "state admission must not park ingress behind preferred ownership")
	require.False(t, svc.openLedgerMu.Snapshot().Held, "state admission must not hold the service gate")

	family.unblock()
	preparedResult := <-done
	require.NoError(t, preparedResult.err)
	prepared := preparedResult.prepared
	require.NotNil(t, prepared)
	t.Cleanup(prepared.Release)
	require.NoError(t, prepared.Wait(t.Context()))
}

func TestPreferredAdmissionRejectsConcurrentConsensusAcceptance(t *testing.T) {
	svc, err := New(DefaultConfig())
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)
	parent := svc.GetClosedLedger()
	preferred, family := coldAcceptanceParent(t, parent)
	family.armed.Store(true)
	switched := make(chan error, 1)
	go func() { switched <- svc.SwitchToPreferredLedger(preferred) }()
	awaitPreferredPreparation(t, family.entered)

	accepted := make(chan error, 1)
	go func() {
		_, err := svc.AcceptConsensusResult(t.Context(), parent, nil, nil, parent.CloseTime().Add(time.Second), true)
		accepted <- err
	}()
	select {
	case err := <-accepted:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("preferred admission blocked consensus acceptance")
	}
	closed, open := svc.GetClosedLedger(), svc.GetOpenLedger()
	require.NotEqual(t, preferred.Hash(), closed.Hash())
	family.unblock()
	require.ErrorIs(t, <-switched, ErrPreferredTransitionStale)
	require.Same(t, closed, svc.GetClosedLedger())
	require.Same(t, open, svc.GetOpenLedger())
	require.False(t, svc.preferredTransitionIsActive())
}

func TestPreferredAdmissionRejectsConcurrentTransitionOwnership(t *testing.T) {
	svc, err := New(DefaultConfig())
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)
	preferred, family := coldAcceptanceParent(t, svc.GetClosedLedger())
	family.armed.Store(true)
	done := make(chan error, 1)
	go func() {
		prepared, err := svc.PreparePreferredLedger(t.Context(), preferred)
		if err == nil {
			prepared.Release()
		}
		done <- err
	}()
	awaitPreferredPreparation(t, family.entered)
	owner, err := svc.PreparePreferredLedger(t.Context(), svc.GetClosedLedger())
	require.NoError(t, err)
	t.Cleanup(owner.Release)
	require.NoError(t, owner.Wait(t.Context()))
	family.unblock()
	select {
	case err := <-done:
		require.ErrorIs(t, err, ErrPreferredTransitionStale)
	case <-time.After(5 * time.Second):
		t.Fatal("admission retried a superseded target behind the new transition owner")
	}
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

func TestPreferredAdmissionCancellationReleasesLifecycleAdmission(t *testing.T) {
	svc, err := New(DefaultConfig())
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)

	preferred, family := coldAcceptanceParent(t, svc.GetClosedLedger())
	family.armed.Store(true)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		prepared, err := svc.PreparePreferredLedger(ctx, preferred)
		if err == nil {
			prepared.Release()
		}
		done <- err
	}()
	awaitPreferredPreparation(t, family.entered)
	cancel()
	family.unblock()
	require.ErrorIs(t, <-done, context.Canceled)
	require.False(t, svc.preferredTransitionIsActive())
	require.False(t, svc.openLedgerMu.Snapshot().Held)
}

func TestPreferredAdmissionStopCancelsBeforeOwnership(t *testing.T) {
	svc, err := New(DefaultConfig())
	require.NoError(t, err)
	require.NoError(t, svc.Start())

	preferred, family := coldAcceptanceParent(t, svc.GetClosedLedger())
	family.armed.Store(true)
	done := make(chan error, 1)
	go func() {
		prepared, err := svc.PreparePreferredLedger(context.Background(), preferred)
		if err == nil {
			prepared.Release()
		}
		done <- err
	}()
	awaitPreferredPreparation(t, family.entered)
	stopped := make(chan struct{})
	go func() {
		svc.Stop()
		close(stopped)
	}()
	select {
	case err := <-done:
		require.ErrorIs(t, err, errServiceNotRunning)
	case <-time.After(5 * time.Second):
		family.unblock()
		t.Fatal("preferred admission did not observe service shutdown")
	}
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not drain preferred admission")
	}
	require.False(t, svc.preferredTransitionIsActive())
	require.False(t, svc.openLedgerMu.Snapshot().Held)
}

func TestStoredPromotionStopCancelsDetachedAdmission(t *testing.T) {
	svc, err := New(DefaultConfig())
	require.NoError(t, err)
	require.NoError(t, svc.Start())

	preferred, family := coldAcceptanceParent(t, svc.GetClosedLedger())
	svc.mu.Lock()
	svc.retainValidationCandidateLocked(preferred)
	svc.mu.Unlock()
	family.armed.Store(true)
	promotionDone := make(chan struct{})
	go func() {
		svc.PromoteStoredValidatedLedgerAt(preferred.Sequence(), preferred.Hash(), time.Time{})
		close(promotionDone)
	}()
	awaitPreferredPreparation(t, family.entered)

	stopped := make(chan struct{})
	go func() {
		svc.Stop()
		close(stopped)
	}()
	select {
	case <-promotionDone:
	case <-time.After(5 * time.Second):
		family.unblock()
		t.Fatal("stored promotion did not observe service shutdown")
	}
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not drain stored promotion")
	}
	require.False(t, svc.preferredTransitionIsActive())
	require.False(t, svc.openLedgerMu.Snapshot().Held)
}

func TestStoredPromotionCallerCancellationStopsDetachedAdmission(t *testing.T) {
	svc, err := New(DefaultConfig())
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)

	preferred, family := coldAcceptanceParent(t, svc.GetClosedLedger())
	svc.mu.Lock()
	svc.retainValidationCandidateLocked(preferred)
	svc.mu.Unlock()
	family.armed.Store(true)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	promotionDone := make(chan struct{})
	go func() {
		svc.PromoteStoredValidatedLedgerAtContext(ctx, preferred.Sequence(), preferred.Hash(), time.Time{})
		close(promotionDone)
	}()
	awaitPreferredPreparation(t, family.entered)
	cancel()
	select {
	case <-promotionDone:
	case <-time.After(5 * time.Second):
		family.unblock()
		t.Fatal("stored promotion did not observe caller cancellation")
	}
	require.False(t, svc.preferredTransitionIsActive())
	require.False(t, svc.openLedgerMu.Snapshot().Held)
}

func TestPreferredPreparationStopKeepsPublishedState(t *testing.T) {
	for _, phase := range []string{"preparing", "ready"} {
		t.Run(phase, func(t *testing.T) {
			svc, preferred, family := preferredPreparationFixture(t)
			closed, open, queue := svc.GetClosedLedger(), svc.openLedgerView.Current(), svc.txQueue
			family.armed.Store(phase == "preparing")
			prepared, err := svc.PreparePreferredLedger(t.Context(), preferred)
			require.NoError(t, err)
			t.Cleanup(func() {
				family.unblock()
				prepared.Release()
			})
			if phase == "preparing" {
				awaitPreferredPreparation(t, family.entered)
			} else {
				require.NoError(t, prepared.Wait(t.Context()))
			}

			stopped := make(chan struct{})
			go func() {
				svc.Stop()
				close(stopped)
			}()
			select {
			case <-prepared.ctx.Done():
			case <-time.After(5 * time.Second):
				t.Fatal("Stop did not cancel successor preparation")
			}
			family.unblock()
			if phase == "preparing" {
				require.ErrorIs(t, prepared.Wait(t.Context()), errServiceNotRunning)
			}
			published, err := prepared.TryPublish()
			require.False(t, published)
			require.ErrorIs(t, err, errServiceNotRunning)
			prepared.Release()
			select {
			case <-stopped:
			case <-time.After(5 * time.Second):
				t.Fatal("Stop did not drain successor preparation")
			}
			require.Same(t, closed, svc.GetClosedLedger())
			require.Same(t, open, svc.openLedgerView.Current())
			require.Same(t, queue, svc.txQueue)
			require.False(t, svc.openLedgerMu.Snapshot().Held)
		})
	}
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
	preferred, family := coldAcceptanceParent(t, svc.GetClosedLedger())
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
