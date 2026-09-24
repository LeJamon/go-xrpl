package replayfault

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRecordPersistsAndRestarts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "replay-fault.json")
	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	want := Fault{
		Class:      ExecutionDisagreement,
		ParentHash: [32]byte{1, 2, 3},
		TargetHash: [32]byte{4, 5, 6},
		Sequence:   42,
		Message:    "state diverged",
		Evidence:   json.RawMessage(`{"ledger":"target"}`),
	}
	if err := store.Record(want); err != nil {
		t.Fatalf("Record: %v", err)
	}
	got := store.Snapshot()
	if got == nil || got.ID == "" || got.CreatedAt.IsZero() || got.Revision == "" {
		t.Fatalf("Record did not normalize fault metadata: %+v", got)
	}
	if got.Class != want.Class || got.Sequence != want.Sequence || string(got.Evidence) != string(want.Evidence) {
		t.Fatalf("Snapshot = %+v, want fields from %+v", got, want)
	}

	restarted, err := Open(path)
	if err != nil {
		t.Fatalf("Open after restart: %v", err)
	}
	if restarted.Snapshot() == nil || restarted.Snapshot().ID != got.ID {
		t.Fatalf("restart Snapshot = %+v, want ID %q", restarted.Snapshot(), got.ID)
	}
	if !restarted.Blocked() {
		t.Fatal("restarted store is healthy while a fault is persisted")
	}

	got.Evidence[0] = 'x'
	if string(store.Snapshot().Evidence) != string(want.Evidence) {
		t.Fatal("Snapshot did not deep-copy evidence")
	}
}

func TestOpenMalformedFileBlocks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "replay-fault.json")
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := Open(path)
	if err == nil {
		t.Fatal("Open malformed file succeeded")
	}
	if store == nil || !store.Blocked() {
		t.Fatalf("malformed store = %#v, want blocked store", store)
	}
	if store.Snapshot() != nil {
		t.Fatal("malformed store exposed a fault snapshot")
	}
	if err := store.WithValidator(func() error { return nil }); !errors.Is(err, ErrBlocked) {
		t.Fatal("malformed store allowed validation")
	}
}

