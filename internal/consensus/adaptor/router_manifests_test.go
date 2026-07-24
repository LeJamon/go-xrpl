package adaptor

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"sync"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/manifest"
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protowire"
)

type runningManifestOverlay struct {
	overlay  *peermanagement.Overlay
	done     chan error
	stopOnce sync.Once
}

func startRunningManifestOverlay(t *testing.T, opts ...peermanagement.Option) *runningManifestOverlay {
	t.Helper()
	allOpts := append([]peermanagement.Option{peermanagement.WithListenAddr("127.0.0.1:0")}, opts...)
	overlay, err := peermanagement.New(allOpts...)
	require.NoError(t, err)
	running := &runningManifestOverlay{overlay: overlay, done: make(chan error, 1)}
	go func() { running.done <- overlay.Run(context.Background()) }()
	select {
	case <-overlay.ListenerReady():
	case err := <-running.done:
		t.Fatalf("overlay stopped before listener became ready: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("overlay listener did not become ready")
	}
	t.Cleanup(func() { running.stop(t) })
	return running
}

func (r *runningManifestOverlay) stop(t *testing.T) {
	t.Helper()
	r.stopOnce.Do(func() {
		require.NoError(t, r.overlay.Stop())
		select {
		case <-r.done:
		case <-time.After(5 * time.Second):
			t.Fatal("overlay did not stop")
		}
	})
}

type manifestOverlayConnection struct {
	overlay *runningManifestOverlay
	peerID  peermanagement.PeerID
}

// buildWireManifest produces a valid serialized manifest that the
// router's handleManifests can apply end-to-end.
func buildWireManifest(t *testing.T, seq uint32, masterSeed, ephSeed byte) []byte {
	t.Helper()

	masterSeedBytes := bytes.Repeat([]byte{masterSeed}, ed25519.SeedSize)
	masterPriv := ed25519.NewKeyFromSeed(masterSeedBytes)
	masterPub := append([]byte{0xED}, masterPriv.Public().(ed25519.PublicKey)...)

	ephSeedBytes := bytes.Repeat([]byte{ephSeed}, ed25519.SeedSize)
	ephPriv := ed25519.NewKeyFromSeed(ephSeedBytes)
	ephPub := append([]byte{0xED}, ephPriv.Public().(ed25519.PublicKey)...)

	j := map[string]any{
		"PublicKey":     hex.EncodeToString(masterPub),
		"SigningPubKey": hex.EncodeToString(ephPub),
		"Sequence":      seq,
	}

	preimageHex, err := binarycodec.Encode(j)
	require.NoError(t, err)
	body, _ := hex.DecodeString(preimageHex)
	prefix := protocol.HashPrefixManifest()
	preimage := append(prefix[:], body...)

	j["Signature"] = hex.EncodeToString(ed25519.Sign(ephPriv, preimage))
	j["MasterSignature"] = hex.EncodeToString(ed25519.Sign(masterPriv, preimage))

	encoded, err := binarycodec.Encode(j)
	require.NoError(t, err)
	raw, err := hex.DecodeString(encoded)
	require.NoError(t, err)
	return raw
}

// TestRouter_HandleManifests_AppliesAccepted drives an inbound
// TMManifests frame through the router's Run loop and asserts that
// after processing the cache contains the master→ephemeral binding
// and the raw wire bytes round-trip.
func TestRouter_HandleManifests_AppliesAccepted(t *testing.T) {
	engine := &mockEngine{}
	adaptor := newTestAdaptor(t)
	inbox := make(chan *peermanagement.InboundMessage, 4)

	router := NewRouter(engine, adaptor, inbox)
	cache := manifest.NewCache()
	// Pass nil overlay — the relay step is a no-op; we're only
	// verifying apply.
	router.SetManifestCache(cache, nil)

	ctx := t.Context()
	go router.Run(ctx)

	serialized := buildWireManifest(t, 3, 0x20, 0x21)
	frame := &message.Manifests{
		List: []message.Manifest{{STObject: serialized}},
	}
	inbox <- &peermanagement.InboundMessage{
		PeerID:  7,
		Type:    uint16(message.TypeManifests),
		Payload: encodePayload(t, frame),
	}

	parsed, err := manifest.Deserialize(serialized)
	require.NoError(t, err)

	// Poll until applied or timeout — the router runs async so we
	// can't assume immediate visibility.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if _, ok := cache.GetSigningKey(parsed.MasterKey); ok {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, ok := cache.GetSigningKey(parsed.MasterKey); !ok {
		t.Fatal("router did not apply manifest to cache")
	}
	stored, ok := cache.GetManifest(parsed.MasterKey)
	if !ok || !bytes.Equal(stored, serialized) {
		t.Fatalf("stored manifest bytes mismatch: ok=%v", ok)
	}
	if got, _ := cache.GetSequence(parsed.MasterKey); got != 3 {
		t.Fatalf("stored sequence: got %d want 3", got)
	}
}

