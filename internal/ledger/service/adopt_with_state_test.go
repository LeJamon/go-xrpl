package service

import (
	"context"
	"encoding/hex"
	"testing"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/crypto/sha512half"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/LeJamon/go-xrpl/shamap"
	shamapbackend "github.com/LeJamon/go-xrpl/shamap/backend"
	"github.com/LeJamon/go-xrpl/storage/relationaldb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// encodeVLForTest mirrors tx.EncodeVL — duplicated locally so this test
// file stays free of cross-package test-helper imports.
func encodeVLForTest(length int) []byte {
	switch {
	case length <= 192:
		return []byte{byte(length)}
	case length <= 12480:
		l := length - 193
		return []byte{byte((l >> 8) + 193), byte(l & 0xFF)}
	default:
		l := length - 12481
		return []byte{byte((l >> 16) + 241), byte((l >> 8) & 0xFF), byte(l & 0xFF)}
	}
}

// makeTxMetaBlobForTest builds a SHAMap-formatted tx+meta leaf blob
// (VL(tx) + VL(meta)) with the supplied tx bytes. Metadata carries only
// TransactionResult + TransactionIndex, enough for persistToRelationalDB
// to extract txn_seq without mattering to the index build.
// Returns (blob, txID) where txID is the canonical XRPL tx hash used as
// the SHAMap key.
func makeTxMetaBlobForTest(
	t *testing.T,
	txBytes []byte,
	txIndex uint32,
	affectedAccounts ...string,
) ([]byte, [32]byte) {
	t.Helper()

	affectedNodes := make([]any, 0, len(affectedAccounts))
	for _, account := range affectedAccounts {
		affectedNodes = append(affectedNodes, map[string]any{
			"ModifiedNode": map[string]any{
				"FinalFields": map[string]any{
					"Account": account,
				},
			},
		})
	}
	metaHex, err := binarycodec.Encode(map[string]any{
		"TransactionResult": "tesSUCCESS",
		"TransactionIndex":  txIndex,
		"AffectedNodes":     affectedNodes,
	})
	require.NoError(t, err)
	metaBytes, err := hex.DecodeString(metaHex)
	require.NoError(t, err)

	txID := sha512half.Sum(protocol.HashPrefixTransactionID().Bytes(), txBytes)

	blob := make([]byte, 0, len(txBytes)+len(metaBytes)+4)
	blob = append(blob, encodeVLForTest(len(txBytes))...)
	blob = append(blob, txBytes...)
	blob = append(blob, encodeVLForTest(len(metaBytes))...)
	blob = append(blob, metaBytes...)
	return blob, txID
}

// TestAdoptLedgerWithState_PreservesTxMap pins R5.1: the verified
// replay-delta tx map must be installed into the adopted ledger, not
// silently discarded in favor of genesis's empty tx map.
//
// Regression guard against the pre-R5.1 behavior where every
// replay-delta-adopted ledger lost its tx history locally —
// `tx`, `tx_history`, `account_tx`, `transaction_entry` RPCs returned
// nothing and the node couldn't re-serve replay-deltas for adopted
// ledgers.
func TestAdoptLedgerWithState_PreservesTxMap(t *testing.T) {
	cfg := DefaultConfig()
	svc, err := New(cfg)
	require.NoError(t, err)
	require.NoError(t, svc.Start())

	// Build a non-empty tx map: 2 distinct tx leaves with proper
	// VL(tx)+VL(meta) shape so persistLedger and collectTransactionResults
	// can parse them. Keys are canonical XRPL tx hashes so the in-memory
	// tx-index assertion below is meaningful.
	txMap := shamap.New(shamap.TypeTransaction)

	blob1, id1 := makeTxMetaBlobForTest(t, []byte("adopt-tx-blob-A-padding-padding"), 0)
	blob2, id2 := makeTxMetaBlobForTest(t, []byte("adopt-tx-blob-B-padding-padding"), 1)
	require.NoError(t, txMap.PutWithNodeType(id1, blob1, shamap.NodeTypeTransactionWithMeta))
	require.NoError(t, txMap.PutWithNodeType(id2, blob2, shamap.NodeTypeTransactionWithMeta))

	expectedTxRoot, err := txMap.Hash()
	require.NoError(t, err)

	// Build a minimal state map (empty is fine — we're testing the
	// tx-map threading, not state content).
	stateMap := shamap.New(shamap.TypeState)
	expectedStateRoot, err := stateMap.Hash()
	require.NoError(t, err)

	// Construct a header whose TxHash matches the tx map root. The
	// adopted ledger's tx map must hash to this same value.
	hdr := &header.LedgerHeader{
		LedgerIndex: svc.GetClosedLedgerIndex() + 1,
		TxHash:      expectedTxRoot,
		AccountHash: expectedStateRoot,
	}
	hdr.Hash = header.CalculateHash(*hdr)
	adoptedHash := hdr.Hash

	require.NoError(t, svc.AdoptLedgerWithState(context.TODO(), hdr, stateMap, txMap),
		"AdoptLedgerWithState must accept a caller-supplied tx map")

	// The adopted ledger must carry the caller-supplied tx map.
	adopted, err := svc.GetLedgerByHash(adoptedHash)
	require.NoError(t, err)
	require.NotNil(t, adopted)
	assert.False(t, adopted.IsValidated(),
		"peer-adopted ledger must remain closed until validation quorum")

	gotTxRoot, err := adopted.TxMapHash()
	require.NoError(t, err)
	assert.Equal(t, expectedTxRoot, gotTxRoot,
		"adopted ledger must carry the supplied tx map, not genesis's empty one")

	// The in-memory tx-index must now contain exactly the 2 hashes that
	// were installed. Pins F2: without collectTransactionResults being
	// invoked on adopt, hash lookups against adopted ledgers silently
	// fail in the `tx` RPC path.
	assert.Len(t, svc.txIndex, 2,
		"txIndex must contain one entry per adopted tx")
	assert.Equal(t, hdr.LedgerIndex, svc.txIndex[id1],
		"txIndex must map tx1 hash to the adopted ledger's seq")
	assert.Equal(t, hdr.LedgerIndex, svc.txIndex[id2],
		"txIndex must map tx2 hash to the adopted ledger's seq")
}

// TestAdoptLedgerWithState_NilTxMapFallsBackToEmpty verifies the
// legacy header+state catchup path still works: passing nil for the
// tx map installs the genesis-shaped empty tx map. This preserves
// pre-replay-delta behavior for the legacy mtGET_LEDGER path that
// doesn't fetch a per-ledger tx tree.
func TestAdoptLedgerWithState_NilTxMapFallsBackToEmpty(t *testing.T) {
	cfg := DefaultConfig()
	svc, err := New(cfg)
	require.NoError(t, err)
	require.NoError(t, svc.Start())

	stateMap := shamap.New(shamap.TypeState)
	stateRoot, err := stateMap.Hash()
	require.NoError(t, err)

	// Genesis's tx-map root — what the adopted ledger should inherit
	// when the caller passes nil for txMap.
	emptyTxMap, err := svc.GetValidatedLedger().TxMapSnapshot()
	require.NoError(t, err)
	emptyTxRoot, err := emptyTxMap.Hash()
	require.NoError(t, err)

	hdr := &header.LedgerHeader{
		LedgerIndex: svc.GetClosedLedgerIndex() + 1,
		TxHash:      emptyTxRoot,
		AccountHash: stateRoot,
	}
	hdr.Hash = header.CalculateHash(*hdr)
	adoptedHash := hdr.Hash

	require.NoError(t, svc.AdoptLedgerWithState(context.TODO(), hdr, stateMap, nil),
		"AdoptLedgerWithState must accept nil txMap (legacy catchup path)")

	adopted, err := svc.GetLedgerByHash(adoptedHash)
	require.NoError(t, err)
	gotTxRoot, err := adopted.TxMapHash()
	require.NoError(t, err)
	assert.Equal(t, emptyTxRoot, gotTxRoot,
		"nil txMap must fall back to the genesis-shaped empty tx map")
}

// TestAdoptLedgerWithState_PersistsToRelationalDB pins F1: validating an
// adopted ledger with a tx map must flush those transactions to the
// RelationalDB so `tx`, `account_tx`, `tx_history`, and
// `transaction_entry` RPCs can answer queries against peer-adopted
// ledgers. Before F1, the adopt path never called persistLedger and
// every adopted ledger's txs were invisible to RPC consumers that hit
// the DB instead of in-memory state.
//
// Mirrors rippled's setFullLedger -> pendSaveValidated chain
// (LedgerMaster.cpp:831).
func TestAdoptLedgerWithState_PersistsToRelationalDB(t *testing.T) {
	ctx := context.Background()

	// Spin up an on-disk (temp-dir) SQLite repository manager — sqlite
	// is the supported test backend; there is no in-memory variant.
	rm := newTestRepositories(t, ctx)

	cfg := DefaultConfig()
	cfg.RelationalDB = rm
	svc, err := New(cfg)
	require.NoError(t, err)
	require.NoError(t, svc.Start())

	// Two txs with canonical-hash keys so the DB row's trans_id column
	// matches the hash we query for.
	txMap := shamap.New(shamap.TypeTransaction)
	raw1, _ := validRelationalTestTransaction(t, 1)
	raw2, _ := validRelationalTestTransaction(t, 2)
	blob1, id1 := makeTxMetaBlobForTest(t, raw1, 0)
	blob2, id2 := makeTxMetaBlobForTest(t, raw2, 1)
	require.NoError(t, txMap.PutWithNodeType(id1, blob1, shamap.NodeTypeTransactionWithMeta))
	require.NoError(t, txMap.PutWithNodeType(id2, blob2, shamap.NodeTypeTransactionWithMeta))
	txRoot, err := txMap.Hash()
	require.NoError(t, err)

	stateMap := shamap.New(shamap.TypeState)
	require.NoError(t, stateMap.Put([32]byte{0xAD, 0x0F, 0x01}, []byte("adopted-state")))
	stateRoot, err := stateMap.Hash()
	require.NoError(t, err)

	hdr := &header.LedgerHeader{
		LedgerIndex: svc.GetClosedLedgerIndex() + 1,
		TxHash:      txRoot,
		AccountHash: stateRoot,
	}
	hdr.Hash = header.CalculateHash(*hdr)
	adoptedHash := hdr.Hash

	require.NoError(t, svc.AdoptLedgerWithState(context.TODO(), hdr, stateMap, txMap))
	svc.FlushPersists()
	unvalidatedInfo, err := rm.Ledger().GetLedgerInfoBySeq(ctx, relationaldb.LedgerIndex(hdr.LedgerIndex))
	require.ErrorIs(t, err, relationaldb.ErrLedgerNotFound)
	require.Nil(t, unvalidatedInfo, "unvalidated adoption must not enter the validated relational index")
	svc.SetValidatedLedger(hdr.LedgerIndex, adoptedHash)
	// Persistence runs on the async worker; barrier before asserting.
	svc.FlushPersists()

	for wantID, wantTxnIndex := range map[[32]byte]uint32{id1: 0, id2: 1} {
		var dbHash relationaldb.Hash
		copy(dbHash[:], wantID[:])
		got, search, err := rm.Transaction().GetTransaction(ctx, dbHash, nil)
		require.NoError(t, err, "GetTransaction must not error for adopted tx")
		require.Equal(t, relationaldb.TxSearchAll, search,
			"adopted tx must be found in the RelationalDB")
		require.NotNil(t, got, "adopted tx row must not be nil")
		assert.Equal(t, relationaldb.LedgerIndex(hdr.LedgerIndex), got.LedgerSeq,
			"adopted tx must be filed under the adopted ledger's seq")
		gotTxnIndex, ok := tx.TransactionIndexFromMetadata(got.TxnMeta)
		require.True(t, ok, "persisted metadata must contain TransactionIndex")
		assert.Equal(t, wantTxnIndex, gotTxnIndex)
	}

	// And the adopted ledger row itself must be persisted.
	ledgerInfo, err := rm.Ledger().GetLedgerInfoBySeq(ctx, relationaldb.LedgerIndex(hdr.LedgerIndex))
	require.NoError(t, err)
	require.NotNil(t, ledgerInfo, "adopted ledger metadata must be persisted")
}

// TestAdoptLedgerWithState_PopulatesTxIndex pins F2: adopting a ledger
// must populate s.txIndex for every tx in the installed tx map so
// hash-lookup RPCs (tx, transaction_entry) can resolve hash -> seq
// without touching the DB. Before F2, adopted ledgers were
// invisible to the in-memory index and hash lookups fell off the
// fast path.
func TestAdoptLedgerWithState_PopulatesTxIndex(t *testing.T) {
	cfg := DefaultConfig()
	svc, err := New(cfg)
	require.NoError(t, err)
	require.NoError(t, svc.Start())

	txMap := shamap.New(shamap.TypeTransaction)

	blob1, id1 := makeTxMetaBlobForTest(t, []byte("idx-tx-blob-A-padding-padpad"), 0)
	blob2, id2 := makeTxMetaBlobForTest(t, []byte("idx-tx-blob-B-padding-padpad"), 1)
	blob3, id3 := makeTxMetaBlobForTest(t, []byte("idx-tx-blob-C-padding-padpad"), 2)
	require.NoError(t, txMap.PutWithNodeType(id1, blob1, shamap.NodeTypeTransactionWithMeta))
	require.NoError(t, txMap.PutWithNodeType(id2, blob2, shamap.NodeTypeTransactionWithMeta))
	require.NoError(t, txMap.PutWithNodeType(id3, blob3, shamap.NodeTypeTransactionWithMeta))

	txRoot, err := txMap.Hash()
	require.NoError(t, err)

	stateMap := shamap.New(shamap.TypeState)
	stateRoot, err := stateMap.Hash()
	require.NoError(t, err)

	hdr := &header.LedgerHeader{
		LedgerIndex: svc.GetClosedLedgerIndex() + 1,
		TxHash:      txRoot,
		AccountHash: stateRoot,
	}
	hdr.Hash = header.CalculateHash(*hdr)

	require.NoError(t, svc.AdoptLedgerWithState(context.TODO(), hdr, stateMap, txMap))

	for _, id := range [][32]byte{id1, id2, id3} {
		seq, ok := svc.txIndex[id]
		assert.True(t, ok, "txIndex must contain every adopted tx hash")
		assert.Equal(t, hdr.LedgerIndex, seq,
			"txIndex must map hash -> adopted ledger seq")
	}
	assert.Len(t, svc.txIndex, 3,
		"txIndex must contain exactly the adopted txs, nothing more")
}

func TestAdoptLedgerWithState_UsesMetadataTransactionIndex(t *testing.T) {
	svc, err := New(DefaultConfig())
	require.NoError(t, err)
	require.NoError(t, svc.Start())

	txMap := shamap.New(shamap.TypeTransaction)
	blob1, id1 := makeTxMetaBlobForTest(t, []byte("metadata-index-A-padding-padding"), 41)
	blob2, id2 := makeTxMetaBlobForTest(t, []byte("metadata-index-B-padding-padding"), 7)
	require.NoError(t, txMap.PutWithNodeType(id1, blob1, shamap.NodeTypeTransactionWithMeta))
	require.NoError(t, txMap.PutWithNodeType(id2, blob2, shamap.NodeTypeTransactionWithMeta))

	wantIndex := map[[32]byte]uint32{id1: 41, id2: 7}
	var traversal [][32]byte
	require.NoError(t, txMap.ForEach(func(item *shamap.Item) bool {
		traversal = append(traversal, item.Key())
		return true
	}))
	require.Len(t, traversal, 2)
	for ordinal, hash := range traversal {
		require.NotEqual(t, uint32(ordinal), wantIndex[hash])
	}

	txRoot, err := txMap.Hash()
	require.NoError(t, err)
	stateMap := shamap.New(shamap.TypeState)
	stateRoot, err := stateMap.Hash()
	require.NoError(t, err)

	hdr := &header.LedgerHeader{
		LedgerIndex: svc.GetClosedLedgerIndex() + 1,
		TxHash:      txRoot,
		AccountHash: stateRoot,
	}
	hdr.Hash = header.CalculateHash(*hdr)
	require.NoError(t, svc.AdoptLedgerWithState(context.TODO(), hdr, stateMap, txMap))

	for hash, want := range wantIndex {
		assert.Equal(t, want, svc.txPositionIndex[hash])
		result, err := svc.GetTransaction(hash)
		require.NoError(t, err)
		assert.Equal(t, want, result.TxIndex)
	}
}

func TestAdoptLedgerWithState_TraversalFailureLeavesCanonicalStateUnchanged(t *testing.T) {
	svc, err := New(DefaultConfig())
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)

	txMap := shamap.New(shamap.TypeTransaction)
	for i := range 2 {
		blob, id := makeTxMetaBlobForTest(t, []byte{byte(i + 1)}, uint32(i))
		require.NoError(t, txMap.PutWithNodeType(id, blob, shamap.NodeTypeTransactionWithMeta))
	}
	txRoot, err := txMap.Hash()
	require.NoError(t, err)
	baseFamily := shamapbackend.NewMemory()
	require.NoError(t, txMap.StoreDirty(func(entries []shamap.FlushEntry) error {
		return baseFamily.StoreBatch(t.Context(), entries)
	}))
	family := &corruptDescendantFamily{
		inner: baseFamily,
		roots: map[[32]byte]struct{}{txRoot: {}},
	}
	backedTxMap, err := shamap.NewFromRootHash(shamap.TypeTransaction, txRoot, family)
	require.NoError(t, err)

	stateMap := shamap.New(shamap.TypeState)
	stateRoot, err := stateMap.Hash()
	require.NoError(t, err)

	closedBefore := svc.GetClosedLedger()
	openBefore := svc.GetOpenLedger()
	hdr := &header.LedgerHeader{
		LedgerIndex: closedBefore.Sequence() + 1,
		ParentHash:  closedBefore.Hash(),
		TxHash:      txRoot,
		AccountHash: stateRoot,
	}
	hdr.Hash = header.CalculateHash(*hdr)

	svc.historyComponent.mu.RLock()
	historyLenBefore := len(svc.ledgerHistory)
	byHashLenBefore := len(svc.ledgerByHash)
	persistedLenBefore := len(svc.persistedLedgers)
	txIndexLenBefore := len(svc.txIndex)
	svc.historyComponent.mu.RUnlock()

	err = svc.AdoptLedgerWithState(t.Context(), hdr, stateMap, backedTxMap)
	require.Error(t, err)

	require.Same(t, closedBefore, svc.GetClosedLedger())
	require.Same(t, openBefore, svc.GetOpenLedger())
	svc.historyComponent.mu.RLock()
	defer svc.historyComponent.mu.RUnlock()
	require.Len(t, svc.ledgerHistory, historyLenBefore)
	require.Len(t, svc.ledgerByHash, byHashLenBefore)
	require.Len(t, svc.persistedLedgers, persistedLenBefore)
	require.Len(t, svc.txIndex, txIndexLenBefore)
	require.NotContains(t, svc.ledgerHistory, hdr.LedgerIndex)
	require.NotContains(t, svc.ledgerByHash, hdr.Hash)
}
