package shamapstore_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger/shamapstore"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/LeJamon/go-xrpl/storage/kvstore/memorydb"
	"github.com/LeJamon/go-xrpl/storage/nodestore"
)

// TestRotation_ReclaimsNodeStoreSpace drives a Rotator against a real
// nodestore. Each synthetic ledger re-stamps live state at its current sequence
// while leaving churned state at its original sequence, allowing rotation to
// reclaim old records without removing the live state.
func TestRotation_ReclaimsNodeStoreSpace(t *testing.T) {
	ctx := context.Background()
	db := nodestore.NewKVDatabase(memorydb.New(), "mem", 10000, time.Hour)
	defer db.Close()

	store, err := shamapstore.New(false, "")
	if err != nil {
		t.Fatalf("New store: %v", err)
	}

	// One shared "live account state" leaf, re-persisted every ledger at the
	// current sequence (mirrors persistToNodeStore walking the full state map).
	liveData := nodestore.Blob("live-account-root")
	liveKey := nodestore.ComputeHash256(liveData)

	headerKeys := make(map[uint32]nodestore.Hash256)
	churnedKeys := make(map[uint32]nodestore.Hash256)

	persist := func(seq uint32) {
		if err := db.Store(ctx, &nodestore.Node{
			Type: nodestore.NodeAccount, Hash: liveKey, Data: liveData, LedgerSeq: seq,
		}); err != nil {
			t.Fatalf("store live: %v", err)
		}
		hData := nodestore.Blob(fmt.Sprintf("header-%d", seq))
		hKey := nodestore.ComputeHash256(hData)
		headerKeys[seq] = hKey
		if err := db.Store(ctx, &nodestore.Node{
			Type: nodestore.NodeLedger, Hash: hKey, Data: hData, LedgerSeq: seq,
		}); err != nil {
			t.Fatalf("store header: %v", err)
		}
		// A state leaf touched only at this ledger: never re-written, so it
		// keeps its original LedgerSeq and becomes superseded once the account
		// changes again — exactly what online-delete reclaims.
		churnData := nodestore.Blob(fmt.Sprintf("churned-state-%d", seq))
		churnKey := nodestore.ComputeHash256(churnData)
		churnedKeys[seq] = churnKey
		if err := db.Store(ctx, &nodestore.Node{
			Type: nodestore.NodeAccount, Hash: churnKey, Data: churnData, LedgerSeq: seq,
		}); err != nil {
			t.Fatalf("store churned leaf: %v", err)
		}
	}

	rot := shamapstore.NewRotator(store, db, nil,
		shamapstore.RotationConfig{DeleteInterval: 10}, nil)
	if rot == nil {
		t.Fatal("NewRotator returned nil")
	}
	rot.SetStateRefresh(func(context.Context, uint32) error { return nil }, nil, nil)

	// Build 25 ledgers, notifying the rotator synchronously per ledger via the
	// internal predicate path so the assertions are deterministic.
	for seq := uint32(1); seq <= 25; seq++ {
		persist(seq)
		rot.NotifyForTest(seq)
	}

	// lastRotated seeds at 1; first rotation fires at seq 11 (>= 1+10),
	// deleting below 1 (nothing) and setting lastRotated=11; the next fires
	// at seq 21 (>= 11+10), deleting below 11 and setting lastRotated=21, so
	// minimumOnline becomes 11+1 = 12.
	if got := rot.MinimumOnline(); got != 12 {
		t.Fatalf("minimumOnline = %d, want 12", got)
	}

	exists := func(h nodestore.Hash256) bool {
		n, err := db.Fetch(ctx, h)
		if err != nil {
			t.Fatalf("Fetch: %v", err)
		}
		return n != nil
	}

	// Headers and superseded (churned) state leaves below 11 must be gone.
	for seq := uint32(1); seq < 11; seq++ {
		if exists(headerKeys[seq]) {
			t.Errorf("header for ledger %d should be reclaimed", seq)
		}
		if exists(churnedKeys[seq]) {
			t.Errorf("superseded state leaf for ledger %d should be reclaimed", seq)
		}
	}
	// Headers and churned leaves at/above 11 must remain.
	for seq := uint32(11); seq <= 25; seq++ {
		if !exists(headerKeys[seq]) {
			t.Errorf("header for ledger %d should be retained", seq)
		}
		if !exists(churnedKeys[seq]) {
			t.Errorf("state leaf for ledger %d should be retained", seq)
		}
	}
	// The live state leaf, re-written at seq 25, must survive every rotation.
	if !exists(liveKey) {
		t.Fatal("live account state must survive rotation")
	}
}

func TestRotation_AdvancesAcquisitionFloorBeforeLiveStateRefresh(t *testing.T) {
	ctx := context.Background()
	db := nodestore.NewKVDatabase(memorydb.New(), "mem", 100, time.Hour)
	defer db.Close()
	family := shamap.NewNodeStoreFamily(db)
	data := []byte("shared-live-node")
	hash := [32]byte(nodestore.ComputeHash256(data))
	if err := family.StoreBatch(ctx, []shamap.FlushEntry{{
		Hash: hash, Data: data, LedgerSeq: 100, MapType: shamap.TypeState,
	}}); err != nil {
		t.Fatal(err)
	}

	store, err := shamapstore.New(false, "")
	if err != nil {
		t.Fatal(err)
	}
	rot := shamapstore.NewRotator(store, db, nil,
		shamapstore.RotationConfig{DeleteInterval: 256}, nil)
	rot.SetStateRefresh(func(_ context.Context, seq uint32) error {
		err := family.StoreBatch(ctx, []shamap.FlushEntry{{
			Hash: hash, Data: data, LedgerSeq: 150, MapType: shamap.TypeState,
		}})
		if !errors.Is(err, shamap.ErrStoreBelowMinimum) {
			t.Fatalf("historical write during refresh = %v, want %v", err, shamap.ErrStoreBelowMinimum)
		}
		return family.StoreBatch(ctx, []shamap.FlushEntry{{
			Hash: hash, Data: data, LedgerSeq: seq, MapType: shamap.TypeState,
		}})
	}, family.SetMinimumLedgerSeq, family.BeginPrune)

	rot.NotifyForTest(200)
	rot.NotifyForTest(456)

	stored, err := db.Fetch(ctx, nodestore.Hash256(hash))
	if err != nil {
		t.Fatal(err)
	}
	if stored == nil || stored.LedgerSeq != 456 {
		t.Fatalf("live shared node after rotation = %+v, want sequence 456", stored)
	}
}
