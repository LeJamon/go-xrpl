package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/crypto/sha512half"
	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/ledger/genesis"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/LeJamon/go-xrpl/storage/kvstore/memorydb"
	"github.com/LeJamon/go-xrpl/storage/nodestore"
	sqlitedb "github.com/LeJamon/go-xrpl/storage/relationaldb/sqlite"
	"github.com/stretchr/testify/require"
)

type corruptDescendantFamily struct {
	inner shamap.Family
	roots map[[32]byte]struct{}
}

type parallelFetchDatabase struct {
	nodestore.Database
	root    nodestore.Hash256
	started chan struct{}
	once    sync.Once
	mu      sync.Mutex
	active  int
	peak    int
}

func (d *parallelFetchDatabase) Fetch(ctx context.Context, hash nodestore.Hash256) (*nodestore.Node, error) {
	if hash == d.root {
		return d.Database.Fetch(ctx, hash)
	}
	d.mu.Lock()
	d.active++
	if d.active > d.peak {
		d.peak = d.active
	}
	if d.active >= 2 {
		d.once.Do(func() { close(d.started) })
	}
	d.mu.Unlock()

	select {
	case <-d.started:
	case <-ctx.Done():
		d.mu.Lock()
		d.active--
		d.mu.Unlock()
		return nil, ctx.Err()
	}
	node, err := d.Database.Fetch(ctx, hash)
	d.mu.Lock()
	d.active--
	d.mu.Unlock()
	return node, err
}

func (d *parallelFetchDatabase) peakFetches() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.peak
}

func (f *corruptDescendantFamily) Fetch(ctx context.Context, hash [32]byte) ([]byte, error) {
	data, err := f.inner.Fetch(ctx, hash)
	if err != nil || data == nil {
		return data, err
	}
	if _, ok := f.roots[hash]; ok {
		return data, nil
	}
	return []byte("corrupt"), nil
}

func (f *corruptDescendantFamily) StoreBatch(ctx context.Context, entries []shamap.FlushEntry) error {
	return f.inner.StoreBatch(ctx, entries)
}

func TestStoredLedgerFeesPreservesDefaultsForAbsentFields(t *testing.T) {
	stateMap := shamap.New(shamap.TypeState)
	feeData, err := binarycodec.EncodeBytes(map[string]any{
		"LedgerEntryType": "FeeSettings",
		"Flags":           uint32(0),
		"BaseFeeDrops":    "17",
	})
	require.NoError(t, err)
	require.NoError(t, stateMap.Put(keylet.Fees().Key, feeData))

	configured := drops.Fees{Base: 23, Reserve: 34_000_000, Increment: 5_000_000}
	fees, err := storedLedgerFees(context.Background(), stateMap, true, configured)
	require.NoError(t, err)
	require.EqualValues(t, 17, fees.Base)
	require.Equal(t, configured.Reserve, fees.Reserve)
	require.Equal(t, configured.Increment, fees.Increment)

	_, err = storedLedgerFees(context.Background(), stateMap, false, configured)
	require.ErrorContains(t, err, "before the amendment is enabled")
}

