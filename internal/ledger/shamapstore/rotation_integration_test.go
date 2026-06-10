package shamapstore_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger/shamapstore"
	"github.com/LeJamon/go-xrpl/storage/kvstore/memorydb"
	"github.com/LeJamon/go-xrpl/storage/nodestore"
)

// TestRotation_ReclaimsNodeStoreSpace drives a Rotator against a real
// nodestore and models the ledger persistence contract: each ledger re-writes
// the full live state plus a unique header and transaction blob. After a
// rotation, headers and transaction blobs below the boundary are reclaimed
// while the live state — re-written at the latest sequence — survives.
func TestRotation_ReclaimsNodeStoreSpace(t *testing.T) {
	ctx := context.Background()
	db := nodestore.NewKVDatabase(memorydb.New(), "mem", 10000, time.Hour)
	defer db.Close()

	store, err := shamapstore.New(false, "")
	if err != nil {
		t.Fatalf("New store: %v", err)
	}

	// One shared "live account state" node, re-persisted every ledger at the
	// current sequence (mirrors persistToNodeStore walking the full state map).
	liveData := nodestore.Blob("live-account-root")
	liveKey := nodestore.ComputeHash256(liveData)

	headerKeys := make(map[uint32]nodestore.Hash256)
	txKeys := make(map[uint32]nodestore.Hash256)

	persist := func(seq uint32) {
		// Live state re-written with the current seq.
		if err := db.Store(ctx, &nodestore.Node{
			Type: nodestore.NodeAccount, Hash: liveKey, Data: liveData, LedgerSeq: seq,
		}); err != nil {
			t.Fatalf("store live: %v", err)
		}
		// Unique header for this ledger.
		hData := nodestore.Blob(fmt.Sprintf("header-%d", seq))
		hKey := nodestore.ComputeHash256(hData)
		headerKeys[seq] = hKey
		if err := db.Store(ctx, &nodestore.Node{
			Type: nodestore.NodeLedger, Hash: hKey, Data: hData, LedgerSeq: seq,
		}); err != nil {
			t.Fatalf("store header: %v", err)
		}
		// Unique transaction blob for this ledger.
		txData := nodestore.Blob(fmt.Sprintf("tx-%d", seq))
		txKey := nodestore.ComputeHash256(txData)
		txKeys[seq] = txKey
		if err := db.Store(ctx, &nodestore.Node{
			Type: nodestore.NodeTransaction, Hash: txKey, Data: txData, LedgerSeq: seq,
		}); err != nil {
			t.Fatalf("store tx: %v", err)
		}
	}

	rot := shamapstore.NewRotator(store, db, nil,
		shamapstore.RotationConfig{DeleteInterval: 10}, nil)
	if rot == nil {
		t.Fatal("NewRotator returned nil")
	}

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

	// Headers and tx blobs below 11 must be gone.
	for seq := uint32(1); seq < 11; seq++ {
		if exists(headerKeys[seq]) {
			t.Errorf("header for ledger %d should be reclaimed", seq)
		}
		if exists(txKeys[seq]) {
			t.Errorf("tx blob for ledger %d should be reclaimed", seq)
		}
	}
	// Headers and tx blobs at/above 11 must remain.
	for seq := uint32(11); seq <= 25; seq++ {
		if !exists(headerKeys[seq]) {
			t.Errorf("header for ledger %d should be retained", seq)
		}
	}
	// The live state node, re-written at seq 25, must survive every rotation.
	if !exists(liveKey) {
		t.Fatal("live account state must survive rotation")
	}
}
