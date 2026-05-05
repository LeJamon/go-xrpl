package adaptor

import (
	"bytes"
	"errors"
	"sync"
	"testing"

	"github.com/LeJamon/goXRPLd/internal/manifest"
	"github.com/LeJamon/goXRPLd/internal/peermanagement"
	"github.com/LeJamon/goXRPLd/internal/peermanagement/message"
)

// fakeManifestSender records every Send / Broadcast invocation so a
// test can assert that the router emitted the expected TMManifests
// frame. Peers() lets BroadcastLocalManifest see a non-zero peer count
// without standing up real connections.
type fakeManifestSender struct {
	mu     sync.Mutex
	sends  []sendCall
	bcasts [][]byte
	peers  []peermanagement.PeerInfo
	// sendErr / broadcastErr let individual tests force the error
	// branches in the emitter.
	sendErr      error
	broadcastErr error
}

type sendCall struct {
	peerID peermanagement.PeerID
	frame  []byte
}

func (f *fakeManifestSender) Send(peerID peermanagement.PeerID, frame []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sends = append(f.sends, sendCall{peerID: peerID, frame: append([]byte(nil), frame...)})
	return f.sendErr
}

func (f *fakeManifestSender) Broadcast(frame []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bcasts = append(f.bcasts, append([]byte(nil), frame...))
	return f.broadcastErr
}

func (f *fakeManifestSender) Peers() []peermanagement.PeerInfo {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]peermanagement.PeerInfo, len(f.peers))
	copy(out, f.peers)
	return out
}

// frameToManifestBytes pulls the wire-format manifest STObject back out
// of an emitted TMManifests frame. Confirms the frame round-trips and
// gives tests a single payload to compare against the expected one.
func frameToManifestBytes(t *testing.T, frame []byte) [][]byte {
	t.Helper()
	// Read the wire header off the front of the frame.
	r := bytes.NewReader(frame)
	hdr, payload, err := message.ReadMessage(r)
	if err != nil {
		t.Fatalf("ReadMessage: %v", err)
	}
	if hdr.MessageType != message.TypeManifests {
		t.Fatalf("frame type: got %v want TypeManifests", hdr.MessageType)
	}
	decoded, err := message.Decode(hdr.MessageType, payload)
	if err != nil {
		t.Fatalf("decode TMManifests payload: %v", err)
	}
	mfs, ok := decoded.(*message.Manifests)
	if !ok {
		t.Fatalf("decoded payload not Manifests: %T", decoded)
	}
	out := make([][]byte, 0, len(mfs.List))
	for _, m := range mfs.List {
		out = append(out, m.STObject)
	}
	return out
}

// withTokenIdentity attaches a synthetic token-mode identity to the
// adaptor so the manifest emitter has something to broadcast. The
// fixture comes from identity_token_test.go's newTokenFixture; see
// that file for the token construction details.
func withTokenIdentity(t *testing.T, ad *Adaptor, seed byte, seq uint32) *ValidatorIdentity {
	t.Helper()
	fix := newTokenFixture(t, seed, seq)
	id, err := NewValidatorIdentityFromToken(fix.tokenBlock)
	if err != nil {
		t.Fatalf("NewValidatorIdentityFromToken: %v", err)
	}
	ad.identity = id
	return id
}

func newRouterWithSender(t *testing.T, ad *Adaptor, sender manifestSender) *Router {
	t.Helper()
	router := NewRouter(&mockEngine{}, ad, nil, nil)
	router.testManifestSender = sender
	return router
}

func TestRouter_SendLocalManifestTo_EmitsExpectedFrame(t *testing.T) {
	ad := newTestAdaptor(t)
	id := withTokenIdentity(t, ad, 0x42, 5)

	sender := &fakeManifestSender{}
	router := newRouterWithSender(t, ad, sender)

	router.SendLocalManifestTo(peermanagement.PeerID(17))

	if len(sender.sends) != 1 {
		t.Fatalf("expected 1 Send, got %d", len(sender.sends))
	}
	if sender.sends[0].peerID != 17 {
		t.Errorf("Send peerID: got %v want 17", sender.sends[0].peerID)
	}

	wire := frameToManifestBytes(t, sender.sends[0].frame)
	if len(wire) != 1 {
		t.Fatalf("expected 1 manifest in frame, got %d", len(wire))
	}
	if !bytes.Equal(wire[0], id.SerializedMfst) {
		t.Errorf("emitted manifest bytes do not match local manifest")
	}

	// Round-trip the emitted bytes through Deserialize: confirms the
	// payload is recognized as a valid manifest by the same decoder
	// rippled and our cache use, not just byte-equal to the source.
	parsed, err := manifest.Deserialize(wire[0])
	if err != nil {
		t.Fatalf("emitted manifest fails Deserialize: %v", err)
	}
	if parsed.MasterKey != id.MasterKey {
		t.Errorf("emitted manifest master key mismatch")
	}
	if parsed.Sequence != id.Manifest.Sequence {
		t.Errorf("emitted manifest sequence: got %d want %d", parsed.Sequence, id.Manifest.Sequence)
	}
}

