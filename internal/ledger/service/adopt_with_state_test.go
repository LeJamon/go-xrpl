package service

import (
	"testing"

	"github.com/LeJamon/goXRPLd/internal/ledger/header"
	"github.com/LeJamon/goXRPLd/shamap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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

	// Build a non-empty tx map: 2 distinct tx leaves with deterministic
	// content so the SHAMap root is stable across runs.
	txMap, err := shamap.New(shamap.TypeTransaction)
	require.NoError(t, err)

	var key1, key2 [32]byte
	for i := range key1 {
		key1[i] = byte(i + 1)
		key2[i] = byte(i + 100)
	}
	// SHAMap leaf values must be ≥12 bytes.
	require.NoError(t, txMap.Put(key1, []byte("tx-blob-content-1")))
	require.NoError(t, txMap.Put(key2, []byte("tx-blob-content-2")))

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
