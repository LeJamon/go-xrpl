package service

import (
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
	blob, hash := startupPaymentBlob(t, "submit-lock-destination", 1)

	applyBlocked := make(chan struct{})
	releaseApply := make(chan struct{})
	modifierDone := make(chan struct{})
	go func() {
		defer close(modifierDone)
		svc.openLedgerView.Modify(func(*ledger.Ledger) bool {
			close(applyBlocked)
			<-releaseApply
			return false
		})
	}()
	<-applyBlocked
	released := false
	defer func() {
		if !released {
			close(releaseApply)
		}
	}()

	type submitResult struct {
		applied bool
		success bool
		code    ter.Result
		err     error
	}
	submitDone := make(chan submitResult, 1)
	go func() {
		if rpc {
			transaction, parseErr := tx.ParseFromBinary(blob)
			if parseErr != nil {
				submitDone <- submitResult{err: parseErr}
				return
			}
			result, submitErr := svc.SubmitTransaction(transaction, blob, false)
			submitDone <- submitResult{
				applied: submitErr == nil && result != nil && result.Applied,
				success: submitErr == nil && result != nil &&
					(result.Result == ter.TesSUCCESS || result.Result.IsTec()),
				code: result.Result,
				err:  submitErr,
			}
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

	require.Eventually(t, func() bool {
		if svc.openLedgerMu.TryLock() {
			svc.openLedgerMu.Unlock()
			return false
		}
		return true
	}, time.Second, time.Millisecond)

	assertClosedRead := func() {
		readDone := make(chan *ledger.Ledger, 1)
		go func() { readDone <- svc.GetClosedLedger() }()
		select {
		case got := <-readDone:
			require.Same(t, closed, got)
		case <-time.After(100 * time.Millisecond):
			t.Fatal("GetClosedLedger blocked behind open-ledger transaction application")
		}
	}
	assertClosedRead()

	switchDone := make(chan error, 1)
	switchWaiting := make(chan struct{})
	go func() {
		switchDone <- svc.switchToPreferredLedger(preferred, func() { close(switchWaiting) })
	}()
	<-switchWaiting
	require.Eventually(t, func() bool {
		if svc.lifecycleMu.TryLock() {
			svc.lifecycleMu.Unlock()
			return false
		}
		return true
	}, time.Second, time.Millisecond)
	assertClosedRead()
	select {
	case err := <-switchDone:
		require.NoError(t, err)
		t.Fatal("preferred-ledger replacement completed during open-ledger submission")
	default:
	}

	close(releaseApply)
	released = true
	<-modifierDone

	result := <-submitDone
	require.NoError(t, result.err)
	require.True(t, result.success, "submission result = %s", result.code)
	require.True(t, result.applied)
	require.NoError(t, <-switchDone)
	require.Equal(t, preferred.Hash(), svc.GetClosedLedger().Hash())

	exists, err := svc.openLedgerView.Current().TxExists(hash)
	require.NoError(t, err)
	require.True(t, exists, "concurrent replacement dropped the submitted transaction")
}

func TestValidatedLedgerWaitsForOpenLedgerSubmission(t *testing.T) {
	svc, err := New(DefaultConfig())
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)
	candidate := retainNextValidationCandidate(t, svc)
	blob, _ := startupPaymentBlob(t, "submit-validation-destination", 1)

	publications := make(chan string, 2)
	svc.SetSubmittedTxCallback(func(SubmittedTxEvent) { publications <- "proposed" })
	svc.SetEventSink(EventSinkFunc(func(*LedgerAcceptedEvent) error {
		publications <- "validated"
		return nil
	}))

	applyBlocked := make(chan struct{})
	releaseApply := make(chan struct{})
	modifierDone := make(chan struct{})
	go func() {
		defer close(modifierDone)
		svc.openLedgerView.Modify(func(*ledger.Ledger) bool {
			close(applyBlocked)
			<-releaseApply
			return false
		})
	}()
	<-applyBlocked
	released := false
	defer func() {
		if !released {
			close(releaseApply)
		}
	}()

	submitDone := make(chan error, 1)
	go func() {
		_, submitErr := svc.SubmitOpenLedgerTxDetailed(blob, true)
		submitDone <- submitErr
	}()
	require.Eventually(t, func() bool {
		if svc.openLedgerMu.TryLock() {
			svc.openLedgerMu.Unlock()
			return false
		}
		return true
	}, time.Second, time.Millisecond)

	validationDone := make(chan struct{})
	go func() {
		svc.SetValidatedLedger(candidate.Sequence(), candidate.Hash())
		close(validationDone)
	}()
	require.Eventually(t, func() bool {
		if svc.lifecycleMu.TryLock() {
			svc.lifecycleMu.Unlock()
			return false
		}
		return true
	}, time.Second, time.Millisecond)

	readDone := make(chan *ledger.Ledger, 1)
	go func() { readDone <- svc.GetClosedLedger() }()
	select {
	case got := <-readDone:
		require.NotNil(t, got)
	case <-time.After(100 * time.Millisecond):
		t.Fatal("GetClosedLedger blocked behind queued ledger validation")
	}

	close(releaseApply)
	released = true
	<-modifierDone
	require.NoError(t, <-submitDone)
	<-validationDone
	require.True(t, candidate.IsValidated())
	require.Same(t, candidate, svc.GetValidatedLedger())
	for _, want := range []string{"proposed", "validated"} {
		select {
		case got := <-publications:
			require.Equal(t, want, got)
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for %s publication", want)
		}
	}
}

func TestStopWaitsForOpenLedgerSubmission(t *testing.T) {
	svc, err := New(DefaultConfig())
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	candidate := retainNextValidationCandidate(t, svc)
	blob, _ := startupPaymentBlob(t, "submit-stop-destination", 1)

	applyBlocked := make(chan struct{})
	releaseApply := make(chan struct{})
	modifierDone := make(chan struct{})
	go func() {
		defer close(modifierDone)
		svc.openLedgerView.Modify(func(*ledger.Ledger) bool {
			close(applyBlocked)
			<-releaseApply
			return false
		})
	}()
	<-applyBlocked
	released := false
	defer func() {
		if !released {
			close(releaseApply)
		}
	}()

	submitDone := make(chan error, 1)
	go func() {
		_, submitErr := svc.SubmitOpenLedgerTxDetailed(blob, true)
		submitDone <- submitErr
	}()
	require.Eventually(t, func() bool {
		if svc.openLedgerMu.TryLock() {
			svc.openLedgerMu.Unlock()
			return false
		}
		return true
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

	close(releaseApply)
	released = true
	<-modifierDone
	require.NoError(t, <-submitDone)
	<-stopDone

	_, err = svc.SubmitOpenLedgerTxDetailed(blob, true)
	require.ErrorContains(t, err, "ledger service is not running")
	svc.SetValidatedLedger(candidate.Sequence(), candidate.Hash())
	require.False(t, candidate.IsValidated(), "validation advanced after the service stopped")
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
