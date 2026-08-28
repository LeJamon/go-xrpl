package nodestore

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/storage/kvstore"
	"github.com/LeJamon/go-xrpl/storage/kvstore/memorydb"
	pebblekv "github.com/LeJamon/go-xrpl/storage/kvstore/pebble"
)

type identityRotatingStore struct {
	kvstore.KeyValueStore
	identity kvstore.RotationIdentity
}

func (s *identityRotatingStore) RotationIdentity() (kvstore.RotationIdentity, error) {
	return s.identity, nil
}

func TestDurableFingerprintPlainIdentityAndManagedMutation(t *testing.T) {
	database := testDatabase(t, memorydb.New(), noCacheConfig())
	first, err := database.DurableFingerprint(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	again, err := database.DurableFingerprint(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if first != again {
		t.Fatal("durable fingerprint changed without a managed mutation")
	}
	if _, err := database.DeleteBefore(t.Context(), 1, 1); err != nil {
		t.Fatal(err)
	}
	afterPrune, err := database.DurableFingerprint(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if afterPrune == first {
		t.Fatal("managed prune did not advance the durable fingerprint")
	}

	replacement := testDatabase(t, memorydb.New(), noCacheConfig())
	replacementFingerprint, err := replacement.DurableFingerprint(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if replacementFingerprint == afterPrune {
		t.Fatal("replacement NodeStore reused the durable identity")
	}
}

func TestDurableFingerprintBindsRotatingManifest(t *testing.T) {
	base := kvstore.RotationIdentity{
		OwnerID: [16]byte{1}, WritableID: [32]byte{2}, ArchiveID: [32]byte{3},
		LastRotated: 10, MinimumOnline: 4,
	}
	store := &identityRotatingStore{KeyValueStore: memorydb.New(), identity: base}
	database := testDatabase(t, store, noCacheConfig())
	want, err := database.DurableFingerprint(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		mutate func(*kvstore.RotationIdentity)
	}{
		{"owner", func(i *kvstore.RotationIdentity) { i.OwnerID[0]++ }},
		{"writable generation", func(i *kvstore.RotationIdentity) { i.WritableID[0]++ }},
		{"archive generation", func(i *kvstore.RotationIdentity) { i.ArchiveID[0]++ }},
		{"last rotated", func(i *kvstore.RotationIdentity) { i.LastRotated++ }},
		{"minimum online", func(i *kvstore.RotationIdentity) { i.MinimumOnline++ }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			store.identity = base
			test.mutate(&store.identity)
			got, err := database.DurableFingerprint(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if got == want {
				t.Fatal("rotating manifest mutation did not change fingerprint")
			}
		})
	}
}

func TestStoreDurableIgnoresCancellationAfterAdmission(t *testing.T) {
	store := &blockingSyncStore{
		KeyValueStore: memorydb.New(),
		started:       make(chan struct{}, 1),
		release:       make(chan struct{}, 1),
	}
	database, err := NewKVDatabase(store, noCacheConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	node := testNode(NodeLedger, []byte("durable"), 1)
	done := make(chan error, 1)
	go func() { done <- database.StoreDurable(ctx, node) }()
	<-store.started
	cancel()
	select {
	case err := <-done:
		t.Fatalf("StoreDurable returned before backend flush completed: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	store.release <- struct{}{}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	stored, err := database.Fetch(context.Background(), node.Hash)
	if err != nil || stored == nil {
		t.Fatalf("durable node unavailable after completion: node=%v err=%v", stored, err)
	}
}

func TestStoreDurableFlushErrorLeavesCompleteRecord(t *testing.T) {
	wantErr := errors.New("injected flush failure")
	store := &syncRecordingStore{KeyValueStore: memorydb.New(), syncErr: wantErr}
	database := testDatabase(t, store, noCacheConfig())
	node := testNode(NodeLedger, []byte("complete-before-flush"), 1)
	if err := database.StoreDurable(t.Context(), node); !errors.Is(err, wantErr) {
		t.Fatalf("StoreDurable error = %v, want %v", err, wantErr)
	}
	stored, err := database.Fetch(t.Context(), node.Hash)
	if err != nil {
		t.Fatal(err)
	}
	if stored == nil || string(stored.Data) != string(node.Data) {
		t.Fatalf("complete admitted record unavailable after flush error: %#v", stored)
	}
}

func TestStoreDurableSurvivesReopenForActiveAndTombstone(t *testing.T) {
	path := t.TempDir()
	open := func() *KVDatabase {
		backend, err := pebblekv.New(path, pebblekv.Options{})
		if err != nil {
			t.Fatal(err)
		}
		database, err := NewKVDatabase(backend, noCacheConfig())
		if err != nil {
			t.Fatal(err)
		}
		return database
	}
	database := open()
	node := testNode(NodeLedger, []byte("active-record"), 1)
	if err := database.StoreDurable(t.Context(), node); err != nil {
		t.Fatal(err)
	}
	fingerprint, err := database.DurableFingerprint(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	database = open()
	reopenedFingerprint, err := database.DurableFingerprint(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if reopenedFingerprint != fingerprint {
		t.Fatal("plain durable identity changed across reopen")
	}
	stored, err := database.Fetch(t.Context(), node.Hash)
	if err != nil || stored == nil || string(stored.Data) != "active-record" {
		t.Fatalf("active record after reopen = %#v, err=%v", stored, err)
	}
	tombstone := node.Clone()
	tombstone.Data = []byte("tombstone-record")
	if err := database.StoreDurable(t.Context(), tombstone); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	database = open()
	defer database.Close()
	stored, err = database.Fetch(t.Context(), node.Hash)
	if err != nil || stored == nil || string(stored.Data) != "tombstone-record" {
		t.Fatalf("tombstone after reopen = %#v, err=%v", stored, err)
	}
}

func TestDurableSnapshotExcludesManagedPruneThroughPublication(t *testing.T) {
	database := testDatabase(t, memorydb.New(), noCacheConfig())
	if _, err := database.DurableFingerprint(t.Context()); err != nil {
		t.Fatal(err)
	}
	old := testNode(NodeLedger, []byte("old"), 1)
	if err := database.Store(t.Context(), old); err != nil {
		t.Fatal(err)
	}
	acquired := make(chan [32]byte, 1)
	release := make(chan struct{})
	snapshotDone := make(chan error, 1)
	publication := testNode(NodeLedger, []byte("checkpoint"), 99)
	go func() {
		snapshotDone <- database.WithDurableSnapshot(context.Background(), func(fingerprint [32]byte) error {
			acquired <- fingerprint
			<-release
			return database.StoreDurable(context.Background(), publication)
		})
	}()
	before := <-acquired
	pruneDone := make(chan error, 1)
	go func() {
		_, err := database.DeleteBefore(context.Background(), 2, 1)
		pruneDone <- err
	}()
	select {
	case err := <-pruneDone:
		t.Fatalf("prune crossed the active durable snapshot: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	close(release)
	if err := <-snapshotDone; err != nil {
		t.Fatal(err)
	}
	if err := <-pruneDone; err != nil {
		t.Fatal(err)
	}
	after, err := database.DurableFingerprint(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if after == before {
		t.Fatal("prune did not invalidate the just-published snapshot fingerprint")
	}
	stored, err := database.Fetch(t.Context(), publication.Hash)
	if err != nil || stored == nil {
		t.Fatalf("publication missing after serialized prune: node=%v err=%v", stored, err)
	}
}

func TestAcquireDurableSnapshotPinsManagedMutationUntilRelease(t *testing.T) {
	database := testDatabase(t, memorydb.New(), noCacheConfig())
	before, release, err := database.AcquireDurableSnapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	releaseAgain := release

	pruneDone := make(chan error, 1)
	go func() {
		_, pruneErr := database.DeleteBefore(context.Background(), 2, 1)
		pruneDone <- pruneErr
	}()
	select {
	case err := <-pruneDone:
		t.Fatalf("prune crossed the retained durable snapshot: %v", err)
	case <-time.After(25 * time.Millisecond):
	}

	release()
	releaseAgain()
	if err := <-pruneDone; err != nil {
		t.Fatal(err)
	}
	after, err := database.DurableFingerprint(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if after == before {
		t.Fatal("released snapshot did not permit the prune generation to advance")
	}
}

func TestDurableSnapshotOrdersPruneInvalidationAfterLease(t *testing.T) {
	database := testDatabase(t, memorydb.New(), noCacheConfig())
	_, release, err := database.AcquireDurableSnapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	pruneStarted := make(chan struct{})
	pruneDone := make(chan error, 1)
	go func() {
		_, pruneErr := database.DeleteBeforeWithPrune(context.Background(), 2, 1, func() func() {
			close(pruneStarted)
			return func() {}
		})
		pruneDone <- pruneErr
	}()
	select {
	case <-pruneStarted:
		t.Fatal("prune invalidated SHAMap proofs before acquiring the durable mutation gate")
	case <-time.After(25 * time.Millisecond):
	}
	release()
	select {
	case <-pruneStarted:
	case <-time.After(time.Second):
		t.Fatal("prune did not start after the durable snapshot was released")
	}
	if err := <-pruneDone; err != nil {
		t.Fatal(err)
	}
}
