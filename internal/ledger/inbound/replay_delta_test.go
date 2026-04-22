package inbound

import (
	"encoding/hex"
	"testing"
	"time"

	"github.com/LeJamon/goXRPLd/codec/binarycodec"
	"github.com/LeJamon/goXRPLd/crypto/common"
	"github.com/LeJamon/goXRPLd/drops"
	"github.com/LeJamon/goXRPLd/internal/ledger"
	"github.com/LeJamon/goXRPLd/internal/ledger/genesis"
	"github.com/LeJamon/goXRPLd/internal/ledger/header"
	"github.com/LeJamon/goXRPLd/internal/peermanagement/message"
	"github.com/LeJamon/goXRPLd/protocol"
	"github.com/LeJamon/goXRPLd/shamap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// makeGenesisLedger returns a genesis-derived ledger usable as the
// "parent" for a replay-delta acquisition test.
func makeGenesisLedger(t *testing.T) *ledger.Ledger {
	t.Helper()
	res, err := genesis.Create(genesis.DefaultConfig())
	require.NoError(t, err)
	return ledger.FromGenesis(res.Header, res.StateMap, res.TxMap, drops.Fees{})
}

// encodeVL returns the XRPL VL prefix for the given length. Mirrors
// internal/tx EncodeVL but is duplicated here so the test file stays
// self-contained (no cross-package test helper imports).
func encodeVL(length int) []byte {
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

// makeMetadataBlob serializes a minimal metadata STObject carrying
// TransactionResult + TransactionIndex. The binary codec round-trips this
// faithfully so the inbound parser can extract sfTransactionIndex.
func makeMetadataBlob(t *testing.T, txIndex uint32) []byte {
	t.Helper()
	hexStr, err := binarycodec.Encode(map[string]any{
		"TransactionResult": "tesSUCCESS",
		"TransactionIndex":  txIndex,
	})
	require.NoError(t, err)
	out, err := hex.DecodeString(hexStr)
	require.NoError(t, err)
	return out
}

// makeTxWithMetaBlob constructs a SHAMapItem-formatted blob (VL(tx) +
// VL(metadata)) with the supplied tx bytes and metadata index. Returns
// (blob, txID) where txID is the canonical XRPL transaction ID used as
// the SHAMap key on insert.
func makeTxWithMetaBlob(t *testing.T, txBytes []byte, txIndex uint32) ([]byte, [32]byte) {
	t.Helper()
	metaBytes := makeMetadataBlob(t, txIndex)

	txID := common.Sha512Half(protocol.HashPrefixTransactionID[:], txBytes)

	blob := make([]byte, 0, len(txBytes)+len(metaBytes)+4)
	blob = append(blob, encodeVL(len(txBytes))...)
	blob = append(blob, txBytes...)
	blob = append(blob, encodeVL(len(metaBytes))...)
	blob = append(blob, metaBytes...)
	return blob, txID
}

// buildDeltaResponse builds a header that links to parent and a tx SHAMap
// containing the supplied (blob, txID) pairs. Returns the wire-shaped
// ReplayDeltaResponse and the expected ledger hash. The header is
// hashed with genesis.CalculateLedgerHash so both sides agree.
func buildDeltaResponse(
	t *testing.T,
	parent *ledger.Ledger,
	blobs [][]byte,
	txIDs [][32]byte,
) (*message.ReplayDeltaResponse, [32]byte) {
	t.Helper()

	// Reconstruct the tx SHAMap so we can extract the canonical TxHash.
	txMap, err := shamap.New(shamap.TypeTransaction)
	require.NoError(t, err)
	for i, blob := range blobs {
		require.NoError(t, txMap.PutWithNodeType(txIDs[i], blob, shamap.NodeTypeTransactionWithMeta))
	}
	require.NoError(t, txMap.SetImmutable())
	txRoot, err := txMap.Hash()
	require.NoError(t, err)

	parentHash := parent.Hash()
	// Use close times well past the XRPL epoch so AddRaw / DeserializeHeader
	// round-trip cleanly (the xrplEpochToTime helper turns a zero
	// uint32 into a Go zero time, which then breaks CalculateLedgerHash's
	// .Unix() arithmetic — the test must avoid that boundary).
	closeTime := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	hdr := header.LedgerHeader{
		LedgerIndex:         parent.Sequence() + 1,
		ParentHash:          parentHash,
		ParentCloseTime:     closeTime,
		CloseTime:           closeTime.Add(10 * time.Second),
		CloseTimeResolution: parent.CloseTimeResolution(),
		CloseFlags:          0,
		Drops:               parent.TotalDrops(),
		TxHash:              txRoot,
		AccountHash:         parent.Header().AccountHash, // unchanged for the test
	}
	headerBytes, err := header.AddRaw(hdr, false)
	require.NoError(t, err)
	// Compute the canonical hash via the same parse-then-hash path the
	// inbound verifier will run, so the test's expectation matches what
	// production code will reach.
	parsed, err := header.DeserializeHeader(headerBytes, false)
	require.NoError(t, err)
	expectedHash := genesis.CalculateLedgerHash(*parsed)

	resp := &message.ReplayDeltaResponse{
		LedgerHash:   expectedHash[:],
		LedgerHeader: headerBytes,
		Transactions: blobs,
	}
	return resp, expectedHash
}

// TestInboundReplayDelta_GotResponse_Success exercises the happy path:
// header hash recomputes correctly, the rebuilt tx SHAMap matches the
// header's TxHash, and OrderedTxs returns the txs in TransactionIndex
// order.
func TestInboundReplayDelta_GotResponse_Success(t *testing.T) {
	parent := makeGenesisLedger(t)

	// Build three txs with non-monotonic TransactionIndex values so the
	// final ordering is observably different from the input order.
	txData := [][]byte{
		[]byte("tx-blob-A--padding-to-pass-shamap-min"),
		[]byte("tx-blob-B--padding-to-pass-shamap-min"),
		[]byte("tx-blob-C--padding-to-pass-shamap-min"),
	}
	indices := []uint32{2, 0, 1}

	blobs := make([][]byte, len(txData))
	txIDs := make([][32]byte, len(txData))
	for i := range txData {
		blob, id := makeTxWithMetaBlob(t, txData[i], indices[i])
		blobs[i] = blob
		txIDs[i] = id
	}
	resp, expectedHash := buildDeltaResponse(t, parent, blobs, txIDs)

	rd := NewReplayDelta(expectedHash, 42, parent, nil)
	require.NoError(t, rd.GotResponse(resp))
	assert.True(t, rd.IsComplete())
	assert.Nil(t, rd.Err())

	out, err := rd.Result()
	require.NoError(t, err)
	require.NotNil(t, out)
	assert.Equal(t, expectedHash, out.Hash())
	assert.Equal(t, parent.Sequence()+1, out.Sequence())

	ordered := rd.OrderedTxs()
	require.Len(t, ordered, 3)
	for i, dt := range ordered {
		assert.Equal(t, uint32(i), dt.Index, "txs must be sorted by TransactionIndex")
	}
}

// TestInboundReplayDelta_HeaderHashMismatch verifies that tampered
// header bytes are rejected with a hash-mismatch error.
func TestInboundReplayDelta_HeaderHashMismatch(t *testing.T) {
	parent := makeGenesisLedger(t)
	blob, id := makeTxWithMetaBlob(t, []byte("tx-data-padding-padding-padding"), 0)
	resp, expectedHash := buildDeltaResponse(t, parent, [][]byte{blob}, [][32]byte{id})

	// Flip a byte inside the header — drops field, well clear of close
	// times so the header still parses but its hash diverges.
	resp.LedgerHeader[5] ^= 0xFF

	rd := NewReplayDelta(expectedHash, 42, parent, nil)
	err := rd.GotResponse(resp)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "header hash mismatch")
	assert.Equal(t, StateFailed, rd.State())
	_, resErr := rd.Result()
	assert.Error(t, resErr, "Result() must fail when state is Failed")
}

// TestInboundReplayDelta_TxMapHashMismatch verifies that a tampered tx
// blob whose recomputed root no longer matches the header's TxHash is
// rejected with a tx-map-root mismatch error.
func TestInboundReplayDelta_TxMapHashMismatch(t *testing.T) {
	parent := makeGenesisLedger(t)
	blob1, id1 := makeTxWithMetaBlob(t, []byte("tx-A-padding-padding-padding"), 0)
	blob2, id2 := makeTxWithMetaBlob(t, []byte("tx-B-padding-padding-padding"), 1)

	// Build the response against the ORIGINAL blobs so the header's
	// TxHash anchors them.
	resp, expectedHash := buildDeltaResponse(t, parent,
		[][]byte{blob1, blob2}, [][32]byte{id1, id2})

	// Now swap in a totally different tx blob — same VL framing, different
	// content. The split + reinsert path will recompute the SHAMap root
	// and fail the comparison.
	tampered, _ := makeTxWithMetaBlob(t, []byte("tx-X-padding-padding-padding"), 0)
	resp.Transactions[1] = tampered

	rd := NewReplayDelta(expectedHash, 42, parent, nil)
	err := rd.GotResponse(resp)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "tx map root mismatch")
	assert.Equal(t, StateFailed, rd.State())
}

