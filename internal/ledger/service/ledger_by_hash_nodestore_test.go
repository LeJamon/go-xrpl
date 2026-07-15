package service

import (
	"context"
	"encoding/hex"
	"errors"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/crypto/sha512half"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/genesis"
	"github.com/LeJamon/go-xrpl/internal/ledger/service/svcerr"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/LeJamon/go-xrpl/storage/kvstore/memorydb"
	"github.com/LeJamon/go-xrpl/storage/nodestore"
	sqlitedb "github.com/LeJamon/go-xrpl/storage/relationaldb/sqlite"
	"github.com/stretchr/testify/require"
)

func TestService_GetLedgerByHashLoadsEvictedLedgerFromNodeStore(t *testing.T) {
	ctx := context.Background()
	db := nodestore.NewKVDatabase(memorydb.New(), "ledger-by-hash", 10_000, time.Hour)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	rm, err := sqlitedb.NewRepositoryManager(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, rm.Open(ctx))
	t.Cleanup(func() { require.NoError(t, rm.Close(ctx)) })

	svc, err := New(Config{
		Standalone:    true,
		GenesisConfig: genesis.DefaultConfig(),
		NodeStore:     db,
		SHAMapFamily:  shamap.NewNodeStoreFamily(db),
		RelationalDB:  rm,
	})
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)

	txBlob := []byte("persisted-transaction")
	txHash := sha512half.Sum(protocol.HashPrefixTransactionID().Bytes(), txBlob)
	require.NoError(t, svc.openLedger.AddTransaction(txHash, txBlob))
	entryKey := [32]byte{0xAA, 0xBB, 0xCC}
	entryData := []byte("persisted-state")
	require.NoError(t, svc.openLedger.Insert(keylet.Keylet{Key: entryKey}, entryData))
	_, err = svc.AcceptLedger(ctx)
	require.NoError(t, err)
	svc.FlushPersists()

	want := svc.GetValidatedLedger()
	require.NotNil(t, want)
	wantHash := want.Hash()
	wantStateHash, err := want.StateMapHash()
	require.NoError(t, err)
	wantTxHash, err := want.TxMapHash()
	require.NoError(t, err)

	svc.mu.Lock()
	svc.evictOldHistoryLocked(want.Sequence() + historyWindow)
	_, historyPresent := svc.ledgerHistory[want.Sequence()]
	_, indexPresent := svc.ledgerByHash[wantHash]
	svc.mu.Unlock()
	require.False(t, historyPresent)
	require.False(t, indexPresent)
	conflicting := buildLedgerWithState(t, want.Sequence())
	require.NotEqual(t, wantHash, conflicting.Hash())
	svc.mu.Lock()
	svc.putHistoryLocked(conflicting)
	svc.mu.Unlock()

	entryResult, err := svc.GetLedgerEntry(ctx, entryKey, hex.EncodeToString(wantHash[:]))
	require.NoError(t, err)
	require.Equal(t, entryData, entryResult.Node)
	require.Equal(t, wantHash, entryResult.LedgerHash)

	got, err := svc.GetLedgerByHash(wantHash)
	require.NoError(t, err)
	require.NotSame(t, want, got)
	require.Equal(t, ledger.StateValidated, got.State())
	require.Equal(t, wantHash, got.Hash())

	gotStateHash, err := got.StateMapHash()
	require.NoError(t, err)
	require.Equal(t, wantStateHash, gotStateHash)
	gotTxHash, err := got.TxMapHash()
	require.NoError(t, err)
	require.Equal(t, wantTxHash, gotTxHash)
	gotTx, ok, err := got.GetTransaction(txHash)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, txBlob, gotTx)

	svc.mu.RLock()
	require.Same(t, conflicting, svc.ledgerHistory[got.Sequence()])
	_, indexPresent = svc.ledgerByHash[wantHash]
	require.False(t, indexPresent)
	require.Same(t, got, svc.persistedLedgers[wantHash])
	svc.mu.RUnlock()

	cached, err := svc.GetLedgerByHash(wantHash)
	require.NoError(t, err)
	require.Same(t, got, cached)

	clearPersistedCache := func() {
		svc.mu.Lock()
		delete(svc.persistedLedgers, wantHash)
		svc.persistedLedgerFIFO = nil
		svc.mu.Unlock()
	}
	clearPersistedCache()
	accountInfo, err := svc.GetAccountInfo(ctx, "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh", hex.EncodeToString(wantHash[:]))
	require.NoError(t, err)
	require.NotNil(t, accountInfo)

	clearPersistedCache()
	ledgerData, err := svc.GetLedgerData(ctx, hex.EncodeToString(wantHash[:]), 1, "")
	require.NoError(t, err)
	require.Equal(t, wantHash, ledgerData.LedgerHash)
}

