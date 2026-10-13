package flatstate

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"testing"

	"github.com/cockroachdb/pebble"
)

func TestStoreAckPublishedOutboxCurrentSkipsSyncUntilNextTransition(t *testing.T) {
	path := t.TempDir()
	syncs := 0
	store, err := Open(path, Options{
		CacheBytes:   1 << 20,
		MemTableSize: 1 << 20,
		Sync: func(db *pebble.DB) error {
			syncs++
			return db.LogData(nil, pebble.Sync)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	first := testDescriptor(1, Hash{}, testHash(1), testHash(2), testHash(3))
	importInitial(t, store, first, nil)
	initialSyncs := syncs
	firstID := SemanticCommitID(first.LedgerHash, "flat-state")
	if err := store.AckPublishedOutbox(context.Background(), firstID); err != nil {
		t.Fatalf("ack current publication: %v", err)
	}
	if syncs != initialSyncs {
		t.Fatalf("current publication ack used %d syncs; want %d", syncs-initialSyncs, 0)
	}

	second := testDescriptor(2, first.LedgerHash, testHash(4), testHash(5), testHash(6))
	if err := store.Advance(context.Background(), AdvanceRequest{
		Expected: &first, Target: second, Changes: NewSliceChangeIterator(nil), Authorization: testAuthorization(first, second),
	}); err != nil {
		t.Fatal(err)
	}
	if syncs != initialSyncs+1 {
		t.Fatalf("next transition used %d syncs; want one final sync", syncs-initialSyncs)
	}
	if pending, err := store.PendingOutbox(context.Background(), 10); err != nil || len(pending) != 1 || pending[0].Descriptor != second {
		t.Fatalf("pending after next transition = %#v, %v", pending, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = Open(path, Options{CacheBytes: 1 << 20, MemTableSize: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if pending, err := store.PendingOutbox(context.Background(), 10); err != nil || len(pending) != 1 || pending[0].Descriptor != second {
		t.Fatalf("reopened pending = %#v, %v", pending, err)
	}
}

func TestStoreAckPublishedOutboxUsesDurablePathForOlderRecord(t *testing.T) {
	syncs := 0
	store, err := Open(t.TempDir(), Options{
		CacheBytes:   1 << 20,
		MemTableSize: 1 << 20,
		Sync: func(db *pebble.DB) error {
			syncs++
			return db.LogData(nil, pebble.Sync)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	first := testDescriptor(1, Hash{}, testHash(1), testHash(2), testHash(3))
	importInitial(t, store, first, nil)
	second := testDescriptor(2, first.LedgerHash, testHash(4), testHash(5), testHash(6))
	if err := store.Advance(context.Background(), AdvanceRequest{
		Expected: &first, Target: second, Changes: NewSliceChangeIterator(nil), Authorization: testAuthorization(first, second),
	}); err != nil {
		t.Fatal(err)
	}
	beforeCurrentAck := syncs
	if err := store.AckPublishedOutbox(context.Background(), SemanticCommitID(second.LedgerHash, "flat-state")); err != nil {
		t.Fatalf("ack current publication: %v", err)
	}
	if syncs != beforeCurrentAck {
		t.Fatalf("current publication ack used %d syncs; want none", syncs-beforeCurrentAck)
	}
	if pending, err := store.PendingOutbox(context.Background(), 10); err != nil || len(pending) != 1 || pending[0].Descriptor != first {
		t.Fatalf("pending after current ack = %#v, %v", pending, err)
	}
	beforeOldAck := syncs
	if err := store.AckPublishedOutbox(context.Background(), SemanticCommitID(first.LedgerHash, "flat-state")); err != nil {
		t.Fatalf("ack older publication: %v", err)
	}
	if syncs != beforeOldAck+1 {
		t.Fatalf("older publication ack used %d syncs; want one durable sync", syncs-beforeOldAck)
	}
	if pending, err := store.PendingOutbox(context.Background(), 10); err != nil || len(pending) != 0 {
		t.Fatalf("pending after older ack = %#v, %v", pending, err)
	}
}

func TestStoreAckPublishedOutboxUsesDurablePathForDescriptorMismatch(t *testing.T) {
	syncs := 0
	store, err := Open(t.TempDir(), Options{
		CacheBytes:   1 << 20,
		MemTableSize: 1 << 20,
		Sync: func(db *pebble.DB) error {
			syncs++
			return db.LogData(nil, pebble.Sync)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	first := testDescriptor(1, Hash{}, testHash(1), testHash(2), testHash(3))
	if err := store.Advance(context.Background(), AdvanceRequest{
		Target: first, Changes: NewSliceChangeIterator(nil),
		Authorization:   RootClosureAuthorization{TargetRoot: first.StateRoot, Token: []byte("genesis-closure")},
		PublicationKind: "older",
	}); err != nil {
		t.Fatal(err)
	}
	target := first
	target.ParentHash = testHash(9)
	target.TxRoot = testHash(8)
	if err := store.Rebase(context.Background(), RebaseRequest{
		Expected: &first, Target: target, Changes: NewSliceChangeIterator(nil),
		Authorization: testAuthorization(first, target), PublicationKind: "current",
	}); err != nil {
		t.Fatal(err)
	}
	beforeOldAck := syncs
	if err := store.AckPublishedOutbox(context.Background(), SemanticCommitID(first.LedgerHash, "older")); err != nil {
		t.Fatalf("ack descriptor-mismatched publication: %v", err)
	}
	if syncs != beforeOldAck+1 {
		t.Fatalf("descriptor-mismatched ack used %d syncs; want one durable sync", syncs-beforeOldAck)
	}
	pending, err := store.PendingOutbox(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].CommitID != SemanticCommitID(target.LedgerHash, "current") {
		t.Fatalf("pending after descriptor-mismatched ack = %#v", pending)
	}
	if err := store.AckPublishedOutbox(context.Background(), SemanticCommitID(target.LedgerHash, "current")); err != nil {
		t.Fatalf("ack exact current publication: %v", err)
	}
	if syncs != beforeOldAck+1 {
		t.Fatalf("exact current ack used %d extra syncs; want none", syncs-(beforeOldAck+1))
	}
}

func TestStoreAckPublishedOutboxRecoversBeforeSyncFailure(t *testing.T) {
	path := t.TempDir()
	failSync := false
	store, err := Open(path, Options{
		CacheBytes:   1 << 20,
		MemTableSize: 1 << 20,
		Sync: func(db *pebble.DB) error {
			if failSync {
				return errors.New("injected pre-sync failure")
			}
			return db.LogData(nil, pebble.Sync)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	first := testDescriptor(1, Hash{}, testHash(1), testHash(2), testHash(3))
	importInitial(t, store, first, nil)
	second := testDescriptor(2, first.LedgerHash, testHash(4), testHash(5), testHash(6))
	if err := store.Advance(context.Background(), AdvanceRequest{
		Expected: &first, Target: second, Changes: NewSliceChangeIterator(nil), Authorization: testAuthorization(first, second),
	}); err != nil {
		t.Fatal(err)
	}
	failSync = true
	err = store.AckPublishedOutbox(context.Background(), SemanticCommitID(first.LedgerHash, "flat-state"))
	if !errors.Is(err, errSyncUncertain) {
		t.Fatalf("pre-sync ack error = %v", err)
	}
	if _, _, currentErr := store.Current(); !errors.Is(currentErr, ErrUnavailable) {
		t.Fatalf("current after pre-sync ack failure = %v, want unavailable", currentErr)
	}
	// The failed no-sync deletion is intentionally recovered at the reopen
	// boundary; the uncertain store must not be used for further publication.
	failSync = false
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(path, Options{CacheBytes: 1 << 20, MemTableSize: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	current, ok, err := store.Current()
	if err != nil || !ok || current != second {
		t.Fatalf("recovered current after pre-sync ack failure = %#v, %v, %v", current, ok, err)
	}
	pending, err := store.PendingOutbox(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].Descriptor != second {
		t.Fatalf("recovered pending after pre-sync ack failure = %#v", pending)
	}
}

func TestStoreAckPublishedOutboxReplaysAfterUnsyncedDeleteLoss(t *testing.T) {
	path := t.TempDir()
	store, err := Open(path, Options{CacheBytes: 1 << 20, MemTableSize: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	first := testDescriptor(1, Hash{}, testHash(1), testHash(2), testHash(3))
	key := testHash(42)
	value := []byte("published-flat-value")
	importInitial(t, store, first, []Change{{Key: key, Value: value}})
	commitID := SemanticCommitID(first.LedgerHash, "flat-state")
	pending, err := store.PendingOutbox(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].CommitID != commitID {
		t.Fatalf("initial pending = %#v", pending)
	}
	savedRecord := pending[0]
	if err := store.AckPublishedOutbox(context.Background(), commitID); err != nil {
		t.Fatalf("ack current publication: %v", err)
	}
	if pending, err := store.PendingOutbox(context.Background(), 10); err != nil || len(pending) != 0 {
		t.Fatalf("pending after unsynced ack = %#v, %v", pending, err)
	}

	// Restore the pre-delete record as a durable snapshot. This deliberately
	// models a crash that loses the unsynced deletion without claiming to be a
	// power-failure test; the flat-state replay path must treat it as idempotent.
	encoded, err := encodeOutbox(savedRecord)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.db.Set(outboxKey(commitID), encoded, pebble.Sync); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(path, Options{CacheBytes: 1 << 20, MemTableSize: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	current, ok, err := store.Current()
	if err != nil || !ok || current != first {
		t.Fatalf("recovered current after restored publication = %#v, %v, %v", current, ok, err)
	}
	pending, err = store.PendingOutbox(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].Descriptor != first {
		t.Fatalf("recovered pending after restored publication = %#v", pending)
	}
	view, err := store.OpenView(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got, found, err := view.Get(key)
	if closeErr := view.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}
	if !found || string(got) != string(value) {
		t.Fatalf("recovered flat value = %q, %v", got, found)
	}
	if err := store.AckPublishedOutbox(context.Background(), commitID); err != nil {
		t.Fatalf("replay current publication: %v", err)
	}
	if pending, err := store.PendingOutbox(context.Background(), 10); err != nil || len(pending) != 0 {
		t.Fatalf("pending after idempotent replay = %#v, %v", pending, err)
	}
	if err := store.AckPublishedOutbox(context.Background(), commitID); err != nil {
		t.Fatalf("repeat idempotent replay: %v", err)
	}
}

func TestStoreAckPublishedOutboxTransitionSyncFailureRecovers(t *testing.T) {
	path := t.TempDir()
	failNextSync := false
	store, err := Open(path, Options{
		CacheBytes:   1 << 20,
		MemTableSize: 1 << 20,
		Sync: func(db *pebble.DB) error {
			if err := db.LogData(nil, pebble.Sync); err != nil {
				return err
			}
			if failNextSync {
				return errors.New("injected final sync uncertainty")
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	first := testDescriptor(1, Hash{}, testHash(1), testHash(2), testHash(3))
	importInitial(t, store, first, nil)
	if err := store.AckPublishedOutbox(context.Background(), SemanticCommitID(first.LedgerHash, "flat-state")); err != nil {
		t.Fatal(err)
	}
	second := testDescriptor(2, first.LedgerHash, testHash(4), testHash(5), testHash(6))
	failNextSync = true
	err = store.Advance(context.Background(), AdvanceRequest{
		Expected: &first, Target: second, Changes: NewSliceChangeIterator(nil), Authorization: testAuthorization(first, second),
	})
	if !errors.Is(err, errSyncUncertain) {
		t.Fatalf("failed transition error = %v", err)
	}
	if _, _, currentErr := store.Current(); !errors.Is(currentErr, ErrUnavailable) {
		t.Fatalf("current after uncertain transition = %v, want unavailable", currentErr)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = Open(path, Options{CacheBytes: 1 << 20, MemTableSize: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if current, ok, err := store.Current(); err != nil || !ok || current != second {
		t.Fatalf("recovered current = %#v, %v, %v", current, ok, err)
	}
	if pending, err := store.PendingOutbox(context.Background(), 10); err != nil || len(pending) != 1 || pending[0].Descriptor != second {
		t.Fatalf("recovered pending = %#v, %v", pending, err)
	}
}

const ackPublishedCrashEnv = "GOXRPL_FLATSTATE_ACK_PUBLISHED_CRASH"

func TestStoreAckPublishedOutboxProcessExitReplay(t *testing.T) {
	if os.Getenv(ackPublishedCrashEnv) == "1" {
		path := os.Getenv(ackPublishedCrashEnv + "_PATH")
		store, err := Open(path, Options{CacheBytes: 1 << 20, MemTableSize: 1 << 20})
		if err != nil {
			os.Exit(2)
		}
		first := testDescriptor(1, Hash{}, testHash(1), testHash(2), testHash(3))
		importInitial(t, store, first, nil)
		if err := store.AckPublishedOutbox(context.Background(), SemanticCommitID(first.LedgerHash, "flat-state")); err != nil {
			os.Exit(3)
		}
		// Exit without Store.Close so recovery observes either outcome of the
		// unsynced deletion, rather than a graceful close flushing it.
		os.Exit(0)
	}

	path := t.TempDir()
	command := exec.Command(os.Args[0], "-test.run=^TestStoreAckPublishedOutboxProcessExitReplay$")
	command.Env = append(os.Environ(), ackPublishedCrashEnv+"=1", ackPublishedCrashEnv+"_PATH="+path)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("crash helper: %v\n%s", err, output)
	}
	store, err := Open(path, Options{CacheBytes: 1 << 20, MemTableSize: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	first := testDescriptor(1, Hash{}, testHash(1), testHash(2), testHash(3))
	if current, ok, err := store.Current(); err != nil || !ok || current != first {
		t.Fatalf("recovered current = %#v, %v, %v", current, ok, err)
	}
	pending, err := store.PendingOutbox(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) > 1 || (len(pending) == 1 && pending[0].Descriptor != first) {
		t.Fatalf("recovered pending = %#v", pending)
	}
	if len(pending) == 1 {
		if err := store.AckPublishedOutbox(context.Background(), pending[0].CommitID); err != nil {
			t.Fatalf("replay current publication: %v", err)
		}
	}
}
