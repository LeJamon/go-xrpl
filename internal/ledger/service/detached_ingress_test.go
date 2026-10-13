package service

import (
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/openledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/replayfault"
	"github.com/LeJamon/go-xrpl/internal/txq"
	"github.com/stretchr/testify/require"
)

func TestDetachedIngressValidationAdvanceDoesNotRepeatApply(t *testing.T) {
	svc, err := New(DefaultConfig())
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)
	candidate := retainNextValidationCandidate(t, svc)
	blob, _ := startupPaymentBlob(t, "detached-validation", 1)
	pending, err := openledger.ParsePendingTx(blob)
	require.NoError(t, err)
	release, err := svc.detachedIngress.acquire()
	require.NoError(t, err)
	defer release()
	attempts := 0
	outcome, _, _, _, err := svc.submitDetachedIngressOwned(detachedIngressRequest{
		pendingTx: pending,
		prepareCommit: func(*ledger.Ledger, *ledger.Ledger, *txq.TxQ, openledger.ApplyConfig, openledger.SubmitOutcome) detachedIngressCommitData {
			attempts++
			svc.SetValidatedLedger(candidate.Sequence(), candidate.Hash())
			return detachedIngressCommitData{}
		},
	})
	require.NoError(t, err)
	require.True(t, outcome.Applied)
	require.Same(t, candidate, svc.GetValidatedLedger())
	require.Equal(t, 1, attempts, "validation alone must not replay an expensive private transaction")
}

func TestDetachedIngressDoesNotBlockConsensusAndPublishesRetry(t *testing.T) {
	svc, err := New(DefaultConfig())
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)

	cold, family := coldAcceptanceParent(t, svc.GetClosedLedger())
	require.NoError(t, svc.SwitchToPreferredLedger(cold))
	blob, hash := startupPaymentBlob(t, "detached-ingress-retry", 1)
	family.armed.Store(true)

	type ingressResult struct {
		outcome openledger.SubmitOutcome
		err     error
	}
	ingressDone := make(chan ingressResult, 1)
	go func() {
		outcome, err := svc.SubmitOpenLedgerTxDetailed(blob, true)
		ingressDone <- ingressResult{outcome: outcome, err: err}
	}()
	select {
	case <-family.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("ingress did not reach a blocked state read")
	}

	accepted := make(chan error, 1)
	go func() {
		_, err := svc.AcceptConsensusResult(t.Context(), cold, nil, nil, cold.CloseTime().Add(10*time.Second), true)
		accepted <- err
	}()
	select {
	case err := <-accepted:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("consensus acceptance blocked behind detached ingress")
	}

	family.unblock()
	result := <-ingressDone
	require.NoError(t, result.err)
	require.True(t, result.outcome.Applied)
	exists, err := svc.openLedgerView.Current().TxExists(hash)
	require.NoError(t, err)
	require.True(t, exists, "detached ingress outcome was lost after the frontier advanced")
}

func TestDetachedIngressStopDrainsBlockedApply(t *testing.T) {
	svc, err := New(DefaultConfig())
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)

	cold, family := coldAcceptanceParent(t, svc.GetClosedLedger())
	require.NoError(t, svc.SwitchToPreferredLedger(cold))
	blob, _ := startupPaymentBlob(t, "detached-ingress-stop", 1)
	family.armed.Store(true)
	submitted := make(chan error, 1)
	go func() {
		_, err := svc.SubmitOpenLedgerTxDetailed(blob, true)
		submitted <- err
	}()
	select {
	case <-family.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("ingress did not reach a blocked state read")
	}

	stopped := make(chan struct{})
	go func() {
		svc.Stop()
		close(stopped)
	}()
	require.Eventually(t, func() bool {
		svc.lifecycleMu.Lock()
		defer svc.lifecycleMu.Unlock()
		return svc.lifecycleState == serviceStopping
	}, time.Second, time.Millisecond)
	select {
	case <-stopped:
		t.Fatal("Stop returned while detached ingress was reading storage")
	default:
	}

	family.unblock()
	require.ErrorIs(t, <-submitted, errServiceNotRunning)
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not drain detached ingress")
	}
}

func TestDetachedIngressRejectsReplayFaultAtFence(t *testing.T) {
	svc, err := New(DefaultConfig())
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)

	cold, family := coldAcceptanceParent(t, svc.GetClosedLedger())
	require.NoError(t, svc.SwitchToPreferredLedger(cold))
	blob, hash := startupPaymentBlob(t, "detached-ingress-fault", 1)
	family.armed.Store(true)
	submitted := make(chan error, 1)
	go func() {
		_, err := svc.SubmitOpenLedgerTxDetailed(blob, true)
		submitted <- err
	}()
	select {
	case <-family.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("ingress did not reach a blocked state read")
	}
	require.NoError(t, recordTestReplayFault(svc.replayFaults, replayfault.Fault{Class: replayfault.ExecutionDisagreement}))
	family.unblock()
	require.ErrorIs(t, <-submitted, replayfault.ErrBlocked)
	exists, err := svc.openLedgerView.Current().TxExists(hash)
	require.NoError(t, err)
	require.False(t, exists, "replay fault allowed detached ingress publication")
	require.Zero(t, svc.localTxs.Size(), "replay fault allowed local-pool side effects")
}

