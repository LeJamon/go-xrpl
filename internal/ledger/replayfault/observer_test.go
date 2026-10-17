package replayfault

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

type journalHook string

const (
	writeHook     journalHook = "write"
	fileSyncHook  journalHook = "fsync"
	renameHook    journalHook = "rename"
	directoryHook journalHook = "dirsync"
)

var journalHooks = []journalHook{writeHook, fileSyncHook, renameHook, directoryHook}

type blockedJournalHook struct {
	entered     chan struct{}
	release     chan struct{}
	calls       atomic.Int32
	onCall      int32
	once        sync.Once
	releaseOnce sync.Once
}

func (block *blockedJournalHook) unblock() {
	block.releaseOnce.Do(func() { close(block.release) })
}

type runningJournalOperation struct {
	result   chan error
	finished chan struct{}
}

func installBlockedJournalHook(t *testing.T, hook journalHook, onCall int) (*blockedJournalHook, func()) {
	t.Helper()
	if onCall < 1 {
		t.Fatalf("onCall = %d, want positive call number", onCall)
	}
	block := &blockedJournalHook{
		entered: make(chan struct{}),
		release: make(chan struct{}),
		onCall:  int32(onCall),
	}
	var restore func()
	blockCall := func() {
		if block.calls.Add(1) != block.onCall {
			return
		}
		block.once.Do(func() { close(block.entered) })
		<-block.release
	}
	switch hook {
	case writeHook:
		original := writeFileFn
		writeFileFn = func(file *os.File, data []byte) (int, error) {
			blockCall()
			return original(file, data)
		}
		restore = func() { writeFileFn = original }
	case fileSyncHook:
		original := syncFileFn
		syncFileFn = func(file *os.File) error {
			blockCall()
			return original(file)
		}
		restore = func() { syncFileFn = original }
	case renameHook:
		original := renameFileFn
		renameFileFn = func(oldPath, newPath string) error {
			blockCall()
			return original(oldPath, newPath)
		}
		restore = func() { renameFileFn = original }
	case directoryHook:
		original := syncDirectoryFn
		syncDirectoryFn = func(path string) error {
			blockCall()
			return original(path)
		}
		restore = func() { syncDirectoryFn = original }
	default:
		t.Fatalf("unknown journal hook %q", hook)
	}
	return block, restore
}

func startBlockedJournalOperation(t *testing.T, block *blockedJournalHook, restore func(), invoke func() error) *runningJournalOperation {
	t.Helper()
	running := &runningJournalOperation{
		result:   make(chan error, 1),
		finished: make(chan struct{}),
	}
	go func() {
		running.result <- invoke()
		close(running.finished)
	}()
	t.Cleanup(func() {
		block.unblock()
		timer := time.NewTimer(2 * time.Second)
		defer timer.Stop()
		select {
		case <-running.finished:
		case <-timer.C:
			t.Errorf("timed out waiting for blocked journal operation during cleanup")
		}
		restore()
	})
	return running
}

func installJournalError(t *testing.T, hook journalHook, onCall int, injected error) func() {
	t.Helper()
	if onCall < 1 {
		t.Fatalf("onCall = %d, want positive call number", onCall)
	}
	var calls atomic.Int32
	shouldFail := func() bool { return calls.Add(1) == int32(onCall) }
	switch hook {
	case writeHook:
		original := writeFileFn
		writeFileFn = func(file *os.File, data []byte) (int, error) {
			if shouldFail() {
				return 0, injected
			}
			return original(file, data)
		}
		return func() { writeFileFn = original }
	case fileSyncHook:
		original := syncFileFn
		syncFileFn = func(file *os.File) error {
			if shouldFail() {
				return injected
			}
			return original(file)
		}
		return func() { syncFileFn = original }
	case renameHook:
		original := renameFileFn
		renameFileFn = func(oldPath, newPath string) error {
			if shouldFail() {
				return injected
			}
			return original(oldPath, newPath)
		}
		return func() { renameFileFn = original }
	case directoryHook:
		original := syncDirectoryFn
		syncDirectoryFn = func(path string) error {
			if shouldFail() {
				return injected
			}
			return original(path)
		}
		return func() { syncDirectoryFn = original }
	default:
		t.Fatalf("unknown journal hook %q", hook)
		return nil
	}
}

func waitForSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	select {
	case <-signal:
	case <-timer.C:
		t.Fatal("timed out waiting for deterministic test signal")
	}
}

func waitForResult(t *testing.T, result <-chan error) error {
	t.Helper()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	select {
	case err := <-result:
		return err
	case <-timer.C:
		t.Fatal("timed out waiting for replay-fault operation")
		return nil
	}
}

func waitForCompletion(t *testing.T, completed <-chan struct{}, what string) {
	t.Helper()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	select {
	case <-completed:
	case <-timer.C:
		t.Errorf("timed out waiting for %s during cleanup", what)
	}
}

func requireObserverReturns(t *testing.T, store *Store) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		_ = store.Blocked()
		_ = store.Status()
		_ = store.Snapshot()
		close(done)
	}()
	waitForSignal(t, done)
}

func newDurableStore(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fault.json")
	store, err := Open(path)
	require.NoError(t, err)
	return store, path
}

func seedFault(t *testing.T, store *Store) string {
	t.Helper()
	id, err := store.BeginReplay(Fault{ParentHash: [32]byte{1}, TargetHash: [32]byte{2}})
	require.NoError(t, err)
	require.NoError(t, store.FailReplay(id, Fault{
		Class:    ExecutionDisagreement,
		Message:  "replay mismatch",
		Evidence: json.RawMessage(`{"proof":true}`),
	}))
	return id
}

func requireValidatorRejected(t *testing.T, store *Store) {
	t.Helper()
	called := false
	err := store.WithValidator(func() error {
		called = true
		return nil
	})
	require.ErrorIs(t, err, ErrBlocked)
	require.False(t, called, "validator callback ran through a blocked replay-fault store")
}

