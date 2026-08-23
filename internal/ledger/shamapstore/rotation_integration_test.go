package shamapstore_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger/shamapstore"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/LeJamon/go-xrpl/shamap/backend"
	"github.com/LeJamon/go-xrpl/storage/kvstore/memorydb"
	kvpebble "github.com/LeJamon/go-xrpl/storage/kvstore/pebble"
	"github.com/LeJamon/go-xrpl/storage/nodestore"
)

func testNodeHash(data []byte) nodestore.Hash256 {
	return nodestore.Hash256(sha256.Sum256(data))
}

func testNodeDatabase(t *testing.T, cacheSize int) *nodestore.KVDatabase {
	t.Helper()
	database, err := nodestore.NewKVDatabase(memorydb.New(), nodestore.DatabaseConfig{
		PositiveCache: nodestore.CacheConfig{
			Enabled:    true,
			MaxEntries: cacheSize,
			TTL:        time.Hour,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return database
}

func testRotatingNodeDatabase(
	t *testing.T,
	store *kvpebble.RotatingStore,
	cacheSize int,
) *nodestore.RotatingKVDatabase {
	t.Helper()
	database, err := nodestore.NewRotatingKVDatabase(store, nodestore.DatabaseConfig{
		PositiveCache: nodestore.CacheConfig{
			Enabled:    true,
			MaxEntries: cacheSize,
			TTL:        time.Hour,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return database
}

// TestRotation_ReclaimsNodeStoreSpace drives a Rotator against a real
// nodestore. Each synthetic ledger re-stamps live state at its current sequence
// while leaving churned state at its original sequence, allowing rotation to
// reclaim old records without removing the live state.
func TestRotation_ReclaimsNodeStoreSpace(t *testing.T) {
	ctx := context.Background()
	db := testNodeDatabase(t, 10000)
	defer db.Close()

	store, err := shamapstore.New(false, "")
	if err != nil {
		t.Fatalf("New store: %v", err)
	}

	// One shared "live account state" leaf, re-persisted every ledger at the
	// current sequence (mirrors persistToNodeStore walking the full state map).
	liveData := []byte("live-account-root")
	liveKey := testNodeHash(liveData)

	headerKeys := make(map[uint32]nodestore.Hash256)
	churnedKeys := make(map[uint32]nodestore.Hash256)

	persist := func(seq uint32) {
		if err := db.Store(ctx, &nodestore.Node{
			Type: nodestore.NodeAccount, Hash: liveKey, Data: liveData, LedgerSeq: seq,
		}); err != nil {
			t.Fatalf("store live: %v", err)
		}
		hData := []byte(fmt.Sprintf("header-%d", seq))
		hKey := testNodeHash(hData)
		headerKeys[seq] = hKey
		if err := db.Store(ctx, &nodestore.Node{
			Type: nodestore.NodeLedger, Hash: hKey, Data: hData, LedgerSeq: seq,
		}); err != nil {
			t.Fatalf("store header: %v", err)
		}
		// A state leaf touched only at this ledger: never re-written, so it
		// keeps its original LedgerSeq and becomes superseded once the account
		// changes again — exactly what online-delete reclaims.
		churnData := []byte(fmt.Sprintf("churned-state-%d", seq))
		churnKey := testNodeHash(churnData)
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
	rot.SetStateRefresh(func(_ context.Context, seq uint32, _ func(context.Context, time.Duration) error) (uint32, error) {
		return seq, nil
	}, nil, nil)

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

func TestRotation_RotatingPebblePromotesLiveStateAndRetiresHistory(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "nodes")
	backend, err := kvpebble.NewRotating(
		path,
		kvpebble.Options{BlockCacheBytes: 16 << 20, MaxOpenFiles: 200},
	)
	if err != nil {
		t.Fatal(err)
	}
	db := testRotatingNodeDatabase(t, backend, 32)

	live := &nodestore.Node{
		Type: nodestore.NodeAccount, Hash: testNodeHash([]byte("live")),
		Data: []byte("live"), LedgerSeq: 1,
	}
	historical := &nodestore.Node{
		Type: nodestore.NodeAccount, Hash: testNodeHash([]byte("historical")),
		Data: []byte("historical"), LedgerSeq: 1,
	}
	for _, node := range []*nodestore.Node{live, historical} {
		if err := db.Store(ctx, node); err != nil {
			t.Fatal(err)
		}
	}

	state, err := shamapstore.New(false, "")
	if err != nil {
		t.Fatal(err)
	}
	rot := shamapstore.NewRotator(
		state,
		db,
		nil,
		shamapstore.RotationConfig{DeleteInterval: 10},
		nil,
	)
	liveHashes := make([]nodestore.Hash256, 0, 2)
	liveHashes = append(liveHashes, live.Hash)
	rot.SetStateRefresh(func(ctx context.Context, seq uint32, _ func(context.Context, time.Duration) error) (uint32, error) {
		for _, hash := range liveHashes {
			node, err := db.FetchForPromotion(ctx, hash)
			if err != nil {
				return 0, err
			}
			if node == nil {
				return 0, fmt.Errorf("live node %x is missing", hash[:8])
			}
		}
		return seq, db.Sync(ctx)
	}, nil, nil)

	rot.NotifyForTest(1)
	rot.NotifyForTest(11)

	newLive := &nodestore.Node{
		Type: nodestore.NodeAccount, Hash: testNodeHash([]byte("new-live")),
		Data: []byte("new-live"), LedgerSeq: 12,
	}
	if err := db.Store(ctx, newLive); err != nil {
		t.Fatal(err)
	}
	liveHashes = append(liveHashes, newLive.Hash)
	rot.NotifyForTest(21)

	if node, err := db.Fetch(ctx, historical.Hash); err != nil {
		t.Fatal(err)
	} else if node != nil {
		t.Fatal("unpromoted historical node survived retirement of its generation")
	}
	for _, want := range []*nodestore.Node{live, newLive} {
		node, err := db.Fetch(ctx, want.Hash)
		if err != nil {
			t.Fatal(err)
		}
		if node == nil || string(node.Data) != string(want.Data) {
			t.Fatalf("live node after rotation = %+v, want %q", node, want.Data)
		}
	}
	if writes := db.Stats().Writes; writes != 3 {
		t.Fatalf("logical writes = %d, want 3; promotions must not count as stores", writes)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	reopenedBackend, err := kvpebble.NewRotating(
		path,
		kvpebble.Options{BlockCacheBytes: 16 << 20, MaxOpenFiles: 200},
	)
	if err != nil {
		t.Fatal(err)
	}
	reopened := testRotatingNodeDatabase(t, reopenedBackend, 32)
	defer reopened.Close()
	for _, want := range []*nodestore.Node{live, newLive} {
		node, err := reopened.Fetch(ctx, want.Hash)
		if err != nil || node == nil {
			t.Fatalf("reopened live node %x = %+v, %v", want.Hash[:8], node, err)
		}
	}
}

func TestRotation_RealGenerationPreservesHigherExistingFloor(t *testing.T) {
	ctx := context.Background()
	rotating, err := kvpebble.NewRotating(
		filepath.Join(t.TempDir(), "nodes"),
		kvpebble.Options{BlockCacheBytes: 16 << 20, MaxOpenFiles: 200},
	)
	if err != nil {
		t.Fatal(err)
	}
	db := testRotatingNodeDatabase(t, rotating, 32)
	defer db.Close()
	committed, err := db.RotateGeneration(ctx, 800, 501)
	if err != nil {
		t.Fatal(err)
	}
	if !committed {
		t.Fatal("initial generation rotation did not commit")
	}

	state, err := shamapstore.New(false, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := state.SetRotation(500, 900); err != nil {
		t.Fatal(err)
	}
	rot := shamapstore.NewRotator(
		state,
		db,
		nil,
		shamapstore.RotationConfig{DeleteInterval: 256},
		nil,
	)
	rot.SetStateRefresh(
		func(_ context.Context, seq uint32, _ func(context.Context, time.Duration) error) (uint32, error) {
			return seq, db.Sync(ctx)
		},
		nil,
		nil,
	)

	rot.NotifyForTest(1100)

	lastRotated, minimumOnline := db.GenerationState()
	if lastRotated != 1100 {
		t.Fatalf("generation lastRotated = %d, want 1100", lastRotated)
	}
	if minimumOnline != 900 {
		t.Fatalf("generation minimumOnline = %d, want existing floor 900", minimumOnline)
	}
	if got := rot.MinimumOnline(); got != 900 {
		t.Fatalf("rotator minimumOnline = %d, want existing floor 900", got)
	}
}

func TestRotation_AdvancesAcquisitionFloorBeforeLiveStateRefresh(t *testing.T) {
	ctx := context.Background()
	db := testNodeDatabase(t, 100)
	defer db.Close()
	family := backend.New(db)
	data := []byte("shared-live-node")
	hash := [32]byte(testNodeHash(data))
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
	rot.SetStateRefresh(func(_ context.Context, seq uint32, _ func(context.Context, time.Duration) error) (uint32, error) {
		err := family.StoreBatch(ctx, []shamap.FlushEntry{{
			Hash: hash, Data: data, LedgerSeq: 150, MapType: shamap.TypeState,
		}})
		if !errors.Is(err, backend.ErrStoreBelowMinimum) {
			t.Fatalf("historical write during refresh = %v, want %v", err, backend.ErrStoreBelowMinimum)
		}
		err = family.StoreBatch(ctx, []shamap.FlushEntry{{
			Hash: hash, Data: data, LedgerSeq: seq, MapType: shamap.TypeState,
		}})
		return seq, err
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
