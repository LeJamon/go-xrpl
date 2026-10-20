package service

import (
	"context"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/openledger"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/stretchr/testify/require"
)

func TestSubmitOpenLedgerTxDoesNotBlockClosedLedgerReadsOrReplacement(t *testing.T) {
	for _, rpc := range []bool{false, true} {
		name := "network"
		if rpc {
			name = "rpc"
		}
		t.Run(name, func(t *testing.T) {
			testSubmitDoesNotBlockClosedLedgerReadsOrReplacement(t, rpc)
		})
	}
}

func testSubmitDoesNotBlockClosedLedgerReadsOrReplacement(t *testing.T, rpc bool) {
	svc, err := New(DefaultConfig())
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)

	closed := svc.GetClosedLedger()
	require.NotNil(t, closed)
	preferred, err := ledger.NewOpen(closed, time.Now())
	require.NoError(t, err)
	require.NoError(t, preferred.Close(time.Now(), 0))
	cold, family := coldAcceptanceParent(t, closed)
	require.NoError(t, svc.SwitchToPreferredLedger(cold))
	svc.mu.RLock()
	_, err = svc.applyConfigLocked()
	svc.mu.RUnlock()
	require.NoError(t, err)
	blob, hash := startupPaymentBlob(t, "submit-lock-destination", 1)

	type submitResult struct {
		applied bool
		success bool
		code    ter.Result
		err     error
	}
	submitDone := make(chan submitResult, 1)
	family.armed.Store(true)
	go func() {
		if rpc {
			transaction, parseErr := tx.ParseFromBinary(blob)
			if parseErr != nil {
				submitDone <- submitResult{err: parseErr}
				return
			}
			result, submitErr := svc.SubmitTransaction(transaction, blob, false)
			out := submitResult{err: submitErr}
			if result != nil {
				out.applied = submitErr == nil && result.Applied
				out.success = submitErr == nil &&
					(result.Result == ter.TesSUCCESS || result.Result.IsTec())
				out.code = result.Result
			}
			submitDone <- out
			return
		}
		outcome, submitErr := svc.SubmitOpenLedgerTxDetailed(blob, true)
		submitDone <- submitResult{
			applied: outcome.Applied,
			success: outcome.Class == openledger.ResultSuccess,
			code:    outcome.Result,
			err:     submitErr,
		}
	}()

	select {
	case <-family.entered:
	case result := <-submitDone:
		t.Fatalf("ingress returned before the cold open-ledger read: %+v", result)
	case <-time.After(5 * time.Second):
		t.Fatal("ingress did not reach the cold open-ledger read")
	}

	require.Eventually(t, func() bool {
		return !svc.openLedgerMu.Snapshot().Held
	}, time.Second, time.Millisecond)

	assertClosedRead := func() {
		readDone := make(chan *ledger.Ledger, 1)
		go func() { readDone <- svc.GetClosedLedger() }()
		select {
		case got := <-readDone:
			require.NotNil(t, got)
		case <-time.After(100 * time.Millisecond):
			t.Fatal("GetClosedLedger blocked behind open-ledger transaction application")
		}
	}
	assertClosedRead()

	switchDone := make(chan error, 1)
	go func() {
		switchDone <- svc.switchToPreferredLedger(preferred, nil)
	}()
	select {
	case err := <-switchDone:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("preferred-ledger replacement blocked behind detached submission")
	}

	family.unblock()

	result := <-submitDone
	require.NoError(t, result.err)
	require.True(t, result.success, "submission result = %s", result.code)
	require.True(t, result.applied)
	require.Equal(t, preferred.Hash(), svc.GetClosedLedger().Hash())

	exists, err := svc.openLedgerView.Current().TxExists(hash)
	require.NoError(t, err)
	require.True(t, exists, "concurrent replacement dropped the submitted transaction")
}