// TestInboundReplayDelta_MalformedTxBlob verifies that a blob whose VL
// header doesn't decode (truncated) is rejected with a parse error.
func TestInboundReplayDelta_MalformedTxBlob(t *testing.T) {
	parent := makeGenesisLedger(t)
	blob, id := makeTxWithMetaBlob(t, []byte("tx-data-padding-padding-padding"), 0)
	resp, expectedHash := buildDeltaResponse(t, parent, [][]byte{blob}, [][32]byte{id})

	// Replace the blob with a single VL byte claiming "200 bytes follow"
	// but providing none. The parser will hit ErrParserOutOfBound.
	resp.Transactions[0] = []byte{0x80}

	rd := NewReplayDelta(expectedHash, 42, parent, nil)
	err := rd.GotResponse(resp)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "split blob")
	assert.Equal(t, StateFailed, rd.State())
}

// TestInboundReplayDelta_ResponseError verifies that a peer-signaled
// error response is rejected without further parsing.
func TestInboundReplayDelta_ResponseError(t *testing.T) {
	parent := makeGenesisLedger(t)
	resp := &message.ReplayDeltaResponse{
		LedgerHash: make([]byte, 32),
		Error:      message.ReplyErrorNoLedger,
	}

	rd := NewReplayDelta([32]byte{}, 42, parent, nil)
	err := rd.GotResponse(resp)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "peer signaled error")
	assert.Equal(t, StateFailed, rd.State())
}