func TestService_FastLoadRestoresPersistedValidatedLedger(t *testing.T) {
	ctx := context.Background()
	db := nodestore.NewKVDatabase(memorydb.New(), "fast-load", 10_000, time.Hour)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	rm, err := sqlitedb.NewRepositoryManager(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, rm.Open(ctx))
	t.Cleanup(func() { require.NoError(t, rm.Close(ctx)) })

	first, err := New(Config{
		Standalone:    true,
		GenesisConfig: genesis.DefaultConfig(),
		NodeStore:     db,
		SHAMapFamily:  shamap.NewNodeStoreFamily(db),
		RelationalDB:  rm,
	})
	require.NoError(t, err)
	require.NoError(t, first.Start())
	txBlob := []byte("synthetic-tx")
	txHash := sha512half.Sum(protocol.HashPrefixTransactionID().Bytes(), txBlob)
	require.NoError(t, first.openLedger.AddTransaction(txHash, txBlob))
	seq, err := first.AcceptLedger(ctx)
	require.NoError(t, err)
	first.FlushPersists()
	want := first.GetValidatedLedger()
	require.NotNil(t, want)
	wantHash := want.Hash()
	wantCloseTime := want.CloseTime()
	first.Stop()

	second, err := New(Config{
		Standalone:    false,
		GenesisConfig: genesis.DefaultConfig(),
		NodeStore:     db,
		SHAMapFamily:  shamap.NewNodeStoreFamily(db),
		RelationalDB:  rm,
		FastLoad:      true,
	})
	require.NoError(t, err)
	require.NoError(t, second.Start())
	t.Cleanup(second.Stop)

	require.False(t, second.NeedsInitialSync())
	require.Equal(t, seq, second.GetValidatedLedgerIndex())
	require.Equal(t, wantHash, second.GetValidatedLedger().Hash())
	second.SetValidatedLedgerAgeClock(func() time.Time {
		return wantCloseTime.Add(37 * time.Second)
	})
	require.Equal(t, 37*time.Second, second.GetValidatedLedgerAge())
	require.Equal(t, seq+1, second.GetCurrentLedgerIndex())
	gotTx, ok, err := second.GetValidatedLedger().GetTransaction(txHash)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, txBlob, gotTx)
	txResult, err := second.GetTransaction(txHash)
	require.NoError(t, err)
	require.Equal(t, txBlob, txResult.TxData)
	firstSeq, lastSeq, ok := second.AdvertisableLedgerRange()
	require.True(t, ok)
	require.Equal(t, seq, firstSeq)
	require.Equal(t, seq, lastSeq)
}

func TestService_VerifyStoredSHAMapWalksRootBranchesInParallel(t *testing.T) {
	ctx := context.Background()
	db := nodestore.NewKVDatabase(memorydb.New(), "parallel-fast-load", 10_000, time.Hour)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	svc, err := New(Config{
		Standalone:    true,
		GenesisConfig: genesis.DefaultConfig(),
		NodeStore:     db,
		SHAMapFamily:  shamap.NewNodeStoreFamily(db),
	})
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)

	for branch := range shamap.BranchFactor {
		var key [32]byte
		key[0] = byte(branch << 4)
		key[31] = byte(branch + 1)
		data := make([]byte, 12)
		data[11] = byte(branch + 1)
		require.NoError(t, svc.openLedger.Insert(keylet.Keylet{Key: key}, data))
	}
	_, err = svc.AcceptLedger(ctx)
	require.NoError(t, err)
	svc.FlushPersists()
	root, err := svc.GetValidatedLedger().StateMapHash()
	require.NoError(t, err)

	tracked := &parallelFetchDatabase{
		Database: db,
		root:     nodestore.Hash256(root),
		started:  make(chan struct{}),
	}
	svc.nodeStore = tracked
	walkCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	require.NoError(t, svc.verifyStoredSHAMap(walkCtx, root, shamap.TypeState))
	require.Greater(t, tracked.peakFetches(), 1)
	require.LessOrEqual(t, tracked.peakFetches(), shamap.BranchFactor)
}

func TestService_FastLoadFallsBackWhenStorageIsEmpty(t *testing.T) {
	ctx := context.Background()
	db := nodestore.NewKVDatabase(memorydb.New(), "fast-load-empty", 100, time.Hour)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	rm, err := sqlitedb.NewRepositoryManager(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, rm.Open(ctx))
	t.Cleanup(func() { require.NoError(t, rm.Close(ctx)) })

	svc, err := New(Config{
		Standalone:    false,
		GenesisConfig: genesis.DefaultConfig(),
		NodeStore:     db,
		SHAMapFamily:  shamap.NewNodeStoreFamily(db),
		RelationalDB:  rm,
		FastLoad:      true,
	})
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)
	require.True(t, svc.NeedsInitialSync())
	require.Nil(t, svc.GetValidatedLedger())
	require.Zero(t, svc.GetValidatedLedgerIndex())
}

