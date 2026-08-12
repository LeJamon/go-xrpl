package service

import (
	"context"
	"testing"

	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/ledger/skiplist"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/shamap"
	shamapbackend "github.com/LeJamon/go-xrpl/shamap/backend"
	"github.com/LeJamon/go-xrpl/storage/relationaldb"
	sqlitedb "github.com/LeJamon/go-xrpl/storage/relationaldb/sqlite"
)

type cleanerMissingFamily struct {
	shamap.Family
	missing bool
}

func (f *cleanerMissingFamily) Fetch(ctx context.Context, hash [32]byte) ([]byte, error) {
	if f.missing {
		return nil, nil
	}
	return f.Family.Fetch(ctx, hash)
}

func TestCleanerLedgerUsesReacquiredCanonicalLedger(t *testing.T) {
	svc, err := New(DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Stop)
	const seq uint32 = 10
	parent := [32]byte{0x99}
	canonical := makeStubLedger(t, seq, [32]byte{0xAA}, parent)
	wrong := makeStubLedger(t, seq, [32]byte{0xBB}, parent)

	svc.mu.Lock()
	svc.historyComponent.mu.Lock()
	svc.validatedLedger = canonical
	svc.putHistoryLocked(wrong)
	svc.historyComponent.mu.Unlock()
	svc.mu.Unlock()

	stateMap, err := canonical.StateMapSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	txMap, err := canonical.TxMapSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	header := canonical.Header()
	if err := svc.StoreLedgerWithState(context.Background(), &header, stateMap, txMap); err != nil {
		t.Fatal(err)
	}

	got, err := svc.CleanerLedger(context.Background(), seq)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Hash() != canonical.Hash() {
		t.Fatalf("CleanerLedger(%d) = %v, want hash %x", seq, got, canonical.Hash())
	}
	svc.historyComponent.mu.RLock()
	defer svc.historyComponent.mu.RUnlock()
	if indexed := svc.ledgerHistory[seq]; indexed != wrong {
		t.Fatal("generic reacquisition unexpectedly replaced history before cleaner repair")
	}
}

