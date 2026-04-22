package peermanagement

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/LeJamon/goXRPLd/internal/peermanagement/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeReplayDeltaProvider is a minimal LedgerProvider double used by the
// replay-delta tests. The only method exercised is GetReplayDelta — the
// other interface methods are no-ops for these tests.
type fakeReplayDeltaProvider struct {
	header   []byte
	txLeaves [][]byte
	err      error
	calls    int
}

func (f *fakeReplayDeltaProvider) GetLedgerHeader(_ []byte, _ uint32) ([]byte, error) {
	return nil, nil
}
func (f *fakeReplayDeltaProvider) GetAccountStateNode(_ []byte, _ []byte) ([]byte, error) {
	return nil, nil
}
func (f *fakeReplayDeltaProvider) GetTransactionNode(_ []byte, _ []byte) ([]byte, error) {
	return nil, nil
}
func (f *fakeReplayDeltaProvider) GetReplayDelta(_ []byte) ([]byte, [][]byte, error) {
	f.calls++
	return f.header, f.txLeaves, f.err
}
func (f *fakeReplayDeltaProvider) GetProofPath(_ []byte, _ []byte, _ message.LedgerMapType) ([]byte, [][]byte, error) {
	return nil, nil, nil
}

// fixedHash returns a 32-byte ledger hash whose contents are predictable
// so test assertions remain stable across runs.
func fixedHash() []byte {
	h := make([]byte, 32)
	for i := range h {
		h[i] = byte(i + 1)
	}
	return h
}

// drainReplayDeltaResponse pulls the first EventLedgerResponse off the
// channel, decodes its wire-framed payload (6-byte header + protobuf
// body) as a ReplayDeltaResponse, and returns it. Uses select+default
// rather than a len() snapshot so a not-yet-emitted event surfaces as
// a test failure deterministically.
func drainReplayDeltaResponse(t *testing.T, events chan Event) *message.ReplayDeltaResponse {
	t.Helper()
	var evt Event
	select {
	case evt = <-events:
	default:
		t.Fatal("expected an event on the channel, got none")
	}
	require.Equal(t, EventLedgerResponse, evt.Type)
	header, body, err := message.ReadMessage(bytes.NewReader(evt.Payload))
	require.NoError(t, err, "event payload must be a valid wire frame")
	require.Equal(t, message.TypeReplayDeltaResponse, header.MessageType)
	decoded, err := message.Decode(message.TypeReplayDeltaResponse, body)
	require.NoError(t, err)
	resp, ok := decoded.(*message.ReplayDeltaResponse)
	require.True(t, ok, "decoded payload must be *message.ReplayDeltaResponse")
	return resp
}

// TestReplayDeltaRequest_Success verifies the happy path: a fake provider
// returns a known header plus three known tx blobs, and the handler emits
// a wire-encoded response carrying both fields in iteration order.
func TestReplayDeltaRequest_Success(t *testing.T) {
	header := []byte("ledger-header-bytes")
	txLeaves := [][]byte{
		[]byte("tx-leaf-1"),
		[]byte("tx-leaf-2"),
		[]byte("tx-leaf-3"),
	}
	provider := &fakeReplayDeltaProvider{header: header, txLeaves: txLeaves}

	events := make(chan Event, 1)
	h := NewLedgerSyncHandler(events)
	h.SetProvider(provider)

	hash := fixedHash()
	err := h.HandleMessage(context.Background(), PeerID(7), &message.ReplayDeltaRequest{LedgerHash: hash})
	require.NoError(t, err)

	resp := drainReplayDeltaResponse(t, events)
	assert.Equal(t, hash, resp.LedgerHash)
	assert.Equal(t, message.ReplyErrorNone, resp.Error)
	assert.Equal(t, header, resp.LedgerHeader)
	assert.Equal(t, txLeaves, resp.Transactions)
	assert.Equal(t, 1, provider.calls)
}

// TestReplayDeltaRequest_BadHashLength verifies the length precheck:
// any ledger_hash whose length is not 32 must yield reBAD_REQUEST without
// touching the provider. Mirrors rippled's `ledgerhash().size() !=
// uint256::size()` guard.
func TestReplayDeltaRequest_BadHashLength(t *testing.T) {
	provider := &fakeReplayDeltaProvider{
		header:   []byte("never-served"),
		txLeaves: [][]byte{[]byte("tx")},
	}

	events := make(chan Event, 1)
	h := NewLedgerSyncHandler(events)
	h.SetProvider(provider)

	short := []byte{0x01, 0x02, 0x03}
	err := h.HandleMessage(context.Background(), PeerID(1), &message.ReplayDeltaRequest{LedgerHash: short})
	require.ErrorIs(t, err, ErrPeerBadRequest,
		"malformed ledger hash must be signaled as ErrPeerBadRequest so the dispatcher can charge the peer")

	resp := drainReplayDeltaResponse(t, events)
	assert.Equal(t, message.ReplyErrorBadRequest, resp.Error)
	assert.Equal(t, short, resp.LedgerHash)
	assert.Empty(t, resp.LedgerHeader)
	assert.Empty(t, resp.Transactions)
	assert.Zero(t, provider.calls, "provider must not be called for bad-length requests")
}