func TestRouter_ProcessManifestSpoolAppliesAndReleasesPeer(t *testing.T) {
	connections := make(chan manifestOverlayConnection, 1)
	source := startRunningManifestOverlay(t, peermanagement.WithCompression(false))
	source.overlay.SetPeerConnectCallback(func(peerID peermanagement.PeerID) {
		connections <- manifestOverlayConnection{overlay: source, peerID: peerID}
	})
	client := startRunningManifestOverlay(t,
		peermanagement.WithDataDir(t.TempDir()),
		peermanagement.WithCompression(false),
		peermanagement.WithMaxOutbound(1),
		peermanagement.WithFixedPeers(source.overlay.ListenAddr()),
	)

	var connection manifestOverlayConnection
	select {
	case connection = <-connections:
	case <-time.After(5 * time.Second):
		t.Fatal("manifest source did not connect")
	}

	wire := buildWireManifest(t, 1, 0x24, 0x25)
	payload := encodePayload(t, &message.Manifests{
		List: []message.Manifest{{STObject: wire}},
	})
	payload = protowire.AppendTag(payload, 9, protowire.BytesType)
	payload = protowire.AppendBytes(payload, make([]byte, 1<<20))
	frame, err := message.BuildWireMessage(message.TypeManifests, payload)
	require.NoError(t, err)
	require.NoError(t, connection.overlay.overlay.Send(connection.peerID, frame))

	var inbound *peermanagement.InboundMessage
	select {
	case inbound = <-client.overlay.ManifestMessages():
	case <-time.After(5 * time.Second):
		t.Fatal("spooled manifest did not reach the processing lane")
	}
	require.NotNil(t, inbound.ManifestFrame)
	require.Nil(t, inbound.Payload)

	router, cache, _ := routerWithCache(t, nil, 0, 0)
	router.processManifestJob(inbound)

	parsed, err := manifest.Deserialize(wire)
	require.NoError(t, err)
	stored, ok := cache.GetManifest(parsed.MasterKey)
	require.True(t, ok)
	require.Equal(t, wire, stored)
	_, err = inbound.ManifestFrame.Materialize(context.Background())
	require.ErrorIs(t, err, peermanagement.ErrManifestFrameClosed)
}

// TestRouter_HandleManifests_InvalidDoesNotStore drives a
// parse-valid-but-signature-invalid manifest through the router. The
// cache must reject it; no state change is the whole guarantee.
// (The bad-data attribution surface is exercised in
// router_bad_data_test.go — here we only verify the cache side.)
func TestRouter_HandleManifests_InvalidDoesNotStore(t *testing.T) {
	engine := &mockEngine{}
	adaptor := newTestAdaptor(t)
	inbox := make(chan *peermanagement.InboundMessage, 4)

	router := NewRouter(engine, adaptor, inbox)
	cache := manifest.NewCache()
	router.SetManifestCache(cache, nil)

	ctx := t.Context()
	go router.Run(ctx)

	// Start from a valid manifest and corrupt MasterSignature so
	// Deserialize succeeds but Verify fails — Cache.ApplyManifest
	// returns Invalid and the cache stays empty.
	serialized := buildWireManifest(t, 5, 0x30, 0x31)
	decoded, err := binarycodec.Decode(hex.EncodeToString(serialized))
	require.NoError(t, err)
	bogus := hex.EncodeToString(bytes.Repeat([]byte{0xAA}, ed25519.SignatureSize))
	decoded["MasterSignature"] = bogus
	corruptedHex, err := binarycodec.Encode(decoded)
	require.NoError(t, err)
	corrupted, err := hex.DecodeString(corruptedHex)
	require.NoError(t, err)

	parsed, err := manifest.Deserialize(corrupted)
	require.NoError(t, err)

	frame := &message.Manifests{List: []message.Manifest{{STObject: corrupted}}}
	inbox <- &peermanagement.InboundMessage{
		PeerID:  123,
		Type:    uint16(message.TypeManifests),
		Payload: encodePayload(t, frame),
	}

	// Give the router a moment to process the frame.
	time.Sleep(50 * time.Millisecond)

	if _, ok := cache.GetSigningKey(parsed.MasterKey); ok {
		t.Fatal("cache stored a manifest whose master signature was corrupted")
	}
}