// TestInboundReplayDelta_EmptyHeader verifies that a response missing
// the serialized header is rejected with an empty-header error.
func TestInboundReplayDelta_EmptyHeader(t *testing.T) {
	parent := makeGenesisLedger(t)
	resp := &message.ReplayDeltaResponse{
		LedgerHash: make([]byte, 32),
	}

	rd := NewReplayDelta([32]byte{}, 42, parent, nil)
	err := rd.GotResponse(resp)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty header")
	assert.Equal(t, StateFailed, rd.State())
}

// TestInboundReplayDelta_Timeout verifies that IsTimedOut flips to true
// after the configured budget elapses. We construct the ReplayDelta and
// rewind its `created` timestamp to simulate the 30s having passed
// without sleeping in the test.
func TestInboundReplayDelta_Timeout(t *testing.T) {
	parent := makeGenesisLedger(t)
	rd := NewReplayDelta([32]byte{}, 42, parent, nil)
	assert.False(t, rd.IsTimedOut(), "fresh acquisition cannot be timed out")

	// Rewind: the only field IsTimedOut consults besides the state is
	// `created`, and the mutex protects both — so taking the lock and
	// poking the field is a faithful simulation of clock advancement.
	rd.mu.Lock()
	rd.created = time.Now().Add(-2 * replayDeltaTimeout)
	rd.mu.Unlock()
	assert.True(t, rd.IsTimedOut(), "advanced clock must trigger timeout")

	// Once a terminal state is reached, IsTimedOut must return false so
	// the maintenance tick doesn't keep retrying a finished acquisition.
	rd.mu.Lock()
	rd.state = StateComplete
	rd.mu.Unlock()
	assert.False(t, rd.IsTimedOut(), "terminal state silences IsTimedOut")
}

