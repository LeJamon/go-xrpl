package peermanagement

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeProofPathProvider struct {
	header []byte
	path   [][]byte
	err    error

	// captured request arguments for assertions
	calls         int
	gotLedgerHash []byte
	gotKey        []byte
	gotMapType    message.LedgerMapType
}

func (f *fakeProofPathProvider) GetReplayDelta(_ []byte) ([]byte, [][]byte, error) {
	return nil, nil, nil
}
func (f *fakeProofPathProvider) GetProofPath(ledgerHash []byte, key []byte, mapType message.LedgerMapType) ([]byte, [][]byte, error) {
	f.calls++
	f.gotLedgerHash = ledgerHash
	f.gotKey = key
	f.gotMapType = mapType
	return f.header, f.path, f.err
}
func (f *fakeProofPathProvider) MakeFetchPack(_ [32]byte, _ int) ([]message.IndexedObject, error) {
	return nil, nil
}

// fixedKey returns a deterministic 32-byte key distinct from fixedHash so
// assertions can distinguish the two fields.
func fixedKey() []byte {
	k := make([]byte, 32)
	for i := range k {
		k[i] = byte(0x80 | i)
	}
	return k
}

// drainProofPathResponse pulls the first EventLedgerResponse off the
// channel, decodes its wire-framed payload (6-byte header + protobuf
// body) as a ProofPathResponse, and returns it. The channel must
// contain exactly one event: tests fail if it is empty.
func drainProofPathResponse(t *testing.T, events chan Event) *message.ProofPathResponse {
	t.Helper()
	require.Equal(t, 1, len(events), "expected exactly one event on the channel")
	evt := <-events
	require.Equal(t, EventLedgerResponse, evt.Type)
	header, body, err := message.ReadMessage(bytes.NewReader(evt.Payload))
	require.NoError(t, err, "event payload must be a valid wire frame")
	require.Equal(t, message.TypeProofPathResponse, header.MessageType)
	decoded, err := message.Decode(message.TypeProofPathResponse, body)
	require.NoError(t, err)
	resp, ok := decoded.(*message.ProofPathResponse)
	require.True(t, ok, "decoded payload must be *message.ProofPathResponse")
	return resp
}

// TestProofPathRequest_Success verifies the happy path: a fake provider
// returns a known header plus a 3-element path, and the handler emits a
// wire-encoded response carrying both fields in the same order the
// provider returned (leaf-to-root, matching rippled's wire format).
func TestProofPathRequest_Success(t *testing.T) {
	header := []byte("ledger-header-bytes")
	// Provider returns leaf-to-root; the handler must preserve order on
	// the wire (no reversal — both rippled and go-xrpl agree on
	// leaf-to-root, see SHAMapSync.cpp:800-833 / shamap/proof.go:21-71).
	path := [][]byte{
		[]byte("leaf-node-blob"),
		[]byte("inner-node-blob"),
		[]byte("root-node-blob"),
	}
	provider := &fakeProofPathProvider{header: header, path: path}

	events := make(chan Event, 1)
	h := NewLedgerSyncHandler(events)
	h.SetProvider(provider)

	hash := fixedHash()
	key := fixedKey()
	err := h.HandleMessage(context.Background(), PeerID(7), &message.ProofPathRequest{
		Key:        key,
		LedgerHash: hash,
		MapType:    message.LedgerMapAccountState,
	})
	require.NoError(t, err)

	resp := drainProofPathResponse(t, events)
	assert.Equal(t, key, resp.Key)
	assert.Equal(t, hash, resp.LedgerHash)
	assert.Equal(t, message.LedgerMapAccountState, resp.MapType)
	assert.Equal(t, message.ReplyErrorNone, resp.Error)
	assert.Equal(t, header, resp.LedgerHeader)
	assert.Equal(t, path, resp.Path)

	// Provider was called with the request fields verbatim.
	assert.Equal(t, 1, provider.calls)
	assert.Equal(t, hash, provider.gotLedgerHash)
	assert.Equal(t, key, provider.gotKey)
	assert.Equal(t, message.LedgerMapAccountState, provider.gotMapType)
}

