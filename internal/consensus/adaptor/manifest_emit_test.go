package adaptor

import (
	"bytes"
	"errors"
	"sync"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/manifest"
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/stretchr/testify/require"
)

// fakeManifestSender records every Send / Broadcast invocation so a
// test can assert that the router emitted the expected TMManifests
// frame. Peers() lets BroadcastLocalManifest see a non-zero peer count
// without standing up real connections.
type fakeManifestSender struct {
	mu           sync.Mutex
	sends        []sendCall
	bcasts       [][]byte
	bcastsExcept []broadcastExceptCall
	peers        []peermanagement.PeerInfo
	// sendErr / broadcastErr let individual tests force the error
	// branches in the emitter.
	sendErr      error
	broadcastErr error
}

type broadcastExceptCall struct {
	peerID peermanagement.PeerID
	frame  []byte
}

type sendCall struct {
	peerID peermanagement.PeerID
	frame  []byte
}

type peerLedgerHints struct {
	closed [32]byte
}

func (p peerLedgerHints) IsPeerConnected(peermanagement.PeerID) bool {
	return true
}

func (p peerLedgerHints) PeerClosedLedger(peermanagement.PeerID) ([32]byte, bool) {
	return p.closed, true
}

type peerBootstrapSessions struct {
	acknowledged peermanagement.PeerID
	rejected     peermanagement.PeerID
}

func (*peerBootstrapSessions) IsPeerConnected(peermanagement.PeerID) bool {
	return true
}

func (p *peerBootstrapSessions) AcknowledgePeerBootstrap(peerID peermanagement.PeerID) {
	p.acknowledged = peerID
}

func (p *peerBootstrapSessions) RejectPeerBootstrap(peerID peermanagement.PeerID) {
	p.rejected = peerID
}

func (f *fakeManifestSender) Send(peerID peermanagement.PeerID, frame []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sends = append(f.sends, sendCall{peerID: peerID, frame: append([]byte(nil), frame...)})
	return f.sendErr
}

func (f *fakeManifestSender) SendManifestFrames(peerID peermanagement.PeerID, frames [][]byte) error {
	for _, frame := range frames {
		if err := f.Send(peerID, frame); err != nil {
			return err
		}
	}
	return nil
}

func (f *fakeManifestSender) Broadcast(frame []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bcasts = append(f.bcasts, append([]byte(nil), frame...))
	return f.broadcastErr
}

func (f *fakeManifestSender) BroadcastManifestFrames(frames [][]byte) error {
	for _, frame := range frames {
		if err := f.Broadcast(frame); err != nil {
			return err
		}
	}
	return nil
}

