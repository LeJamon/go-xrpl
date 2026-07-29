package service

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/codec/addresscodec"
	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/LeJamon/go-xrpl/shamap/backend"
	"github.com/LeJamon/go-xrpl/storage/nodestore"
	"github.com/LeJamon/go-xrpl/storage/relationaldb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type controlledSyncDatabase struct {
	nodestore.Database
	fail atomic.Bool
}

type gatedTipDatabase struct {
	nodestore.Database
	once    sync.Once
	entered chan struct{}
	release chan struct{}
}

func (d *gatedTipDatabase) Store(ctx context.Context, node *nodestore.Node) error {
	if node.Hash == validatedTipKey && node.LedgerSeq != 0 {
		d.once.Do(func() {
			close(d.entered)
			<-d.release
		})
	}
	return d.Database.Store(ctx, node)
}

func (d *controlledSyncDatabase) Sync(ctx context.Context) error {
	if d.fail.Load() {
		return errors.New("injected sync failure")
	}
	return d.Database.Sync(ctx)
}

func makeStubLedgerWithTransaction(
	t *testing.T,
	seq uint32,
	hash, parentHash [32]byte,
	account string,
	accountSequence uint32,
) (*ledger.Ledger, [32]byte) {
	t.Helper()

	txHex, err := binarycodec.Encode(map[string]any{
		"TransactionType": "AccountSet",
		"Account":         account,
		"Fee":             "10",
		"Sequence":        accountSequence,
	})
	require.NoError(t, err)
	txBytes, err := hex.DecodeString(txHex)
	require.NoError(t, err)
	txBlob, txID := makeTxMetaBlobForTest(t, txBytes, 0)

	txMap := shamap.New(shamap.TypeTransaction)
	require.NoError(t, txMap.PutWithNodeType(txID, txBlob, shamap.NodeTypeTransactionWithMeta))
	txRoot, err := txMap.Hash()
	require.NoError(t, err)

	hdr := makeStubLedger(t, seq, hash, parentHash).Header()
	hdr.TxHash = txRoot
	l, err := ledger.NewFromHeader(hdr, shamap.New(shamap.TypeState), txMap, drops.Fees{})
	require.NoError(t, err)
	return l, txID
}

func accountIDFromAddress(t *testing.T, address string) relationaldb.AccountID {
	t.Helper()

	_, raw, err := addresscodec.DecodeClassicAddressToAccountID(address)
	require.NoError(t, err)
	require.Len(t, raw, len(relationaldb.AccountID{}))

	var accountID relationaldb.AccountID
	copy(accountID[:], raw)
	return accountID
}

func TestCompleteLedgers_FreshNetworkStartupIsEmpty(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Standalone = false
	svc, err := New(cfg)
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)

	info := svc.GetServerInfo()
	assert.Equal(t, "empty", info.CompleteLedgers)
	assert.False(t, info.HavePublished)
}

func TestCompleteLedgers_PreservesHoles(t *testing.T) {
	svc, err := New(DefaultConfig())
	require.NoError(t, err)

	for _, seq := range []uint32{10, 11, 13, 16, 17} {
		l := makeStubLedger(t, seq, [32]byte{byte(seq)}, [32]byte{})
		require.NoError(t, svc.persistValidatedLedger(context.Background(), l, false))
	}

	assert.Equal(t, "10-11,13,16-17", svc.GetServerInfo().CompleteLedgers)
}

func TestCompleteLedgers_FailedValidatedPersistenceRemovesSequence(t *testing.T) {
	base := newTestNodeStore(t, 100)
	t.Cleanup(func() { require.NoError(t, base.Close()) })
	db := &controlledSyncDatabase{Database: base}

	cfg := DefaultConfig()
	cfg.NodeStore = db
	svc, err := New(cfg)
	require.NoError(t, err)

	l := makeStubLedger(t, 20, [32]byte{0x20}, [32]byte{0x19})
	db.fail.Store(true)
	require.Error(t, svc.persistValidatedLedger(context.Background(), l, false))
	assert.Equal(t, "empty", svc.GetServerInfo().CompleteLedgers)
}

