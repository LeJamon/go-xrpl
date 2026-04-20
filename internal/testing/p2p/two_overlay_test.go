// Package p2p contains end-to-end integration tests that wire two real
// peermanagement.Overlay instances together over a localhost TLS
// connection and exercise the wire-level message paths added in
// Phase A/B of the P2P plan. These tests catch wire-level integration
// bugs (compression flags, framing off-by-one, protobuf field order,
// dispatch routing) that the per-layer unit tests cannot.
package p2p

import (
	"bytes"
	"context"
	"encoding/hex"
	"testing"
	"time"

	"github.com/LeJamon/goXRPLd/codec/binarycodec"
	"github.com/LeJamon/goXRPLd/crypto/common"
	"github.com/LeJamon/goXRPLd/drops"
	"github.com/LeJamon/goXRPLd/internal/consensus/adaptor"
	"github.com/LeJamon/goXRPLd/internal/ledger"
	"github.com/LeJamon/goXRPLd/internal/ledger/genesis"
	"github.com/LeJamon/goXRPLd/internal/ledger/header"
	"github.com/LeJamon/goXRPLd/internal/ledger/inbound"
	"github.com/LeJamon/goXRPLd/internal/peermanagement"
	"github.com/LeJamon/goXRPLd/internal/peermanagement/message"
	"github.com/LeJamon/goXRPLd/protocol"
	"github.com/LeJamon/goXRPLd/shamap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// vlEncode mirrors internal/tx EncodeVL so the test file is
// self-contained.
func vlEncode(length int) []byte {
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

// makeMetaBlob serializes a metadata STObject carrying just enough for
// the inbound parser to extract sfTransactionIndex.
func makeMetaBlob(t *testing.T, txIndex uint32) []byte {
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

// makeTxLeaf builds a SHAMapItem-formatted (VL(tx) + VL(meta)) leaf
// blob and returns (blob, txID).
func makeTxLeaf(t *testing.T, payload []byte, txIndex uint32) ([]byte, [32]byte) {
	t.Helper()
	meta := makeMetaBlob(t, txIndex)
	txID := common.Sha512Half(protocol.HashPrefixTransactionID[:], payload)
	blob := make([]byte, 0, len(payload)+len(meta)+4)
	blob = append(blob, vlEncode(len(payload))...)
	blob = append(blob, payload...)
	blob = append(blob, vlEncode(len(meta))...)
	blob = append(blob, meta...)
	return blob, txID
}

// makeImmutableLedger produces a parent + child pair sharing genesis
// as the bedrock, with `txCount` synthetic txs in the child.
func makeImmutableLedger(t *testing.T, txCount int) (parent, child *ledger.Ledger) {
	t.Helper()
	res, err := genesis.Create(genesis.DefaultConfig())
	require.NoError(t, err)
	parent = ledger.FromGenesis(res.Header, res.StateMap, res.TxMap, drops.Fees{})

	open, err := ledger.NewOpen(parent, time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC))
	require.NoError(t, err)
	for i := 0; i < txCount; i++ {
		payload := []byte{0x10, 0x20, 0x30, byte(i), 0x99, 0xAA, 0xBB, 0xCC, 0xDD, 0xEE, 0xFF, 0x01}
		blob, id := makeTxLeaf(t, payload, uint32(i))
		require.NoError(t, open.AddTransactionWithMeta(id, blob))
	}
	require.NoError(t, open.Close(time.Date(2025, 1, 2, 3, 4, 15, 0, time.UTC), 0))
	child = open
	return
}

// inMemoryLookup is a minimal LedgerProvider lookup backed by a map.
type inMemoryLookup struct {
	byHash map[[32]byte]*ledger.Ledger
}

// lookupProvider implements peermanagement.LedgerProvider over the
// in-memory lookup.
type lookupProvider struct {
	lookup *inMemoryLookup
}

func (p *lookupProvider) GetLedgerHeader(hash []byte, _ uint32) ([]byte, error) {
	if h, ok := to32(hash); ok {
		if l := p.lookup.byHash[h]; l != nil {
			return l.SerializeHeader(), nil
		}
	}
	return nil, nil
}

func (p *lookupProvider) GetAccountStateNode(_ []byte, _ []byte) ([]byte, error) {
	return nil, nil
}

func (p *lookupProvider) GetTransactionNode(_ []byte, _ []byte) ([]byte, error) {
	return nil, nil
}

func (p *lookupProvider) GetReplayDelta(hash []byte) ([]byte, [][]byte, error) {
	h, ok := to32(hash)
	if !ok {
		return nil, nil, nil
	}
	l := p.lookup.byHash[h]
	if l == nil || !l.IsImmutable() {
		return nil, nil, nil
	}
	hdr := l.Header()
	headerBytes, err := header.AddRaw(hdr, false)
	if err != nil {
		return nil, nil, err
	}
	txMap, err := l.TxMapSnapshot()
	if err != nil {
		return nil, nil, err
	}
	var leaves [][]byte
	if err := txMap.ForEach(func(item *shamap.Item) bool {
		leaves = append(leaves, append([]byte(nil), item.Data()...))
		return true
	}); err != nil {
		return nil, nil, err
	}
	return headerBytes, leaves, nil
}

func (p *lookupProvider) GetProofPath(_ []byte, _ []byte, _ message.LedgerMapType) ([]byte, [][]byte, error) {
	return nil, nil, nil
}

func to32(b []byte) ([32]byte, bool) {
	var out [32]byte
	if len(b) != 32 {
		return out, false
	}
	copy(out[:], b)
	return out, true
}

// startOverlay spins up a single Overlay bound to localhost on an
// ephemeral port. Returns the overlay and a cancel function.
func startOverlay(t *testing.T) (*peermanagement.Overlay, context.CancelFunc) {
	t.Helper()
	dataDir := t.TempDir()
	o, err := peermanagement.New(
		peermanagement.WithListenAddr("127.0.0.1:0"),
		peermanagement.WithDataDir(dataDir),
		peermanagement.WithMaxPeers(4),
		peermanagement.WithMaxInbound(2),
		peermanagement.WithMaxOutbound(2),
		peermanagement.WithReduceRelay(false),
		peermanagement.WithCompression(false),
	)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = o.Run(ctx) }()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if o.ListenAddr() != "" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	require.NotEmpty(t, o.ListenAddr(), "overlay listener never came up")
	return o, cancel
}

// waitForPeers polls until both overlays have a connected peer, or the
// deadline expires.
func waitForPeers(t *testing.T, a, b *peermanagement.Overlay, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if a.PeerCount() > 0 && b.PeerCount() > 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("peers never connected (a=%d, b=%d)", a.PeerCount(), b.PeerCount())
}

// TestTwoOverlay_HandshakeAndConnect verifies that two real overlays
// can complete the XRPL TLS+HTTP handshake against each other on
// localhost. This is the canary that confirms the transport stack
// (TLS 1.2 + HTTP upgrade + shared-value derivation) works end-to-end.
func TestTwoOverlay_HandshakeAndConnect(t *testing.T) {
	if testing.Short() {
		t.Skip("two-overlay handshake is heavyweight; skipped in -short mode")
	}

	a, cancelA := startOverlay(t)
	defer cancelA()
	defer a.Stop()

	b, cancelB := startOverlay(t)
	defer cancelB()
	defer b.Stop()

	require.NoError(t, b.Connect(a.ListenAddr()),
		"two overlays must complete the XRPL TLS+HTTP handshake against each other")
	waitForPeers(t, a, b, 5*time.Second)

	infosA := a.Peers()
	infosB := b.Peers()
	require.Len(t, infosA, 1, "A must see exactly one inbound peer")
	require.Len(t, infosB, 1, "B must see exactly one outbound peer")
	assert.True(t, infosA[0].Inbound, "A's view of B must be inbound")
	assert.False(t, infosB[0].Inbound, "B's view of A must be outbound")
}

// TestTwoOverlay_PostHandshakeSendReceive verifies that after the XRPL
// TLS+HTTP handshake completes between two real Overlay instances, an
// application-layer wire frame sent by one side is observed by the
// other side's readLoop. This is the minimal smoke test for peer-to-peer
// I/O after handshake. The heavier-framing regression test — which
// caught the missing wire-header bug on the ledger-sync reply path —
// is TestTwoOverlay_ReplayDelta_RoundTrip below.
func TestTwoOverlay_PostHandshakeSendReceive(t *testing.T) {
	if testing.Short() {
		t.Skip("two-overlay send/receive is heavyweight; skipped in -short mode")
	}

	a, cancelA := startOverlay(t)
	defer cancelA()
	defer a.Stop()

	b, cancelB := startOverlay(t)
	defer cancelB()
	defer b.Stop()

	require.NoError(t, b.Connect(a.ListenAddr()),
		"two overlays must complete the XRPL TLS+HTTP handshake")
	waitForPeers(t, a, b, 5*time.Second)

	// Find the peer-id of B as seen by A (the inbound peer on A). We
	// send from A -> B (A initiates the send, B's readLoop must
	// surface it on Messages()).
	infosA := a.Peers()
	require.Len(t, infosA, 1)
	targetPeerID := infosA[0].ID

	// Build a minimal Ping frame. Ping is the smallest valid wire
	// frame and is treated as ordinary application traffic at the
	// framing layer.
	ping := &message.Ping{PType: message.PingTypePing, Seq: 0xDEADBEEF}
	encoded, err := message.Encode(ping)
	require.NoError(t, err)
	frame, err := message.BuildWireMessage(message.TypePing, encoded)
	require.NoError(t, err)

	require.NoError(t, a.Send(targetPeerID, frame),
		"A.Send must enqueue the frame to B")

	// Wait for B's inbound traffic counter to tick, which proves
	// B's readLoop actually parsed a frame off the wire. (B's overlay
	// intercepts mtPING internally and does not forward it to the
	// external Messages() channel, so we can't wait on Messages()
	// alone for the simplest possible frame.)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, info := range b.Peers() {
			if info.MessagesIn > 0 {
				return // success: a frame crossed the wire and was decoded
			}
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Diagnostic dump: enumerate both sides' peer state so the
	// failure message tells us whether the bytes left A's writeLoop
	// or never reached B's readLoop.
	var diag string
	for _, info := range a.Peers() {
		diag += "  A.peer: id=" + formatPeerID(info.ID) +
			" state=" + info.State.String() +
			" in=" + formatUint(info.MessagesIn) +
			" out=" + formatUint(info.MessagesOut) + "\n"
	}
	for _, info := range b.Peers() {
		diag += "  B.peer: id=" + formatPeerID(info.ID) +
			" state=" + info.State.String() +
			" in=" + formatUint(info.MessagesIn) +
			" out=" + formatUint(info.MessagesOut) + "\n"
	}
	t.Fatalf("B never observed the PING frame A sent after handshake\n%s", diag)
}

func formatPeerID(id peermanagement.PeerID) string { return formatUint(uint64(id)) }

// TestTwoOverlay_ReplayDelta_RoundTrip drives a full wire round-trip of
// mtREPLAY_DELTA_REQUEST → mtREPLAY_DELTA_RESPONSE between two real
// Overlay instances. This covers: A holds an immutable ledger via a
// real LedgerProvider; B asks A for it; A's ledger-sync handler
// answers; B's consensus router verifies the response and adopts the
// derived ledger.
//
// This test can only pass if the overlay transport actually delivers
// non-trivial frames end-to-end after handshake. It is the regression
// test for the missing-wire-header bug on LedgerSyncHandler's reply
// path: sendReplayDeltaResponse used to ship bare protobuf bytes onto
// the events channel, which Overlay.onLedgerResponse handed straight
// to peer.Send — leaving the receiver to parse the first 6 protobuf
// bytes as a garbage frame header and stall forever on the phantom
// payload. See sendReplayDeltaResponse in ledgersync.go.
func TestTwoOverlay_ReplayDelta_RoundTrip(t *testing.T) {
	if testing.Short() {
		t.Skip("two-overlay replay-delta is heavyweight; skipped in -short mode")
	}

	parent, child := makeImmutableLedger(t, 3)
	lookup := &inMemoryLookup{byHash: map[[32]byte]*ledger.Ledger{
		parent.Hash(): parent,
		child.Hash():  child,
	}}

	a, cancelA := startOverlay(t)
	defer cancelA()
	defer a.Stop()

	b, cancelB := startOverlay(t)
	defer cancelB()
	defer b.Stop()

	// Wire the provider onto A's ledger-sync handler so A can answer
	// replay-delta requests from a real on-the-wire peer.
	a.LedgerSync().SetProvider(&lookupProvider{lookup: lookup})

	require.NoError(t, b.Connect(a.ListenAddr()),
		"two overlays must complete the XRPL TLS+HTTP handshake")
	waitForPeers(t, a, b, 5*time.Second)

	// B's view of A (outbound peer on B).
	infosB := b.Peers()
	require.Len(t, infosB, 1)
	peerAOnB := infosB[0].ID

	// B sends the replay-delta request to A over the wire.
	sender := adaptor.NewOverlaySender(b)
	hash := child.Hash()
	require.NoError(t, sender.RequestReplayDelta(uint64(peerAOnB), hash),
		"B must be able to send a replay-delta request to A")

	// The response is delivered to B via its Messages() channel.
	var resp *message.ReplayDeltaResponse
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()

Loop:
	for {
		select {
		case msg := <-b.Messages():
			if message.MessageType(msg.Type) != message.TypeReplayDeltaResponse {
				continue
			}
			decoded, err := message.Decode(message.TypeReplayDeltaResponse, msg.Payload)
			require.NoError(t, err)
			resp = decoded.(*message.ReplayDeltaResponse)
			break Loop
		case <-timer.C:
			t.Fatal("B never received the replay-delta response from A over the wire")
		}
	}

	// Verify the round-tripped response reconstructs the exact ledger.
	rd := inbound.NewReplayDelta(hash, uint64(peerAOnB), parent, nil)
	require.NoError(t, rd.GotResponse(resp))
	got, err := rd.Result()
	require.NoError(t, err)
	assert.Equal(t, child.Hash(), got.Hash(),
		"the derived ledger hash must byte-match A's source ledger")
	assert.Equal(t, child.Sequence(), got.Sequence())
}

// TestTwoOverlay_Squelch_RoundTrip drives a full wire round-trip of
// a TMSquelch frame between two real Overlay instances. A tells B to
// squelch validator V; B decodes the frame and records the squelch on
// its view of A. The test verifies the squelch state becomes observable
// on B's side within 1s. Mirrors rippled's OverlayImpl::squelch which
// is the transport counterpart of the reduce-relay selection logic.
func TestTwoOverlay_Squelch_RoundTrip(t *testing.T) {
	if testing.Short() {
		t.Skip("two-overlay squelch round-trip is heavyweight; skipped in -short mode")
	}

	a, cancelA := startOverlay(t)
	defer cancelA()
	defer a.Stop()

	b, cancelB := startOverlay(t)
	defer cancelB()
	defer b.Stop()

	require.NoError(t, b.Connect(a.ListenAddr()),
		"two overlays must complete the XRPL TLS+HTTP handshake")
	waitForPeers(t, a, b, 5*time.Second)

	// Both overlays see exactly one peer.
	infosA := a.Peers()
	infosB := b.Peers()
	require.Len(t, infosA, 1)
	require.Len(t, infosB, 1)
	peerBOnA := infosA[0].ID // A's view of B (inbound)
	peerAOnB := infosB[0].ID // B's view of A (outbound)

	// A 33-byte validator pub-key — matches rippled's secp256k1-compressed
	// format. The exact bytes are irrelevant; only the map-key round-trip
	// is under test.
	validator := make([]byte, 33)
	for i := range validator {
		validator[i] = byte(0x10 | i)
	}

	// A squelches V on its view of B (peerBOnA). The overlay sends a
	// TMSquelch frame to B; B's handleSquelchMessage records it on its
	// peer-view-of-A.
	a.IssueSquelch(validator, peerBOnA, true, 5*time.Minute)

	// Poll for the squelch to show up on B's side. We bound the wait to
	// 1s per the spec; on localhost this is typically delivered in single
	// digits of milliseconds.
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		if b.IsValidatorSquelchedOnPeer(peerAOnB, validator) {
			// Also verify the inverse removal path end-to-end: A
			// clears the squelch; B must observe it gone.
			a.IssueSquelch(validator, peerBOnA, false, 0)
			deadline2 := time.Now().Add(1 * time.Second)
			for time.Now().Before(deadline2) {
				if !b.IsValidatorSquelchedOnPeer(peerAOnB, validator) {
					return
				}
				time.Sleep(10 * time.Millisecond)
			}
			t.Fatal("B kept the squelch after A sent an unsquelch")
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("B never observed the squelch A sent over the wire")
}

// TestTwoOverlay_ProofPath_RoundTrip drives a full wire round-trip of
// mtPROOF_PATH_REQ → mtPROOF_PATH_RESPONSE between two real Overlay
// instances. B requests a proof for a known key in A's state map; A
// serves it from a real in-memory lookup; B decodes and verifies the
// returned path against A's state root.
func TestTwoOverlay_ProofPath_RoundTrip(t *testing.T) {
	if testing.Short() {
		t.Skip("two-overlay proof-path is heavyweight; skipped in -short mode")
	}

	// Build a small immutable ledger and pick one known key from the
	// tx map to prove against.
	parent, child := makeImmutableLedger(t, 3)
	var knownKey [32]byte
	txMap, err := child.TxMapSnapshot()
	require.NoError(t, err)
	require.NoError(t, txMap.ForEach(func(item *shamap.Item) bool {
		knownKey = item.Key()
		return false // stop on first item
	}))

	// The lookup must serve proofs for the tx map of the child ledger.
	// Build a provider whose GetProofPath returns the serialized
	// leaf-to-root nodes from the real SHAMap.
	lookup := &inMemoryLookup{byHash: map[[32]byte]*ledger.Ledger{
		parent.Hash(): parent,
		child.Hash():  child,
	}}
	provider := &proofPathLookupProvider{
		base: &lookupProvider{lookup: lookup},
		tx: func(hash []byte, key []byte) ([]byte, [][]byte, error) {
			l, ok := lookup.byHash[toArr32(hash)]
			if !ok {
				return nil, nil, peermanagement.ErrLedgerNotFound
			}
			txSnap, err := l.TxMapSnapshot()
			if err != nil {
				return nil, nil, err
			}
			var k [32]byte
			copy(k[:], key)
			proof, err := txSnap.GetProofPath(k)
			if err != nil {
				return nil, nil, err
			}
			if proof == nil || !proof.Found {
				return nil, nil, peermanagement.ErrKeyNotFound
			}
			hdr, err := header.AddRaw(l.Header(), false)
			if err != nil {
				return nil, nil, err
			}
			return hdr, proof.Path, nil
		},
	}

	a, cancelA := startOverlay(t)
	defer cancelA()
	defer a.Stop()

	b, cancelB := startOverlay(t)
	defer cancelB()
	defer b.Stop()

	a.LedgerSync().SetProvider(provider)

	require.NoError(t, b.Connect(a.ListenAddr()))
	waitForPeers(t, a, b, 5*time.Second)

	infosB := b.Peers()
	require.Len(t, infosB, 1)
	peerAOnB := infosB[0].ID

	// B sends the proof-path request via the overlay Send API. The
	// frame is built here rather than going through OverlaySender
	// because no sender method exists for proof-path; this exercises
	// the overlay's Send path directly (same path sender methods take).
	hash := child.Hash()
	req := &message.ProofPathRequest{
		Key:        knownKey[:],
		LedgerHash: hash[:],
		MapType:    message.LedgerMapTransaction,
	}
	encoded, err := message.Encode(req)
	require.NoError(t, err)
	frame, err := message.BuildWireMessage(message.TypeProofPathReq, encoded)
	require.NoError(t, err)
	require.NoError(t, b.Send(peerAOnB, frame))

	// Read B.Messages() until the proof-path response lands.
	var resp *message.ProofPathResponse
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
Loop:
	for {
		select {
		case msg := <-b.Messages():
			if message.MessageType(msg.Type) != message.TypeProofPathResponse {
				continue
			}
			decoded, err := message.Decode(message.TypeProofPathResponse, msg.Payload)
			require.NoError(t, err)
			resp = decoded.(*message.ProofPathResponse)
			break Loop
		case <-timer.C:
			t.Fatal("B never received the proof-path response from A over the wire")
		}
	}

	require.Equal(t, message.ReplyErrorNone, resp.Error)
	require.NotEmpty(t, resp.Path)

	// Verify the proof against A's tx-map root. The root hash lives on
	// the ledger header — we pull it out of the child directly since
	// it's the same ledger A serialized.
	txRoot := child.Header().TxHash
	assert.True(t,
		shamap.VerifyProofPath(txRoot, knownKey, resp.Path),
		"proof returned by A must verify against the tx-map root")
}

// proofPathLookupProvider wraps lookupProvider and overrides
// GetProofPath with a closure, so the proof-path round-trip test can
// drive a real SHAMap lookup without duplicating the replay-delta
// plumbing in the shared lookup helper.
type proofPathLookupProvider struct {
	base *lookupProvider
	tx   func(hash, key []byte) ([]byte, [][]byte, error)
}

func (p *proofPathLookupProvider) GetLedgerHeader(hash []byte, seq uint32) ([]byte, error) {
	return p.base.GetLedgerHeader(hash, seq)
}
func (p *proofPathLookupProvider) GetAccountStateNode(h, n []byte) ([]byte, error) {
	return p.base.GetAccountStateNode(h, n)
}
func (p *proofPathLookupProvider) GetTransactionNode(h, n []byte) ([]byte, error) {
	return p.base.GetTransactionNode(h, n)
}
func (p *proofPathLookupProvider) GetReplayDelta(h []byte) ([]byte, [][]byte, error) {
	return p.base.GetReplayDelta(h)
}
func (p *proofPathLookupProvider) GetProofPath(h, k []byte, m message.LedgerMapType) ([]byte, [][]byte, error) {
	if m != message.LedgerMapTransaction {
		return nil, nil, peermanagement.ErrKeyNotFound
	}
	return p.tx(h, k)
}

func toArr32(b []byte) [32]byte {
	var out [32]byte
	copy(out[:], b)
	return out
}

func formatUint(v uint64) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}

// TestSingleOverlay_ReplayDelta_HandlerRoundTrip drives the request →
// handler → provider → response → verifier loop end-to-end against a
// real *peermanagement.Overlay's ledger-sync handler. Catches:
//
//   - Provider invocation against a real *ledger.Ledger
//   - Wire-encode of TMReplayDeltaResponse on the events channel
//   - Wire-decode by the consumer
//   - Tx-SHAMap reconstruction and root-vs-header verification
//
// The matching unit tests in internal/peermanagement and
// internal/ledger/inbound cover individual error branches; this one
// catches integration regressions.
func TestSingleOverlay_ReplayDelta_HandlerRoundTrip(t *testing.T) {
	parent, child := makeImmutableLedger(t, 3)
	lookup := &inMemoryLookup{byHash: map[[32]byte]*ledger.Ledger{
		parent.Hash(): parent,
		child.Hash():  child,
	}}
	provider := &lookupProvider{lookup: lookup}

	// Spin up a fresh handler with a test-controlled events channel so
	// the response is observable. The overlay's eventLoop would
	// otherwise consume the response and try to ship it to a
	// non-existent peer.
	events := make(chan peermanagement.Event, 4)
	h := peermanagement.NewLedgerSyncHandler(events)
	h.SetProvider(provider)

	hash := child.Hash()
	require.NoError(t, h.HandleMessage(
		context.Background(),
		peermanagement.PeerID(7),
		&message.ReplayDeltaRequest{LedgerHash: hash[:]},
	))

	var resp *message.ReplayDeltaResponse
	select {
	case evt := <-events:
		require.Equal(t, peermanagement.EventLedgerResponse, evt.Type)
		// The handler now wire-frames its responses (6-byte header +
		// protobuf body) so the overlay's onLedgerResponse can hand the
		// payload straight to the peer's send queue without needing to
		// know the message type. See LedgerSyncHandler.sendReplayDeltaResponse.
		hdr, body, err := message.ReadMessage(bytes.NewReader(evt.Payload))
		require.NoError(t, err, "event payload must be a valid wire frame")
		require.Equal(t, message.TypeReplayDeltaResponse, hdr.MessageType)
		decoded, err := message.Decode(message.TypeReplayDeltaResponse, body)
		require.NoError(t, err)
		resp = decoded.(*message.ReplayDeltaResponse)
	case <-time.After(2 * time.Second):
		t.Fatal("no ledger response on the events channel")
	}

	rd := inbound.NewReplayDelta(hash, 7, parent, nil)
	require.NoError(t, rd.GotResponse(resp),
		"the round-tripped response must verify cleanly")

	got, err := rd.Result()
	require.NoError(t, err)
	assert.Equal(t, child.Hash(), got.Hash(), "round-trip must reconstruct the same ledger")
	assert.Equal(t, child.Sequence(), got.Sequence())
	assert.Len(t, rd.OrderedTxs(), 3, "all three txs must be preserved through the round-trip")
}
