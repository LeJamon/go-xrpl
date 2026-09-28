package adaptor

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/resource"
	validatorlist "github.com/LeJamon/go-xrpl/internal/validator/list"
	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/stretchr/testify/require"
)

func TestRvl_CollectionChargeUsesInboundLifecycle(t *testing.T) {
	journal := &chargeLogHandler{}
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(journal))
	defer slog.SetDefault(previousLogger)

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
		t.Fatal("validator-list source did not connect")
	}
	slog.SetDefault(previousLogger)

	r, sender := makeRouterWithBadDataRecorder(t)
	r.overlay = client.overlay
	publisherSeeds := []byte{0x41, 0x43, 0x45}
	keys := make([]validatorlist.PublisherKey, 0, len(publisherSeeds))
	for _, seed := range publisherSeeds {
		keys = append(keys, rvlPublisherKey(seed))
	}
	agg, err := validatorlist.New(validatorlist.Config{PublisherKeys: keys, Threshold: 1})
	require.NoError(t, err)
	r.SetValidatorListAggregator(agg)

	send := func(coll *message.ValidatorListCollection) {
		t.Helper()
		frame, err := message.EncodeFrame(coll)
		require.NoError(t, err)
		require.NoError(t, connection.overlay.overlay.Send(connection.peerID, frame))
		var inbound *peermanagement.InboundMessage
		select {
		case inbound = <-client.overlay.ConsensusMessages():
		case <-time.After(5 * time.Second):
			t.Fatal("validator-list collection did not reach consensus lane")
		}
		require.Equal(t, message.TypeValidatorListCollection, inbound.Type)
		r.handleInboundMessage(inbound)
	}

	check := func(name string, coll *message.ValidatorListCollection, want resource.Charge, wantContext string) {
		t.Helper()
		before := len(journal.snapshot())
		send(coll)
		var charges []chargeLog
		for _, charge := range journal.snapshot()[before:] {
			if strings.HasPrefix(charge.context, message.TypeValidatorListCollection.String()) {
				charges = append(charges, charge)
			}
		}
		require.Len(t, charges, 1, "%s must retain exactly one message charge", name)
		require.Equal(t, want.String(), charges[0].fee, name)
		require.Contains(t, charges[0].context, wantContext, name)
		require.Empty(t, sender.getBadDataCalls(), "%s must not use bad-data fallback", name)
	}

	check("empty", &message.ValidatorListCollection{Version: 2}, resource.FeeHeavyBurdenPeer(), "vl-coll-no-blobs")
	duplicate := rvlSignedCollection(t, 0x41, 0x42, 1, false)
	check("duplicate first delivery", duplicate, resource.FeeTrivialPeer(), message.TypeValidatorListCollection.String())
	check("duplicate", duplicate, resource.FeeUselessData(), "vl-coll-duplicate")
	check("invalid signature", rvlSignedCollection(t, 0x45, 0x46, 3, true), resource.FeeInvalidSignature(), "vl-coll-badsig-invalid")
	check("accepted", rvlSignedCollection(t, 0x43, 0x44, 2, false), resource.FeeTrivialPeer(), message.TypeValidatorListCollection.String())
}

func rvlPublisherKey(seed byte) validatorlist.PublisherKey {
	private := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{seed}, ed25519.SeedSize))
	encoded := append([]byte{0xED}, private.Public().(ed25519.PublicKey)...)
	var key validatorlist.PublisherKey
	copy(key[:], encoded)
	return key
}

func rvlSignedCollection(t *testing.T, masterSeed, signingSeed byte, sequence uint32, invalidSignature bool) *message.ValidatorListCollection {
	t.Helper()
	manifestRaw := buildWireManifest(t, sequence, masterSeed, signingSeed)
	manifestB64 := []byte(base64.StdEncoding.EncodeToString(manifestRaw))
	validatorKey := rvlPublisherKey(0x70)
	type validator struct {
		ValidationPublicKey string `json:"validation_public_key"`
	}
	type listBody struct {
		Sequence   uint32      `json:"sequence"`
		Expiration uint32      `json:"expiration"`
		Validators []validator `json:"validators"`
	}
	body, err := json.Marshal(listBody{
		Sequence:   sequence,
		Expiration: uint32(time.Now().Add(24*time.Hour).Unix() - protocol.RippleEpochUnix),
		Validators: []validator{{ValidationPublicKey: hex.EncodeToString(validatorKey[:])}},
	})
	require.NoError(t, err)
	signingKey := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{signingSeed}, ed25519.SeedSize))
	signature := ed25519.Sign(signingKey, body)
	if invalidSignature {
		signature = make([]byte, ed25519.SignatureSize)
	}
	return &message.ValidatorListCollection{
		Version:  2,
		Manifest: manifestB64,
		Blobs: []message.ValidatorBlobInfo{{
			Blob:      []byte(base64.StdEncoding.EncodeToString(body)),
			Signature: []byte(hex.EncodeToString(signature)),
		}},
	}
}