func (f *fakeManifestSender) BroadcastExcept(peerID peermanagement.PeerID, frame []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bcastsExcept = append(f.bcastsExcept, broadcastExceptCall{
		peerID: peerID,
		frame:  append([]byte(nil), frame...),
	})
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

// routerWithCache builds a router with a fresh manifest cache attached
// and a fake sender installed. The optional `seed` and `seq` mint a
// token-mode identity whose manifest is applied to the cache so the
// emission paths have something to gossip — mirroring the production
// startup path that seeds the local manifest into the shared cache.
//
// Pass empty seed/seq=0 to skip seeding (observer mode: empty cache).
func routerWithCache(t *testing.T, sender manifestSender, seedKey byte, seq uint32) (*Router, *manifest.Cache, *ValidatorIdentity) {
	t.Helper()
	ad := newTestAdaptor(t)
	cache := manifest.NewCache()

	var id *ValidatorIdentity
	if seq != 0 {
		fix := newTokenFixture(t, seedKey, seq)
		var err error
		id, err = NewValidatorIdentityFromToken(fix.tokenBlock)
		if err != nil {
			t.Fatalf("NewValidatorIdentityFromToken: %v", err)
		}
		ad.identity = id
		if d := cache.ApplyManifest(id.Manifest); d != manifest.Accepted {
			t.Fatalf("seed local manifest into cache: %s", d)
		}
	}

	router := NewRouter(&mockEngine{}, ad, nil)
	router.manifests = cache
	router.overrideManifestSender = sender
	return router, cache, id
}

func TestEncodeManifestFramesBoundsFramesAndPreservesOrder(t *testing.T) {
	wires := make([][]byte, 96)
	for i := range wires {
		wires[i] = bytes.Repeat([]byte{byte(i)}, 16<<10)
	}

	frames, err := encodeManifestFrames(wires...)
	if err != nil {
		t.Fatalf("encodeManifestFrames: %v", err)
	}
	if len(frames) < 2 {
		t.Fatalf("frames: got %d want at least 2", len(frames))
	}

	got := make([][]byte, 0, len(wires))
	for i, frame := range frames {
		if len(frame) > manifestFrameTargetSize {
			t.Errorf("frame %d size: got %d want <= %d", i, len(frame), manifestFrameTargetSize)
		}
		got = append(got, frameToManifestBytes(t, frame)...)
	}
	if len(got) != len(wires) {
		t.Fatalf("decoded manifests: got %d want %d", len(got), len(wires))
	}
	for i := range wires {
		if !bytes.Equal(got[i], wires[i]) {
			t.Fatalf("manifest %d changed or reordered", i)
		}
	}

	if _, err := encodeManifestFrames(make([]byte, manifestFrameTargetSize)); err == nil {
		t.Fatal("oversized individual manifest was accepted")
	}
}

func TestEncodeManifestFramesCapsEntryCount(t *testing.T) {
	wires := make([][]byte, 205)
	for i := range wires {
		wires[i] = []byte{byte(i)}
	}
	frames, err := encodeManifestFrames(wires...)
	require.NoError(t, err)
	require.Len(t, frames, 3)

	got := make([][]byte, 0, len(wires))
	for _, frame := range frames {
		entries := frameToManifestBytes(t, frame)
		require.LessOrEqual(t, len(entries), manifestFrameMaxEntries)
		got = append(got, entries...)
	}
	require.Equal(t, wires, got)
}

func TestRouter_SendLocalManifestTo_EmitsExpectedFrame(t *testing.T) {
	sender := &fakeManifestSender{}
	router, _, id := routerWithCache(t, sender, 0x42, 5)

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
	sender := &fakeManifestSender{
		// Stub three peers — the count shapes the return value and is
		// what BroadcastLocalManifest checks before calling Broadcast.
		peers: []peermanagement.PeerInfo{{}, {}, {}},
	}
	router, _, id := routerWithCache(t, sender, 0x55, 3)

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
	sender := &fakeManifestSender{} // empty Peers()
	router, _, _ := routerWithCache(t, sender, 0x66, 2)

	if n := router.BroadcastLocalManifest(); n != 0 {
		t.Errorf("expected 0 with no peers, got %d", n)
	}
	if len(sender.bcasts) != 0 {
		t.Errorf("expected no Broadcast call when peer list is empty, got %d", len(sender.bcasts))
	}
}

// Empty-cache mode covers both observer (no validator at all) and
// seed-only (validator without a token-mode manifest). In both cases
// nothing has been applied to the cache and emission must skip.
func TestRouter_LocalManifestEmission_EmptyCacheSkips(t *testing.T) {
	sender := &fakeManifestSender{
		peers: []peermanagement.PeerInfo{{}},
	}
	// seq=0 → routerWithCache skips seeding.
	router, _, _ := routerWithCache(t, sender, 0, 0)

	router.SendLocalManifestTo(peermanagement.PeerID(1))
	if n := router.BroadcastLocalManifest(); n != 0 {
		t.Errorf("empty-cache broadcast should return 0, got %d", n)
	}
	if len(sender.sends) != 0 || len(sender.bcasts) != 0 {
		t.Errorf("empty cache must not emit: sends=%d bcasts=%d", len(sender.sends), len(sender.bcasts))
	}
}

func TestRouter_LocalManifestEmission_NilCacheSkips(t *testing.T) {
	sender := &fakeManifestSender{
		peers: []peermanagement.PeerInfo{{}},
	}
	router, _, _ := routerWithCache(t, sender, 0, 0)
	// Drop the cache entirely — exercises the r.manifests == nil
	// guard in cachedManifestFrame.
	router.manifests = nil

	router.SendLocalManifestTo(peermanagement.PeerID(1))
	if n := router.BroadcastLocalManifest(); n != 0 {
		t.Errorf("nil-cache broadcast should return 0, got %d", n)
	}
	if len(sender.sends) != 0 || len(sender.bcasts) != 0 {
		t.Errorf("nil cache must not emit: sends=%d bcasts=%d", len(sender.sends), len(sender.bcasts))
	}
}

// Two cached manifests (local + a peer-gossiped one) must both end up
// in the emitted frame — this is the rippled getManifestsMessage
// parity property.
func TestRouter_LocalManifestEmission_AggregatesCache(t *testing.T) {
	sender := &fakeManifestSender{}
	router, cache, id := routerWithCache(t, sender, 0x91, 4)

	// Mint a second token-mode identity and apply its manifest to the
	// cache as if it had been gossiped by a trusted peer.
	otherFix := newTokenFixture(t, 0xA3, 11)
	other, err := NewValidatorIdentityFromToken(otherFix.tokenBlock)
	if err != nil {
		t.Fatalf("NewValidatorIdentityFromToken (other): %v", err)
	}
	if d := cache.ApplyManifest(other.Manifest); d != manifest.Accepted {
		t.Fatalf("apply remote manifest: %s", d)
	}

	router.SendLocalManifestTo(peermanagement.PeerID(7))
	if len(sender.sends) != 1 {
		t.Fatalf("expected 1 Send, got %d", len(sender.sends))
	}

	got := frameToManifestBytes(t, sender.sends[0].frame)
	if len(got) != 2 {
		t.Fatalf("expected 2 manifests in aggregated frame, got %d", len(got))
	}
	// Cache iteration order is map-random; check both expected payloads
	// appear regardless of order.
	want := map[string]bool{
		string(id.SerializedMfst):    false,
		string(other.SerializedMfst): false,
	}
	for _, w := range got {
		if _, ok := want[string(w)]; ok {
			want[string(w)] = true
		}
	}
	for k, seen := range want {
		if !seen {
			t.Errorf("aggregated frame missing manifest %x...", []byte(k)[:8])
		}
	}
}

func TestRouter_HandlePeerConnect_DelegatesToSendLocalManifest(t *testing.T) {
	sender := &fakeManifestSender{}
	router, cache, id := routerWithCache(t, sender, 0x77, 9)

	otherFix := newTokenFixture(t, 0x76, 3)
	other, err := NewValidatorIdentityFromToken(otherFix.tokenBlock)
	if err != nil {
		t.Fatalf("NewValidatorIdentityFromToken: %v", err)
	}
	if d := cache.ApplyManifest(other.Manifest); d != manifest.Accepted {
		t.Fatalf("apply learned manifest: %s", d)
	}

	router.handlePeerConnect(peermanagement.PeerID(42))

	if len(sender.sends) != 1 {
		t.Fatalf("expected 1 Send from HandlePeerConnect, got %d", len(sender.sends))
	}
	if sender.sends[0].peerID != 42 {
		t.Errorf("HandlePeerConnect routed to wrong peer: got %v want 42", sender.sends[0].peerID)
	}
	got := frameToManifestBytes(t, sender.sends[0].frame)
	if len(got) != 2 {
		t.Fatalf("warm connect emitted %d manifests, want local + learned", len(got))
	}
	want := map[string]bool{
		string(id.SerializedMfst):    false,
		string(other.SerializedMfst): false,
	}
	for _, wire := range got {
		if _, ok := want[string(wire)]; ok {
			want[string(wire)] = true
		}
	}
	for wire, seen := range want {
		if !seen {
			t.Fatalf("warm connect omitted cached manifest %x...", []byte(wire)[:8])
		}
	}
}

func TestRouterHandlePeerConnectSeedsHandshakeLedgerHint(t *testing.T) {
	ad := newTestAdaptor(t)
	router := NewRouter(&mockEngine{}, ad, nil)
	closed := [32]byte{0xAA, 0xBB}
	router.setPeerSessionView(peerLedgerHints{closed: closed})

	router.handlePeerConnect(peermanagement.PeerID(42))

	ledgers := ad.PeerReportedLedgers()
	if len(ledgers) != 1 {
		t.Fatalf("peer ledger hints: got %d entries, want 1", len(ledgers))
	}
	if got := [32]byte(ledgers[0]); got != closed {
		t.Fatalf("peer ledger hint: got %x, want %x", got, closed)
	}
}

func TestRouterQueuedPeerConnectCannotResurrectDisconnectedPeer(t *testing.T) {
	sender := &fakeManifestSender{}
	router, _, _ := routerWithCache(t, sender, 0x75, 2)
	ledger, _ := newWideWorkLedger(t)
	router.fetchTracker.Track(ledger)
	sessions := &testPeerSessions{connected: map[peermanagement.PeerID]bool{22: true}}
	router.setPeerSessionView(sessions)

	router.HandlePeerConnect(22)
	if _, pending := router.pendingPeerConnects.Load(peermanagement.PeerID(22)); !pending {
		t.Fatal("peer connect was not queued")
	}
	sessions.set(22, false)
	router.queuePeerDisconnect(22)
	if _, pending := router.pendingPeerConnects.Load(peermanagement.PeerID(22)); pending {
		t.Fatal("disconnect did not discard queued peer connect")
	}

	// Model a connect drain that already obtained the key while the disconnect
	// callback was running. The live-session check is the final race barrier.
	router.pendingPeerConnects.Store(peermanagement.PeerID(22), struct{}{})
	router.drainPeerConnects()
	if len(sender.sends) != 0 {
		t.Fatalf("disconnected peer received %d manifest frames", len(sender.sends))
	}
	for _, peerID := range ledger.Peers() {
		if peerID == 22 {
			t.Fatal("disconnected peer was re-added to active acquisition")
		}
	}
}

func TestRouterAcknowledgesBootstrapOnlyAfterValidManifest(t *testing.T) {
	router, _, _ := routerWithCache(t, nil, 0, 0)
	sessions := &peerBootstrapSessions{}
	router.setPeerSessionView(sessions)

	router.processManifestJob(&peermanagement.InboundMessage{
		PeerID:  7,
		Type:    uint16(message.TypeManifests),
		Payload: []byte("malformed"),
	})
	if sessions.acknowledged != 0 {
		t.Fatalf("malformed manifests acknowledged peer %d", sessions.acknowledged)
	}
	if sessions.rejected != 7 {
		t.Fatalf("malformed manifests rejected peer %d, want 7", sessions.rejected)
	}

	validWire := buildWireManifest(t, 1, 0xA1, 0xA2)
	invalidWire := append([]byte(nil), validWire...)
	invalidWire[len(invalidWire)-1] ^= 1
	if _, err := manifest.Deserialize(invalidWire); err != nil {
		t.Fatalf("signature-invalid manifest must remain structurally valid: %v", err)
	}

	tests := []struct {
		name string
		list []message.Manifest
	}{
		{name: "parse invalid", list: []message.Manifest{{STObject: []byte{1}}}},
		{name: "cache invalid", list: []message.Manifest{{STObject: invalidWire}}},
	}

	payload, err := message.Encode(&message.Manifests{})
	if err != nil {
		t.Fatalf("encode empty manifests: %v", err)
	}
	sessions.acknowledged = 0
	sessions.rejected = 0
	router.processManifestJob(&peermanagement.InboundMessage{PeerID: 7, Type: uint16(message.TypeManifests), Payload: payload})
	if sessions.acknowledged != 7 || sessions.rejected != 0 {
		t.Fatalf("empty manifests ack=%d reject=%d, want ack=7 reject=0", sessions.acknowledged, sessions.rejected)
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sessions.acknowledged = 0
			sessions.rejected = 0
			payload, err := message.Encode(&message.Manifests{List: tt.list})
			if err != nil {
				t.Fatalf("encode manifests: %v", err)
			}
			router.processManifestJob(&peermanagement.InboundMessage{
				PeerID:  7,
				Type:    uint16(message.TypeManifests),
				Payload: payload,
			})
			if sessions.acknowledged != 0 {
				t.Fatalf("invalid manifests acknowledged peer %d", sessions.acknowledged)
			}
			if sessions.rejected != 7 {
				t.Fatalf("invalid manifests rejected peer %d, want 7", sessions.rejected)
			}
		})
	}

	for _, name := range []string{"accepted", "stale"} {
		t.Run(name, func(t *testing.T) {
			sessions.acknowledged = 0
			sessions.rejected = 0
			payload, err := message.Encode(&message.Manifests{
				List: []message.Manifest{{STObject: validWire}},
			})
			if err != nil {
				t.Fatalf("encode manifests: %v", err)
			}
			router.processManifestJob(&peermanagement.InboundMessage{
				PeerID:  7,
				Type:    uint16(message.TypeManifests),
				Payload: payload,
			})
			if sessions.acknowledged != 7 {
				t.Fatalf("valid manifests acknowledged peer %d, want 7", sessions.acknowledged)
			}
			if sessions.rejected != 0 {
				t.Fatalf("valid manifests rejected peer %d", sessions.rejected)
			}
		})
	}
}

