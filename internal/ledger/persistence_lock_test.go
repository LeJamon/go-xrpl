package ledger

import (
	"bytes"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/shamap"
)

func TestPersistenceDoesNotBlockValidationOrHeaderReads(t *testing.T) {
	for _, mapType := range []shamap.Type{shamap.TypeState, shamap.TypeTransaction} {
		t.Run(mapType.String(), func(t *testing.T) {
			l, _ := newPersistenceLedger(t, mapType, StateClosed, false, 1)
			entered := make(chan struct{})
			release := make(chan struct{})
			persisted := make(chan error, 1)
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			defer func() {
				unblock()
				<-persisted
			}()

			store := func(entries []shamap.FlushEntry) error {
				if len(entries) == 0 {
					t.Error("persistence callback received no dirty entries")
				}
				close(entered)
				<-release
				return nil
			}
			go func() {
				persisted <- storeDirtyForMap(l, mapType, store)
			}()

			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("persistence did not reach storage callback")
			}

			if got := l.Hash(); got != l.header.Hash {
				t.Fatalf("Hash while persistence blocked = %x, want %x", got, l.header.Hash)
			}
			if got := l.Sequence(); got != l.header.LedgerIndex {
				t.Fatalf("Sequence while persistence blocked = %d, want %d", got, l.header.LedgerIndex)
			}

			validated := make(chan error, 1)
			go func() { validated <- l.SetValidated() }()
			select {
			case err := <-validated:
				if err != nil {
					t.Fatalf("SetValidated while persistence blocked: %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("SetValidated blocked by storage callback")
			}
			if !l.IsValidated() || !l.Header().Validated {
				t.Fatal("SetValidated did not update ledger state while persistence was blocked")
			}
		})
	}
}

func TestPersistenceFailureRetainsDirtyNodesForRetry(t *testing.T) {
	for _, mapType := range []shamap.Type{shamap.TypeState, shamap.TypeTransaction} {
		t.Run(mapType.String(), func(t *testing.T) {
			l, _ := newPersistenceLedger(t, mapType, StateClosed, false, 2)
			wantErr := errors.New("injected persistence failure")
			calls := 0
			var firstBatch, secondBatch int
			store := func(entries []shamap.FlushEntry) error {
				calls++
				if calls == 1 {
					firstBatch = len(entries)
					return wantErr
				}
				secondBatch = len(entries)
				return nil
			}
			err := storeDirtyForMap(l, mapType, store)
			if !errors.Is(err, wantErr) {
				t.Fatalf("first persistence error = %v, want %v", err, wantErr)
			}
			if firstBatch == 0 {
				t.Fatal("failed persistence callback received no dirty entries")
			}

			if err := storeDirtyForMap(l, mapType, store); err != nil {
				t.Fatalf("retry persistence: %v", err)
			}
			if calls != 2 || secondBatch != firstBatch {
				t.Fatalf("persistence calls/batches = %d/%d, want 2/%d", calls, secondBatch, firstBatch)
			}
		})
	}
}

func TestMutablePersistenceSerializesWithAdoptAndFamilyRebind(t *testing.T) {
	target, _ := newPersistenceLedger(t, shamap.TypeState, StateOpen, true, 3)
	source, sourceKey := newPersistenceLedger(t, shamap.TypeState, StateOpen, true, 4)
	entered := make(chan struct{})
	release := make(chan struct{})
	persisted := make(chan error, 1)
	var releaseOnce sync.Once
	persistedReceived := false
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer func() {
		unblock()
		if !persistedReceived {
			if err := <-persisted; err != nil {
				t.Errorf("persistence: %v", err)
			}
		}
	}()

	go func() {
		persisted <- target.StoreStateDirty(func(entries []shamap.FlushEntry) error {
			if len(entries) == 0 {
				t.Error("persistence callback received no dirty entries")
			}
			close(entered)
			<-release
			return nil
		})
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("persistence did not reach storage callback")
	}

	adopted := make(chan error, 1)
	go func() { adopted <- target.AdoptState(source) }()
	select {
	case err := <-adopted:
		t.Fatalf("AdoptState completed while mutable persistence was blocked: %v", err)
	case <-time.After(time.Second):
	}

	family := newConstructorRecordingFamily()
	rebound := make(chan struct{})
	go func() {
		target.SetSHAMapFamily(family)
		close(rebound)
	}()
	select {
	case <-rebound:
		t.Fatal("SetSHAMapFamily completed while mutable persistence was blocked")
	case <-time.After(time.Second):
	}

	unblock()
	select {
	case err := <-persisted:
		persistedReceived = true
		if err != nil {
			t.Fatalf("persistence: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("persistence did not finish after release")
	}
	select {
	case err := <-adopted:
		if err != nil {
			t.Fatalf("AdoptState after persistence: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("AdoptState did not finish after persistence release")
	}
	select {
	case <-rebound:
	case <-time.After(time.Second):
		t.Fatal("SetSHAMapFamily did not finish after persistence release")
	}

	finalFamily := newConstructorRecordingFamily()
	target.SetSHAMapFamily(finalFamily)
	if !target.stateMap.IsBacked() || !target.txMap.IsBacked() {
		t.Fatal("family rebind did not update both adopted maps")
	}
	item, found, err := target.stateMap.Get(sourceKey)
	if err != nil || !found || item == nil || !bytes.Equal(item.Data(), bytes.Repeat([]byte{4}, 16)) {
		t.Fatalf("adopted state = %v, %v, %v; want source state", item, found, err)
	}
}

func storeDirtyForMap(l *Ledger, mapType shamap.Type, store func([]shamap.FlushEntry) error) error {
	if mapType == shamap.TypeState {
		return l.StoreStateDirty(store)
	}
	return l.StoreTransactionDirty(store)
}

func newPersistenceLedger(t *testing.T, mapType shamap.Type, state State, writable bool, seed byte) (*Ledger, [32]byte) {
	t.Helper()
	stateMap := shamap.New(shamap.TypeState)
	txMap := shamap.New(shamap.TypeTransaction)
	var key [32]byte
	key[0] = seed
	data := bytes.Repeat([]byte{seed}, 16)
	if mapType == shamap.TypeState {
		if err := stateMap.Put(key, data); err != nil {
			t.Fatalf("seed state map: %v", err)
		}
	} else if err := txMap.Put(key, data); err != nil {
		t.Fatalf("seed transaction map: %v", err)
	}
	if state != StateOpen {
		if err := stateMap.SetImmutable(); err != nil {
			t.Fatalf("freeze state map: %v", err)
		}
		if err := txMap.SetImmutable(); err != nil {
			t.Fatalf("freeze transaction map: %v", err)
		}
	}
	return &Ledger{
		stateMap: stateMap,
		txMap:    txMap,
		header: header.LedgerHeader{
			LedgerIndex: uint32(seed),
			Hash:        [32]byte{seed},
		},
		state:    state,
		writable: writable,
	}, key
}
