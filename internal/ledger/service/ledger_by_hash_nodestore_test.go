package service

import (
	"context"
	"encoding/hex"
	"errors"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/crypto/sha512half"
	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/genesis"
	"github.com/LeJamon/go-xrpl/internal/ledger/service/svcerr"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/protocol"
	shamapbackend "github.com/LeJamon/go-xrpl/shamap/backend"
	"github.com/LeJamon/go-xrpl/storage/nodestore"
	"github.com/LeJamon/go-xrpl/storage/relationaldb"
	"github.com/stretchr/testify/require"
)

type fetchErrorDatabase struct {
	nodestore.Database
	err error
}

func (d *fetchErrorDatabase) Fetch(context.Context, nodestore.Hash256) (*nodestore.Node, error) {
	return nil, d.err
}

func TestService_GetLedgerByHashLoadsEvictedLedgerFromNodeStore(t *testing.T) {
	ctx := context.Background()
	db := newTestNodeStore(t, 10_000)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	rm := newTestRepositories(t, ctx)

	genesisConfig := genesis.DefaultConfig()
	genesisConfig.Amendments = append(genesisConfig.Amendments, amendment.FeatureXRPFees)
	svc, err := New(Config{
		Standalone:    true,
		Startup:       StartupConfig{Mode: StartupFresh},
		GenesisConfig: genesisConfig,
		NodeStore:     db,
		SHAMapFamily:  shamapbackend.New(db),
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
	feeData, err := state.SerializeFeeSettings(&state.FeeSettings{
		XRPFeesMode:           true,
		BaseFeeDrops:          17,
		ReserveBaseDrops:      23_000_000,
		ReserveIncrementDrops: 4_000_000,
	})
	require.NoError(t, err)
	require.NoError(t, svc.openLedger.Update(keylet.Fees(), feeData))
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
	baseFee, reserveBase, reserveIncrement := readFeesFromLedger(want)
	require.Equal(t, drops.Fees{
		Base:      drops.XRPAmount(baseFee),
		Reserve:   drops.XRPAmount(reserveBase),
		Increment: drops.XRPAmount(reserveIncrement),
	}, got.GetFees())

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
	persisted := svc.persistedLedgers[wantHash]
	require.NotNil(t, persisted)
	require.Equal(t, ledger.StateClosed, persisted.State())
	svc.mu.RUnlock()

	cached, err := svc.GetLedgerByHash(wantHash)
	require.NoError(t, err)
	require.NotSame(t, got, cached)
	require.Equal(t, ledger.StateValidated, cached.State())

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

func TestService_GetLedgerByHashRejectsNodeStoreOnlyLedger(t *testing.T) {
	ctx := context.Background()
	db := newTestNodeStore(t, 10_000)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	rm := newTestRepositories(t, ctx)
	svc, err := New(Config{
		Standalone:    true,
		GenesisConfig: genesis.DefaultConfig(),
		NodeStore:     db,
		SHAMapFamily:  shamapbackend.New(db),
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
	require.Nil(t, loaded)
	require.ErrorIs(t, err, ErrLedgerNotFound)

	_, err = svc.GetLedgerEntry(ctx, entryKey, hex.EncodeToString(closedHash[:]))
	require.ErrorIs(t, err, ErrLedgerNotFound)

	require.NoError(t, closed.SetValidated())
	svc.mu.Lock()
	svc.validatedLedger = closed
	svc.mu.Unlock()
	require.NoError(t, svc.persistValidatedLedger(ctx, closed, false))
	persisted, err := svc.GetLedgerByHash(closedHash)
	require.NoError(t, err)
	require.NotSame(t, closed, persisted)
	require.True(t, persisted.IsValidated())
	result, err := svc.GetLedgerEntry(ctx, entryKey, hex.EncodeToString(closedHash[:]))
	require.NoError(t, err)
	require.True(t, result.Validated)
}

func TestService_GetLedgerByHashDoesNotValidateStaleRelationalFork(t *testing.T) {
	ctx := context.Background()
	db := newTestNodeStore(t, 10_000)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	rm := newTestRepositories(t, ctx)
	svc, err := New(Config{
		Standalone:    true,
		GenesisConfig: genesis.DefaultConfig(),
		NodeStore:     db,
		SHAMapFamily:  shamapbackend.New(db),
		RelationalDB:  rm,
	})
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)

	_, err = svc.AcceptLedger(ctx)
	require.NoError(t, err)
	svc.FlushPersists()
	canonical := svc.GetValidatedLedger()
	require.NotNil(t, canonical)

	fork, err := ledger.NewOpen(svc.genesisLedger, canonical.CloseTime().Add(time.Second))
	require.NoError(t, err)
	require.NoError(t, fork.Insert(keylet.Keylet{Key: [32]byte{0xFA}}, []byte("fork-state-payload")))
	require.NoError(t, fork.Close(canonical.CloseTime().Add(time.Second), 0))
	require.NoError(t, fork.SetValidated())
	require.NotEqual(t, canonical.Hash(), fork.Hash())
	require.NoError(t, svc.persistValidatedLedger(ctx, fork, false))

	loaded, err := svc.GetLedgerByHash(fork.Hash())
	require.NoError(t, err)
	require.Equal(t, ledger.StateClosed, loaded.State())
	require.False(t, loaded.IsValidated())
}

func TestService_GetLedgerByHashValidatesHistoricalCanonicalChain(t *testing.T) {
	ctx := context.Background()
	db := newTestNodeStore(t, 100_000)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	rm := newTestRepositories(t, ctx)
	svc, err := New(Config{
		GenesisConfig: genesis.DefaultConfig(),
		NodeStore:     db,
		SHAMapFamily:  shamapbackend.New(db),
		RelationalDB:  rm,
	})
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)

	parent := svc.genesisLedger
	var ledger256, ledger257, ledger512 *ledger.Ledger
	for seq := uint32(2); seq <= 513; seq++ {
		closeTime := protocol.FromRippleTime(seq)
		next, err := ledger.NewOpen(parent, closeTime)
		require.NoError(t, err)
		require.NoError(t, next.Close(closeTime, 0))
		switch seq {
		case 256:
			ledger256 = next
		case 257:
			ledger257 = next
		case 512:
			ledger512 = next
		}
		parent = next
	}
	tip := parent
	for _, l := range []*ledger.Ledger{ledger256, ledger257, ledger512, tip} {
		require.NotNil(t, l)
		require.NoError(t, l.SetValidated())
		require.NoError(t, svc.persistValidatedLedger(ctx, l, l == tip))
	}
	svc.mu.Lock()
	svc.validatedLedger = tip
	svc.persistedLedgers = make(map[[32]byte]*ledger.Ledger)
	svc.persistedLedgerFIFO = nil
	svc.mu.Unlock()

	aligned, err := svc.GetLedgerByHash(ledger256.Hash())
	require.NoError(t, err)
	require.True(t, aligned.IsValidated())
	nonAligned, err := svc.GetLedgerByHash(ledger257.Hash())
	require.NoError(t, err)
	require.True(t, nonAligned.IsValidated())

	fork, err := ledger.NewOpen(ledger256, ledger257.CloseTime().Add(time.Second))
	require.NoError(t, err)
	require.NoError(t, fork.Insert(keylet.Keylet{Key: [32]byte{0xFB}}, []byte("historical-fork-state")))
	require.NoError(t, fork.Close(ledger257.CloseTime().Add(time.Second), 0))
	require.NoError(t, fork.SetValidated())
	require.NoError(t, svc.persistValidatedLedger(ctx, fork, false))
	loadedFork, err := svc.GetLedgerByHash(fork.Hash())
	require.NoError(t, err)
	require.False(t, loadedFork.IsValidated())
	require.Equal(t, ledger.StateClosed, loadedFork.State())
}

func TestService_GetLedgerByHashReturnsNotFoundWithoutPersistedLedger(t *testing.T) {
	t.Run("no nodestore", func(t *testing.T) {
		svc, err := New(DefaultConfig())
		require.NoError(t, err)

		_, err = svc.GetLedgerByHash([32]byte{0x01})
		require.ErrorIs(t, err, ErrLedgerNotFound)
	})

	t.Run("missing nodestore header", func(t *testing.T) {
		ctx := context.Background()
		db := newTestNodeStore(t, 100)
		t.Cleanup(func() { require.NoError(t, db.Close()) })
		rm := newTestRepositories(t, ctx)
		missingHash := [32]byte{0x02}
		require.NoError(t, rm.Ledger().SaveValidatedLedger(ctx, relationaldb.LedgerInfo{
			Hash:     relationaldb.Hash(missingHash),
			Sequence: 2,
		}))
		svc, err := New(Config{
			NodeStore:    db,
			SHAMapFamily: shamapbackend.New(db),
			RelationalDB: rm,
		})
		require.NoError(t, err)

		_, err = svc.GetLedgerByHash(missingHash)
		require.ErrorIs(t, err, ErrLedgerNotFound)
		_, err = svc.GetNFTBuyOffers(ctx, [32]byte{}, hex.EncodeToString(missingHash[:]), 1, "")
		require.ErrorIs(t, err, svcerr.ErrLedgerNotFound)
	})

	t.Run("canceled relational lookup", func(t *testing.T) {
		ctx := context.Background()
		db := newTestNodeStore(t, 100)
		t.Cleanup(func() { require.NoError(t, db.Close()) })
		rm := newTestRepositories(t, ctx)
		svc, err := New(Config{
			NodeStore:    db,
			SHAMapFamily: shamapbackend.New(db),
			RelationalDB: rm,
		})
		require.NoError(t, err)
		canceled, cancel := context.WithCancel(ctx)
		cancel()
		_, err = svc.GetLedgerByHashContext(canceled, [32]byte{0x04})
		require.ErrorIs(t, err, context.Canceled)
		require.False(t, errors.Is(err, ErrLedgerNotFound))
	})

	t.Run("nodestore failure remains operational", func(t *testing.T) {
		ctx := context.Background()
		backend := newTestNodeStore(t, 100)
		t.Cleanup(func() { require.NoError(t, backend.Close()) })
		fetchErr := errors.New("nodestore unavailable")
		db := &fetchErrorDatabase{Database: backend, err: fetchErr}
		rm := newTestRepositories(t, ctx)
		wantHash := [32]byte{0x05}
		require.NoError(t, rm.Ledger().SaveValidatedLedger(ctx, relationaldb.LedgerInfo{
			Hash:     relationaldb.Hash(wantHash),
			Sequence: 2,
		}))
		svc, err := New(Config{
			NodeStore:    db,
			SHAMapFamily: shamapbackend.New(db),
			RelationalDB: rm,
		})
		require.NoError(t, err)

		_, err = svc.GetLedgerByHashContext(ctx, wantHash)
		require.ErrorIs(t, err, fetchErr)
		require.False(t, errors.Is(err, ErrLedgerNotFound))
	})

	t.Run("nodestore cancellation remains cancellation", func(t *testing.T) {
		ctx := context.Background()
		backend := newTestNodeStore(t, 100)
		t.Cleanup(func() { require.NoError(t, backend.Close()) })
		db := &fetchErrorDatabase{Database: backend, err: context.Canceled}
		rm := newTestRepositories(t, ctx)
		wantHash := [32]byte{0x06}
		require.NoError(t, rm.Ledger().SaveValidatedLedger(ctx, relationaldb.LedgerInfo{
			Hash:     relationaldb.Hash(wantHash),
			Sequence: 2,
		}))
		svc, err := New(Config{
			NodeStore:    db,
			SHAMapFamily: shamapbackend.New(db),
			RelationalDB: rm,
		})
		require.NoError(t, err)

		_, err = svc.GetLedgerByHashContext(ctx, wantHash)
		require.ErrorIs(t, err, context.Canceled)
		require.False(t, errors.Is(err, ErrLedgerNotFound))
	})
}

func TestService_GetLedgerByHashTreatsCorruptPersistedHeaderAsNotFound(t *testing.T) {
	ctx := context.Background()
	db := newTestNodeStore(t, 100)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	rm := newTestRepositories(t, ctx)
	wantHash := [32]byte{0x03}
	require.NoError(t, rm.Ledger().SaveValidatedLedger(ctx, relationaldb.LedgerInfo{
		Hash:     relationaldb.Hash(wantHash),
		Sequence: 1,
	}))
	require.NoError(t, db.Store(ctx, &nodestore.Node{
		Type: nodestore.NodeLedger,
		Hash: nodestore.Hash256(wantHash),
		Data: []byte("corrupt"),
	}))
	svc, err := New(Config{
		NodeStore:    db,
		SHAMapFamily: shamapbackend.New(db),
		RelationalDB: rm,
	})
	require.NoError(t, err)

	_, err = svc.GetLedgerByHash(wantHash)
	require.ErrorIs(t, err, ErrLedgerNotFound)
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