func TestCompleteLedgers_DeduplicatesPendingValidatedPersistence(t *testing.T) {
	svc, err := New(DefaultConfig())
	require.NoError(t, err)
	svc.persistMu.Lock()
	svc.persistStarted = true
	svc.persistMu.Unlock()

	l := makeStubLedger(t, 25, [32]byte{0x25}, [32]byte{0x24})
	svc.enqueueValidatedHistoryPersist(l)
	svc.enqueuePersist(l)

	svc.persistMu.Lock()
	require.Len(t, svc.persistQueue, 1)
	require.Len(t, svc.validatedPersistJobs, 1)
	assert.True(t, svc.persistQueue[0].updatesTip.Load())
	svc.persistMu.Unlock()

	svc.invalidateCompleteLedger(25)
	replacement := makeStubLedger(t, 25, [32]byte{0x26}, [32]byte{0x24})
	svc.enqueuePersist(replacement)

	svc.persistMu.Lock()
	defer svc.persistMu.Unlock()
	require.Len(t, svc.persistQueue, 2)
	assert.Same(t, replacement, svc.validatedPersistJobs[25].l)
}

func TestCompleteLedgers_HistoryPersistenceDoesNotDisplaceCanonicalJob(t *testing.T) {
	svc, err := New(DefaultConfig())
	require.NoError(t, err)
	svc.persistMu.Lock()
	svc.persistStarted = true
	svc.persistMu.Unlock()

	canonical := makeStubLedger(t, 25, [32]byte{0x25}, [32]byte{0x24})
	sibling := makeStubLedger(t, 25, [32]byte{0x26}, [32]byte{0x24})
	svc.enqueuePersist(canonical)
	svc.enqueueValidatedHistoryPersist(sibling)

	svc.persistMu.Lock()
	defer svc.persistMu.Unlock()
	require.Len(t, svc.persistQueue, 1)
	assert.Same(t, canonical, svc.validatedPersistJobs[25].l)
}

func TestCompleteLedgers_InvalidatedQueuedPersistenceSkipsAbandonedFork(t *testing.T) {
	ctx := context.Background()
	db := newTestNodeStore(t, 100)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	repositories := newTestRepositories(t, ctx)

	svc, err := New(Config{
		NodeStore:    db,
		SHAMapFamily: backend.New(db),
		RelationalDB: repositories,
	})
	require.NoError(t, err)
	svc.persistMu.Lock()
	svc.persistStarted = true
	svc.persistMu.Unlock()

	const seq uint32 = 26
	abandoned := makeStubLedger(t, seq, [32]byte{0x25}, [32]byte{0x24})
	replacement := makeStubLedger(t, seq, [32]byte{0x26}, [32]byte{0x24})
	require.NoError(t, svc.persistValidatedTip(ctx, abandoned))
	svc.enqueuePersist(abandoned)
	svc.invalidateCompleteLedger(seq)
	svc.enqueuePersist(replacement)

	svc.persistMu.Lock()
	jobs := append([]*persistJob(nil), svc.persistQueue...)
	svc.persistQueue = nil
	svc.persistMu.Unlock()
	require.Len(t, jobs, 2)
	for _, job := range jobs {
		svc.runPersistJob(job)
	}

	tip, err := db.Fetch(ctx, validatedTipKey)
	require.NoError(t, err)
	require.NotNil(t, tip)
	replacementHash := replacement.Hash()
	assert.Equal(t, replacementHash[:], tip.Data)
	pairs, err := repositories.Ledger().GetHashesByRange(
		ctx, relationaldb.LedgerIndex(seq), relationaldb.LedgerIndex(seq),
	)
	require.NoError(t, err)
	pair, ok := pairs[relationaldb.LedgerIndex(seq)]
	require.True(t, ok)
	assert.Equal(t, replacementHash, [32]byte(pair.LedgerHash))
	assert.Equal(t, "26", svc.completeLedgersString())
}

