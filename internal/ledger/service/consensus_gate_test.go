package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestConsensusAdmissionPrecedesQueuedIngressDuringColdApply(t *testing.T) {
	svc, err := New(DefaultConfig())
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)

	cold, family := coldAcceptanceParent(t, svc.GetClosedLedger())
	require.NoError(t, svc.SwitchToPreferredLedger(cold))
	svc.mu.RLock()
	_, err = svc.applyConfigLocked()
	svc.mu.RUnlock()
	require.NoError(t, err)

	ingressBlob, _ := startupPaymentBlob(t, "cold-gate-ingress", 1)
	queuedIngressBlob, _ := startupPaymentBlob(t, "cold-gate-queued-ingress", 1)

	family.armed.Store(true)
	ingressDone := make(chan struct {
		outcomeApplied bool
		err            error
	}, 1)
	go func() {
		outcome, submitErr := svc.SubmitOpenLedgerTxDetailed(ingressBlob, true)
		ingressDone <- struct {
			outcomeApplied bool
			err            error
		}{outcome.Applied, submitErr}
	}()
	select {
	case <-family.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("ingress did not reach the cold open-ledger read")
	}
	require.Eventually(t, func() bool {
		snapshot := svc.openLedgerMu.Snapshot()
		return !snapshot.Held
	}, time.Second, time.Millisecond)

	queuedIngressDone := make(chan error, 1)
	go func() {
		_, submitErr := svc.SubmitOpenLedgerTxDetailed(queuedIngressBlob, true)
		queuedIngressDone <- submitErr
	}()
	require.Eventually(t, func() bool {
		svc.detachedIngress.mu.Lock()
		defer svc.detachedIngress.mu.Unlock()
		return len(svc.detachedIngress.waiters) == 1
	}, time.Second, time.Millisecond)

	consensusDone := make(chan error, 1)
	go func() {
		_, acceptErr := svc.AcceptConsensusResult(
			t.Context(), cold, nil, nil, cold.CloseTime().Add(10*time.Second), true,
		)
		consensusDone <- acceptErr
	}()
	select {
	case <-ingressDone:
		t.Fatal("detached ingress completed before its storage barrier was released")
	default:
	}

	readDone := make(chan struct{})
	go func() {
		if svc.GetClosedLedger() == nil || svc.GetValidatedLedger() == nil {
			return
		}
		close(readDone)
	}()
	select {
	case <-readDone:
	case <-time.After(time.Second):
		t.Fatal("closed-ledger reads blocked behind cold ingress apply")
	}

	family.unblock()
	ingress := <-ingressDone
	require.NoError(t, ingress.err)
	require.True(t, ingress.outcomeApplied)
	select {
	case consensusErr := <-consensusDone:
		require.NoError(t, consensusErr)
	case <-time.After(5 * time.Second):
		t.Fatal("consensus did not complete after ingress released")
	}

	readDone = make(chan struct{})
	go func() {
		if svc.GetClosedLedger() == nil || svc.GetValidatedLedger() == nil {
			return
		}
		close(readDone)
	}()
	select {
	case <-readDone:
	case <-time.After(time.Second):
		t.Fatal("closed-ledger reads blocked behind post-publication relay")
	}

	select {
	case err := <-queuedIngressDone:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("queued ingress remained parked after consensus publication")
	}
}

func TestStopRejectsQueuedConsensusAndIngressDuringColdApply(t *testing.T) {
	svc, err := New(DefaultConfig())
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)

	cold, family := coldAcceptanceParent(t, svc.GetClosedLedger())
	require.NoError(t, svc.SwitchToPreferredLedger(cold))
	svc.mu.RLock()
	_, err = svc.applyConfigLocked()
	svc.mu.RUnlock()
	require.NoError(t, err)

	ingressBlob, _ := startupPaymentBlob(t, "cold-stop-ingress", 1)
	queuedIngressBlob, _ := startupPaymentBlob(t, "cold-stop-queued-ingress", 1)
	family.armed.Store(true)
	ingressDone := make(chan struct {
		outcomeApplied bool
		err            error
	}, 1)
	go func() {
		outcome, submitErr := svc.SubmitOpenLedgerTxDetailed(ingressBlob, true)
		ingressDone <- struct {
			outcomeApplied bool
			err            error
		}{outcome.Applied, submitErr}
	}()
	select {
	case <-family.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("ingress did not reach the cold open-ledger read")
	}
	require.Eventually(t, func() bool {
		snapshot := svc.openLedgerMu.Snapshot()
		return !snapshot.Held
	}, time.Second, time.Millisecond)

	consensusDone := make(chan error, 1)
	go func() {
		_, acceptErr := svc.AcceptConsensusResult(
			t.Context(), cold, nil, nil, cold.CloseTime().Add(10*time.Second), true,
		)
		consensusDone <- acceptErr
	}()
	require.Eventually(t, func() bool {
		svc.detachedIngress.mu.Lock()
		defer svc.detachedIngress.mu.Unlock()
		return len(svc.detachedIngress.waiters) == 0
	}, time.Second, time.Millisecond)

	queuedIngressDone := make(chan error, 1)
	go func() {
		_, submitErr := svc.SubmitOpenLedgerTxDetailed(queuedIngressBlob, true)
		queuedIngressDone <- submitErr
	}()
	require.Eventually(t, func() bool {
		svc.detachedIngress.mu.Lock()
		defer svc.detachedIngress.mu.Unlock()
		return len(svc.detachedIngress.waiters) == 1
	}, time.Second, time.Millisecond)

	stopDone := make(chan struct{})
	go func() {
		svc.Stop()
		close(stopDone)
	}()
	require.Eventually(t, func() bool {
		svc.lifecycleMu.Lock()
		state := svc.lifecycleState
		svc.lifecycleMu.Unlock()
		return state == serviceStopping
	}, time.Second, time.Millisecond)
	select {
	case <-stopDone:
		t.Fatal("Stop returned while cold ingress and queued lifecycle work were active")
	default:
	}

	readDone := make(chan struct{})
	go func() {
		if svc.GetClosedLedger() == nil || svc.GetValidatedLedger() == nil {
			return
		}
		close(readDone)
	}()
	select {
	case <-readDone:
	case <-time.After(time.Second):
		t.Fatal("closed-ledger reads blocked behind cold ingress apply during Stop")
	}

	family.unblock()
	ingress := <-ingressDone
	require.ErrorIs(t, ingress.err, errServiceNotRunning)
	require.False(t, ingress.outcomeApplied)
	select {
	case consensusErr := <-consensusDone:
		if consensusErr != nil {
			require.ErrorIs(t, consensusErr, errServiceNotRunning)
		}
	case <-time.After(time.Second):
		t.Fatal("consensus acceptance did not drain during Stop")
	}
	require.ErrorIs(t, <-queuedIngressDone, errServiceNotRunning)
	select {
	case <-stopDone:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not drain queued lifecycle work")
	}
}