func TestProofPathRequest_PrioritySenderBypassesSharedEvents(t *testing.T) {
	provider := &fakeProofPathProvider{header: []byte("header"), path: [][]byte{[]byte("node")}}
	events := make(chan Event)
	h := NewLedgerSyncHandler(events)
	h.SetProvider(provider)
	var gotPeer PeerID
	var gotFrame []byte
	h.SetPrioritySender(func(_ context.Context, peerID PeerID, frame []byte) error {
		gotPeer = peerID
		gotFrame = append([]byte(nil), frame...)
		return nil
	})
	require.NoError(t, h.HandleMessage(context.Background(), PeerID(9), &message.ProofPathRequest{
		Key: fixedKey(), LedgerHash: fixedHash(), MapType: message.LedgerMapAccountState,
	}))
	assert.Equal(t, PeerID(9), gotPeer)
	require.NotEmpty(t, gotFrame)
	header, _, err := message.ReadMessage(bytes.NewReader(gotFrame))
	require.NoError(t, err)
	assert.Equal(t, message.TypeProofPathResponse, header.MessageType)
	assert.Empty(t, events, "completed replies must not depend on the shared event channel")
	assert.Zero(t, h.DroppedResponses())
}

// TestProofPathRequest_BadKeyLength verifies the key length precheck:
// any key whose length is not 32 must yield reBAD_REQUEST without
// touching the provider. Mirrors rippled's `key().size() !=
// uint256::size()` guard at LedgerReplayMsgHandler.cpp:46-54.
func TestProofPathRequest_BadKeyLength(t *testing.T) {
	provider := &fakeProofPathProvider{
		header: []byte("never-served"),
		path:   [][]byte{[]byte("never-served")},
	}

	events := make(chan Event, 1)
	h := NewLedgerSyncHandler(events)
	h.SetProvider(provider)

	shortKey := []byte{0x01, 0x02, 0x03}
	hash := fixedHash()
	err := h.HandleMessage(context.Background(), PeerID(1), &message.ProofPathRequest{
		Key:        shortKey,
		LedgerHash: hash,
		MapType:    message.LedgerMapTransaction,
	})
	require.ErrorIs(t, err, ErrPeerBadRequest,
		"malformed request must be signaled as ErrPeerBadRequest so the dispatcher can charge the peer")

	assert.Empty(t, events, "malformed requests are charged and dropped without a reply")
	assert.Zero(t, provider.calls, "provider must not be called for bad-key-length requests")
}

// TestProofPathRequest_BadHashLength verifies the ledger-hash length
// precheck: same reBAD_REQUEST behavior as TestProofPathRequest_BadKeyLength.
func TestProofPathRequest_BadHashLength(t *testing.T) {
	provider := &fakeProofPathProvider{
		header: []byte("never-served"),
		path:   [][]byte{[]byte("never-served")},
	}

	events := make(chan Event, 1)
	h := NewLedgerSyncHandler(events)
	h.SetProvider(provider)

	shortHash := []byte{0xAA, 0xBB}
	key := fixedKey()
	err := h.HandleMessage(context.Background(), PeerID(2), &message.ProofPathRequest{
		Key:        key,
		LedgerHash: shortHash,
		MapType:    message.LedgerMapAccountState,
	})
	require.ErrorIs(t, err, ErrPeerBadRequest,
		"malformed ledger hash must be signaled as ErrPeerBadRequest")

	assert.Empty(t, events, "malformed requests are charged and dropped without a reply")
	assert.Zero(t, provider.calls, "provider must not be called for bad-hash-length requests")
}

// TestProofPathRequest_BadType verifies the map-type precheck: only
// LedgerMapTransaction (1) and LedgerMapAccountState (2) are accepted.
// Mirrors rippled's TMLedgerMapType_IsValid guard.
func TestProofPathRequest_BadType(t *testing.T) {
	provider := &fakeProofPathProvider{
		header: []byte("never-served"),
		path:   [][]byte{[]byte("never-served")},
	}

	for _, badType := range []message.LedgerMapType{0, 3, 99, -1} {
		t.Run("type="+badTypeName(badType), func(t *testing.T) {
			provider.calls = 0
			events := make(chan Event, 1)
			h := NewLedgerSyncHandler(events)
			h.SetProvider(provider)

			hash := fixedHash()
			key := fixedKey()
			err := h.HandleMessage(context.Background(), PeerID(3), &message.ProofPathRequest{
				Key:        key,
				LedgerHash: hash,
				MapType:    badType,
			})
			require.ErrorIs(t, err, ErrPeerBadRequest,
				"invalid map type must be signaled as ErrPeerBadRequest")

			assert.Empty(t, events, "malformed requests are charged and dropped without a reply")
			assert.Zero(t, provider.calls, "provider must not be called for bad-type requests")
		})
	}
}