// TestReplayDeltaRequest_UnknownLedger verifies that GetReplayDelta
// returning (nil, nil, nil) — the documented contract for "unknown or
// not immutable" — produces a reNO_LEDGER reply mirroring rippled's
// `!ledger || !ledger->isImmutable()` branch.
func TestReplayDeltaRequest_UnknownLedger(t *testing.T) {
	provider := &fakeReplayDeltaProvider{} // returns (nil, nil, nil)

	events := make(chan Event, 1)
	h := NewLedgerSyncHandler(events)
	h.SetProvider(provider)

	hash := fixedHash()
	err := h.HandleMessage(context.Background(), PeerID(2), &message.ReplayDeltaRequest{LedgerHash: hash})
	require.NoError(t, err)

	resp := drainReplayDeltaResponse(t, events)
	assert.Equal(t, message.ReplyErrorNoLedger, resp.Error)
	assert.Equal(t, hash, resp.LedgerHash)
	assert.Empty(t, resp.LedgerHeader)
	assert.Empty(t, resp.Transactions)
	assert.Equal(t, 1, provider.calls)
}

// TestReplayDeltaRequest_ProviderError covers the same reNO_LEDGER branch
// when the provider surfaces an error (e.g., backend I/O failure). The
// handler must not bubble the error up to the caller — peer-facing
// failures are conveyed in-band as a TMReplyError.
func TestReplayDeltaRequest_ProviderError(t *testing.T) {
	provider := &fakeReplayDeltaProvider{err: errors.New("backend down")}

	events := make(chan Event, 1)
	h := NewLedgerSyncHandler(events)
	h.SetProvider(provider)

	hash := fixedHash()
	err := h.HandleMessage(context.Background(), PeerID(3), &message.ReplayDeltaRequest{LedgerHash: hash})
	require.NoError(t, err)

	resp := drainReplayDeltaResponse(t, events)
	assert.Equal(t, message.ReplyErrorNoLedger, resp.Error)
}

// TestReplayDeltaRequest_NoProvider verifies that with no provider wired
// the handler is silent — no event is emitted and no error is returned.
// This matches handleGetLedger's behavior so a node without a configured
// ledger source simply doesn't answer.
func TestReplayDeltaRequest_NoProvider(t *testing.T) {
	events := make(chan Event, 1)
	h := NewLedgerSyncHandler(events)
	// intentionally no SetProvider

	hash := fixedHash()
	err := h.HandleMessage(context.Background(), PeerID(4), &message.ReplayDeltaRequest{LedgerHash: hash})
	require.NoError(t, err)

	assert.Equal(t, 0, len(events), "no event should be emitted when provider is nil")
}

// TestReplayDeltaRequest_OversizedResponse drives the defensive size cap.
// A provider returns a payload whose total bytes exceed
// MaxReplayDeltaResponseBytes; the handler must refuse to encode the tx
// list and reply with reNO_LEDGER — NOT reBAD_REQUEST. The request
// itself is well-formed; we just can't serve at this size, so the
// lighter "no ledger available" code avoids charging the requester
// feeMalformedRequest on rippled's side (PeerImp.cpp:1545-1548).
func TestReplayDeltaRequest_OversizedResponse(t *testing.T) {
	// Header is small; the tx leaves push us past the cap.
	header := []byte("hdr")
	chunkSize := 1 << 20 // 1 MiB
	chunk := make([]byte, chunkSize)
	for i := range chunk {
		chunk[i] = 0xAB
	}
	// 17 leaves of 1 MiB each = 17 MiB > 16 MiB cap.
	numLeaves := (MaxReplayDeltaResponseBytes / chunkSize) + 1
	txLeaves := make([][]byte, numLeaves)
	for i := range txLeaves {
		txLeaves[i] = chunk
	}

	provider := &fakeReplayDeltaProvider{header: header, txLeaves: txLeaves}

	events := make(chan Event, 1)
	h := NewLedgerSyncHandler(events)
	h.SetProvider(provider)

	hash := fixedHash()
	err := h.HandleMessage(context.Background(), PeerID(5), &message.ReplayDeltaRequest{LedgerHash: hash})
	require.NoError(t, err)

	resp := drainReplayDeltaResponse(t, events)
	assert.Equal(t, message.ReplyErrorNoLedger, resp.Error)
	assert.Equal(t, hash, resp.LedgerHash)
	assert.Empty(t, resp.LedgerHeader, "oversized response must drop the header")
	assert.Empty(t, resp.Transactions, "oversized response must drop the tx list")
}