func TestRouter_BroadcastLocalManifest_EmitsToAllPeers(t *testing.T) {
	ad := newTestAdaptor(t)
	id := withTokenIdentity(t, ad, 0x55, 3)

	sender := &fakeManifestSender{
		// Stub three peers — the count shapes the return value and is
		// what BroadcastLocalManifest checks before calling Broadcast.
		peers: []peermanagement.PeerInfo{{}, {}, {}},
	}
	router := newRouterWithSender(t, ad, sender)

	n := router.BroadcastLocalManifest()
	if n != 3 {
		t.Errorf("BroadcastLocalManifest: got %d, want 3", n)
	}
	if len(sender.bcasts) != 1 {
		t.Fatalf("expected 1 Broadcast, got %d", len(sender.bcasts))
	}

	wire := frameToManifestBytes(t, sender.bcasts[0])
	if len(wire) != 1 || !bytes.Equal(wire[0], id.SerializedMfst) {
		t.Errorf("broadcast frame did not carry the local manifest")
	}
}

func TestRouter_BroadcastLocalManifest_NoPeersIsNoOp(t *testing.T) {
	ad := newTestAdaptor(t)
	withTokenIdentity(t, ad, 0x66, 2)

	sender := &fakeManifestSender{} // empty Peers()
	router := newRouterWithSender(t, ad, sender)

	if n := router.BroadcastLocalManifest(); n != 0 {
		t.Errorf("expected 0 with no peers, got %d", n)
	}
	if len(sender.bcasts) != 0 {
		t.Errorf("expected no Broadcast call when peer list is empty, got %d", len(sender.bcasts))
	}
}

func TestRouter_LocalManifestEmission_SeedOnlySkips(t *testing.T) {
	ad := newTestAdaptor(t)
	// Seed-only identity has no Manifest / no SerializedMfst.
	seedID, err := NewValidatorIdentity("snoPBrXtMeMyMHUVTgbuqAfg1SUTb")
	if err != nil {
		t.Fatalf("NewValidatorIdentity: %v", err)
	}
	if seedID.Manifest != nil || len(seedID.SerializedMfst) != 0 {
		t.Fatal("precondition: seed identity must carry no manifest")
	}
	ad.identity = seedID

	sender := &fakeManifestSender{
		peers: []peermanagement.PeerInfo{{}},
	}
	router := newRouterWithSender(t, ad, sender)

	router.SendLocalManifestTo(peermanagement.PeerID(1))
	if n := router.BroadcastLocalManifest(); n != 0 {
		t.Errorf("seed-only broadcast should return 0, got %d", n)
	}
	if len(sender.sends) != 0 || len(sender.bcasts) != 0 {
		t.Errorf("seed-only must not emit: sends=%d bcasts=%d", len(sender.sends), len(sender.bcasts))
	}
}

func TestRouter_LocalManifestEmission_NoIdentitySkips(t *testing.T) {
	ad := newTestAdaptor(t)
	ad.identity = nil

	sender := &fakeManifestSender{
		peers: []peermanagement.PeerInfo{{}},
	}
	router := newRouterWithSender(t, ad, sender)

	router.SendLocalManifestTo(peermanagement.PeerID(1))
	if n := router.BroadcastLocalManifest(); n != 0 {
		t.Errorf("no-identity broadcast should return 0, got %d", n)
	}
	if len(sender.sends) != 0 || len(sender.bcasts) != 0 {
		t.Errorf("no-identity must not emit: sends=%d bcasts=%d", len(sender.sends), len(sender.bcasts))
	}
}

func TestRouter_HandlePeerConnect_DelegatesToSendLocalManifest(t *testing.T) {
	ad := newTestAdaptor(t)
	withTokenIdentity(t, ad, 0x77, 9)

	sender := &fakeManifestSender{}
	router := newRouterWithSender(t, ad, sender)

	router.HandlePeerConnect(peermanagement.PeerID(42))

	if len(sender.sends) != 1 {
		t.Fatalf("expected 1 Send from HandlePeerConnect, got %d", len(sender.sends))
	}
	if sender.sends[0].peerID != 42 {
		t.Errorf("HandlePeerConnect routed to wrong peer: got %v want 42", sender.sends[0].peerID)
	}
}

func TestRouter_SendLocalManifestTo_SwallowsSenderError(t *testing.T) {
	ad := newTestAdaptor(t)
	withTokenIdentity(t, ad, 0x88, 1)

	sender := &fakeManifestSender{sendErr: errors.New("peer gone")}
	router := newRouterWithSender(t, ad, sender)

	// Must not panic / propagate. The peer can race a disconnect
	// between addPeer and the connect callback firing — the emitter
	// is expected to log and move on.
	router.SendLocalManifestTo(peermanagement.PeerID(1))
}