func TestService_GetLedgerByHashDoesNotValidateUnprovenNodeStoreLedger(t *testing.T) {
	ctx := context.Background()
	db := nodestore.NewKVDatabase(memorydb.New(), "ledger-by-hash-unvalidated", 10_000, time.Hour)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	rm, err := sqlitedb.NewRepositoryManager(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, rm.Open(ctx))
	t.Cleanup(func() { require.NoError(t, rm.Close(ctx)) })
	svc, err := New(Config{
		Standalone:    true,
		GenesisConfig: genesis.DefaultConfig(),
		NodeStore:     db,
		SHAMapFamily:  shamap.NewNodeStoreFamily(db),
		RelationalDB:  rm,
	})
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)

	entryKey := [32]byte{0xDD, 0xEE, 0xFF}
	entryData := []byte("unvalidated-state")
	require.NoError(t, svc.openLedger.Insert(keylet.Keylet{Key: entryKey}, entryData))
	require.NoError(t, svc.openLedger.Close(time.Now(), 0))
	closed := svc.openLedger
	require.False(t, closed.IsValidated())
	svc.enqueueNodePersist(closed)
	svc.FlushPersists()

	svc.mu.Lock()
	svc.putHistoryLocked(closed)
	svc.deleteHistoryLocked(closed.Sequence())
	svc.mu.Unlock()

	closedHash := closed.Hash()
	loaded, err := svc.GetLedgerByHash(closedHash)
	require.NoError(t, err)
	require.Equal(t, ledger.StateClosed, loaded.State())
	require.False(t, loaded.IsValidated())

	result, err := svc.GetLedgerEntry(ctx, entryKey, hex.EncodeToString(closedHash[:]))
	require.NoError(t, err)
	require.False(t, result.Validated)

	require.NoError(t, closed.SetValidated())
	require.NoError(t, svc.persistValidatedLedger(ctx, closed, false))
	promoted, err := svc.GetLedgerByHash(closedHash)
	require.NoError(t, err)
	require.Same(t, loaded, promoted)
	require.True(t, promoted.IsValidated())
}

func TestService_GetLedgerByHashReturnsNotFoundWithoutPersistedLedger(t *testing.T) {
	t.Run("no nodestore", func(t *testing.T) {
		svc, err := New(DefaultConfig())
		require.NoError(t, err)

		_, err = svc.GetLedgerByHash([32]byte{0x01})
		require.ErrorIs(t, err, ErrLedgerNotFound)
	})

	t.Run("missing nodestore header", func(t *testing.T) {
		db := nodestore.NewKVDatabase(memorydb.New(), "ledger-by-hash-missing", 100, time.Hour)
		t.Cleanup(func() { require.NoError(t, db.Close()) })
		svc, err := New(Config{
			NodeStore:    db,
			SHAMapFamily: shamap.NewNodeStoreFamily(db),
		})
		require.NoError(t, err)

		missingHash := [32]byte{0x02}
		_, err = svc.GetLedgerByHash(missingHash)
		require.ErrorIs(t, err, ErrLedgerNotFound)
		_, err = svc.GetNFTBuyOffers(context.Background(), [32]byte{}, hex.EncodeToString(missingHash[:]), 1, "")
		require.ErrorIs(t, err, svcerr.ErrLedgerNotFound)
	})
}

func TestService_GetLedgerByHashReportsCorruptPersistedHeader(t *testing.T) {
	db := nodestore.NewKVDatabase(memorydb.New(), "ledger-by-hash-corrupt", 100, time.Hour)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	wantHash := [32]byte{0x03}
	require.NoError(t, db.Store(context.Background(), &nodestore.Node{
		Type: nodestore.NodeLedger,
		Hash: nodestore.Hash256(wantHash),
		Data: []byte("corrupt"),
	}))
	svc, err := New(Config{
		NodeStore:    db,
		SHAMapFamily: shamap.NewNodeStoreFamily(db),
	})
	require.NoError(t, err)

	_, err = svc.GetLedgerByHash(wantHash)
	require.Error(t, err)
	require.False(t, errors.Is(err, ErrLedgerNotFound))
}

func TestService_PersistedLedgerHashCacheIsBounded(t *testing.T) {
	svc := &Service{persistedLedgers: make(map[[32]byte]*ledger.Ledger)}
	var firstHash, lastHash [32]byte
	for i := uint32(1); i <= persistedLedgerCacheSize+1; i++ {
		l := buildLedgerWithState(t, i)
		if i == 1 {
			firstHash = l.Hash()
		}
		lastHash = l.Hash()
		svc.cachePersistedLedgerLocked(l)
	}

	require.Len(t, svc.persistedLedgers, persistedLedgerCacheSize)
	require.Len(t, svc.persistedLedgerFIFO, persistedLedgerCacheSize)
	require.NotContains(t, svc.persistedLedgers, firstHash)
	require.Contains(t, svc.persistedLedgers, lastHash)
}