func TestRouter_HandleManifests_AdmissionBoundsDurableCache(t *testing.T) {
	sender := &fakeManifestSender{}
	router, cache, _ := routerWithCache(t, sender, 0, 0)
	wire := buildWireManifest(t, 1, 0x41, 0x42)
	parsed, err := manifest.Deserialize(wire)
	require.NoError(t, err)
	msg := &peermanagement.InboundMessage{
		PeerID: 7,
		Type:   uint16(message.TypeManifests),
		Payload: encodePayload(t, &message.Manifests{List: []message.Manifest{{
			STObject: wire,
		}}}),
	}

	router.SetManifestAdmission(func([33]byte) bool { return false })
	require.True(t, router.handleManifests(msg))
	_, stored := cache.GetManifest(parsed.MasterKey)
	require.False(t, stored)
	require.Empty(t, sender.bcastsExcept)

	router.SetManifestAdmission(func(master [33]byte) bool { return master == parsed.MasterKey })
	require.True(t, router.handleManifests(msg))
	storedWire, stored := cache.GetManifest(parsed.MasterKey)
	require.True(t, stored)
	require.Equal(t, wire, storedWire)
	require.Len(t, sender.bcastsExcept, 1)
}

func TestRouter_HandleManifests_ChargesInvalidEntriesOncePerFrame(t *testing.T) {
	router, sender := makeRouterWithBadDataRecorder(t)
	router.SetManifestCache(manifest.NewCache(), nil)
	list := []message.Manifest{
		{STObject: []byte{0x01}},
		{STObject: []byte{0x02}},
		{STObject: []byte{0x03}},
	}

	router.handleManifests(&peermanagement.InboundMessage{
		PeerID:  9,
		Type:    uint16(message.TypeManifests),
		Payload: encodePayload(t, &message.Manifests{List: list}),
	})

	calls := sender.getBadDataCalls()
	require.Len(t, calls, 1)
	require.Equal(t, badDataCall{peerID: 9, reason: "manifest-invalid"}, calls[0])
}