func TestService_FastLoadRejectsRelationalLedgerWithoutValidatedTip(t *testing.T) {
	ctx := context.Background()
	db := nodestore.NewKVDatabase(memorydb.New(), "fast-load-unvalidated", 10_000, time.Hour)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	rm, err := sqlitedb.NewRepositoryManager(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, rm.Open(ctx))
	t.Cleanup(func() { require.NoError(t, rm.Close(ctx)) })

	writer, err := New(Config{
		Standalone:    false,
		GenesisConfig: genesis.DefaultConfig(),
		NodeStore:     db,
		SHAMapFamily:  shamap.NewNodeStoreFamily(db),
		RelationalDB:  rm,
	})
	require.NoError(t, err)
	untrusted := buildLedgerWithState(t, 99)
	require.NoError(t, writer.persistValidatedLedger(ctx, untrusted, false))

	reader, err := New(Config{
		Standalone:    false,
		GenesisConfig: genesis.DefaultConfig(),
		NodeStore:     db,
		SHAMapFamily:  shamap.NewNodeStoreFamily(db),
		RelationalDB:  rm,
		FastLoad:      true,
	})
	require.NoError(t, err)
	require.NoError(t, reader.Start())
	t.Cleanup(reader.Stop)
	require.True(t, reader.NeedsInitialSync())
	require.Nil(t, reader.GetValidatedLedger())
	require.Zero(t, reader.GetValidatedLedgerIndex())
}

func TestService_FastLoadFallsBackWhenTreeIsCorrupt(t *testing.T) {
	ctx := context.Background()
	db := nodestore.NewKVDatabase(memorydb.New(), "fast-load-corrupt", 10_000, time.Hour)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	rm, err := sqlitedb.NewRepositoryManager(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, rm.Open(ctx))
	t.Cleanup(func() { require.NoError(t, rm.Close(ctx)) })

	first, err := New(Config{
		Standalone:    true,
		GenesisConfig: genesis.DefaultConfig(),
		NodeStore:     db,
		SHAMapFamily:  shamap.NewNodeStoreFamily(db),
		RelationalDB:  rm,
	})
	require.NoError(t, err)
	require.NoError(t, first.Start())
	_, err = first.AcceptLedger(ctx)
	require.NoError(t, err)
	first.FlushPersists()
	stateRoot := first.GetValidatedLedger().Header().AccountHash
	first.Stop()

	stored, err := db.Fetch(ctx, nodestore.Hash256(stateRoot))
	require.NoError(t, err)
	require.NotNil(t, stored)
	require.NoError(t, db.Store(ctx, &nodestore.Node{
		Type:      stored.Type,
		Hash:      stored.Hash,
		Data:      []byte("corrupt"),
		LedgerSeq: stored.LedgerSeq,
	}))

	second, err := New(Config{
		Standalone:    false,
		GenesisConfig: genesis.DefaultConfig(),
		NodeStore:     db,
		SHAMapFamily:  shamap.NewNodeStoreFamily(db),
		RelationalDB:  rm,
		FastLoad:      true,
	})
	require.NoError(t, err)
	require.NoError(t, second.Start())
	t.Cleanup(second.Stop)
	require.True(t, second.NeedsInitialSync())
	require.Nil(t, second.GetValidatedLedger())
	require.Zero(t, second.GetValidatedLedgerIndex())
}

func TestService_GetLedgerByHashTreatsCorruptDescendantAsNotFound(t *testing.T) {
	ctx := context.Background()
	db := nodestore.NewKVDatabase(memorydb.New(), "fast-load-corrupt-descendant", 10_000, time.Hour)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	rm, err := sqlitedb.NewRepositoryManager(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, rm.Open(ctx))
	t.Cleanup(func() { require.NoError(t, rm.Close(ctx)) })

	family := shamap.NewNodeStoreFamily(db)
	writer, err := New(Config{
		Standalone:    true,
		GenesisConfig: genesis.DefaultConfig(),
		NodeStore:     db,
		SHAMapFamily:  family,
		RelationalDB:  rm,
	})
	require.NoError(t, err)
	require.NoError(t, writer.Start())
	_, err = writer.AcceptLedger(ctx)
	require.NoError(t, err)
	writer.FlushPersists()
	persisted := writer.GetValidatedLedger()
	wantHash := persisted.Hash()
	hdr := persisted.Header()
	writer.Stop()

	roots := map[[32]byte]struct{}{hdr.AccountHash: {}}
	if hdr.TxHash != ([32]byte{}) {
		roots[hdr.TxHash] = struct{}{}
	}
	reader, err := New(Config{
		Standalone:    false,
		GenesisConfig: genesis.DefaultConfig(),
		NodeStore:     db,
		SHAMapFamily: &corruptDescendantFamily{
			inner: family,
			roots: roots,
		},
		RelationalDB: rm,
	})
	require.NoError(t, err)

	_, err = reader.GetLedgerByHash(wantHash)
	require.ErrorIs(t, err, ErrLedgerNotFound)
	require.False(t, errors.Is(err, shamap.ErrInvalidNodeData))
}
