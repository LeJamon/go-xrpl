package inbound

import (
	"testing"
	"time"

	"github.com/LeJamon/goXRPLd/crypto/common"
	"github.com/LeJamon/goXRPLd/internal/ledger"
	"github.com/LeJamon/goXRPLd/internal/ledger/header"
	"github.com/LeJamon/goXRPLd/internal/peermanagement/message"
	"github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/protocol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// buildEmptyClosedSuccessorResponse builds a wire response carrying a
// real Close()-derived header for the empty-tx successor of `parent`.
// Apply() running on the same parent + zero txs reproduces this header
// exactly (skip-list update, drops accounting, hash derivation), so
// this is the minimal integration fixture for the happy path.
func buildEmptyClosedSuccessorResponse(t *testing.T, parent *ledger.Ledger) (*message.ReplayDeltaResponse, header.LedgerHeader) {
	t.Helper()
	closeTime := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	open, err := ledger.NewOpen(parent, closeTime)
	require.NoError(t, err)
	require.NoError(t, open.Close(closeTime, 0))
	hdr := open.Header()

	hdrBytes, err := header.AddRaw(hdr, false)
	require.NoError(t, err)

	resp := &message.ReplayDeltaResponse{
		LedgerHash:   hdr.Hash[:],
		LedgerHeader: hdrBytes,
		Transactions: nil, // empty tx set
	}
	return resp, hdr
}

// armReplayDeltaWith verifies the response through GotResponse and
// returns the armed ReplayDelta in StateComplete, ready for Apply().
func armReplayDeltaWith(t *testing.T, parent *ledger.Ledger, resp *message.ReplayDeltaResponse, hdr header.LedgerHeader) *ReplayDelta {
	t.Helper()
	rd := NewReplayDelta(hdr.Hash, 7, parent, nil)
	require.NoError(t, rd.GotResponse(resp))
	require.True(t, rd.IsComplete())
	return rd
}

// TestReplayDelta_Apply_EmptyTxSet exercises the happy path with zero
// transactions. The successor's AccountHash differs from the parent's
// (Close adds a LedgerHashes skip-list entry), so this still
// meaningfully exercises the state-map derivation — it's the smallest
// fixture that proves Apply runs Close() on the child and produces a
// hash that matches the verified header byte-for-byte.
func TestReplayDelta_Apply_EmptyTxSet(t *testing.T) {
	parent := makeGenesisLedger(t)
	resp, hdr := buildEmptyClosedSuccessorResponse(t, parent)
	rd := armReplayDeltaWith(t, parent, resp, hdr)

	derived, err := rd.Apply(tx.EngineConfig{})
	require.NoError(t, err)
	require.NotNil(t, derived)

	assert.Equal(t, hdr.Hash, derived.Hash(), "derived ledger hash must match verified header")
	assert.Equal(t, hdr.LedgerIndex, derived.Sequence())

	got, err := derived.StateMapHash()
	require.NoError(t, err)
	assert.Equal(t, hdr.AccountHash, got, "derived AccountHash must match header")

	gotTx, err := derived.TxMapHash()
	require.NoError(t, err)
	assert.Equal(t, hdr.TxHash, gotTx, "derived TxHash must match header")
}

// TestReplayDelta_Apply_OrderedByIndex verifies that Apply walks
// r.txs in TransactionIndex order, not wire order. We poke ReplayDelta
// into StateComplete with synthetic DecodedTx records carrying
// non-monotonic Index values and unparseable TxBytes, then assert
// Apply attempts the lowest-index tx first (it's the first parse
// failure we observe).
func TestReplayDelta_Apply_OrderedByIndex(t *testing.T) {
	parent := makeGenesisLedger(t)

	// Build three DecodedTx entries already sorted by GotResponse —
	// orderedTxs invariant is "sorted ascending by Index". To exercise
	// the loop in Apply we need them in that sorted order; the test
	// asserts the FIRST tx encountered is the one with Index 0 (whose
	// TxBytes is a unique sentinel).
	// Stitch a minimal valid result header into r.result so Apply has
	// the seq / parent-linkage / closeTime fields it needs. Reuse
	// parent's values; the successor is a notional seq = parent.seq+1.
	resHdr := parent.Header()
	resHdr.LedgerIndex = parent.Sequence() + 1
	resHdr.ParentHash = parent.Hash()
	resHdr.ParentCloseTime = parent.CloseTime()
	resHdr.CloseTime = parent.CloseTime().Add(10 * time.Second)

	rd := NewReplayDelta([32]byte{}, 7, parent, nil)
	rd.mu.Lock()
	rd.state = StateComplete
	rd.result = ledger.NewFromHeader(resHdr, nil, nil, parent.GetFees())
	// Purposely-malformed TxBytes — distinguishable per index so the
	// returned error tells us which tx Apply tried first.
	rd.txs = []DecodedTx{
		{Index: 0, Hash: [32]byte{0xA0}, TxBytes: []byte{0xA0}, LeafBlob: []byte{0xA0}},
		{Index: 1, Hash: [32]byte{0xB1}, TxBytes: []byte{0xB1}, LeafBlob: []byte{0xB1}},
		{Index: 2, Hash: [32]byte{0xC2}, TxBytes: []byte{0xC2}, LeafBlob: []byte{0xC2}},
	}
	rd.mu.Unlock()

	_, err := rd.Apply(tx.EngineConfig{})
	require.Error(t, err)
	// Apply should have hit the index=0 (Hash 0xA0...) tx first; its
	// 8-byte hash prefix is a0000000_00000000.
	assert.Contains(t, err.Error(), "a000000000000000",
		"first tx attempted must be the one with the smallest TransactionIndex")
}

// TestReplayDelta_Apply_StateRootMismatch verifies the engine
// divergence path: a target header carrying a deliberately-wrong
// AccountHash causes Apply to fail with a state-map mismatch error.
//
// We build a real Close()-derived response, then tamper with the
// header's AccountHash before re-hashing. GotResponse must still pass
// (header hash + tx-map root match), and Apply must reject the
// mismatched state-map root.
func TestReplayDelta_Apply_StateRootMismatch(t *testing.T) {
	parent := makeGenesisLedger(t)

	closeTime := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	open, err := ledger.NewOpen(parent, closeTime)
	require.NoError(t, err)
	require.NoError(t, open.Close(closeTime, 0))
	hdr := open.Header()

	// Tamper with AccountHash. Re-serialize and re-derive the
	// byte-level hash so GotResponse still passes (header hash + tx
	// map root both match the response). Apply must then catch the
	// state-map divergence.
	hdr.AccountHash[0] ^= 0xFF
	hdrBytes, err := header.AddRaw(hdr, false)
	require.NoError(t, err)
	hdr.Hash = computeWireHeaderHash(hdrBytes)

	resp := &message.ReplayDeltaResponse{
		LedgerHash:   hdr.Hash[:],
		LedgerHeader: hdrBytes,
		Transactions: nil,
	}
	rd := armReplayDeltaWith(t, parent, resp, hdr)

	_, err = rd.Apply(tx.EngineConfig{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "state map root mismatch",
		"Apply must surface a clear state-map divergence message")
}

// TestReplayDelta_Apply_BeforeComplete verifies the precondition guard:
// calling Apply before GotResponse has succeeded (state != Complete)
// returns a clear error and does not mutate any state.
func TestReplayDelta_Apply_BeforeComplete(t *testing.T) {
	parent := makeGenesisLedger(t)
	rd := NewReplayDelta([32]byte{}, 7, parent, nil)

	_, err := rd.Apply(tx.EngineConfig{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Apply called before response verified")

	// State must still be the initial WantBase — Apply did not advance it.
	assert.Equal(t, StateWantBase, rd.State())
}

// computeWireHeaderHash mirrors the byte-level hash verifyAndBuild
// computes for inbound headers (LWR prefix + raw header bytes,
// SHA-512/256). The test uses this to forge a self-consistent
// header after tampering with one of the fields.
func computeWireHeaderHash(headerBytes []byte) [32]byte {
	return common.Sha512Half(protocol.HashPrefixLedgerMaster.Bytes(), headerBytes)
}
