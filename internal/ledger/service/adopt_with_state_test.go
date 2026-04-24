package service

import (
	"context"
	"encoding/hex"
	"testing"

	"github.com/LeJamon/goXRPLd/codec/binarycodec"
	"github.com/LeJamon/goXRPLd/crypto/common"
	"github.com/LeJamon/goXRPLd/internal/ledger/header"
	"github.com/LeJamon/goXRPLd/protocol"
	"github.com/LeJamon/goXRPLd/shamap"
	"github.com/LeJamon/goXRPLd/storage/relationaldb"
	sqlitedb "github.com/LeJamon/goXRPLd/storage/relationaldb/sqlite"
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
func makeTxMetaBlobForTest(t *testing.T, txBytes []byte, txIndex uint32) ([]byte, [32]byte) {
	t.Helper()

	metaHex, err := binarycodec.Encode(map[string]any{
		"TransactionResult": "tesSUCCESS",
		"TransactionIndex":  txIndex,
	})
	require.NoError(t, err)
	metaBytes, err := hex.DecodeString(metaHex)
	require.NoError(t, err)

	txID := common.Sha512Half(protocol.HashPrefixTransactionID[:], txBytes)

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
	txMap, err := shamap.New(shamap.TypeTransaction)
	require.NoError(t, err)

	blob1, id1 := makeTxMetaBlobForTest(t, []byte("adopt-tx-blob-A-padding-padding"), 0)
	blob2, id2 := makeTxMetaBlobForTest(t, []byte("adopt-tx-blob-B-padding-padding"), 1)
	require.NoError(t, txMap.PutWithNodeType(id1, blob1, shamap.NodeTypeTransactionWithMeta))
	require.NoError(t, txMap.PutWithNodeType(id2, blob2, shamap.NodeTypeTransactionWithMeta))

	expectedTxRoot, err := txMap.Hash()
	require.NoError(t, err)

	// Build a minimal state map (empty is fine — we're testing the
	// tx-map threading, not state content).
	stateMap, err := shamap.New(shamap.TypeState)
	require.NoError(t, err)
	expectedStateRoot, err := stateMap.Hash()
	require.NoError(t, err)

	// Construct a header whose TxHash matches the tx map root. The
	// adopted ledger's tx map must hash to this same value.
	var adoptedHash [32]byte
	adoptedHash[0] = 0xAD // arbitrary distinct value
	hdr := &header.LedgerHeader{
		LedgerIndex: svc.GetClosedLedgerIndex() + 1,
		Hash:        adoptedHash,
		TxHash:      expectedTxRoot,
		AccountHash: expectedStateRoot,
	}

	require.NoError(t, svc.AdoptLedgerWithState(hdr, stateMap, txMap),
		"AdoptLedgerWithState must accept a caller-supplied tx map")

	// The adopted ledger must carry the caller-supplied tx map.
	adopted, err := svc.GetLedgerByHash(adoptedHash)
	require.NoError(t, err)
	require.NotNil(t, adopted)

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

	stateMap, err := shamap.New(shamap.TypeState)
	require.NoError(t, err)
	stateRoot, err := stateMap.Hash()
	require.NoError(t, err)

	// Genesis's tx-map root — what the adopted ledger should inherit
	// when the caller passes nil for txMap.
	emptyTxMap, err := svc.GetValidatedLedger().TxMapSnapshot()
	require.NoError(t, err)
	emptyTxRoot, err := emptyTxMap.Hash()
	require.NoError(t, err)

	var adoptedHash [32]byte
	adoptedHash[0] = 0xBE
	hdr := &header.LedgerHeader{
		LedgerIndex: svc.GetClosedLedgerIndex() + 1,
		Hash:        adoptedHash,
		TxHash:      emptyTxRoot,
		AccountHash: stateRoot,
	}

	require.NoError(t, svc.AdoptLedgerWithState(hdr, stateMap, nil),
		"AdoptLedgerWithState must accept nil txMap (legacy catchup path)")

	adopted, err := svc.GetLedgerByHash(adoptedHash)
	require.NoError(t, err)
	gotTxRoot, err := adopted.TxMapHash()
	require.NoError(t, err)
	assert.Equal(t, emptyTxRoot, gotTxRoot,
		"nil txMap must fall back to the genesis-shaped empty tx map")
}

// TestAdoptLedgerWithState_PersistsToRelationalDB pins F1: adopting a
// ledger with a tx map must flush those transactions to the
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
	rm, err := sqlitedb.NewRepositoryManager(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, rm.Open(ctx))
	t.Cleanup(func() { _ = rm.Close(ctx) })

	cfg := DefaultConfig()
	cfg.RelationalDB = rm
	svc, err := New(cfg)
	require.NoError(t, err)
	require.NoError(t, svc.Start())

	// Two txs with canonical-hash keys so the DB row's trans_id column
	// matches the hash we query for.
	txMap, err := shamap.New(shamap.TypeTransaction)
	require.NoError(t, err)
	blob1, id1 := makeTxMetaBlobForTest(t, []byte("persist-tx-blob-A-padding-pad"), 0)
	blob2, id2 := makeTxMetaBlobForTest(t, []byte("persist-tx-blob-B-padding-pad"), 1)
	require.NoError(t, txMap.PutWithNodeType(id1, blob1, shamap.NodeTypeTransactionWithMeta))
	require.NoError(t, txMap.PutWithNodeType(id2, blob2, shamap.NodeTypeTransactionWithMeta))
	txRoot, err := txMap.Hash()
	require.NoError(t, err)

	stateMap, err := shamap.New(shamap.TypeState)
	require.NoError(t, err)
	stateRoot, err := stateMap.Hash()
	require.NoError(t, err)

	var adoptedHash [32]byte
	adoptedHash[0] = 0xC1
	hdr := &header.LedgerHeader{
		LedgerIndex: svc.GetClosedLedgerIndex() + 1,
		Hash:        adoptedHash,
		TxHash:      txRoot,
		AccountHash: stateRoot,
	}

	require.NoError(t, svc.AdoptLedgerWithState(hdr, stateMap, txMap))

	// Both adopted transactions must now be retrievable from the DB.
	for _, wantID := range [][32]byte{id1, id2} {
		var dbHash relationaldb.Hash
		copy(dbHash[:], wantID[:])
		got, search, err := rm.Transaction().GetTransaction(ctx, dbHash, nil)
		require.NoError(t, err, "GetTransaction must not error for adopted tx")
		require.Equal(t, relationaldb.TxSearchAll, search,
			"adopted tx must be found in the RelationalDB")
		require.NotNil(t, got, "adopted tx row must not be nil")
		assert.Equal(t, relationaldb.LedgerIndex(hdr.LedgerIndex), got.LedgerSeq,
			"adopted tx must be filed under the adopted ledger's seq")
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

	txMap, err := shamap.New(shamap.TypeTransaction)
	require.NoError(t, err)

	blob1, id1 := makeTxMetaBlobForTest(t, []byte("idx-tx-blob-A-padding-padpad"), 0)
	blob2, id2 := makeTxMetaBlobForTest(t, []byte("idx-tx-blob-B-padding-padpad"), 1)
	blob3, id3 := makeTxMetaBlobForTest(t, []byte("idx-tx-blob-C-padding-padpad"), 2)
	require.NoError(t, txMap.PutWithNodeType(id1, blob1, shamap.NodeTypeTransactionWithMeta))
	require.NoError(t, txMap.PutWithNodeType(id2, blob2, shamap.NodeTypeTransactionWithMeta))
	require.NoError(t, txMap.PutWithNodeType(id3, blob3, shamap.NodeTypeTransactionWithMeta))

	txRoot, err := txMap.Hash()
	require.NoError(t, err)

	stateMap, err := shamap.New(shamap.TypeState)
	require.NoError(t, err)
	stateRoot, err := stateMap.Hash()
	require.NoError(t, err)

	var adoptedHash [32]byte
	adoptedHash[0] = 0xF2
	hdr := &header.LedgerHeader{
		LedgerIndex: svc.GetClosedLedgerIndex() + 1,
		Hash:        adoptedHash,
		TxHash:      txRoot,
		AccountHash: stateRoot,
	}

	require.NoError(t, svc.AdoptLedgerWithState(hdr, stateMap, txMap))

	for _, id := range [][32]byte{id1, id2, id3} {
		seq, ok := svc.txIndex[id]
		assert.True(t, ok, "txIndex must contain every adopted tx hash")
		assert.Equal(t, hdr.LedgerIndex, seq,
			"txIndex must map hash -> adopted ledger seq")
	}
	assert.Len(t, svc.txIndex, 3,
		"txIndex must contain exactly the adopted txs, nothing more")
}