func TestDetachedIngressRejectsExistingReplayFault(t *testing.T) {
	svc, err := New(DefaultConfig())
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)
	require.NoError(t, recordTestReplayFault(svc.replayFaults, replayfault.Fault{Class: replayfault.ExecutionDisagreement}))

	blob, hash := startupPaymentBlob(t, "detached-ingress-blocked", 1)
	_, err = svc.SubmitOpenLedgerTxDetailed(blob, true)
	require.ErrorIs(t, err, replayfault.ErrBlocked)
	exists, err := svc.openLedgerView.Current().TxExists(hash)
	require.NoError(t, err)
	require.False(t, exists)
	require.Zero(t, svc.localTxs.Size())
}

func TestDetachedIngressSnapshotCurrentRejectsReplayFaultWithoutGateLeak(t *testing.T) {
	svc, err := New(DefaultConfig())
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)

	snapshot, err := svc.captureDetachedIngress(detachedIngressRequest{})
	require.NoError(t, err)
	require.NoError(t, recordTestReplayFault(svc.replayFaults, replayfault.Fault{Class: replayfault.ExecutionDisagreement}))

	current, err := svc.detachedIngressSnapshotCurrent(detachedIngressRequest{}, snapshot)
	require.ErrorIs(t, err, replayfault.ErrBlocked)
	require.False(t, current)
	require.True(t, svc.openLedgerMu.TryLock(), "snapshot fence leaked the open-ledger gate")
	svc.openLedgerMu.Unlock()
}

func TestDetachedIngressSnapshotCurrentRejectsStoppedService(t *testing.T) {
	svc, err := New(DefaultConfig())
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)

	snapshot, err := svc.captureDetachedIngress(detachedIngressRequest{})
	require.NoError(t, err)
	svc.lifecycleMu.Lock()
	previous := svc.lifecycleState
	svc.lifecycleState = serviceStopping
	svc.lifecycleMu.Unlock()
	defer func() {
		svc.lifecycleMu.Lock()
		svc.lifecycleState = previous
		svc.lifecycleMu.Unlock()
	}()

	current, err := svc.detachedIngressSnapshotCurrent(detachedIngressRequest{}, snapshot)
	require.ErrorIs(t, err, errServiceNotRunning)
	require.False(t, current)
	require.True(t, svc.openLedgerMu.TryLock(), "stopped snapshot fence leaked the open-ledger gate")
	svc.openLedgerMu.Unlock()
}

func TestDetachedIngressRejectsExhaustedMemory(t *testing.T) {
	budget := &preferredMemoryBudget{
		limit: 8 << 20, capacity: 8 << 20, used: 8 << 20, changed: make(chan struct{}),
	}
	cfg := DefaultConfig()
	cfg.PersistenceMemory = budget
	svc, err := New(cfg)
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)
	blob, _ := startupPaymentBlob(t, "detached-ingress-memory", 1)
	submitted := make(chan error, 1)
	go func() {
		_, err := svc.SubmitOpenLedgerTxDetailed(blob, true)
		submitted <- err
	}()
	select {
	case err := <-submitted:
		require.ErrorIs(t, err, ErrPreferredTransitionMemory)
	case <-time.After(time.Second):
		t.Fatal("ingress waited for credits retained by unfinished work")
	}
	svc.Stop()
	require.Equal(t, uint64(8<<20), budget.Used())
	budget.Release(8 << 20)
	require.Zero(t, budget.Used())
}

func TestDetachedIngressAdmissionBoundsWaitersAndStops(t *testing.T) {
	var admission detachedIngressAdmission
	releaseActive, err := admission.acquire()
	require.NoError(t, err)

	results := make(chan error, detachedIngressWaitLimit)
	for range detachedIngressWaitLimit {
		go func() {
			release, err := admission.acquire()
			if err == nil {
				release()
			}
			results <- err
		}()
	}
	require.Eventually(t, func() bool {
		admission.mu.Lock()
		defer admission.mu.Unlock()
		return len(admission.waiters) == detachedIngressWaitLimit
	}, time.Second, time.Millisecond)

	_, err = admission.acquire()
	require.ErrorIs(t, err, errDetachedIngressBusy)

	done := admission.stop()
	select {
	case <-done:
		t.Fatal("active detached ingress completed before its owner released")
	default:
	}
	for range detachedIngressWaitLimit {
		require.ErrorIs(t, <-results, errServiceNotRunning)
	}

	releaseActive()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("detached ingress admission did not drain")
	}

	_, err = admission.acquire()
	require.ErrorIs(t, err, errServiceNotRunning)
}

func TestDetachedIngressAdmissionStopDrainsHandedOffOwner(t *testing.T) {
	var admission detachedIngressAdmission
	releaseActive, err := admission.acquire()
	require.NoError(t, err)

	ready := make(chan func(), 1)
	go func() {
		release, err := admission.acquire()
		if err != nil {
			ready <- nil
			return
		}
		ready <- release
	}()
	require.Eventually(t, func() bool {
		admission.mu.Lock()
		defer admission.mu.Unlock()
		return len(admission.waiters) == 1
	}, time.Second, time.Millisecond)

	releaseActive()
	releaseOwner := <-ready
	require.NotNil(t, releaseOwner)
	done := admission.stop()
	select {
	case <-done:
		t.Fatal("stop completed while the handed-off owner was active")
	default:
	}
	releaseOwner()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("stop did not wait for the handed-off owner")
	}
}
