package shamap

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/storage/nodestore"
)

type blockingNodeStore struct {
	nodestore.Database
	once    sync.Once
	entered chan struct{}
	release chan struct{}
}

func (s *blockingNodeStore) StoreBatch(ctx context.Context, nodes []*nodestore.Node) error {
	blocked := false
	s.once.Do(func() {
		blocked = true
		close(s.entered)
	})
	if blocked {
		select {
		case <-s.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return s.Database.StoreBatch(ctx, nodes)
}

func TestNodeStoreFamily_RejectsWritesBelowMinimumLedger(t *testing.T) {
	ctx := context.Background()
	family := NewMemoryNodeStoreFamily()
	hash := [32]byte{0x91}
	entry := FlushEntry{Hash: hash, Data: []byte("node"), LedgerSeq: 300, MapType: TypeState}
	if err := family.StoreBatch(ctx, []FlushEntry{entry}); err != nil {
		t.Fatal(err)
	}

	family.SetMinimumLedgerSeq(250)
	entry.LedgerSeq = 200
	if err := family.StoreBatch(ctx, []FlushEntry{entry}); !errors.Is(err, ErrStoreBelowMinimum) {
		t.Fatalf("below-floor store error = %v, want %v", err, ErrStoreBelowMinimum)
	}
	stored, err := family.db.Fetch(ctx, nodestore.Hash256(hash))
	if err != nil {
		t.Fatal(err)
	}
	if stored == nil || stored.LedgerSeq != 300 {
		t.Fatalf("stored sequence = %v, want 300", stored)
	}

	family.SetMinimumLedgerSeq(100)
	belowFloor := [32]byte{0x92}
	entry.Hash = belowFloor
	entry.LedgerSeq = 225
	if err := family.StoreBatch(ctx, []FlushEntry{entry}); !errors.Is(err, ErrStoreBelowMinimum) {
		t.Fatalf("monotonic floor store error = %v, want %v", err, ErrStoreBelowMinimum)
	}
	stored, err = family.db.Fetch(ctx, nodestore.Hash256(belowFloor))
	if err != nil {
		t.Fatal(err)
	}
	if stored != nil {
		t.Fatalf("minimum ledger sequence regressed: stored %+v", stored)
	}
}

func TestNodeStoreFamily_MinimumWaitsForInFlightStore(t *testing.T) {
	ctx := context.Background()
	base := NewMemoryNodeStoreFamily().db
	store := &blockingNodeStore{
		Database: base,
		entered:  make(chan struct{}),
		release:  make(chan struct{}),
	}
	family := NewNodeStoreFamily(store)
	oldHash := [32]byte{0xA1}
	storeDone := make(chan error, 1)
	go func() {
		storeDone <- family.StoreBatch(ctx, []FlushEntry{{
			Hash: oldHash, Data: []byte("old"), LedgerSeq: 100, MapType: TypeState,
		}})
	}()
	<-store.entered

	floorDone := make(chan struct{})
	go func() {
		family.SetMinimumLedgerSeq(200)
		close(floorDone)
	}()
	select {
	case <-floorDone:
		t.Fatal("minimum advanced before the in-flight store completed")
	case <-time.After(50 * time.Millisecond):
	}

	close(store.release)
	if err := <-storeDone; err != nil {
		t.Fatal(err)
	}
	<-floorDone
	stored, err := base.Fetch(ctx, nodestore.Hash256(oldHash))
	if err != nil || stored == nil {
		t.Fatalf("in-flight store = %v, %v", stored, err)
	}

	newHash := [32]byte{0xA2}
	if err := family.StoreBatch(ctx, []FlushEntry{{
		Hash: newHash, Data: []byte("new"), LedgerSeq: 100, MapType: TypeState,
	}}); !errors.Is(err, ErrStoreBelowMinimum) {
		t.Fatalf("post-advance store error = %v, want %v", err, ErrStoreBelowMinimum)
	}
	stored, err = base.Fetch(ctx, nodestore.Hash256(newHash))
	if err != nil {
		t.Fatal(err)
	}
	if stored != nil {
		t.Fatalf("below-floor store after advance = %+v", stored)
	}
}