func TestCompleteLedgers_SameSequenceSwitchKeepsPreferredPersistence(t *testing.T) {
	ctx := context.Background()
	db := newTestNodeStore(t, 100)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	repositories := newTestRepositories(t, ctx)

	cfg := DefaultConfig()
	cfg.Standalone = false
	cfg.NodeStore = db
	cfg.SHAMapFamily = backend.New(db)
	cfg.RelationalDB = repositories
	svc, err := New(cfg)
	require.NoError(t, err)
	svc.persistMu.Lock()
	svc.persistStarted = true
	svc.persistMu.Unlock()
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)

	seq := svc.GetClosedLedgerIndex()
	parentHash := svc.GetClosedLedger().ParentHash()
	abandoned := makeStubLedger(t, seq, [32]byte{0xA1}, parentHash)
	preferred := makeStubLedger(t, seq, [32]byte{0xB1}, parentHash)
	unrelated := makeStubLedger(t, seq, [32]byte{0xC1}, parentHash)
	require.NoError(t, svc.persistValidatedLedger(ctx, abandoned, true))

	svc.mu.Lock()
	svc.closedLedger = abandoned
	svc.putHistoryLocked(abandoned)
	svc.mu.Unlock()
	svc.enqueueValidatedHistoryPersist(unrelated)

	svc.persistMu.Lock()
	require.Len(t, svc.persistQueue, 1)
	unrelatedJob := svc.persistQueue[0]
	svc.persistMu.Unlock()

	require.NoError(t, svc.SwitchToPreferredLedger(preferred))
	assert.True(t, unrelatedJob.canceled.Load())
	svc.persistMu.Lock()
	preferredJob := svc.validatedPersistJobs[seq]
	svc.persistMu.Unlock()
	require.NotNil(t, preferredJob)
	assert.Same(t, preferred, preferredJob.l)

	svc.persistMu.Lock()
	jobs := append([]*persistJob(nil), svc.persistQueue...)
	svc.persistQueue = nil
	svc.persistMu.Unlock()
	require.Len(t, jobs, 2)
	for _, job := range jobs {
		svc.runPersistJob(job)
	}

	tip, err := db.Fetch(ctx, validatedTipKey)
	require.NoError(t, err)
	require.NotNil(t, tip)
	preferredHash := preferred.Hash()
	assert.Equal(t, preferredHash[:], tip.Data)
	pairs, err := repositories.Ledger().GetHashesByRange(
		ctx, relationaldb.LedgerIndex(seq), relationaldb.LedgerIndex(seq),
	)
	require.NoError(t, err)
	pair, ok := pairs[relationaldb.LedgerIndex(seq)]
	require.True(t, ok)
	assert.Equal(t, preferredHash, [32]byte(pair.LedgerHash))
	assert.Equal(t, fmt.Sprint(seq), svc.completeLedgersString())
}

func TestCompleteLedgers_SameSequenceReplacementRebuildsTransactionIndexes(t *testing.T) {
	ctx := context.Background()
	repositories := newTestRepositories(t, ctx)

	cfg := DefaultConfig()
	cfg.RelationalDB = repositories
	svc, err := New(cfg)
	require.NoError(t, err)

	const (
		seq                uint32 = 27
		abandonedAccount          = "r9cZA1mLK5R5Am25ArfXFmqgNwjZgnfk59"
		replacementAccount        = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	)
	parentHash := [32]byte{0x26}
	abandoned, abandonedTx := makeStubLedgerWithTransaction(
		t, seq, [32]byte{0xA1}, parentHash, abandonedAccount, 1,
	)
	replacement, replacementTx := makeStubLedgerWithTransaction(
		t, seq, [32]byte{0xB1}, parentHash, replacementAccount, 2,
	)

	queryAccount := func(address string) []relationaldb.TransactionInfo {
		t.Helper()
		result, err := repositories.AccountTransaction().GetOldestAccountTxsPage(ctx, relationaldb.AccountTxPageOptions{
			Account:   accountIDFromAddress(t, address),
			MinLedger: relationaldb.LedgerIndex(seq),
			MaxLedger: relationaldb.LedgerIndex(seq),
			Limit:     100,
		})
		require.NoError(t, err)
		return result.Transactions
	}

	require.NoError(t, svc.persistValidatedLedger(ctx, abandoned, false))
	abandonedInfo, _, err := repositories.Transaction().GetTransaction(
		ctx, relationaldb.Hash(abandonedTx), nil,
	)
	require.NoError(t, err)
	require.NotNil(t, abandonedInfo)
	require.Len(t, queryAccount(abandonedAccount), 1)

	require.NoError(t, svc.persistValidatedLedger(ctx, replacement, false))

	ledgerInfo, err := repositories.Ledger().GetLedgerInfoBySeq(ctx, relationaldb.LedgerIndex(seq))
	require.NoError(t, err)
	require.NotNil(t, ledgerInfo)
	assert.Equal(t, replacement.Hash(), [32]byte(ledgerInfo.Hash))

	abandonedInfo, _, err = repositories.Transaction().GetTransaction(
		ctx, relationaldb.Hash(abandonedTx), nil,
	)
	require.NoError(t, err)
	assert.Nil(t, abandonedInfo)

	replacementInfo, _, err := repositories.Transaction().GetTransaction(
		ctx, relationaldb.Hash(replacementTx), nil,
	)
	require.NoError(t, err)
	require.NotNil(t, replacementInfo)
	assert.Equal(t, relationaldb.LedgerIndex(seq), replacementInfo.LedgerSeq)

	assert.Empty(t, queryAccount(abandonedAccount))
	replacementAccountTxs := queryAccount(replacementAccount)
	require.Len(t, replacementAccountTxs, 1)
	assert.Equal(t, relationaldb.Hash(replacementTx), replacementAccountTxs[0].Hash)
}