func TestRouter_ManifestAcceptedAfterPeerRemovalCompletesBootstrap(t *testing.T) {
	connections := make(chan manifestOverlayConnection, 2)
	first := startRunningManifestOverlay(t)
	second := startRunningManifestOverlay(t, peermanagement.WithListenAddr("[::1]:0"))
	first.overlay.SetPeerConnectCallback(func(peerID peermanagement.PeerID) {
		connections <- manifestOverlayConnection{overlay: first, peerID: peerID}
	})
	second.overlay.SetPeerConnectCallback(func(peerID peermanagement.PeerID) {
		connections <- manifestOverlayConnection{overlay: second, peerID: peerID}
	})

	client := startRunningManifestOverlay(t,
		peermanagement.WithMaxOutbound(1),
		peermanagement.WithFixedPeers(first.overlay.ListenAddr(), second.overlay.ListenAddr()),
	)

	var source manifestOverlayConnection
	select {
	case source = <-connections:
	case <-time.After(5 * time.Second):
		t.Fatal("bootstrap source did not connect")
	}

	wire := buildWireManifest(t, 1, 0xB1, 0xB2)
	frame, err := message.EncodeFrame(&message.Manifests{
		List: []message.Manifest{{STObject: wire}},
	})
	require.NoError(t, err)
	require.NoError(t, source.overlay.overlay.Send(source.peerID, frame))

	var inbound *peermanagement.InboundMessage
	select {
	case inbound = <-client.overlay.ManifestMessages():
	case <-time.After(5 * time.Second):
		t.Fatal("manifest frame did not reach client overlay")
	}
	require.Equal(t, uint16(message.TypeManifests), inbound.Type)

	source.overlay.stop(t)
	require.Eventually(t, func() bool {
		return !client.overlay.IsPeerConnected(inbound.PeerID)
	}, 5*time.Second, 10*time.Millisecond)
	select {
	case <-connections:
		t.Fatal("replacement connected before manifest handler decision")
	case <-time.After(100 * time.Millisecond):
	}

	router, cache, _ := routerWithCache(t, nil, 0, 0)
	router.setPeerSessionView(client.overlay)
	router.handleMessage(inbound)

	parsed, err := manifest.Deserialize(wire)
	require.NoError(t, err)
	stored, ok := cache.GetManifest(parsed.MasterKey)
	require.True(t, ok)
	require.Equal(t, wire, stored)
	select {
	case <-connections:
	case <-time.After(5 * time.Second):
		t.Fatal("bootstrap did not continue after disconnected manifest was accepted")
	}
}

func TestRouter_HandleManifests_RelaysAcceptedEntriesExceptSource(t *testing.T) {
	sender := &fakeManifestSender{broadcastErr: peermanagement.ErrSendBufferFull}
	router, _, _ := routerWithCache(t, sender, 0, 0)
	badData := &badDataRecordingSender{}
	router.adaptor.sender = badData

	const acceptedCount = 100
	wires := make([][]byte, 0, acceptedCount)
	list := make([]message.Manifest, 0, acceptedCount+2)
	for i := range acceptedCount {
		wire := buildWireManifest(t, 1, byte(i), byte(i+acceptedCount))
		wires = append(wires, wire)
		list = append(list, message.Manifest{STObject: wire})
	}
	list = append(list,
		message.Manifest{STObject: wires[0]},
		message.Manifest{STObject: []byte{0x01}},
	)

	router.handleMessage(&peermanagement.InboundMessage{
		PeerID:  7,
		Type:    uint16(message.TypeManifests),
		Payload: encodePayload(t, &message.Manifests{List: list}),
	})

	sender.mu.Lock()
	broadcasts := append([]broadcastExceptCall(nil), sender.bcastsExcept...)
	sender.mu.Unlock()
	require.Len(t, broadcasts, 1, "one inbound collection must produce one relay attempt")
	require.Empty(t, sender.bcasts)
	require.Equal(t, peermanagement.PeerID(7), broadcasts[0].peerID)

	got := frameToManifestBytes(t, broadcasts[0].frame)
	require.Len(t, got, acceptedCount)
	for i := range wires {
		require.Equal(t, wires[i], got[i], "accepted manifests must retain input order and bytes")
	}
	require.Equal(t, []badDataCall{{peerID: 7, reason: "manifests-oversize"}}, badData.getBadDataCalls())
}