func TestValidatedLedgerDoesNotWaitForOpenLedgerSubmission(t *testing.T) {
	svc, err := New(DefaultConfig())
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)
	closed := svc.GetClosedLedger()
	cold, family := coldAcceptanceParent(t, closed)
	require.NoError(t, svc.SwitchToPreferredLedger(cold))
	candidate := svc.GetClosedLedger()
	svc.mu.RLock()
	_, err = svc.applyConfigLocked()
	svc.mu.RUnlock()
	require.NoError(t, err)
	blob, _ := startupPaymentBlob(t, "submit-validation-destination", 1)

	publications := make(chan string, 2)
	svc.SetSubmittedTxCallback(func(SubmittedTxEvent) { publications <- "proposed" })
	svc.SetEventSink(EventSinkFunc(func(*LedgerAcceptedEvent) error {
		publications <- "validated"
		return nil
	}))

	submitDone := make(chan error, 1)
	family.armed.Store(true)
	go func() {
		_, submitErr := svc.SubmitOpenLedgerTxDetailed(blob, true)
		submitDone <- submitErr
	}()
	select {
	case <-family.entered:
	case err := <-submitDone:
		t.Fatalf("ingress returned before the cold open-ledger read: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("ingress did not reach the cold open-ledger read")
	}
	require.Eventually(t, func() bool {
		return !svc.openLedgerMu.Snapshot().Held
	}, time.Second, time.Millisecond)

	validationDone := make(chan struct{})
	go func() {
		svc.SetValidatedLedger(candidate.Sequence(), candidate.Hash())
		close(validationDone)
	}()
	select {
	case <-validationDone:
	case <-time.After(time.Second):
		t.Fatal("validation admission blocked behind detached ledger submission")
	}
	require.True(t, candidate.IsValidated(), "validation did not advance while ingress was blocked")

	readDone := make(chan *ledger.Ledger, 1)
	go func() { readDone <- svc.GetClosedLedger() }()
	select {
	case got := <-readDone:
		require.NotNil(t, got)
	case <-time.After(100 * time.Millisecond):
		t.Fatal("GetClosedLedger blocked behind queued ledger validation")
	}

	family.unblock()
	require.NoError(t, <-submitDone)
	require.Same(t, candidate, svc.GetValidatedLedger())
	seen := make(map[string]bool, 2)
	for len(seen) < 2 {
		select {
		case publication := <-publications:
			seen[publication] = true
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for submit and validation publications")
		}
	}
	require.True(t, seen["proposed"])
	require.True(t, seen["validated"])
}

func TestOpenLedgerWaitDoesNotBlockValidationAdmission(t *testing.T) {
	svc, err := New(DefaultConfig())
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)
	require.NoError(t, svc.lockOpenLedgerIfRunning(openLedgerConsensus))
	released := false
	defer func() {
		if !released {
			svc.openLedgerMu.Unlock()
		}
	}()

	waiterDone := make(chan error, 1)
	go func() {
		err := svc.lockOpenLedgerIfRunning(openLedgerConsensus)
		if err == nil {
			svc.openLedgerMu.Unlock()
		}
		waiterDone <- err
	}()
	require.Eventually(t, func() bool {
		return svc.openLedgerMu.Snapshot().QueuedPriority != 0
	}, time.Second, time.Millisecond)

	admitted := make(chan bool, 1)
	go func() {
		ok := svc.beginValidatedLedgerUpdate()
		if ok {
			svc.validationWG.Done()
		}
		admitted <- ok
	}()
	select {
	case ok := <-admitted:
		require.True(t, ok)
	case <-time.After(time.Second):
		t.Fatal("validation admission blocked behind an open-ledger waiter")
	}
	select {
	case <-waiterDone:
		t.Fatal("open-ledger serialization was bypassed")
	default:
	}
	svc.openLedgerMu.Unlock()
	released = true
	require.NoError(t, <-waiterDone)
}

func TestStopRejectsQueuedConsensusAcceptance(t *testing.T) {
	svc, err := New(DefaultConfig())
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)
	closed := svc.GetClosedLedger()
	require.NoError(t, svc.lockOpenLedgerIfRunning(openLedgerConsensus))
	released := false
	defer func() {
		if !released {
			svc.openLedgerMu.Unlock()
		}
	}()

	acceptDone := make(chan error, 1)
	go func() {
		_, err := svc.AcceptConsensusResult(context.Background(), closed, nil, nil, time.Now(), true)
		acceptDone <- err
	}()
	require.Eventually(t, func() bool {
		return svc.openLedgerMu.Snapshot().QueuedPriority != 0
	}, time.Second, time.Millisecond)
	stopDone := make(chan struct{})
	go func() {
		svc.Stop()
		close(stopDone)
	}()
	require.Eventually(t, func() bool {
		if !svc.lifecycleMu.TryLock() {
			return false
		}
		defer svc.lifecycleMu.Unlock()
		return svc.lifecycleState == serviceStopping
	}, time.Second, time.Millisecond, "queued consensus blocked Stop from closing admission")
	select {
	case <-stopDone:
		t.Fatal("Stop did not drain the active open-ledger operation")
	default:
	}
	// Fresh callers must also be rejected without waiting on the busy mutex.
	require.ErrorIs(t, svc.lockOpenLedgerIfRunning(openLedgerConsensus), errServiceNotRunning)
	require.False(t, svc.beginValidatedLedgerUpdate())
	svc.openLedgerMu.Unlock()
	released = true
	select {
	case err := <-acceptDone:
		require.ErrorIs(t, err, errServiceNotRunning)
	case <-time.After(time.Second):
		t.Fatal("queued consensus acceptance did not return after shutdown")
	}
	select {
	case <-stopDone:
	case <-time.After(time.Second):
		t.Fatal("Stop did not complete")
	}
	require.Same(t, closed, svc.GetClosedLedger(), "queued acceptance mutated the checkpoint frontier")
	require.ErrorIs(t, svc.lockOpenLedgerIfRunning(openLedgerConsensus), errServiceNotRunning)
}