func TestRouter_SendLocalManifestTo_SwallowsSenderError(t *testing.T) {
	sender := &fakeManifestSender{sendErr: errors.New("peer gone")}
	router, _, _ := routerWithCache(t, sender, 0x88, 1)

	// Must not panic / propagate. The peer can race a disconnect
	// between addPeer and the connect callback firing — the emitter
	// is expected to log and move on.
	router.SendLocalManifestTo(peermanagement.PeerID(1))
}

// Two back-to-back emissions with no cache mutation between them must
// produce the SAME frame bytes — this is the rippled
// (manifestMessage_, manifestListSeq_) reuse property at
// OverlayImpl.cpp:1184-1212. Identity here means "same backing array",
// which proves the second emission did not re-encode.
func TestRouter_CachedManifestFrame_ReusedAcrossEmissions(t *testing.T) {
	sender := &fakeManifestSender{}
	router, _, _ := routerWithCache(t, sender, 0xB1, 6)

	router.SendLocalManifestTo(peermanagement.PeerID(1))
	router.SendLocalManifestTo(peermanagement.PeerID(2))
	if len(sender.sends) != 2 {
		t.Fatalf("expected 2 Sends, got %d", len(sender.sends))
	}

	if len(router.manifestFrames) != 1 {
		t.Fatalf("cached frames: got %d want 1", len(router.manifestFrames))
	}
	first := router.manifestFrames[0]
	if first == nil {
		t.Fatalf("frame cache empty after first Send")
	}
	if seq := router.manifestFrameSeq; seq != 1 {
		// The setup's first-insert bumps cache.Sequence to 1 (rippled
		// 3.2.0 #6059 bumps on first-insert), so the cursor tracks it.
		t.Fatalf("cached cursor: got %d want 1", seq)
	}

	router.SendLocalManifestTo(peermanagement.PeerID(3))
	if got := router.manifestFrames[0]; &got[0] != &first[0] {
		t.Errorf("frame re-encoded despite unchanged cache (backing arrays differ)")
	}
}