func TestObserversRemainResponsiveDuringJournalIO(t *testing.T) {
	cases := []struct {
		name        string
		hook        journalHook
		onCall      int
		prepare     func(*testing.T, *Store) func() error
		wantBlocked bool
	}{
		{
			name: "begin",
			hook: fileSyncHook,
			prepare: func(_ *testing.T, store *Store) func() error {
				return func() error {
					_, err := store.BeginReplay(Fault{TargetHash: [32]byte{1}})
					return err
				}
			},
		},
		{
			name: "fail",
			hook: directoryHook,
			prepare: func(t *testing.T, store *Store) func() error {
				id, err := store.BeginReplay(Fault{TargetHash: [32]byte{1}})
				require.NoError(t, err)
				return func() error { return store.FailReplay(id, Fault{Class: CorruptState}) }
			},
			wantBlocked: true,
		},
		{
			name: "complete",
			hook: writeHook,
			prepare: func(t *testing.T, store *Store) func() error {
				id, err := store.BeginReplay(Fault{TargetHash: [32]byte{1}})
				require.NoError(t, err)
				return func() error { return store.CompleteReplay(id) }
			},
		},
		{
			name: "cancel",
			hook: renameHook,
			prepare: func(t *testing.T, store *Store) func() error {
				id, err := store.BeginReplay(Fault{TargetHash: [32]byte{1}})
				require.NoError(t, err)
				return func() error { return store.CancelReplay(id) }
			},
		},
		{
			name: "update",
			hook: fileSyncHook,
			prepare: func(t *testing.T, store *Store) func() error {
				id := seedFault(t, store)
				return func() error { return store.Update(id, Fault{RecoveryError: "updated"}) }
			},
			wantBlocked: true,
		},
		{
			name: "reserve acquisition",
			hook: directoryHook,
			prepare: func(t *testing.T, store *Store) func() error {
				id := seedFault(t, store)
				return func() error { return store.ReserveAcquisition(id, 2) }
			},
			wantBlocked: true,
		},
		{
			name:   "revalidate begin",
			hook:   renameHook,
			onCall: 1,
			prepare: func(t *testing.T, store *Store) func() error {
				id := seedFault(t, store)
				return func() error {
					return store.Revalidate(context.Background(), id, func(context.Context, Fault) error { return nil })
				}
			},
			wantBlocked: true,
		},
		{
			name:   "revalidate failure",
			hook:   fileSyncHook,
			onCall: 2,
			prepare: func(t *testing.T, store *Store) func() error {
				id := seedFault(t, store)
				return func() error {
					return store.Revalidate(context.Background(), id, func(context.Context, Fault) error {
						return errors.New("verification failed")
					})
				}
			},
			wantBlocked: true,
		},
		{
			name:   "revalidate archive",
			hook:   directoryHook,
			onCall: 2,
			prepare: func(t *testing.T, store *Store) func() error {
				id := seedFault(t, store)
				return func() error {
					return store.Revalidate(context.Background(), id, func(context.Context, Fault) error { return nil })
				}
			},
			wantBlocked: true,
		},
		{
			name:   "revalidate clear",
			hook:   renameHook,
			onCall: 3,
			prepare: func(t *testing.T, store *Store) func() error {
				id := seedFault(t, store)
				return func() error {
					return store.Revalidate(context.Background(), id, func(context.Context, Fault) error { return nil })
				}
			},
			wantBlocked: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, _ := newDurableStore(t)
			invoke := tc.prepare(t, store)
			onCall := tc.onCall
			if onCall == 0 {
				onCall = 1
			}
			block, restore := installBlockedJournalHook(t, tc.hook, onCall)
			running := startBlockedJournalOperation(t, block, restore, invoke)
			waitForSignal(t, block.entered)
			requireObserverReturns(t, store)
			if tc.wantBlocked && !store.Blocked() {
				t.Fatal("published status lost the failure latch while journal I/O was blocked")
			}
			block.unblock()
			err := waitForResult(t, running.result)
			if tc.name == "revalidate failure" {
				require.ErrorContains(t, err, "verification failed")
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestFailureLatchAndValidatorFenceDuringBlockedPersistence(t *testing.T) {
	store, _ := newDurableStore(t)
	id, err := store.BeginReplay(Fault{TargetHash: [32]byte{1}})
	require.NoError(t, err)
	block, restore := installBlockedJournalHook(t, fileSyncHook, 1)
	running := startBlockedJournalOperation(t, block, restore, func() error {
		return store.FailReplay(id, Fault{Class: ExecutionDisagreement})
	})
	waitForSignal(t, block.entered)

	require.True(t, store.Blocked())
	snapshot := store.Snapshot()
	require.NotNil(t, snapshot)
	require.Equal(t, id, snapshot.ID)
	require.Equal(t, ExecutionDisagreement, snapshot.Class)
	require.True(t, store.Status().Blocked)
	requireObserverReturns(t, store)

	validatorCalled := make(chan struct{})
	validatorDone := make(chan error, 1)
	validatorFinished := make(chan struct{})
	go func() {
		defer close(validatorFinished)
		validatorDone <- store.WithValidator(func() error {
			close(validatorCalled)
			return nil
		})
	}()
	t.Cleanup(func() {
		block.unblock()
		waitForCompletion(t, validatorFinished, "validator")
	})
	select {
	case <-validatorCalled:
		t.Fatal("validator callback ran while a failure latch was being persisted")
	case <-time.After(50 * time.Millisecond):
	}
	block.unblock()
	require.NoError(t, waitForResult(t, running.result))
	require.ErrorIs(t, waitForResult(t, validatorDone), ErrBlocked)
}

func TestSuccessfulRecoveryStaysBlockedUntilDurableClear(t *testing.T) {
	store, _ := newDurableStore(t)
	id := seedFault(t, store)
	block, restore := installBlockedJournalHook(t, renameHook, 3)
	running := startBlockedJournalOperation(t, block, restore, func() error {
		return store.Revalidate(context.Background(), id, func(context.Context, Fault) error { return nil })
	})
	waitForSignal(t, block.entered)

	status := store.Status()
	require.True(t, status.Blocked)
	require.True(t, status.Recovery.InFlight)
	require.NotNil(t, store.Snapshot())
	requireObserverReturns(t, store)

	validatorCalled := make(chan struct{})
	validatorDone := make(chan error, 1)
	validatorFinished := make(chan struct{})
	go func() {
		defer close(validatorFinished)
		validatorDone <- store.WithValidator(func() error {
			close(validatorCalled)
			return nil
		})
	}()
	t.Cleanup(func() {
		block.unblock()
		waitForCompletion(t, validatorFinished, "validator")
	})
	select {
	case <-validatorCalled:
		t.Fatal("validator callback ran before the durable clear completed")
	case <-time.After(50 * time.Millisecond):
	}
	block.unblock()
	require.NoError(t, waitForResult(t, running.result))
	require.NoError(t, waitForResult(t, validatorDone))
	require.False(t, store.Blocked())
	require.Nil(t, store.Snapshot())
}

func TestJournalFaultsPreserveRestartSafety(t *testing.T) {
	fault := errors.New("injected journal failure")
	for _, hook := range journalHooks {
		t.Run(fmt.Sprintf("intent/%s", hook), func(t *testing.T) {
			store, path := newDurableStore(t)
			restore := installJournalError(t, hook, 1, fault)
			id, err := store.BeginReplay(Fault{TargetHash: [32]byte{1}})
			restore()
			require.ErrorIs(t, err, fault)
			require.Empty(t, id)
			require.True(t, store.Blocked())
			require.NotNil(t, store.Status().PersistenceError)
			requireValidatorRejected(t, store)
			fresh, openErr := Open(path)
			require.NoError(t, openErr)
			if hook == directoryHook {
				require.True(t, fresh.Blocked(), "directory sync failure follows a published intent")
				require.NotNil(t, fresh.Snapshot())
				requireValidatorRejected(t, fresh)
			} else {
				require.False(t, fresh.Blocked(), "a pre-publish failure must not manufacture durable execution")
				require.Nil(t, fresh.Snapshot())
			}
		})

		t.Run(fmt.Sprintf("fail/%s", hook), func(t *testing.T) {
			store, path := newDurableStore(t)
			id, err := store.BeginReplay(Fault{TargetHash: [32]byte{1}})
			require.NoError(t, err)
			restore := installJournalError(t, hook, 1, fault)
			require.ErrorIs(t, store.FailReplay(id, Fault{Class: CorruptState}), fault)
			restore()
			require.True(t, store.Blocked())
			fresh, openErr := Open(path)
			require.NoError(t, openErr)
			require.True(t, fresh.Blocked())
			require.NotNil(t, fresh.Snapshot())
			requireValidatorRejected(t, fresh)
		})

		for _, operation := range []string{"complete", "cancel"} {
			t.Run(fmt.Sprintf("%s/%s", operation, hook), func(t *testing.T) {
				store, path := newDurableStore(t)
				id, err := store.BeginReplay(Fault{TargetHash: [32]byte{1}})
				require.NoError(t, err)
				restore := installJournalError(t, hook, 1, fault)
				if operation == "complete" {
					require.ErrorIs(t, store.CompleteReplay(id), fault)
				} else {
					require.ErrorIs(t, store.CancelReplay(id), fault)
				}
				restore()
				require.True(t, store.Blocked())
				fresh, openErr := Open(path)
				require.NoError(t, openErr)
				require.True(t, fresh.Blocked())
				require.NotNil(t, fresh.Snapshot())
				requireValidatorRejected(t, fresh)
			})
		}
	}
}

func TestRecoveryJournalFaultsKeepTheGateLatched(t *testing.T) {
	fault := errors.New("injected recovery journal failure")
	stages := []struct {
		name   string
		onCall int
		verify func() error
	}{
		{name: "begin", onCall: 1, verify: func() error { return nil }},
		{name: "failure", onCall: 2, verify: func() error { return errors.New("verification failed") }},
		{name: "archive", onCall: 2, verify: func() error { return nil }},
		{name: "clear", onCall: 3, verify: func() error { return nil }},
	}
	for _, stage := range stages {
		for _, hook := range journalHooks {
			t.Run(fmt.Sprintf("%s/%s", stage.name, hook), func(t *testing.T) {
				store, path := newDurableStore(t)
				id := seedFault(t, store)
				restore := installJournalError(t, hook, stage.onCall, fault)
				err := store.Revalidate(context.Background(), id, func(context.Context, Fault) error {
					return stage.verify()
				})
				restore()

				if stage.name == "failure" {
					require.ErrorContains(t, err, "verification failed")
				} else if stage.name == "begin" {
					require.NoError(t, err)
				} else {
					require.ErrorIs(t, err, fault)
				}
				fresh, openErr := Open(path)
				require.NoError(t, openErr)
				if stage.name == "begin" {
					require.False(t, fresh.Blocked(), "successful verified recovery cleared the durable fault")
				} else {
					require.True(t, store.Blocked())
					require.NotNil(t, store.Snapshot())
					require.True(t, fresh.Blocked())
					require.NotNil(t, fresh.Snapshot())
					requireValidatorRejected(t, fresh)
				}
			})
		}
	}
}

func TestVerifiedClearAndFailedRestoreKeepCurrentProcessBlocked(t *testing.T) {
	store, path := newDurableStore(t)
	id := seedFault(t, store)
	syncErr := errors.New("clear directory sync failed")
	restoreErr := errors.New("restore rename failed")
	restoreSync := installJournalError(t, directoryHook, 3, syncErr)
	defer restoreSync()
	restoreRename := installJournalError(t, renameHook, 4, restoreErr)
	defer restoreRename()
	verified := false
	err := store.Revalidate(t.Context(), id, func(context.Context, Fault) error {
		verified = true
		return nil
	})
	require.ErrorIs(t, err, syncErr)
	require.ErrorIs(t, err, restoreErr)
	require.True(t, verified)
	require.True(t, store.Blocked())
	requireValidatorRejected(t, store)

	// A clear that survived the failed sync still follows completed verification.
	// The current process stays fenced even when restoration also fails.
	restarted, err := Open(path)
	require.NoError(t, err)
	require.False(t, restarted.Blocked())
}

func TestBeginValidatorRaceFencesTheCallback(t *testing.T) {
	store, _ := newDurableStore(t)
	block, restore := installBlockedJournalHook(t, fileSyncHook, 1)
	running := startBlockedJournalOperation(t, block, restore, func() error {
		_, err := store.BeginReplay(Fault{TargetHash: [32]byte{1}})
		return err
	})
	waitForSignal(t, block.entered)

	callbackCalled := make(chan struct{})
	validatorDone := make(chan error, 1)
	validatorFinished := make(chan struct{})
	go func() {
		defer close(validatorFinished)
		validatorDone <- store.WithValidator(func() error {
			close(callbackCalled)
			return nil
		})
	}()
	t.Cleanup(func() {
		block.unblock()
		waitForCompletion(t, validatorFinished, "validator")
	})
	select {
	case <-callbackCalled:
		t.Fatal("validator callback overlapped a replay intent write")
	case <-time.After(50 * time.Millisecond):
	}
	block.unblock()
	require.NoError(t, waitForResult(t, running.result))
	require.NoError(t, waitForResult(t, validatorDone))
}

func TestFailWinsAgainstConcurrentComplete(t *testing.T) {
	store, _ := newDurableStore(t)
	id, err := store.BeginReplay(Fault{TargetHash: [32]byte{1}})
	require.NoError(t, err)
	block, restore := installBlockedJournalHook(t, fileSyncHook, 1)
	failRunning := startBlockedJournalOperation(t, block, restore, func() error {
		return store.FailReplay(id, Fault{Class: CorruptState})
	})
	waitForSignal(t, block.entered)
	completeDone := make(chan error, 1)
	completeFinished := make(chan struct{})
	go func() {
		defer close(completeFinished)
		completeDone <- store.CompleteReplay(id)
	}()
	t.Cleanup(func() {
		block.unblock()
		waitForCompletion(t, completeFinished, "concurrent completion")
	})
	select {
	case err := <-completeDone:
		t.Fatalf("CompleteReplay raced through an owned failure: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	block.unblock()
	require.NoError(t, waitForResult(t, failRunning.result))
	require.ErrorIs(t, waitForResult(t, completeDone), ErrBlocked)
	require.True(t, store.Blocked())
}

func TestRecoveryUpdateInvalidatesStaleGeneration(t *testing.T) {
	store, _ := newDurableStore(t)
	id := seedFault(t, store)
	verifyStarted := make(chan struct{})
	verifyRelease := make(chan struct{})
	var verifyReleaseOnce sync.Once
	recoveryDone := make(chan error, 1)
	recoveryFinished := make(chan struct{})
	go func() {
		defer close(recoveryFinished)
		recoveryDone <- store.Revalidate(context.Background(), id, func(context.Context, Fault) error {
			close(verifyStarted)
			<-verifyRelease
			return nil
		})
	}()
	t.Cleanup(func() {
		verifyReleaseOnce.Do(func() { close(verifyRelease) })
		waitForCompletion(t, recoveryFinished, "stale recovery")
	})
	waitForSignal(t, verifyStarted)

	updateBlock, restore := installBlockedJournalHook(t, directoryHook, 1)
	updateRunning := startBlockedJournalOperation(t, updateBlock, restore, func() error {
		return store.Update(id, Fault{RecoveryError: "new evidence"})
	})
	waitForSignal(t, updateBlock.entered)
	status := store.Status()
	require.True(t, status.Blocked)
	require.NotNil(t, status.Fault)
	require.Equal(t, "new evidence", status.Fault.RecoveryError)
	updateBlock.unblock()
	require.NoError(t, waitForResult(t, updateRunning.result))
	verifyReleaseOnce.Do(func() { close(verifyRelease) })
	require.ErrorIs(t, waitForResult(t, recoveryDone), ErrStaleRevalidation)
	require.True(t, store.Blocked())
	require.Equal(t, "new evidence", store.Snapshot().RecoveryError)
}

func TestRecoveryUpdatePreservesNewerErrorOnStaleVerifierFailure(t *testing.T) {
	store, path := newDurableStore(t)
	id := seedFault(t, store)
	verifyStarted := make(chan struct{})
	verifyRelease := make(chan struct{})
	var verifyReleaseOnce sync.Once
	verifierErr := errors.New("historical verification failed")
	recoveryDone := make(chan error, 1)
	recoveryFinished := make(chan struct{})
	go func() {
		defer close(recoveryFinished)
		recoveryDone <- store.Revalidate(context.Background(), id, func(context.Context, Fault) error {
			close(verifyStarted)
			<-verifyRelease
			return verifierErr
		})
	}()
	t.Cleanup(func() {
		verifyReleaseOnce.Do(func() { close(verifyRelease) })
		waitForCompletion(t, recoveryFinished, "stale verifier failure")
	})
	waitForSignal(t, verifyStarted)
	require.NoError(t, store.Update(id, Fault{RecoveryError: "newer recovery evidence"}))
	verifyReleaseOnce.Do(func() { close(verifyRelease) })
	err := waitForResult(t, recoveryDone)
	require.ErrorIs(t, err, verifierErr)
	require.ErrorIs(t, err, ErrStaleRevalidation)
	require.True(t, store.Blocked())
	require.Equal(t, "newer recovery evidence", store.Snapshot().RecoveryError)
	require.Equal(t, "newer recovery evidence", store.Status().Recovery.LastError)

	restarted, openErr := Open(path)
	require.NoError(t, openErr)
	require.True(t, restarted.Blocked())
	require.Equal(t, "newer recovery evidence", restarted.Snapshot().RecoveryError)
	require.Equal(t, "newer recovery evidence", restarted.Status().Recovery.LastError)
}

func TestConcurrentSnapshotsRemainIndependent(t *testing.T) {
	store, _ := Open("")
	id := seedFault(t, store)
	const readers = 8
	const iterations = 500
	var wg sync.WaitGroup
	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				snapshot := store.Snapshot()
				if snapshot == nil || snapshot.ID != id {
					t.Errorf("snapshot = %+v, want fault %q", snapshot, id)
					return
				}
				if !json.Valid(snapshot.Evidence) {
					t.Errorf("snapshot evidence = %q, want valid immutable JSON", snapshot.Evidence)
					return
				}
				snapshot.Evidence[0] = 'x'
				status := store.Status()
				if status.Fault == nil || status.Fault.ID != id || len(status.Fault.Evidence) != 0 {
					t.Errorf("status fault = %+v, want summary for %q", status.Fault, id)
					return
				}
				if !store.Blocked() {
					t.Error("concurrent observer reported an unblocked fault")
					return
				}
			}
		}()
	}
	for i := 0; i < iterations; i++ {
		require.NoError(t, store.Update(id, Fault{
			Class:    ExecutionDisagreement,
			Evidence: json.RawMessage(fmt.Sprintf(`{"version":%d}`, i)),
		}))
	}
	wg.Wait()
	require.Equal(t, id, store.Snapshot().ID)
}

func TestZeroAndNilStoreObservers(t *testing.T) {
	var zero Store
	require.False(t, zero.Blocked())
	require.Empty(t, zero.Status().Recovery)
	require.Nil(t, zero.Snapshot())
	require.NoError(t, zero.WithValidator(func() error { return nil }))

	var nilStore *Store
	require.True(t, nilStore.Blocked())
	require.True(t, nilStore.Status().Blocked)
	require.NotNil(t, nilStore.Status().PersistenceError)
	require.Nil(t, nilStore.Snapshot())
	require.ErrorIs(t, nilStore.WithValidator(func() error { return nil }), ErrBlocked)
}

func TestTimingCollectorRecordsObserverAndJournalPhases(t *testing.T) {
	before := collectTimingSamples(t)
	store, _ := newDurableStore(t)
	_, err := store.BeginReplay(Fault{TargetHash: [32]byte{1}})
	require.NoError(t, err)
	require.NoError(t, store.WithValidator(func() error { return nil }))
	_ = store.Blocked()
	_ = store.Status()
	_ = store.Snapshot()
	after := collectTimingSamples(t)
	for _, phase := range []string{
		"file_sync",
		"directory_sync",
		"gate_observation",
		"state_lock_wait",
		"state_lock_hold",
		"mutation_fence_wait",
		"validator_fence_wait",
	} {
		require.Greater(t, after[phase], before[phase], "timing phase %q did not record an observation", phase)
	}
}

func collectTimingSamples(t *testing.T) map[string]uint64 {
	t.Helper()
	registry := prometheus.NewRegistry()
	require.NoError(t, registry.Register(TimingCollector()))
	families, err := registry.Gather()
	require.NoError(t, err)
	result := make(map[string]uint64)
	for _, family := range families {
		if family.GetName() != "goxrpl_replay_fault_duration_seconds" {
			continue
		}
		for _, metric := range family.GetMetric() {
			phase := ""
			for _, label := range metric.GetLabel() {
				if label.GetName() == "phase" {
					phase = label.GetValue()
					break
				}
			}
			if phase != "" && metric.GetHistogram() != nil {
				result[phase] = metric.GetHistogram().GetSampleCount()
			}
		}
	}
	return result
}
