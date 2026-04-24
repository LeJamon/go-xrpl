package adaptor

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"testing"
	"time"

	"github.com/LeJamon/goXRPLd/codec/binarycodec"
	"github.com/LeJamon/goXRPLd/internal/manifest"
	"github.com/LeJamon/goXRPLd/internal/peermanagement"
	"github.com/LeJamon/goXRPLd/internal/peermanagement/message"
	"github.com/LeJamon/goXRPLd/protocol"
	"github.com/stretchr/testify/require"
)

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
	prefix := protocol.HashPrefixManifest
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

	router := NewRouter(engine, adaptor, nil, inbox)
	cache := manifest.NewCache()
	// Pass nil overlay — the relay step is a no-op; we're only
	// verifying apply.
	router.SetManifestCache(cache, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
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

// TestRouter_HandleManifests_InvalidDoesNotStore drives a
// parse-valid-but-signature-invalid manifest through the router. The
// cache must reject it; no state change is the whole guarantee.
// (The bad-data attribution surface is exercised in
// router_bad_data_test.go — here we only verify the cache side.)
func TestRouter_HandleManifests_InvalidDoesNotStore(t *testing.T) {
	engine := &mockEngine{}
	adaptor := newTestAdaptor(t)
	inbox := make(chan *peermanagement.InboundMessage, 4)

	router := NewRouter(engine, adaptor, nil, inbox)
	cache := manifest.NewCache()
	router.SetManifestCache(cache, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
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