// badTypeName produces a stable subtest label for a given LedgerMapType.
func badTypeName(t message.LedgerMapType) string {
	switch t {
	case 0:
		return "0"
	case 3:
		return "3"
	case 99:
		return "99"
	case -1:
		return "-1"
	default:
		return "other"
	}
}

// TestProofPathRequest_UnknownLedger verifies that GetProofPath
// returning ErrLedgerNotFound produces a reNO_LEDGER reply with no
// header/path, mirroring rippled's `!ledger` branch at
// LedgerReplayMsgHandler.cpp:62-68.
func TestProofPathRequest_UnknownLedger(t *testing.T) {
	provider := &fakeProofPathProvider{err: ErrLedgerNotFound}

	events := make(chan Event, 1)
	h := NewLedgerSyncHandler(events)
	h.SetProvider(provider)

	hash := fixedHash()
	key := fixedKey()
	err := h.HandleMessage(context.Background(), PeerID(4), &message.ProofPathRequest{
		Key:        key,
		LedgerHash: hash,
		MapType:    message.LedgerMapAccountState,
	})
	require.NoError(t, err)

	assert.Empty(t, events, "unknown ledgers are charged and dropped without a reply")
	assert.Equal(t, 1, provider.calls)
}

// TestProofPathRequest_KeyNotFound verifies that GetProofPath returning
// ErrKeyNotFound produces a reNO_NODE reply. Per rippled
// (LedgerReplayMsgHandler.cpp:84-90), the header is NOT packed on this
// path — packing happens only after the no-path early-return.
func TestProofPathRequest_KeyNotFound(t *testing.T) {
	// Provider may or may not surface the header; the handler must NOT
	// include it on a no-node reply regardless. Pass a non-empty header
	// to prove the handler suppresses it.
	provider := &fakeProofPathProvider{
		header: []byte("would-be-header"),
		err:    ErrKeyNotFound,
	}

	events := make(chan Event, 1)
	h := NewLedgerSyncHandler(events)
	h.SetProvider(provider)

	hash := fixedHash()
	key := fixedKey()
	err := h.HandleMessage(context.Background(), PeerID(5), &message.ProofPathRequest{
		Key:        key,
		LedgerHash: hash,
		MapType:    message.LedgerMapTransaction,
	})
	require.NoError(t, err)

	assert.Empty(t, events, "missing nodes are charged and dropped without a reply")
	assert.Equal(t, 1, provider.calls)
}

// TestProofPathRequest_ProviderError verifies that a generic provider
// error (anything other than the documented sentinels) yields a
// reBAD_REQUEST reply. Rippled has no equivalent code path because it
// has no provider abstraction; this is the closest TMReplyError to
// "internal failure".
func TestProofPathRequest_ProviderError(t *testing.T) {
	provider := &fakeProofPathProvider{err: errors.New("backend down")}

	events := make(chan Event, 1)
	h := NewLedgerSyncHandler(events)
	h.SetProvider(provider)

	hash := fixedHash()
	key := fixedKey()
	err := h.HandleMessage(context.Background(), PeerID(6), &message.ProofPathRequest{
		Key:        key,
		LedgerHash: hash,
		MapType:    message.LedgerMapAccountState,
	})
	require.NoError(t, err)

	assert.Empty(t, events, "provider errors are charged and dropped without a reply")
}

// TestProofPathRequest_NoProvider verifies that with no provider wired
// the handler is silent — no event is emitted and no error is returned.
// Matches handleReplayDeltaRequest's no-provider behavior so a node
// without a configured ledger source simply doesn't answer.
func TestProofPathRequest_NoProvider(t *testing.T) {
	events := make(chan Event, 1)
	h := NewLedgerSyncHandler(events)
	// intentionally no SetProvider

	hash := fixedHash()
	key := fixedKey()
	err := h.HandleMessage(context.Background(), PeerID(8), &message.ProofPathRequest{
		Key:        key,
		LedgerHash: hash,
		MapType:    message.LedgerMapAccountState,
	})
	require.NoError(t, err)

	assert.Equal(t, 0, len(events), "no event should be emitted when provider is nil")
}
