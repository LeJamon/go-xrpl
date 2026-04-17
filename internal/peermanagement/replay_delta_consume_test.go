package peermanagement

import (
	"context"
	"testing"

	"github.com/LeJamon/goXRPLd/internal/peermanagement/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// wellFormedReplayDeltaResponse builds a ReplayDeltaResponse with a known
// hash, header and three transaction blobs. The contents are deliberately
// short — the peermanagement layer does no parsing, so the bytes are only
// asserted on for round-trip equality.
func wellFormedReplayDeltaResponse() *message.ReplayDeltaResponse {
	return &message.ReplayDeltaResponse{
		LedgerHash:   fixedHash(),
		LedgerHeader: []byte("ledger-header-bytes"),
		Transactions: [][]byte{
			[]byte("tx-leaf-1"),
			[]byte("tx-leaf-2"),
			[]byte("tx-leaf-3"),
		},
	}
}

// TestReplayDeltaResponse_Success verifies the happy path: a well-formed
// response is re-published as EventReplayDeltaReceived with the original
// fields intact after a wire encode/decode round-trip.
func TestReplayDeltaResponse_Success(t *testing.T) {
	events := make(chan Event, 1)
	h := NewLedgerSyncHandler(events)

	resp := wellFormedReplayDeltaResponse()
	err := h.HandleMessage(context.Background(), PeerID(7), resp)
	require.NoError(t, err)

	select {
	case ev := <-events:
		assert.Equal(t, EventReplayDeltaReceived, ev.Type)
		assert.Equal(t, PeerID(7), ev.PeerID)

		decoded, err := message.Decode(message.TypeReplayDeltaResponse, ev.Payload)
		require.NoError(t, err)
		got, ok := decoded.(*message.ReplayDeltaResponse)
		require.True(t, ok, "decoded payload must be *message.ReplayDeltaResponse")

		assert.Equal(t, resp.LedgerHash, got.LedgerHash)
		assert.Equal(t, resp.LedgerHeader, got.LedgerHeader)
		assert.Equal(t, resp.Transactions, got.Transactions)
		assert.Equal(t, message.ReplyErrorNone, got.Error)
	default:
		t.Fatal("expected EventReplayDeltaReceived, got none")
	}
}

// TestReplayDeltaResponse_ErrorFlagDropped verifies that a peer-reported
// error response is silently dropped: no event must be emitted because there
// is no payload to surface to the consumer.
func TestReplayDeltaResponse_ErrorFlagDropped(t *testing.T) {
	events := make(chan Event, 1)
	h := NewLedgerSyncHandler(events)

	// reNO_LEDGER mirrors what a peer would send for an unknown ledger.
	resp := &message.ReplayDeltaResponse{
		LedgerHash: fixedHash(),
		Error:      message.ReplyErrorNoLedger,
	}
	err := h.HandleMessage(context.Background(), PeerID(8), resp)
	require.NoError(t, err)

	select {
	case ev := <-events:
		t.Fatalf("unexpected event %v emitted for error-flagged response", ev)
	default:
	}
}

// TestReplayDeltaResponse_EmptyHeaderDropped verifies that responses missing
// the serialized ledger header are dropped: the consumer cannot validate
// anything without it.
func TestReplayDeltaResponse_EmptyHeaderDropped(t *testing.T) {
	events := make(chan Event, 1)
	h := NewLedgerSyncHandler(events)

	resp := wellFormedReplayDeltaResponse()
	resp.LedgerHeader = nil
	err := h.HandleMessage(context.Background(), PeerID(9), resp)
	require.NoError(t, err)

	select {
	case ev := <-events:
		t.Fatalf("unexpected event %v emitted for header-less response", ev)
	default:
	}
}

// TestReplayDeltaResponse_EmptyHashDropped verifies that responses missing
// the ledger hash are dropped: without the hash the consumer has no anchor
// to verify the recomputed header against.
func TestReplayDeltaResponse_EmptyHashDropped(t *testing.T) {
	events := make(chan Event, 1)
	h := NewLedgerSyncHandler(events)

	resp := wellFormedReplayDeltaResponse()
	resp.LedgerHash = nil
	err := h.HandleMessage(context.Background(), PeerID(10), resp)
	require.NoError(t, err)

	select {
	case ev := <-events:
		t.Fatalf("unexpected event %v emitted for hash-less response", ev)
	default:
	}
}

// TestReplayDeltaResponse_NoTransactionsDropped verifies that responses
// without any transaction blobs are dropped: a delta with no transactions
// is not actionable for fast-catchup.
func TestReplayDeltaResponse_NoTransactionsDropped(t *testing.T) {
	events := make(chan Event, 1)
	h := NewLedgerSyncHandler(events)

	resp := wellFormedReplayDeltaResponse()
	resp.Transactions = nil
	err := h.HandleMessage(context.Background(), PeerID(11), resp)
	require.NoError(t, err)

	select {
	case ev := <-events:
		t.Fatalf("unexpected event %v emitted for tx-less response", ev)
	default:
	}
}

// TestDispatchReplayDeltaResponse_RoutesToHandler is the integration check
// that overlay-level dispatch correctly forwards a wire-encoded
// mtREPLAY_DELTA_RESPONSE to the LedgerSyncHandler, which in turn re-publishes
// it as EventReplayDeltaReceived. Mirrors the pattern used by
// TestBroadcastFromValidator_SkipsSquelchedPeers in squelch_test.go.
func TestDispatchReplayDeltaResponse_RoutesToHandler(t *testing.T) {
	events := make(chan Event, 1)
	o := &Overlay{
		peers:      make(map[PeerID]*Peer),
		events:     events,
		ledgerSync: NewLedgerSyncHandler(events),
	}

	resp := wellFormedReplayDeltaResponse()
	encoded, err := message.Encode(resp)
	require.NoError(t, err)

	o.dispatchReplayDeltaResponse(Event{
		Type:        EventMessageReceived,
		PeerID:      PeerID(42),
		MessageType: uint16(message.TypeReplayDeltaResponse),
		Payload:     encoded,
	})

	select {
	case ev := <-events:
		assert.Equal(t, EventReplayDeltaReceived, ev.Type)
		assert.Equal(t, PeerID(42), ev.PeerID)

		decoded, err := message.Decode(message.TypeReplayDeltaResponse, ev.Payload)
		require.NoError(t, err)
		got, ok := decoded.(*message.ReplayDeltaResponse)
		require.True(t, ok)
		assert.Equal(t, resp.LedgerHash, got.LedgerHash)
		assert.Equal(t, resp.LedgerHeader, got.LedgerHeader)
		assert.Equal(t, resp.Transactions, got.Transactions)
	default:
		t.Fatal("expected EventReplayDeltaReceived from overlay dispatch, got none")
	}
}