func TestCleanerReacquisitionPreservesValidatedLedger(t *testing.T) {
	svc, err := New(DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	const seq uint32 = 10
	parent := [32]byte{0x99}
	validated := makeStubLedger(t, seq, [32]byte{0xAA}, parent)
	if !validated.IsValidated() {
		t.Fatal("test ledger is not validated")
	}
	stateMap, err := validated.StateMapSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	txMap, err := validated.TxMapSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	header := validated.Header()
	header.Validated = false
	reacquired, err := ledger.NewFromHeader(header, stateMap, txMap, drops.Fees{})
	if err != nil {
		t.Fatal(err)
	}
	if reacquired.IsValidated() {
		t.Fatal("test reacquisition is unexpectedly validated")
	}

	svc.mu.Lock()
	svc.historyComponent.mu.Lock()
	svc.validatedLedger = validated
	svc.cachePersistedLedgerLocked(validated)
	svc.cachePersistedLedgerLocked(reacquired)
	svc.historyComponent.mu.Unlock()
	svc.mu.Unlock()

	got, err := svc.CleanerLedger(context.Background(), seq)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || !got.IsValidated() {
		t.Fatalf("CleanerLedger(%d) = %v, want validated ledger", seq, got)
	}
	if _, err := svc.RepairCleanerLedgerIndex(context.Background(), seq, validated.Hash(), parent); err != nil {
		t.Fatal(err)
	}
	svc.historyComponent.mu.RLock()
	defer svc.historyComponent.mu.RUnlock()
	if indexed := svc.ledgerHistory[seq]; indexed == nil || !indexed.IsValidated() {
		t.Fatal("cleaner index repair downgraded validated history")
	}
}

func TestCleanerReacquireTargetRepairsMissingCanonicalProof(t *testing.T) {
	stateMap := shamap.New(shamap.TypeState)
	hashes := make([][32]byte, 9)
	for i := range hashes {
		hashes[i][0] = byte(i + 1)
	}
	if err := skiplist.Write(stateMap, keylet.LedgerHashes().Key, nil, hashes, 9); err != nil {
		t.Fatal(err)
	}
	for i := byte(1); i <= 16; i++ {
		key := [32]byte{i, 0xA5}
		data := make([]byte, 12)
		data[0] = i
		if err := stateMap.Put(key, data); err != nil {
			t.Fatal(err)
		}
	}
	stateRoot, err := stateMap.Hash()
	if err != nil {
		t.Fatal(err)
	}
	batch, err := stateMap.FlushDirty()
	if err != nil {
		t.Fatal(err)
	}
	baseFamily := shamapbackend.NewMemory()
	if err := baseFamily.StoreBatch(context.Background(), batch.Entries); err != nil {
		t.Fatal(err)
	}
	family := &cleanerMissingFamily{Family: baseFamily}
	backedState, err := shamap.NewFromRootHash(shamap.TypeState, stateRoot, family)
	if err != nil {
		t.Fatal(err)
	}
	txMap := shamap.New(shamap.TypeTransaction)
	txRoot, err := txMap.Hash()
	if err != nil {
		t.Fatal(err)
	}
	tipHash := [32]byte{0xAA}
	tip, err := ledger.NewFromHeader(header.LedgerHeader{
		LedgerIndex: 10,
		Hash:        tipHash,
		ParentHash:  hashes[8],
		AccountHash: stateRoot,
		TxHash:      txRoot,
		Validated:   true,
	}, backedState, txMap, drops.Fees{})
	if err != nil {
		t.Fatal(err)
	}
	family.missing = true

	svc, err := New(DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	svc.mu.Lock()
	svc.validatedLedger = tip
	svc.mu.Unlock()

	hash, seq, ok, err := svc.CleanerReacquireTarget(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || hash != tipHash || seq != tip.Sequence() {
		t.Fatalf("CleanerReacquireTarget() = (%x, %d, %t), want (%x, %d, true)", hash, seq, ok, tipHash, tip.Sequence())
	}
}

func TestCleanerReacquireTargetUsesResolvedTargetBehindLocalAnchor(t *testing.T) {
	svc, err := New(DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	const targetSeq uint32 = 300
	const anchorSeq uint32 = 512
	targetHash := [32]byte{0x30}
	anchorState := shamap.New(shamap.TypeState)
	hashes := make([][32]byte, 256)
	hashes[targetSeq-(anchorSeq-256)] = targetHash
	if err := skiplist.Write(anchorState, keylet.LedgerHashes().Key, nil, hashes, anchorSeq-1); err != nil {
		t.Fatal(err)
	}
	anchorRoot, err := anchorState.Hash()
	if err != nil {
		t.Fatal(err)
	}
	txMap := shamap.New(shamap.TypeTransaction)
	txRoot, err := txMap.Hash()
	if err != nil {
		t.Fatal(err)
	}
	anchorHash := [32]byte{0x51}
	anchor, err := ledger.NewFromHeader(header.LedgerHeader{
		LedgerIndex: anchorSeq,
		Hash:        anchorHash,
		AccountHash: anchorRoot,
		TxHash:      txRoot,
		Validated:   true,
	}, anchorState, txMap, drops.Fees{})
	if err != nil {
		t.Fatal(err)
	}
	tipState := shamap.New(shamap.TypeState)
	if err := skiplist.Write(tipState, keylet.LedgerHashesForSeq(anchorSeq).Key, nil, [][32]byte{{0x25}, anchorHash}, anchorSeq); err != nil {
		t.Fatal(err)
	}
	tipRoot, err := tipState.Hash()
	if err != nil {
		t.Fatal(err)
	}
	tip, err := ledger.NewFromHeader(header.LedgerHeader{
		LedgerIndex: 800,
		Hash:        [32]byte{0x80},
		AccountHash: tipRoot,
		TxHash:      txRoot,
		Validated:   true,
	}, tipState, txMap, drops.Fees{})
	if err != nil {
		t.Fatal(err)
	}

	svc.mu.Lock()
	svc.historyComponent.mu.Lock()
	svc.validatedLedger = tip
	svc.cachePersistedLedgerLocked(anchor)
	svc.historyComponent.mu.Unlock()
	svc.mu.Unlock()

	hash, seq, ok, err := svc.CleanerReacquireTarget(context.Background(), targetSeq)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || hash != targetHash || seq != targetSeq {
		t.Fatalf("CleanerReacquireTarget() = (%x, %d, %t), want (%x, %d, true)", hash, seq, ok, targetHash, targetSeq)
	}
}

func TestAvailableLedgerRangeUsesCompletePublishedRange(t *testing.T) {
	svc, err := New(DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, ok := svc.AvailableLedgerRange(); ok {
		t.Fatal("range available before any ledger was published")
	}

	svc.mu.Lock()
	svc.publishedLedgerSeq = 100
	svc.havePublished = true
	svc.mu.Unlock()
	svc.completeMu.Lock()
	svc.completedLedgers.addRange(10, 50)
	svc.completedLedgers.addRange(60, 100)
	svc.completeMu.Unlock()

	minLedger, maxLedger, ok := svc.AvailableLedgerRange()
	if !ok || minLedger != 60 || maxLedger != 100 {
		t.Fatalf("AvailableLedgerRange() = (%d, %d, %t), want (60, 100, true)", minLedger, maxLedger, ok)
	}
}

func TestRepairCleanerLedgerIndexReplacesWrongHistoryMapping(t *testing.T) {
	svc, err := New(DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	seq := uint32(10)
	hash := [32]byte{0xAA}
	parent := [32]byte{0x99}
	canonical := makeStubLedger(t, seq, hash, parent)
	wrong := makeStubLedger(t, seq, [32]byte{0xBB}, parent)

	svc.mu.Lock()
	svc.historyComponent.mu.Lock()
	svc.ledgerHistory[seq] = wrong
	svc.ledgerByHash[wrong.Hash()] = seq
	svc.persistedLedgers[hash] = canonical
	svc.historyComponent.mu.Unlock()
	svc.mu.Unlock()

	repaired, err := svc.RepairCleanerLedgerIndex(
		context.Background(), seq, hash, parent,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !repaired {
		t.Fatal("wrong history mapping was not reported as repaired")
	}

	svc.historyComponent.mu.RLock()
	defer svc.historyComponent.mu.RUnlock()
	if got := svc.ledgerHistory[seq]; got != canonical {
		t.Fatalf("ledgerHistory[%d] = %p, want canonical %p", seq, got, canonical)
	}
	if got := svc.ledgerByHash[hash]; got != seq {
		t.Fatalf("ledgerByHash[%x] = %d, want %d", hash[:4], got, seq)
	}
	if _, ok := svc.ledgerByHash[wrong.Hash()]; ok {
		t.Fatal("stale hash-to-sequence mapping survived repair")
	}
}

func TestCleanerRepairRewritesMissingRelationalLedger(t *testing.T) {
	ctx := context.Background()
	repositories, err := sqlitedb.NewRepositoryManager(ctx, t.TempDir(), sqlitedb.Settings{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repositories.Close() })

	cfg := DefaultConfig()
	cfg.RelationalDB = repositories
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	seq := uint32(10)
	hash := [32]byte{0xAA}
	parent := [32]byte{0x99}
	canonical := makeStubLedger(t, seq, hash, parent)
	svc.mu.Lock()
	svc.historyComponent.mu.Lock()
	svc.putHistoryLocked(canonical)
	svc.historyComponent.mu.Unlock()
	svc.mu.Unlock()

	repairTxns, err := svc.RepairCleanerLedgerIndex(ctx, seq, hash, parent)
	if err != nil {
		t.Fatal(err)
	}
	if !repairTxns {
		t.Fatal("missing relational ledger did not force transaction repair")
	}
	if err := svc.RepairLedgerTransactions(ctx, seq); err != nil {
		t.Fatal(err)
	}

	info, err := repositories.Ledger().GetLedgerInfoBySeq(
		ctx, relationaldb.LedgerIndex(seq),
	)
	if err != nil {
		t.Fatal(err)
	}
	if [32]byte(info.Hash) != hash || [32]byte(info.ParentHash) != parent {
		t.Fatalf("repaired ledger info = hash %x parent %x", info.Hash[:4], info.ParentHash[:4])
	}
}