// TestInboundReplayDelta_SubTaskRetryLoop exercises the R4.8 peer-swap
// machinery. Mirrors rippled's LedgerDeltaAcquire sub-task rotation
// (LedgerReplayer.h:49-51 SUB_TASK_TIMEOUT=250ms × SUB_TASK_MAX=10):
// a peer that hasn't answered within the sub-task window is rotated
// by NoteSubTaskRetry, which resets the sub-task clock and records
// the new peer in TriedPeers. RetriesExhausted fires after
// subTaskRetryMax rotations so the router knows to stop rotating and
// let the outer budget escalate to the legacy fallback.
func TestInboundReplayDelta_SubTaskRetryLoop(t *testing.T) {
	parent := makeGenesisLedger(t)
	rd := NewReplayDelta([32]byte{0xAA}, 100, parent, nil)

	// Fresh acquisition: sub-task not yet timed out, no retries done.
	assert.False(t, rd.IsSubTaskTimedOut(), "fresh request not sub-task-timed-out")
	assert.False(t, rd.RetriesExhausted(), "no retries yet")
	assert.Equal(t, 0, rd.RetryCount())
	assert.Equal(t, []uint64{100}, rd.TriedPeers(), "initial peer must be tracked")

	// Rewind the sub-task clock to simulate 250ms elapsed without a
	// response. Can't use clock injection here (SystemClock is fixed),
	// so we poke the private field — same technique as the outer
	// timeout test above.
	rd.mu.Lock()
	rd.subTaskStart = time.Now().Add(-2 * subTaskRetryInterval)
	rd.mu.Unlock()
	assert.True(t, rd.IsSubTaskTimedOut(),
		"sub-task must time out after subTaskRetryInterval elapsed")

	// Rotate to a new peer: sub-task timer resets, retryCount advances,
	// triedPeers grows.
	rd.NoteSubTaskRetry(200)
	assert.Equal(t, uint64(200), rd.PeerID(), "peer ID must advance")
	assert.Equal(t, 1, rd.RetryCount())
	assert.Equal(t, []uint64{100, 200}, rd.TriedPeers())
	assert.False(t, rd.IsSubTaskTimedOut(),
		"NoteSubTaskRetry must reset the sub-task clock")

	// Exhaust retries by driving the loop subTaskRetryMax-1 more times.
	for i := 2; i <= subTaskRetryMax; i++ {
		rd.NoteSubTaskRetry(uint64(100 + i*100))
	}
	assert.True(t, rd.RetriesExhausted(),
		"after subTaskRetryMax rotations RetriesExhausted must fire")
	assert.Equal(t, subTaskRetryMax, rd.RetryCount())

	// After exhaustion a subsequent sub-task timeout does NOT produce
	// a further retry from the router (SubTaskTimedOut filter in the
	// replayer skips exhausted entries).
	r := NewReplayer(nil, nil, 1)
	r.inFlight[rd.Hash()] = rd
	assert.Empty(t, r.SubTaskTimedOut(),
		"exhausted acquisition must not appear in SubTaskTimedOut")
}

// TestInboundReplayDelta_SubTaskTimeoutDoesNotFireInTerminalState
// verifies the short-circuit on state==Complete/Failed: once an
// acquisition reaches a terminal state the sub-task retry machinery
// goes quiet, matching the same guard on IsTimedOut.
func TestInboundReplayDelta_SubTaskTimeoutDoesNotFireInTerminalState(t *testing.T) {
	parent := makeGenesisLedger(t)
	rd := NewReplayDelta([32]byte{0xBB}, 42, parent, nil)

	rd.mu.Lock()
	rd.subTaskStart = time.Now().Add(-2 * subTaskRetryInterval)
	rd.state = StateComplete
	rd.mu.Unlock()

	assert.False(t, rd.IsSubTaskTimedOut(),
		"terminal state must silence IsSubTaskTimedOut (no rotation after success)")
}

// TestInboundReplayDelta_ParentHashMismatch verifies the parent-linkage
// invariant: a peer that returns a header whose ParentHash differs from
// our parent's hash is rejected (defends against fork-serving peers).
func TestInboundReplayDelta_ParentHashMismatch(t *testing.T) {
	parent := makeGenesisLedger(t)
	blob, id := makeTxWithMetaBlob(t, []byte("tx-data-padding-padding-padding"), 0)
	resp, expectedHash := buildDeltaResponse(t, parent, [][]byte{blob}, [][32]byte{id})

	// Build a wrong-parent ledger: snapshot genesis but install a
	// synthetic hash so Hash() returns something other than the value
	// baked into the response header.
	other := makeGenesisLedger(t)
	hdr := other.Header()
	hdr.Hash = [32]byte{0xDE, 0xAD, 0xBE, 0xEF}
	stateSnap, err := other.StateMapSnapshot()
	require.NoError(t, err)
	require.NoError(t, stateSnap.SetImmutable())
	txSnap, err := other.TxMapSnapshot()
	require.NoError(t, err)
	require.NoError(t, txSnap.SetImmutable())
	wrong := ledger.NewFromHeader(hdr, stateSnap, txSnap, drops.Fees{})

	rd := NewReplayDelta(expectedHash, 42, wrong, nil)
	err = rd.GotResponse(resp)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parent hash mismatch")
}