func TestStopWaitsForOpenLedgerSubmission(t *testing.T) {
	svc, err := New(DefaultConfig())
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	candidate := retainNextValidationCandidate(t, svc)
	closed := svc.GetClosedLedger()
	cold, family := coldAcceptanceParent(t, closed)
	require.NoError(t, svc.SwitchToPreferredLedger(cold))
	svc.mu.RLock()
	_, err = svc.applyConfigLocked()
	svc.mu.RUnlock()
	require.NoError(t, err)
	blob, _ := startupPaymentBlob(t, "submit-stop-destination", 1)

	submitDone := make(chan error, 1)
	family.armed.Store(true)
	go func() {
		_, submitErr := svc.SubmitOpenLedgerTxDetailed(blob, true)
		submitDone <- submitErr
	}()
	select {
	case <-family.entered:
	case err := <-submitDone:
		t.Fatalf("ingress returned before the cold open-ledger read: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("ingress did not reach the cold open-ledger read")
	}
	require.Eventually(t, func() bool {
		return !svc.openLedgerMu.Snapshot().Held
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
		t.Fatal("Stop returned while an open-ledger submission was still running")
	default:
	}

	family.unblock()
	require.ErrorIs(t, <-submitDone, errServiceNotRunning)
	<-stopDone

	_, err = svc.SubmitOpenLedgerTxDetailed(blob, true)
	require.ErrorContains(t, err, "ledger service is not running")
	svc.SetValidatedLedger(candidate.Sequence(), candidate.Hash())
	require.False(t, candidate.IsValidated(), "validation advanced after the service stopped")
}

func TestQueuedIngressDoesNotHoldLifecycleLock(t *testing.T) {
	svc, err := New(DefaultConfig())
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)

	svc.openLedgerMu.Lock()
	released := false
	defer func() {
		if !released {
			svc.openLedgerMu.Unlock()
		}
	}()
	admissionDone := make(chan error, 1)
	go func() {
		err := svc.lockOpenLedgerIfRunning(openLedgerIngress)
		if err == nil {
			svc.openLedgerMu.Unlock()
		}
		admissionDone <- err
	}()
	require.Eventually(t, func() bool {
		return svc.openLedgerMu.Snapshot().QueuedIngress == 1
	}, time.Second, time.Millisecond)

	require.True(t, svc.lifecycleMu.TryLock(), "queued ingress must not hold lifecycleMu")
	svc.lifecycleMu.Unlock()
	svc.openLedgerMu.Unlock()
	released = true
	require.NoError(t, <-admissionDone)
}

func TestQueuedIngressRejectedWhenStopWinsOpenLedgerGate(t *testing.T) {
	svc, err := New(DefaultConfig())
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)

	svc.openLedgerMu.Lock()
	released := false
	defer func() {
		if !released {
			svc.openLedgerMu.Unlock()
		}
	}()
	blob, _ := startupPaymentBlob(t, "queued-stop-destination", 1)
	submitDone := make(chan error, 1)
	go func() {
		_, submitErr := svc.SubmitOpenLedgerTxDetailed(blob, true)
		submitDone <- submitErr
	}()
	require.Eventually(t, func() bool {
		return svc.openLedgerMu.Snapshot().QueuedIngress == 1
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

	svc.openLedgerMu.Unlock()
	released = true
	require.ErrorContains(t, <-submitDone, "ledger service is not running")
	<-stopDone
}

func TestStopWaitsForValidatedLedgerWork(t *testing.T) {
	svc, err := New(DefaultConfig())
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	require.True(t, svc.beginValidatedLedgerUpdate())
	released := false
	defer func() {
		if !released {
			svc.validationWG.Done()
		}
	}()

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
		t.Fatal("Stop returned while validated-ledger work was still running")
	default:
	}

	svc.validationWG.Done()
	released = true
	<-stopDone
}

func TestValidatedLedgerCallbackCanStopService(t *testing.T) {
	svc, err := New(DefaultConfig())
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	candidate := retainNextValidationCandidate(t, svc)

	callbackDone := make(chan struct{})
	svc.SetOnValidatedLedger(func(uint32, [32]byte, [32]byte) {
		svc.Stop()
		close(callbackDone)
	})
	validationDone := make(chan struct{})
	go func() {
		svc.SetValidatedLedger(candidate.Sequence(), candidate.Hash())
		close(validationDone)
	}()
	select {
	case <-callbackDone:
	case <-time.After(time.Second):
		t.Fatal("validated-ledger callback deadlocked while stopping the service")
	}
	<-validationDone
}

func retainNextValidationCandidate(t *testing.T, svc *Service) *ledger.Ledger {
	t.Helper()
	closed := svc.GetClosedLedger()
	require.NotNil(t, closed)
	candidate, err := ledger.NewOpen(closed, time.Now())
	require.NoError(t, err)
	require.NoError(t, candidate.Close(time.Now(), 0))
	svc.mu.Lock()
	svc.historyComponent.mu.Lock()
	svc.retainValidationCandidateLocked(candidate)
	svc.historyComponent.mu.Unlock()
	svc.mu.Unlock()
	return candidate
}