func TestRecordWriteFailureRemainsBlocked(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "parent")
	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(parent, "replay-fault.json")
	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := os.Remove(parent); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(parent, []byte("directory replaced"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.Record(Fault{Class: CorruptState}); err == nil {
		t.Fatal("Record unexpectedly persisted through a file parent")
	}
	if !store.Blocked() || store.Snapshot() == nil {
		t.Fatal("write failure did not leave an in-memory gate")
	}
	if err := store.WithValidator(func() error { return nil }); !errors.Is(err, ErrBlocked) {
		t.Fatalf("WithValidator after write failure = %v, want ErrBlocked", err)
	}
}

func TestWithValidatorSerializesRecord(t *testing.T) {
	store, err := Open("")
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	validatorDone := make(chan error, 1)
	go func() {
		validatorDone <- store.WithValidator(func() error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered

	recordDone := make(chan error, 1)
	go func() { recordDone <- store.Record(Fault{ID: "first", Class: MissingState}) }()
	select {
	case err := <-recordDone:
		t.Fatalf("Record completed while validator was running: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	close(release)
	if err := <-validatorDone; err != nil {
		t.Fatalf("validator: %v", err)
	}
	if err := <-recordDone; err != nil {
		t.Fatalf("Record: %v", err)
	}
	if err := store.WithValidator(func() error { return nil }); !errors.Is(err, ErrBlocked) {
		t.Fatalf("WithValidator after Record = %v, want ErrBlocked", err)
	}
}

func TestRevalidateStaleClearAndRecovery(t *testing.T) {
	store, err := Open("")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Record(Fault{ID: "fault", Class: MissingState}); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		result <- store.Revalidate(context.Background(), "fault", func(_ context.Context, _ Fault) error {
			close(started)
			<-release
			return nil
		})
	}()
	<-started
	if status := store.Status(); !status.Recovery.InFlight || status.Recovery.ID != "fault" {
		t.Fatalf("recovery status = %+v, want in-flight fault", status.Recovery)
	}
	if err := store.Record(Fault{ID: "new-fault", Class: CorruptState}); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-result; !errors.Is(err, ErrStaleRevalidation) {
		t.Fatalf("stale revalidation = %v, want ErrStaleRevalidation", err)
	}
	if !store.Blocked() || store.Snapshot().ID != "fault" {
		t.Fatal("stale revalidation cleared or replaced the first fault")
	}
}

func TestRevalidateFailureThenSuccess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "replay-fault.json")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Record(Fault{ID: "fault", Class: ExecutionDisagreement}); err != nil {
		t.Fatal(err)
	}
	wantErr := errors.New("verification failed")
	if err := store.Revalidate(context.Background(), "fault", func(context.Context, Fault) error { return wantErr }); !errors.Is(err, wantErr) {
		t.Fatalf("failed revalidation = %v, want %v", err, wantErr)
	}
	if !store.Blocked() || store.Snapshot().Attempts != 1 {
		t.Fatalf("failed revalidation status = %+v", store.Status())
	}
	if err := store.Revalidate(context.Background(), "fault", func(_ context.Context, fault Fault) error {
		if fault.Attempts != 2 {
			t.Errorf("callback Attempts = %d, want 2", fault.Attempts)
		}
		return nil
	}); err != nil {
		t.Fatalf("successful revalidation: %v", err)
	}
	if store.Blocked() || store.Snapshot() != nil {
		t.Fatal("successful revalidation left the gate blocked")
	}
	restarted, err := Open(path)
	if err != nil {
		t.Fatalf("Open after clear: %v", err)
	}
	if restarted.Blocked() {
		t.Fatal("durable clear did not survive restart")
	}
}

func TestRevalidateArchivesResolvedFault(t *testing.T) {
	path := filepath.Join(t.TempDir(), "replay-fault.json")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Record(Fault{ID: "fault", Class: ExecutionDisagreement, Evidence: json.RawMessage(`{"target":true}`)}); err != nil {
		t.Fatal(err)
	}
	if err := store.Revalidate(context.Background(), "fault", func(context.Context, Fault) error { return nil }); err != nil {
		t.Fatalf("Revalidate: %v", err)
	}

	archivePath := path + ".fault.resolved"
	data, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatalf("ReadFile archive: %v", err)
	}
	var archived Fault
	if err := json.Unmarshal(data, &archived); err != nil {
		t.Fatalf("decode archive: %v", err)
	}
	if archived.ID != "fault" || archived.Class != ExecutionDisagreement || archived.Attempts != 1 {
		t.Fatalf("archived fault = %+v", archived)
	}
	var evidence map[string]bool
	if err := json.Unmarshal(archived.Evidence, &evidence); err != nil || !evidence["target"] {
		t.Fatalf("archived evidence = %s", archived.Evidence)
	}

	restarted, err := Open(path)
	if err != nil {
		t.Fatalf("Open after clear: %v", err)
	}
	if restarted.Blocked() {
		t.Fatal("durable clear left the store blocked")
	}
}

func TestRevalidateClearFailureRestoresFault(t *testing.T) {
	path := filepath.Join(t.TempDir(), "replay-fault.json")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Record(Fault{ID: "fault", Class: MissingState}); err != nil {
		t.Fatal(err)
	}

	originalSync := syncDirectoryFn
	failNextSync := false
	syncDirectoryFn = func(dir string) error {
		if failNextSync {
			failNextSync = false
			return errors.New("injected directory sync failure")
		}
		return originalSync(dir)
	}
	defer func() { syncDirectoryFn = originalSync }()

	err = store.Revalidate(context.Background(), "fault", func(context.Context, Fault) error {
		failNextSync = true
		return nil
	})
	if err == nil {
		t.Fatal("Revalidate unexpectedly succeeded after clear sync failure")
	}
	if !store.Blocked() || store.Snapshot() == nil {
		t.Fatalf("clear failure lost in-memory gate: status=%+v", store.Status())
	}

	restarted, err := Open(path)
	if err != nil {
		t.Fatalf("Open after failed clear: %v", err)
	}
	if !restarted.Blocked() || restarted.Snapshot() == nil {
		t.Fatalf("restart lost unresolved fault: status=%+v", restarted.Status())
	}
	if _, err := os.Stat(path + ".fault.resolved"); err != nil {
		t.Fatalf("resolved archive missing after failed clear: %v", err)
	}
}

func TestRevalidateRequiresCallbackAndOnlyOneInflight(t *testing.T) {
	store, err := Open("")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Record(Fault{ID: "fault"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Revalidate(context.Background(), "fault", nil); !errors.Is(err, ErrVerifierRequired) {
		t.Fatalf("nil verifier = %v, want ErrVerifierRequired", err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = store.Revalidate(context.Background(), "fault", func(context.Context, Fault) error {
			close(started)
			<-release
			return nil
		})
	}()
	<-started
	if err := store.Revalidate(context.Background(), "fault", func(context.Context, Fault) error { return nil }); !errors.Is(err, ErrRevalidationInProgress) {
		t.Fatalf("second revalidation = %v, want ErrRevalidationInProgress", err)
	}
	close(release)
	wg.Wait()
}

func TestUpdatePreservesIdentityAndAttempts(t *testing.T) {
	store, err := Open("")
	if err != nil {
		t.Fatal(err)
	}
	created := time.Unix(10, 20).UTC()
	if err := store.Record(Fault{ID: "fault", Class: Unclassified, CreatedAt: created}); err != nil {
		t.Fatal(err)
	}
	if err := store.Revalidate(context.Background(), "fault", func(context.Context, Fault) error { return errors.New("retry") }); err == nil {
		t.Fatal("failed revalidation unexpectedly succeeded")
	}
	if err := store.Update("fault", Fault{Class: CorruptState, Evidence: json.RawMessage(`{"proof":true}`)}); err != nil {
		t.Fatal(err)
	}
	got := store.Snapshot()
	if got.ID != "fault" || !got.CreatedAt.Equal(created) || got.Attempts != 1 || got.Class != CorruptState {
		t.Fatalf("updated fault = %+v", got)
	}
}

func TestRevalidationPanicRemainsBlockedAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fault.json")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Record(Fault{Class: ExecutionDisagreement, Message: "fixture"}); err != nil {
		t.Fatal(err)
	}
	id := store.Snapshot().ID
	err = store.Revalidate(context.Background(), id, func(context.Context, Fault) error { panic("broken verifier") })
	if err == nil || !store.Blocked() || store.Status().Recovery.InFlight {
		t.Fatal("panic did not leave a responsive blocked gate", err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reopened.Blocked() || reopened.Status().Recovery.LastError == "" {
		t.Fatal("failed recovery diagnostics lost on restart")
	}
	if err := reopened.Revalidate(context.Background(), id, func(context.Context, Fault) error { return nil }); err != nil {
		t.Fatal(err)
	}
}

func TestAcquisitionBudgetCannotBeRolledBack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fault.json")
	store, err := Open(path)
	require.NoError(t, err)
	require.NoError(t, store.Record(Fault{Class: MissingState}))
	stale := store.Snapshot()
	for range 3 {
		require.NoError(t, store.ReserveAcquisition(stale.ID, 3))
	}
	require.NoError(t, store.Update(stale.ID, *stale))
	require.Error(t, store.ReserveAcquisition(stale.ID, 3))
	reopened, err := Open(path)
	require.NoError(t, err)
	require.Equal(t, 3, reopened.Snapshot().AcquisitionAttempts)
	require.Error(t, reopened.ReserveAcquisition(stale.ID, 3))
}
