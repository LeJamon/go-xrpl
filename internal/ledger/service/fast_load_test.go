package service

import (
	"context"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/crypto/sha512half"
	"github.com/LeJamon/go-xrpl/internal/ledger/genesis"
	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/LeJamon/go-xrpl/storage/kvstore/memorydb"
	"github.com/LeJamon/go-xrpl/storage/nodestore"
	sqlitedb "github.com/LeJamon/go-xrpl/storage/relationaldb/sqlite"
	"github.com/stretchr/testify/require"
)

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
	require.Equal(t, uint32(1), svc.GetValidatedLedgerIndex())
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
	require.Equal(t, uint32(1), reader.GetValidatedLedgerIndex())
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
	require.Equal(t, uint32(1), second.GetValidatedLedgerIndex())
}