// A subsequent ApplyManifest that REPLACES an existing master must bump
// cache.Sequence and force the next emission to re-encode.
func TestRouter_CachedManifestFrame_RebuiltOnSequenceAdvance(t *testing.T) {
	sender := &fakeManifestSender{}
	router, cache, _ := routerWithCache(t, sender, 0xC2, 1)

	router.SendLocalManifestTo(peermanagement.PeerID(1))
	first := router.manifestFrames[0]

	// Mint a higher-sequence manifest under the SAME master+ephemeral
	// keypair (newTokenFixture is seed-deterministic — same seed byte
	// = same keys; only the sequence differs). This hits the update
	// branch in cache.ApplyManifest. Every accept bumps Sequence in
	// rippled 3.2.0 (#6059), so after the setup first-insert (=1) this
	// update takes it to 2.
	rotated := newTokenFixture(t, 0xC2, 7)
	rotatedID, err := NewValidatorIdentityFromToken(rotated.tokenBlock)
	if err != nil {
		t.Fatalf("rotated identity: %v", err)
	}
	if d := cache.ApplyManifest(rotatedID.Manifest); d != manifest.Accepted {
		t.Fatalf("apply rotated manifest: %s", d)
	}
	if seq := cache.Sequence(); seq != 2 {
		t.Fatalf("cache.Sequence after update: got %d want 2", seq)
	}

	router.SendLocalManifestTo(peermanagement.PeerID(2))
	if len(router.manifestFrames) == 0 {
		t.Fatalf("frame cache empty after rotation")
	}
	if &router.manifestFrames[0][0] == &first[0] {
		t.Errorf("frame NOT re-encoded after Sequence advance — cache cursor stuck")
	}
	if router.manifestFrameSeq != 2 {
		t.Errorf("cached cursor: got %d want 2", router.manifestFrameSeq)
	}
}

// Empty cache hits the "cache the empty fact" branch — second call
// must NOT re-walk SerializedAll.
func TestRouter_CachedManifestFrame_EmptyCacheCachesNegative(t *testing.T) {
	sender := &fakeManifestSender{
		peers: []peermanagement.PeerInfo{{}},
	}
	router, _, _ := routerWithCache(t, sender, 0, 0)

	router.SendLocalManifestTo(peermanagement.PeerID(1))
	if !router.manifestFrameBuilt {
		t.Errorf("empty-cache path did not record the negative result")
	}
	if router.manifestFrames != nil {
		t.Errorf("empty cache should cache nil frames, got %d", len(router.manifestFrames))
	}
}
