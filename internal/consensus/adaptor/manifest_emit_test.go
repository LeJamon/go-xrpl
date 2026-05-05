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

	router := NewRouter(&mockEngine{}, ad, nil, nil)
	router.manifests = cache
	router.overrideManifestSender = sender
	return router, cache, id
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
	router, _, _ := routerWithCache(t, sender, 0x77, 9)

	router.HandlePeerConnect(peermanagement.PeerID(42))

	if len(sender.sends) != 1 {
		t.Fatalf("expected 1 Send from HandlePeerConnect, got %d", len(sender.sends))
	}
	if sender.sends[0].peerID != 42 {
		t.Errorf("HandlePeerConnect routed to wrong peer: got %v want 42", sender.sends[0].peerID)
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

	first := router.manifestFrame
	if first == nil {
		t.Fatalf("frame cache empty after first Send")
	}
	if seq := router.manifestFrameSeq; seq != 0 {
		// First-insert path doesn't bump cache.Sequence (rippled
		// Manifest.cpp:507-518 parity), so the cursor stays at 0.
		t.Fatalf("cached cursor: got %d want 0 (first-insert quirk)", seq)
	}

	router.SendLocalManifestTo(peermanagement.PeerID(3))
	if got := router.manifestFrame; &got[0] != &first[0] {
		t.Errorf("frame re-encoded despite unchanged cache (backing arrays differ)")
	}
}

// A subsequent ApplyManifest that REPLACES an existing master must bump
// cache.Sequence and force the next emission to re-encode.
func TestRouter_CachedManifestFrame_RebuiltOnSequenceAdvance(t *testing.T) {
	sender := &fakeManifestSender{}
	router, cache, _ := routerWithCache(t, sender, 0xC2, 1)

	router.SendLocalManifestTo(peermanagement.PeerID(1))
	first := router.manifestFrame

	// Mint a higher-sequence manifest under the SAME master+ephemeral
	// keypair (newTokenFixture is seed-deterministic — same seed byte
	// = same keys; only the sequence differs). This hits the update
	// branch in cache.ApplyManifest, which is the only path that bumps
	// Sequence — matching rippled Manifest.cpp:538.
	rotated := newTokenFixture(t, 0xC2, 7)
	rotatedID, err := NewValidatorIdentityFromToken(rotated.tokenBlock)
	if err != nil {
		t.Fatalf("rotated identity: %v", err)
	}
	if d := cache.ApplyManifest(rotatedID.Manifest); d != manifest.Accepted {
		t.Fatalf("apply rotated manifest: %s", d)
	}
	if seq := cache.Sequence(); seq != 1 {
		t.Fatalf("cache.Sequence after update: got %d want 1", seq)
	}

	router.SendLocalManifestTo(peermanagement.PeerID(2))
	if router.manifestFrame == nil {
		t.Fatalf("frame cache empty after rotation")
	}
	if &router.manifestFrame[0] == &first[0] {
		t.Errorf("frame NOT re-encoded after Sequence advance — cache cursor stuck")
	}
	if router.manifestFrameSeq != 1 {
		t.Errorf("cached cursor: got %d want 1", router.manifestFrameSeq)
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
	if router.manifestFrame != nil {
		t.Errorf("empty cache should cache nil frame, got %d bytes", len(router.manifestFrame))
	}
}
