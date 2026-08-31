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

	readDone := make(chan *ledger.Ledger, 1)
	go func() { readDone <- svc.GetClosedLedger() }()
	select {
	case got := <-readDone:
		require.Same(t, closed, got)
	case <-time.After(100 * time.Millisecond):
		t.Fatal("GetClosedLedger blocked behind open-ledger transaction application")
	}

	switchDone := make(chan error, 1)
	switchWaiting := make(chan struct{})
	go func() {
		switchDone <- svc.switchToPreferredLedger(preferred, func() { close(switchWaiting) })
	}()
	<-switchWaiting
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

func TestStopWaitsForOpenLedgerSubmission(t *testing.T) {
	svc, err := New(DefaultConfig())
	require.NoError(t, err)
	require.NoError(t, svc.Start())
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
}
