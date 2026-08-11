//go:build cgo && docker

package peermanagement

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/manifest"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/peertls"
	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/stretchr/testify/require"
)

const (
	manifestInteropConnectTimeout = 15 * time.Second
	manifestInteropStepTimeout    = 15 * time.Second
	manifestInteropBatchCount     = 200
)

type manifestInteropPeer struct {
	peer      *Peer
	manifests chan *InboundMessage
	events    chan Event
	cancel    context.CancelFunc
	runErr    chan error
	stopOnce  sync.Once
}

func newManifestInteropPeer(t *testing.T, addr string, payloadLimit uint32) *manifestInteropPeer {
	t.Helper()

	identity, err := NewIdentity()
	require.NoError(t, err)
	certPEM, keyPEM, err := identity.TLSCertificatePEM()
	require.NoError(t, err)
	endpoint, err := ParseEndpoint(addr)
	require.NoError(t, err)

	manifests := make(chan *InboundMessage, 8)
	events := make(chan Event, 16)
	peer := NewPeer(1, endpoint, false, identity, events)
	peer.SetManifestMessages(manifests)
	peer.SetManifestPayloadLimit(payloadLimit)
	peer.handshakeCfg = DefaultHandshakeConfig()
	peer.handshakeCfg.UserAgent = "goXRPL/manifest-interop"
	peer.handshakeCfg.NetworkID = 1

	connectCtx, connectCancel := context.WithTimeout(context.Background(), manifestInteropConnectTimeout)
	require.NoError(t, peer.Connect(connectCtx, PeerConfig{
		PeerTLSConfig: &peertls.Config{CertPEM: certPEM, KeyPEM: keyPEM},
	}))
	connectCancel()

	runCtx, runCancel := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	go func() {
		runErr <- peer.Run(runCtx)
	}()

	result := &manifestInteropPeer{
		peer:      peer,
		manifests: manifests,
		events:    events,
		cancel:    runCancel,
		runErr:    runErr,
	}
	t.Cleanup(result.stop)
	return result
}

func (p *manifestInteropPeer) stop() {
	p.stopOnce.Do(func() {
		p.cancel()
		_ = p.peer.Close()
		select {
		case <-p.runErr:
		case <-time.After(2 * time.Second):
		}
	})
}

type encodedManifestFrame struct {
	frame   []byte
	payload []byte
}

func encodeManifestFrame(t *testing.T, serialized ...[]byte) encodedManifestFrame {
	t.Helper()
	list := make([]message.Manifest, len(serialized))
	for i, stObject := range serialized {
		list[i] = message.Manifest{STObject: stObject}
	}
	manifests := &message.Manifests{List: list}
	payload, err := message.Encode(manifests)
	require.NoError(t, err)
	frame, err := message.EncodeFrame(manifests)
	require.NoError(t, err)
	return encodedManifestFrame{frame: frame, payload: payload}
}

func receiveManifests(t *testing.T, p *manifestInteropPeer) *message.Manifests {
	t.Helper()
	timer := time.NewTimer(manifestInteropStepTimeout)
	defer timer.Stop()

	for {
		select {
		case inbound := <-p.manifests:
			decoded, err := message.Decode(message.TypeManifests, inbound.Payload)
			require.NoError(t, err)
			manifests, ok := decoded.(*message.Manifests)
			require.True(t, ok)
			if len(manifests.List) == 0 {
				continue
			}
			return manifests
		case <-timer.C:
			t.Fatalf("timed out waiting for TMManifests (peer state: %s)", p.peer.State())
			return nil
		}
	}
}

func requirePong(t *testing.T, p *manifestInteropPeer, seq uint32) {
	t.Helper()
	frame, err := message.EncodeFrame(&message.Ping{
		PType: message.PingTypePing,
		Seq:   seq,
	})
	require.NoError(t, err)
	require.NoError(t, p.peer.Send(frame))

	timer := time.NewTimer(manifestInteropStepTimeout)
	defer timer.Stop()
	for {
		select {
		case event := <-p.events:
			if event.Type != EventMessageReceived || event.MessageType != message.TypePing {
				continue
			}
			decoded, err := message.Decode(message.TypePing, event.Payload)
			if err != nil {
				continue
			}
			pong, ok := decoded.(*message.Ping)
			if ok && pong.PType == message.PingTypePong && pong.Seq == seq {
				return
			}
		case <-timer.C:
			t.Fatalf("timed out waiting for pong %d (peer state: %s)", seq, p.peer.State())
		}
	}
}

func requireRelayedManifest(t *testing.T, p *manifestInteropPeer, want []byte) {
	t.Helper()
	got := receiveManifests(t, p)
	require.Len(t, got.List, 1)
	require.Equal(t, want, got.List[0].STObject)
	parsed, err := manifest.Deserialize(got.List[0].STObject)
	require.NoError(t, err)
	require.NoError(t, parsed.Verify())
}

func interopKeypair(role string, index uint32) (ed25519.PublicKey, ed25519.PrivateKey) {
	seed := sha256.Sum256([]byte(fmt.Sprintf("go-xrpl manifest interop/%s/%d", role, index)))
	privateKey := ed25519.NewKeyFromSeed(seed[:])
	publicKey := make([]byte, 1, ed25519.PublicKeySize+1)
	publicKey[0] = 0xed
	publicKey = append(publicKey, privateKey.Public().(ed25519.PublicKey)...)
	return ed25519.PublicKey(publicKey), privateKey
}