func TestRouter_ManifestWorkerDoesNotBlockDispatch(t *testing.T) {
	adaptor := newTestAdaptor(t)
	inbox := make(chan *peermanagement.InboundMessage, 10)
	manifestInbox := make(chan *peermanagement.InboundMessage, 4)
	router := NewRouter(&mockEngine{}, adaptor, inbox)
	router.SetManifestInbox(manifestInbox)
	cache := manifest.NewCache()
	router.SetManifestCache(cache, nil)

	entered := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	var blockOnce sync.Once
	releaseWorker := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseWorker()
	cache.SetOnAccepted(func(*manifest.Manifest) {
		blockOnce.Do(func() {
			close(entered)
			<-release
		})
	})

	go router.Run(t.Context())
	manifestInbox <- &peermanagement.InboundMessage{
		PeerID: 7,
		Type:   uint16(message.TypeManifests),
		Payload: encodePayload(t, &message.Manifests{List: []message.Manifest{{
			STObject: buildWireManifest(t, 1, 220, 221),
		}}}),
	}

	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("manifest worker did not begin processing")
	}

	queued := make([]*manifest.Manifest, 0, cap(manifestInbox))
	for i := range cap(manifestInbox) {
		wire := buildWireManifest(t, 1, byte(222+i), byte(232+i))
		parsed, err := manifest.Deserialize(wire)
		require.NoError(t, err)
		queued = append(queued, parsed)
		manifestInbox <- &peermanagement.InboundMessage{
			PeerID:  peermanagement.PeerID(10 + i),
			Type:    uint16(message.TypeManifests),
			Payload: encodePayload(t, &message.Manifests{List: []message.Manifest{{STObject: wire}}}),
		}
	}
	var ledgerHash [32]byte
	ledgerHash[0] = 0xAB
	inbox <- statusChangeMessage(t, 9, 1, ledgerHash)
	require.Eventually(t, func() bool {
		router.peersMu.RLock()
		state := router.peerStates[9]
		router.peersMu.RUnlock()
		return state != nil && state.LedgerSeq == 1 && state.LedgerHash == ledgerHash
	}, time.Second, 5*time.Millisecond)

	releaseWorker()
	require.Eventually(t, func() bool {
		for _, parsed := range queued {
			if _, ok := cache.GetSigningKey(parsed.MasterKey); !ok {
				return false
			}
		}
		return true
	}, time.Second, 5*time.Millisecond)
}

func TestRouter_ManifestWorkerJoinsOnShutdown(t *testing.T) {
	inbox := make(chan *peermanagement.InboundMessage, 3)
	manifestInbox := make(chan *peermanagement.InboundMessage, 3)
	router := NewRouter(&mockEngine{}, newTestAdaptor(t), inbox)
	router.SetManifestInbox(manifestInbox)
	cache := manifest.NewCache()
	sender := &fakeManifestSender{}
	router.SetManifestCache(cache, nil)
	router.overrideManifestSender = sender

	entered := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	var blockOnce sync.Once
	releaseWorker := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseWorker()
	cache.SetOnAccepted(func(*manifest.Manifest) {
		blockOnce.Do(func() {
			close(entered)
			<-release
		})
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		router.Run(ctx)
		close(done)
	}()
	manifestInbox <- &peermanagement.InboundMessage{
		PeerID: 1,
		Type:   uint16(message.TypeManifests),
		Payload: encodePayload(t, &message.Manifests{List: []message.Manifest{{
			STObject: buildWireManifest(t, 1, 240, 241),
		}}}),
	}

	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("manifest worker did not begin processing")
	}

	queuedWire := buildWireManifest(t, 1, 242, 243)
	queuedManifest, err := manifest.Deserialize(queuedWire)
	require.NoError(t, err)
	manifestInbox <- &peermanagement.InboundMessage{
		PeerID: 2,
		Type:   uint16(message.TypeManifests),
		Payload: encodePayload(t, &message.Manifests{List: []message.Manifest{{
			STObject: queuedWire,
		}}}),
	}
	var ledgerHash [32]byte
	ledgerHash[0] = 0xCD
	inbox <- statusChangeMessage(t, 3, 1, ledgerHash)
	require.Eventually(t, func() bool {
		router.peersMu.RLock()
		state := router.peerStates[3]
		router.peersMu.RUnlock()
		return state != nil && state.LedgerHash == ledgerHash
	}, time.Second, 5*time.Millisecond)

	cancel()
	select {
	case <-done:
		t.Fatal("router returned before its manifest worker stopped")
	case <-time.After(25 * time.Millisecond):
	}
	releaseWorker()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("router did not join its manifest worker")
	}
	_, ok := cache.GetSigningKey(queuedManifest.MasterKey)
	require.True(t, ok, "shutdown must drain queued manifest jobs")
	sender.mu.Lock()
	broadcasts := len(sender.bcastsExcept)
	sender.mu.Unlock()
	require.Equal(t, 2, broadcasts, "shutdown must relay active and queued accepted manifests")
}