func TestCompleteLedgers_InvalidationRepairsInFlightValidatedTip(t *testing.T) {
	ctx := context.Background()
	base := newTestNodeStore(t, 100)
	t.Cleanup(func() { require.NoError(t, base.Close()) })
	db := &gatedTipDatabase{
		Database: base,
		entered:  make(chan struct{}),
		release:  make(chan struct{}),
	}
	repositories := newTestRepositories(t, ctx)

	svc, err := New(Config{
		NodeStore:    db,
		SHAMapFamily: backend.New(db),
		RelationalDB: repositories,
	})
	require.NoError(t, err)
	svc.persistMu.Lock()
	svc.persistStarted = true
	svc.persistMu.Unlock()

	const seq uint32 = 27
	abandoned := makeStubLedger(t, seq, [32]byte{0x27}, [32]byte{0x26})
	replacement := makeStubLedger(t, seq, [32]byte{0x28}, [32]byte{0x26})
	svc.enqueuePersist(abandoned)
	svc.persistMu.Lock()
	require.Len(t, svc.persistQueue, 1)
	abandonedJob := svc.persistQueue[0]
	svc.persistQueue = nil
	svc.persistMu.Unlock()

	jobDone := make(chan struct{})
	go func() {
		svc.runPersistJob(abandonedJob)
		close(jobDone)
	}()
	select {
	case <-db.entered:
	case <-time.After(time.Second):
		t.Fatal("abandoned persistence did not reach the validated-tip store")
	}

	invalidationDone := make(chan struct{})
	go func() {
		svc.invalidateCompleteLedger(seq)
		close(invalidationDone)
	}()
	require.Eventually(t, abandonedJob.canceled.Load, time.Second, time.Millisecond)
	close(db.release)
	select {
	case <-jobDone:
	case <-time.After(time.Second):
		t.Fatal("abandoned persistence did not finish")
	}
	select {
	case <-invalidationDone:
	case <-time.After(time.Second):
		t.Fatal("invalidation did not repair the persisted tip")
	}

	invalidatedTip, err := db.Fetch(ctx, validatedTipKey)
	require.NoError(t, err)
	require.NotNil(t, invalidatedTip)
	assert.Zero(t, invalidatedTip.LedgerSeq)

	svc.enqueuePersist(replacement)
	svc.persistMu.Lock()
	require.Len(t, svc.persistQueue, 1)
	replacementJob := svc.persistQueue[0]
	svc.persistQueue = nil
	svc.persistMu.Unlock()
	svc.runPersistJob(replacementJob)

	tip, err := db.Fetch(ctx, validatedTipKey)
	require.NoError(t, err)
	require.NotNil(t, tip)
	replacementHash := replacement.Hash()
	assert.Equal(t, replacementHash[:], tip.Data)
	pairs, err := repositories.Ledger().GetHashesByRange(
		ctx, relationaldb.LedgerIndex(seq), relationaldb.LedgerIndex(seq),
	)
	require.NoError(t, err)
	pair, ok := pairs[relationaldb.LedgerIndex(seq)]
	require.True(t, ok)
	assert.Equal(t, replacementHash, [32]byte(pair.LedgerHash))
	assert.Equal(t, "27", svc.completeLedgersString())
}

func TestCompleteLedgers_InvalidationRejectsStalePersistenceCompletion(t *testing.T) {
	svc, err := New(DefaultConfig())
	require.NoError(t, err)

	const seq uint32 = 30
	staleToken := svc.beginValidatedPersistence(seq, [32]byte{0x30})
	svc.recordValidatedPersistence(seq, staleToken, true)
	assert.Equal(t, "30", svc.completeLedgersString())

	svc.invalidateCompleteLedger(seq)
	svc.recordValidatedPersistence(seq, staleToken, true)
	assert.Equal(t, "empty", svc.completeLedgersString())

	currentToken := svc.beginValidatedPersistence(seq, [32]byte{0x31})
	svc.recordValidatedPersistence(seq, currentToken, true)
	assert.Equal(t, "30", svc.completeLedgersString())
}

func TestCompleteLedgers_OnlineDeleteClampRejectsOlderPersists(t *testing.T) {
	svc, err := New(DefaultConfig())
	require.NoError(t, err)

	seedCompleteLedgers(t, svc, 40, 50)
	staleToken := svc.beginValidatedPersistence(44, [32]byte{0x44})
	svc.SetMinimumOnlineFunc(func() uint32 { return 45 })

	assert.Equal(t, "45-50", svc.GetServerInfo().CompleteLedgers)
	svc.recordValidatedPersistence(44, staleToken, true)
	assert.Equal(t, "45-50", svc.GetServerInfo().CompleteLedgers)
}