func buildInteropManifest(t *testing.T, seq, masterIndex, signingIndex uint32, revoked bool) []byte {
	t.Helper()
	masterPublic, masterPrivate := interopKeypair("master", masterIndex)
	fields := map[string]any{
		"PublicKey": hex.EncodeToString(masterPublic),
		"Sequence":  seq,
	}
	if !revoked {
		signingPublic, signingPrivate := interopKeypair("signing", signingIndex)
		fields["SigningPubKey"] = hex.EncodeToString(signingPublic)

		preimage := manifestSigningPreimage(t, fields)
		fields["Signature"] = hex.EncodeToString(ed25519.Sign(signingPrivate, preimage))
		fields["MasterSignature"] = hex.EncodeToString(ed25519.Sign(masterPrivate, preimage))
	} else {
		preimage := manifestSigningPreimage(t, fields)
		fields["MasterSignature"] = hex.EncodeToString(ed25519.Sign(masterPrivate, preimage))
	}
	encoded, err := binarycodec.Encode(fields)
	require.NoError(t, err)
	serialized, err := hex.DecodeString(encoded)
	require.NoError(t, err)
	return serialized
}

func manifestSigningPreimage(t *testing.T, fields map[string]any) []byte {
	t.Helper()
	encoded, err := binarycodec.Encode(fields)
	require.NoError(t, err)
	body, err := hex.DecodeString(encoded)
	require.NoError(t, err)
	return append(protocol.HashPrefixManifest().Bytes(), body...)
}

func TestManifest_Interop_RippledDocker(t *testing.T) {
	if os.Getenv("PEERTLS_DOCKER_INTEROP") == "" {
		t.Skip("PEERTLS_DOCKER_INTEROP not set")
	}

	node := startRippledInterop(t)
	lowerLimit := uint32(MaximumManifestsMessageSize(50, 50))
	defaultLimit := uint32(DefaultMaxManifestPayload)
	higherLimit := uint32(MaximumManifestsMessageSize(1000, 1000))

	source := newManifestInteropPeer(t, node.addr, defaultLimit)
	defaultWitness := newManifestInteropPeer(t, node.addr, defaultLimit)
	lowerWitness := newManifestInteropPeer(t, node.addr, lowerLimit)
	higherWitness := newManifestInteropPeer(t, node.addr, higherLimit)

	serialized := make([][]byte, 0, manifestInteropBatchCount)
	for i := range manifestInteropBatchCount {
		serialized = append(serialized, buildInteropManifest(t, 1, uint32(i), uint32(i)+manifestInteropBatchCount, false))
	}
	batch := encodeManifestFrame(t, serialized...)
	require.Greater(t, len(batch.payload), int(lowerLimit))
	require.LessOrEqual(t, len(batch.payload), int(defaultLimit))
	require.NoError(t, source.peer.SendManifestFrames([][]byte{batch.frame}))

	defaultBatch := receiveManifests(t, defaultWitness)
	higherBatch := receiveManifests(t, higherWitness)
	sourceBatch := receiveManifests(t, source)
	require.Len(t, defaultBatch.List, manifestInteropBatchCount)
	require.Len(t, higherBatch.List, manifestInteropBatchCount)
	require.Len(t, sourceBatch.List, manifestInteropBatchCount)
	require.Equal(t, batch.payload, mustEncodeManifestsPayload(t, defaultBatch))
	require.Equal(t, batch.payload, mustEncodeManifestsPayload(t, higherBatch))
	require.Equal(t, batch.payload, mustEncodeManifestsPayload(t, sourceBatch))

	masterIndex := uint32(0x15950001)
	first := buildInteropManifest(t, 1, masterIndex, masterIndex+1, false)
	firstFrame := encodeManifestFrame(t, first)
	require.NoError(t, source.peer.SendManifestFrames([][]byte{firstFrame.frame}))
	requireRelayedManifest(t, defaultWitness, first)
	requireRelayedManifest(t, lowerWitness, first)
	requireRelayedManifest(t, higherWitness, first)

	rotated := buildInteropManifest(t, 2, masterIndex, masterIndex+2, false)
	rotatedFrame := encodeManifestFrame(t, rotated)
	require.NoError(t, source.peer.SendManifestFrames([][]byte{rotatedFrame.frame}))
	requireRelayedManifest(t, defaultWitness, rotated)
	requireRelayedManifest(t, lowerWitness, rotated)
	requireRelayedManifest(t, higherWitness, rotated)

	revoked := buildInteropManifest(t, manifest.RevokedSequence, masterIndex, 0, true)
	revokedFrame := encodeManifestFrame(t, revoked)
	require.NoError(t, source.peer.SendManifestFrames([][]byte{revokedFrame.frame}))
	requireRelayedManifest(t, defaultWitness, revoked)
	requireRelayedManifest(t, lowerWitness, revoked)
	requireRelayedManifest(t, higherWitness, revoked)

	requirePong(t, lowerWitness, 1595)
	select {
	case got := <-lowerWitness.manifests:
		t.Fatalf("lower-limit witness dispatched oversized manifest: %#v", got)
	default:
	}

	defaultWitness.stop()
	reconnected := newManifestInteropPeer(t, node.addr, defaultLimit)
	snapshot := receiveManifests(t, reconnected)
	require.Len(t, snapshot.List, manifestInteropBatchCount+1)
	var revokedCount int
	for _, item := range snapshot.List {
		parsed, err := manifest.Deserialize(item.STObject)
		require.NoError(t, err)
		if parsed.Revoked() {
			revokedCount++
		}
	}
	require.Equal(t, 1, revokedCount)
}

func mustEncodeManifestsPayload(t *testing.T, manifests *message.Manifests) []byte {
	t.Helper()
	payload, err := message.Encode(manifests)
	require.NoError(t, err)
	return payload
}
